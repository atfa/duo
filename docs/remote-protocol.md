# Duo Remote protocol, version 1

This is the contract between a Duo session running on the user's machine (the
**host**), the mobile page a phone opens after scanning `/qrcode` (the
**guest**), and the Cloudflare Worker + Durable Object that relays between them
(the **relay**).

The relay is deliberately dumb and blind:

- it stores and forwards **ciphertext envelopes only**;
- it never sees the pairing secret, a prompt, an agent message or a repository
  path;
- it can refuse service, delay or drop messages, but it cannot read or forge
  content.

Everything that matters — the agents, Git, logs and model calls — stays on the
user's machine. See [SECURITY.md](../SECURITY.md) for the threat model.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/p/{room}` | The static mobile page. The page is identical for every room and reads the secret from `location.hash`. |
| `GET` | `/ws?room={room}` | WebSocket upgrade. The client offers subprotocol `duo.v1`; the `101` must echo it. |
| `POST` | `/api/room?room={room}` | Host-only registration. Body: `{"proof","ttlSeconds"}`. |
| `DELETE` | `/api/room/{room}` | Revocation, authenticated with `Authorization: Bearer <proof>`. |
| `GET` | `/healthz` | `ok`. Never falls through to the static assets. |

The `duo.v1` subprotocol is required: browsers reject a `101` that does not
select one of the offered subprotocols. It carries no secret — the proof travels
only in the first frame, so no credential can reach an HTTP access log.

`/ws` and `/healthz` must never be answered by the asset fallback, and `/p/*`
must always serve the page.

## Pairing

Duo generates, locally:

- `K` — 32 random bytes, base64url without padding (43 characters);
- `room` — 16 random bytes, base64url without padding (22 characters).

The QR code encodes `https://<relay>/p/<room>#<K>`. A URL fragment is not sent
to the server, so the relay never receives `K`.

From `K` both sides derive:

```text
proof      = base64url(HMAC-SHA256(K, "duo-remote-auth-v1"))            // 43 chars
encKey     = HKDF-SHA256(ikm = K, salt = "duo-remote-v1",
                         info = "duo-remote-aes-256-gcm", L = 32)
cmdMACKey  = HKDF-SHA256(ikm = K, salt = "duo-remote-v1",
                         info = "duo-remote-cmd-mac", L = 32)
```

`proof` is what the relay stores and compares. It is a keyed hash of a fixed
string, so it proves possession of `K` without disclosing it, and it cannot be
turned back into `K`. A relay operator who holds `proof` can join the room but
still cannot read or forge anything.

## Registration

The host registers the room before it shows a QR code:

```http
POST /api/room?room=<22 chars>
{"proof":"<43 chars>","ttlSeconds":28800}
```

- The first writer wins. A second registration of a live room is accepted only
  with the matching `proof`, so a phone cannot take over a room id it observed.
- `expiresAt` is clamped server-side to at most 8 hours; `ttlSeconds` is a
  request, not a grant.
- Registrations are rate limited per client IP in a small `Registrar` Durable
  Object. A `429` is a refusal, not a queue.

`DELETE /api/room/{room}` with the matching `proof` deletes the room record, its
stored envelopes and its command ids, and closes every live socket with
`bye{revoked}`. It answers `204` even for a room that is already gone, so a
caller can treat revocation as idempotent. This is the `/remote off` path.

## Handshake

Every client, host or guest, opens `/ws?room={room}` and sends exactly one
`hello` as its first frame:

```json
{"t":"hello","v":1,"role":"host","room":"<room>","proof":"<proof>","since":0}
```

The relay replies with:

```json
{"t":"ready","v":1,"room":"<room>","seq":<latest stored seq>,"expiresAt":<unix>}
```

Rules:

- Any frame before a valid `hello` is `error{auth}` followed by a close.
- A `hello` with a malformed field, a `proof` that is not 43 base64url
  characters, or a proof that does not match the stored one in constant time is
  `error{auth}` followed by a close.
- A `hello` for a missing or revoked room is `error{auth}` followed by a close;
  for an expired room it is `error{expired}`, which is equally terminal. Guests
  can never create a room; only `POST /api/room` can.
- Unauthenticated sockets are bounded: at most 2 are held per room (a further
  upgrade is refused with `429`), and a `hello` that arrives more than 5 seconds
  after the upgrade is rejected with `error{auth}`. An unauthenticated socket
  that never sends anything is never authenticated, so it can never relay a
  frame.
- `role:"host"` takes over the room: the previous host socket receives
  `bye{replaced}` and is closed. This is how `/new` keeps the phone connected.
- `since` replays stored envelopes with `seq > since` in ascending order, for
  host and guest alike.
