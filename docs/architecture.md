# Architecture

Duo is intentionally split into a **thin Pi adapter** and an **authoritative Go coordination core**.

## Design goal

The core should enforce only what benefits from deterministic coordination:

- agent identity;
- peer routing;
- shared Plan versioning;
- phase transitions;
- signatures and evidence;
- Git worktree isolation;
- stale-signature detection;
- integration;
- idle/stall recovery;
- durable checkpointing and crash recovery;
- a safe, provably-lossless handoff back to the user's repository.

Everything else should remain flexible enough for the models to collaborate naturally.

## Components

### `internal/protocol`

Wire messages and stable Austin/Tony identifiers. No Git, Pi or UI logic should live here.

### `internal/transport`

Owns local TCP connections and the client registry. The coordinator sends semantic messages through this layer rather than touching `net.Conn` directly.

### `internal/project`

Owns the shared state machine:

```text
PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

It stores the shared Plan, Plan version, per-agent readiness, notes and evidence.

### `internal/coordinator`

The orchestration boundary. It interprets wire messages, routes peer messages, verifies phase evidence through the workspace manager, advances phases, detects stale signatures, and sends phase notices back to the agents.

### `internal/harness`

Tracks runtime activity independently from project state. If the project is unfinished and progress appears to have stopped, the harness nudges Austin to recover coordination.

### `internal/workspace`

Owns Git-specific isolation and evidence:

- creates Austin/Tony worktrees from one base commit;
- reports HEAD/dirty/ahead status;
- captures clean commit artifacts;
- merges Tony into Austin during INTEGRATE;
- leaves conflicts visible for resolution.

### `internal/agent`

Owns the two real Pi processes. Each `Session` starts Pi in its own pseudo-terminal (`creack/pty`), keeps a bounded raw-output ring buffer for native-attach replay, and tracks a process state that distinguishes `exited` from `failed`. `Manager` starts, resizes, restarts and stops both sessions and emits lifecycle events so the caller can journal them.

This layer also owns Pi session identity: unless `DUO_PI_COMMAND` already supplies a `--session-id`, Duo appends its own so Austin and Tony keep their own conversation across a restart.

### `internal/delivery`

Owns the handoff back to the human's repository. `Check` inspects the original repository **without modifying it** and decides whether auto-delivery is safe; `Deliver` fast-forwards the recorded branch only when that check passes. See [Integration and delivery boundary](#integration-and-delivery-boundary).

### `internal/sessionstore`

Owns durable state on disk: an atomically written `state.json` checkpoint, an append-only `events.jsonl` journal, a redacting `duo.log`, and an advisory `flock` lock that records the owner PID and hostname. Sessions are keyed by repository (`RepoID`) and session id under `~/.duo/sessions/`.

`events.jsonl` is both the diagnostic journal and the TUI transcript: `tui_entry` records are replayed into the panes on `--resume` (capped at 200 entries per pane), so a resumed session shows the same visible history it had before.

### `internal/recovery`

Owns the resume path: composing a snapshot from live state, validating it against Git, and reconciling the two. Reconciliation is deliberately conservative — any signature whose evidence can no longer be proved valid is revoked before agents start, so a crash cannot resurrect a stale approval.

### `internal/events`

A small in-process pub/sub bus carrying typed events (`system`, `assistant`, `peer`, `activity`, `harness`, `user`, `error`) from the coordinator, harness and transport to the UI. Delivery is non-blocking: a subscriber that cannot keep up drops events rather than stalling coordination.

### `internal/tui`

The terminal UI: model, renderer, layout, help, input decoding, mouse selection and in-pane markdown rendering. It is a projection of authoritative state, never a second source of truth. Frames are rebuilt from scratch and scheduled through a dirty tracker at about 60 FPS.

### `internal/terminal`

Low-level terminal ownership: raw mode, alt screen, mouse reporting, `modifyOtherKeys`, synchronized output, and the exact escape sequences used to hand the terminal to native Pi and take it back.

### `pi-extension`

A deliberately thin adapter:

```text
Pi event → Duo activity
Pi tool  → Duo request
Duo msg  → Pi steer
```

The extension should not become a second source of project truth.

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

## Why phases are checkpoints

Agents are allowed to behave opportunistically. For example Tony may begin reviewing Austin's diff before the formal transition from EXECUTE to REVIEW. Duo does not prevent that.

The phase determines what evidence is required to advance:

| Phase | Required evidence |
|---|---|
| PLAN | both approve the same Plan version |
| EXECUTE | clean commit artifact for each agent |
| REVIEW | each approves the exact peer HEAD reviewed |
| INTEGRATE | both approve the same clean integrated Austin HEAD |

This keeps coordination deterministic without making agent behavior rigid.

## Integration and delivery boundary

Integration **inside** Duo is intentionally asymmetric: Austin is the integration worktree, and Duo merges Tony into Austin after REVIEW. Conflicts stay visible in Austin's worktree for the agents or human to resolve; the core does not guess at a resolution.

The handoff **back to the human** is deliberately narrower than a merge. Once both agents sign INTEGRATE, Duo fast-forwards only the branch it recorded when the session started, and only to the final integrated HEAD. It never creates a merge commit, never rebases, and never rewrites history in the user's repository.

Delivery refuses — leaving the repository completely untouched — when the original repository:

- has uncommitted changes;
- is on a different branch than the one Duo recorded;
- has diverged from the final result, or the final result is not derived from the recorded base commit;
- is on a detached HEAD, or the recorded starting branch is unknown.

Two properties make this safe to automate:

1. **Check before write.** `internal/delivery` computes the whole decision from read-only Git queries before touching anything, so a refusal costs nothing.
2. **Idempotent apply.** Delivery is a no-op whenever the final HEAD is already reachable from the current HEAD (`git merge-base --is-ancestor <final-head> HEAD`), not only when the two SHAs are identical. That is what lets a crash between the fast-forward and the `DONE` write be reconciled without double-applying, and it also recognizes a merge the human resolved themselves: after the human finishes the handoff with their own `git merge --no-ff <final-head>`, `duo apply` reports the result as already applied and keeps the human's commit rather than demanding a fast-forward.

So the boundary is not "Duo never moves your branch". It is: **Duo only ever moves the recorded branch forward, by an amount Git itself proves to be lossless, and refuses rather than guesses.** When it refuses, the human finishes the handoff from the printed final HEAD with `git merge --no-ff <final-head>` and re-runs `duo apply`, which then recognizes the result as already applied. A cherry-pick does not work here: it leaves the final HEAD out of the branch's history, so `duo apply` refuses again until the histories are merged.

This asymmetry is not configurable yet; see the roadmap entry for a configurable integration strategy.
