#!/usr/bin/env node
// End-to-end check of the real relay: it talks to a running `wrangler dev
// --local` over actual HTTP and WebSocket, so auth, routing, the Durable Object
// and the hibernation handlers are all exercised together.
//
// It is intentionally not part of `node --test`: it needs a Worker runtime.
// scripts/remote-e2e.sh starts one and runs this file.

import { randomBytes } from "node:crypto";

const base = (process.env.DUO_RELAY_BASE ?? "http://127.0.0.1:8787").replace(/\/$/, "");
const wsBase = base.replace(/^http/, "ws");

const room = randomBytes(16).toString("base64url");
const proof = randomBytes(32).toString("base64url");
const peerProof = randomBytes(32).toString("base64url");

const failures = [];
const note = (message) => console.log(`ok - ${message}`);
function check(condition, message) {
  if (condition) note(message);
  else {
    failures.push(message);
    console.error(`not ok - ${message}`);
  }
}

function connect(role, helloOverrides = {}) {
  const socket = new WebSocket(`${wsBase}/ws?room=${room}`, ["duo.v1"]);
  const frames = [];
  let resolveReady;
  let rejectReady;
  const ready = new Promise((resolve, reject) => {
    resolveReady = resolve;
    rejectReady = reject;
  });
  const timer = setTimeout(() => rejectReady(new Error(`${role}: timeout waiting for ready`)), 10000);
  socket.onmessage = (event) => {
    const frame = JSON.parse(event.data);
    frames.push(frame);
    if (frame.t === "ready") {
      clearTimeout(timer);
      resolveReady(frame);
    }
    if (frame.t === "error" && frame.code === "auth") {
      clearTimeout(timer);
      rejectReady(new Error(`${role}: auth rejected`));
    }
  };
  socket.onerror = () => rejectReady(new Error(`${role}: socket error`));
  const opened = new Promise((resolve) => (socket.onopen = resolve));
  return {
    socket,
    frames,
    ready,
    async hello(overrides = {}) {
      await opened;
      socket.send(JSON.stringify({ t: "hello", v: 1, role, room, proof, since: 0, ...helloOverrides, ...overrides }));
      return ready;
    },
    closed() {
      return new Promise((resolve) => {
        if (socket.readyState === WebSocket.CLOSED) return resolve();
        socket.onclose = () => resolve();
        setTimeout(resolve, 5000);
      });
    },
  };
}

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// Frames arrive asynchronously; poll instead of guessing a sleep, so a slow
// first request (cold Worker) cannot make a check flaky.
async function waitFor(frames, predicate, label, timeout = 8000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const found = frames.find(predicate);
    if (found) return found;
    await wait(50);
  }
  console.error(`timed out waiting for ${label}`);
  return null;
}

async function main() {
  const health = await fetch(`${base}/healthz`);
  check(health.status === 200 && (await health.text()) === "ok", "GET /healthz answers ok");

  const page = await fetch(`${base}/p/${room}`);
  const html = await page.text();
  check(page.status === 200 && html.includes("DUO remote"), "GET /p/<room> serves the pairing page");
  check(!/<script[^>]+https?:/i.test(html) && !/<link[^>]+https?:/i.test(html), "the pairing page requests no external asset");

  const register = await fetch(`${base}/api/room?room=${room}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "CF-Connecting-IP": "203.0.113.42" },
    body: JSON.stringify({ proof, ttlSeconds: 600 }),
  });
  const registered = await register.json();
  check(register.status === 200 && registered.room === room, "registration creates the room");
  check(registered.expiresAt > 0, "registration returns an expiry");

  const squatter = await fetch(`${base}/api/room?room=${room}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ proof: peerProof, ttlSeconds: 600 }),
  });
  check(squatter.status === 409, "a second registration without the pairing proof is refused");

  const imposter = connect("host", { proof: peerProof });
  let imposterRejected = false;
  try {
    await imposter.hello({ proof: peerProof });
  } catch {
    imposterRejected = true;
  }
  check(imposterRejected, "a hello with the wrong proof is rejected");
  imposter.socket.close();

  const host = connect("host");
  const hostReady = await host.hello();
  check(hostReady.seq === 0, "a fresh room starts at seq 0");

  const guest = connect("guest");
  const guestReady = await guest.hello();
  check(guestReady.room === room, "the guest joins the registered room");

  check(
    (await waitFor(guest.frames, (frame) => frame.t === "presence" && frame.host === true, "presence")) !== null,
    "the guest is told the host is present",
  );

  const iv = randomBytes(12).toString("base64url");
  const ct = randomBytes(48).toString("base64url");
  host.socket.send(JSON.stringify({ t: "msg", seq: 1, kind: "state", iv, ct }));
  const forwarded = await waitFor(guest.frames, (frame) => frame.t === "msg", "relayed envelope");
  check(forwarded && forwarded.seq === 1 && forwarded.iv === iv && forwarded.ct === ct, "an encrypted envelope is relayed unchanged");

  host.socket.send(JSON.stringify({ t: "msg", seq: 1, kind: "state", iv, ct }));
  check(
    (await waitFor(host.frames, (frame) => frame.t === "error" && frame.code === "bad_frame", "bad seq")) !== null,
    "a non-increasing seq is refused",
  );

  const reconnected = connect("guest");
  await reconnected.hello();
  // A reconnect asks for everything it has not seen; since 0 replays the room.
  check(
    (await waitFor(reconnected.frames, (frame) => frame.t === "msg" && frame.seq === 1, "replayed envelope")) !== null,
    "a reconnecting guest replays stored envelopes",
  );
  reconnected.socket.close();

  const before = host.frames.filter((frame) => frame.t === "cmd").length;
  const command = { t: "cmd", id: "e2e-command-1", iv, ct };
  guest.socket.send(JSON.stringify(command));
  guest.socket.send(JSON.stringify(command));
  await waitFor(host.frames, (frame) => frame.t === "cmd", "forwarded command");
  await wait(500); // give a duplicate time to arrive if dedupe failed
  const after = host.frames.filter((frame) => frame.t === "cmd").length;
  check(after - before === 1, "a duplicated command id reaches the host once");

  const revoke = await fetch(`${base}/api/room/${room}`, {
    method: "DELETE",
    headers: { Authorization: `Bearer ${proof}` },
  });
  check(revoke.status === 204, "revocation succeeds");
  const hostBye = await waitFor(host.frames, (frame) => frame.t === "bye" && frame.code === "revoked", "host bye");
  const guestBye = await waitFor(guest.frames, (frame) => frame.t === "bye" && frame.code === "revoked", "guest bye");
  await Promise.all([host.closed(), guest.closed()]);
  check(hostBye !== null && guestBye !== null, "both sockets are told the room was revoked");

  const afterRevoke = connect("guest");
  let revokedRejected = false;
  try {
    await afterRevoke.hello();
  } catch {
    revokedRejected = true;
  }
  check(revokedRejected, "a revoked room rejects new hellos");
  afterRevoke.socket.close();

  const reregister = await fetch(`${base}/api/room?room=${room}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ proof, ttlSeconds: 600 }),
  });
  check(reregister.status === 409, "a revoked room cannot be re-registered during the tombstone");

  if (failures.length > 0) {
    console.error(`\n${failures.length} end-to-end check(s) failed`);
    process.exit(1);
  }
  console.log("\nrelay end-to-end checks passed");
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
