import { expect, test } from "bun:test";
import { installLifecycle } from "./lifecycle";
import { isToolEnabledForMode, parseDuoLanguage, parseDuoMode } from "./mode";
import { buildDuoPrompt, installDuoPrompt } from "./prompt";

test("compaction abort does not report an agent error", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sent: any[] = [];
  installLifecycle(
    { on: (name: string, handler: (event: any) => Promise<void>) => handlers.set(name, handler) },
    { setHandler() {}, sendActivity() {}, send: (message: any) => sent.push(message) } as any,
    "Austin",
  );

  const messageEnd = handlers.get("message_end")!;
  await messageEnd({ message: { role: "assistant", stopReason: "error", errorMessage: "This operation was aborted" } });
  expect(sent).toEqual([]);

  await messageEnd({ message: { role: "assistant", stopReason: "error", errorMessage: "request failed" } });
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({ type: "agent_error", agent: "Austin", text: "request failed" });
});

test("an unknown DUO_MODE falls back to the conservative goal workflow", () => {
  expect(parseDuoMode(undefined)).toBe("goal");
  expect(parseDuoMode("")).toBe("goal");
  expect(parseDuoMode("goal")).toBe("goal");
  expect(parseDuoMode("turbo")).toBe("goal");
  expect(parseDuoMode("fast")).toBe("fast");
  expect(parseDuoMode(" FAST ")).toBe("fast");
  expect(parseDuoMode("step")).toBe("step");
  expect(parseDuoMode(" STEP ")).toBe("step");
});

test("mode gates the shared-plan and verification tools", () => {
  expect(isToolEnabledForMode("plan", "goal")).toBe(true);
  expect(isToolEnabledForMode("plan", "fast")).toBe(false);
  expect(isToolEnabledForMode("verification", "fast")).toBe(true);
  expect(isToolEnabledForMode("verification", "goal")).toBe(false);
  expect(isToolEnabledForMode("escalate", "fast")).toBe(true);
  expect(isToolEnabledForMode("escalate", "goal")).toBe(false);
});

// STEP keeps the Goal phase chain, so it needs the prose plan as well as the
// queue, and its per-step gate is Tony's HEAD-bound verification. STEP is chosen
// at start, so nothing escalates into it.
test("STEP gates its tools: plan and queue and verification, never escalate", () => {
  expect(isToolEnabledForMode("plan", "step")).toBe(true);
  expect(isToolEnabledForMode("steps", "step")).toBe(true);
  expect(isToolEnabledForMode("verification", "step")).toBe(true);
  expect(isToolEnabledForMode("escalate", "step")).toBe(false);

  // The queue verb belongs to no other mode.
  expect(isToolEnabledForMode("steps", "goal")).toBe(false);
  expect(isToolEnabledForMode("steps", "fast")).toBe(false);
});

test("STEP tells Austin to drive one step and never tick the queue", () => {
  const austin = buildDuoPrompt("Austin", "step", "/repo", ".");
  expect(austin).toContain("EXECUTE -> REVIEW -> INTEGRATE -> land the step");
  expect(austin).toContain("duo_set_steps");
  expect(austin).toContain("duo_set_plan");
  expect(austin).toContain("You never tick a box yourself");
  expect(austin).toContain("Do not start the next step first");
  // Austin's per-step verb is a sign-off, not a verification verdict.
  expect(austin).not.toContain("call duo_set_verification");
});

test("STEP makes Tony the per-step verifier whose pass lands the step", () => {
  const tony = buildDuoPrompt("Tony", "step", "/repo", ".");
  expect(tony).toContain("duo_set_verification");
  expect(tony).toContain("lands the step and advances the queue");
  expect(tony).toContain("read-only on the delivered artifact");
  expect(tony).toContain("fast-forwards your worktree to the landed HEAD");
  expect(tony).toContain("do not restate the same verdict with duo_send");
});

test("Chinese STEP prompts keep the protocol verbs", () => {
  const austin = buildDuoPrompt("Austin", "step", "/repo", ".", "zh");
  expect(austin).toContain("落地该步骤");
  expect(austin).toContain("duo_set_steps");
  expect(austin).toContain("你绝不自己打勾");
  const tony = buildDuoPrompt("Tony", "step", "/repo", ".", "zh");
  expect(tony).toContain("duo_set_verification");
  expect(tony).toContain("落地该步骤并推进队列");
  expect(tony).toContain("快进到已落地的 HEAD");
});

test("FAST makes Tony a read-only verifier and never offers the shared plan", () => {
  const tony = buildDuoPrompt("Tony", "fast", "/repo", ".");
  expect(tony).toContain("RUNNING -> VERIFY -> DONE");
  expect(tony).toContain("READ-ONLY");
  expect(tony).toContain("duo_set_verification");
  // The verdict is relayed by Duo Core, so Tony must not duplicate it with duo_send.
  expect(tony).toContain("do not restate the same verdict with duo_send");
  expect(tony).not.toContain("maintain one complete shared plan");
  expect(tony).not.toContain("INTEGRATE");
});

test("FAST makes Austin the driver and the sole delivered branch", () => {
  const austin = buildDuoPrompt("Austin", "fast", "/repo", ".");
  expect(austin).toContain("DRIVER ROLE");
  expect(austin).toContain("only branch Duo ever delivers");
  expect(austin).toContain("duo_set_status");
  expect(austin).not.toContain("maintain one complete shared plan");
});

