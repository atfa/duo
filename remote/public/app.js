// The STEP 1 page: it proves the pairing end to end. It completes the relay
// handshake, authenticates envelopes with the QR secret, and shows room, expiry
// and whether the computer's Duo host is reachable. The full mobile console
// (state, commands, New Session, observer) arrives in STEP 2.

import { LinkError, decryptEnvelope, deriveKeys, parsePairingKey, parseRoom } from "./crypto.js";

const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 15000;
const PING_MS = 25000;

const els = {
  status: document.getElementById("status"),
  room: document.getElementById("room"),
  expiry: document.getElementById("expiry"),
  host: document.getElementById("host"),
  message: document.getElementById("message"),
};

let room = null;
let keys = null;
let socket = null;
let lastSeq = 0;
let expiresAt = 0;
let attempts = 0;
let hostPresent = false;
let fatal = false;
let authRetries = 0;
let retryTimer = 0;
let pingTimer = 0;
let expiryTimer = 0;

function setStatus(text, kind) {
  els.status.textContent = text;
  els.status.className = `status status-${kind}`;
}

function setMessage(text) {
  els.message.textContent = text;
}

function renderHost() {
  els.host.textContent = hostPresent ? "connected" : "offline";
}

function send(frame) {
  if (!socket || socket.readyState !== WebSocket.OPEN) return false;
  socket.send(JSON.stringify(frame));
  return true;
}

function stopPing() {
  if (pingTimer) clearInterval(pingTimer);
  pingTimer = 0;
}

function startPing() {
  stopPing();
  pingTimer = setInterval(() => send({ t: "ping" }), PING_MS);
}

function startExpiry() {
  if (expiryTimer) clearInterval(expiryTimer);
  const tick = () => {
    if (!expiresAt) return;
    const left = expiresAt * 1000 - Date.now();
    if (left <= 0) {
      els.expiry.textContent = "expired";
      fail("expired", "This pairing expired. Run /qrcode in Duo to create a new one.");
      return;
    }
    const minutes = Math.floor(left / 60000);
    els.expiry.textContent = minutes >= 60 ? `in ${Math.floor(minutes / 60)}h${String(minutes % 60).padStart(2, "0")}m` : `in ${minutes}m`;
  };
  tick();
  expiryTimer = setInterval(tick, 30000);
}

function fail(status, message) {
  fatal = true;
  setStatus(status, "bad");
  if (message) setMessage(message);
  stopPing();
  try {
    if (socket) socket.close(1000, status);
  } catch {
    // already closed
  }
  socket = null;
}

function handleError(code) {
  switch (code) {
    case "auth":
      // A scan can land in the moment between the computer minting the room and
      // registering it on the relay. Give that a few short retries before
      // calling the key rejected; a rotated key still ends as fatal.
      if (authRetries < 5) {
        authRetries += 1;
        setMessage("Waiting for the computer to register this pairing…");
        scheduleReconnect();
        break;
      }
      fail("rejected", "The relay rejected this key. It may have been rotated or revoked.");
      break;
    case "expired":
      fail("expired", "This pairing expired. Run /qrcode in Duo to create a new one.");
      break;
    case "room_full":
      setMessage("The relay is at its connection limit for this room. Retrying shortly.");
      break;
    case "offline":
      setMessage("Duo is not connected right now. This will retry automatically.");
      break;
    case "rate_limited":
      setMessage("Too many commands in a minute. The relay is protecting itself; your computer is unaffected.");
      break;
    case "bad_frame":
      setMessage("The relay rejected a malformed frame.");
      break;
    default:
      setMessage(`Relay error: ${code}`);
  }
}

function handleBye(code) {
  switch (code) {
    case "revoked":
      fail("revoked", "Remote access was revoked on the computer. Scan a new QR code to pair again.");
      break;
    case "expired":
      fail("expired", "This pairing expired.");
      break;
    case "over_budget":
      fail("paused", "The free relay budget for this room is spent. Duo itself keeps running on the computer.");
      break;
    case "replaced":
      setMessage("Another device replaced this session. Reconnecting…");
      break;
    default:
      setMessage(`Relay closed the connection: ${code}`);
  }
}

