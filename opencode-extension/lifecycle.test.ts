import { expect, test } from "bun:test";
import { installLifecycle } from "./lifecycle";
import { SessionTracker } from "./session";

// A prompt Duo injects arrives as a *user* message. Reporting it back as an
// assistant_message filled the timeline with the human's own words, and because
// the seeded messageID then filtered out the real reply, the agent's actual
// output never appeared at all.
test("an injected user message is never reported as assistant output", async () => {
  const sent: any[] = [];
  const activities: any[] = [];
  const handler = installLifecycle(
    { sendActivity: (a: string, extra: any = {}) => activities.push({ activity: a, ...extra }), send: (m: any) => sent.push(m) } as any,
    "Austin",
    () => true,
  );

  const user = "msg_user";
  const assistant = "msg_assistant";

  await handler({ event: { type: "message.updated", properties: { info: { id: user, role: "user" }, sessionID: "ses_1" } } });
  await handler({ event: { type: "message.part.updated", properties: { part: { type: "text", text: "[Human task from Duo]\n\nfix it", messageID: user }, sessionID: "ses_1" } } });

  // The agent then answers in its own message.
  await handler({ event: { type: "message.updated", properties: { info: { id: assistant, role: "assistant" }, sessionID: "ses_1" } } });
  await handler({ event: { type: "message.part.updated", properties: { part: { type: "text", text: "I found the bug.", messageID: assistant }, sessionID: "ses_1" } } });
  await handler({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });

  const messages = sent.filter((m) => m.type === "assistant_message");
  expect(messages).toHaveLength(1);
  expect(messages[0].text).toBe("I found the bug.");
  expect(messages[0].text).not.toContain("Human task from Duo");
});

// opencode reports the model call as the span between an assistant message being
// created and its step-finish. Without it the harness never learns the agent is
// thinking and nudges a model that is merely slow.
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

  await handler({ event: { type: "message.updated", properties: { info: { id: "msg_a", role: "assistant" }, sessionID: "ses_1" } } });
  expect(activities).toContain("provider_start");

  await handler({ event: { type: "message.part.updated", properties: { part: { type: "text", text: "hi", messageID: "msg_a" }, sessionID: "ses_1" } } });
  expect(activities).not.toContain("provider_end");

  await handler({ event: { type: "message.part.updated", properties: { part: { type: "step-finish", messageID: "msg_a" }, sessionID: "ses_1" } } });
  expect(activities).toContain("provider_end");

  // An idle without a step must still close the span, or the agent looks busy forever.
  activities.length = 0;
  await handler({ event: { type: "message.updated", properties: { info: { id: "msg_b", role: "assistant" }, sessionID: "ses_1" } } });
  await handler({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
  expect(activities).toContain("provider_end");
});

// opencode reports real token counts on the assistant message, so context usage
// is measured rather than guessed.
test("real token counts are reported as context usage", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    { sendActivity: (a: string, extra: any = {}) => sent.push({ activity: a, ...extra }), send: () => {} } as any,
    "Austin",
    () => true,
    async () => 200000,
  );

  await handler({ event: { type: "message.updated", properties: { info: { id: "msg_a", role: "assistant", providerID: "anthropic", modelID: "claude", tokens: { input: 40000, output: 500, reasoning: 100, cache: { read: 4000, write: 0 } } }, sessionID: "ses_1" } } });
  await handler({ event: { type: "message.part.updated", properties: { part: { type: "text", text: "hello", messageID: "msg_a" }, sessionID: "ses_1" } } });
  await handler({ event: { type: "message.part.updated", properties: { part: { type: "step-finish", messageID: "msg_a", tokens: { input: 44000, output: 900, reasoning: 100, cache: { read: 4000, write: 0 } } }, sessionID: "ses_1" } } });

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
    { sendActivity: (a: string, extra: any = {}) => sent.push({ activity: a, ...extra }), send: () => {} } as any,
    "Austin",
    () => true,
  );

  await handler({ event: { type: "message.updated", properties: { info: { id: "msg_a", role: "assistant" }, sessionID: "ses_1" } } });
  // A part re-reports its cumulative text, so each update must advance the clock.
  // Otherwise the samples collapse into one instant and the rate stays zero.
  let text = "";
  for (let i = 0; i < 4; i++) {
    text += "x".repeat(40);
    const now = Date.now() + i * 1100;
    const realNow = Date.now;
    Date.now = () => now;
    await handler({ event: { type: "message.part.updated", properties: { part: { type: "text", text, id: "prt_1", messageID: "msg_a" }, sessionID: "ses_1" } } });
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
    { sendActivity: (a: string, extra: any = {}) => sent.push({ activity: a, ...extra }), send: () => {} } as any,
    "Austin",
    () => true,
    async () => 100000,
  );

  await handler({ event: { type: "message.updated", properties: { info: { id: "msg_a", role: "assistant", providerID: "p", modelID: "m", tokens: { input: 12000, output: 10, reasoning: 0, cache: { read: 0, write: 0 } } }, sessionID: "ses_1" } } });
  await handler({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });

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

  await handler({ event: { type: "message.updated", properties: { info: { id: "msg_a", role: "assistant" }, sessionID: "ses_1" } } });
  await handler({ event: { type: "message.part.updated", properties: { part: { type: "text", text: "first", messageID: "msg_a" }, sessionID: "ses_1" } } });
  await handler({ event: { type: "message.part.updated", properties: { part: { type: "text", text: "second", messageID: "msg_a" }, sessionID: "ses_1" } } });
  await handler({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });

  const messages = sent.filter((m) => m.type === "assistant_message");
  expect(messages).toHaveLength(1);
  expect(messages[0].text).toBe("first\nsecond");
});

// A part before its message.updated has no role yet. Treating "unknown" as
// assistant would reintroduce the echo, so it is dropped rather than guessed.
test("a text part with no known role is not reported", async () => {
  const sent: any[] = [];
  const handler = installLifecycle(
    { sendActivity: () => {}, send: (m: any) => sent.push(m) } as any,
    "Austin",
    () => true,
  );
  await handler({ event: { type: "message.part.updated", properties: { part: { type: "text", text: "orphan", messageID: "msg_unknown" }, sessionID: "ses_1" } } });
  await handler({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
  expect(sent.filter((m) => m.type === "assistant_message")).toHaveLength(0);
});

// message.updated carries the session on properties.info, and its id is a
// message id ("msg_…"). Falling back to info.id would point the tracker at a
// message and filter out the very events the role lookup needs.
test("the tracker reads the session from info.sessionID, not info.id", () => {
  const tracker = new SessionTracker("");
  const ours = tracker.observe("message.updated", {
    info: { id: "msg_123", role: "assistant", sessionID: "ses_real" },
  });
  expect(ours).toBe(true);
  expect(tracker.sessionID).toBe("ses_real");
});

test("the tracker ignores another session's events", () => {
  const tracker = new SessionTracker("");
  tracker.observe("session.created", { info: { id: "ses_root" } });
  expect(tracker.observe("message.updated", { info: { id: "msg_1", sessionID: "ses_other" } })).toBe(false);
  expect(tracker.sessionID).toBe("ses_root");
});
