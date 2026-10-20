import { expect, test } from "bun:test";
import { installLifecycle } from "./lifecycle";
import { SessionTracker } from "./session";
import { parseDuoLanguage } from "./mode";
import { buildDuoPrompt, installDuoPrompt } from "./prompt";

test("Chinese system prompt is installed with workflow rules intact", async () => {
  expect(parseDuoLanguage("zh")).toBe("zh");
  expect(parseDuoLanguage(undefined)).toBe("en");
  const prompt = buildDuoPrompt("Tony", "goal", "/repo", "src", "zh");
  expect(prompt).toContain("独立审查");
  expect(prompt).toContain("duo_send");
  expect(prompt).toContain("duo_set_status({ready:true})");
  const hooks: Record<string, (input: any) => void> = {};
  installDuoPrompt(
    {
      session: {
        hook: (name: string, cb: any) => {
          hooks[name] = cb;
        },
      },
    } as any,
    "Tony",
    "fast",
    "zh",
  );
  const input: any = { system: [] };
  await hooks["context"](input);
  expect(input.system).toHaveLength(1);
  expect(input.system[0].type).toBe("text");
  expect(input.system[0].text).toContain("你是独立验证者和副驾驶");
  expect(input.system[0].text).toContain("duo_set_verification");
});

// Duo must never delegate a decision back to the human through opencode's
// interactive question tool: the harness owns acknowledgements and both agents
// run unattended. The context hook sees the final per-request tool map, so this
// is the reliable place to remove it even for built-in build/plan agents.
test("Duo context removes question and re-reads the live mode", async () => {
  let mode: "fast" | "step" = "fast";
  const hooks: Record<string, (input: any) => void> = {};
  installDuoPrompt(
    {
      session: {
        hook: (name: string, cb: any) => {
          hooks[name] = cb;
        },
      },
    } as any,
    "Austin",
    () => mode,
    "en",
  );

  const first: any = { system: [], tools: { question: {}, bash: {} } };
  await hooks["context"](first);
  expect(first.tools.question).toBeUndefined();
  expect(first.tools.bash).toBeDefined();
  expect(first.system[0].text).toContain("DRIVER ROLE");

  mode = "step";
  const second: any = { system: [], tools: { question: {} } };
  await hooks["context"](second);
  expect(second.system[0].text).toContain("STEP ROLE");
  expect(second.system[0].text).toContain("[fast] or [goal]");
});

// opencode 2.0 reports only assistant output on session.text.*; an injected
// prompt arrives as an inbox item, never as an assistant part, so it can no
// longer be echoed back into Duo's timeline as if the agent had said it.
test("injected inbox prompts are not reported as assistant output", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    {
      sendActivity: (a: string, extra: any = {}) => {
        void extra;
        sent.push({ activity: a });
      },
      send: (m: any) => sent.push(m),
    } as any,
    "Austin",
    () => true,
  );

  // Duo injecting a steer prompt is visible on the inbox, not as assistant text.
  await handler({
    type: "session.inbox.enqueued",
    data: {
      sessionID: "ses_1",
      item: {
        payload: { text: "[Human task from Duo]\n\nfix it" },
        delivery: "steer",
      },
    },
  });

  // The agent then answers in its own message.
  await handler({
    type: "session.execution.started",
    data: { sessionID: "ses_1" },
  });
  await handler({
    type: "session.text.started",
    data: { sessionID: "ses_1", assistantMessageID: "msg_a", ordinal: 0 },
  });
  await handler({
    type: "session.text.ended",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      ordinal: 0,
      text: "I found the bug.",
    },
  });
  await handler({
    type: "session.execution.succeeded",
    data: { sessionID: "ses_1" },
  });

  const messages = sent.filter((m) => m.type === "assistant_message");
  expect(messages).toHaveLength(1);
  expect(messages[0].text).toBe("I found the bug.");
  expect(messages[0].text).not.toContain("Human task from Duo");
});

