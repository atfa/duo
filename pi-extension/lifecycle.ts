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

export function installLifecycle(pi: any, transport: DuoTransport, agent: AgentName) {
  let lastAssistantText: string | null = null;
  let lastStreamActivityAt = 0;

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
    transport.sendActivity("provider_start");
  });

  pi.on("after_provider_response", async () => {
    transport.sendActivity("provider_end");
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

  pi.on("message_update", async (event: any) => {
    const now = Date.now();
    if (now - lastStreamActivityAt >= 1000) {
      lastStreamActivityAt = now;
      transport.sendActivity("stream", { detail: tailText(extractAssistantText(event?.message), 400) });
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
    transport.sendActivity("agent_settled");
  });

  pi.on("session_shutdown", async () => {
    transport.close();
  });
}
