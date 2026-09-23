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
- idle/stall recovery.

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

## Integration boundary

Austin is the integration worktree in v0.2. Duo merges Tony into Austin after REVIEW. Duo never automatically merges Austin back into the user's original branch.

That human-controlled final merge is an intentional safety boundary.