- `seq` is **host-assigned**: the host sets it on every `msg` because it is part
  of the AEAD additional data, so the host must know it before it encrypts. A
  fresh room starts at 1; the relay persists the host's `seq`, rejects a
  non-increasing one with `error{bad_frame}`, and never rewrites it. `ready.seq`
  is the last persisted value, and the host continues at `ready.seq + 1`. A
  recreated room reports `ready.seq = 0`, and the guest treats `seq <= lastSeq`
  as a new epoch rather than as a duplicate.
- At most 8 authenticated sockets per room.

## Frames

All frames are one JSON object in a text WebSocket message. Sizes are capped:
a frame may not exceed 4 KiB, and a plaintext payload may not exceed 3 KiB.

Host → relay → guest (stored for replay):

```json
{"t":"msg","seq":1,"kind":"state","iv":"<b64url 12 bytes>","ct":"<b64url>"}
{"t":"msg","seq":2,"kind":"event","iv":"...","ct":"..."}
```

Guest → relay → host (stored for dedupe only):

```json
{"t":"cmd","id":"<uuid>","iv":"...","ct":"..."}
```

Guest → relay (delivery watermark, never forwarded to the host):

```json
{"t":"ack","seq":2}
```

Keepalive and control:

```json
{"t":"ping"}                                  // client → relay
{"t":"pong"}                                  // relay → client
{"t":"presence","host":true,"guests":1}       // relay → client, after every join/leave
{"t":"bye","code":"revoked|expired|over_budget|replaced"}
{"t":"error","code":"auth|bad_frame|rate_limited|expired|room_full|offline|unsupported"}
```

`presence` tells a guest whether a host is attached, which is how the page says
"Duo is offline" instead of looking frozen. `error{offline}` answers a command
sent while no host socket is connected; that command is not recorded, so the
guest may retry the same `id` once a host returns. `error{rate_limited}` and
`error{offline}` are recoverable; `bye{over_budget}`, `bye{revoked}` and
`bye{expired}` end the room for this client.

`ack` prunes replay state for messages both sides are done with; the relay keeps
at least the last 50 envelopes regardless, and the last 100 command ids.

## Encryption

Every `msg` and `cmd` carries AES-256-GCM ciphertext:

- key: `encKey`;
- IV: 12 random bytes per message, never reused;
- additional data:
  - `msg`: `room + "|" + kind + "|" + decimal(seq)`
  - `cmd`: `room + "|cmd|" + id`
- ciphertext layout: the GCM tag is appended by the AEAD (Go `Seal` with a
  prefix-less nonce, WebCrypto `AES-GCM` with a 128-bit tag).

The relay never has the key, so it cannot check any of this. A client that
receives a frame it cannot authenticate drops it and does not render it.

## Plaintext payloads

### `state`

Published on connect, after every workflow change, and at a low frequency
while the session is live.

```json
{
  "v": 1,
  "n": 7,
  "ts": 1761000000000,
  "project": "duo",
  "repo": "atfa/duo",
  "session": "20261023-221903-24cfe5e3",
  "mode": "step",
  "phase": "EXECUTE",
  "started": true,
  "step": {"index": 1, "total": 3, "policy": "goal", "title": "relay + Go client"},
  "plan": "first line of the shared plan",
  "agents": {
    "austin": {"connected": true, "running": true, "driver": "pi", "model": "claude-sonnet-4-6", "thinking": "high", "activity": "editing remote/src/room.js", "state": "running"},
    "tony":   {"connected": true, "running": false, "driver": "opencode", "model": "gpt-5", "thinking": "", "activity": "", "state": "running"}
  },
  "pause": {"state": "running"},
  "activity": [
    {"at": 1761000000000, "kind": "assistant", "agent": "austin", "text": "..."}
  ],
  "observer": {"analyzing": false, "reports": [{"at": 1761000000000, "text": "..."}]},
  "delivery": null,
  "command": {"id": "…", "kind": "task", "accepted": true, "note": ""}
}
```

`agents` is keyed `austin` and `tony`, and a page folds either spelling onto the
same agent so an older host still renders. `driver` names what runs the agent
(`pi`, `opencode`, …) and `model` the model it is using, so the phone can say
which of the two is working with what. `state` is the driver process state —
`starting`, `running`, `stopping`, `exited` or `failed` — and empty when the
session runs no driver for that agent. The page draws its indicator from these
fields: green for attached and idle, blue for working, grey for not attached
yet (a normal moment during startup), and red only for `failed`.

`command` is the host's answer to the last command it received, addressed by
its `id`. A refusal is reported, not swallowed: `accepted: false` with a `note`
the page shows, because the phone is the only place the human is looking. The
host refuses a command whose MAC does not verify, whose `n` is not one of the
last nonces it published ("the command was for an older state"), whose `id` it
already executed ("the command was already executed"), whose kind it does not
implement, or which the session itself rejected.

