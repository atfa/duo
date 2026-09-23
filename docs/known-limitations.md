# Known limitations — v0.2-alpha

Duo is an experimental runtime. The collaboration model works, but the current release intentionally leaves several areas unfinished.

## Fixed two-agent topology

The runtime currently assumes exactly two agents named **Austin** and **Tony**. Agent names, count and role templates are not yet configurable.

## Pi-specific adapter

v0.2 targets Pi. The Go core is structured so other adapters could be added later, but no second agent runtime is currently supported.

## Headless only

There is no integrated TUI yet. You run Duo Core plus two Pi terminals. The planned v0.3 UI will provide side-by-side agent panes and one global human composer.

## In-memory collaboration state

Project phase, Plan signatures and runtime activity are currently process-local. A Duo Core restart does not yet provide full session resume/recovery semantics.

Git worktrees and branches remain on disk, but the collaboration state machine itself is not persisted as a durable session database.

## Worktree cleanup is manual / conservative

Worktrees are preserved when Duo stops so unfinished work is not destroyed. Automatic lifecycle cleanup and pruning are not yet a polished user-facing workflow.

## Integration is intentionally asymmetric

In v0.2, Tony is merged into Austin and Austin becomes the integration worktree. This is simple and deterministic but not yet configurable.

## Merge conflicts are not automatically solved by the core

Conflicts remain in Austin's worktree for the agents/human to resolve. Duo does not attempt low-level automatic conflict resolution behind the user's back.

## No automatic merge to the human branch

This is intentional rather than a bug. The integrated Duo branch must be reviewed and merged/cherry-picked by the human.

## POSIX-oriented scripts

The supplied helper scripts are Bash-oriented and have primarily been exercised on macOS/Linux. Windows support and shell portability have not yet been hardened.

## Local TCP trust model

The bridge uses a localhost TCP endpoint and currently assumes a trusted local development environment. Authentication, encryption and remote/multi-host operation are not goals of v0.2.

## Harness is heuristic

The harness detects idle/stall situations from runtime activity. It improves liveness, but it is not a formal guarantee that every model/provider failure can be recovered automatically.

## Agent behavior still matters

Duo supplies coordination infrastructure; it does not make model judgment infallible. Agents can still misunderstand code, produce bad plans, or approve weak work. Git evidence makes completion auditable, not automatically correct.

## Plan is intentionally lightweight

The shared Plan is currently a versioned text artifact rather than a full structured task graph. This is deliberate for v0.2; richer task/artifact tracking may be added only if real usage proves it useful.
