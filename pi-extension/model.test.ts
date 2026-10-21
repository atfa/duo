import { expect, test } from "bun:test";
import { installModelBridge, nextThinkingLevel } from "./model";
import type { DuoMessage } from "./protocol";

test("nextThinkingLevel escalates and wraps", () => {
  expect(nextThinkingLevel("off")).toBe("minimal");
  expect(nextThinkingLevel("high")).toBe("xhigh");
  expect(nextThinkingLevel("max")).toBe("off");
  expect(nextThinkingLevel(undefined)).toBe("off");
});

function bridgeFixture(options: { thinking?: string; available?: string[]; resetThinkingOnModel?: boolean } = {}) {
  const handlers = new Map<string, (event: any, ctx: any) => Promise<void>>();
  const inbound: Array<(message: DuoMessage) => void> = [];
  const sent: any[] = [];
  const model = { provider: "cline", id: "anthropic/claude-opus-4.8" };
  const state = { thinking: options.thinking ?? "low" };
  const available = options.available;
  const pi = {
    on: (name: string, handler: (event: any, ctx: any) => Promise<void>) => {
      handlers.set(name, handler);
    },
    getModelRegistry: () => ({ find: (p: string, m: string) => (p === model.provider && m === model.id ? model : undefined) }),
    getModel: () => model,
    getThinkingLevel: () => state.thinking,
    setModel: async (selected: any) => {
      if (selected !== model) return false;
      if (options.resetThinkingOnModel) state.thinking = "medium";
      return true;
    },
    // Mirrors Pi: a level the current model does not support clamps back to the
    // current one, and only a real change emits thinking_level_select.
    setThinkingLevel: (level: string) => {
      if (available && !available.includes(level)) return;
      if (level === state.thinking) return;
      const previousLevel = state.thinking;
      state.thinking = level;
      void handlers.get("thinking_level_select")?.({ level, previousLevel }, {});
    },
  };
  const transport = {
    addHandler: (handler: (message: DuoMessage) => void) => inbound.push(handler),
    send: (message: any) => sent.push(message),
  };
  installModelBridge(pi, transport as any, "Austin");
  return { handlers, state, inbound: (message: DuoMessage) => inbound.forEach((h) => h(message)), sent };
}

test("set_model resolves the registry entry and reports the new state", async () => {
  const { inbound, sent } = bridgeFixture();
  inbound({ version: 1, type: "set_model", to: "Austin", provider: "cline", model: "anthropic/claude-opus-4.8" });
  await new Promise((resolve) => setTimeout(resolve, 0));

  expect(sent[0]).toMatchObject({
    type: "model_state",
    agent: "Austin",
    provider: "cline",
    model: "anthropic/claude-opus-4.8",
    ok: true,
  });
});

test("selecting a model preserves the chosen thinking level", async () => {
  const { state, inbound, sent } = bridgeFixture({
    thinking: "xhigh",
    resetThinkingOnModel: true,
    available: ["off", "low", "medium", "high", "xhigh"],
  });
  inbound({ version: 1, type: "set_model", to: "Austin", provider: "cline", model: "anthropic/claude-opus-4.8" });
  await new Promise((resolve) => setTimeout(resolve, 0));

  expect(state.thinking).toBe("xhigh");
  const thinking = sent.filter((message) => message.type === "thinking_state");
  expect(thinking.at(-1)).toMatchObject({ thinking: "xhigh", ok: true });
});

test("an unknown model is reported instead of silently ignored", async () => {
  const { inbound, sent } = bridgeFixture();
  inbound({ version: 1, type: "set_model", to: "Austin", provider: "cline", model: "nope" });
  await new Promise((resolve) => setTimeout(resolve, 0));

  expect(sent[0]).toMatchObject({ type: "model_state", ok: false });
  expect(String(sent[0].text)).toContain("model not found");
});

test("cycle_thinking advances to the next level the model supports", () => {
  const { state, inbound, sent } = bridgeFixture({ thinking: "low", available: ["off", "low", "medium", "high"] });
  inbound({ version: 1, type: "cycle_thinking", to: "Tony" });
  expect(state.thinking).toBe("low"); // never touches another agent
  inbound({ version: 1, type: "cycle_thinking", to: "Austin" });
  expect(state.thinking).toBe("medium");
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({ type: "thinking_state", agent: "Austin", thinking: "medium", ok: true });
});

test("cycle_thinking reports the level Pi actually settled on", () => {
  // xhigh is unsupported and clamps back to high; the bridge must report high,
  // not claim xhigh and disagree with the agent.
  const { state, inbound, sent } = bridgeFixture({ thinking: "high", available: ["off", "low", "medium", "high"] });
  inbound({ version: 1, type: "cycle_thinking", to: "Austin" });
  expect(state.thinking).toBe("high");
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({ type: "thinking_state", thinking: "high", ok: true });
});

test("cycle_thinking still reports when a model has only one level", () => {
  const { state, inbound, sent } = bridgeFixture({ thinking: "off", available: ["off"] });
  inbound({ version: 1, type: "cycle_thinking", to: "Austin" });
  expect(state.thinking).toBe("off");
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({ type: "thinking_state", thinking: "off", ok: true });
});
