import type { DuoTransport } from "./transport";
import type { AgentName } from "./protocol";

type Role = "user" | "assistant";

/**
 * Maps opencode's event bus onto Duo's activity vocabulary.
 *
 * Duo's harness keeps a busy/idle clock per agent and only nudges when it
 * believes the agent has stalled, so the mapping has to be conservative in one
 * direction and generous in the other: reporting idle when work continues makes
 * Duo interrupt a working agent, and reporting an error that did not happen
 * makes Duo ask the peer to diagnose a stall that never happened.
 */
export function installLifecycle(
  transport: DuoTransport,
  agent: AgentName,
  isOurSession: (eventType: string, properties: any) => boolean,
) {
  // opencode's parts carry a messageID but no role, so the only way to tell
  // assistant output from an injected prompt is to learn the role from
  // `message.updated`, which always arrives before the message's parts. Guessing
  // from messageID instead meant the first text part seen — the prompt Duo had
  // just injected as a user message — became the reported assistant message, and
  // every real assistant part was then discarded as belonging to another message.
  const roles = new Map<string, Role>();

  let turnStarted = false;
  let lastStreamAt = 0;
  // Keyed by part, not by message: one assistant message routinely interleaves
  // several text parts with tool calls, and a streaming part re-reports its own
  // cumulative text under the same id. Keying by message collapsed all of them
  // into the last one.
  const blocks = new Map<string, string>();
  let blockOrder: string[] = [];

  // The provider call is the span between opencode creating an assistant message
  // and that message's first part. Without it the harness never learns an agent
  // is thinking, and it nudges a model that is merely slow.
  let providerOpen = false;

  const reset = () => {
    turnStarted = false;
    blocks.clear();
    blockOrder = [];
    lastStreamAt = 0;
    closeProvider();
  };

  const beginTurn = () => {
    if (turnStarted) return;
    turnStarted = true;
    blocks.clear();
    blockOrder = [];
    lastStreamAt = 0;
    transport.sendActivity("agent_start");
  };

  const closeProvider = () => {
    if (!providerOpen) return;
    providerOpen = false;
    transport.sendActivity("provider_end");
  };

  const assistantText = (): string =>
    blockOrder
      .map((id) => blocks.get(id) ?? "")
      .filter((text) => text.trim() !== "")
      .join("\n")
      .trim();

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
      case "message.updated": {
        const info = properties.info;
        if (!info?.id) break;
        if (info.role === "assistant" || info.role === "user") {
          roles.set(info.id, info.role);
        }
        // An assistant message is created when the model call starts.
        if (info.role === "assistant" && !providerOpen) {
          beginTurn();
          providerOpen = true;
          transport.sendActivity("provider_start");
        }
        break;
      }

      case "session.status": {
        if (properties.status?.type === "busy") beginTurn();
        break;
      }

      case "message.part.updated": {
        const part = properties.part;
        if (!part) break;

        if (part.type === "tool") {
          // Tool calls are the assistant acting, so they need no role lookup, but
          // they must not be attributed to a user message either.
          if (part.messageID && roles.get(part.messageID) === "user") break;
          if (providerOpen) closeProvider();
          const state = part.state ?? {};
          const name = part.tool ?? "tool";
          if (state.status === "pending" || state.status === "running") {
            beginTurn();
            // tool_end deliberately carries no detail: Duo keeps the arguments
            // from tool_start visible in the work preview.
            transport.sendActivity("tool_start", { tool: name, detail: compact(state.input) });
          } else if (state.status === "completed") {
            transport.sendActivity("tool_end", { tool: name, detail: "" });
          } else if (state.status === "error") {
            transport.sendActivity("tool_error", { tool: name, detail: compact(state.error) });
          }
          break;
        }

        if (part.type === "text" && part.text) {
          // The rule that fixes the echo: only an assistant message is output.
          // A prompt or harness nudge arrives as a user message and is never
          // reported back to the human as if the agent had said it.
          if (!part.messageID || roles.get(part.messageID) !== "assistant") break;
          if (providerOpen) closeProvider();
          beginTurn();
          const blockID = part.id ?? `${part.messageID}#${blockOrder.length}`;
          if (!blocks.has(blockID)) blockOrder.push(blockID);
          blocks.set(blockID, part.text);

          // Throttle: a streamed token fires per delta, and Duo only needs
          // enough to show that the agent is alive.
          const now = Date.now();
          if (now - lastStreamAt >= 1000) {
            lastStreamAt = now;
            transport.sendActivity("stream", { detail: tail(assistantText(), 1200) });
          }
        }
        break;
      }

      case "session.idle": {
        closeProvider();
        const text = assistantText();
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
        closeProvider();
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
