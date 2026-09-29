# Known limitations

Duo is an experimental runtime. The collaboration model works, but the current release intentionally leaves several areas unfinished.

## Fixed two-agent topology

The runtime currently assumes exactly two agents named **Austin** and **Tony**. Agent names, count and role templates are not yet configurable.

## Pi-specific adapter

Duo targets Pi. The Go core is structured so other adapters could be added later, but no second agent runtime is currently supported.

## Dynamic mode escalation: Fast to Goal

A Fast mode session can be dynamically escalated to Goal mode on the fly without losing in-flight work or recreating worktrees. Escalation can be triggered by the human typing `/escalate [reason]` or `/mode goal` into the composer, or autonomously by agents calling `duo_escalate`. Upon escalation, the session enters the PLAN phase with a shared plan v1 seeded with the escalation context, and both agents transition into collaborative co-design.

Downgrading from Goal mode back to Fast mode is not supported.

## Fast mode is single-writer

In Fast, Austin owns the deliverable branch and Tony is a read-only verifier: Tony does not commit to the delivered artifact, and Tony's worktree is never delivered. A change Tony believes is needed is reported through `duo_set_verification issue_found` (or `duo_send`) and Austin applies it. Duo enforces this through the verification gate and the prompt, not filesystem permissions — Tony can still edit its own worktree, but those edits cannot reach the user's repository.

## Terminal UI scope

Duo provides one conversation timeline and one global human composer. Rich Pi features still run in the native Pi terminal reached through `Ctrl+A` or `Ctrl+T`; the timeline is not a terminal emulator.

The composer is multiline: up to four wrapped lines are visible, with cursor editing, and `Ctrl+Enter` or `Shift+Enter` inserts a newline. The conversation timeline scrolls its earlier output with the mouse wheel.

Agent and Duo output is rendered as lightweight markdown: headings, lists, code fences and pipe tables are laid out for the timeline width, and narrow table cells wrap instead of being clipped.

Text in the timeline can be selected by dragging the mouse and is copied to the clipboard on release. Duo emits standard OSC 52 terminal copy escape sequences (for SSH, tmux, and modern terminal emulators) and runs native clipboard utilities (`pbcopy` on macOS, `wl-copy` on Wayland, `xclip`/`xsel` on X11). Releasing without dragging (a plain click) clears the selection instead of copying.

What is still limited in the UI:

- Timeline scrolling supports mouse wheel and `PgUp` / `PgDn` keyboard scrolling, but there is no line-by-line keyboard cursor in the timeline (`↑`/`↓` belongs to the multiline composer).
- There is no scrollback search or timeline scrollback export. Selection is limited to what is currently rendered in the timeline.
- The composer recalls previously submitted tasks with `↑`/`↓` and persists them to `~/.duo/history`. Per-session branch/verification snapshots do not carry composer history separately.
- `Shift+Enter` inserts a newline only when the terminal reports it as a distinct sequence. A terminal that sends a bare carriage return for `Shift+Enter` will submit the message instead.

Frames are rebuilt from scratch (no partial-damage/diff updates) and capped at about 60 FPS by the renderer scheduler. This keeps redraw correctness easy to reason about, but very large terminals do redraw the whole screen on each dirty frame rather than only the changed regions.

## Intentionally shipped artifacts depend on the agent prompt

Duo asks Austin to clean collaboration-only artifacts out of the final tree and asks Tony to reject a dirty final tree during INTEGRATE. In Fast, Austin owns the delivered tree and Tony verifies it, and the same prompt-level obligation applies. This is not a repository rule: if the agent or agents approve a tree that still contains scratch files, Duo delivers that tree faithfully. The delivered commit is auditable after the fact, but Duo does not independently classify files as confidential or temporary.

## Durable state is a checkpoint, not a transcript

Collaboration state now survives a crash. Mode, phase, verification (Fast) or Plan version and signatures (Goal), evidence, the worktree record and per-agent Pi session identity are persisted to `~/.duo/sessions/<repo-id>/<session-id>/state.json` and validated against Git when a session is resumed.

The pane transcript is persisted too: `events.jsonl` carries `tui_entry` records (200 per pane) that are replayed into the UI on resume, so the visible history survives a crash even though it is not a searchable transcript.

What is persisted is the *state machine*, not the agents' reasoning. A resumed Austin or Tony keeps its own Pi conversation history and the shared Plan, but Duo does not summarize or replay what was in flight. Recovery is also deliberately conservative: it revokes any verification or signature it cannot prove is still valid, so a crash can legitimately cost a re-verification or re-sign-off. In Fast, a passed verification whose commit is no longer Austin's HEAD returns the session to `RUNNING` deterministically; recovery otherwise never moves a session backwards.

Recovery repairs bookkeeping; it never rewrites your Git history and never discards uncommitted files. A dirty worktree is reported, not cleaned.

## Session state is local and path-bound

`~/.duo/sessions/` is per machine and keyed by repository path, so a session is not portable across machines and does not follow a moved or renamed checkout.

## No automatic crash restart

Duo restarts Austin and Tony when it starts, and an exited agent can be restarted with `Ctrl+R` / `Ctrl+Y`. A dead Duo Core still requires a human to run `duo --resume`; Duo does not supervise or resurrect itself.

## Worktree cleanup is manual / conservative

Worktrees are preserved when Duo stops so unfinished work is not destroyed. Automatic lifecycle cleanup and pruning are not yet a polished user-facing workflow.

## Integration is intentionally asymmetric

Tony is merged into Austin and Austin becomes the integration worktree. This is simple and deterministic but not yet configurable. Fast has no integration step by design: Tony is read-only, so only Austin's branch is ever delivered.

## Merge conflicts are not automatically solved by the core

Conflicts remain in Austin's worktree for the agents/human to resolve. Duo does not attempt low-level automatic conflict resolution behind the user's back.

## Delivery is fast-forward only

Duo does deliver the final result back into the user's original repository, but only as a fast-forward on the branch it recorded when the session started. A handoff the human completed themselves is recognized rather than rejected: if the final HEAD is already an ancestor of the current HEAD, `duo apply` treats the result as already applied and preserves the human's commit, so a manual `git merge --no-ff <final-head>` is a supported way to finish a blocked delivery. A task that changes no repository files is a no-op: once the final commit is already an ancestor, delivery succeeds and records `DONE` even with a dirty working tree. For a real delivery it refuses to act on a dirty, wrong-branch, diverged or detached repository, and it never runs `reset --hard`, `checkout -f`, `clean`, `merge --no-ff` or `rebase` on the user's repository. When the branch cannot be fast-forwarded, Duo leaves the repository untouched, keeps the session in `INTEGRATE` (Goal) or `VERIFY` (Fast), records a `pending` delivery, and reports the final HEAD so the user can finish the handoff with `git merge --no-ff <final-head>` before re-running `duo apply`. A cherry-pick alone is not enough: it does not make the final HEAD an ancestor, so `duo apply` keeps refusing until the histories are actually merged.

Delivery is also all-or-nothing at the branch level: it moves the recorded branch to the final integrated commit, so it does not support partial or per-file handoff.

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

In Goal mode the shared Plan is currently a versioned text artifact rather than a full structured task graph. This is deliberate; richer task/artifact tracking may be added only if real usage proves it useful. Fast mode has no shared Plan at all.
