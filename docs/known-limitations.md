# Known limitations — v0.4.0

Duo is an experimental runtime. The collaboration model works, but the current release intentionally leaves several areas unfinished.

## Fixed two-agent topology

The runtime currently assumes exactly two agents named **Austin** and **Tony**. Agent names, count and role templates are not yet configurable.

## Pi-specific adapter

Duo targets Pi. The Go core is structured so other adapters could be added later, but no second agent runtime is currently supported.

## Terminal UI scope

Duo provides side-by-side summary panes and one global human composer. Rich Pi features still run in the native Pi terminal reached through `Ctrl+A` or `Ctrl+T`; the summary panes are not terminal emulators.

The composer is currently single-line, and pane history does not yet support scrolling.

Frames are rebuilt from scratch (no partial-damage/diff updates) and capped at about 60 FPS by the renderer scheduler. This keeps redraw correctness easy to reason about, but very large terminals do redraw the whole screen on each dirty frame rather than only the changed regions.

## Durable state is a checkpoint, not a transcript

Collaboration state now survives a crash. Phase, Plan version, signatures, evidence, the worktree record and per-agent Pi session identity are persisted to `~/.duo/sessions/<repo-id>/<session-id>/state.json` and validated against Git when a session is resumed.

What is persisted is the *state machine*, not the agents' reasoning. A resumed Austin or Tony keeps its own Pi conversation history and the shared Plan, but Duo does not summarize or replay what was in flight. Recovery is also deliberately conservative: it revokes any signature it cannot prove is still valid, so a crash can legitimately cost a re-sign-off.

Recovery repairs bookkeeping; it never rewrites your Git history and never discards uncommitted files. A dirty worktree is reported, not cleaned.

## Session state is local and path-bound

`~/.duo/sessions/` is per machine and keyed by repository path, so a session is not portable across machines and does not follow a moved or renamed checkout.

## No automatic crash restart

Duo restarts Austin and Tony when it starts, and an exited agent can be restarted with `Ctrl+R` / `Ctrl+Y`. A dead Duo Core still requires a human to run `duo --resume`; Duo does not supervise or resurrect itself.

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

Native Pi sessions use direct Go-owned PTYs; host SIGWINCH is coalesced and resizes both Pi PTYs, even while detached. Manual restart is available for exited agents, but automatic crash restart is not implemented. Reattach replays at most 1 MiB of raw output, not a reconstructed terminal screen. Below 60×18 Duo shows a bounded too-small notice rather than rendering a full UI.

## Harness is heuristic

The harness detects idle/stall situations from runtime activity. It improves liveness, but it is not a formal guarantee that every model/provider failure can be recovered automatically.

## Agent behavior still matters

Duo supplies coordination infrastructure; it does not make model judgment infallible. Agents can still misunderstand code, produce bad plans, or approve weak work. Git evidence makes completion auditable, not automatically correct.

## Plan is intentionally lightweight

The shared Plan is currently a versioned text artifact rather than a full structured task graph. This is deliberate; richer task/artifact tracking may be added only if real usage proves it useful.
