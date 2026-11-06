import { expect, test, mock } from "bun:test";
import { parseDuoMode } from "./mode";
import { buildDuoPrompt } from "./prompt";
import { installLoopGuard } from "./loop-guard";

test("LOOP assigns ownership without an advisor approval gate", () => {
  expect(parseDuoMode("loop")).toBe("loop");
  const a = buildDuoPrompt("Austin", "loop", "/repo", ".");
  const b = buildDuoPrompt("Tony", "loop", "/repo", ".");
  expect(a).toContain("task owner and sole user-facing reporter");
  expect(a).toContain("no agreement is required");
  expect(b).toContain("observing advisor");
  expect(b).toContain("Never call duo_finish");
  for (const p of [a, b]) {
    expect(p).toContain("duo_ask");
    expect(p).toContain("duo_reply");
    expect(p).toContain("Model latency is normal");
    expect(p).not.toContain("approve=proposal_id");
  }
});

test("Pi gates commands on authoritative consultation state, including bridge failure", async () => {
  const hooks: Record<string, any> = {};
  let mode = "loop" as any;
  let allowed = false;
  let offline = false;
  let calls = 0;
  const transport = { request: async (type: string) => {
    expect(type).toBe("loop_guard"); calls++;
    if (offline) throw new Error("offline");
    return { ok: allowed, text: "wait for question 7" };
  } } as any;
  installLoopGuard({ on: (name: string, fn: any) => hooks[name] = fn }, transport, () => mode);
  expect(await hooks.tool_call({ toolName: "bash" })).toEqual({ block: true, reason: "wait for question 7", terminate: true });
  expect(await hooks.tool_call({ toolName: "duo_events" })).toBeUndefined();
  allowed = true;
  expect(await hooks.tool_call({ toolName: "bash" })).toBeUndefined();
  offline = true;
  expect((await hooks.tool_call({ toolName: "write" })).block).toBe(true);
  mode = "fast";
  expect(await hooks.tool_call({ toolName: "write" })).toBeUndefined();
  expect(calls).toBe(3);
});

test("Pi ends successful wait and consultation turns and marks failures", async () => {
  mock.module("@earendil-works/pi-ai", () => ({ Type: { Object: () => ({}), Optional: () => ({}), String: () => ({}) } }));
  const { registerLoopTools } = await import("./tools/loop");
  const tools: Record<string, any> = {};
  const hooks: Record<string, any> = {};
  let response: any = { ok: true, text: "Wait intent registered. End this turn" };
  registerLoopTools({ on: (name: string, fn: any) => hooks[name] = fn, registerTool: (tool: any) => tools[tool.name] = tool }, { request: async () => response } as any);
  expect((await tools.duo_wait.execute("1", {})).terminate).toBe(true);
  response = { ok: true, text: "Not waiting: unread events exist." };
  expect((await tools.duo_wait.execute("2", {})).terminate).toBeUndefined();
  response = { ok: true, text: "queued" };
  expect((await tools.duo_ask.execute("3", { question: "why?" })).terminate).toBe(true);
  response = { ok: false, text: "not allowed" };
  const failed = await tools.duo_reply.execute("4", { questionId: "1", message: "x" });
  expect(await hooks.tool_result({ toolName: "duo_reply", details: failed.details })).toEqual({ isError: true });
});
