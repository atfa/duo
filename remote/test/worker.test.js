import test from "node:test";
import assert from "node:assert/strict";

import worker from "../src/index.js";
import { newRegistrar } from "./harness.js";

const ROOM = "r".repeat(22);
const PROOF = "p".repeat(43);

// The Worker is stateless: these stubs assert the routing contract, while the
// room and registrar behaviour itself is covered against the real classes.
function env({ registrar } = {}) {
  const calls = [];
  const assets = [];
  const limiter = registrar ?? {
    fetch: async () => new Response(JSON.stringify({ ok: true }), { status: 200 }),
  };
  return {
    calls,
    assets,
    env: {
      ROOMS: {
        idFromName: (name) => ({ name }),
        get: (id) => ({
          fetch: async (request) => {
            calls.push({ room: id.name, url: request.url, method: request.method, auth: request.headers.get("Authorization") });
            return new Response(JSON.stringify({ ok: true }), { status: 200 });
          },
        }),
      },
      REGISTRAR: {
        idFromName: () => ({ name: "global" }),
        get: () => limiter,
      },
      ASSETS: {
        fetch: async (request) => {
          assets.push(new URL(request.url).pathname);
          return new Response("asset", { status: 200 });
        },
      },
    },
  };
}

function post(body, headers = {}) {
  return new Request("https://remote.xduo.dev/api/room", {
    method: "POST",
    headers: { "Content-Type": "application/json", ...headers },
    body: JSON.stringify(body),
  });
}

test("healthz answers without touching a Durable Object", async () => {
  const fake = env();
  const response = await worker.fetch(new Request("https://remote.xduo.dev/healthz"), fake.env);
  assert.equal(response.status, 200);
  assert.equal(await response.text(), "ok");
  assert.equal(fake.calls.length, 0);
});

test("websocket upgrades are validated before reaching a room", async () => {
  const fake = env();
  const plain = await worker.fetch(new Request("https://remote.xduo.dev/ws"), fake.env);
  assert.equal(plain.status, 426);

  const badRoom = await worker.fetch(
    new Request("https://remote.xduo.dev/ws?room=short", { headers: { Upgrade: "websocket" } }),
    fake.env,
  );
  assert.equal(badRoom.status, 400);

  const ok = await worker.fetch(
    new Request(`https://remote.xduo.dev/ws?room=${ROOM}`, { headers: { Upgrade: "websocket" } }),
    fake.env,
  );
  assert.equal(ok.status, 200);
  assert.equal(fake.calls[0].room, ROOM);
});

test("registration validates the room and proof, then forwards to that room only", async () => {
  const fake = env();
  assert.equal((await worker.fetch(post({ room: "short", proof: PROOF }), fake.env)).status, 400);
  assert.equal((await worker.fetch(post({ room: ROOM, proof: "!".repeat(43) }), fake.env)).status, 400);
  assert.equal(fake.calls.length, 0);

  const response = await worker.fetch(
    post({ room: ROOM, proof: PROOF, ttlSeconds: 60 }, { "CF-Connecting-IP": "203.0.113.9" }),
    fake.env,
  );
  assert.equal(response.status, 200);
  assert.equal(fake.calls[0].room, ROOM);
  assert.equal(new URL(fake.calls[0].url).pathname, "/register");
  assert.equal(new URL(fake.calls[0].url).searchParams.get("room"), ROOM);

  // The Go client puts the room in the query string instead of the body.
  const viaQuery = await worker.fetch(
    new Request(`https://remote.xduo.dev/api/room?room=${ROOM}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ proof: PROOF, ttlSeconds: 60 }),
    }),
    fake.env,
  );
  assert.equal(viaQuery.status, 200);
  assert.equal(fake.calls[1].room, ROOM);
});

test("a rate-limited registrar refuses with 429 and never reaches the room", async () => {
  const fake = env({ registrar: { fetch: async () => new Response(JSON.stringify({ ok: false }), { status: 429 }) } });
  const response = await worker.fetch(post({ room: ROOM, proof: PROOF }), fake.env);
  assert.equal(response.status, 429);
  assert.equal(fake.calls.length, 0);
});

test("a body over the small registration limit is refused", async () => {
  const fake = env();
  const oversized = await worker.fetch(
    post({ room: ROOM, proof: PROOF, padding: "x".repeat(4096) }),
    fake.env,
  );
  assert.equal(oversized.status, 413);
  assert.equal(fake.calls.length, 0);
});

test("revocation rewrites the path and preserves the bearer proof", async () => {
  const fake = env();
  const missing = await worker.fetch(new Request(`https://remote.xduo.dev/api/room/${ROOM}`), fake.env);
  assert.equal(missing.status, 405);

  const response = await worker.fetch(
    new Request(`https://remote.xduo.dev/api/room/${ROOM}`, {
      method: "DELETE",
      headers: { Authorization: `Bearer ${PROOF}` },
    }),
    fake.env,
  );
  assert.equal(response.status, 200);
  assert.equal(fake.calls[0].room, ROOM);
  assert.equal(fake.calls[0].method, "DELETE");
  assert.equal(fake.calls[0].auth, `Bearer ${PROOF}`);
  assert.equal(new URL(fake.calls[0].url).pathname, "/revoke");
});

test("the pairing page is served by static assets for any room", async () => {
  const fake = env();
  const response = await worker.fetch(new Request(`https://remote.xduo.dev/p/${ROOM}`), fake.env);
  assert.equal(await response.text(), "asset");
  assert.deepEqual(fake.assets, [`/p/${ROOM}`]);
});

test("the registrar enforces per-IP and global hourly caps", async () => {
  const { registrar, advance } = await newRegistrar();
  const ask = (ip) =>
    registrar.fetch(
      new Request("https://registrar.internal/check", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ip }),
      }),
    );

  for (let i = 0; i < 30; i++) assert.equal((await ask("203.0.113.7")).status, 200);
  assert.equal((await ask("203.0.113.7")).status, 429);
  assert.equal((await ask("203.0.113.8")).status, 200);

  advance(2 * 60 * 60 * 1000);
  assert.equal((await ask("203.0.113.7")).status, 200);
});
