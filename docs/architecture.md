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

## State ownership and module boundaries

| Area | Owns | Boundary to preserve |
|---|---|---|
| `internal/project` | Mode, phase, Plan (Goal), verification (Fast), signatures and evidence | The authoritative state machine; UI and adapters do not keep competing copies. |
| `internal/coordinator` | Wire requests, gates, phase transitions, peer routing and notices | Apply mode policy once and validate claims against Git evidence. |
| `internal/transport`, `internal/protocol`, `internal/mcp` | Local bridge messages, stable identities and stdio MCP transport | Transport carries requests; it does not own workflow policy. |
| `internal/driver`, `plugins/`, `internal/agent` | Plugin contract/discovery, per-agent CLI behavior and process/PTY lifecycle | Core has no driver-name branches; plugins own CLI flags, identity and capabilities. |
| `internal/models` | A driver's model catalog and how a reported reference is spelled | The catalog arrives over the plugin protocol; Core never builds a catalog command of its own, and a driver without one is reported rather than guessed at. |
| `internal/workspace` | Isolated worktrees, commit evidence and Goal integration | Austin and Tony never edit the same worktree; conflicts remain visible for resolution. |
| `internal/recovery`, `internal/sessionstore` | Snapshot reconciliation, journal, transcript replay and session lock | Revoke unverifiable approvals before agents start; never discard user files or rewrite user history. |
| `internal/delivery` | Read-only safety decision and final handoff | Move only the recorded branch by a provably safe fast-forward; otherwise refuse without changing the checkout. |
| `internal/harness`, `internal/events` | Runtime activity tracking, nudges and event fan-out | Harness observes runtime state; events are non-blocking and do not become project state. |
| `internal/tui` | Input, rendering, help and timeline | A projection of project state, never another source of truth. |
| `pi-extension/`, `opencode-extension/` | Agent-side tool, activity and prompt adapters | Keep the adapter thin; Go Core remains responsible for shared state and authorization. |

The Driver Plugin Protocol is specified in [driver-plugin-protocol.md](driver-plugin-protocol.md),
and the agent-side socket contract in [duo-bridge-protocol.md](duo-bridge-protocol.md).

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

This integration strategy is fixed: Austin is the integration worktree in Goal, and
Fast delivers Austin's verified commit directly.

## Current constraints

- The topology has two fixed roles, Austin and Tony. Fast can escalate to Goal, but
  Goal cannot downgrade to Fast.
- Session state is local to a machine and repository path. Recovery restores
  coordination state and agent session identities, not model reasoning.
- Duo never rewrites the user's Git history. Delivery is a checked fast-forward;
  when it cannot prove that safe, it leaves the repository untouched and reports
  the final commit for a human-managed merge.
- The bridge uses a random token on a localhost TCP endpoint. It is not designed
  for remote or untrusted networks; see [SECURITY.md](../SECURITY.md).
