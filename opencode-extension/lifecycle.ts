import type { DuoTransport } from "./transport";
import type { AgentName } from "./protocol";

/**
 * opencode 2.0 reports real token usage per model call on `session.step.ended`
 * (`tokens {input, output, reasoning, cache{read, write}}`). Only the model
 * catalog knows the context window, so the caller supplies a resolver keyed by
 * provider/model id.
 */
export type ContextWindowResolver = (
  providerID: string,
  modelID: string,
) => Promise<number | undefined>;

/**
 * Context tokens carried by a request: everything the model must re-read on the
 * next turn. `output` is excluded because it is not yet part of the context.
 */
function contextTokens(tokens: any): number | undefined {
  if (!tokens || typeof tokens !== "object") return undefined;
  const input = Number(tokens.input ?? 0);
  const reasoning = Number(tokens.reasoning ?? 0);
  const cacheRead = Number(tokens.cache?.read ?? 0);
  if (![input, reasoning, cacheRead].every(Number.isFinite)) return undefined;
  const total = input + reasoning + cacheRead;
  return total > 0 ? total : undefined;
}

/**
 * Maps opencode's event bus onto Duo's activity vocabulary.
 *
 * Duo's harness keeps a busy/idle clock per agent and only nudges when it
 * believes the agent has stalled, so the mapping has to be conservative in one
 * direction and generous in the other: reporting idle when work continues makes
 * Duo interrupt a working agent, and reporting an error that did not happen
 * makes Duo ask the peer to diagnose a stall that never happened.
 *
 * In 2.0 the text and tool events (`session.text.*`, `session.tool.*`) are
 * assistant-anchored: they only ever fire for assistant messages, so an
 * injected prompt can never be echoed back as the agent's own output and the
 * per-message role lookup the old API required is gone.
 */
