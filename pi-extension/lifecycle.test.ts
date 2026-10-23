import { expect, test } from "bun:test";
import { installLifecycle } from "./lifecycle";
import { parseDuoLanguage, parseDuoMode } from "./mode";
import { buildDuoPrompt, installDuoPrompt } from "./prompt";

test("compaction abort does not report an agent error", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sent: any[] = [];
  installLifecycle(
    {
      on: (name: string, handler: (event: any) => Promise<void>) =>
        handlers.set(name, handler),
    },
    {
      setHandler() {},
      sendActivity() {},
      send: (message: any) => sent.push(message),
    } as any,
    "Austin",
  );

  const messageEnd = handlers.get("message_end")!;
  await messageEnd({
    message: {
      role: "assistant",
      stopReason: "error",
      errorMessage: "This operation was aborted",
    },
  });
  expect(sent).toEqual([]);

  await messageEnd({
    message: {
      role: "assistant",
      stopReason: "error",
      errorMessage: "request failed",
    },
  });
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({
    type: "agent_error",
    agent: "Austin",
    text: "request failed",
  });
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

test("STEP tells Austin to plan the queue and follow each item's policy", () => {
  const austin = buildDuoPrompt("Austin", "step", "/repo", ".");
  expect(austin).toContain("EXECUTE -> REVIEW -> INTEGRATE -> land the step");
  expect(austin).toContain("duo_set_steps");
  expect(austin).toContain("duo_set_plan");
  expect(austin).toContain("you never tick a box yourself");
  expect(austin).toContain("never work ahead on a later item");
  expect(austin).toContain("[fast] or [goal]");
  expect(austin).toContain("A FAST item is yours alone");
  expect(austin).toContain("A GOAL item is joint work");
  // Austin's per-step verb is a sign-off, not a verification verdict.
  expect(austin).not.toContain("call duo_set_verification");
});

test("STEP makes Tony a FAST verifier and a GOAL co-author", () => {
  const tony = buildDuoPrompt("Tony", "step", "/repo", ".");
  expect(tony).toContain("duo_set_verification");
  expect(tony).toContain("lands the step and advances the queue");
  expect(tony).toContain("read-only verifier");
  expect(tony).toContain("A GOAL item makes you a co-author");
  expect(tony).toContain("fast-forwards your worktree to the landed HEAD");
  expect(tony).toContain("do not restate the same verdict with duo_send");
});

test("Chinese STEP prompts keep the protocol verbs for both policies", () => {
  const austin = buildDuoPrompt("Austin", "step", "/repo", ".", "zh");
  expect(austin).toContain("落地该步骤");
  expect(austin).toContain("duo_set_steps");
  expect(austin).toContain("你绝不自己打勾");
  expect(austin).toContain("[fast] 或 [goal]");
  expect(austin).toContain("GOAL 条目是共同工作");
  const tony = buildDuoPrompt("Tony", "step", "/repo", ".", "zh");
  expect(tony).toContain("duo_set_verification");
  expect(tony).toContain("落地该步骤并推进队列");
  expect(tony).toContain("快进到已落地的 HEAD");
  expect(tony).toContain("GOAL 条目中你是共同作者");
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
  expect(tony).toContain(
    "Duo Core sends each new Goal task to both you and Austin",
  );
  expect(tony).toContain("do not wait for Austin to wake you");
  expect(tony).toContain("INTEGRATE");
  expect(tony).not.toContain("READ-ONLY");
  expect(tony).not.toContain("RUNNING -> VERIFY");

  const austin = buildDuoPrompt("Austin", "goal", "/repo", ".");
  expect(austin).toContain(
    "Duo Core sends each new Goal task to both you and Tony",
  );
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
  expect(goalTony).toContain(
    "Duo Core 会把每个 Goal 新任务同时发送给你和 Austin",
  );
  expect(goalTony).toContain("不要等待 Austin 再唤醒你");
});

test("installDuoPrompt publishes the mode selected for this session", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sections: Record<string, string> = {};
  installDuoPrompt(
    {
      on: (name: string, handler: (event: any) => Promise<void>) =>
        handlers.set(name, handler),
    },
    "Tony",
    parseDuoMode("fast"),
    // The assertion below is about the mode, not the language, so the language is
    // pinned: inheriting DUO_LANGUAGE made this test fail in a Chinese session.
    "en",
  );

  await handlers.get("before_agent_start")!({
    systemPromptOptions: { sections },
  });
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
    {
      on: (name: string, handler: (event: any, ctx?: any) => Promise<void>) =>
        handlers.set(name, handler),
    },
    {
      setHandler() {},
      sendActivity: (activity: string, extra: any = {}) =>
        sent.push({ activity, ...extra }),
      send() {},
    } as any,
    "Austin",
  );

  const originalNow = Date.now;
  let now = 1000;
  Date.now = () => now;
  try {
    const update = handlers.get("message_update")!;
    const context = {
      getContextUsage: () => ({ tokens: 1234, contextWindow: 100000 }),
    };
    await update(
      {
        assistantMessageEvent: { type: "text_delta", delta: "a".repeat(40) },
        message: {
          role: "assistant",
          content: [{ type: "text", text: "draft" }],
        },
      },
      context,
    );
    expect(sent[0]).toMatchObject({
      activity: "stream",
      contextTokens: 1234,
      contextWindow: 100000,
    });

    now = 2500;
    await update(
      {
        assistantMessageEvent: { type: "text_delta", delta: "b".repeat(40) },
        message: {
          role: "assistant",
          content: [{ type: "text", text: "draft continues" }],
        },
      },
      context,
    );
    expect(sent[1].tokensPerSecond).toBeCloseTo(13.3, 0);
  } finally {
    Date.now = originalNow;
  }
});

