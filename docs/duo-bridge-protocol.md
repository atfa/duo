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
- `clientType: "observer"` is for an authenticated activity and assistant-message
  connection. Core dispatches only those messages and does not register it as the
  agent endpoint or issue connect/disconnect events, so it cannot trigger a resume
  wake.

A valid `hello` is not answered: Core registers the connection and starts
dispatching. A wrong agent, session id, token or version closes the socket without a
reply, and the session log records why.

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

## Reporting what a turn cost

```json
{"version":1,"type":"usage","agent":"Austin","usage":{"inputTokens":1200,"outputTokens":340,"cacheReadTokens":9000,"cacheWriteTokens":0,"totalTokens":10540,"costUSD":0.042}}
```

Send one `usage` message per completed agent turn, with the tokens and dollar cost
your agent measured — never an estimate Duo has to trust. Core adds it to the
session's cost ledger (per agent and per phase), and the ledger survives resume, so
`duo sessions` can show what a session cost in total.

Send it only if your manifest declared `usage: true`; Core ignores usage from a
driver that did not, because an unmeasured turn must stay unmeasured rather than
enter the ledger as a guess. Omit `turns` for a single turn (it means 1); omit
`totalTokens` and Core derives it from the four parts. A report of all zeros is
dropped: nothing happened, so nothing is recorded.

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
| `mode_change` | Duo switched Fast/Goal/Step; update the prompt mode and deliver any text. An empty `text` updates the stored mode without starting a user turn, so a bare mode change does not wake the agent. |
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

Send `model_state` for the model and `thinking_state` for the reasoning level when the
agent starts and whenever either changes — including when the agent clamps a
requested level to what the current model supports. Report what the agent **settled
on**, not what was asked for. Duo's whole value here is that the picker shows reality.

If your agent does not implement a live switch, send neither, and declare
`liveModelSwitch: false` so Core restarts the process instead of asking.

## Coordination tools

A richer integration can expose Duo's coordination tools to your agent — the shared
plan and queue, status, verification verdicts, escalation and safe restart. They are
request/response calls over the same socket:

| call | purpose |
|---|---|
| `duo_status` | read mode, phase, plan or queue, signatures and worktrees |
| `duo_send` | send a message to the peer; in LOOP enqueue a durable event |
| `duo_events` | LOOP: read pending events or acknowledge a processed event |
| `duo_wait` | LOOP: register an event/deadline wait and end the current turn |
| `duo_ask` | LOOP owner Austin: ask Tony and wait for its correlated reply |
| `duo_reply` | LOOP advisor Tony: answer the exact question ID |
| `duo_finish` | LOOP owner Austin: record a stopping summary without peer approval |
| `duo_set_plan` | publish the shared plan (Goal, or the design prose in Step) |
| `duo_set_steps` | publish the Step queue (Step only) |
| `duo_set_status` | Fast and every Step `[fast]` item: Austin requests Tony's verification. Goal and Step plans and `[goal]` items: both agents sign. The final Step also needs both signatures. |
| `duo_set_verification` | publish a Fast or Step `[fast]` verdict (Tony only) |
| `duo_escalate` | switch the session to Goal from any mode |
| `duo_restart_duo` | request a guarded STEP/EXECUTE restart between steps, optionally installing a new binary |

These eleven are the whole set; availability still follows the session mode. The MCP
server (`duo mcp-server`) exposes all eleven over JSON-RPC, so an adapter can serve
the tools natively or over MCP.

Set `mcp` on your manifest only if your launch command really makes these reachable.
An agent that cannot call them has no coordination tools, and Duo says so plainly.

## Outside a Duo session

`DUO_ACTIVE` is `1` inside a session and unset otherwise. An Agent Adapter **must be
completely inert** without it, so a normal session with your agent behaves exactly as
it would with Duo uninstalled. Both shipped adapters return immediately when
`DUO_ACTIVE !== "1"`, and that check belongs at the very top of your entry point.

