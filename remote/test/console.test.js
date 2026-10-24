import test from "node:test";
import assert from "node:assert/strict";

import { agentId, agentLabel, agentMeta, applyEvent, buildCommand, dotClass, nextSeq, viewModel } from "../public/console.js";
import { b64uEncode, decryptEnvelope, deriveKeys, parsePairingKey, sealCommand } from "../public/crypto.js";
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
