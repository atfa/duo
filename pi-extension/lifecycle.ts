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

  pi.on("tool_execution_start", async () => {
    transport.sendActivity("tool_start");
  });

  pi.on("tool_execution_end", async () => {
    transport.sendActivity("tool_end");
  });

  pi.on("message_update", async () => {
    const now = Date.now();
    if (now - lastStreamActivityAt >= 1000) {
      lastStreamActivityAt = now;
      transport.sendActivity("stream");
    }
  });

  pi.on("message_end", async (event: any) => {
    const message = event?.message;
    if (message?.role === "assistant" && (message.stopReason === "error" || message.errorMessage)) {
      transport.send({
        version: 1,
        type: "agent_error",
        agent,
        text: message.errorMessage || `Request ${message.stopReason}`,
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