// opencode reports the model call as the span between session.step.started and
// session.step.ended. Without it the harness never learns the agent is thinking
// and nudges a model that is merely slow.
//
// The span deliberately stays open across streamed text: Duo only renders the
// output rate while the provider is active, so closing it on the first part
// discarded the speed it exists to show.
test("a model call is reported as provider_start then provider_end", async () => {
  const activities: string[] = [];
  const handler = installLifecycle(
    { sendActivity: (a: string) => activities.push(a), send: () => {} } as any,
    "Austin",
    () => true,
  );

  await handler({
    type: "session.execution.started",
    data: { sessionID: "ses_1" },
  });
  expect(activities).toContain("agent_start");

  await handler({
    type: "session.step.started",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      model: { providerID: "anthropic", id: "claude" },
    },
  });
  expect(activities).toContain("provider_start");

  await handler({
    type: "session.text.delta",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      ordinal: 0,
      delta: "hi",
    },
  });
  expect(activities).not.toContain("provider_end");

  await handler({
    type: "session.step.ended",
    data: { sessionID: "ses_1", assistantMessageID: "msg_a", tokens: {} },
  });
  expect(activities).toContain("provider_end");

  // A turn end without a step must still close the span, or the agent looks busy forever.
  activities.length = 0;
  await handler({
    type: "session.step.started",
    data: { sessionID: "ses_1", assistantMessageID: "msg_b" },
  });
  await handler({
    type: "session.execution.succeeded",
    data: { sessionID: "ses_1" },
  });
  expect(activities).toContain("provider_end");
});

// opencode reports real token counts per step, so context usage is measured
// rather than guessed.
test("real token counts are reported as context usage", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    {
      sendActivity: (a: string, extra: any = {}) =>
        sent.push({ activity: a, ...extra }),
      send: () => {},
    } as any,
    "Austin",
    () => true,
    async () => 200000,
  );

  await handler({
    type: "session.step.started",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      model: { providerID: "anthropic", id: "claude" },
    },
  });
  await handler({
    type: "session.text.delta",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      ordinal: 0,
      delta: "hello",
    },
  });
  await handler({
    type: "session.step.ended",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      tokens: {
        input: 44000,
        output: 900,
        reasoning: 100,
        cache: { read: 4000, write: 0 },
      },
    },
  });

  const end = sent.find((m) => m.activity === "provider_end");
  // 44000 input + 100 reasoning + 4000 cache.read. output is excluded: it is not
  // yet part of the context the next turn carries.
  expect(end.contextTokens).toBe(48100);
  expect(end.contextWindow).toBe(200000);
});

// Speed is only visible while the provider is active, so it must be attached to
// stream events inside the span and estimated from streamed characters.
test("stream events carry an output rate while the provider is active", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    {
      sendActivity: (a: string, extra: any = {}) =>
        sent.push({ activity: a, ...extra }),
      send: () => {},
    } as any,
    "Austin",
    () => true,
  );

  await handler({
    type: "session.step.started",
    data: { sessionID: "ses_1", assistantMessageID: "msg_a" },
  });
  // Delta frames are stream fragments; each must advance the output clock or the
  // samples collapse into one instant and the rate stays zero.
  let text = "";
  for (let i = 0; i < 4; i++) {
    text += "x".repeat(40);
    const now = Date.now() + i * 1100;
    const realNow = Date.now;
    Date.now = () => now;
    await handler({
      type: "session.text.delta",
      data: {
        sessionID: "ses_1",
        assistantMessageID: "msg_a",
        ordinal: 0,
        delta: "x".repeat(40),
      },
    });
    Date.now = realNow;
  }

  const streams = sent.filter((m) => m.activity === "stream");
  expect(streams.length).toBeGreaterThan(1);
  expect(streams[streams.length - 1].tokensPerSecond).toBeGreaterThan(0);
});

// provider_end clears the speed in the tracker, so the context figure has to ride
// along on it or the preview goes blank the moment a step completes.
test("provider_end retains the latest context usage", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    {
      sendActivity: (a: string, extra: any = {}) =>
        sent.push({ activity: a, ...extra }),
      send: () => {},
    } as any,
    "Austin",
    () => true,
    async () => 100000,
  );

  await handler({
    type: "session.step.started",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      model: { providerID: "p", id: "m" },
    },
  });
  await handler({
    type: "session.step.ended",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      tokens: {
        input: 12000,
        output: 10,
        reasoning: 0,
        cache: { read: 0, write: 0 },
      },
    },
  });
  await handler({
    type: "session.execution.succeeded",
    data: { sessionID: "ses_1" },
  });

  const end = sent.find((m) => m.activity === "provider_end");
  expect(end.contextTokens).toBe(12000);
  expect(end.contextWindow).toBe(100000);
});

// One assistant message routinely interleaves text and tool parts. Keeping only
// the last text block silently discarded everything before it.
test("text blocks across one assistant message are all reported", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    { sendActivity: () => {}, send: (m: any) => sent.push(m) } as any,
    "Austin",
    () => true,
  );

  await handler({
    type: "session.text.started",
    data: { sessionID: "ses_1", assistantMessageID: "msg_a", ordinal: 0 },
  });
  await handler({
    type: "session.text.ended",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      ordinal: 0,
      text: "first",
    },
  });
  await handler({
    type: "session.text.started",
    data: { sessionID: "ses_1", assistantMessageID: "msg_a", ordinal: 1 },
  });
  await handler({
    type: "session.text.ended",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      ordinal: 1,
      text: "second",
    },
  });
  await handler({
    type: "session.execution.succeeded",
    data: { sessionID: "ses_1" },
  });

  const messages = sent.filter((m) => m.type === "assistant_message");
  expect(messages).toHaveLength(1);
  expect(messages[0].text).toBe("first\nsecond");
});

