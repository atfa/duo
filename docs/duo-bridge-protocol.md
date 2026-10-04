# Duo bridge protocol, version 1

This is how a Driver Plugin's **Agent Adapter** talks to Duo Core once an agent is
running. It is a separate protocol from the [Driver Plugin
Protocol](driver-plugin-protocol.md), and the two are independent:

- The **Driver Plugin Protocol** runs before and around the agent. Core uses it to
  learn what the agent is and to build a command line.
- The **bridge protocol** runs while the agent is alive. The Agent Adapter uses it
  to report what the agent is doing and to receive instructions.

They share a version number only in the sense that both are currently `1`. A plugin
speaking bridge version 1 needs no particular Driver Plugin Protocol version, and
vice versa. An agent with no Agent Adapter at all is perfectly valid: Core writes to
the agent's terminal instead, which is what `ptyFallback: true` means.

If you are writing an Agent Adapter in Go, `internal/mcp` already contains a working
client — `mcp.ConnectBridge` — and both shipped TypeScript adapters are complete
worked examples:

- `pi-extension/transport.ts` — the transport, verbatim reusable
- `opencode-extension/transport.ts` — the same transport against opencode's plugin API

## Connecting

A TCP socket to `DUO_HOST:DUO_PORT`. The host, port, session id and token all come
from the plugin process's environment, which Core sets for you.

Every line is one JSON object, UTF-8, newline-terminated, in both directions.

## hello

The first message. Core rejects the connection if the session id or token is wrong,
or if the version does not match exactly.

```json
{"version":1,"type":"hello","agent":"Austin","sessionId":"…","token":"…","timestamp":1772000000000}
```

- `agent` is `Austin` or `Tony`. The two agents have separate conversations and
  separate sessions.
- `version` must be exactly `1`.

Core answers with a `response`:

```json
{"version":1,"type":"response","ok":true,"agent":"Austin","sessionId":"…"}
```

Connect once and keep the socket. Reconnect after a drop, with a short backoff; Core
treats a reconnect as a new `hello` and does not mind.

## Reporting activity

This is the point of an Agent Adapter: without one, the operator sees a terminal and
cannot tell what the agent is doing.

```json
{"version":1,"type":"activity","agent":"Austin","activity":"tool_start",
 "tool":"Bash","detail":"npm test","timestamp":1772000000000}
```

`activity` is one of:

| value | when |
|---|---|
| `agent_start` | the agent began a turn |
| `agent_settled` | the turn ended |
| `provider_start` / `provider_end` | a sub-agent or side task began or ended |
| `tool_start` / `tool_end` | a tool call began or ended; `tool` names it |
| `tool_error` | `tool_end` for a call that failed |
| `stream` | the tail of the assistant's text, in `detail` |

Optional on any activity: `contextTokens`, `contextWindow`, `tokensPerSecond`. Send
them only if your `manifest` declared `contextUsage` or `tokenRate` — an adapter that
reports a capability Core was not told about is making a claim it cannot back up.

The same message carries what the human is shown: `tool` is the running tool, and
`detail` is a compact argument or error summary. Do not send your agent's full-screen
output; Core renders this instead of trying to parse a PTY.

## Reporting the agent's own text

```json
{"version":1,"type":"assistant_message","agent":"Austin","text":"…","timestamp":…}
```

Core shows this in the shared timeline and records it in the transcript. It is how
the peer agent sees what the other one said.

## Receiving instructions

Core sends these. The ones that matter for an Agent Adapter:

| type | what to do |
|---|---|
| `human_prompt` | the human typed a task. Deliver it as a user turn. |
| `peer_message` | the other agent sent you something. |
| `steer` | the other agent is talking to you while you work. Deliver it **mid-turn** — that is what `liveSteering` promises. |
| `duo_notice` | Duo has something to say. |
| `harness_prompt` | Duo's watchdog noticed you are idle. |
| `resume_prompt` | this session was resumed; re-read the current state. |
| `set_model` | switch model. Only sent when you declared `liveModelSwitch`. |
| `cycle_thinking` | advance reasoning effort. Only sent when you declared `liveThinkingSwitch`. |
| `get_status` | answer with the shared plan and phase (`duo_status`). |

Check `to` when it is present and ignore messages addressed to the other agent.

`set_model` and `cycle_thinking` are only ever sent if your manifest declared the
matching capability. If you cannot honour one, reply
`{"type":"response","ok":false,"requestId":…,"text":"…"}` with the reason — do not
silently ignore it. Core shows that reason, and a silent failure here is a model
that never changes with nothing reporting why.

## Request and response

Any message with a `requestId` expects a response with the same id:

```json
{"version":1,"type":"response","requestId":"…","ok":true,"text":"…"}
```

## Errors

```json
{"version":1,"type":"agent_error","agent":"Austin","text":"…"}
```

Send this when your adapter fails. It is recorded, and it is how a driver reports a
problem in its own integration rather than leaving the operator to infer one from a
silent agent.

## The one-endpoint rule

Duo Core keeps exactly one bridge connection per agent. A second connection from the
same agent **replaces and closes** the first. So a driver has exactly one endpoint:

- an adapter inside the agent process (`bridge: "agent"`), or
- the plugin process itself (`bridge: "plugin"`).

Never both. If both connect, they will evict each other in a loop and the symptom is
an agent that appears to connect and drop repeatedly.

## Reporting your own state

Two optional messages keep Duo's model and thinking displays honest:

```json
{"version":1,"type":"model_state","agent":"Austin","provider":"openai","model":"gpt-5.3-codex","ok":true}
{"version":1,"type":"thinking_state","agent":"Austin","thinking":"high","ok":true}
```

Send `model_state` when the agent starts and whenever the model changes — including
when your agent clamps a requested thinking level to what the current model supports.
Report what the agent **settled on**, not what was asked for. Duo's whole value here
is that the picker shows reality.

If your agent does not implement a live switch, send neither, and declare
`liveModelSwitch: false` so Core restarts the process instead of asking.

## Coordination tools

A richer integration can expose Duo's coordination tools to your agent — the shared
plan, status, verification verdicts, escalation. Those are HTTP-shaped calls over the
same socket, and `internal/mcp` implements them:

| call | purpose |
|---|---|
| `duo_set_plan` | publish the shared plan (Goal mode) |
| `duo_status` | read the shared plan, phase and signatures |
| `duo_set_status` | sign the current phase (Goal) or request verification (Fast) |
| `duo_set_verification` | publish a Fast-mode verdict |
| `duo_send` | send a message to the peer or the human |
| `duo_escalate` | upgrade the session from Fast to Goal |

These six are the whole set today, and they are the same tools Duo exposes over MCP,
so an adapter can serve them either way.

Set `capabilities.mcp` only if your adapter really serves these. An agent that
cannot call them has no coordination tools, and Duo says so plainly.

## Outside a Duo session

`DUO_ACTIVE` is `1` inside a session and unset otherwise. An Agent Adapter **must be
completely inert** without it, so a normal session with your agent behaves exactly as
it would with Duo uninstalled. Both shipped adapters return immediately when
`DUO_ACTIVE !== "1"`, and that check belongs at the very top of your entry point.
