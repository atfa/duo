import type { AgentName, DuoMessage } from "./protocol";
import { DuoTransport } from "./transport";

// The thinking levels Pi understands, in escalation order. Pi clamps a level to
// the current model's capabilities, so Duo only advances and re-displays what
// Pi reports back.
const THINKING_LEVELS = ["off", "minimal", "low", "medium", "high", "xhigh", "max"];

export function nextThinkingLevel(current: unknown): string {
  const index = typeof current === "string" ? THINKING_LEVELS.indexOf(current) : -1;
  if (index < 0) return THINKING_LEVELS[0];
  return THINKING_LEVELS[(index + 1) % THINKING_LEVELS.length];
}

/**
 * Bridges Duo's model picker to Pi's session control. It reports the active
 * model and thinking level whenever Pi changes them, and applies set_model /
 * cycle_thinking requests from Duo Core. Pi persists both in the session
 * transcript, so a restart or resume keeps the human's last choice.
 */
export function installModelBridge(pi: any, transport: DuoTransport, agent: AgentName) {
  let registry: any;

  const reportModel = (model: any) => {
    if (!model?.provider || !model?.id) return;
    transport.send({
      version: 1,
      type: "model_state",
      agent,
      provider: model.provider,
      model: model.id,
      ok: true,
      timestamp: Date.now(),
    });
  };

  const reportThinking = (level: unknown) => {
    if (typeof level !== "string" || !level) return;
    transport.send({
      version: 1,
      type: "thinking_state",
      agent,
      thinking: level,
      ok: true,
      timestamp: Date.now(),
    });
  };

  const fail = (type: "model_state" | "thinking_state", text: string) => {
    transport.send({ version: 1, type, agent, ok: false, text, timestamp: Date.now() });
  };

  const applyModel = async (message: DuoMessage) => {
    const models = registry ?? pi.getModelRegistry?.();
    const model = models?.find?.(message.provider, message.model);
    if (!model) {
      fail("model_state", `model not found: ${message.provider}/${message.model}`);
      return;
    }
    try {
      const ok = await pi.setModel(model);
      if (!ok) {
        fail("model_state", `no credentials for ${model.provider}/${model.id}`);
        return;
      }
      reportModel(model);
      reportThinking(pi.getThinkingLevel?.());
    } catch (error: any) {
      fail("model_state", error?.message ?? String(error));
    }
  };

  const cycleThinking = () => {
    try {
      const before = pi.getThinkingLevel?.();
      // Pi clamps a level to what the current model supports, and only reports a
      // change when the clamped level differs. Stepping through the escalation
      // order can therefore land back on the current level (for example xhigh on
      // a model that stops at high), so keep stepping until Pi actually moves or
      // every level has been tried.
      let changed = false;
      let level = before;
      for (let i = 0; i < THINKING_LEVELS.length; i++) {
        level = nextThinkingLevel(level);
        pi.setThinkingLevel(level);
        if (pi.getThinkingLevel?.() !== before) {
          changed = true;
          break;
        }
      }
      // A model with a single available level never changes: report the level
      // anyway so the picker still shows what Pi settled on.
      if (!changed) reportThinking(pi.getThinkingLevel?.());
    } catch (error: any) {
      fail("thinking_state", error?.message ?? String(error));
    }
  };

  transport.addHandler((message: DuoMessage) => {
    if (message.to && message.to.toLowerCase() !== agent.toLowerCase()) return;
    if (message.type === "set_model") {
      void applyModel(message);
    } else if (message.type === "cycle_thinking") {
      cycleThinking();
    }
  });

  pi.on("session_start", async (_event: any, ctx: any) => {
    registry = ctx?.modelRegistry ?? registry;
    reportModel(ctx?.getModel?.() ?? pi.getModel?.());
    reportThinking(pi.getThinkingLevel?.());
  });

  pi.on("model_select", async (event: any) => {
    reportModel(event?.model);
    reportThinking(pi.getThinkingLevel?.());
  });

  pi.on("thinking_level_select", async (event: any) => {
    reportThinking(event?.level);
  });
}
