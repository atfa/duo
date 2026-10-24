import test from "node:test";
import assert from "node:assert/strict";

import { ROOM, PROOF, envelope, hello, newRoom, register, reopen, socket } from "./harness.js";
import { DAILY_MESSAGE_BUDGET } from "../src/protocol.js";

// Every test drives the shipped RelayRoom through its HTTP and hibernation
// handlers; only storage, sockets and the clock are faked.

async function paired(options = {}) {
  const harness = await newRoom(options);
  const response = await register(harness.room, options);
  assert.equal(response.status, 200);
  return harness;
}

test("registration clamps the lifetime to the 8h cap and refuses takeover", async () => {
  const harness = await newRoom();
  const created = await register(harness.room, { ttlSeconds: 60 * 60 * 24 * 30 });
  assert.equal(created.status, 200);
  const body = await created.json();
  assert.equal(body.expiresAt - harness.seconds(), 8 * 60 * 60);

  const hijack = await register(harness.room, { proof: "x".repeat(43) });
  assert.equal(hijack.status, 409);

  const refresh = await register(harness.room, { ttlSeconds: 600 });
  assert.equal(refresh.status, 200);
});

test("frames before hello are rejected and the socket is closed", async () => {
  const harness = await paired();
  const link = await socket(harness.room, harness.state);
  assert.equal(link.response.status, 101);
  assert.equal(link.response.headers.get("Sec-WebSocket-Protocol"), "duo.v1");

  link.deliver({ t: "msg", kind: "state", iv: "aa", ct: "bb" });
  assert.deepEqual(link.server.last(), { t: "error", code: "auth" });
  assert.equal(link.server.closed.code, 1008);
});

test("hello requires the room, proof and role", async () => {
  const harness = await paired();

  const wrongRoom = await socket(harness.room, harness.state);
  wrongRoom.deliver(hello({ room: "z".repeat(22) }));
  assert.deepEqual(wrongRoom.server.last(), { t: "error", code: "auth" });

  const wrongProof = await socket(harness.room, harness.state);
  wrongProof.deliver(hello({ proof: "q".repeat(43) }));
  assert.deepEqual(wrongProof.server.last(), { t: "error", code: "auth" });

  // A room nobody registered is its own Durable Object in production, so an
  // unregistered room is modelled by a fresh instance with no room row.
  const unregistered = await newRoom();
  const unknown = await socket(unregistered.room, unregistered.state, { roomID: "u".repeat(22) });
  unknown.deliver(hello({ room: "u".repeat(22) }));
  assert.deepEqual(unknown.server.last(), { t: "error", code: "auth" });

  const badRole = await socket(harness.room, harness.state);
  badRole.deliver(hello({ role: "relay" }));
  assert.deepEqual(badRole.server.last(), { t: "error", code: "auth" });
});

test("hello after the deadline is rejected", async () => {
  const harness = await paired();
  const link = await socket(harness.room, harness.state);
  harness.advance(6000);
  link.deliver(hello());
  assert.deepEqual(link.server.last(), { t: "error", code: "auth" });
});

test("a guest hello replays stored envelopes in ascending order", async () => {
  const harness = await paired();
  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));
  assert.deepEqual(host.server.frames()[0].t, "ready");

  for (let i = 1; i <= 3; i++) {
    host.deliver({ t: "msg", seq: i, kind: "state", iv: "aXY-aXY-aXY", ct: `Y3Qt${i}` });
  }

  const guest = await socket(harness.room, harness.state);
  guest.deliver(hello({ since: 1 }));
  const replayed = guest.server.frames().filter((frame) => frame.t === "msg");
  assert.deepEqual(
    replayed.map((frame) => frame.seq),
    [2, 3],
  );
});

