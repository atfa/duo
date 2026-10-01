import type { Plugin } from "@opencode-ai/plugin";
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
 * opencode has a first-class plugin API, so unlike the agy driver this is a
 * real bridge: the agent gets Duo's coordination tools, Duo sees live tool and
 * stream activity, and peer messages, harness nudges and human tasks are
 * injected straight into the running session instead of being typed at a PTY.
 *
 * The wire protocol is identical to the Pi bridge's, so `protocol.ts` and
 * `transport.ts` are shared verbatim with it.
 */
export default (async ({ client }: any) => {
  // Outside Duo the plugin must be completely inert, so a normal opencode
  // session behaves exactly as it would without Duo installed.
  if (process.env.DUO_ACTIVE !== "1") return {};

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

  const lifecycle = installLifecycle(transport, AGENT, (type, properties) =>
    tracker.observe(type, properties),
  );

  // Inbound Duo messages become prompts on the live session. promptAsync
  // returns immediately, so a peer message does not block Duo's coordinator and
  // can arrive while the agent is mid-turn.
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

    const target = tracker.sessionID;
    if (!target) {
      console.error("[duo] no opencode session yet; dropping " + message.type);
      return;
    }

    const text =
      message.type === "steer"
        ? `[Peer message from ${message.from ?? "peer"}]\n\n${message.text}`
        : message.text;

    try {
      await client.session.promptAsync({
        path: { id: target },
        body: { parts: [{ type: "text", text }] },
      });
    } catch (error) {
      console.error(`[duo] failed to inject ${message.type}:`, error);
    }
  };

  const hooks: any = {};
  installDuoPrompt(hooks, AGENT, mode);
  hooks.event = lifecycle;
  hooks.tool = buildDuoTools(transport, AGENT, mode);
  transport.setHandler(deliver);

  // opencode instantiates the plugin before the TUI is interactive, and the
  // session id only exists once opencode has created or attached one. Connecting
  // on the first event that carries the id is the earliest safe moment; the
  // transport reconnects on its own if the first attempt lands too early.
  let connectTimer: ReturnType<typeof setTimeout> | null = null;
  const observe = hooks.event;
  hooks.event = async (input: any) => {
    await observe(input);
    if (tracker.sessionID && !connectTimer) {
      connectTimer = setTimeout(() => {
        connectTimer = null;
        transport.connect();
      }, 0);
    }
  };

  hooks.dispose = async () => {
    if (connectTimer) clearTimeout(connectTimer);
    transport.close();
  };

  return hooks;
}) satisfies Plugin;
