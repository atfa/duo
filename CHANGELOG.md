# Changelog

All notable project milestones are documented here.

## Unreleased

TUI usability and readability:

- The system log between the panes is now a real scrollable pane (four rows by default, mouse-wheel and selectable), so harness notes, delivery results and errors no longer vanish after two lines.
- A scrolled pane keeps its place while new output arrives, shows `↑N` with `▼` when unseen output landed below, and no longer re-parses every markdown block on each frame (wrapped lines are cached until the entries or width change).
- A per-agent **work preview** now sits between the panes and the system log, so a long turn is no longer opaque. Its header names the state and how long the current turn has been running; the rows below show what the agent is doing now (the running tool with its age, or the model and thinking level it is thinking with), the turn's recent tool trail with ✓/✗ and durations, the turn's last error, and the tail of the text being streamed. Failures are reported by the bridge (`tool_error`, agent errors) rather than parsed out of the full-screen PTY, so an error that already scrolled off Pi's screen is still visible. `Ctrl+P` collapses the band, and a short terminal hides it automatically so the panes keep their rows.
- The composer gained Home/End, Ctrl+U/Ctrl+K, Ctrl+W, Alt+←/→, line-up/down movement and task-history recall with Up/Down; `Ctrl+G` toggles message timestamps; `Ctrl+O` opens a session overview with the worktrees, the full verification note or shared plan, and the delivery state.
- `Ctrl+M` opens a model picker so the human no longer has to enter native Pi just to switch a model: it lists the catalog of the same Pi installation (`pi --list-models`), filters as you type, switches the target between Austin and Tony with `Tab`, and applies live through the Pi bridge (`pi.setModel`) without restarting the agent or losing the conversation. `Shift+Tab` cycles the target's thinking level, and `●` marks the model each agent is actually on. `Enter` applies the model and closes the picker; `Space` applies it and keeps the picker open, so a model and a thinking level can be set in one visit. Both the model and the thinking level are recorded by Pi in its session transcript, so they survive a restart or resume. `Ctrl+M` is only distinct from `Enter` on terminals that honor modifyOtherKeys/CSI-u, so `Alt+M` is provided as a fallback.
- Wrapping prefers a space boundary so words are no longer split mid-token; markdown links are OSC 8 hyperlinks whose label stays copyable; consecutive messages are separated by a dim rule; and the delivery state is always visible in the status area.

- A task submitted through the Duo composer after `DONE` reopens the session for a new round instead of being answered with no path back to the peer: Fast returns to `RUNNING`, Goal to `PLAN`, and the finished round's delivery checkpoint is cleared so a fresh verification and delivery can run. Native Pi attachment (`Ctrl+A` / `Ctrl+T`) still leaves the phase untouched.
- A Fast verifier's `issue_found` verdict is no longer rendered as a red `ERROR`. It is a normal workflow outcome, so it now shows as a warning in the verifier's pane, with a one-line summary in the Duo pane instead of a second full copy of the report.
- Harness nudges inject a compact state block that references the shared plan (`v2, 4.8KB — read it with duo_status`) instead of re-inlining the whole plan text on every nudge. When and how the harness nudges is unchanged; only the repeated multi-KB payload is gone. `duo_status` still returns the full plan.
- The Fast verifier prompt now tells Tony to report its verdict once with `duo_set_verification` and not restate it with `duo_send`, because Duo Core already relays the verdict to Austin. This removes a duplicate copy of every verification report.
- Emoji are measured as the two terminal columns they occupy. The width table only covered `U+1F300..U+1FAFF`, so common emoji such as ✅, ❌ and ⭐ were counted as one column and every affected pane line overflowed, pushing the divider to the right. Width now follows the Unicode properties: default-emoji (Emoji_Presentation) are two columns, a text-presentation pictograph such as ⚠ or ↗ is one column unless followed by VS16, and zero-width joiners, variation selectors and skin-tone modifiers are zero width. The pane header button (`↗`) no longer shifts the `┬` away from the `│` below it.

## v0.5.0 — 2026-09

Fast Mode is now the default; Goal Mode is the explicit heavy workflow.

