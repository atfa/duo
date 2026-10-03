# Architecture

Duo is intentionally split into **thin agent adapters** — one per supported agent CLI — and an **authoritative Go coordination core**.

## Design goal

The core should enforce only what benefits from deterministic coordination:

- agent identity;
- peer routing;
- session mode (Fast or Goal) and mode-scoped phase transitions;
- shared Plan versioning (Goal);
- verification, signatures and evidence;
- Git worktree isolation;
- stale-verification and stale-signature detection;
- integration (Goal);
- idle/stall recovery;
- durable checkpointing and crash recovery;
- a safe, provably-lossless handoff back to the user's repository.

Everything else should remain flexible enough for the models to collaborate naturally.

## Components

### `internal/protocol`

Wire messages and stable Austin/Tony identifiers. No Git, agent-CLI or UI logic should live here.

### `internal/transport`

Owns local TCP connections and the client registry. The coordinator sends semantic messages through this layer rather than touching `net.Conn` directly.

### `internal/project`

Owns the shared state machine and the per-session mode. A small internal workflow policy per mode keeps mode checks from spreading across layers.

```text
Fast: RUNNING → VERIFY → DONE        (issue_found returns VERIFY → RUNNING)
Goal: PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

It stores the mode, phase, shared Plan and version (Goal), the verification result and target commit (Fast), and per-agent readiness, notes and evidence. A legacy snapshot with no recorded mode is restored as Goal.

### `internal/coordinator`

The orchestration boundary. It interprets wire messages, dispatches once on the session mode, routes peer messages, verifies phase evidence through the workspace manager, enforces the verification gates, advances phases, detects stale verification and signatures, and sends phase notices back to the agents. Fast rejects `duo_set_plan` and a Tony `duo_set_status`; only Tony may call `duo_set_verification`, and only while the session is in `VERIFY`.

### `internal/harness`

Tracks runtime activity independently from project state. In Goal it nudges when the project is unfinished and both agents have gone idle. In Fast it nudges the agent that owns the current phase — Austin in `RUNNING`, Tony in `VERIFY` — so a verifier waiting on nothing cannot block the driver, and it escalates a stuck `RUNNING` episode into one diagnosis request to Tony.

### `internal/workspace`

Owns Git-specific isolation and evidence:

- creates Austin/Tony worktrees from one base commit;
- reports HEAD/dirty/ahead status;
- captures clean commit artifacts;
- merges Tony into Austin during INTEGRATE;
- leaves conflicts visible for resolution.

### `internal/driver`

The Duo Driver Plugin Protocol v1: the wire types, the RPC client, the plugin-side
`Serve` helper, manifest validation, and discovery by executable name. Duo Core
reaches every coding agent through this package and nothing else, so adding an agent
means adding a plugin rather than editing Core. See
[docs/driver-plugin-protocol.md](driver-plugin-protocol.md) for the specification.

Two implementations of one interface, `Caller`. A `Client` speaks it over a pipe to
a plugin process; a `Builtin` speaks it by calling a `Handler` in the same process.
They are the same contract, which is what lets a driver Duo ships be introduced
through exactly the interface a third party implements.

### `plugins/`

The drivers Duo ships, each a Driver Plugin:

- `plugins/pi` — the Pi CLI, paired with `pi-extension/` as its Agent Adapter
- `plugins/agy` — the Antigravity CLI, including a transcript observer, because agy
  has no extension API
- `plugins/opencode` — the opencode CLI, paired with `opencode-extension/`
- `cmd/duo-plugin-example` — the reference plugin, and the positive control for
  contract tests

A plugin is split in two, and the split is the design. The **Host Adapter** answers
the protocol and is all Duo Core ever sees. The **Agent Adapter** runs inside the
agent process, or observes it from outside, and is invisible to Core — which is why
Core cannot tell a native extension from a transcript watcher from MCP, and does not
need to.

### `internal/agent`

Owns the two agent processes behind one `Driver` interface: process lifecycle, PTY
interaction, resize, native-attach replay, and the distinction between `exited` and
`failed`. A `Session` asks its driver for a launch specification and runs what comes
back, so what gets launched is the driver's business.

It knows nothing about any agent. There is no field in `Config` for a session
identity, a log file or an activity sink, and no branch on a driver name. What Core
knows about an agent is what that agent's plugin declared: `Manifest()`,
`Capabilities()`, `DriverState()`. `Manager` starts, resizes, restarts and stops both
sessions, emits lifecycle events for the durable log, and releases the drivers at the
end of a session — a plugin may own background work, such as a transcript observer,
that must not outlive it.

### `internal/models`

Being replaced by the drivers' own `models` methods. The model catalog comes from the
same agent installation Duo launches, and after each driver ships its parser the
per-CLI knowledge — `pi --list-models`, `agy models`, `opencode models`, and each one's
default-model rule — belongs to that driver rather than to Core.

### `internal/delivery`

Owns the handoff back to the human's repository. `Check` inspects the original repository **without modifying it** and decides whether auto-delivery is safe; `Deliver` fast-forwards the recorded branch only when that check passes. See [Integration and delivery boundary](#integration-and-delivery-boundary).

### `internal/sessionstore`

Owns durable state on disk: an atomically written `state.json` checkpoint, an append-only `events.jsonl` journal, a redacting `duo.log`, and an advisory `flock` lock that records the owner PID and hostname. Sessions are keyed by repository (`RepoID`) and session id under `~/.duo/sessions/`.

`events.jsonl` is both the diagnostic journal and the TUI transcript: `tui_entry` records are replayed into the conversation timeline on `--resume` (capped at 200 entries per speaker), so a resumed session shows the same visible history it had before.

### `internal/recovery`

Owns the resume path: composing a snapshot from live state, validating it against Git, and reconciling the two. Reconciliation is deliberately conservative — any verification or signature whose evidence can no longer be proved valid is revoked before agents start, so a crash cannot resurrect a stale approval. In Fast, a `passed` verification whose commit is no longer Austin's HEAD deterministically returns the session to `RUNNING`; recovery never otherwise moves a session backwards.

### `internal/events`

A small in-process pub/sub bus carrying typed events (`system`, `assistant`, `peer`, `activity`, `harness`, `user`, `error`, `verdict`, `model`, `thinking`) from the coordinator, harness and transport to the UI. Delivery is non-blocking: a subscriber that cannot keep up drops events rather than stalling coordination.

### `internal/tui`

The terminal UI: model, renderer, layout, help, input decoding, mouse selection and markdown rendering in the conversation timeline. It is a projection of authoritative state, never a second source of truth. Frames are rebuilt from scratch and scheduled through a dirty tracker at about 60 FPS.

### `internal/terminal`

Low-level terminal ownership: raw mode, alt screen, mouse reporting, `modifyOtherKeys`, synchronized output, and the exact escape sequences used to hand the terminal to native Pi and take it back.

### `pi-extension`

A deliberately thin adapter:

```text
Pi event → Duo activity
Pi tool  → Duo request
Duo msg  → Pi steer
```

It publishes a mode-aware system prompt: a common base (worktree model and shared rules) plus exactly one policy section (`Fast driver`, `Fast verifier`, or `Goal`), selected from `DUO_MODE`. Tool registration is mode-gated too — `duo_set_plan` is offered only in Goal and `duo_set_verification` only in Fast — but the Go core still rejects the wrong tool. The extension should not become a second source of project truth.

### `opencode-extension`

The same three-way mapping for opencode, over opencode's own plugin API:

```text
opencode event → Duo activity
Duo tool       → Duo request
Duo msg        → opencode prompt on the live session
```

`protocol.ts`, `transport.ts` and `mode.ts` are shared verbatim with `pi-extension`: both bridges speak wire protocol version 1, so only the host binding differs. Inbound Duo messages become `client.session.promptAsync` calls on the tracked session, which is how a peer message or harness nudge reaches the agent mid-turn instead of being typed at a PTY.

opencode exposes `experimental.chat.system.transform` with a mutable `system: string[]`, so the Duo policy is appended as one extra section instead of replacing anything opencode built. The plugin is inert unless `DUO_ACTIVE=1`, so a normal opencode session behaves exactly as it would without Duo installed.

### `internal/mcp`

Serves the same coordination tools over the Model Context Protocol (JSON-RPC 2.0 on stdio) instead of a socket bridge, for agent CLIs that speak MCP. Each tool is validated and forwarded to the coordinator exactly as the bridge version is, so the state machine has one implementation and one set of gates; the MCP layer adds transport, not policy.

## Why worktrees instead of file locks?

Tool-level write locks are incomplete. An agent can modify files through `edit`, `write`, shell commands, generators, scripts, Git operations and many other paths.

Worktrees turn accidental overwrite into a Git integration problem instead:

```text
Austin edits foo.go in branch A
Tony   edits foo.go in branch B
             ↓
        no lost work
             ↓
     merge conflict if needed
