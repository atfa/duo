# gotui terminal frontend

gotui is Duo's default and only bundled terminal frontend. `duo` opens it
without a separate UI-selection flag; modes and resume are passed on the command
line as usual:

```bash
duo
duo --mode goal
duo resume
duo resume <session-id>
```

## What it shows

The gotui frontend is a live dashboard over `session.Service`. It never owns
workflow state or makes project transitions:

- **Timeline (default).** One chronological conversation. The selected view is
  saved as `tuiMode` in the repository's `.duo/config.json` whenever you switch
  views, and restored on the next launch.
- **Split dashboard.** A workflow sidebar with the phase chain,
  verification/delivery state, worktrees and two agent cards; Austin and Tony in
  side-by-side transcripts; and a full-width Duo/Human pane below them.
- The `Split` / `Timeline` switch sits at the right of the second header row
  instead of taking a row of its own; `F2` also switches views.
- **Step workflow.** Step shows the queue lifecycle `PLAN → STEPS → DONE`, not
  Goal's `EXECUTE → REVIEW → INTEGRATE` chain. A `step status` line reports the
  current item's stage (planning, working, reviewing or integrating).
- **Composer.** Multiline editing (`Ctrl+Enter`, `Shift+Enter` where reported,
  `Alt+Enter`, `Ctrl+J`), history recall, bracketed-paste handling, tracked-file
  `@` completion, clipboard-image paths (`Ctrl+V`), cursor-aware scrolling, slash
  palette, and the model/thinking picker.
- **Language.** `/language zh|en` changes Duo-authored messages and the dashboard
  chrome immediately, and the choice is persisted in `.duo/config.json` for the
  next launch. Protocol names such as FAST/GOAL/STEP stay canonical.
- **Mode switching.** `/mode fast|goal|step` and `/escalate` apply the new mode
  immediately, then ask whether to resume Austin and Tony. The safe default is
  **Not now**, which leaves both agents idle until the human submits the next
  task; it also holds harness nudges so the watchdog cannot start work by itself.
- **Pause and drivers.** `/pause` waits for a turn boundary and shows
  `PAUSING`/`PAUSED` in the header, with the footer naming who is still working.
  `/unpause` cancels an incomplete request; `/continue` resumes a full pause.
  `/driver` is available before the first task, while fully paused, or after
  `DONE`: agent tabs, typing filter, a marker on the current driver, and no
  resume when a choice is applied.
- **Overlays.** Help, session details, model picker, driver picker and
  destructive confirmations are centered modals with a cleared content backdrop.
  The slash palette occupies the conversation pane so no text or border fragments
  show through it.
- **Copy feedback.** Copying a transcript selection or Austin's latest message
  briefly overlays a `COPIED` panel in the top-right; it dismisses itself after
  two seconds and does not consume layout space.
