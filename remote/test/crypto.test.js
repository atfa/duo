import test from "node:test";
import assert from "node:assert/strict";

import {
  LinkError,
  b64uDecode,
  b64uEncode,
  commandMac,
  decryptEnvelope,
  deriveKeys,
  parsePairingKey,
  parseRoom,
} from "../public/crypto.js";

// These are the golden vectors for protocol v1. The Go client uses the same
// values in internal/remote/crypto_test.go, which is how two independent
// implementations stay byte-compatible without sharing code.
const VECTOR = {
  key: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8",
  room: "QEFCQ0RFRkdISUpLTE1OTw",
  proof: "lY4LXUExKI_Iu9P2mf-aQ6kQ81cqaTdyp9tTFSSOT3g",
  iv: "AQIDBAUGBwgJCgsM",
  seq: 1,
  kind: "state",
  plaintext: '{"v":1,"t":"state","session":{"id":"demo","mode":"step","phase":"execute"}}',
  ciphertext:
    "tH3sLK7dMllmfr7Je85_wAfDvVr3nHftuuvP8In30ERgsENojPcqVHcLqhX7Ubz97D87hcvKwIcWfB-B6GpXz_V59p5MWaXd_2_2yEF1L_glKE83WgvZ35jUNA",
  mac: "z3lbGYzVxeRridRNlSahRvSQdrkPOnSAtC-BGqRVIH4",
};

test("base64url is unpadded and round-trips", () => {
  const bytes = new Uint8Array([0, 251, 255, 1, 2, 3]);
  const encoded = b64uEncode(bytes);
  assert.equal(encoded.includes("="), false);
  assert.deepEqual(Array.from(b64uDecode(encoded)), Array.from(bytes));
  assert.throws(() => b64uDecode("not base64!"), LinkError);
});

test("the pairing key comes from the fragment and must be 32 bytes", () => {
  const key = parsePairingKey(`#${VECTOR.key}`);
  assert.equal(key.length, 32);
  assert.equal(parsePairingKey(""), null);
  assert.throws(() => parsePairingKey(`#${b64uEncode(new Uint8Array(31))}`), LinkError);
});

test("room ids are exactly the 22-character base64url of 16 bytes", () => {
  assert.equal(parseRoom(`/p/${VECTOR.room}`), VECTOR.room);
  assert.equal(parseRoom(`/p/${VECTOR.room}/`), VECTOR.room);
  assert.equal(parseRoom("/p/short"), null);
  assert.equal(parseRoom("/"), null);
});

test("the relay proof matches the Go implementation's golden vector", async () => {
  const { proof } = await deriveKeys(parsePairingKey(VECTOR.key));
  assert.equal(proof, VECTOR.proof);
  assert.equal(proof.length, 43);
});

test("an envelope decrypts with room, kind and sequence bound as AAD", async () => {
  const { encKey } = await deriveKeys(parsePairingKey(VECTOR.key));
  const frame = { kind: VECTOR.kind, seq: VECTOR.seq, iv: VECTOR.iv, ct: VECTOR.ciphertext };
  const plain = await decryptEnvelope(encKey, VECTOR.room, frame);
  assert.deepEqual(plain, JSON.parse(VECTOR.plaintext));
});

test("a tampered envelope or a moved envelope does not decrypt", async () => {
  const { encKey } = await deriveKeys(parsePairingKey(VECTOR.key));
  const ciphertext = b64uDecode(VECTOR.ciphertext);
  ciphertext[0] ^= 0xff;
  await assert.rejects(
    decryptEnvelope(encKey, VECTOR.room, { kind: "state", seq: 1, iv: VECTOR.iv, ct: b64uEncode(ciphertext) }),
    LinkError,
  );
  await assert.rejects(
    decryptEnvelope(encKey, VECTOR.room, { kind: "event", seq: 1, iv: VECTOR.iv, ct: VECTOR.ciphertext }),
    LinkError,
  );
  await assert.rejects(
    decryptEnvelope(encKey, VECTOR.room, { kind: "state", seq: 2, iv: VECTOR.iv, ct: VECTOR.ciphertext }),
    LinkError,
  );
});

test("the command MAC matches the Go implementation's golden vector", async () => {
  const { macKey } = await deriveKeys(parsePairingKey(VECTOR.key));
  assert.equal(await commandMac(macKey, "cmd-1", "task", 7, "restart the build"), VECTOR.mac);
  await assert.rejects(commandMac(macKey, "cmd|1", "task", 7, "x"), LinkError);
});
