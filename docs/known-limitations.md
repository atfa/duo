# Known limitations — v0.3.1

Duo is an experimental runtime. The collaboration model works, but the current release intentionally leaves several areas unfinished.

## Fixed two-agent topology

The runtime currently assumes exactly two agents named **Austin** and **Tony**. Agent names, count and role templates are not yet configurable.

## Pi-specific adapter

Duo targets Pi. The Go core is structured so other adapters could be added later, but no second agent runtime is currently supported.

## Terminal UI scope

Duo provides side-by-side summary panes and one global human composer. Rich Pi features still run in the native Pi terminal reached through `Ctrl+A` or `Ctrl+T`; the summary panes are not terminal emulators.

The composer is currently single-line, and pane history does not yet support scrolling.

## In-memory collaboration state

Project phase, Plan signatures and runtime activity are currently process-local. A Duo Core restart does not yet provide full session resume/recovery semantics.

Git worktrees and branches remain on disk, but the collaboration state machine itself is not persisted as a durable session database.

## Worktree cleanup is manual / conservative

Worktrees are preserved when Duo stops so unfinished work is not destroyed. Automatic lifecycle cleanup and pruning are not yet a polished user-facing workflow.

## Integration is intentionally asymmetric

Tony is merged into Austin and Austin becomes the integration worktree. This is simple and deterministic but not yet configurable.

## Merge conflicts are not automatically solved by the core

Conflicts remain in Austin's worktree for the agents/human to resolve. Duo does not attempt low-level automatic conflict resolution behind the user's back.

## No automatic merge to the human branch

This is intentional rather than a bug. The integrated Duo branch must be reviewed and merged/cherry-picked by the human.

## POSIX-oriented scripts

The supplied helper scripts are Bash-oriented and have primarily been exercised on macOS/Linux. Windows support and shell portability have not yet been hardened.

## Local TCP trust model

Each Duo run uses a dynamic localhost port and a random session token. This isolates ordinary Pi processes and concurrent Duo projects, but the protocol is not designed for remote or untrusted networks.

## PTY and process recovery

Native Pi sessions currently use the platform `script` utility. Terminal resize is not propagated directly to the hidden PTY, and an exited Pi process requires restarting Duo; automatic agent restart is not implemented yet.

## Harness is heuristic

The harness detects idle/stall situations from runtime activity. It improves liveness, but it is not a formal guarantee that every model/provider failure can be recovered automatically.

## Agent behavior still matters

Duo supplies coordination infrastructure; it does not make model judgment infallible. Agents can still misunderstand code, produce bad plans, or approve weak work. Git evidence makes completion auditable, not automatically correct.

## Plan is intentionally lightweight

The shared Plan is currently a versioned text artifact rather than a full structured task graph. This is deliberate; richer task/artifact tracking may be added only if real usage proves it useful.
