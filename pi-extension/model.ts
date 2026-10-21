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
  let lastThinking: string | undefined;

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
    if (typeof level !== "string" || !level || level === lastThinking) return;
    lastThinking = level;
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
      // Selecting a model can reset Pi's effort to the model's default. Keep the
      // level the operator chose in this dialog, then report whatever Pi actually
      // accepts for the new model.
      const thinking = pi.getThinkingLevel?.();
      const ok = await pi.setModel(model);
      if (!ok) {
        fail("model_state", `no credentials for ${model.provider}/${model.id}`);
        return;
      }
      if (thinking) pi.setThinkingLevel(thinking);
      reportModel(model);
      reportThinking(pi.getThinkingLevel?.());
    } catch (error: any) {
      fail("model_state", error?.message ?? String(error));
    }
  };

  const cycleThinking = () => {
    try {
      const before = pi.getThinkingLevel?.();
      const next = nextThinkingLevel(before);
      pi.setThinkingLevel(next);
      // Report what Pi actually settled on. Some models clamp an unsupported
      // level (for example xhigh -> medium); claiming the requested level would
      // make the dashboard and the agent disagree.
      reportThinking(pi.getThinkingLevel?.() ?? before);
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
