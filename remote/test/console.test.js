import test from "node:test";
import assert from "node:assert/strict";

import { agentId, agentLabel, agentMeta, applyEvent, buildCommand, COMMAND_KINDS, dotClass, modeKey, needsModeConfirm, nextSeq, PENDING_TIMEOUT_MS, resendText, settlePending, viewModel } from "../public/console.js";
import { b64uEncode, commandMac, decryptEnvelope, deriveKeys, parsePairingKey, sealCommand } from "../public/crypto.js";
import { VECTOR } from "./vectors.js";

test("the newest sequence number wins, and a restart moves the epoch", () => {
  assert.equal(nextSeq(0, 1), 1);
  assert.equal(nextSeq(4, 5), 5);
  assert.equal(nextSeq(7, 2), 2);
  assert.equal(nextSeq(3, 0), 3);
  assert.equal(nextSeq(3, undefined), 3);
});

test("a state payload becomes a renderable model, tolerating missing fields", () => {
  const model = viewModel({
    v: 1,
    n: 7,
    project: "duo",
    session: "sess-1",
    mode: "step",
    phase: "EXECUTE",
    step: { index: 2, total: 3, policy: "goal", title: "mobile console" },
    agents: { austin: { connected: true, running: true, model: "claude", activity: "editing" } },
    activity: [{ at: Date.now(), kind: "assistant", agent: "tony", text: "reviewing" }],
    observer: { analyzing: false, reports: [{ at: Date.now(), text: "all green" }] },
  });
  assert.equal(model.nonce, 7);
  assert.equal(model.mode, "STEP");
  assert.equal(model.step.total, 3);
  assert.equal(model.agents.length, 2);
  assert.equal(model.agents[0].running, true);
  assert.equal(model.agents[1].connected, false);
  assert.equal(model.activity[0].agent, "tony");
  assert.equal(model.observer.report, "all green");

  const empty = viewModel({});
  assert.equal(empty.project, "Duo");
  assert.equal(empty.nonce, 0);
  assert.deepEqual(empty.activity, []);
});

test("events fold into the model and unknown kinds are ignored", () => {
  const base = viewModel({ n: 3, activity: [], agents: {}, observer: {} });
  const withActivity = applyEvent(base, { kind: "activity", agent: "austin", text: "wrote a file", n: 4 });
  assert.equal(withActivity.activity.length, 1);
  assert.equal(withActivity.nonce, 4);
  assert.equal(applyEvent(base, { kind: "phase", text: "REVIEW" }).phase, "REVIEW");
  // Observer and delivery results arrive with the next snapshot, never guessed
  // from a one-line event.
  assert.equal(applyEvent(base, { kind: "observer", text: "report text" }), base);
  assert.equal(applyEvent(base, { kind: "delivery", text: "applied" }), base);
  assert.equal(applyEvent(base, { kind: "from-the-future", text: "x" }), base);
});

test("an agent is found whatever spelling the host sends", () => {
  assert.equal(agentId("Austin"), "austin");
  assert.equal(agentId(" tony "), "tony");
  assert.equal(agentId("duo"), "");
  assert.equal(agentId(undefined), "");

  // An older host keys the snapshot with the protocol's own spelling. A page
  // that only matched "austin" drew a red dot for a connected agent, so both
  // spellings must reach the same entry, and with it the model.
  const model = viewModel({
    n: 5,
    agents: {
      Austin: { connected: true, running: false, model: "claude-sonnet-4-6", activity: "idle" },
      Tony: { connected: true, running: true, model: "gpt-5", activity: "editing" },
    },
  });
  assert.deepEqual(
    model.agents.map((agent) => [agent.id, agent.connected, agent.running, agent.model]),
    [
      ["austin", true, false, "claude-sonnet-4-6"],
      ["tony", true, true, "gpt-5"],
    ],
  );

  // The event path had the same mismatch, so an "Austin" event never marked
  // anyone as running.
  const folded = applyEvent(model, { kind: "agent", agent: "Austin", text: "wrote a file", n: 6 });
  assert.equal(folded.agents[0].running, true);
  assert.equal(folded.agents[0].activity, "wrote a file");
  assert.equal(folded.agents[1].running, true);
  assert.equal(folded.nonce, 6);
});

test("the agent indicator is green when healthy and red only for a failure", () => {
  // Green: attached and idle. Blue: working. Grey: not attached yet, which is a
  // normal moment during startup and used to be painted red. Red: a failure.
  assert.equal(dotClass({ connected: true, running: false, state: "running" }), "dot-on");
  assert.equal(dotClass({ connected: true, running: true, state: "running" }), "dot-run");
  assert.equal(dotClass({ connected: false, running: false, state: "starting" }), "dot-wait");
  assert.equal(dotClass({ connected: false, running: false, state: "" }), "dot-wait");
  assert.equal(dotClass({ connected: false, running: false, state: "failed" }), "dot-bad");
  assert.equal(dotClass(undefined), "dot-wait");
});

