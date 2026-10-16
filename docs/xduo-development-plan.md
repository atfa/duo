# xduo development plan

xduo and the terminal interface drive the same `internal/session` service. Their
workflow modes, verification gates, delivery and resume behavior therefore match.
The differences below are in the human interface. Version 0.12.0 makes the GUI a
usable desktop release; it does not claim terminal-interface feature parity.

| Area | Available in xduo 0.12.0 | Remaining gap and suggested priority |
| --- | --- | --- |
| Task composer | IME, Unicode editing, mouse selection, multiline tasks, and task history shared with Duo | P1: wrap long unbroken lines and add keyboard selection shortcuts. |
| Conversation | Chronological timeline, optional timestamps, and copy of Austin's latest human-directed message | P1: select and copy arbitrary messages; P2: render lightweight Markdown and add timeline navigation. |
| Session overview | Mode, phase, status, Plan, readiness, steps, delivery, and both agents' worktree path, branch and HEAD | P2: show Git diffstat and verification evidence in dedicated rows. |
| Work preview | Both agents' current tool, errors, streamed text, context usage, throughput, and recent tool results | P2: update elapsed-time labels continuously and improve long-output layout. |
| Models and thinking | Per-agent picker using each driver's real catalog, plus per-agent thinking controls | P2: searchable keyboard help and clearer indication when switching a driver restarts its process. |
| Session safety | Unfinished-session quit confirmation for keyboard and window close | P1: native desktop visual smoke tests on macOS, Wayland and X11. |
| Help and commands | Keyboard help, restart, Goal escalation, language, overview, preview and copy shortcuts | P2: a slash-command palette for people moving from the terminal interface. |
| Agent access | Agent health and restart | P3: native interactive attach only after a PTY transport and terminal emulator can preserve agent behavior. |
| Session management | Start or resume through `duo gui`; desktop launcher chooses a Git repository | P3: consider graphical session listing and maintenance; CLI `duo sessions`, `logs`, `apply`, `clean` and `plugins` remain available. |

The next release should start with the P1 composer and timeline issues, because
they affect daily reading and writing. P2 items improve parity and visibility but
do not change the workflow. Native agent attach is a separate transport project:
the GUI protocol currently carries only Duo state and human session commands,
while an agent's interactive terminal needs a PTY stream, resize, focus and
detach semantics. A cosmetic attach button without that transport would risk
losing control of a live session.

Every change to the host projection should update
[`gui-protocol.md`](gui-protocol.md), keep commands mapped only to human
`session.Command` verbs, and pass the Rust renderer and Go host tests.
