#!/usr/bin/env node
// The page's own code, driven as a guest against a running relay.
//
// console.js and crypto.js are DOM-free, so this file can import the exact
// modules the phone runs — pairing derivation, envelope decryption, sequence
// rule and command signing — and exercise them against a real Worker and a real
// host. It is the cross-implementation check: the page's bytes, not a
// reimplementation of them.
//
//   node remote/e2e/page.mjs <base> <room> <key> <task text>
//
// Prints one line per step and exits non-zero on any failure.

import { decryptEnvelope, deriveKeys, parsePairingKey } from "../public/crypto.js";
import { buildCommand, nextSeq, viewModel } from "../public/console.js";

const [base, room, key, taskText] = process.argv.slice(2);
if (!base || !room || !key) {
  console.error("usage: page.mjs <base> <room> <key> [task]");
  process.exit(2);
}

const note = (message) => console.log(`ok - ${message}`);
function check(condition, message) {
  if (condition) note(message);
  else {
    console.error(`not ok - ${message}`);
    process.exit(1);
  }
}

const pairingKey = parsePairingKey(`#${key}`);
check(pairingKey.length === 32, "the pairing key is 32 bytes");
const keys = await deriveKeys(pairingKey);
check(/^[A-Za-z0-9_-]{43}$/.test(keys.proof), "the relay proof is derived");

const wsBase = base.replace(/^http/, "ws");
const socket = new WebSocket(`${wsBase}/ws?room=${room}`, ["duo.v1"]);
const deadline = setTimeout(() => {
  console.error("not ok - timeout");
  process.exit(1);
}, 20000);

let lastSeq = 0;
let answered = false;

socket.onmessage = async (event) => {
  let frame;
  try {
    frame = JSON.parse(event.data);
  } catch {
    console.error("not ok - the relay sent a frame that is not JSON");
    process.exit(1);
  }
  if (!frame || typeof frame !== "object") return;
  if (frame.t === "ready") {
    lastSeq = nextSeq(0, Number(frame.seq) || 0);
    note(`joined the room at seq ${lastSeq}`);
    return;
  }
  if (frame.t === "presence") {
    check(frame.host === true, "the host is present");
    return;
  }
  if (frame.t === "error") {
    console.error(`not ok - relay error ${frame.code}`);
    process.exit(1);
  }
  if (frame.t !== "msg" || frame.kind !== "state") return;

  lastSeq = nextSeq(lastSeq, frame.seq);
  const state = await decryptEnvelope(keys.encKey, room, frame);
  const model = viewModel(state);
  check(model.project !== "", "the state decrypted and rendered");
  socket.send(JSON.stringify({ t: "ack", seq: frame.seq }));

  if (!model.nonce) {
    console.error("not ok - the state carried no command nonce");
    process.exit(1);
  }

  if (state.command && taskText && !answered) {
    answered = true;
    check(state.command.accepted === true, `the host accepted the command (${state.command.note || "no note"})`);
    clearTimeout(deadline);
    socket.close();
    process.exit(0);
  }

  if (!taskText) {
    clearTimeout(deadline);
    socket.close();
    process.exit(0);
  }

  const id = crypto.randomUUID();
  const command = await buildCommand({
    room,
    encKey: keys.encKey,
    macKey: keys.macKey,
    kind: "task",
    text: taskText,
    nonce: model.nonce,
    id,
  });
  socket.send(JSON.stringify(command));
  note(`sent a signed task command with nonce ${model.nonce}`);
};

await new Promise((resolve, reject) => {
  socket.onopen = resolve;
  socket.onerror = () => reject(new Error("socket error"));
});
socket.send(JSON.stringify({ t: "hello", v: 1, role: "guest", room, proof: keys.proof, since: 0 }));
note("said hello as a guest");