test("GOAL keeps the full negotiated workflow", () => {
  const tony = buildDuoPrompt("Tony", "goal", "/repo", ".");
  expect(tony).toContain("PLAN -> EXECUTE -> REVIEW -> INTEGRATE -> DONE");
  expect(tony).toContain("duo_set_plan");
  expect(tony).toContain("Duo Core sends each new Goal task to both you and Austin");
  expect(tony).toContain("do not wait for Austin to wake you");
  expect(tony).toContain("INTEGRATE");
  expect(tony).not.toContain("READ-ONLY");
  expect(tony).not.toContain("RUNNING -> VERIFY");

  const austin = buildDuoPrompt("Austin", "goal", "/repo", ".");
  expect(austin).toContain("Duo Core sends each new Goal task to both you and Tony");
  expect(austin).not.toContain("wake Tony");
});

test("Chinese system prompts preserve Duo rules and protocol tokens", () => {
  expect(parseDuoLanguage("zh")).toBe("zh");
  expect(parseDuoLanguage("fr")).toBe("en");
  const austin = buildDuoPrompt("Austin", "goal", "/repo", "src", "zh");
  expect(austin).toContain("PLAN 是协商阶段");
  expect(austin).toContain("duo_set_plan");
  expect(austin).toContain("duo_set_status({ready:true})");
  expect(austin).toContain("[Duo session resumed]");
  expect(austin).toContain("Duo Core 会把每个 Goal 新任务同时发送给你和 Tony");
  const tony = buildDuoPrompt("Tony", "fast", "/repo", ".", "zh");
  expect(tony).toContain("只读");
  expect(tony).toContain("duo_set_verification");
  expect(tony).toContain("不要用 duo_send 重复结论");
  const goalTony = buildDuoPrompt("Tony", "goal", "/repo", ".", "zh");
  expect(goalTony).toContain("Duo Core 会把每个 Goal 新任务同时发送给你和 Austin");
  expect(goalTony).toContain("不要等待 Austin 再唤醒你");
});

test("installDuoPrompt publishes the mode selected for this session", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sections: Record<string, string> = {};
  installDuoPrompt(
    { on: (name: string, handler: (event: any) => Promise<void>) => handlers.set(name, handler) },
    "Tony",
    parseDuoMode("fast"),
    // The assertion below is about the mode, not the language, so the language is
    // pinned: inheriting DUO_LANGUAGE made this test fail in a Chinese session.
    "en",
  );

  await handlers.get("before_agent_start")!({ systemPromptOptions: { sections } });
  expect(sections["duo"]).toContain("READ-ONLY");
  expect(sections["duo"]).toContain("duo_set_verification");
  expect(sections["duo"]).not.toContain("maintain one complete shared plan");
});

// The Duo work preview reads what an agent is doing from these activity
// details, so the payload shape is part of the bridge contract.
test("stream activity carries Pi context usage and estimated token speed", async () => {
  const handlers = new Map<string, (event: any, ctx?: any) => Promise<void>>();
  const sent: any[] = [];
  installLifecycle(
    { on: (name: string, handler: (event: any, ctx?: any) => Promise<void>) => handlers.set(name, handler) },
    { setHandler() {}, sendActivity: (activity: string, extra: any = {}) => sent.push({ activity, ...extra }), send() {} } as any,
    "Austin",
  );

  const originalNow = Date.now;
  let now = 1000;
  Date.now = () => now;
  try {
    const update = handlers.get("message_update")!;
    const context = { getContextUsage: () => ({ tokens: 1234, contextWindow: 100000 }) };
    await update({ assistantMessageEvent: { type: "text_delta", delta: "a".repeat(40) }, message: { role: "assistant", content: [{ type: "text", text: "draft" }] } }, context);
    expect(sent[0]).toMatchObject({ activity: "stream", contextTokens: 1234, contextWindow: 100000 });

    now = 2500;
    await update({ assistantMessageEvent: { type: "text_delta", delta: "b".repeat(40) }, message: { role: "assistant", content: [{ type: "text", text: "draft continues" }] } }, context);
    expect(sent[1].tokensPerSecond).toBeCloseTo(13.3, 0);
  } finally {
    Date.now = originalNow;
  }
});

test("tool and stream activity carry the preview detail", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sent: any[] = [];
  installLifecycle(
    { on: (name: string, handler: (event: any) => Promise<void>) => handlers.set(name, handler) },
    {
      setHandler() {},
      sendActivity: (activity: string, extra: any = {}) => sent.push({ activity, ...extra }),
      send: (message: any) => sent.push(message),
    } as any,
    "Austin",
  );

  await handlers.get("tool_execution_start")!({ toolName: "bash", args: { command: "go test ./...\n" } });
  expect(sent[0]).toMatchObject({ activity: "tool_start", tool: "bash", detail: "go test ./..." });

  await handlers.get("tool_execution_end")!({ toolName: "bash", isError: true, result: "exit status 1" });
  expect(sent[1]).toMatchObject({ activity: "tool_error", tool: "bash", detail: "exit status 1" });

  // A successful end must not replace the arguments captured at tool_start.
  await handlers.get("tool_execution_end")!({ toolName: "bash", isError: false, result: "ok" });
  expect(sent[2]).toMatchObject({ activity: "tool_end", tool: "bash", detail: "" });

  await handlers.get("message_update")!({
    message: { role: "assistant", content: [{ type: "text", text: "first line\nsecond line" }] },
  });
  expect(sent[3]).toMatchObject({ activity: "stream", detail: "first line second line" });
});