test("an agent shows its driver and model, and says when none is configured", () => {
  assert.equal(
    agentMeta({ driver: "pi", model: "claude-sonnet-4-6", thinking: "high", state: "running", stateKnown: true }),
    "pi · claude-sonnet-4-6 · high",
  );
  // The host reports an empty state only for an agent it has no driver for, so
  // the phone never claims "starting" for something that is not configured.
  assert.equal(agentMeta({ driver: "", model: "", state: "", stateKnown: true }), "not configured");
  // A host that predates the field says nothing at all; the page stays quiet.
  assert.equal(agentMeta({ model: "gpt-5", state: "", stateKnown: false }), "gpt-5");
  assert.equal(agentMeta(undefined), "");

  assert.equal(agentLabel("austin"), "Austin");
  assert.equal(agentLabel("Tony"), "Tony");
  assert.equal(agentLabel("Duo"), "Duo");
});

test("a state payload carries the driver and process state the phone draws", () => {
  const model = viewModel({
    n: 11,
    agents: {
      austin: { connected: true, running: false, driver: "pi", model: "claude-sonnet-4-6", state: "running" },
      tony: { connected: false, running: false, driver: "", model: "", state: "" },
    },
  });
  assert.equal(model.agents[0].driver, "pi");
  assert.equal(model.agents[0].stateKnown, true);
  assert.equal(dotClass(model.agents[0]), "dot-on");
  assert.equal(agentMeta(model.agents[0]), "pi · claude-sonnet-4-6");
  assert.equal(dotClass(model.agents[1]), "dot-wait");
  assert.equal(agentMeta(model.agents[1]), "not configured");

  // An older host sends neither field: no state to judge, nothing to claim.
  const older = viewModel({ n: 12, agents: { Austin: { connected: true, running: false, model: "gpt-5" } } });
  assert.equal(older.agents[0].stateKnown, false);
  assert.equal(dotClass(older.agents[0]), "dot-on");
  assert.equal(agentMeta(older.agents[0]), "gpt-5");
});

test("a command is signed and encrypted the way the host verifies it", async () => {
  const pairingKey = parsePairingKey(VECTOR.key);
  const { encKey, macKey } = await deriveKeys(pairingKey);
  const frame = await buildCommand({
    room: VECTOR.room,
    encKey,
    macKey,
    kind: "task",
    text: "restart the build",
    nonce: 7,
    id: "cmd-1",
    randomBytes: () => new Uint8Array(12).fill(3),
  });
  assert.equal(frame.t, "cmd");
  assert.equal(frame.id, "cmd-1");

  // A command envelope binds the relay's id as additional data: room|cmd|id.
  const plain = await decryptEnvelope(encKey, VECTOR.room, {
    kind: "cmd",
    seq: frame.id,
    iv: frame.iv,
    ct: frame.ct,
  });
  assert.equal(plain.kind, "task");
  assert.equal(plain.text, "restart the build");
  assert.equal(plain.n, 7);
  assert.equal(plain.mac, VECTOR.mac);

  await assert.rejects(buildCommand({ room: VECTOR.room, encKey, macKey, kind: "reboot", nonce: 1, id: "x" }));
});

// A mode switch is a signed command whose text is the mode name: the MAC covers
// it, so the relay cannot turn "step" into "fast". The page asks before a switch
// that would discard a round, and never asks when there is nothing to lose.
test("a mode switch carries the mode name and asks before discarding a round", async () => {
  assert.ok(COMMAND_KINDS.has("switch_mode"));
  assert.equal(modeKey("STEP"), "step");
  assert.equal(modeKey(" step "), "step");
  assert.equal(modeKey("turbo"), "");
  assert.equal(modeKey(undefined), "");

  assert.equal(needsModeConfirm({ started: false, phase: "PLAN" }), false);
  assert.equal(needsModeConfirm({ started: true, phase: "DONE" }), false);
  assert.equal(needsModeConfirm({ started: true, phase: "EXECUTE" }), true);
  assert.equal(needsModeConfirm(null), false);

  const { encKey, macKey } = await deriveKeys(parsePairingKey(VECTOR.key));
  const frame = await buildCommand({
    room: VECTOR.room,
    encKey,
    macKey,
    kind: "switch_mode",
    text: "step",
    nonce: 7,
    id: "cmd-mode",
    randomBytes: () => new Uint8Array(12).fill(4),
  });
  const plain = await decryptEnvelope(encKey, VECTOR.room, { kind: "cmd", seq: frame.id, iv: frame.iv, ct: frame.ct });
  assert.equal(plain.kind, "switch_mode");
  assert.equal(plain.text, "step");
  assert.equal(plain.mac, await commandMac(macKey, "cmd-mode", "switch_mode", 7, "step"));
});

