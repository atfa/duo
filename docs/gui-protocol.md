# Duo GUI protocol (v1)

This document is the complete contract between Duo Core and the GPUI graphical
frontend. It is deliberately small: it carries the state the window displays and
the commands a human can issue, while the session remains authoritative.

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

- Go (`duo gui`) spawns the Rust renderer (`xduo-renderer`) and keeps its stdin and
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
| `terminal_output` | `agent`, `data` | One base64-encoded chunk of that agent's PTY output; decoded chunks are at most 16 KiB. |
| `terminal_error` | `agent`, `text` | The attached terminal was detached after a transport or PTY-output error. |

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
| `version` | string | The Duo version, e.g. `v0.12.2`. |
| `session` | string | The session id. |
| `mode` | string | `fast`, `goal` or `step`. |
| `phase` | string | The workflow phase, e.g. `PLAN`, `EXECUTE`, `REVIEW`. |
| `started` | bool | Whether the session has started its task; xduo uses this with `phase` for quit confirmation. |
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
| `verification` | Verification | The Fast-mode verdict bound to its reviewed HEAD; empty fields mean no verdict. |
| `step_evidence` | array of StepVerification | Per-step review and delivery records in queue order (empty when the session has no step queue). |
| `notices` | array of string | Driver diagnostics buffered before the renderer attached. Rendered as state, so repeating them on every `view` is idempotent and the renderer must not append them to the timeline. |
| `agents` | object of Agent | Keyed by agent id: `austin`, `tony`. |
| `available_drivers` | array of string | Driver identities that passed the last local availability probe. |
| `agent_driver_names` | array of string | Driver identities supported by this Duo build or installed as plugins. |
| `desktop_agents` | array of DesktopAgent | Machine-local executable paths and probe results shown in Settings. |
| `agent_scan_busy` | bool | Whether an initial scan or an explicit Settings operation is running. |
| `agent_scan_error` | string | Last settings read or scan error, if any. |
| `project_selection` | ProjectSelection or null | Welcome-page project and unfinished-session choices; `repository` stays empty until a session starts. |

xduo saves the versioned machine-local catalog at
`os.UserConfigDir()/xduo/agents.json` with owner-only file permissions. The first
launch shows the welcome window immediately and scans the login-shell PATH in the
background only when this file is absent. Later launches use the saved catalog;
Refresh rescans explicitly and preserves manually registered paths. The app's
managed executable shims feed the same paths to driver probes, model listing and
agent launches. CLI and TUI sessions do not read this desktop-only catalog.

`DesktopAgent` contains `name` (driver identity), `cli` (the CLI name from its
driver manifest), `path`, `source` (`path`, `manual`, or `detected`), `available`,
and an optional `error`.

`ProjectSelection` contains the validated repository `root`, `busy`, an inline
`error`, and `sessions`. Each session choice has its ID, mode, phase, update time,
and either an `active` marker or a record error. Active and broken sessions are
shown for context but cannot be resumed. Choosing a project never starts a
session; the renderer must send `start_session` after the user chooses New or
Resume.

### Agent

| Field | Type | Meaning |
| --- | --- | --- |
| `present` | bool | The session has a driver for this agent at all. |
| `worktree_path` | string | The agent's session worktree path. |
| `worktree_branch` | string | The agent's worktree branch. |
| `worktree_head` | string | The worktree's projected HEAD. |
| `diffstat` | Diffstat | Cached Git change counts against `base_commit`, refreshed asynchronously by the host. |
| `diff_preview` | DiffPreview | Cached, bounded tracked patch excerpt against `base_commit`, refreshed asynchronously by the host. |
| `ready_note` | string | The agent's current phase sign-off note, if any. |
| `ready_evidence` | string | Evidence attached to the agent's current phase sign-off, if any. |
| `connected` | bool | The agent currently holds the bridge endpoint. |
| `process` | string | The process state, e.g. `running`, `exited`. |
| `driver` | string | The driver name. |
| `model` | string | The model the agent runs. |
| `live_model_switch` | bool | Whether this driver's model change takes effect without restarting its process. |
| `thinking` | string | The reasoning effort, empty when the driver has none. |
| `busy` | bool | The harness reports the agent working. |
| `activity` | string | A one-line description of what the agent is doing now. |
| `work` | WorkPreview | Structured fields for the native work preview, copied from the agent runtime. |
| `models` | array of Model | Options returned by this agent's driver catalog; empty while loading or when no options were returned. |
| `models_loaded` | bool | The catalog request finished, whether successfully or with an error. |
| `models_error` | string | Catalog error, including when the driver publishes no catalog. Omitted when successful. |
| `native_open` | bool | A native or embedded terminal attach is pending or active; xduo uses it to show or switch the corresponding terminal pane. |