`n` is the command nonce: it must be a fresh random or monotonically increasing
value on every state and event the host publishes, so a command the guest sends
back can be tied to something the relay could not have invented.

`text` fields are truncated by the host before encryption. The page renders
them as text, never as HTML.

### `event`

An incremental update, so a live session does not republish a whole snapshot per
keystroke:

```json
{"v":1,"n":8,"at":1761000000000,"kind":"activity","agent":"tony","text":"..."}
```

`kind` is one of `activity`, `phase`, `agent`, `observer`, `delivery`. A guest
that cannot apply an event (unknown kind, missing context) may simply wait for
the next `state`; the host republishes one after a reconnect.

### `cmd`

```json
{"v":1,"id":"<uuid>","kind":"task","text":"run the tests"}
{"v":1,"id":"<uuid>","kind":"new_session"}
{"v":1,"id":"<uuid>","kind":"observer_report"}
```

`id` is generated by the guest and repeated verbatim in the MAC, so the relay
cannot swap or duplicate it. The host:

1. rejects a command whose MAC does not verify;
2. rejects a command whose `n` is not one of the last 8 nonces it issued;
3. ignores an `id` it already executed, and re-sends its current state instead;
4. applies the command through the session seam (`SubmitTask`, `NewSession`,
   `RequestObserverReport`) and publishes a new state.

The command MAC is:

```text
mac = base64url(HMAC-SHA256(cmdMACKey,
                            id + "|" + kind + "|" + decimal(n) + "|" + text))
```

It covers the fields directly rather than the serialized JSON, so neither side
needs a canonical-JSON rule: `id` and `kind` cannot contain `|`, `n` is decimal
and `text` is last, which makes the split unambiguous. `mac` is sent inside the
encrypted payload, so the relay cannot even see it.

## Relay storage

The room Durable Object is SQLite-backed:

| Table | Columns | Retention |
| --- | --- | --- |
| `room` | `room, proof, created_at, expires_at, revoked` | until revoke/expiry |
| `msg` | `seq, kind, iv, ct, at` | last 50, and nothing older than 1 h |
| `cmd` | `id, at` | last 100, and nothing older than 1 h |
| `usage` | `day, count` | today only |

A Durable Object alarm enforces expiry even when no socket is connected.
Sockets are accepted through the WebSocket Hibernation API, so an idle room
costs no duration.

## Caps

| Limit | Value | On breach |
| --- | --- | --- |
| frame size | 4 KiB | `error{bad_frame}` |
| sockets per room | 8 | `error{room_full}` |
| unauthenticated sockets | 2, 5 s to hello | close |
| guest commands | 30/min per room | `error{rate_limited}` (recoverable; socket stays open) |
| relayed messages | 2000/day/room | `bye{over_budget}` |
| room lifetime | 8 h maximum | `bye{expired}`; the row survives 24 h so a late `hello` still receives `error{expired}` |
| revocation tombstone | 24 h | registration and `hello` refused with `error{auth}` |

`over_budget` and `expired` close the remote connection only. Duo keeps running
locally, which is the whole point of the default free-tier posture.

## Test vectors

`K` and the derived values are fixed here so the Go client and the page can be
checked against the same bytes. They are test values and must never ship as a
pairing.

| Name | Value |
| --- | --- |
| `K` | `AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA` |
| `proof` | `_5LL3yvE9T7Xl6BoJZINPgtr-SikscYPsELlvqDqqoc` |
| `encKey` | `cHuL5VvcM-gsf8RTpabMoA7aTVrSNqpQ_pK4syfZBlU` |
| `cmdMACKey` | `b-HHX7hlMWpmmrACZlQvIVDq3JbJ_37nGs3iPIf3oGY` |
| command MAC | `8yZ0b3JtkHnfvu5JAcpQhGkNfTeLTn-FvBQBcuiG9XQ` for `id-1`, `task`, `1`, `run the tests` |
| `state` plaintext | `{"v":1,"n":1,"ts":0,"project":"duo"}` |
| `iv` | `AAAAAAAAAAAAAAAA` |
| `msg` AAD | the room, `state` and `1`, joined by `|` |
| `msg` `ct` | `I2rNLAXdr3JyQ4ho2NzLC25q8jvkbuu2n1qzb8Gw2Qcn1Pe84oiDPR1fAItw13nyFKrzEQ` |

These values were produced with RFC 5869 HKDF, HMAC-SHA256 and AES-256-GCM
outside this repository, and `internal/remote/crypto_test.go` asserts them, so
the page and the Go client can be checked against the same bytes. The room used
for the `msg` vector is the literal `room`, which is not a valid room id; it only
keeps the vector short.
