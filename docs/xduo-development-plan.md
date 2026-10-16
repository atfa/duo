# xduo development plan

xduo and the terminal interface drive the same `internal/session` service. Their
workflow modes, verification gates, delivery and resume behavior match. Version
0.12.1 improves desktop usability; the remaining differences are in the human
interface.

| Area | Available in xduo 0.12.1 | Remaining gap and priority |
| --- | --- | --- |
| Task composer | IME, Unicode editing, mouse and keyboard selection, long-line wrapping, multiline tasks, and shared task history | P3: richer editing shortcuts and accessibility review. |
| Conversation | Chronological timeline, timestamps, lightweight Markdown, message selection, keyboard navigation and copy of original text | P2: search and finer text selection within a message. |
| Session overview | Mode, phase, Plan, readiness, steps, delivery, agent worktrees, Git diffstat and verification evidence | P2: inspect full diff and detailed verification logs from the window. |
| Work preview | Both agents' tools, streamed text, errors, context usage, throughput, recent results, elapsed time and current step | P2: richer scrolling and filtering for long output. |
| Models and thinking | Per-agent catalog picker, thinking controls and restart confirmation when the driver cannot switch live | P2: make pending model changes and driver capabilities visible in the agent card. |
| Session safety | Unfinished-session quit confirmation and a [desktop window smoke guide](xduo-window-smoke.md) | P1: execute visual smoke tests on macOS and common Linux X11/Wayland desktops before publishing. |
| Help and commands | Searchable keyboard help and common slash commands | P3: evaluate additional commands against actual GUI usage. |
| Agent access | Agent health and restart | P3: native interactive attach requires a PTY transport and terminal emulator to preserve agent behavior. |
| Session management | Start or resume from `duo gui`; launchers offer unfinished sessions in a chosen repository | P3: consider graphical session listing and maintenance; CLI `duo sessions`, `logs`, `apply`, `clean` and `plugins` remain available. |

Native agent attach is a separate transport project. The GUI protocol currently
carries Duo state and human session commands. An interactive agent terminal also
needs a PTY stream, resize, focus and detach semantics. A cosmetic attach button
without that transport could lose control of a live session.

Every host projection change should update [the GUI protocol](gui-protocol.md),
keep commands mapped to human `session.Command` verbs, and pass the Rust renderer
and Go host tests. Before publishing a desktop build, run the headless smoke
suite and the platform checks in the window smoke guide.
