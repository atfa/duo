# Duo GUI protocol (v1)

This document is the complete contract between Duo Core and the GPUI graphical
frontend. It is deliberately small: the graphical frontend is an MVP, and a
protocol that only carries what a screen shows is a protocol that does not have to
change when the screen does.

The graphical frontend is not a second source of project state and it does not
reach into Core. It reads a projection of one session and sends the verbs a human
performs, exactly like the terminal interface. The isolation rule is the same as
`internal/tui`'s: the frontend depends on `internal/session` (or on a host built
from it) and holds no coordinator, no transport and no workspace manager. See
[architecture.md](architecture.md#driving-a-session-from-a-frontend).

## Transport

Two OS pipes bound to one child process.

**stdout is protocol, and only protocol.** The renderer's stdout carries NDJSON and
nothing else; every diagnostic, log line and panic message goes to stderr. A `log`
or `tracing` subscriber that defaults to stdout would corrupt the parent's parser on
its first line, so this is a MUST rather than a style preference.

- Go (`duo gui`) spawns the Rust renderer (`duo-gpui`) and keeps its stdin and
  stdout. The parent is the session host; the child is a renderer.
- **Go → renderer** is the child's **stdin**. **Renderer → Go** is the child's
  **stdout**. stderr is not part of the protocol and carries diagnostics.

Framing is newline-delimited JSON (NDJSON): one UTF-8 JSON object per line, no
enclosing array, `\n` terminated. A blank line is ignored. Unknown fields are
ignored by both sides, so a field can be added without a protocol version bump.

The two directions are **not** symmetric about bad input, because only one of them
has a human watching:

- **Session → renderer**: a line the renderer cannot parse is skipped and reported
  on stderr; it does not end the stream. The host is assumed correct, and a
  renderer that cannot read a frame cannot usefully ask about it.
- **Renderer → session**: every rejected command is answered with an `error` frame
  and the stream continues. A parse failure, a `type` other than `command`, an
  unknown `kind`, a `submit_task` with empty `text`, a `set_language` with an empty
  `language`, and a `restart_agent` naming an unknown agent are all errors rather
  than silence: the human pressed something, and the window has to say why nothing
  happened. Nothing in this direction is reported on stderr.

## Agent identity

An agent id is **lowercase on the wire, everywhere**: the keys and values of
`view.agents`, the keys of `view.ready`, `event.agent`, `event.peer`, the agent a
replayed history entry maps to, and `command.agent`. The three values are `austin`,
`tony` and `duo`.

Core spells the constant `protocol.Austin` as `"Austin"`, and a recorded history
entry's `Pane` carries that spelling too. The host normalizes it once, at the
boundary, in both directions, and does not rely on a renderer to compare
case-insensitively: a renderer that looked up `"Austin"` in a lowercase map would
silently fail, and `restart_agent` is exactly the kind of command that fails
silently. A renderer therefore uses the lowercase spellings as constants and never
re-cases them.

## Session → renderer

Every message is an object with a `type` field.

| `type` | Payload | Meaning |
| --- | --- | --- |
| `view` | `view` (View) | The complete state of the session at one moment. |
| `event` | `event` (Event) | One conversation or workflow entry appended to the timeline. |
| `error` | `text` | The last command was rejected. The text is shown to the human. |
| `closed` | `text` | The session ended or the host is gone. The renderer shows the text and stops expecting state. |

The host sends one `view` immediately on start, one `event` **and then** one `view`
for every session event, and one `view` on a 500 ms heartbeat so time-derived
fields (agent activity) stay current. Because a `view` follows every `event`, a
renderer that only drew `view` would still be correct; `event` exists so the
timeline keeps its history.

The start of a stream is ordered: first one `view` (so `notices` are visible), then
the replayed history as `event` messages in recorded order, and only then live
events. A renderer that appends `event` in arrival order therefore builds a resumed
timeline in the right order without a second code path.

### View

`snake_case` field names.

| Field | Type | Meaning |
| --- | --- | --- |
| `version` | string | The Duo version, e.g. `v0.11.0`. |
| `session` | string | The session id. |
| `mode` | string | `fast`, `goal` or `step`. |
| `phase` | string | The workflow phase, e.g. `PLAN`, `EXECUTE`, `REVIEW`. |
| `language` | string | `zh` or `en`. |
| `repository` | string | The repository the session runs in. |
| `base_branch` | string | The branch Duo recorded at session start. |
| `base_commit` | string | The base commit. |
| `status` | string | The workflow status block, rendered by Core for the human. |
| `plan_version` | number | The shared Plan version (0 when there is none). |
| `plan` | string | The shared Plan body, empty when there is none. |
| `ready` | object of bool | Per agent, whether that agent signed the current phase. |
| `steps_total` | number | The size of the Step-mode queue. |
| `step_cursor` | number | The 1-based Step cursor. |
| `delivery` | string | The delivery status, e.g. `applied`, `pending`, or empty. |
| `notices` | array of string | Driver diagnostics buffered before the renderer attached. Rendered as state, so repeating them on every `view` is idempotent and the renderer must not append them to the timeline. |
| `agents` | object of Agent | Keyed by agent id: `austin`, `tony`. |

### Agent

| Field | Type | Meaning |
| --- | --- | --- |
| `present` | bool | The session has a driver for this agent at all. |
| `connected` | bool | The agent currently holds the bridge endpoint. |
| `process` | string | The process state, e.g. `running`, `exited`. |
| `driver` | string | The driver name. |
| `model` | string | The model the agent runs. |
| `thinking` | string | The reasoning effort, empty when the driver has none. |
| `busy` | bool | The harness reports the agent working. |
| `activity` | string | A one-line description of what the agent is doing now. |

### Event

| Field | Type | Meaning |
| --- | --- | --- |
| `time` | string | RFC 3339 timestamp. |
| `kind` | string | `system`, `assistant`, `peer`, `activity`, `harness`, `user`, `error`, `verdict`, `model` (not sent by this version), `thinking` (not sent by this version). |
| `agent` | string | The speaking agent, or empty. |
| `peer` | string | The addressed agent, or empty. |
| `text` | string | The entry body. |
| `label` | string | The timeline header, e.g. `Austin → Tony`. Empty means "fall back to the speaker name". |

### Timeline entries and resumed history

A live `event` is a transcription of the bus event: `time`, `kind`, `agent`,
`peer` and `text` arrive as Core emitted them, and `label` is empty.

A resumed session replays its recorded history through the same `event` message,
because a renderer should not have to know that the two sources are different
shapes. The host maps a `sessionstore.TUIEntry` onto an `Event` like this, and the
mapping is fixed rather than heuristic:

| Event field | Source |
| --- | --- |
| `time`, `text` | `Time`, `Text` |
| `agent` | `Pane` (an agent id, or `duo` for a Core entry) |
| `label` | `Label` |
| `kind` | `error` when `Error`, else `verdict` when `Warning`, else `assistant` |
| `peer` | empty |

`TUIEntry` has no `kind`, which is why the mapping is boolean-driven. Two bus kinds
are deliberately dropped: `model` and `thinking`. They are not lost — a driver's
model and effort are read into `Agent.model` and `Agent.thinking` on every `view`
(`internal/session/service.go`), so the 500 ms heartbeat already refreshes them, and
a renderer that dropped every `model` event would still show the right model after
one heartbeat. That is what makes dropping them an MVP boundary rather than a bug.

## Renderer → session

Every message is an object with `type: "command"` and a `kind` discriminator.

| `kind` | Extra fields | Effect |
| --- | --- | --- |
| `submit_task` | `text` | Send the human's task text to Austin. |
| `escalate_to_goal` | — | Ask Core to move a Fast session into Goal. |
| `restart_agent` | `agent` | Restart one agent's process. |
| `set_language` | `language` | Switch the language Duo writes in. |
| `quit` | — | The human closed the window; the host closes the session. |

`escalate_to_goal` is only meaningful in Fast. Goal and Step refuse it, because a
session's mode is fixed once it starts. A renderer should offer the command only
when `view.mode` is `fast`; in the other modes it is a request the operator will see
refused rather than a state change.

Absent optional fields are omitted rather than sent as `null`.

Two commands a later version may add are deliberately absent: `set_model` and
`cycle_thinking`. Both need a driver's model catalog, and a picker built from a
catalog the MVP does not fetch would be a menu that silently does nothing.

## Why this shape

The renderer never needs to be a server, so it never binds a port and never
authenticates: the parent owns the pipes, and a renderer that dies is just EOF on
the parent's stdin. The protocol carries a full `View` rather than a diff, because
a few hundred bytes per event is cheaper than the state machine a partial update
would need, and because a renderer that missed a heartbeat is correct again on the
next one rather than permanently diverged.

The gate verbs — `SetPlan`, `SetReady`, `SetVerification` — are not on this
protocol and will not be. They are authorized by an agent's live bridge connection,
and a graphical frontend is the human, not an agent. A GUI that could sign a phase
would be a way to forge a peer's approval.