## LOOP events and waiting

`loop_events` takes optional `eventId`, the string ID of a processed event from
this agent's inbox. Responses list pending events, which remain until explicitly
acknowledged. Reads do not register waiting or change deadlines. Acknowledging
another agent's or an unknown event fails. Retrying the latest successful ack
is idempotent; older ack retries fail rather than hiding a mistaken event ID.

`loop_wait` takes optional `text` (a future RFC3339 deadline) and `note` (reason
and working summary). It no longer accepts acknowledgements. If the inbox is
nonempty it reports **not waiting** and directs the agent to `duo_events`.
Otherwise it persists wait intent and asks the agent to end the turn without
more tools. The existing `activity: agent_settled` report confirms effective
waiting. A subsequent `agent_start` or another `tool_start` cancels the wait.
Waiting is shown as effective only while settled with no pending event. Core
never interrupts a running tool to force the boundary. Adapters must report
settled activity to provide this confirmation; a tool response alone is not proof.
A message racing with registration is retained and dispatched when the agent
settles. Timers and inboxes survive restart; wake notifications do not consume
an event. Pause and resume hold suppress dispatch.

`loop_ask` takes `text` (the question); only Austin may call it. The response
contains its question ID. New tasks are dispatched directly to Austin and queued
as context for Tony; a disconnected advisor does not block ordinary owner work. It registers a durable consultation, then Austin ends
the turn. `loop_reply` takes `questionId` and `text`; only Tony can answer the
current exact ID. A reply event has `kind: "reply"` and `questionId`. Austin's
work remains blocked until he processes and acknowledges that reply event.
Ordinary peer messages, timers, continuation and restart cannot release it.
Duplicate identical asks/replies are idempotent; stale/wrong-ID replies fail.
There is no implicit timeout or automatic cancellation.

`loop_guard` is an adapter pre-execution check, not a model-facing tool. Its
response has `ok: false` while Austin awaits a consultation or has registered
completion, or after DONE. Adapters must stop ordinary tool execution when it
fails or the bridge is unavailable, while keeping inbox/status/wait/communication
tools available. This check cannot stop already-started commands. Do not execute
an ask and the next action concurrently. Pi and OpenCode implement this guard;
third-party drivers must do the same for their non-MCP tools.

Core records each ordinary Austin tool completion/error as a compact `step`
event for Tony. Events have `id`, `from`, `text`, optional `kind`, `questionId`
and `at`. Step/notification batches wait five seconds from the oldest event;
questions bypass the delay. Core never interrupts a busy advisor, and never
nudges an idle advisor to invent work. Austin with an unanswered consultation
is not nudged or released by unrelated events. Wakes remain best effort with
durable inboxes and bounded transport retries.

`loop_finish` takes `note` (summary); only Austin may call it. Old `approve` and
`reject` parameters fail with an explanation. Pending consultations prevent
finishing; optional advice does not. Completion registers a stopping intent,
suppresses further dispatch, and records DONE when Austin's turn settles. Tony
need not sign or settle. No Git integration or delivery occurs. New human input
cancels a pending finish, or reopens a finished session. Restore requires Austin
to re-evaluate an unobserved finish; it never cancels a pending consultation.

The journal records consultation requests/replies, inbox acknowledgements,
wait boundaries, wake attempts, finish intent and actual owner completion.
Schema 5 migrates v4 by discarding old completion proposals/approvals while
preserving inboxes, IDs, deadlines and DONE summaries. Resume remains silent
until human continuation.

`loop_events` optionally takes either `eventId` (one processed event) or `through`
(the last processed ID in an inbox prefix). `through` acknowledges that whole
processed prefix in one request. Retrying the latest acknowledgement is safe;
wrong-inbox and unknown IDs fail. Batch acknowledgement releases a consultation
only if its matching reply is in the processed prefix. The model-facing tool
uses `ack` for `eventId` and `through` unchanged.