async function handleEnvelope(frame) {
  if (!Number.isSafeInteger(frame.seq) || !frame.kind || typeof frame.iv !== "string" || typeof frame.ct !== "string") return;
  if (frame.seq <= lastSeq) lastSeq = 0; // a recreated room restarts its sequence
  lastSeq = Math.max(lastSeq, frame.seq);
  try {
    const plain = await decryptEnvelope(keys.encKey, room, frame);
    renderState(plain);
    send({ t: "ack", seq: frame.seq });
  } catch (error) {
    const reason = error instanceof LinkError ? error.message : "unreadable frame";
    setMessage(`Could not verify a frame from the computer (${reason}). Ignored.`);
  }
}

// renderState follows docs/remote-protocol.md: a state carries the project,
// session id, mode and phase as top-level strings. It stays defensive because a
// newer host may add fields, never because it trusts them.
function renderState(plain) {
  const parts = [];
  if (plain && typeof plain.project === "string" && plain.project) parts.push(plain.project);
  if (plain && typeof plain.session === "string" && plain.session) parts.push(`session ${plain.session}`);
  if (plain && typeof plain.mode === "string" && plain.mode) parts.push(`mode ${plain.mode}`);
  if (plain && typeof plain.phase === "string" && plain.phase) parts.push(`phase ${plain.phase}`);
  setMessage(parts.length > 0 ? `Paired with ${parts.join(" · ")}` : "Paired with Duo.");
}

function handleFrame(text) {
  let frame;
  try {
    frame = JSON.parse(text);
  } catch {
    return;
  }
  if (!frame || typeof frame !== "object") return;
  switch (frame.t) {
    case "ready":
      authRetries = 0;
      expiresAt = Number(frame.expiresAt) || 0;
      startExpiry();
      setStatus("paired", "ok");
      if (!hostPresent) setMessage("Paired. Waiting for your computer to publish its state.");
      break;
    case "msg":
      void handleEnvelope(frame);
      break;
    case "presence":
      hostPresent = Boolean(frame.host);
      renderHost();
      if (hostPresent) setStatus("paired", "ok");
      break;
    case "error":
      handleError(String(frame.code ?? ""));
      break;
    case "bye":
      handleBye(String(frame.code ?? ""));
      break;
    case "pong":
      break;
    default:
      break;
  }
}

function scheduleReconnect() {
  if (fatal) return;
  const backoff = Math.min(RECONNECT_MAX_MS, RECONNECT_MIN_MS * 2 ** attempts) + Math.floor(Math.random() * 250);
  attempts += 1;
  setStatus("reconnecting", "wait");
  clearTimeout(retryTimer);
  retryTimer = setTimeout(connect, backoff);
}

function connect() {
  if (fatal) return;
  clearTimeout(retryTimer);
  setStatus(attempts > 0 ? "reconnecting" : "connecting", "wait");
  const scheme = location.protocol === "https:" ? "wss" : "ws";
  let next;
  try {
    next = new WebSocket(`${scheme}://${location.host}/ws?room=${encodeURIComponent(room)}`, ["duo.v1"]);
  } catch {
    scheduleReconnect();
    return;
  }
  socket = next;
  next.onopen = () => {
    attempts = 0;
    send({ t: "hello", v: 1, role: "guest", room, proof: keys.proof, since: lastSeq });
    startPing();
  };
  next.onmessage = (event) => handleFrame(event.data);
  next.onclose = () => {
    stopPing();
    hostPresent = false;
    renderHost();
    if (fatal) return;
    if (socket === next) socket = null;
    scheduleReconnect();
  };
  next.onerror = () => {
    // The close handler owns reconnection; browsers report failures there too.
  };
}

async function boot() {
  room = parseRoom(location.pathname);
  els.room.textContent = room ?? "—";
  if (!room) {
    fail("invalid link", "This link is not a Duo pairing link. Run /qrcode in Duo and scan again.");
    return;
  }
  let pairingKey;
  try {
    pairingKey = parsePairingKey(location.hash);
  } catch {
    fail("invalid key", "The key in this link is malformed. Run /qrcode in Duo and scan again.");
    return;
  }
  if (!pairingKey) {
    setStatus("no key", "wait");
    setMessage("Open this page from the QR code Duo shows: the key lives in the link fragment and never reaches the relay.");
    return;
  }
  try {
    keys = await deriveKeys(pairingKey);
  } catch {
    fail("invalid key", "Could not derive the pairing keys on this device.");
    return;
  }
  connect();
}

window.addEventListener("online", () => {
  if (!fatal && (!socket || socket.readyState === WebSocket.CLOSED)) connect();
});
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible" && !fatal && (!socket || socket.readyState === WebSocket.CLOSED)) connect();
});

void boot();
