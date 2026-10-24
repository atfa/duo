# Remote control

Duo can be steered from a phone. The computer runs everything; a Cloudflare
Worker only relays ciphertext, so a prompt, an agent message and a repository
path never leave the machine in a form the relay can read.

## Pair a phone

```
/qrcode          # show the QR code and the link
/qrcode new      # rotate the pairing: a new room and a new secret
/remote          # status: relay, room, expiry, connected phones
/remote off      # revoke the pairing and disconnect
```

Scan the code with the phone camera. The link is
`https://remote.xduo.dev/p/<room>#<secret>`: the fragment is never sent to a
server, so the relay never sees the secret.

A pairing is stored in `~/.duo/remote.json` (mode `0600`) and expires after 8
hours. While it is live, a new or resumed session reconnects by itself, so
`/new` keeps the phone connected and no rescan is needed. A machine that has
never paired sends nothing to the relay at all.

Closing the QR panel does not end the pairing: `Esc` only hides it, and the
phone keeps controlling the session. Access ends in exactly three ways —
`/remote off` (which revokes the room on the relay), the pairing expiring after
8 hours, or Duo itself exiting. While the pairing is live, `/qrcode` shows the
same room and the same link again, so a phone that is still open needs no
second scan.

## What the phone can do

- show the project, session, mode, phase, the current step, and Austin's and
  Tony's state and recent activity;
- send a task, which enters the workflow through the same `SubmitTask` a human
  keystroke uses;
- start a fresh session (`/new`), refresh the observer's status report.

Every command is answered: the phone shows `accepted`, or the reason the host
refused it — a bad signature, a command built from a stale state, a duplicate,
an unsupported kind, or a session that takes no task in its current phase.

## Configuration

```json
{ "remote": { "url": "https://remote.xduo.dev", "enabled": true } }
```

`DUO_REMOTE_URL` overrides the file, and `DUO_REMOTE=off` disables remote
access for one run. A cleartext URL is accepted only for a local relay
(`http://127.0.0.1:8787`), which is what `wrangler dev` serves.

Self-hosting means deploying `remote/` with Wrangler and pointing `remote.url`
at your own worker. See [remote/README.md](../remote/README.md).

The default relay is deployed and live: `https://remote.xduo.dev/healthz`
answers `ok`. A live end-to-end check against it is opt-in
(`DUO_RELAY_BASE=https://remote.xduo.dev scripts/remote-e2e.sh`) and can time
out from a network that blocks outbound WebSocket; the hermetic suites and
`wrangler dev --local` are the gate, never a deployed relay.

## Security in one paragraph

The relay stores ciphertext envelopes and a keyed proof of the pairing secret.
It can refuse, delay or drop traffic; it cannot read a message, and it cannot
forge a command, because a command carries a MAC over the fields it would have
to change and a nonce the host issued. The wire format, the derivation and the
limits are specified in [remote-protocol.md](remote-protocol.md); the trust
boundary is in [SECURITY.md](../SECURITY.md).

## Cost

The default relay is meant to run on Cloudflare's free tier: WebSocket
hibernation, event-driven pushes, a 30-second keepalive, a per-room daily
message budget and a per-room command rate limit. When a room exceeds its
budget the relay closes the remote connection and Duo keeps running locally —
a busy session can never turn into a bill.