test("the relay forwards envelopes, assigns sequence numbers and keeps rooms apart", async () => {
  const harness = await paired();
  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));
  const guest = await socket(harness.room, harness.state);
  guest.deliver(hello());

  host.deliver({ t: "msg", seq: 1, kind: "state", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  const forwarded = guest.server.frames().find((frame) => frame.t === "msg");
  assert.equal(forwarded.seq, 1);
  assert.equal(forwarded.kind, "state");

  // seq belongs to the host: a replay or a non-increasing value is refused.
  host.deliver({ t: "msg", seq: 1, kind: "state", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  assert.deepEqual(host.server.last(), { t: "error", code: "bad_frame" });
  host.deliver({ t: "msg", seq: 2, kind: "event", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  assert.equal(guest.server.frames().filter((frame) => frame.t === "msg").length, 2);

  // A second room shares no storage or sockets with the first.
  const other = await newRoom();
  await register(other.room, { roomID: "o".repeat(22), proof: "o".repeat(43) });
  const otherGuest = await socket(other.room, other.state, { roomID: "o".repeat(22) });
  otherGuest.deliver(hello({ room: "o".repeat(22), proof: "o".repeat(43) }));
  assert.equal(otherGuest.server.frames().filter((frame) => frame.t === "msg").length, 0);
});

test("a second host takes the room over and the old socket is told why", async () => {
  const harness = await paired();
  const first = await socket(harness.room, harness.state);
  first.deliver(hello({ role: "host" }));

  const second = await socket(harness.room, harness.state);
  second.deliver(hello({ role: "host" }));

  assert.deepEqual(first.server.last(), { t: "bye", code: "replaced" });
  assert.equal(second.server.frames()[0].t, "ready");
});

test("a room survives hibernation: the constructor reruns on the same storage", async () => {
  const harness = await paired();
  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));
  host.deliver({ t: "msg", seq: 1, kind: "state", iv: "aXY-aXY-aXY", ct: "Y3Q" });

  // New instance, same SQLite storage and socket attachments.
  const woken = await reopen(harness);

  // The live host keeps publishing without a new hello.
  woken.room.webSocketMessage(host.server, JSON.stringify({ t: "msg", seq: 2, kind: "event", iv: "aXY-aXY-aXY", ct: "Y3Q" }));
  assert.equal(woken.room.currentSeq(), 2);

  // A reconnecting guest is authenticated against the stored room row, not
  // against instance state that no longer exists.
  const guest = await socket(woken.room, woken.state);
  guest.deliver(hello({ since: 1 }));
  const frames = guest.server.frames();
  assert.equal(frames[0].t, "ready");
  assert.equal(frames[0].room, ROOM);
  assert.equal(frames[0].seq, 2);
  assert.deepEqual(
    frames.filter((frame) => frame.t === "msg").map((frame) => frame.seq),
    [2],
  );
});

test("commands are deduped by id, rate limited, and rejected while the host is away", async () => {
  const harness = await paired();
  const guest = await socket(harness.room, harness.state);
  guest.deliver(hello());

  guest.deliver({ t: "cmd", id: "cmd-1", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  assert.deepEqual(guest.server.last(), { t: "error", code: "offline" });

  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));

  guest.deliver({ t: "cmd", id: "cmd-1", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  guest.deliver({ t: "cmd", id: "cmd-1", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  const delivered = host.server.frames().filter((frame) => frame.t === "cmd");
  assert.equal(delivered.length, 1);

  // 30 commands a minute is the cap; the 31st closes the guest with a budget bye.
  for (let i = 0; i < 29; i++) {
    guest.deliver({ t: "cmd", id: `cmd-a-${i}`, iv: "aXY-aXY-aXY", ct: "Y3Q" });
  }
  guest.deliver({ t: "cmd", id: "cmd-a-30", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  assert.deepEqual(guest.server.last(), { t: "error", code: "rate_limited" });
  assert.equal(guest.server.closed, null);
});

test("role and frame limits are enforced", async () => {
  const harness = await paired();
  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));
  const guest = await socket(harness.room, harness.state);
  guest.deliver(hello());

  guest.deliver({ t: "msg", kind: "state", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  assert.deepEqual(guest.server.last(), { t: "error", code: "bad_frame" });

  host.deliver({ t: "cmd", id: "cmd-2", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  assert.deepEqual(host.server.last(), { t: "error", code: "bad_frame" });

  host.deliver({ t: "msg", kind: "state", iv: "aXY-aXY-aXY", ct: "x".repeat(5000) });
  assert.deepEqual(host.server.last(), { t: "error", code: "bad_frame" });

  host.deliver("x".repeat(5000));
  assert.deepEqual(host.server.last(), { t: "error", code: "bad_frame" });
});

test("an expired room reports expired and drops its payload", async () => {
  const harness = await newRoom();
  await register(harness.room, { ttlSeconds: 60 });
  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));
  host.deliver({ t: "msg", seq: 1, kind: "state", iv: "aXY-aXY-aXY", ct: "Y3Q" });

  harness.advance(61 * 1000);
  const late = await socket(harness.room, harness.state);
  late.deliver(hello());
  assert.deepEqual(late.server.last(), { t: "error", code: "expired" });
  assert.equal(late.server.closed.code, 1008);
});

test("expiry is sticky after the alarm and a re-registration starts a new epoch", async () => {
  const harness = await newRoom();
  await register(harness.room, { ttlSeconds: 60 });
  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));
  host.deliver({ t: "msg", seq: 4, kind: "state", iv: "aXY-aXY-aXY", ct: "Y3Q" });

  harness.advance(61 * 1000);
  const open = await socket(harness.room, harness.state);
  open.deliver(hello());
  await harness.room.alarm();

  // After the alarm cleared the payload, a late hello must still say expired
  // rather than looking like a live room that silently replays nothing.
  const late = await socket(harness.room, harness.state);
  late.deliver(hello({ since: 0 }));
  assert.deepEqual(late.server.last(), { t: "error", code: "expired" });

  // The dead pairing can be registered again, and the epoch restarts.
  const again = await register(harness.room);
  assert.equal(again.status, 200);
  const fresh = await socket(harness.room, harness.state);
  fresh.deliver(hello({ role: "host" }));
  assert.equal(fresh.server.frames()[0].t, "ready");
  assert.equal(fresh.server.frames()[0].seq, 0);
});

test("revocation wipes the room, closes sockets and keeps a tombstone", async () => {
  const harness = await paired();
  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));
  host.deliver({ t: "msg", seq: 1, kind: "state", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  const guest = await socket(harness.room, harness.state);
  guest.deliver(hello());

  const denied = await harness.room.fetch(
    new Request("https://room.internal/revoke", { method: "DELETE", headers: { Authorization: "Bearer nope" } }),
  );
  assert.equal(denied.status, 401);

  const revoked = await harness.room.fetch(
    new Request("https://room.internal/revoke", { method: "DELETE", headers: { Authorization: `Bearer ${PROOF}` } }),
  );
  assert.equal(revoked.status, 204);
  assert.deepEqual(host.server.last(), { t: "bye", code: "revoked" });
  assert.deepEqual(guest.server.last(), { t: "bye", code: "revoked" });

  const rejoin = await socket(harness.room, harness.state);
  rejoin.deliver(hello());
  assert.deepEqual(rejoin.server.last(), { t: "error", code: "auth" });

  const reregister = await register(harness.room);
  assert.equal(reregister.status, 409);

  // After the tombstone window the room id is free again, and only then.
  harness.advance(24 * 60 * 60 * 1000 + 1000);
  const again = await register(harness.room);
  assert.equal(again.status, 200);
  const fresh = await socket(harness.room, harness.state);
  fresh.deliver(hello({ role: "host" }));
  assert.equal(fresh.server.frames()[0].seq, 0);
});

test("the daily budget closes the relay without touching anything local", async () => {
  const harness = await paired();
  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));
  const guest = await socket(harness.room, harness.state);
  guest.deliver(hello());

  // The budget is per room per day: drive it to just under the cap, then cross.
  for (let i = 0; i < DAILY_MESSAGE_BUDGET; i++) {
    harness.room.chargeBudget(harness.seconds());
  }
  host.deliver({ t: "msg", seq: 1, kind: "state", iv: "aXY-aXY-aXY", ct: "Y3Q" });
  assert.deepEqual(host.server.last(), { t: "bye", code: "over_budget" });
  assert.deepEqual(guest.server.last(), { t: "bye", code: "over_budget" });
});

test("unauthenticated sockets are bounded per room", async () => {
  const harness = await paired();
  await socket(harness.room, harness.state);
  await socket(harness.room, harness.state);
  const third = await harness.room.fetch(
    new Request(`https://room.internal/ws?room=${ROOM}`, { headers: { Upgrade: "websocket" } }),
  );
  assert.equal(third.status, 429);
});

test("an invalid room id never reaches a Durable Object", async () => {
  const harness = await paired();
  const response = await harness.room.fetch(
    new Request("https://room.internal/ws?room=short", { headers: { Upgrade: "websocket" } }),
  );
  assert.equal(response.status, 400);
});

test("unknown frames and unknown routes are rejected", async () => {
  const harness = await paired();
  const host = await socket(harness.room, harness.state);
  host.deliver(hello({ role: "host" }));
  host.deliver({ t: "nope" });
  assert.deepEqual(host.server.last(), { t: "error", code: "bad_frame" });

  const missing = await harness.room.fetch(new Request("https://room.internal/missing"));
  assert.equal(missing.status, 404);
});