```

This failure mode is explicit, recoverable and auditable.

## Why PLAN is not a write lock

Duo models two engineers, not a synchronized transaction protocol.

Waiting for peer feedback should not prohibit an engineer from reading code, testing an idea, or building a prototype. Therefore PLAN permits provisional edits inside isolated worktrees. The hard boundary is **formal agreement and artifact sign-off**, not every file write.

Fast needs no such negotiation: there is no shared Plan, and the only hard boundary is the verification result that gates delivery.

## Why phases are checkpoints

Agents are allowed to behave opportunistically. For example Tony may begin reviewing Austin's diff before the formal transition from EXECUTE to REVIEW. Duo does not prevent that.

The phase determines what evidence is required to advance:

| Phase | Required evidence |
|---|---|
| RUNNING (Fast) | a clean committed Austin HEAD; `duo_set_status ready=true` |
| VERIFY (Fast) | Tony's `passed` or `issue_found` + note for that exact Austin HEAD |
| PLAN (Goal) | both approve the same Plan version |
| EXECUTE (Goal) | clean commit artifact for each agent |
| REVIEW (Goal) | each approves the exact peer HEAD reviewed |
| INTEGRATE (Goal) | both approve the same clean integrated Austin HEAD |

This keeps coordination deterministic without making agent behavior rigid.

## Integration and delivery boundary

Integration **inside** Duo is intentionally asymmetric: Austin is the integration worktree, and Duo merges Tony into Austin after REVIEW. Conflicts stay visible in Austin's worktree for the agents or human to resolve; the core does not guess at a resolution. Fast has no integration step by design: Tony is read-only, so Austin's branch is already the deliverable and only Austin's HEAD is ever delivered.

The handoff **back to the human** is deliberately narrower than a merge. Once the artifact is approved — both agents sign INTEGRATE in Goal, or Tony's verification passes in Fast — Duo fast-forwards only the branch it recorded when the session started, and only to the final result. It never creates a merge commit, never rebases, and never rewrites history in the user's repository.

Delivery refuses — leaving the repository completely untouched — when the original repository:

- has uncommitted changes;
- is on a different branch than the one Duo recorded;
- has diverged from the final result, or the final result is not derived from the recorded base commit;
- is on a detached HEAD, or the recorded starting branch is unknown.

A task that changes no repository files is a no-op: once the final commit is already an ancestor of the current HEAD, delivery succeeds and records `DONE` even if the working tree is dirty. Every other delivery still goes through the checks above.

Two properties make this safe to automate:

1. **Check before write.** `internal/delivery` computes the whole decision from read-only Git queries before touching anything, so a refusal costs nothing.
2. **Idempotent apply.** Delivery is a no-op whenever the final HEAD is already reachable from the current HEAD (`git merge-base --is-ancestor <final-head> HEAD`), not only when the two SHAs are identical. That is what lets a crash between the fast-forward and the `DONE` write be reconciled without double-applying, and it also recognizes a merge the human resolved themselves: after the human finishes the handoff with their own `git merge --no-ff <final-head>`, `duo apply` reports the result as already applied and keeps the human's commit rather than demanding a fast-forward.

So the boundary is not "Duo never moves your branch". It is: **Duo only ever moves the recorded branch forward, by an amount Git itself proves to be lossless, and refuses rather than guesses.** When it refuses, the human finishes the handoff from the printed final HEAD with `git merge --no-ff <final-head>` and re-runs `duo apply`, which then recognizes the result as already applied. A cherry-pick does not work here: it leaves the final HEAD out of the branch's history, so `duo apply` refuses again until the histories are merged.

This asymmetry is not configurable yet; see the roadmap entry for a configurable integration strategy.
