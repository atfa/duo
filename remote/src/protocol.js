// Wire constants and validators for the Duo remote relay (protocol v1).
//
// The relay is content-blind: it only stores and forwards opaque envelopes, so
// everything here is about shape and limits, never about plaintext.  The
// canonical protocol description lives in docs/remote-protocol.md.

export const VERSION = 1;
export const SUBPROTOCOL = "duo.v1";

// Limits. Every one of them exists to keep a free Cloudflare account bounded.
export const FRAME_LIMIT = 4096;
export const ROOM_TTL_SECONDS = 8 * 60 * 60;
export const ROOM_TTL_MIN_SECONDS = 60;
export const TOMBSTONE_SECONDS = 24 * 60 * 60;
export const KEEP_ENVELOPES = 50;
export const KEEP_COMMANDS = 100;
export const MAX_SOCKETS = 8;
export const MAX_UNAUTH_SOCKETS = 2;
export const HELLO_DEADLINE_MS = 5000;
export const GUEST_CMD_PER_MINUTE = 30;
export const DAILY_MESSAGE_BUDGET = 2000;
export const MAX_CMD_ID_LEN = 64;
export const REGISTRATIONS_PER_HOUR = 30;
export const REGISTRATIONS_GLOBAL_PER_HOUR = 2000;
export const MAX_BODY_BYTES = 1024;

export const ROOM_PATTERN = /^[A-Za-z0-9_-]{22}$/;
export const PROOF_PATTERN = /^[A-Za-z0-9_-]{43,64}$/;
export const B64URL_PATTERN = /^[A-Za-z0-9_-]+$/;
export const KINDS = new Set(["state", "event"]);
export const ROLES = new Set(["host", "guest"]);

const encoder = new TextEncoder();

// byteLength is what the limit means: a 4 KiB cap on bytes, not on UTF-16 units.
export function byteLength(text) {
  return encoder.encode(text).byteLength;
}

export function frameFits(text) {
  return typeof text === "string" && byteLength(text) <= FRAME_LIMIT;
}

// decodeFrame accepts only a JSON object. Arrays and scalars are not frames, so
// a client cannot smuggle a payload past the field validation below.
export function decodeFrame(text) {
  if (typeof text !== "string" || !frameFits(text)) return null;
  let value;
  try {
    value = JSON.parse(text);
  } catch {
    return null;
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) return null;
  return value;
}

export function encodeFrame(value) {
  return JSON.stringify(value);
}

// timingSafeEqual compares two strings without an early return. workerd has no
// crypto.subtle.timingSafeEqual, so the XOR accumulation is the primitive.
export function timingSafeEqual(a, b) {
  const left = encoder.encode(String(a));
  const right = encoder.encode(String(b));
  let diff = left.length ^ right.length;
  const len = Math.max(left.length, right.length);
  for (let i = 0; i < len; i++) {
    diff |= (left[i] ?? 0) ^ (right[i] ?? 0);
  }
  return diff === 0;
}

export function validRoom(room) {
  return typeof room === "string" && ROOM_PATTERN.test(room);
}

export function validProof(proof) {
  return typeof proof === "string" && PROOF_PATTERN.test(proof);
}

// clampTTL never trusts the client's number: a room cannot outlive the cap and
// cannot be created with a uselessly short lifetime either.
export function clampTTL(ttlSeconds, nowSeconds) {
  const requested = Number.isFinite(ttlSeconds) ? Math.floor(ttlSeconds) : ROOM_TTL_SECONDS;
  const ttl = Math.min(Math.max(requested, ROOM_TTL_MIN_SECONDS), ROOM_TTL_SECONDS);
  return nowSeconds + ttl;
}

export function isSeq(value) {
  return Number.isSafeInteger(value) && value >= 0;
}

export function validB64URL(value, maxLen = 1024) {
  return typeof value === "string" && value.length > 0 && value.length <= maxLen && B64URL_PATTERN.test(value);
}

// presence is the one frame the relay adds on its own: the page needs to know
// whether the host is reachable before it offers to send a command.
export function presenceFrame(host, guests) {
  return encodeFrame({ t: "presence", host, guests });
}

export function errorFrame(code) {
  return encodeFrame({ t: "error", code });
}

export function byeFrame(code) {
  return encodeFrame({ t: "bye", code });
}

export function readyFrame(room, seq, expiresAt) {
  return encodeFrame({ t: "ready", v: VERSION, room, seq, expiresAt });
}