### Diffstat, Verification and StepVerification

`diffstat` has `state`, `files`, `insertions`, `deletions`, `untracked`, and
optional `error`. `state` is `unavailable` when the agent has no worktree,
`loading` while Git is being queried, `ready` when counts are available, and
`error` when Git could not read the worktree. Counts describe tracked changes
from `base_commit`; `untracked` separately counts untracked paths, whose line
contents are not read. The host refreshes summaries asynchronously and caches
them for at least three seconds, so Git latency does not delay a view heartbeat.

`diff_preview` has `state`, `text`, `truncated`, and optional `error`. It follows
the same availability and loading states as `diffstat`. When ready, `text`
contains at most the first 12 KiB of the tracked patch against `base_commit`;
`truncated` tells the renderer when more patch output existed. Untracked file
contents are excluded. The host disables fsmonitor, external diff drivers,
textconv, and color for these automatic Git reads, applies a command timeout, and
continues draining output after the preview limit so a large patch stays bounded
without blocking a heartbeat. A renderer can offer a per-agent “Inspect changes”
view and display the returned text in a scrollable area; it should show a
truncation marker when `truncated` is true and use the existing state/error fields
for loading and failure feedback.

`verification` has `status`, `head`, `note`, and optional RFC 3339 `at`. It is
the existing Fast-mode verification record. `step_evidence` contains one record
per step queue entry: `index` (1-based), `title`, `done`, submitted `head`, a
nested `verification` with the same shape, `delivery_head`, and optional
`delivered_at`. These records are read-only copies of project state. Readiness
notes and evidence are carried per agent in `ready_note` and `ready_evidence`.
Long evidence text is bounded on the wire (500 runes for notes and 1000 for
evidence, verification notes, and step titles).

### WorkPreview and Model

`work` carries `provider_active`, `tool_depth`, `human_attached`, `last_activity`,
`tool`, `tool_detail`, `tool_failed`, `stream_tail`, `last_error`,
`context_tokens`, `context_window`, `tokens_per_second`, `tools`, `turn_started`,
`turn_elapsed_ms`, `tool_started`, `tool_elapsed_ms`, `token_speed_at`, `failures`,
and `recent`. The elapsed values are nonnegative milliseconds from the corresponding
start time to the time that view was projected. An unset start time produces zero;
the host refreshes these values with each view heartbeat. The absolute RFC 3339
start values remain available for clients that need them.
Each recent tool note has `name`, `detail`, `ok`, and `duration_ms`. These values
are copied from `session.View`; zero and empty values mean the driver has no data.
Text in this preview is bounded on the wire (tool names 100 runes, tool details
500 runes, streamed text 1200 runes, error text 500 runes, and recent note text
300 runes) so a long stream tail cannot inflate every heartbeat frame.

A model option carries `provider`, `id`, `reference`, `thinking`, and `images`.
The reference is the driver's canonical selectable spelling, including its bare
reference behavior. The host asks each driver's existing catalog concurrently;
heartbeats continue while a catalog command is running, and the result appears in
the next view heartbeat. The catalog is never assembled from a built-in list.

### Event

| Field | Type | Meaning |
| --- | --- | --- |
| `time` | string | RFC 3339 timestamp. |
| `kind` | string | `system`, `assistant`, `peer`, `activity`, `harness`, `user`, `error`, `verdict`, `model`, or `thinking`. |
| `agent` | string | The speaking agent, or empty. |
| `peer` | string | The addressed agent, or empty. |
| `text` | string | The entry body. |
| `label` | string | The timeline header, e.g. `Austin → Tony`. Empty means "fall back to the speaker name". |
| `provider` | string | The provider reported by a `model` event. |
| `model` | string | The model id reported by a `model` event. |
| `thinking` | string | The reasoning level reported by a `thinking` event. |

### Timeline entries and resumed history

A live `event` is a transcription of the bus event: `time`, `kind`, `agent`,
`peer`, `text`, and any structured model or thinking fields arrive as Core emitted
them, and `label` is empty. Model and thinking events may have empty `text`; the
renderer presents them as activity entries using their structured fields and
coalesces consecutive duplicate state events. Other empty error entries remain
visible with a clear fallback rather than as blank messages.

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

