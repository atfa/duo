# Duo Driver Plugin Protocol v1

A Duo Driver Plugin teaches Duo Core about one coding agent. That is the whole job.
Duo Core owns collaboration between two agents — the workflow, the phase machine,
the shared plan, worktrees, review, verification, delivery — and knows nothing about
any specific coding agent. A plugin owns exactly one agent and describes it.

This document is the normative specification. [Plugin Development
Guide](plugin-development-guide.md) is the tutorial: it walks through writing a
plugin from nothing. Read the guide if you are writing one; read this if you are
implementing the protocol itself or reviewing a plugin.

- [The shape of the thing](#the-shape-of-the-thing)
- [Discovery](#discovery)
- [The wire protocol](#the-wire-protocol)
- [Methods](#methods)
- [The manifest](#the-manifest)
- [Capabilities, and the one rule about them](#capabilities-and-the-one-rule-about-them)
- [Session state and resume](#session-state-and-resume)
- [Environment](#environment)
- [Bridge ownership and the delivery rule](#bridge-ownership-and-the-delivery-rule)
- [Errors](#errors)
- [Lifecycle](#lifecycle)
- [Versioning](#versioning)

## The shape of the thing

A plugin is an executable. Duo Core starts one process per session, keeps it alive,
and talks to it over its stdin and stdout with newline-delimited JSON.

Core never launches the plugin *as* an agent. It asks the plugin what command line
to run, and runs that in a pseudo-terminal. This is why a plugin needs no support in
Core beyond this document: Core does not need to know what the command line does,
which flags it contains, or what the agent is.

```
Duo Core                                   your plugin
  |                                             |
  |-- start process, set env ------------------>|
  |-- {"method":"describe"} ------------------>|   who are you
  |<-- manifest --------------------------------|
  |                                             |
  |-- {"method":"probe"} ---------------------->|   can I use your agent right now
  |<-- {"available":true} ----------------------|
  |                                             |
  |-- {"method":"prepare","params":{...}} ---->|   build me a command line
  |<-- {"command":"codex --model '...'"} ------|
  |                                             |
  |-- runs `sh -lc "exec codex --model ..."` --->  your agent, in a PTY
  |                                             |
  |-- your Agent Adapter connects to Duo's bridge, reports activity,
  |   and receives prompts                       |
```

Everything else a plugin does — attaching an extension inside the agent process,
watching a transcript, speaking MCP or ACP, reporting token usage — happens on your
side of the boundary. Core sees only the capabilities you declare.

## Discovery

Core finds plugins by executable name. For a driver called `codex` it looks for, in
order:

1. `duo-plugin-codex` in `~/.duo/plugins/`
2. `duo-plugin-codex` anywhere on `$PATH`
3. `duo-driver-codex`, then `duo-codex`, in the same two places

`duo-plugin-<name>` is canonical. The other two spellings exist so a plugin written
against Duo 0.9 keeps working without a reinstall.

`~/.duo/plugins/` is searched before `$PATH`, so a plugin you are working on shadows
a released one. That is deliberate: you can develop a plugin without uninstalling
it.

Two rules about names:

- `duo`, `duo-mcp-server` and `duo-plugin-test` are reserved. Core will never read
  them as plugins.
- A non-executable file is not a plugin, however well it is named.

`duo plugins` lists everything Core can see, and prints the executable that will
actually run for each name.

## The wire protocol

One JSON object per line, UTF-8, in both directions. A request:

```json
{"protocol":1,"id":7,"method":"prepare","params":{ … }}
```

A successful reply:

```json
{"protocol":1,"id":7,"ok":true,"result":{ … }}
```

A failed reply:

```json
{"protocol":1,"id":7,"ok":false,"error":{"code":"unsupported","message":"…"}}
```

- `protocol` is `1`. It is independent of the Duo binary version and of your
  plugin's version.
- `id` is echoed back exactly. Core may have several calls in flight.
- A line Core cannot parse is ignored rather than fatal, so a stray `println` cannot
  desynchronise the stream. A plugin that writes anything other than protocol JSON
  to **stdout** is broken; write diagnostics to **stderr**.
- Requests arrive one at a time. You do not need your own locking around method
  implementations, and you may not reorder replies.

Requests arrive on stdin and you answer on stdout. On end-of-input, exit cleanly.

## Methods

`describe`, `probe` and `prepare` are required. The rest exist only when the matching
capability is true, and Core will not call them otherwise.

| method | required | returns |
|---|---|---|
| `describe` | yes | the manifest |
| `probe` | yes | whether the agent CLI is usable right now |
| `prepare` | yes | a launch plan |
| `state` | if `capabilities.resume` is `server` | the resume blob as it stands now |
| `models` | if `capabilities.models` | the model catalog |
| `thinking` | if `capabilities.thinking` | reasoning-effort levels |
| `close` | no | — Core may simply exit the process instead |

### `describe`

No params. Returns the manifest, described below. Core calls this first and refuses
to start an agent whose manifest is invalid, so get it right.

### `probe`

No params. Reports whether the underlying agent CLI can be used right now.

```json
{"available":true,"agentPath":"/usr/local/bin/codex","agentVersion":"0.5.0"}
```

An agent CLI that is not installed is **not an error**. Answer
`{"available":false,"reason":"the codex CLI is not on PATH"}` — a reason is
required, because it is what the operator is shown. Returning an error here instead
makes Core report a plugin failure for what is really a missing dependency.

### `prepare`

Turns Core state into a runnable launch specification. This is the load-bearing
method: everything agent-specific about a launch happens here.

Request:

```json
{
  "agent": "Austin",
  "mode": "goal",
  "cwd": "/path/to/worktree",
  "repositoryRoot": "/path/to/repo",
  "scopePath": "internal/driver",
  "session": "20261003-185149-5f5f11fd",
  "token": "…",
  "host": "127.0.0.1",
  "port": "52341",
  "baseCommand": "codex",
  "model": "openai/gpt-5.3-codex",
  "thinking": "high",
  "state": {"sessionId":"…"}
}
```

| field | meaning |
|---|---|
| `agent` | `Austin` or `Tony`. The two agents are always separate. |
| `mode` | `fast` or `goal`. The session's workflow. |
| `cwd` | the agent's own worktree. **Your agent runs here.** |
| `repositoryRoot`, `scopePath` | the repository and the subdirectory the human scoped this session to. |
| `session` | Duo's session id. Stable for the session, including across resumes. |
| `token` | bearer token for Duo's bridge. See [Environment](#environment). |
| `host`, `port` | Duo's bridge address. |
| `baseCommand` | the operator's own command for this agent, possibly empty, possibly with arguments. **Preserve it.** |
| `model`, `thinking` | the current selections, empty when unset. |
| `state` | the blob you returned from the previous `prepare`. Absent on a first run. |

Response:

```json
{
  "command": "codex --model 'openai/gpt-5.3-codex' --resume '$CODEX_THREAD'",
  "env": {"CODEX_THREAD":"01ABC…"},
  "state": {"sessionId":"01ABC…"},
  "sessionIdentity": "01ABC…",
  "cleanup": ["/tmp/codex-austin.log"],
  "notices": ["sqlite3 is required for token metrics"]
}
```

| field | meaning |
|---|---|
| `command` | **required.** A complete shell command line. Core runs it as `sh -lc "exec <command>"` in a pseudo-terminal. |
| `env` | added to the agent process environment, on top of Core's own. Use it for values you would rather not interpolate into a command line. |
| `state` | the opaque blob Core will hand back next time. See [Session state](#session-state-and-resume). |
| `sessionIdentity` | a short label for logs. Core displays it and never parses it. |
| `cleanup` | paths Core must delete before the **next** launch. Use it for anything your agent writes that a stale value would be misread from. |
| `notices` | diagnostics for the operator, in your own words. See below. |

Three rules about `command`:

- It is a **shell command line**, not an argv array. Quote values the shell's way —
  single quotes — or pass them through `env` and reference `"$VAR"`.
- An empty `model` or `thinking` must add **no flag**. `--model ''` is not the same
  as no flag; it is a flag whose argument is the empty string, which most CLIs reject.
- If `baseCommand` already carries a flag, the operator chose it deliberately.
  Replace it only when your agent would abort on it, and say so in a comment.

`notices` belong here and not in the manifest, because they are a property of the
machine at this moment. A static string in `describe` is a claim Core cannot keep
true: sqlite3 may be installed between the two calls.

### `models`

No params. Returns `{"models":[…]}` with `provider`, `id`, `displayName`,
`contextWindow`, `thinking`, `images` per entry. Only called when
`capabilities.models` is true. An empty catalog is an error, not an empty picker.

### `thinking`

No params. Returns `{"levels":["off","low","high"],"default":"low"}`. Only called
when `capabilities.thinking` is true.

## The manifest

```json
{
  "protocol": 1,
  "name": "codex",
  "version": "1.0.0",
  "description": "OpenAI Codex CLI",
  "agent": {"cli": "codex", "defaultCommand": "codex"},
  "defaultModel": "openai/gpt-5.3-codex",
  "modelReference": "qualified",
  "capabilities": { … }
}
```

| field | required | meaning |
|---|---|---|
| `protocol` | yes | must be `1`. Core refuses any other value loudly. |
| `name` | yes | the driver name, matching your executable's suffix. |
| `version` | yes | your plugin's version. Independent of Duo's. |
| `description` | no | one line, shown by `duo plugins`. |
| `agent.cli` | yes | the executable you launch, for diagnostics. |
| `agent.defaultCommand` | no | the command to use when the operator configured none. |
| `defaultModel` | no | the model to run with when the operator chose none. Return `""` rather than a guess: injecting a foreign model id makes most agents abort with "Model not found", and an agent that resolves its own default is better off. |
| `modelReference` | yes | `qualified` or `bare`. See below. |
| `capabilities` | yes | see below. |

`modelReference` is how Core joins a reported provider and id back into the single
reference it shows the human. Declare `qualified` if your agent accepts
`provider/id`, and `bare` if it rejects a provider prefix. Getting this wrong shows a
model in the picker under a name the agent cannot accept.

Core validates the manifest in one place and refuses it there, with these specific
errors: wrong protocol version, empty name, missing or unknown `resume` mode, missing
or unknown `bridge`, unknown `modelReference`, and `liveSteering` declared without an
agent-owned bridge.

## Capabilities, and the one rule about them

> **Every capability must change something Core does. If Core never branches on it,
> it is descriptive metadata, not a capability, and declaring it is a lie that will
> be believed.**

```json
{
  "resume": "client",
  "models": true,
  "thinking": true,
  "liveModelSwitch": false,
  "liveThinkingSwitch": false,
  "activity": true,
  "contextUsage": true,
  "tokenRate": false,
  "bridge": "agent",
  "mcp": true,
  "ptyFallback": true,
  "liveSteering": true,
  "selfReports": false
}
```

| capability | what Core does with it |
|---|---|
| `resume` | see [Session state and resume](#session-state-and-resume). The one that loses data when wrong. |
| `models` | Core offers a model picker and calls `models`. |
| `thinking` | Core offers a thinking picker and calls `thinking`. |
| `liveModelSwitch` | when false, Core restarts the agent to apply a new model. When true, Core does not restart. |
| `liveThinkingSwitch` | the same, for reasoning effort. |
| `activity` | Core expects activity over the bridge rather than inferring it from a file. |
| `contextUsage` | Core shows a context-window figure. |
| `tokenRate` | Core shows an output-rate estimate. |
| `bridge` | who holds Duo's one bridge endpoint. See below. |
| `mcp` | your Agent Adapter can serve Duo's coordination tools over MCP. |
| `ptyFallback` | Core may deliver a prompt by writing to the agent's terminal when no bridge can take it. |
| `liveSteering` | a peer message reaches the agent **mid-turn**, rather than being queued until it finishes. |
| `selfReports` | your bridge attaches the moment the agent process starts, so Core does not announce the connection itself. Set false when your endpoint cannot attach until the agent has something to say. |

`liveModelSwitch` and `liveThinkingSwitch` are the two most commonly got wrong.
Declaring `liveModelSwitch: true` when your agent only takes a model as a startup
flag means Core sends a `set_model` message that nothing handles, tells the human
the model changed, and does not restart the process. The model silently does not
change.

## Session state and resume

`state` is yours. Core stores the JSON you return and hands it back verbatim on the
next `prepare`. It never reads it, never migrates it, and never changes its schema.
Put whatever your agent needs in it.

`resume` declares who owns the agent session's identity:

| value | meaning | what `prepare` must do |
|---|---|---|
| `client` | your agent adopts an identity you choose. | Mint one on the first run, store it in `state`, and replay it on every later launch. |
| `server` | your agent assigns the identity itself. | Send **nothing** until your Agent Adapter has learned a real id, then report it through the `state` method and replay it. |
| `none` | the agent cannot reattach to a previous session. | Store nothing. |

`server` is not a detail. An agent that rejects an id it never issued — answering
"not found, ignoring the flag" and opening a *different* conversation — will start
fresh on **every** launch if you hand it an invented id, because the rejected value
is what gets persisted and replayed. The symptom is an agent that never seems to
resume, with nothing reporting a problem.

### `state`

No params. Returns `{"state": <blob>}` — the resume blob as it stands *now*.

`prepare` runs before the agent starts, so it cannot know the identity an agent
assigns to itself. That is fine for `client`, which mints the id up front, and
impossible for `server`. Without this method a `server` driver has nowhere to put a
learned identity: Core saves the blob `prepare` returned, so every launch starts a
new conversation while every other check still passes.

Core calls `state` when it writes a snapshot, which is the only moment a value
learned mid-session can still reach the disk. Keep the answer in memory on the
plugin — remember what your Agent Adapter reported — and return it. Return the same
blob `prepare` returned if you have learned nothing new; returning an empty blob is
treated as "keep what you already had", not as an error. A `state` call that fails
or is unimplemented falls back to the last blob `prepare` returned, so a plugin that
crashes mid-call cannot cost the user the identity they already had.

Declare `state` only when you need it. Core never calls it on a `client` or `none`
driver, and the contract suite checks the opposite direction: a driver that declares
`resume: server` and does not answer `state` fails.

**Compatibility requirement.** A session written by Duo 0.9.0 migrates to
`{"sessionId":"…"}` under the one word every driver shares, because Core does not
know your agent's vocabulary. Your `prepare` **must** accept `sessionId` as the
identity, in addition to whatever spelling you prefer to write:

```go
type state struct {
    ConversationID string `json:"conversationId,omitempty"` // yours
    SessionID      string `json:"sessionId,omitempty"`      // read-only: Core's compatibility spelling
}

func (s state) conversation() string {
    if s.ConversationID != "" {
        return s.ConversationID
    }
    return s.SessionID
}
```

Write only your own spelling. Accept both. If your state has other fields — a log
file path, a cursor, a rollout name — they survive the round trip untouched, which is
what lets you add resume support without anyone changing Duo's schema.

## Environment

Core defines these for your plugin process **and** for the agent process:

```
DUO_ACTIVE=1
DUO_DRIVER=<your name>
DUO_AGENT=Austin|Tony
DUO_MODE=fast|goal
DUO_HOST=127.0.0.1
DUO_PORT=<port>
DUO_SESSION=<session id>
DUO_TOKEN=<bridge token>
DUO_REPOSITORY_ROOT=<path>
DUO_SCOPE_PATH=<path>
```

That is the whole contract. It is deliberately small, and it is identical for every
plugin, so a new driver needs no change to Core.

You define everything else, in `LaunchPlan.env`. Those values reach the agent process
only. `DUO_PI_SESSION_ID` is Pi's; a plugin should not set a variable named after
another driver, and Core will not read one if you do.

Your Agent Adapter needs `DUO_HOST`, `DUO_PORT`, `DUO_SESSION` and `DUO_TOKEN` to
reach Duo's bridge — which is why the plugin process gets them. See the [Duo bridge
protocol](duo-bridge-protocol.md).

## Bridge ownership and the delivery rule

Duo Core keeps exactly **one** bridge endpoint per agent. A second connection
replaces the first and closes it. So exactly one of these is true:

- `bridge: "agent"` — an adapter running **inside** your agent process connects. It
  can receive injected prompts, so Core delivers downstream messages over the
  bridge.
- `bridge: "plugin"` — **your plugin process** connects. It observes the agent from
  the outside and reports upstream, but it cannot write to the terminal Core owns,
  so Core delivers downstream messages to the terminal instead.
- `bridge: "none"` — no bridge at all.

Both connecting evicts the other. Pick one.

From which follows the rule Core applies to every message it sends:

> **A bridge endpoint may only be used for a request whose capability the driver
> declared.**

| message | gated on | if false |
|---|---|---|
| prompt, peer message, steer, harness nudge, resume wake | `liveSteering` | written to the agent's terminal |
| `set_model` | `liveModelSwitch` | recorded locally; Core restarts the agent |
| `cycle_thinking` | `liveThinkingSwitch` | recorded locally; Core restarts the agent |

Declaring `liveSteering: true` on a plugin-owned bridge is rejected at `describe`,
because such an endpoint cannot receive the message and the request would vanish
without an error.

## Errors

Reply with an error, not an exit:

```json
{"protocol":1,"id":7,"ok":false,"error":{"code":"unsupported","message":"…"}}
```

| code | when |
|---|---|
| `unsupported` | you do not implement this method. Normal for a method whose capability is false. |
| `invalid_request` | the request was malformed, or its `state` was not readable. |
| `unavailable` | the agent is temporarily unusable. Include what to do about it. |
| `internal` | your bug. Say what went wrong. |
| `protocol_mismatch` | Core spoke another protocol version. |

Core distinguishes `unsupported` from a crash, because one means a capability check
was wrong and the other means a plugin is broken.

If your process dies, Core reports the failure once — to the session log and the
interface — and restarts you on the next call. A crash costs a retry, never a
session. It never hangs.

## Lifecycle

```
Core starts you            → describe
                            → probe
Core starts each agent     → prepare          (once per agent, per launch)
                            → Core runs the command you returned
your Agent Adapter         → connects to Duo's bridge when it can
Core saves a snapshot      → state            (if you declared resume: server)
Core restarts an agent     → prepare          (again, with the state from last time)
Core ends the session      → close, or stdin closes and you exit
```

`prepare` is called again on every restart, and it must produce a command that
reattaches to the same conversation. That is the whole contract of a resume.

## Versioning

`protocol` is an integer and Core requires an **exact** match. A plugin declaring
another version is refused at `describe`, with a message naming both versions, rather
than being driven with fields it may misread.

Adding an optional field, or a new method with its own capability, does not change
the protocol version: old plugins are unaffected and new Core ignores what they do
not know. Anything that changes the meaning of an existing field does.

Your `version` is yours. It is independent of Duo's binary version, because a plugin
is released with its agent rather than with Core.

## See also

- [Plugin Development Guide](plugin-development-guide.md) — write a plugin from nothing
- [Duo bridge protocol](duo-bridge-protocol.md) — how an Agent Adapter reports to Core
- [Plugin Development Guide](plugin-development-guide.md) — the tutorial
