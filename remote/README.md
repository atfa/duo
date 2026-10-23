# Duo remote relay

The relay half of Duo remote: a content-blind Cloudflare Worker that pairs a
phone with a Duo session running on your computer.

Cloudflare stores and forwards **encrypted envelopes only**. Agent processes,
Git operations, logs and model calls all stay on the computer. The relay never
sees a prompt, a command, a report, or the pairing key.

```text
Duo on the computer  ──wss──▶  Cloudflare Worker / Durable Object  ◀──wss──  phone
        (host)                    ciphertext, replay, caps                     (guest)
```

## What lives here

```text
remote/
  wrangler.jsonc     Worker + two SQLite Durable Objects + static assets
  src/index.js       routing: /ws, /api/room, /healthz, assets fallback
  src/room.js        RelayRoom: pairing, auth, replay, dedupe, caps, expiry
  src/registrar.js   Registrar: per-IP and global registration limits
  src/protocol.js    frame limits, validation, constant-time compare
  public/            the mobile page (one page for every room, key in fragment)
  test/              node:test suites; a fake Durable Object harness
  e2e/relay.mjs      optional end-to-end check against a running relay
```

The wire protocol itself is documented in
[`../docs/remote-protocol.md`](../docs/remote-protocol.md).

## Design in one paragraph

Duo generates a 32-byte key `K`, puts it only in the QR code's URL fragment
(`https://<relay>/p/<room>#<K>`) and registers `proof = HMAC(K, ...)` with the
relay. The phone reads `K` from the fragment, derives the same proof and the same
AES-256-GCM key, and joins the room. Every state frame and command is encrypted
before it reaches the Worker, so a relay operator can drop or delay frames but
cannot read or forge them. A second host connection with a valid proof takes the
room over, which is how `/new` keeps the phone connected across a session
restart.

## Tests

No dependencies to install; Node 22+ is enough.

```bash
cd remote
node --test
# bundling check with the wrangler you already have
npx wrangler deploy --dry-run --outdir /tmp/duo-remote-build
```

An optional end-to-end check runs the real Worker runtime (`wrangler dev
--local`) and drives it with real HTTP/WebSocket from `remote/e2e/relay.mjs`:

```bash
scripts/remote-e2e.sh                                   # starts the local relay
DUO_RELAY_BASE=https://remote.xduo.dev scripts/remote-e2e.sh   # checks a deploy
```

The suites drive the shipped `RelayRoom`/`Registrar` classes through their real
HTTP and hibernation handlers, with `node:sqlite` for storage, fake sockets, and
a fake clock: auth rejection, hello deadline, replay-from-`since`, host takeover,
command dedupe, rate limits, expiry, revocation + tombstone, budget cut-off and
two-room isolation. `test/crypto.test.js` pins the golden AES-GCM and command-MAC
vectors shared with the Go client.

## Deploy

```bash
cd remote
wrangler login          # once, on a machine with a browser
wrangler deploy         # attaches remote.xduo.dev from wrangler.jsonc
curl -fsS https://remote.xduo.dev/healthz   # -> ok
```

`wrangler.jsonc` routes the custom domain `remote.xduo.dev`; that requires the
`xduo.dev` zone in the same Cloudflare account. Without it, change the `routes`
entry (or remove it and use the `*.workers.dev` URL) and point Duo at the new
address with `remote.url` in `~/.duo/config.json` or `DUO_REMOTE_URL`.

### Self-hosting

The relay is self-contained: copy this directory, change `name` and `routes`,
deploy, and set the same URL in Duo. Nothing in the mobile page or the Go client
assumes `xduo.dev`; the page derives its WebSocket URL from its own origin.

## Free-tier behaviour

The relay is built to stay inside Cloudflare's free plan and to fail closed:

- WebSocket Hibernation: an idle room costs no duration.
- Static page views are served by Workers Assets, not the Worker script.
- The host numbers `seq` (it is part of the AEAD additional data); the relay
  only refuses a non-increasing value and persists it for replay.
- Replay storage is bounded (50 envelopes, 100 command ids per room).
- Frames are capped at 4 KiB; guests get 30 commands/minute (refused with
  `error{rate_limited}`, not a disconnect); a room relays at most 2000
  messages/day; registration is limited per IP and globally.
- `bye{over_budget}` closes only the relay connection. Duo keeps running
  locally; reopening the QR code creates a fresh room.

## Security notes

- `/healthz` is the only unauthenticated response body; everything else needs a
  room proof.
- Proofs are compared in constant time and never logged. The relay logs at most
  a room id and a role.
- Revocation wipes envelopes and command ids and keeps a 24-hour tombstone, so a
  revoked room id cannot be re-registered immediately.
- `public/_headers` sets a strict CSP (self only), no referrer, and no framing.
