import test from "node:test";
import assert from "node:assert/strict";

import { applyEvent, buildCommand, nextSeq, viewModel } from "../public/console.js";
import { b64uEncode, decryptEnvelope, deriveKeys, parsePairingKey, sealCommand } from "../public/crypto.js";
import { VECTOR } from "./vectors.js";

test("the sequence rule restarts the epoch on a recreated room", () => {
  assert.equal(nextSeq(0, 1), 1);
  assert.equal(nextSeq(4, 5), 5);
  assert.equal(nextSeq(7, 2), 2);
  assert.equal(nextSeq(3, 0), 3);
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
  assert.equal(applyEvent(base, { kind: "observer", text: "report text" }).observer.report, "report text");
  assert.equal(applyEvent(base, { kind: "from-the-future", text: "x" }), base);
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