// Tools without a declared name would render as "tool"; the name arrives on the
// call's first event and must stick for the call's completion.
test("tool activities keep the tool name learned from input.started", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    {
      sendActivity: (a: string, extra: any = {}) =>
        sent.push({ activity: a, ...extra }),
      send: () => {},
    } as any,
    "Austin",
    () => true,
  );

  await handler({
    type: "session.tool.input.started",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      id: "call_1",
      name: "read",
    },
  });
  await handler({
    type: "session.tool.called",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      id: "call_1",
      input: { filePath: "/a/b.txt" },
    },
  });
  await handler({
    type: "session.tool.success",
    data: {
      sessionID: "ses_1",
      assistantMessageID: "msg_a",
      id: "call_1",
      content: [],
    },
  });

  const start = sent.find((m) => m.activity === "tool_start");
  const end = sent.find((m) => m.activity === "tool_end");
  expect(start.tool).toBe("read");
  expect(start.detail).toBe("/a/b.txt");
  expect(end.tool).toBe("read");
});

// session.execution.failed reports a real failure once, but an intentional
// abort must stay quiet or Duo would ask the peer to diagnose a stall that
// never happened. Either way the execution is terminal, so it must settle.
test("aborts and interrupts are quiet but still settle the turn", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    {
      sendActivity: (a: string, extra: any = {}) => {
        void extra;
        sent.push({ activity: a });
      },
      send: (m: any) => sent.push(m),
    } as any,
    "Austin",
    () => true,
  );

  await handler({
    type: "session.execution.started",
    data: { sessionID: "ses_1" },
  });
  await handler({
    type: "session.step.started",
    data: { sessionID: "ses_1", assistantMessageID: "msg_a" },
  });
  await handler({
    type: "session.execution.failed",
    data: {
      sessionID: "ses_1",
      error: { type: "AbortError", message: "The operation was aborted" },
    },
  });
  await handler({
    type: "session.execution.interrupted",
    data: { sessionID: "ses_1", reason: "user" },
  });

  expect(sent.filter((m) => m.type === "agent_error")).toHaveLength(0);
  expect(sent.filter((m) => m.activity === "agent_settled")).toHaveLength(2);
  // A quiet settle closes the dangling provider span.
  expect(sent.filter((m) => m.activity === "provider_end")).toHaveLength(1);
});

// A real provider error is still reported, exactly once per failed execution,
// and the turn settles like any terminal state.
test("a real provider error is reported as agent_error", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    {
      sendActivity: (a: string, extra: any = {}) => {
        void extra;
        sent.push({ activity: a });
      },
      send: (m: any) => sent.push(m),
    } as any,
    "Austin",
    () => true,
  );

  await handler({
    type: "session.execution.started",
    data: { sessionID: "ses_1" },
  });
  await handler({
    type: "session.execution.failed",
    data: {
      sessionID: "ses_1",
      error: { type: "APIError", message: "rate limited", status: 429 },
    },
  });

  const errors = sent.filter((m) => m.type === "agent_error");
  expect(errors).toHaveLength(1);
  expect(errors[0].text).toBe("rate limited");
  expect(sent.filter((m) => m.activity === "agent_settled")).toHaveLength(1);
});

// opencode 2.0 events carry the session id directly on data.
test("the tracker reads the session from data.sessionID", () => {
  const tracker = new SessionTracker("");
  const ours = tracker.observe("session.step.started", {
    sessionID: "ses_real",
    assistantMessageID: "msg_123",
  });
  expect(ours).toBe(true);
  expect(tracker.sessionID).toBe("ses_real");
});

test("the tracker adopts the root session and ignores another session's events", () => {
  const tracker = new SessionTracker("");
  tracker.observe("session.created", { sessionID: "ses_root" });
  expect(tracker.sessionID).toBe("ses_root");
  // A side-task session reports a parentID and must not be adopted.
  tracker.observe("session.created", {
    sessionID: "ses_child",
    parentID: "ses_root",
  });
  expect(
    tracker.observe("session.text.delta", {
      sessionID: "ses_other",
      delta: "x",
    }),
  ).toBe(false);
  expect(tracker.sessionID).toBe("ses_root");
});
