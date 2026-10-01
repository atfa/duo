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
// created and its first part. Without it the harness never learns the agent is
// thinking and nudges a model that is merely slow.
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
  expect(activities).toContain("provider_end");

  // An idle without a part must still close the span, or the agent looks busy forever.
  activities.length = 0;
  await handler({ event: { type: "message.updated", properties: { info: { id: "msg_b", role: "assistant" }, sessionID: "ses_1" } } });
  await handler({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } });
  expect(activities).toContain("provider_end");
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
