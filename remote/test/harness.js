// A hermetic Durable Object harness: node:sqlite backs the SQL storage, fake
// sockets stand in for WebSockets, and the alarm is a recorded timestamp. The
// room code under test is the real implementation that ships to Cloudflare.

import { DatabaseSync } from "node:sqlite";

function cursor(rows) {
  return {
    toArray: () => rows,
    one: () => rows[0] ?? null,
    raw: () => rows.map((row) => Object.values(row)),
  };
}

class FakeSQL {
  constructor(db) {
    this.db = db;
  }

  exec(query, ...params) {
    const statement = this.db.prepare(query);
    if (/^\s*(select|pragma|with)\b/i.test(query)) {
      return cursor(statement.all(...params));
    }
    statement.run(...params);
    return cursor([]);
  }
}

export class FakeSocket {
  constructor() {
    this.sent = [];
    this.closed = null;
    this.attachment = null;
  }

  send(message) {
    if (this.closed) throw new Error("socket is closed");
    this.sent.push(String(message));
  }

  close(code = 1000, reason = "") {
    if (!this.closed) this.closed = { code, reason };
  }

  serializeAttachment(value) {
    this.attachment = value;
  }

  deserializeAttachment() {
    return this.attachment;
  }

  frames() {
    return this.sent.map((text) => JSON.parse(text));
  }

  last() {
    const frames = this.frames();
    return frames[frames.length - 1] ?? null;
  }
}

class FakeState {
  constructor() {
    this.db = new DatabaseSync(":memory:");
    this.alarm = null;
    this.sockets = [];
    this.storage = {
      sql: new FakeSQL(this.db),
      setAlarm: (at) => {
        this.alarm = at;
      },
      deleteAlarm: () => {
        this.alarm = null;
      },
      getAlarm: () => this.alarm,
    };
  }

  acceptWebSocket(socket) {
    this.sockets.push(socket);
  }

  getWebSockets() {
    return this.sockets.filter((socket) => !socket.closed);
  }

  setWebSocketAutoResponse() {}

  blockConcurrencyWhile(fn) {
    return fn();
  }
}

// WebSocketPair only exists in workerd. The harness installs the smallest
// possible pair so upgrade() can run unchanged.
class FakePair {
  constructor() {
    this[0] = new FakeSocket();
    this[1] = new FakeSocket();
  }
}

let installed = false;

// workerd allows a 101 Response carrying a webSocket; undici does not. The
// wrapper keeps the shipped upgrade() code unchanged and exposes the same
// fields the tests read.
class TestResponse extends Response {
  constructor(body, init) {
    const upgrade = init && init.status === 101;
    super(body, upgrade ? { ...init, status: 200 } : init);
    this.__upgrade = upgrade;
    this.__webSocket = upgrade ? init.webSocket : null;
    this.__status = upgrade ? 101 : super.status;
  }

  get status() {
    return this.__upgrade ? 101 : super.status;
  }

  get webSocket() {
    return this.__webSocket;
  }
}

function installGlobals() {
  if (installed) return;
  installed = true;
  if (typeof globalThis.WebSocketPair !== "function") globalThis.WebSocketPair = FakePair;
  globalThis.Response = TestResponse;
}

export async function newRoom({ now = 1_700_000_000_000 } = {}) {
  installGlobals();
  const { RelayRoom } = await import("../src/room.js");
  const state = new FakeState();
  const room = new RelayRoom(state, {});
  let clock = now;
  room.nowMs = () => clock;
  return {
    room,
    state,
    now: () => clock,
    advance(ms) {
      clock += ms;
    },
    seconds() {
      return Math.floor(clock / 1000);
    },
  };
}

export async function newRegistrar({ now = 1_700_000_000_000 } = {}) {
  const { Registrar } = await import("../src/registrar.js");
  const state = new FakeState();
  const registrar = new Registrar(state, {});
  let clock = now;
  registrar.nowMs = () => clock;
  return {
    registrar,
    state,
    advance(ms) {
      clock += ms;
    },
  };
}

export const ROOM = "r".repeat(22);
export const PROOF = "p".repeat(43);

export function register(room, { roomID = ROOM, proof = PROOF, ttlSeconds = 600 } = {}) {
  return room.fetch(
    new Request(`https://room.internal/register?room=${roomID}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ proof, ttlSeconds }),
    }),
  );
}

export async function socket(room, state, { roomID = ROOM } = {}) {
  const response = await room.fetch(
    new Request(`https://room.internal/ws?room=${roomID}`, { headers: { Upgrade: "websocket" } }),
  );
  const server = state.sockets[state.sockets.length - 1];
  const deliver = (frame) => room.webSocketMessage(server, typeof frame === "string" ? frame : JSON.stringify(frame));
  const close = () => room.webSocketClose(server);
  return { response, server, client: response.webSocket, deliver, close };
}

export function hello(overrides = {}) {
  return { t: "hello", v: 1, role: "guest", room: ROOM, proof: PROOF, since: 0, ...overrides };
}

export function envelope(seq, kind = "state", iv = "aXY-aXY-aXY", ct = "Y3Q", extra = {}) {
  return { t: "msg", seq, kind, iv, ct, ...extra };
}