export function installLifecycle(
  transport: DuoTransport,
  agent: AgentName,
  isOurSession: (eventType: string, data: any) => boolean,
  resolveContextWindow?: ContextWindowResolver,
) {
  let turnStarted = false;
  let lastStreamAt = 0;
  // One assistant message routinely interleaves several text blocks with tool
  // calls, streamed as `session.text.{started,delta,ended}` frames keyed by
  // ordinal within the message, so each block is tracked exactly.
  const blocks = new Map<string, string>();
  let blockOrder: string[] = [];

  // The provider call is the span from `session.step.started` to
  // `session.step.ended/failed`. Without it the harness never learns an agent
  // is thinking, and it nudges a model that is merely slow.
  //
  // The span deliberately covers streamed output: Duo only renders the output
  // rate while the provider is active, so closing it on the first token
  // discarded the speed it was supposed to show.
  let providerOpen = false;

  // Latest real usage, kept so the numbers survive activity messages that
  // carry no usage of their own (tool_start, stream throttles).
  let usage: { contextTokens?: number; contextWindow?: number } = {};
  let speedSamples: Array<{ at: number; chars: number }> = [];
  const windowCache = new Map<string, number>();
  // The model is named on the step, so the ids seen at the previous step are
  // reused for the window lookup at step end.
  let model: { providerID?: string; modelID?: string } = {};

  // Tool events other than `session.tool.input.started` carry only a call id,
  // never the tool name, so names are learned on the first event of each call.
  const toolNames = new Map<string, string>();

  const reset = () => {
    turnStarted = false;
    blocks.clear();
    blockOrder = [];
    lastStreamAt = 0;
    speedSamples = [];
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
    speedSamples = [];
    transport.sendActivity("provider_end", { ...usage });
  };

  /**
   * Approximate output rate from recently streamed characters (~4 chars/token).
   * opencode reports exact output tokens, but only once per step, so a rate
   * computed from them would lag the text the user is watching by a whole step.
   */
  const tokensPerSecond = (now: number): number => {
    speedSamples = speedSamples.filter((sample) => now - sample.at <= 3000);
    if (speedSamples.length < 2) return 0;
    const elapsed = (now - speedSamples[0].at) / 1000;
    if (elapsed < 0.5) return 0;
    const chars = speedSamples.reduce(
      (total, sample) => total + sample.chars,
      0,
    );
    return chars / 4 / elapsed;
  };

  /**
   * Resolve and remember the model's context window. Cached per model because
   * the catalog lookup is an async round trip on a path that runs per step.
   */
  const windowFor = async (providerID?: string, modelID?: string) => {
    if (!resolveContextWindow || !providerID || !modelID) return;
    if (usage.contextWindow) return;
    const key = `${providerID}/${modelID}`;
    const cached = windowCache.get(key);
    if (cached) {
      usage.contextWindow = cached;
      return;
    }
    const resolved = await resolveContextWindow(providerID, modelID);
    if (resolved && resolved > 0) {
      windowCache.set(key, resolved);
      usage.contextWindow = Math.round(resolved);
    }
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
      const key = [
        "command",
        "path",
        "filePath",
        "file_path",
        "pattern",
        "query",
        "url",
        "prompt",
        "message",
      ].find((k) => typeof record[k] === "string" && record[k]);
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

  const textKey = (data: any): string =>
    `${data?.assistantMessageID ?? ""}#${data?.ordinal ?? 0}`;

  const maybeStream = (chars: number) => {
    if (chars > 0) speedSamples.push({ at: Date.now(), chars });
    const now = Date.now();
    if (now - lastStreamAt < 1000) return;
    lastStreamAt = now;
    const rate = tokensPerSecond(now);
    transport.sendActivity("stream", {
      detail: tail(assistantText(), 1200),
      ...usage,
      ...(rate > 0 ? { tokensPerSecond: rate } : {}),
    });
  };

  const ingestText = (data: any, kind: "started" | "delta" | "ended") => {
    const key = textKey(data);
    beginTurn();
    const previous = blocks.get(key) ?? "";
    if (!blocks.has(key)) blockOrder.push(key);
    if (kind === "started") {
      blocks.set(key, "");
      return;
    }
    // `delta` frames are live fragments; `ended` carries the full block value
    // and wins over whatever was streamed before it.
    const text =
      kind === "ended"
        ? String(data?.text ?? "")
        : previous + String(data?.delta ?? "");
    blocks.set(key, text);
    maybeStream(Math.max(0, text.length - previous.length));
  };

  const nameOf = (callID: any): string =>
    (typeof callID === "string" && toolNames.get(callID)) || "tool";

  /**
   * The end of an execution. opencode v1 reported a `session.idle` after every
   * run (including failed and aborted ones); v2 gives terminal events instead,
   * so each of them settles the turn the same way or the harness would count a
   * finished agent as busy forever.
   */
  const settle = () => {
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
  };

  return async (event: any) => {
    if (!isOurSession(event?.type, event?.data)) return;
    const data = event?.data;

    switch (event.type) {
      case "session.execution.started": {
        beginTurn();
        break;
      }

      case "session.step.started": {
        beginTurn();
        model = {
          providerID: data?.model?.providerID,
          modelID: data?.model?.id,
        };
        // Fire and forget: a pending window lookup must not delay the event
        // pump, and the next usage update carries whatever it resolved.
        void windowFor(model.providerID, model.modelID);
        if (!providerOpen) {
          providerOpen = true;
          speedSamples = [];
          transport.sendActivity("provider_start");
        }
        break;
      }

      case "session.step.ended": {
        // The end of one model call. Tokens are reported per step, so
        // input + reasoning + cache.read approximates the current context size.
        const tokens = contextTokens(data?.tokens);
        if (tokens !== undefined) usage.contextTokens = tokens;
        void windowFor(model.providerID, model.modelID);
        closeProvider();
        break;
      }

      case "session.step.failed": {
        // Close the span; the failure is reported once at
        // session.execution.failed (and filtered there when it is an abort).
        closeProvider();
        break;
      }

      // `session.usage.updated` carries the session's cumulative token totals,
      // not the context carried by one request. Treating it as context usage
      // makes a long session show millions of tokens against a 200k window.

      case "session.text.started":
      case "session.text.delta":
      case "session.text.ended": {
        const kind = event.type.split(".").pop() as
          | "started"
          | "delta"
          | "ended";
        ingestText(data, kind);
        break;
      }

      case "session.tool.input.started": {
        if (data?.id && data?.name) toolNames.set(data.id, data.name);
        break;
      }

      case "session.tool.called": {
        beginTurn();
        // Tool calls are the assistant acting.
        if (providerOpen) closeProvider();
        // tool_end deliberately carries no detail: Duo keeps the arguments from
        // tool_start visible in the work preview.
        transport.sendActivity("tool_start", {
          tool: nameOf(data?.id),
          detail: compact(data?.input),
        });
        break;
      }

      case "session.tool.success": {
        transport.sendActivity("tool_end", {
          tool: nameOf(data?.id),
          detail: "",
        });
        break;
      }

      case "session.tool.failed": {
        const error = data?.error ?? {};
        transport.sendActivity("tool_error", {
          tool: nameOf(data?.id),
          detail: compact(error?.message ?? error),
        });
        break;
      }

      case "session.execution.succeeded": {
        settle();
        break;
      }

      case "session.execution.failed": {
        closeProvider();
        const error = data?.error ?? {};
        const kind = String(error?.type ?? error?.name ?? "");
        const message = String(error?.message ?? "");
        // An abort ends the provider request on purpose. Reporting it would
        // make Duo count a failure and send the peer to diagnose a stall that
        // never happened.
        if (!/abort/i.test(kind) && !/abort/i.test(message) && message) {
          transport.send({
            version: 1,
            type: "agent_error",
            agent,
            text: message,
            timestamp: Date.now(),
          });
        }
        settle();
        break;
      }

      case "session.execution.interrupted": {
        // A user interrupt is not a failure; on non-shutdown interrupts the
        // agent has stopped either way, so close everything quietly.
        settle();
        break;
      }
    }
  };
}