`TUIEntry` has no `kind`, which is why the mapping is boolean-driven. Replayed TUI
history does not synthesize model or thinking events; live events carry those
changes in their structured fields.

## Renderer → session

Every message is an object with `type: "command"` and a `kind` discriminator.

| `kind` | Extra fields | Effect |
| --- | --- | --- |
| `submit_task` | `text` | Send the human's task text to Austin. |
| `escalate_to_goal` | — | Ask Core to move a Fast session into Goal. |
| `restart_agent` | `agent` | Restart one agent's process. |
| `set_language` | `language` | Switch the language Duo writes in. |
| `set_model` | `agent`, `provider`, `model` | Select a model option returned by that agent's driver catalog. |
| `cycle_thinking` | `agent` | Advance that agent's reasoning effort using the session command. |
| `set_driver` | `agent`, `driver` | Switch one agent to a locally available driver. The host probes and prepares it before replacing the current process. |
| `open_project` | `path` | Validate a selected Git folder and show its New/Resume choices in the current welcome window. From a running session this opens a new welcome window with the folder preselected. |
| `start_session` | `path`, optional `session_id` | Start a new session when the ID is absent, or resume the named unfinished session. The host verifies that the root is the selected project and rechecks that a resume target is still available. |
| `back_to_welcome` | — | Clear the selected project without starting a session. |
| `refresh_agents` | — | Rescan the login-shell PATH and save the machine-local catalog. Manually added paths are retained. |
| `add_agent` | `driver`, `path` | Register an absolute executable path for a known driver after validating and probing it. |
| `terminal_open` | `agent` | Attach the in-app terminal view to that agent's live PTY. |
| `terminal_close` | `agent` | Detach that in-app terminal without stopping the agent. |
| `terminal_input` | `agent`, `data` | Write standard-base64-decoded bytes (at most 64 KiB) to that agent's PTY. |
| `terminal_resize` | `agent`, `cols`, `rows` | Resize only that agent's PTY; both dimensions must be 1–512. |
| `open_native` / `close_native` | `agent` | Legacy launcher-based attach commands. The built-in `duo gui` no longer configures an external terminal launcher; xduo uses `terminal_*`. |
| `quit` | — | The human closed the window; the host closes the session. |

`escalate_to_goal` is only meaningful in Fast. Goal and Step refuse it, because a
session's mode is fixed once it starts. A renderer should offer the command only
when `view.mode` is `fast`; in the other modes it is a request the operator will see
refused rather than a state change.

Absent optional fields are omitted rather than sent as `null`.

`set_model` should be offered from the matching agent's returned catalog. A driver
that has no catalog reports that through `models_error`; the renderer must not
invent model choices. `cycle_thinking` follows the driver's session semantics and
may restart an agent when that driver cannot change effort live.

`live_model_switch` is copied from the driver's declared capability. When false, a
model change restarts that agent's process to apply the selected model; when true,
the running process applies it without a restart.

The header's folder picker is provided by GPUI. Cancelling it sends no command. A
selected repository opens in a separate xduo window, so an active task is not
silently moved to another checkout. Clicking an agent name shows the drivers that
the local Duo installation successfully probes. Choosing one replaces that
agent's process while retaining its worktree and workflow state; its model and
thinking selection are resolved again from the new driver's manifest.

The embedded terminal uses the existing stdio protocol; the renderer never receives
a socket path or an agent bridge token. PTY replay is limited to 1 MiB and is split
into the same 16 KiB output chunks as live data. The renderer queues at most 64
incoming frames; when full, its stdin reader blocks, propagating backpressure over
the pipe. The host independently buffers at most 64 chunks (1 MiB) per terminal.
On overflow it stops accepting PTY bytes, preserves already-queued chunks, emits
`terminal_error`, and detaches rather than silently losing data. If a blocked
renderer pipe prevents that drain, the host waits at most two seconds before
detaching; it keeps the bounded writer alive to flush accepted chunks and the
error if the renderer resumes. If it remains blocked for a further 30 seconds, the
host closes the renderer input pipe to release the writer and reports a host
disconnect. Closing a terminal detaches it and leaves the agent running.
`terminal_resize` applies only to the named agent, so the other agent's PTY
dimensions are unchanged. This is separate from the structured work preview shown
in the agent card: the preview reads bounded state, while the bottom terminal pane
renders the attached agent's real PTY stream.

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
