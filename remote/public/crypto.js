// The page side of the Duo remote protocol: unpadded base64url, the pairing
// proof, and the AES-256-GCM envelope the relay can store but never read.
//
// Both keys are derived from the QR secret K, which only exists in the URL
// fragment on this device and in local Duo memory.

const encoder = new TextEncoder();
const decoder = new TextDecoder();

const SALT = "duo-remote-v1";
const ENC_INFO = "duo-remote-aes-256-gcm";
const MAC_INFO = "duo-remote-cmd-mac";
const AUTH_LABEL = "duo-remote-auth-v1";

export class LinkError extends Error {}

export function b64uEncode(bytes) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function b64uDecode(text) {
  if (typeof text !== "string" || !/^[A-Za-z0-9_-]+$/.test(text)) throw new LinkError("not base64url");
  const padded = text.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((text.length + 3) % 4);
  let binary;
  try {
    binary = atob(padded);
  } catch {
    throw new LinkError("not base64url");
  }
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

// parsePairingKey reads the 32-byte secret from the URL fragment. A fragment is
// never sent to the relay, which is why the key lives there.
export function parsePairingKey(hash) {
  const raw = String(hash ?? "").replace(/^#/, "");
  if (raw === "") return null;
  const key = b64uDecode(raw);
  if (key.length !== 32) throw new LinkError("pairing key must be 32 bytes");
  return key;
}

export function parseRoom(pathname) {
  const match = /^\/p\/([A-Za-z0-9_-]{22})\/?$/.exec(String(pathname ?? ""));
  return match ? match[1] : null;
}

async function hmac(key, message) {
  const imported = await crypto.subtle.importKey("raw", key, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return new Uint8Array(await crypto.subtle.sign("HMAC", imported, encoder.encode(message)));
}

async function hkdf(key, info) {
  const imported = await crypto.subtle.importKey("raw", key, "HKDF", false, ["deriveBits"]);
  const bits = await crypto.subtle.deriveBits(
    { name: "HKDF", hash: "SHA-256", salt: encoder.encode(SALT), info: encoder.encode(info) },
    imported,
    256,
  );
  return new Uint8Array(bits);
}

// deriveKeys returns everything this page needs: the relay credential, the
// envelope key, and the key that authenticates commands addressed to the host.
export async function deriveKeys(pairingKey) {
  const proof = b64uEncode(await hmac(pairingKey, AUTH_LABEL));
  const encKey = await crypto.subtle.importKey("raw", await hkdf(pairingKey, ENC_INFO), { name: "AES-GCM" }, false, [
    "encrypt",
    "decrypt",
  ]);
  const macKey = await hkdf(pairingKey, MAC_INFO);
  return { proof, encKey, macKey };
}

// commandMac is the guest's signature over one command: it binds the relay's
// frame id to the host-issued nonce and the command text, so a relay operator
// who can rewrite frames still cannot make the host do anything.
export async function commandMac(macKey, id, kind, nonce, text) {
  if (String(id).includes("|") || String(kind).includes("|")) throw new LinkError("id and kind cannot contain |");
  return b64uEncode(await hmac(macKey, `${id}|${kind}|${Number(nonce)}|${text}`));
}

export async function decryptEnvelope(encKey, room, frame) {
  const iv = b64uDecode(frame.iv);
  const ciphertext = b64uDecode(frame.ct);
  // The AAD binds an envelope to its room, kind and position, so a stored frame
  // cannot be replayed into another slot or another pairing.
  const aad = encoder.encode(`${room}|${frame.kind}|${frame.seq}`);
  let plain;
  try {
    plain = await crypto.subtle.decrypt({ name: "AES-GCM", iv, additionalData: aad }, encKey, ciphertext);
  } catch {
    throw new LinkError("envelope failed authentication");
  }
  try {
    return JSON.parse(decoder.decode(plain));
  } catch {
    throw new LinkError("envelope is not JSON");
  }
}