test("tool and stream activity carry the preview detail", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sent: any[] = [];
  installLifecycle(
    {
      on: (name: string, handler: (event: any) => Promise<void>) =>
        handlers.set(name, handler),
    },
    {
      setHandler() {},
      sendActivity: (activity: string, extra: any = {}) =>
        sent.push({ activity, ...extra }),
      send: (message: any) => sent.push(message),
    } as any,
    "Austin",
  );

  await handlers.get("tool_execution_start")!({
    toolName: "bash",
    args: { command: "go test ./...\n" },
  });
  expect(sent[0]).toMatchObject({
    activity: "tool_start",
    tool: "bash",
    detail: "go test ./...",
  });

  await handlers.get("tool_execution_end")!({
    toolName: "bash",
    isError: true,
    result: "exit status 1",
  });
  expect(sent[1]).toMatchObject({
    activity: "tool_error",
    tool: "bash",
    detail: "exit status 1",
  });

  // A successful end must not replace the arguments captured at tool_start.
  await handlers.get("tool_execution_end")!({
    toolName: "bash",
    isError: false,
    result: "ok",
  });
  expect(sent[2]).toMatchObject({
    activity: "tool_end",
    tool: "bash",
    detail: "",
  });

  await handlers.get("message_update")!({
    message: {
      role: "assistant",
      content: [{ type: "text", text: "first line\nsecond line" }],
    },
  });
  expect(sent[3]).toMatchObject({
    activity: "stream",
    detail: "first line second line",
  });
});

test("a turn's round-trips are reported once, at agent_settled", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sent: any[] = [];
  installLifecycle(
    {
      on: (name: string, handler: (event: any) => Promise<void>) =>
        handlers.set(name, handler),
    },
    {
      setHandler() {},
      sendActivity() {},
      send: (message: any) => sent.push(message),
    } as any,
    "Austin",
  );

  const messageEnd = handlers.get("message_end")!;
  const settled = handlers.get("agent_settled")!;

  // One agent turn with a tool loop: two measured round-trips.
  await messageEnd({
    message: {
      role: "assistant",
      stopReason: "toolUse",
      content: [{ type: "text", text: "calling" }],
      usage: {
        input: 1200,
        output: 340,
        cacheRead: 9000,
        cacheWrite: 0,
        totalTokens: 10540,
        cost: { input: 0.01, output: 0.02, cacheRead: 0.005, cacheWrite: 0, total: 0.035 },
      },
    },
  });
  // Mid-turn round-trips report nothing on their own.
  expect(sent.filter((m) => m.type === "usage")).toEqual([]);

  await messageEnd({
    message: {
      role: "assistant",
      stopReason: "stop",
      content: [{ type: "text", text: "done" }],
      usage: {
        input: 500,
        output: 100,
        cacheRead: 2000,
        cacheWrite: 0,
        totalTokens: 2600,
        cost: { input: 0.001, output: 0.002, cacheRead: 0.001, cacheWrite: 0, total: 0.004 },
      },
    },
  });
  await settled();

  const usage = sent.filter((m) => m.type === "usage");
  expect(usage).toHaveLength(1);
  expect(usage[0]).toMatchObject({
    agent: "Austin",
    usage: {
      turns: 1,
      inputTokens: 1700,
      outputTokens: 440,
      cacheReadTokens: 11000,
      cacheWriteTokens: 0,
      totalTokens: 13140,
      costUSD: 0.039,
    },
  });

  // The next turn starts from zero: a settled turn is not counted twice.
  sent.length = 0;
  await settled();
  expect(sent.filter((m) => m.type === "usage")).toEqual([]);
});

test("an unmeasured turn reports no usage", async () => {
  const handlers = new Map<string, (event: any) => Promise<void>>();
  const sent: any[] = [];
  installLifecycle(
    {
      on: (name: string, handler: (event: any) => Promise<void>) =>
        handlers.set(name, handler),
    },
    { setHandler() {}, sendActivity() {}, send: (m: any) => sent.push(m) } as any,
    "Tony",
  );

  const messageEnd = handlers.get("message_end")!;
  const settled = handlers.get("agent_settled")!;
  // No usage at all (a local reply), and an all-zero usage: neither is a measurement.
  await messageEnd({ message: { role: "assistant", content: [{ type: "text", text: "x" }] } });
  await messageEnd({
    message: {
      role: "assistant",
      content: [{ type: "text", text: "y" }],
      usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cost: { total: 0 } },
    },
  });
  await settled();
  expect(sent.filter((m) => m.type === "usage")).toEqual([]);
});
