# gotui terminal frontend

Duo has two terminal frontends over the same Go core. The **Legacy TUI** is still
the default, so `duo` and `duo --ui=legacy` behave exactly as before. The gotui
frontend is opt-in:

```bash
duo --ui=gotui
duo --ui gotui --mode goal
duo resume --ui=gotui
duo resume <session-id> --ui=gotui
```

`duo resume --ui=legacy` selects the original interface explicitly. The choice is
per invocation; there is no UI setting in the config file or environment.

## What it shows

The gotui frontend is a live dashboard over the same `session.Service` used by
Legacy. It never owns workflow state or makes project transitions:

- **Split dashboard (default).** A workflow sidebar with the phase chain,
  verification/delivery state, worktrees and two agent cards; Austin and Tony in
  side-by-side transcripts; and a full-width Duo/Human pane below them.
- **Timeline.** One chronological conversation, one toggle away.
- **Composer.** Multiline editing, history recall, bracketed-paste handling,
  cursor-aware scrolling, slash palette, and the model/thinking picker.
- **Overlays.** Help, session details, model picker and destructive confirmations
  are centered modals with a cleared content backdrop. The slash palette occupies
  the conversation pane so no text or border fragments show through it.
- **Native attach.** `Ctrl+A` / `Ctrl+T` suspend the dashboard, hand the real PTY
  to the agent, and `Ctrl+]`, `Ctrl+\` or `Ctrl+】` return to a fully redrawn
  dashboard.
- **Resume.** Restored history is rendered immediately. A startup banner is only
  added to a new session, so a resumed Duo pane starts with the human's request.

Long lines wrap at cell boundaries, including wide CJK runes; Markdown headings,
lists, quotes, fenced code, inline emphasis and links are rendered in the
transcript. Colours are TrueColor with terminal fallback.

## Keys

Shared Duo keys — slash commands, composer editing, history, native attach,
model picker, copy and quit — are documented in the [main README](../README.md#terminal-ui).

The dashboard adds:

| Key | Action |
|---|---|
| `F2` or the `Split` / `Timeline` tabs | Toggle the dashboard view |
| `Ctrl+P` | Show or hide the workflow sidebar |
| `F1` / `Ctrl+/` | Open help |
| `Ctrl+O` | Open session details and the Git diffstat |
| `Ctrl+M` / `Alt+M` | Open the model and thinking picker |
| `Ctrl+G` | Toggle message timestamps |
| `Ctrl+A` / `Ctrl+T` | Attach to Austin / Tony |
| `Ctrl+]` / `Ctrl+\` / `Ctrl+】` | Detach from an attached agent |
| `Ctrl+Q` / `Ctrl+D` | Quit and preserve the session |
| Mouse wheel | Scroll the transcript or an open help/details modal |
| Mouse drag | Select transcript text and copy it |
| Click on a tab | Switch view |

Modal choices are keyboard-driven (`←` / `→`, `Enter`, `Esc`). The model picker
uses `Tab` to switch the target agent and `Space` to apply without closing.

## Responsive layout

The layout is computed from the terminal size on every resize:

| Space | Result |
|---|---|
| at least 104 columns and enough height | workflow sidebar and agent cards |
| main pane at least 72 columns and 16 rows | split Austin/Tony/Duo dashboard |
| narrower or shorter | conversation timeline without the sidebar |
| under 80 columns | compact footer hints |

The Duo composer grows to three visible text rows as height allows.
The workflow phase chain wraps to a second line when the sidebar is too narrow
for all phases.

## Architecture

The frontend lives entirely under `internal/gotui/`:

| File | Responsibility |
|---|---|
| `app.go` | lifecycle, event loop, layout updates, draw order, startup/history policy |
| `layout.go` | responsive rectangles for header, sidebar, panes, composer, footer |
| `panels.go` | header, workflow, workflow status, agent cards, footer |
| `transcript.go`, `text.go` | transcript projection, wrapping, selection |
| `markdown.go`, `format.go` | styled Markdown and display formatting |
| `messages.go` | event-to-entry projection and recording |
| `session.go` | local Session seam over `session.Service` |
| `theme.go` | TrueColor palette and shared styles |
| `composer.go` | multiline editing, visual rows, scrolling, history |
| `slash.go`, `picker.go`, `overlay.go` | command palette and modal surfaces |
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

## Legacy comparison

| | Legacy (default) | gotui (`--ui=gotui`) |
|---|---|---|
| Layout | single timeline with an Austin/Tony work-preview band | split workflow/agent dashboard with a toggleable timeline |
| Workflow context | inline phase and verification status | dedicated sidebar, phase chain, agent cards and worktrees |
| Conversation | one chronological stream | per-agent panes plus a Duo/Human pane, or one timeline |
| Styling | Legacy styling | rounded panels, TrueColor theme, responsive footer |
| Shared behavior | composer, slash commands, picker, help/details, native attach, clipboard, resume, Core | the same, through the same `Session` surface |
| Default | yes | no |

## Captures

These are real text frames rendered by tcell's SimulationScreen during the
committed capture test, not mockups. The source test is
`internal/gotui/capture_test.go`; colour and style attributes are not visible in
the text files.

| Frame | What it covers |
|---|---|
| [dashboard 120×42](screens/01-dashboard-120x42.txt) | split dashboard, workflow sidebar, agent cards, Duo/Human pane |
| [timeline 120×42](screens/02-timeline-120x42.txt) | single conversation view and restored history |
| [slash palette 120×42](screens/03-slash-120x42.txt) | filtered command palette over the conversation pane |
| [help 120×42](screens/04-help-120x42.txt) | help modal on a cleared backdrop |
| [details 120×42](screens/05-details-120x42.txt) | session details and diffstat |
| [models 120×42](screens/06-models-120x42.txt) | model/thinking picker after the async catalog load |
| [confirm 80×24](screens/07-confirm-80x24.txt) | destructive confirmation at a compact size |
| [narrow 64×22](screens/08-narrow-64x22.txt) | no-sidebar timeline with compact footer |

Regenerate them with:

```bash
DUO_GOTUI_CAPTURE_DIR="$PWD/docs/gotui/screens" \
  go test ./internal/gotui -run TestCaptureFrames -count=1
```

## Verification

The frontend has focused tests for layout, wrapping, Markdown, composer/paste
behavior, session restore, slash filtering, picker state, overlay draw order,
CLI selection, native detach parsing, and a real PTY attach/detach round trip
that checks terminal-state restoration. Run the repository checks with:

```bash
go test ./...
go vet ./...
go build ./cmd/...
make check
```

## Known limitations

- Legacy remains the default; `--ui=gotui` is opt-in.
- The split view and workflow sidebar intentionally disappear on terminals that
  are too small rather than squeezing unreadable panes.
- Captures are deterministic text frames; they do not preserve colour or style.
- Markdown is lightweight: no images, embedded HTML, aligned tables or full
  CommonMark; table rows are shown as plain text.
- Modal buttons are keyboard-driven; mouse support covers tabs, transcripts,
  selection and modal wheel scrolling.
- The available models and whether a switch is live or restarts the agent are
  driver capabilities, not frontend choices.
