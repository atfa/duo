# xduo development plan

xduo and the terminal UI drive the same `internal/session` service, so the
workflow modes, verification gates, delivery and resume behavior already match.
The gap is in the human interface and in how much of the session projection the
GUI displays. Work started from commit `4094bd4`; the table tracks the current
implementation and remaining work.

| Area | Duo terminal UI | xduo at baseline | Next step |
| --- | --- | --- | --- |
| Task composer | IME-capable terminal input, Unicode editing, cursor movement, paste, multiple lines, persistent history | Native input bridge, IME composition, paste, basic Unicode cursor movement, Shift+Enter newlines; no mouse selection or history | P0: accurate caret/selection rendering and IME candidate position, then persistent history |
| Conversation | Chronological stream with speaker alignment, lightweight Markdown, timestamps, selection/copy and navigation | Scrollable plain-text stream, recent 500 events, no selection or timestamps | P1: timestamps and selectable/copyable text; P2: Markdown rendering and navigation |
| Session overview | Worktrees, verification/Plan, delivery, diffstat and full status | Mode, phase, readiness, step and delivery summary; `view.plan` and `view.status` are now in a toggleable overview | P1: add worktree/diffstat projection later |
| Agent controls | Restart Austin or Tony; enter either agent's native session | Can restart both agents; no agent session attach | P3: design a safe native-session transport before attach |
| Work preview | Per-agent tool trail, streamed text, errors and elapsed time | One flattened activity line per agent | P2: extend GUI projection with structured runtime fields, then build preview panels |
| Models and thinking | Driver-specific model catalogue, picker and thinking cycle | Displays current model, no picker or effort control | P2: expose catalog and human model/effort commands through the session host, then add picker |
| Session safety | Confirms quit during unfinished work and preserves the session | Ctrl+Q exits immediately | P1: confirmation when work is unfinished, including window close |
| Help and commands | Searchable help, slash commands, status and copy actions | Keyboard shortcuts shown in a hint line; no help panel or slash commands | P1: visible help and direct actions for existing commands; P2: useful slash-command parity |
| Session management | `duo sessions`, `logs`, `apply`, `clean` and `plugins` are separate CLI commands | `duo gui` starts or resumes one session; xduo has no management screens | P3: decide which maintenance actions merit a graphical shell; keep Core and CLI as the source of truth |

Implementation order:

1. **P0 — usable composer.** The first slice replaced manual key accumulation
   with GPUI's text input bridge, including IME composition, paste, Unicode
   cursor movement and Shift+Enter newlines. Next give the composer an accurate
   visual caret, selection, mouse positioning and IME candidate location, then
   add persistent task history. Preserve the existing `submit_task` command and
   keep text local to the renderer until submission. Linux and macOS IME
   behavior need native visual runs.
2. **P1 — expose state and basic controls.** The overview now shows
   `view.status` and `view.plan`, and Tony restart uses the existing command.
   Next add timestamps, worktree/diffstat fields, and unfinished-session quit
   confirmation. Add tests for any new state rules.
3. **P2 — richer session data.** Add structured work preview and a driver model
   catalog to the Go host protocol. Preserve the rule that GUI commands map only
   to human `session.Command` verbs. Implement model and thinking controls only
   after the actual driver's catalog is available. Improve timeline rendering.
4. **P3 — native agent access.** Specify and implement a terminal transport for
   direct agent attachment, with focus and detach behavior on macOS and Linux.
   This needs an explicit protocol design because the current NDJSON pipes carry
   only Duo session state and commands. Separately assess whether session lists,
   logs, delivery and cleanup need a graphical shell around the renderer.

Each slice should keep the GUI usable against the same host, update
`docs/gui-protocol.md` when its wire format changes, and pass the relevant Rust
and Go checks before integration.
