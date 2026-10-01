import type { DuoTransport } from "./transport";
import type { AgentName } from "./protocol";

/**
 * Maps opencode's event bus onto Duo's activity vocabulary.
 *
 * Duo's harness keeps a busy/idle clock per agent and only nudges when it
 * believes the agent has stalled, so the mapping has to be conservative in one
 * direction and generous in the other: reporting idle when work continues makes
 * Duo interrupt, and reporting an error that did not happen makes Duo ask the
 * peer to diagnose a phantom stall.
 */
export function installLifecycle(
  transport: DuoTransport,
  agent: AgentName,
  isOurSession: (eventType: string, properties: any) => boolean,
) {
  let turnStarted = false;
  let lastStreamAt = 0;
  let assistantText = "";
  let assistantMessageID: string | null = null;

  const reset = () => {
    turnStarted = false;
    assistantText = "";
    assistantMessageID = null;
    lastStreamAt = 0;
  };

  const beginTurn = () => {
    if (turnStarted) return;
    turnStarted = true;
    assistantText = "";
    assistantMessageID = null;
    transport.sendActivity("agent_start");
  };

  const compact = (value: unknown, max = 160): string => {
    if (value === undefined || value === null) return "";
    let text: string;
    if (typeof value === "string") {
      text = value;
    } else if (typeof value === "object") {
      const record = value as Record<string, unknown>;
      const key = ["command", "path", "filePath", "file_path", "pattern", "query", "url", "prompt", "message"]
        .find((k) => typeof record[k] === "string" && record[k]);
      text = key ? String(record[key]) : JSON.stringify(record);
    } else {
      text = String(value);
    }
    text = text.replace(/\s+/g, " ").trim();
    return text.length > max ? text.slice(0, max - 1) + "…" : text;
  };

  const tail = (text: string, max: number): string => {
    const flat = text.replace(/\s+/g, " ").trim();
    return flat.length > max ? flat.slice(flat.length - max) : flat;
  };

  return async ({ event }: { event: any }) => {
    if (!isOurSession(event?.type, event?.properties)) return;
    const properties = event?.properties ?? {};

    switch (event.type) {
      case "session.status": {
        // A turn begins when opencode reports the session busy.
        if (properties.status?.type === "busy") beginTurn();
        break;
      }

      case "message.part.updated": {
        const part = properties.part;
        if (!part) break;
        // Only assistant output belongs to the turn we report.
        if (assistantMessageID && part.messageID && part.messageID !== assistantMessageID) break;

        if (part.type === "tool") {
          const state = part.state ?? {};
          const name = part.tool ?? "tool";
          const input = state.input ?? {};
          if (state.status === "pending" || state.status === "running") {
            beginTurn();
            // tool_end deliberately carries no detail: Duo keeps the arguments
            // from tool_start visible in the work preview.
            transport.sendActivity("tool_start", { tool: name, detail: compact(input) });
          } else if (state.status === "completed") {
            transport.sendActivity("tool_end", { tool: name, detail: "" });
          } else if (state.status === "error") {
            transport.sendActivity("tool_error", { tool: name, detail: compact(state.error) });
          }
          break;
        }

        if (part.type === "text" && part.text) {
          beginTurn();
          assistantMessageID = part.messageID ?? assistantMessageID;
          assistantText = part.text;
          // Throttle: a streamed token fires per delta, and Duo only needs
          // enough to show that the agent is alive.
          const now = Date.now();
          if (now - lastStreamAt >= 1000) {
            lastStreamAt = now;
            transport.sendActivity("stream", { detail: tail(assistantText, 1200) });
          }
        }
        break;
      }

      case "session.idle": {
        const text = assistantText.trim();
        if (text) {
          transport.send({
            version: 1,
            type: "assistant_message",
            agent,
            text,
            timestamp: Date.now(),
          });
        }
        reset();
        transport.sendActivity("agent_settled");
        break;
      }

      case "session.error": {
        const error = properties.error;
        const name = error?.name ?? "";
        const message = String(error?.data?.message ?? error?.message ?? "");
        // Compaction aborts the provider request on purpose. Reporting it would
        // make Duo count a failure and send the peer to diagnose a stall that
        // never happened.
        if (/abort/i.test(name) || /abort/i.test(message)) break;
        if (!message) break;
        transport.send({
          version: 1,
          type: "agent_error",
          agent,
          text: message,
          timestamp: Date.now(),
        });
        break;
      }
    }
  };
}
