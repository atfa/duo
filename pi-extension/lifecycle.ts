import type { AgentName, DuoMessage } from "./protocol";
import { DuoTransport } from "./transport";

function extractAssistantText(message: any): string | null {
  if (!message || message.role !== "assistant" || !Array.isArray(message.content)) {
    return null;
  }

  const text = message.content
    .filter((block: any) => block.type === "text")
    .map((block: any) => block.text)
    .join("\n")
    .trim();

  return text || null;
}

function isCompactionAbort(error: unknown): boolean {
  return error === "This operation was aborted" || error === "Error: This operation was aborted";
}

/**
 * Compact a tool argument or result into one preview line. Tool payloads can be
 * arbitrarily large (a whole file, a full diff), and the Duo preview only needs
 * enough to recognise the action.
 */
function compactDetail(value: unknown, max = 160): string {
  let text: string;
  if (typeof value === "string") {
    text = value;
  } else if (value && typeof value === "object") {
    const record = value as Record<string, unknown>;
    const key = ["command", "path", "file_path", "pattern", "query", "url", "prompt", "task"].find(
      (candidate) => typeof record[candidate] === "string" && record[candidate],
    );
    text = key ? String(record[key]) : JSON.stringify(record);
  } else {
    text = value === undefined || value === null ? "" : String(value);
  }
  text = text.replace(/\s+/g, " ").trim();
  return text.length > max ? text.slice(0, max - 1) + "…" : text;
}

/** Keep the most recent part of streamed text, which is what a preview shows. */
function tailText(text: string | null, max: number): string {
  if (!text) return "";
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length > max ? flat.slice(flat.length - max) : flat;
}

function contextUsage(ctx: any): { contextTokens?: number; contextWindow?: number } {
  try {
    if (typeof ctx?.getContextUsage !== "function") return {};
    const usage = ctx.getContextUsage();
    if (!Number.isFinite(usage?.tokens) || !Number.isFinite(usage?.contextWindow) || usage.contextWindow <= 0) return {};
    return { contextTokens: Math.max(0, Math.round(usage.tokens)), contextWindow: Math.round(usage.contextWindow) };
  } catch {
    return {};
  }
}

function outputDeltaChars(event: any): number {
  const update = event?.assistantMessageEvent;
  if (!update || !["text_delta", "thinking_delta", "toolcall_delta"].includes(update.type)) return 0;
  return typeof update.delta === "string" ? update.delta.length : 0;
}

export function installLifecycle(pi: any, transport: DuoTransport, agent: AgentName) {
  let lastAssistantText: string | null = null;
  let lastStreamActivityAt = 0;
  let speedSamples: Array<{ at: number; chars: number }> = [];

  const tokensPerSecond = (now: number): number => {
    speedSamples = speedSamples.filter((sample) => now - sample.at <= 3000);
    if (speedSamples.length < 2) return 0;
    const elapsed = (now - speedSamples[0].at) / 1000;
    if (elapsed < 0.5) return 0;
    const chars = speedSamples.reduce((total, sample) => total + sample.chars, 0);
    return chars / 4 / elapsed;
  };

  transport.setHandler((message: DuoMessage) => {
    if (!["steer", "duo_notice", "harness_prompt", "human_prompt", "resume_prompt"].includes(message.type)) return;
    if (message.to && message.to.toLowerCase() !== agent.toLowerCase()) return;
    if (!message.text) return;

    const text = message.type === "steer"
      ? `[Peer message from ${message.from ?? "peer"}]\n\n${message.text}`
      : message.text;

    Promise.resolve(
      pi.sendUserMessage(text, { deliverAs: "steer" }),
    ).catch((error: any) => {
      console.error(
        `[duo] failed to inject ${message.type}: ${error?.message ?? String(error)}`,
      );
    });
  });

  pi.on("agent_start", async () => {
    lastAssistantText = null;
    transport.sendActivity("agent_start");
  });

  pi.on("before_provider_request", async () => {
    speedSamples = [];
    transport.sendActivity("provider_start");
  });

  pi.on("after_provider_response", async (_event: any, ctx: any) => {
    speedSamples = [];
    transport.sendActivity("provider_end", contextUsage(ctx));
  });

  pi.on("after_provider_response", async (event: any) => {
    if (typeof event?.status !== "number" || event.status < 400) return;
    transport.send({
      version: 1,
      type: "agent_error",
      agent,
      text: `Provider request failed (${event.status})`,
      timestamp: Date.now(),
    });
  });

  pi.on("tool_execution_start", async (event: any) => {
    transport.sendActivity("tool_start", { tool: event?.toolName, detail: compactDetail(event?.args) });
  });

  pi.on("tool_execution_end", async (event: any) => {
    // A successful result is not worth a line; a failure is the one thing a
    // waiting human needs to see, so it replaces the arguments.
    transport.sendActivity(event?.isError ? "tool_error" : "tool_end", {
      tool: event?.toolName,
      detail: event?.isError ? compactDetail(event?.result) : "",
    });
  });

  pi.on("message_update", async (event: any, ctx: any) => {
    const now = Date.now();
    const chars = outputDeltaChars(event);
    if (chars > 0) speedSamples.push({ at: now, chars });
    if (now - lastStreamActivityAt >= 1000) {
      lastStreamActivityAt = now;
      // Pi provides an estimated context count; output rate is estimated from
      // recent streamed characters (~4 chars/token) because no live rate API exists.
      const rate = tokensPerSecond(now);
      transport.sendActivity("stream", {
        detail: tailText(extractAssistantText(event?.message), 1200),
        ...contextUsage(ctx),
        ...(rate > 0 ? { tokensPerSecond: rate } : {}),
      });
    }
  });

  pi.on("message_end", async (event: any) => {
    const message = event?.message;
    const errorMessage = message?.errorMessage || `Request ${message?.stopReason}`;
    if (
      message?.role === "assistant" &&
      (message.stopReason === "error" || message.errorMessage) &&
      !isCompactionAbort(errorMessage)
    ) {
      transport.send({
        version: 1,
        type: "agent_error",
        agent,
        text: errorMessage,
        timestamp: Date.now(),
      });
    }
    const text = extractAssistantText(message);
    if (text) lastAssistantText = text;
  });

  pi.on("agent_settled", async () => {
    if (lastAssistantText) {
      transport.send({
        version: 1,
        type: "assistant_message",
        agent,
        text: lastAssistantText,
        timestamp: Date.now(),
      });
      lastAssistantText = null;
    }
    speedSamples = [];
    transport.sendActivity("agent_settled");
  });

  pi.on("session_shutdown", async () => {
    transport.close();
  });
}