test("a sealed command does not decrypt under a message envelope", async () => {
  const { encKey } = await deriveKeys(parsePairingKey(VECTOR.key));
  const sealed = await sealCommand(encKey, VECTOR.room, "cmd-9", '{"ok":true}', () => new Uint8Array(12).fill(1));
  assert.equal(b64uEncode(new Uint8Array(12).fill(1)), sealed.iv);
  const plain = await decryptEnvelope(encKey, VECTOR.room, { kind: "cmd", seq: "cmd-9", iv: sealed.iv, ct: sealed.ct });
  assert.equal(plain.ok, true);
  await assert.rejects(
    decryptEnvelope(encKey, VECTOR.room, { kind: "state", seq: "cmd-9", iv: sealed.iv, ct: sealed.ct }),
  );
});

test("a state carries the host's answer to the last command", () => {
  const answered = viewModel({
    n: 9,
    command: { id: "cmd-1", kind: "task", accepted: false, note: "the task was empty" },
  });
  assert.equal(answered.command.id, "cmd-1");
  assert.equal(answered.command.accepted, false);
  assert.equal(answered.command.note, "the task was empty");
  assert.equal(viewModel({ n: 1 }).command, null);
});

// One command in flight, three ways the wait ends. A page that only cleared the
// pending command on a matching answer stayed disabled for good after a session
// restart or a lost frame, which reads as "the phone cannot send anything".
test("a pending command ends on its answer, a new session, or a bounded wait", () => {
  const pending = { id: "cmd-1", session: "session-1", at: 1000 };

  const accepted = settlePending(pending, { session: "session-1", command: { id: "cmd-1", accepted: true } }, 1500);
  assert.deepEqual(accepted, { pending: null, note: "Sent.", resend: false });

  // A command that did not run has to give its text back: the composer was
  // cleared when it was sent, so "send it again" without the text loses it.
  const refused = settlePending(
    pending,
    { session: "session-1", command: { id: "cmd-1", accepted: false, note: "the task was empty" } },
    1500,
  );
  assert.deepEqual(refused, { pending: null, note: "the task was empty", resend: true });

  // The host restarted: the session that would have answered is gone.
  const restarted = settlePending(pending, { session: "session-2", command: null }, 1500);
  assert.equal(restarted.pending, null);
  assert.match(restarted.note, /new session/);
  assert.equal(restarted.resend, true);

  // A state published before the command was sent is not an answer to it, and
  // must not end the wait: the answer can still arrive.
  const stale = settlePending(pending, { session: "session-1", command: null }, 1500);
  assert.equal(stale.pending, pending);
  assert.equal(stale.note, null);

  // Another command's answer belongs to an earlier attempt.
  const other = settlePending(pending, { session: "session-1", command: { id: "cmd-0", accepted: true } }, 1500);
  assert.equal(other.pending, pending);

  // A silent host ends the wait with an honest note, and nothing is resent.
  const silent = settlePending(pending, { session: "session-1" }, 1000 + PENDING_TIMEOUT_MS);
  assert.equal(silent.pending, null);
  assert.match(silent.note, /did not answer/);
  assert.equal(silent.resend, true);
  assert.equal(settlePending(pending, { session: "session-1" }, 1000 + PENDING_TIMEOUT_MS - 1).pending, pending);

  // A host that reports no session cannot trigger the restart rule.
  assert.equal(settlePending({ id: "cmd-2", at: 1000 }, { command: null }, 1500).pending.id, "cmd-2");

  assert.deepEqual(settlePending(null, { session: "session-1" }, 0), { pending: null, note: null, resend: false });
});

// The text a command that did not run gives back. An accepted command keeps the
// composer empty (it ran), and a draft the human has already started is never
// overwritten.
test("a command that did not run gives its text back to an empty composer", () => {
  const sent = { id: "cmd-1", session: "session-1", at: 1000, text: "run the tests" };

  assert.equal(resendText(sent, ""), "run the tests");
  assert.equal(resendText(sent, "   "), "run the tests", "a composer holding only spaces is empty");
  assert.equal(resendText(sent, "a new draft"), "", "a started draft must survive");
  assert.equal(resendText({ id: "cmd-2", at: 1000, text: "" }, ""), "", "a non-task command has no text to give back");
  assert.equal(resendText(null, ""), "");
});
