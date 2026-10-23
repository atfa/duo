// Worker entry point: route the control-plane and WebSocket traffic to the
// room Durable Object and hand everything else to the static assets, which is
// how /p/<room> serves the same room-independent page for every pairing.

import { MAX_BODY_BYTES, validProof, validRoom } from "./protocol.js";

// Durable Object classes must be exported from the Worker entry point.
export { RelayRoom } from "./room.js";
export { Registrar } from "./registrar.js";

export default {
  async fetch(request, env) {
    let url;
    try {
      url = new URL(request.url);
    } catch {
      return json({ error: "bad_request" }, 400);
    }

    if (url.pathname === "/healthz") {
      return new Response("ok", { status: 200, headers: { "Content-Type": "text/plain; charset=utf-8" } });
    }
    if (url.pathname === "/ws") return websocket(request, env, url);
    if (url.pathname === "/api/room") return createRoom(request, env, url);
    if (url.pathname.startsWith("/api/room/")) return revokeRoom(request, env, url);
    return env.ASSETS.fetch(request);
  },
};

async function websocket(request, env, url) {
  if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") return json({ error: "upgrade" }, 426);
  const room = url.searchParams.get("room") ?? "";
  if (!validRoom(room)) return json({ error: "bad_room" }, 400);
  return env.ROOMS.get(env.ROOMS.idFromName(room)).fetch(request);
}

async function createRoom(request, env, url) {
  if (request.method !== "POST") return json({ error: "method" }, 405);
  const raw = await limitedBody(request);
  if (raw === null) return json({ error: "bad_body" }, 413);

  let body;
  try {
    body = JSON.parse(raw);
  } catch {
    return json({ error: "bad_body" }, 400);
  }
  const room = url.searchParams.get("room") ?? body?.room;
  if (!validRoom(room)) return json({ error: "bad_room" }, 400);
  if (!validProof(body?.proof)) return json({ error: "bad_proof" }, 400);

  // Registration is the only endpoint that can allocate storage, so it is also
  // the only one that needs a rate limit. The IP never reaches the room.
  const ip = request.headers.get("CF-Connecting-IP") ?? "unknown";
  const limited = await env.REGISTRAR.get(env.REGISTRAR.idFromName("global")).fetch(
    new Request("https://registrar.internal/check", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ip }),
    }),
  );
  if (!limited.ok) return json({ error: "rate_limited" }, 429);

  const target = env.ROOMS.get(env.ROOMS.idFromName(room));
  return target.fetch(
    new Request(`https://room.internal/register?room=${encodeURIComponent(room)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ proof: body.proof, ttlSeconds: body.ttlSeconds }),
    }),
  );
}

async function revokeRoom(request, env, url) {
  if (request.method !== "DELETE") return json({ error: "method" }, 405);
  const room = url.pathname.slice("/api/room/".length);
  if (!validRoom(room)) return json({ error: "bad_room" }, 400);
  const authorization = request.headers.get("Authorization") ?? "";
  return env.ROOMS.get(env.ROOMS.idFromName(room)).fetch(
    new Request("https://room.internal/revoke", {
      method: "DELETE",
      headers: { Authorization: authorization },
    }),
  );
}

async function limitedBody(request) {
  const declared = Number(request.headers.get("Content-Length") ?? "0");
  if (Number.isFinite(declared) && declared > MAX_BODY_BYTES) return null;
  const text = await request.text();
  return text.length > MAX_BODY_BYTES ? null : text;
}

function json(value, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json", "Cache-Control": "no-store" },
  });
}
