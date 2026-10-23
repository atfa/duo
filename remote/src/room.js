// RelayRoom is the whole relay for one pairing: a SQLite-backed Durable Object
// that stores opaque envelopes for replay, enforces the free-tier caps, and
// hibernates while nobody is talking. It never sees plaintext, a key, or a
// command body, and it never logs a frame.

import {
  DAILY_MESSAGE_BUDGET,
  FRAME_LIMIT,
  GUEST_CMD_PER_MINUTE,
  HELLO_DEADLINE_MS,
  KEEP_COMMANDS,
  KEEP_ENVELOPES,
  KINDS,
  MAX_CMD_ID_LEN,
  MAX_SOCKETS,
  MAX_UNAUTH_SOCKETS,
  ROOM_TTL_SECONDS,
  TOMBSTONE_SECONDS,
  VERSION,
  byeFrame,
  clampTTL,
  decodeFrame,
  encodeFrame,
  errorFrame,
  frameFits,
  isSeq,
  presenceFrame,
  readyFrame,
  timingSafeEqual,
  validB64URL,
  validProof,
  validRoom,
} from "./protocol.js";

const schema = [
  `CREATE TABLE IF NOT EXISTS room (
     id TEXT PRIMARY KEY,
     proof TEXT,
     created_at INTEGER NOT NULL,
     expires_at INTEGER NOT NULL,
     revoked_at INTEGER,
     expired_at INTEGER
   )`,
  `CREATE TABLE IF NOT EXISTS envelopes (
     seq INTEGER PRIMARY KEY,
     kind TEXT NOT NULL,
     iv TEXT NOT NULL,
     ct TEXT NOT NULL,
     at INTEGER NOT NULL
   )`,
  `CREATE TABLE IF NOT EXISTS commands (id TEXT PRIMARY KEY, at INTEGER NOT NULL)`,
  `CREATE TABLE IF NOT EXISTS cmd_times (at INTEGER NOT NULL)`,
  `CREATE TABLE IF NOT EXISTS budget (day TEXT PRIMARY KEY, messages INTEGER NOT NULL)`,
  `CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
];

export class RelayRoom {
  constructor(state) {
    this.state = state;
    this.sql = state.storage.sql;
    // Overridable so tests can move the clock without sleeping.
    this.nowMs = () => Date.now();
    for (const statement of schema) this.sql.exec(statement);
    // Rows created by an earlier build predate expired_at. Adding it lazily is
    // the whole migration: SQLite's IF NOT EXISTS only covers tables, not
    // columns.
    try {
      this.sql.exec("ALTER TABLE room ADD COLUMN expired_at INTEGER");
    } catch (error) {
      if (!String(error).includes("duplicate column")) throw error;
    }
  }

  // --- helpers -------------------------------------------------------------

  all(query, ...params) {
    return this.sql.exec(query, ...params).toArray();
  }

  first(query, ...params) {
    const rows = this.all(query, ...params);
    return rows.length > 0 ? rows[0] : null;
  }

  nowSeconds() {
    return Math.floor(this.nowMs() / 1000);
  }

  roomRow() {
    return this.first("SELECT * FROM room LIMIT 1");
  }

  setSeq(seq) {
    this.sql.exec("INSERT INTO meta (k, v) VALUES ('seq', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v", String(seq));
  }

  currentSeq() {
    return Number(this.first("SELECT v FROM meta WHERE k = 'seq'")?.v ?? 0);
  }

  budgetUsed(day) {
    return Number(this.first("SELECT messages FROM budget WHERE day = ?", day)?.messages ?? 0);
  }

  chargeBudget(now) {
    const day = new Date(now * 1000).toISOString().slice(0, 10);
    const used = this.budgetUsed(day) + 1;
    this.sql.exec(
      "INSERT INTO budget (day, messages) VALUES (?, ?) ON CONFLICT(day) DO UPDATE SET messages = excluded.messages",
      day,
      used,
    );
    return used;
  }

  scheduleAlarm() {
    const row = this.roomRow();
    const candidates = [];
    if (row) {
      if (row.revoked_at) candidates.push(row.revoked_at + TOMBSTONE_SECONDS);
      if (row.expired_at) candidates.push(row.expired_at + TOMBSTONE_SECONDS);
      if (!row.revoked_at && !row.expired_at) candidates.push(row.expires_at);
    }
    if (candidates.length === 0) return;
    const at = Math.min(...candidates);
    this.state.storage.setAlarm(Math.max(at * 1000, this.nowMs() + 1000));
  }

  authedSockets() {
    const sockets = this.state.getWebSockets();
    const out = [];
    for (const ws of sockets) {
      const attachment = ws.deserializeAttachment();
      if (attachment?.authed) out.push({ ws, role: attachment.role });
    }
    return out;
  }

  hostSockets() {
    return this.authedSockets().filter((s) => s.role === "host").map((s) => s.ws);
  }

  guestSockets() {
    return this.authedSockets().filter((s) => s.role === "guest").map((s) => s.ws);
  }

  send(ws, text) {
    try {
      ws.send(text);
    } catch {
      // A closed socket is not an error worth propagating: presence and the
      // client's own reconnect already cover it.
    }
  }

  // reject is for authentication-class failures: the frame is answered and the
  // socket is closed, because nothing it sends can be trusted.
  reject(ws, code) {
    this.send(ws, errorFrame(code));
    try {
      ws.close(1008, code);
    } catch {
      // already closed
    }
  }

  // badFrame is non-terminal on purpose: a live client that sends one malformed
  // frame (a stale seq, an oversized payload) keeps its socket and can recover.
  badFrame(ws) {
    this.send(ws, errorFrame("bad_frame"));
  }

  sendPresence() {
    const hosts = this.hostSockets().length;
    const guests = this.guestSockets().length;
    const frame = presenceFrame(hosts > 0, guests);
    for (const { ws } of this.authedSockets()) this.send(ws, frame);
  }

  closeAll(code) {
    for (const ws of this.state.getWebSockets()) {
      this.send(ws, byeFrame(code));
      try {
        ws.close(1000, code);
      } catch {
        // already closed
      }
    }
  }

  // --- HTTP -----------------------------------------------------------------

  async fetch(request) {
    let url;
    try {
      url = new URL(request.url);
    } catch {
      return new Response("bad request", { status: 400 });
    }
    switch (url.pathname) {
      case "/register":
        return this.register(request, url.searchParams.get("room") ?? "");
      case "/revoke":
        return this.revoke(request);
      case "/ws":
        return this.upgrade(url.searchParams.get("room") ?? "");
      default:
        return new Response("not found", { status: 404 });
    }
  }

  // wipeRoom removes every trace of a room, including the sequence counter, so
  // a re-registered room id starts a fresh epoch instead of reusing an old seq.
  wipeRoom() {
    this.sql.exec("DELETE FROM room");
    this.sql.exec("DELETE FROM envelopes");
    this.sql.exec("DELETE FROM commands");
    this.sql.exec("DELETE FROM cmd_times");
    this.sql.exec("DELETE FROM meta WHERE k = 'seq'");
  }

  async register(request, room) {
    if (request.method !== "POST") return json({ error: "method" }, 405);
    if (!validRoom(room)) return json({ error: "bad_room" }, 400);
    this.roomID = room;
    let body;
    try {
      body = await request.json();
    } catch {
      return json({ error: "bad_body" }, 400);
    }
    const proof = body?.proof;
    if (!validProof(proof)) return json({ error: "bad_proof" }, 400);

    const now = this.nowSeconds();
    const expiresAt = clampTTL(body?.ttlSeconds, now);
    const existing = this.roomRow();
    if (existing) {
      if (existing.revoked_at) {
        if (existing.revoked_at + TOMBSTONE_SECONDS > now) return json({ error: "revoked" }, 409);
        this.wipeRoom();
      } else if (existing.expired_at || existing.expires_at <= now) {
        // An expired pairing is dead: its payload is already gone, so a fresh
        // registration starts a new epoch on the same (unguessable) room id.
        this.wipeRoom();
      } else {
        // First writer wins: only the pairing that created the room may refresh
        // it, so a leaked room id cannot be taken over.
        if (!timingSafeEqual(existing.proof, proof)) return json({ error: "exists" }, 409);
        this.sql.exec("UPDATE room SET expires_at = ?", expiresAt);
        this.scheduleAlarm();
        return json({ room: existing.id, expiresAt });
      }
    }

    this.sql.exec(
      "INSERT INTO room (id, proof, created_at, expires_at, revoked_at, expired_at) VALUES (?, ?, ?, ?, NULL, NULL)",
      this.roomID,
      proof,
      now,
      expiresAt,
    );
    this.scheduleAlarm();
    return json({ room: this.roomID, expiresAt });
  }

  async revoke(request) {
    if (request.method !== "DELETE" && request.method !== "POST") return json({ error: "method" }, 405);
    const row = this.roomRow();
    if (!row) return new Response(null, { status: 204 });
    const header = request.headers.get("Authorization") ?? "";
    const proof = header.startsWith("Bearer ") ? header.slice(7) : "";
    if (!timingSafeEqual(row.proof ?? "", proof)) return json({ error: "auth" }, 401);

    const now = this.nowSeconds();
    this.sql.exec("UPDATE room SET proof = NULL, revoked_at = ?", now);
    this.sql.exec("DELETE FROM envelopes");
    this.sql.exec("DELETE FROM commands");
    this.sql.exec("DELETE FROM cmd_times");
    this.closeAll("revoked");
    this.scheduleAlarm();
    return new Response(null, { status: 204 });
  }

  upgrade(room) {
    if (!validRoom(room)) return new Response("bad room", { status: 400 });
    this.roomID = room;

    const unauth = this.state
      .getWebSockets()
      .filter((ws) => !ws.deserializeAttachment()?.authed).length;
    if (unauth >= MAX_UNAUTH_SOCKETS) return new Response("busy", { status: 429 });

    const pair = new WebSocketPair();
    const [server, client] = Object.values(pair);
    this.state.acceptWebSocket(server);
    server.serializeAttachment({ authed: false, at: this.nowMs() });
    return new Response(null, {
      status: 101,
      webSocket: client,
      headers: { "Sec-WebSocket-Protocol": "duo.v1" },
    });
  }

  // --- WebSocket (hibernation) handlers -------------------------------------

  webSocketMessage(ws, message) {
    const frame = typeof message === "string" && frameFits(message) ? decodeFrame(message) : null;
    if (!frame) return this.badFrame(ws);

    const attachment = ws.deserializeAttachment() ?? {};
    if (!attachment.authed) {
      if (frame.t !== "hello") return this.reject(ws, "auth");
      return this.handleHello(ws, attachment, frame);
    }

    switch (frame.t) {
      case "msg":
        return this.handleEnvelope(ws, attachment, frame);
      case "cmd":
        return this.handleCommand(ws, attachment, frame);
      case "ack":
        return this.handleAck(ws, frame);
      case "ping":
        return this.send(ws, encodeFrame({ t: "pong" }));
      default:
        return this.badFrame(ws);
    }
  }

  handleHello(ws, attachment, frame) {
    const now = this.nowSeconds();
    if (this.nowMs() - attachment.at > HELLO_DEADLINE_MS) return this.reject(ws, "auth");
    if (frame.v !== VERSION || (frame.role !== "host" && frame.role !== "guest")) {
      return this.reject(ws, "auth");
    }
    if (!validProof(frame.proof)) return this.reject(ws, "auth");

    // The room id comes from the stored row, not from this instance: after a
    // hibernation eviction the constructor runs again and only storage and the
    // sockets' attachments survive.
    const row = this.roomRow();
    if (!row || row.revoked_at || frame.room !== row.id) return this.reject(ws, "auth");
    if (row.expired_at || row.expires_at <= now) {
      this.send(ws, errorFrame("expired"));
      try {
        ws.close(1008, "expired");
      } catch {
        // already closed
      }
      return;
    }
    if (!timingSafeEqual(row.proof ?? "", frame.proof)) return this.reject(ws, "auth");

    if (frame.role === "host") {
      // Host takeover is how /new keeps the room: the newest host socket is the
      // live one and the previous process is told why it lost the room.
      for (const other of this.hostSockets()) {
        if (other === ws) continue;
        this.send(other, byeFrame("replaced"));
        try {
          other.close(1000, "replaced");
        } catch {
          // already closed
        }
      }
    } else if (this.state.getWebSockets().length > MAX_SOCKETS) {
      return this.reject(ws, "room_full");
    }

    ws.serializeAttachment({ authed: true, role: frame.role, at: this.nowMs() });
    this.send(ws, readyFrame(row.id, this.currentSeq(), row.expires_at));

    const since = isSeq(frame.since) ? frame.since : 0;
    const replay = this.all(
      "SELECT seq, kind, iv, ct FROM envelopes WHERE seq > ? ORDER BY seq ASC LIMIT ?",
      since,
      KEEP_ENVELOPES,
    );
    for (const envelope of replay) {
      this.send(
        ws,
        encodeFrame({ t: "msg", seq: envelope.seq, kind: envelope.kind, iv: envelope.iv, ct: envelope.ct }),
      );
    }
    this.sendPresence();
  }

  handleEnvelope(ws, attachment, frame) {
    if (attachment.role !== "host") return this.badFrame(ws);
    // The host owns seq because it is inside the AEAD additional data. The
    // relay only insists that it moves forward, so a replay cannot be inserted
    // under an older number.
    if (!isSeq(frame.seq) || frame.seq <= this.currentSeq()) return this.badFrame(ws);
    if (!KINDS.has(frame.kind) || !validB64URL(frame.iv, 64) || !validB64URL(frame.ct, FRAME_LIMIT)) {
      return this.badFrame(ws);
    }
    const now = this.nowSeconds();
    if (this.chargeBudget(now) > DAILY_MESSAGE_BUDGET) return this.closeAll("over_budget");

    const seq = frame.seq;
    this.setSeq(seq);
    this.sql.exec(
      "INSERT INTO envelopes (seq, kind, iv, ct, at) VALUES (?, ?, ?, ?, ?)",
      seq,
      frame.kind,
      frame.iv,
      frame.ct,
      now,
    );
    // Keep a bounded replay window: the newest KEEP_ENVELOPES are all a page
    // needs to recover from a dropped connection.
    this.sql.exec(
      "DELETE FROM envelopes WHERE seq <= ? AND seq NOT IN (SELECT seq FROM envelopes ORDER BY seq DESC LIMIT ?)",
      seq - 1,
      KEEP_ENVELOPES,
    );
    this.sql.exec("DELETE FROM envelopes WHERE at <= ?", now - 3600);

    const outgoing = encodeFrame({ t: "msg", seq, kind: frame.kind, iv: frame.iv, ct: frame.ct });
    for (const guest of this.guestSockets()) this.send(guest, outgoing);
  }

  handleCommand(ws, attachment, frame) {
    if (attachment.role !== "guest") return this.badFrame(ws);
    if (typeof frame.id !== "string" || frame.id.length === 0 || frame.id.length > MAX_CMD_ID_LEN) {
      return this.badFrame(ws);
    }
    if (!validB64URL(frame.iv, 64) || !validB64URL(frame.ct, FRAME_LIMIT)) {
      return this.badFrame(ws);
    }

    const now = this.nowSeconds();
    // Duplicate command protection across reconnects: an id the relay has
    // already forwarded is dropped, so a retry after a lost ack is harmless.
    if (this.first("SELECT id FROM commands WHERE id = ?", frame.id)) return;

    const hosts = this.hostSockets();
    if (hosts.length === 0) {
      // Not recorded: the page may retry this exact id once the host is back.
      return this.send(ws, errorFrame("offline"));
    }

    this.sql.exec("DELETE FROM cmd_times WHERE at <= ?", now - 60);
    const recent = Number(this.first("SELECT COUNT(*) AS n FROM cmd_times")?.n ?? 0);
    if (recent >= GUEST_CMD_PER_MINUTE) {
      // Non-terminal on purpose: the Go client and the page back off, and the
      // command is simply not forwarded.
      return this.send(ws, errorFrame("rate_limited"));
    }
    if (this.chargeBudget(now) > DAILY_MESSAGE_BUDGET) return this.closeAll("over_budget");

    this.sql.exec("INSERT INTO commands (id, at) VALUES (?, ?)", frame.id, now);
    this.sql.exec(
      "DELETE FROM commands WHERE id NOT IN (SELECT id FROM commands ORDER BY at DESC LIMIT ?)",
      KEEP_COMMANDS,
    );
    this.sql.exec("INSERT INTO cmd_times (at) VALUES (?)", now);

    const outgoing = encodeFrame({ t: "cmd", id: frame.id, iv: frame.iv, ct: frame.ct });
    for (const host of hosts) this.send(host, outgoing);
  }

  handleAck(ws, frame) {
    if (!isSeq(frame.seq)) return this.badFrame(ws);
    // Prune only what the guest has consumed and what is older than the replay
    // window: the last KEEP_ENVELOPES always survive for a reconnecting page.
    const keepFrom = Math.max(0, this.currentSeq() - KEEP_ENVELOPES);
    const cutoff = Math.min(frame.seq, keepFrom);
    if (cutoff > 0) this.sql.exec("DELETE FROM envelopes WHERE seq <= ?", cutoff);
  }

  webSocketClose(ws) {
    try {
      ws.serializeAttachment({ authed: false, at: this.nowMs() });
    } catch {
      // socket already gone
    }
    this.sendPresence();
  }

  webSocketError() {
    // Errors are already visible through the close handler; nothing to log.
  }

  // --- alarms ---------------------------------------------------------------

  async alarm() {
    const now = this.nowSeconds();
    const row = this.roomRow();
    if (row) {
      if (row.revoked_at && row.revoked_at + TOMBSTONE_SECONDS <= now) {
        this.wipeRoom();
      } else if (row.expired_at && row.expired_at + TOMBSTONE_SECONDS <= now) {
        this.wipeRoom();
      } else if (!row.revoked_at && !row.expired_at && row.expires_at <= now) {
        // Expiry is sticky: the row survives as a tombstone so a late hello is
        // told "expired" instead of silently looking like a wrong key, while
        // the payload (the only part that costs storage) is dropped.
        this.sql.exec("UPDATE room SET expired_at = ?", now);
        this.sql.exec("DELETE FROM envelopes");
        this.sql.exec("DELETE FROM commands");
        this.sql.exec("DELETE FROM cmd_times");
        this.sql.exec("DELETE FROM meta WHERE k = 'seq'");
        this.closeAll("expired");
      }
    }
    this.sql.exec("DELETE FROM budget WHERE day < ?", new Date((now - 2 * 24 * 3600) * 1000).toISOString().slice(0, 10));
    this.scheduleAlarm();
  }
}

function json(value, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json", "Cache-Control": "no-store" },
  });
}

export { ROOM_TTL_SECONDS };
