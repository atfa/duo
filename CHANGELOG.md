# Changelog

All notable project milestones are documented here.

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
- human-controlled final merge boundary;
- modular Go core and modular Pi extension.

### Design change from earlier experiments

PLAN is not treated as a global write lock. Agents may make provisional edits in their own worktrees while discussion continues. Formal coordination happens at Plan and artifact sign-off checkpoints.