- **Native attach.** `Ctrl+A` / `Ctrl+T` suspend the dashboard, hand the real PTY
  to the agent, and `Ctrl+]`, `Ctrl+\` or `Ctrl+】` return to a fully redrawn
  dashboard. This is also the reliable way to paste a real image when `Ctrl+V`
  cannot reach a clipboard-image tool: the agent's own terminal receives the
  pixels directly.
- **Resume.** Restored history is rendered immediately and nothing is sent to
  the agents: they start idle, and harness nudges stay quiet until the human acts.
  A one-time notice explains how to continue; `/resume` sends each agent its
  phase-appropriate resume prompt, and submitting a task wakes them too. A
  startup banner is only added to a new session, so a resumed Duo pane starts
  with the human's request.

Long lines wrap at cell boundaries, including wide CJK runes; Markdown headings,
lists, quotes, fenced code, tables, inline emphasis and links are rendered in the
transcript. Table cells wrap individually so a wide table keeps its column
alignment instead of breaking its borders. URLs and existing file paths are
clickable; file paths resolve against the message's worktree first and the
repository second. Link detection happens in the frontend over the text the
agents already produced, so it needs no extra LLM output. Colours are TrueColor
with terminal fallback.

## Keys

Slash commands and composer editing are documented in the in-app help (`F1`,
`Ctrl+/`, `/help`); `duo --help` covers startup flags, commands and environment
variables. The dashboard adds:

| Key | Action |
|---|---|
| `F2` or the `Split` / `Timeline` tabs | Toggle the dashboard view |
| `Ctrl+P` | Show or hide the workflow sidebar |
| `F1` / `Ctrl+/` / `Alt+H` | Open help |
| `Ctrl+O` / `Alt+O` | Open session details and the Git diffstat |
| `Ctrl+M` / `Alt+M` | Open the model and thinking picker |
| `Ctrl+G` | Toggle message timestamps |
| `Ctrl+A` / `Alt+A` | Attach to Austin |
| `Ctrl+T` / `Alt+T` | Attach to Tony |
| `Ctrl+]` / `Ctrl+\` / `Ctrl+】` | Detach from an attached agent |
| `Ctrl+Q` / `Ctrl+D` | Quit and preserve the session |
| Mouse wheel | Scroll the transcript or an open help/details modal |
| Mouse drag | Select transcript text and copy it; a short-lived `COPIED` toast appears in the top-right |
| Click a URL | Open it in the default browser |
| Click an existing file path | Open its containing folder in the default file manager |
| Click `Split` / `Timeline` | Switch view; the labels sit at the right of the second header row |
| Click a sidebar panel | Expand workflow, Austin or Tony; click again to restore |

Modal choices are keyboard-driven (`←` / `→`, `Enter`, `Esc`). The model picker
uses `Tab` to switch the target agent, `Space` to select a model without closing,
`Shift+Tab` to cycle the selected agent's thinking level in place, and `Ctrl+T` to
switch between the explicit Models and Thinking tabs. Thinking-capable model rows
show the canonical level after the model name (`off` through `max`), so a
clamped level is visible instead of being translated away.
Model and thinking selections are written to the repository's `.duo/config.json`
under the agent's driver, so the next launch loads them again.
Closing the picker keeps the selected agent. `Shift+Tab` is deliberately not bound
in the main view, where the target agent would be ambiguous.

## Responsive layout

The layout is computed from the terminal size on every resize:

| Space | Result |
|---|---|
| at least 104 columns and 21 content rows | default workflow sidebar and agent cards |
| at least 104 columns and 18 content rows, with a panel focused | sidebar; the focused panel expands |
| main pane at least 72 columns and 16 rows | split Austin/Tony/Duo dashboard |
| narrower or shorter | conversation timeline without the sidebar |

The Duo composer grows to three visible text rows as height allows. The header
uses two rows: title and mode/phase, then session information and the view
switch on the right. Long sidebar values wrap instead of being truncated, and the
workflow phase chain wraps to a second line when the sidebar is too narrow for
all phases.
Clicking one of the three sidebar panels expands it to the available height and
shrinks the other two to their title bars; clicking it again restores the even
layout. The disclosure triangle in each title (`▸` collapsed, `▾` expanded)
shows the state.

## Architecture

The frontend lives entirely under `internal/gotui/`:

| File | Responsibility |
|---|---|
| `app.go` | lifecycle, event loop, layout updates, draw order, startup/history policy |
| `layout.go` | responsive rectangles for header, sidebar, panes, composer, footer |
| `panels.go` | header, workflow, workflow status, agent cards, footer |
| `transcript.go`, `text.go` | transcript projection, wrapping, selection |
| `links.go` | URL/path detection, click hit-testing and OS openers |
| `markdown.go`, `format.go` | styled Markdown and display formatting |
| `messages.go` | event-to-entry projection and recording |
| `session.go` | local Session seam over `session.Service` |
| `theme.go` | TrueColor palette and shared styles |
| `composer.go` | multiline editing, visual rows, scrolling, history |
| `slash.go`, `picker.go`, `driverpicker.go`, `overlay.go` | command palette, model picker, driver picker and modal surfaces |
| `keys.go`, `commands.go` | key routing, mouse, slash commands, native actions |
| `native.go` | suspend/attach/detach/restore and input assembly |
| `capture_test.go` | deterministic SimulationScreen frames |

`New` restores the last entries through the `Session` adapter, then `runWithBackend`
subscribes to session events. The event loop selects over session events, tcell
events (`Screen.EventQ()`), renderer async results and a busy-only tick, then
projects the latest `session.View` into widgets. Workflow facts, phase gates,
delivery, sessions, MCP and drivers remain in the existing core packages; the
frontend only reads them.

Rendering uses the gotui `Backend` and `Screen` directly rather than gotui's
`Application`, because Duo owns quit, redraw and native-terminal behavior.
Overlays embed a gotui `Block` and draw into the same buffer; there is no second
screen state.

Native attach uses `Screen.Suspend()`, reopens `/dev/tty` in raw mode, calls the
session's `Attach`/`Write`/`Resize`/`Detach`, and then calls `Screen.Resume()`
plus `Sync()` and a full layout rebuild. Escape sequences are assembled across
reads so a split function-key reply is not sent to the agent as a bare Escape.
The detach chord itself is consumed, not forwarded.

## Captures

These are real text frames rendered by tcell's SimulationScreen during the
committed capture test, not mockups. The source test is
`internal/gotui/capture_test.go`; colour and style attributes are not visible in
the text files.

| Frame | What it covers |
|---|---|
| [dashboard 120×42](gotui/screens/01-dashboard-120x42.txt) | split dashboard, workflow sidebar, agent cards, Duo/Human pane |
| [timeline 120×42](gotui/screens/02-timeline-120x42.txt) | single conversation view and restored history |
| [slash palette 120×42](gotui/screens/03-slash-120x42.txt) | filtered command palette over the conversation pane |
| [help 120×42](gotui/screens/04-help-120x42.txt) | help modal on a cleared backdrop |
| [details 120×42](gotui/screens/05-details-120x42.txt) | session details and diffstat |
| [models 120×42](gotui/screens/06-models-120x42.txt) | model/thinking picker after the async catalog load |
| [paused 120×42](gotui/screens/09-paused-120x42.txt) | PAUSED badge and the paused footer hint |
| [drivers 120×42](gotui/screens/10-drivers-120x42.txt) | driver picker |
| [confirm 80×24](gotui/screens/07-confirm-80x24.txt) | destructive confirmation at a compact size |
| [narrow 64×22](gotui/screens/08-narrow-64x22.txt) | no-sidebar timeline in a small terminal |

Regenerate them with:

```bash
DUO_GOTUI_CAPTURE_DIR="$PWD/docs/gotui/screens" \
  go test ./internal/gotui -run TestCaptureFrames -count=1
```

## Verification

The frontend has focused tests for layout, wrapping, Markdown, composer/paste
behavior, session restore, slash filtering, pause commands and progress, driver
picker gating/application, picker state, overlay draw order, CLI selection,
native detach parsing, and a real PTY attach/detach round trip that checks
terminal-state restoration. Run the repository checks with:

```bash
go test ./...
go vet ./...
go build ./cmd/...
make check
```

## Known limitations

- The split view and workflow sidebar intentionally disappear on terminals that
  are too small rather than squeezing unreadable panes.
- Captures are deterministic text frames; they do not preserve colour or style.
- Markdown is lightweight: no images, embedded HTML or full CommonMark. Tables
  render with box-drawing borders and wrap wide cells to the transcript width;
  malformed tables fall back to plain text.
- Modal buttons are keyboard-driven; mouse support covers tabs, sidebar panel
  expansion, transcripts, selection, link opening and modal wheel scrolling.
- Paths with embedded spaces are not linkified unless they appear as a complete
  token; only paths that exist are clickable.
- The available models and whether a switch is live or restarts the agent are
  driver capabilities, not frontend choices.
