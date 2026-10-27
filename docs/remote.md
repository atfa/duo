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
- switch mode: fast, step or goal. The switch is quiet, like `/mode` on the
  computer: the agents are not woken up, and the next task is the one that runs
  in the new mode. A started session that is not `DONE` asks first, because the
  round in progress is discarded;
- change the driver behind an agent, picked from the drivers the computer can
  run. The session decides when that is allowed — before the first task, while
  fully paused, or after `DONE` — and the page greys the control when it is not;
- change an agent's model. The list comes from the driver itself and is read when
  the phone asks for it, because listing models starts the driver's own CLI, so
  the picker offers at most the first forty references with a filter and says so
  when the list was cut. A pick is resolved on the computer against the list it
  published, so the phone never has to know how a provider and an id are joined,
  and a driver that publishes no catalog says that instead of offering an empty
  picker;
- start a fresh session (`/new`), refresh the observer's status report.

The page itself is one screen with three tabs under a header that always shows
mode, phase, step and expiry: **Session** holds the current step, the agents,
the observer's report and the shared plan (folded away until asked for),
**Activity** holds the newest lines of the terminal's Conversation view, newest
first — the same speaker, time and text the dashboard shows, because the host
publishes the entries the interface recorded rather than a second rendering of
the events behind them — and **Settings** holds the
controls that are touched rarely — mode, new session, drivers, the model picker
and the pairing details. The composer is pinned to the bottom edge above the
home indicator, so a task can be typed at any moment without scrolling, and the
cards reserve that height so the last one still scrolls clear of it.

Every command is answered: the phone shows `accepted`, or the reason the host
refused it — a bad signature, a command built from a stale state, a duplicate,
an unsupported kind, or a session that takes no task in its current phase.

The host handles one command at a time inside its connection loop, so a slow verb
holds the next one: changing a driver resolves and probes a plugin, which is why
the page waits a minute for those answers instead of the fifteen seconds a task
gets. The relay's idle window is ninety seconds, so a command in progress never
looks like a dead connection.

## Configuration

```json
{ "remote": { "url": "https://remote.xduo.dev", "enabled": true } }
```

`DUO_REMOTE_URL` overrides the file, and `DUO_REMOTE=off` disables remote
access for one run. A cleartext URL is accepted only for a local relay
(`http://127.0.0.1:8787`), which is what `wrangler dev` serves.

Self-hosting means deploying `remote/` with Wrangler and pointing `remote.url`
at your own worker. See [remote/README.md](../remote/README.md).

### The phone page comes from the relay

The page itself is part of the relay: `remote/wrangler.jsonc` serves
`remote/public/` as the Worker's static assets. A change there reaches a phone
only after `cd remote && wrangler deploy`, so a deploy is part of landing such a
change. A phone that loaded the page before a deploy may keep the previous HTML
and `app.js` for up to five minutes (those responses carried `max-age=300`), and
an open tab keeps them until it reloads: force-reload the page, or wait, before
concluding a fix did not work. The page and its scripts now ship `no-cache` with
an ETag, so from this version on a reload always revalidates.

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

The budget is 20,000 frames per room per day, and the host only spends it while
a phone is attached: with no phone connected the computer publishes nothing, so
a session that runs all day costs one frame. A phone that is attached for hours
still cannot exhaust the room by accident, because the host coalesces its
updates to at most one state a second. If a room does run out, the next
`/qrcode` mints a new room instead of handing out a code for a room the relay
has ended, and the reason appears in the session log.