- `duo` starts in **Fast** mode: Austin is the driver, Tony is a read-only independent verifier, and the session runs `RUNNING → VERIFY → DONE` with no shared Plan and no dual sign-off.
- `duo --mode goal` / `-m goal` selects the previous `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE` workflow, unchanged. The mode is fixed per session and persisted; a legacy session with no recorded mode resumes as Goal.
- New `duo_set_verification` tool (Tony only, Fast only) reports `passed` or `issue_found` with a concrete note for the exact Austin commit under review. A `passed` result is bound to that commit; any new commit invalidates it and returns the session to `RUNNING`.
- Fast has no `duo_set_plan`, and a Tony `duo_set_status` in Fast is rejected with guidance. Only Austin's verified commit is ever delivered.
- The harness is mode-aware: it nudges the agent that owns the current phase (Austin in `RUNNING`, Tony in `VERIFY`) and escalates a stuck `RUNNING` episode into one diagnosis request to Tony; Goal keeps its both-idle behavior.
- The TUI status line, startup message, Help, resume prompt and Pi system prompt are mode-aware.
- `DUO_MODE` (`fast` or `goal`) configures a new session; `--mode`/`-m` wins, and `--resume` keeps the persisted mode.
- Recovery reconciles Fast verification against Git and deterministically returns `VERIFY → RUNNING` if the verified commit is no longer Austin's HEAD.

TUI readability and handoff polish:

- Agent output is rendered as lightweight markdown: styled headings, blockquotes, links and bold/italic/code spans, and markdown tables drawn with real, aligned borders. A table wider than its pane wraps its widest cells instead of truncating them.
- Mouse drag over a pane selects that agent's text and copies it to the clipboard (`pbcopy` on macOS). Clicking a pane title still opens that agent's native Pi; a click inside a pane without a drag selects nothing.
- `Ctrl+】` is accepted as a third return-from-native-Pi sequence alongside `Ctrl+]` and `Ctrl+\`.
- A wrapped markdown list keeps a hanging indent, so continuation lines line up under the item text instead of the bullet. Nested lists are indented by their own depth.
- A resumed session replays each pane's most recent 200 entries, persisted as `tui_entry` records in `events.jsonl`, so the previous conversation is visible immediately.
- Delivery recognizes a user-resolved merge: once the final Duo commit is an ancestor of the original HEAD — including after `git merge --no-ff` — `duo apply` treats the handoff as already applied and records `DONE` without moving the branch again. A refused delivery now prints that merge command.
- The Pi bridge no longer reports a compaction abort (`"This operation was aborted"`) as an agent error.

## v0.4.7 — 2026-09

TUI scrolling and multiline composer improvements.

- Austin and Tony panes now support independent mouse-wheel history scrolling.
- The composer supports multiple visible lines, cursor editing, and `Ctrl+Enter` or `Shift+Enter` for a newline.
- Restored `Ctrl+A`, `Ctrl+T`, `Ctrl+R`, `Ctrl+Y`, `Ctrl+Q`, and `Ctrl+/` while `modifyOtherKeys` is enabled.

## v0.4.6 — 2026-09

TUI Help and usability polish; no collaboration, recovery, or delivery semantics changed.

- Added a full alternate-screen Help view with wrapped content, keyboard scrolling, resize-safe clamping, and `Ctrl+/` / `Esc` close behavior.
- Kept native Pi input passthrough intact, including `Ctrl+/`; only Duo main TUI handles Help.
- Clarified the Austin-only human composer, separated transient status, and reduced persistent footer hints.

## v0.4.5 — 2026-09

Resume collaboration wake-up.

- A resumed runtime sends each reconnected Pi agent one phase-aware `resume_prompt`, derived from authoritative durable state; reconnects do not duplicate it and fresh sessions keep the Austin-only bootstrap.

## v0.4.4 — 2026-09

Launch-directory working scope.

- Git ownership stays at the repository root while Austin and Tony start in the repository-relative directory Duo was launched from: `cd repo/packages/web && duo` keeps branches, worktrees and delivery at `repo`, and both agents' default working directory at `packages/web` inside their private worktrees.
- The scope is recorded in the durable snapshot and restored across `duo --resume` and agent restart, instead of collapsing back to the repository root.
- The Pi bridge reports each agent's active scope in its system prompt.
- Coordinator tests now wait for the durable delivery checkpoint, removing a teardown race.

## v0.4.3 — 2026-09

Test lifecycle and release validation hardening.

- Coordinator E2E teardown now deterministically joins clients, server goroutines and Git worktree cleanup before temporary directories are removed.
- Main and tagged CI continue to gate the four-platform release archives.

## v0.4.2 — 2026-09

Reliability fixes for delivery and releases.

- Coordinator end-to-end tests now wait for matching request/response acknowledgements instead of treating independent TCP writes as ordered.
- INTEGRATE final approval is edge-triggered; duplicate `ready=true` messages are idempotent and cannot trigger delivery again.
- Each session serializes the complete delivery transaction, rechecks current state under the transaction lock, and treats an applied checkpoint for the same final HEAD as monotonic.
- Critical pending, applied and DONE checkpoints fail closed; a failed pending write prevents any original-repository mutation.
- Tagged release builds now require `go test ./...`, `go vet ./...`, and a normal binary build before archives publish.

## v0.4.1 — 2026-09

Deliverable handoff. INTEGRATE sign-off no longer means only "agreed": `DONE` now means the integrated artifact has been delivered back into the repository the user launched Duo from.

- Final sign-off in INTEGRATE records approval (`readyForDelivery`) but keeps the session in INTEGRATE; `DONE` is a separate, explicit transition that runs after delivery.
- On a dual INTEGRATE sign-off Duo delivers the whole final integrated HEAD into the user's original repository, never a per-file copy.
- Delivery is fast-forward only: Duo refuses a dirty repository, a different checked-out branch, a diverged history or a detached HEAD, and never runs `reset --hard`, `checkout -f`, `clean`, `merge --no-ff` or `rebase` on the user's repository.
- A refused delivery keeps the session in INTEGRATE with both signatures intact, records a `pending` delivery checkpoint, and prints the exact `duo apply <session-id>` command.
- `duo apply [session-id]` retries a blocked delivery with the same safety rules; on a repository with a single pending delivery it needs no arguments.
- The final approval and pending-delivery checkpoint are persisted before Git is touched, so a crash between the fast-forward and the DONE write is reconciled on the next `duo --resume` or `duo apply`.
- A v0.4.0 session already marked `DONE` can still be handed off with `duo apply`, resolving the final HEAD from the recorded delivery, integration head, Austin worktree or Austin branch.
- `DONE` keeps both final signatures and reports the target branch, final HEAD and applied HEAD instead of claiming the original branch was untouched.
- The INTEGRATE prompt now gives Austin an explicit final-tree cleanup duty and Tony a repository-hygiene review, so collaboration-only artifacts do not ship.
- The harness stops nudging once INTEGRATE is dual-signed and delivery is in progress.

## v0.4.0 — 2026-09

Durable Duo sessions and crash recovery. After a Duo Core, Austin or Tony crash Duo can continue the original task instead of silently restarting at PLAN.

- Persisted, versioned session snapshot in `~/.duo/sessions/<repo-id>/<session-id>/state.json`, written atomically (temp file → `fsync` → rename → directory `fsync`).
- An unsupported `schemaVersion` is refused with an explicit error instead of being read best-effort.
- `duo --resume [session-id]` reopens a session. Bare `duo --resume` picks the repository's single unfinished session; when several exist they are listed instead of guessed.
- Existing worktrees are validated and reused, never recreated. A missing worktree fails with a clear message.
- Git is the ground truth: on resume, worktree HEADs, an in-progress merge (`MERGE_HEAD`) and the integration result are re-checked, and every signature whose evidence no longer matches is revoked.
- Recovery never moves a session backwards to PLAN and never repeats a merge that already completed or is still in progress.
- A dirty worktree no longer blocks resume: recovery reports it and revokes only the signatures it invalidates, leaving uncommitted files untouched.
- An advisory `flock` per session, with PID metadata for diagnostics, stops two Duo processes from driving the same session.
- `events.jsonl` holds a diagnostic journal; `duo.log` holds lifecycle output and redacts session tokens.
- Per-agent Pi session identity is stable across restarts via `--session-id`, so Austin and Tony keep their own Pi conversation history. `DUO_PI_COMMAND` is still honoured.
- The bridge protocol version is now shared between the Go core and the Pi extension; mismatched clients are rejected.
- A resumed session gets a harness grace period (`DUO_HARNESS_RESUME_GRACE_SECONDS`, default 45s) so reconnecting agents are not mistaken for stalled ones.
- Fixed a path-comparison bug that made resume reject its own worktrees on macOS, where Git reports `/private/var/...` for a persisted `/var/...` path.

## v0.3.3 — 2026-09

TUI renderer hardening; no agent, persistence, or collaboration changes.

- Fixed resize artifacts (border trails, stale footer, stacked frames) by forcing a full-screen clear on terminal resize, native Pi return, alternate-screen re-entry, renderer start, and any layout change.
- Removed the fake 60×18 minimum terminal size. Layout now uses the real geometry, and sub-minimum terminals get a bounded `Terminal too small` notice that cannot wrap or scroll.
- Wrapped each frame in DEC synchronized output (`CSI ?2026 h/l`) and temporarily disabled autowrap for the frame write.
- Coalesced SIGWINCH bursts into a single latest-size PTY update and one frame, removing redundant redraws while dragging a window.
- Added a dirty/renderer scheduler (~60 FPS): event, input, resize, restart, and native detach all funnel through `markDirty`; idle Duo no longer repaints and the spinner only advances while an agent is busy.
- Distinguished `exited` from `failed` process state; a Duo-initiated stop is reported as a normal exit.

## v0.3.2 — 2026-09

- Replaced the external `script` host with direct Go-owned Austin/Tony PTYs; detached agents continue running and retain recent raw output.
- Propagated SIGWINCH to both PTYs, including detached processes.
- Added manual restart of exited agents (Ctrl+R Austin, Ctrl+Y Tony) without changing worktrees or bridge credentials.
- Added a random hex suffix to automatically generated Duo session IDs.

## v0.3.1 — 2026-09

Runtime hardening and first binary release.

### Fixed

- included the previously ignored `cmd/duo` CLI entrypoint in source releases;
- made the installed Pi bridge opt-in for Duo-launched sessions;
- isolated concurrent Duo runs with dynamic ports, session IDs and random tokens;
- stopped reporting normally exited Pi processes as running;
- stopped the TUI cleanly when terminal input closes;
- aligned CI and documentation with the integrated TUI release.

### Added

- macOS and Linux release archives for amd64 and arm64;
- one-command release installer for the binary and Pi extension.

## v0.3.0-alpha.1 — 2026-09

First integrated terminal UI preview.

### Added

- integrated Duo TUI with Austin/Tony native Pi attach and detach;
- animated agent status and standard ANSI colors;
- full provider and agent error reporting, including HTTP 402/429 responses;
- copy-pasteable Git setup guidance for non-Git and empty repositories;
- terminal mode cleanup after native Pi sessions.

## v0.2-alpha — 2026-09

First public experimental release candidate.

### Added

- two persistent peer Pi agents: Austin and Tony;
- single human entry via Austin;
- live `duo_send` peer messaging;
- versioned shared Plan and dual sign-off;
- lifecycle: PLAN, EXECUTE, REVIEW, INTEGRATE, DONE;
- idle/stall harness that can nudge Austin;
- isolated Git worktrees and per-agent branches;
- commit-SHA evidence for execution completion;
- peer-HEAD evidence for review completion;
- automatic stale-signature revocation;
- Tony → Austin integration merge;
- human-controlled final merge boundary (accurate for v0.2: the automatic fast-forward handoff arrived in v0.4.1, below);
- modular Go core and modular Pi extension.

### Design change from earlier experiments

PLAN is not treated as a global write lock. Agents may make provisional edits in their own worktrees while discussion continues. Formal coordination happens at Plan and artifact sign-off checkpoints.
