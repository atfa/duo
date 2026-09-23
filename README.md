# Duo v0.3.1

**Two peer Pi coding agents in one terminal.**

Duo v0.3.1 combines the peer collaboration runtime with an integrated terminal UI and isolated, authenticated local sessions.

## What changed in v0.3

- `duo` can be launched from inside a Git repository: `cd project && duo`.
- Duo creates Austin/Tony worktrees automatically.
- Duo launches **two real interactive Pi TUI processes** in hidden pseudo-terminals.
- The default terminal shows three areas: Austin summary, Tony summary, and the Duo composer/status area.
- `Ctrl+A` opens Austin's real Pi TUI; `Ctrl+T` opens Tony's real Pi TUI.
- Clicking either top header (`[↗]`) also opens that agent's native Pi terminal on terminals that report SGR mouse clicks.
- While inside native Pi, `/model`, `/settings`, `/tree`, extension UI, custom footer, etc. are handled by Pi itself.
- Press `Ctrl+]` or `Ctrl+\\` to detach from native Pi and return to Duo.
- `Ctrl+Q` quits Duo and preserves worktrees.
- Provider errors such as HTTP 402/429 are shown in full and highlighted in red.
- The TUI uses color for titles, borders, status, help, and error output.
- Non-Git directories and empty repositories show copy-pasteable Git setup commands without initializing the repository automatically.

This version intentionally does **not** reimplement Pi's slash commands.

## Install / upgrade

Install the latest macOS or Linux release (amd64 or arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

This installs the `duo` binary at `~/.local/bin/duo` and the opt-in Pi bridge at `~/.pi/agent/extensions/duo`. The bridge stays inactive during ordinary `pi` sessions and is enabled only for Pi processes launched by Duo.

To build from source instead:

From the Duo source directory:

```bash
./scripts/install.sh
```

This installs:

- the current bridge at `~/.pi/agent/extensions/duo`
- the `duo` binary at `~/.local/bin/duo`

The installer disables bridge directories created by the earlier Duo prototypes (`duo-v02` / `duo-export.ts`) to avoid duplicate `duo_send` tool registration.

If `~/.local/bin` is not already in your shell PATH:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

## Start

Preferred:

```bash
cd /Users/atfa/fix/pet
duo
```

Also supported:

```bash
duo /Users/atfa/fix/pet
```

If you are developing Duo itself without installing the binary:

```bash
./scripts/install-pi-extension.sh
./scripts/run-core.sh /Users/atfa/fix/pet
```

Duo resolves the Git repository root even if you start it from a subdirectory. Each run uses an OS-assigned localhost port plus a random session token, so separate Duo projects do not share agent connections.

## Default UI

Conceptually:

```text
┌ Austin · working                         [↗] ┬ Tony · idle                         [↗] ┐
│ ... structured Duo summary ...               │ ... structured Duo summary ...          │
│                                              │                                          │
├──────────────────────────────────────────────┴──────────────────────────────────────────┤
│ Duo · PLAN · Plan v1 · Austin ✓ · Tony ○                                               │
│ Plan: ...                                                                               │
│ ... Duo / harness / phase events ...                                                    │
│ > user task                                                                             │
└ Enter send · Ctrl+A Austin native · Ctrl+T Tony native · Ctrl+Q quit · Ctrl+] / Ctrl+\\ return ┘
```

The bottom input is **Duo's human entry point**. Text is sent to Austin. Austin's Duo system prompt tells Austin to wake Tony through `duo_send` and build a shared plan when collaboration is useful.

## Native Pi mode

Duo does not emulate Pi's control surface.

Press:

```text
Ctrl+A   Austin native Pi
Ctrl+T   Tony native Pi
Ctrl+] / Ctrl+\\   return to Duo
```

When native mode is active, keyboard bytes go directly to that agent's real Pi process. Therefore Pi remains responsible for `/model`, `/settings`, session controls, extension shortcuts, custom footer/UI, and any other native Pi functionality.

The first native view is reconstructed from recent PTY output.

## Collaboration lifecycle

```text
PLAN -> EXECUTE -> REVIEW -> INTEGRATE -> DONE
```

PLAN is **not** a file-write lock. Both agents may investigate or make provisional changes in their private worktrees while negotiating. Formal sign-off controls the agreed plan and artifacts, not every edit operation.

EXECUTE sign-off is bound to a clean commit SHA. REVIEW sign-off is bound to the peer HEAD actually reviewed. INTEGRATE sign-off is bound to Austin's clean integrated HEAD. Stale signatures are revoked automatically when the signed target changes.

## Requirements

- macOS or Linux
- Git
- Pi available as `pi` in PATH
- the Unix `script` command (preinstalled on macOS and typical Linux distributions)

Building from source additionally requires Go 1.22+.

To launch a non-default Pi command:

```bash
DUO_PI_COMMAND='pi --some-flag' duo
```

## Keyboard

| Key | Action |
|---|---|
| Enter | send Duo composer text to Austin |
| Ctrl+A | attach Austin native Pi |
| Ctrl+T | attach Tony native Pi |
| Ctrl+] / Ctrl+\\ | detach native Pi and return to Duo |
| Ctrl+Q | quit Duo |
| Backspace | edit Duo composer |

## Known limitations of v0.3.1

- The Duo composer is currently a single-line editor. Use native Pi mode for rich/multiline direct agent interaction.
- Summary panes currently show structured assistant completions, peer messages, connection/phase events, and live working/idle state; they do not yet reproduce every token or rich tool card.
- The hidden Pi terminal uses the OS `script` utility as a PTY host. Native attach is fullscreen takeover, not an embedded xterm emulator.
- Native Pi process auto-restart is not implemented yet. If a Pi process exits, restart Duo.
- Worktrees are preserved when Duo exits.
- Duo does not merge the final integration branch into the human's original branch automatically.

## Development validation

```bash
go test ./...
go build ./cmd/duo
```

The test suite includes Git worktree/integration tests and a PTY smoke test using `script`.

## v0.3 next steps

1. Better streaming summaries/tool-event cards in Austin/Tony panes.
2. Process death detection + restart controls.
3. Session persistence/resume.
4. Better composer editing/history/multiline paste.
5. More exact native PTY resize propagation; if necessary, replace the `script` host with a direct PTY implementation.
