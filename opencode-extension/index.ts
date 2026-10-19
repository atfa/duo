import { parseDuoMode } from "./mode";
import type { AgentName } from "./protocol";
import { DuoTransport } from "./transport";
import { SessionTracker } from "./session";
import { installLifecycle } from "./lifecycle";
import { installDuoPrompt } from "./prompt";
import { buildDuoTools } from "./tools";

/**
 * Duo bridge for the opencode CLI.
 *
 * opencode 2.0 plugins export `{ id, setup }`; the setup context exposes event
 * subscription, session hooks and tool registration, so this stays a real
 * bridge: the agent gets Duo's coordination tools, Duo sees live tool and
 * stream activity, and peer messages, harness nudges and human tasks are
 * injected into the running session instead of being typed at a PTY.
 *
 * The wire protocol is identical to the Pi bridge's, so `protocol.ts` and
 * `transport.ts` are shared verbatim with it.
 */
export default {
  id: "duo",
  setup: async (context: any) => {
    // Outside Duo the plugin must be completely inert, so a normal opencode
    // session behaves exactly as it would without Duo installed.
    if (process.env.DUO_ACTIVE !== "1") return;

    const agent = process.env.DUO_AGENT;
    const host = process.env.DUO_HOST;
    const port = Number(process.env.DUO_PORT);
    const sessionId = process.env.DUO_SESSION;
    const token = process.env.DUO_TOKEN;

    if (!host || !Number.isInteger(port) || port <= 0 || !sessionId || !token) {
      throw new Error(
        "DUO_HOST, DUO_PORT, DUO_SESSION, and DUO_TOKEN are required when DUO_ACTIVE=1",
      );
    }
    const AGENT = agent as AgentName;
    if (AGENT !== "Austin" && AGENT !== "Tony") {
      throw new Error(`DUO_AGENT must be Austin or Tony, got: ${AGENT}`);
    }

    const transport = new DuoTransport(AGENT, host, port, sessionId, token);
    const mode = parseDuoMode(process.env.DUO_MODE);
    const tracker = new SessionTracker(process.env.DUO_OPENCODE_SESSION_FILE ?? "");

    // The context window is not carried by any event: it lives in the model
    // catalog, so it costs one request the first time a model is seen.
    const lifecycle = installLifecycle(
      transport,
      AGENT,
      (type, data) => tracker.observe(type, data),
      async (providerID, modelID) => {
        try {
          const { data } = await context.model.list();
          const record = (data ?? []).find(
            (m: any) => m?.providerID === providerID && m?.modelID === modelID,
          );
          const limit = record?.limit?.context;
          if (typeof limit === "number" && limit > 0) return limit;
        } catch (error) {
          console.error("[duo] failed to resolve context window:", error);
        }
        return undefined;
      },
    );

    // opencode's TUI only creates its session on the first thing the human or the
    // agent sends, so before that there is no session to deliver to. Queueing keeps
    // anything that arrives early from being lost.
    const queued: string[] = [];

    const inject = async (text: string, type: string) => {
      const target = tracker.sessionID;
      if (!target) {
        queued.push(text);
        return;
      }
      try {
        // Returns an inbox item immediately, so a peer message does not block
        // Duo's coordinator and can arrive while the agent is mid-turn.
        await context.session.prompt({ sessionID: target, text, delivery: "steer" });
      } catch (error) {
        console.error(`[duo] failed to inject ${type}:`, error);
      }
    };

    // Inbound Duo messages become prompts on the live session.
    const deliver = async (message: any) => {
      if (
        !["steer", "duo_notice", "harness_prompt", "human_prompt", "resume_prompt"].includes(
          message.type,
        )
      ) {
        return;
      }
      if (message.to && message.to.toLowerCase() !== AGENT.toLowerCase()) return;
      if (!message.text) return;

      await inject(
        message.type === "steer"
          ? `[Peer message from ${message.from ?? "peer"}]\n\n${message.text}`
          : message.text,
        message.type,
      );
    };

    transport.setHandler(deliver);

    // Setup-time registrations.
    installDuoPrompt(context, AGENT, mode);
    await context.tool.transform((editor: any) => {
      for (const tool of buildDuoTools(transport, AGENT, mode)) {
        editor.add(tool);
      }
    });

    // Connect only once the TUI's own session exists, and only then because the
    // bridge can actually deliver to it.
    //
    // opencode mints that session on the agent's first input, and a session the
    // plugin creates for itself is never rendered in the TUI the human is watching,
    // so driving one would work while showing the human an empty pane. Connecting
    // early is worse than useless: Duo routes prompts to a connected bridge, so an
    // unconnected bridge is what lets the first task reach the TUI through Duo's
    // PTY fallback and start the session in the first place.
    //
    // Once connected, anything that arrived in the meantime is flushed in order.
    const controller = new AbortController();
    let stopping = false;
    let connectTimer: ReturnType<typeof setTimeout> | null = null;

    // The one event pump: it feeds the activity mapping, schedules the connect
    // once a session id is known, and flushes everything queued before then.
    void (async () => {
      for await (const event of context.event.subscribe({ signal: controller.signal })) {
        try {
          await lifecycle(event);
        } catch (error) {
          console.error("[duo] event handling failed:", error);
        }
        if (tracker.sessionID && !connectTimer) {
          connectTimer = setTimeout(() => {
            connectTimer = null;
            transport.connect();
          }, 0);
        }
        if (!tracker.sessionID || queued.length === 0) continue;
        const pending = queued.splice(0, queued.length);
        for (const text of pending) await inject(text, "queued");
      }
    })().catch((error) => {
      if (!stopping) console.error("[duo] event pump stopped:", error);
    });

    return () => {
      stopping = true;
      if (connectTimer) clearTimeout(connectTimer);
      controller.abort();
      transport.close();
    };
  },
};
