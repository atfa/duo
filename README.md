# Duo v0.4.0

**Two peer Pi coding agents in one terminal.**

Duo v0.4.0 combines the peer collaboration runtime with an integrated terminal UI, isolated local sessions, and durable sessions that survive a crash.

## What changed in v0.4.0

- **Durable sessions.** Phase, Plan version, signatures, evidence, worktree record and Pi session identity are persisted to `~/.duo/sessions/<repo-id>/<session-id>/state.json` on every state change, written atomically so a crash can never leave a half-written checkpoint.
- **`duo --resume [session-id]`.** Continue an interrupted session instead of starting over. With no id, Duo resumes the repository's only unfinished session and lists the candidates when there is more than one.
- **Git is the ground truth.** Resume re-checks both worktrees, notices an interrupted or already-completed merge, and revokes every signature whose evidence no longer matches. A session never slips backwards to PLAN and an old approval is never treated as still valid.
- **Dirty worktrees are recoverable.** Uncommitted work does not block resume; Duo reports it, revokes only the signatures it invalidates, and leaves your files alone.
- **Stable Pi identity.** Austin and Tony keep their own Pi conversation across a restart via `--session-id`.
- **One process per session.** An advisory lock, plus a diagnostic journal (`events.jsonl`) and a redacting log (`duo.log`), make concurrent or crashed runs auditable.

## What changed in v0.3.3

- Hardened TUI redraw: resize, native-attach re-entry and layout changes now force one full-screen clear instead of overwriting the previous frame in place, which removes border trails and visual residue when dragging a terminal window.
- Removed the fake 60×18 minimum terminal size. Duo lays out on the real geometry and shows a bounded `Terminal too small` notice below 60×18 instead of wrapping or scrolling.
- Frames are wrapped in DEC synchronized output (`CSI ?2026 h/l`) and autowrap is disabled for the duration of a frame write.
- SIGWINCH is coalesced: a burst of resize signals collapses into one latest-size PTY update and one frame.
- Rendering goes through a dirty scheduler at about 60 FPS; an idle Duo does not repaint, and the spinner only animates while an agent is busy.
- Process state distinguishes `exited` from `failed`, and a Duo-initiated stop is reported as a normal exit.

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

Duo resolves the Git repository root even if you start it from a subdirectory. Each run uses an OS-assigned localhost port plus a random session token, so separate Duo projects do not share agent connections. Automatically generated session names use a timestamp plus an eight-digit random hex suffix; explicit `DUO_SESSION` values are unchanged.

## Resume a session

If Duo, Austin or Tony was killed, or the machine crashed, do not start a new session — resume the old one:

```bash
cd /Users/atfa/fix/pet
duo --resume
```

To choose a specific session, or when several sessions are unfinished:

```bash
duo --resume duo-20260914-8f31c0a2
```

Bare `duo --resume` picks the only unfinished session for this repository; if there is more than one it lists them instead of guessing, and if there is none it tells you so.

On resume Duo:

1. takes the session lock, so two Duo processes cannot drive the same session;
2. restores the checkpoint into the state machine;
3. validates that the recorded worktrees still exist, are Git worktrees, and are on the expected branches — existing worktrees are reused, never recreated;
4. re-derives the truth from Git (worktree HEADs, an interrupted `MERGE_HEAD`, the Austin/Tony integration result);
5. revokes stale signatures and applies the reconciled state **before** agents start, so a crash during startup cannot resurrect an old approval;
6. restarts Austin and Tony with their previous Pi session identity in the same worktrees.

While `duo` runs, a session is considered active. Quitting Duo with `Ctrl+Q` preserves the session and prints the exact resume command.

Session files live in `~/.duo/sessions/`:

```text
state.json    versioned session checkpoint (atomic writes)
events.jsonl  diagnostic journal of phase, signature and merge events
duo.log       lifecycle log; session tokens are redacted
lock          advisory flock, released automatically if Duo dies
```

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
- no Unix `script` command: Duo owns both Pi PTYs directly (macOS/Linux)

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
| Ctrl+R / Ctrl+Y | restart exited Austin / Tony |
| Ctrl+Q | quit Duo |
| Backspace | edit Duo composer |

## Known limitations of v0.4.0

- Duo persists collaboration state and validates it against Git, but it does not reconstruct an agent's *reasoning*. If a crash lands mid-task, the agents resume with their own Pi history and the shared Plan, exactly as a human reopening the terminal would.
- Recovery is conservative by design: when it cannot prove an approval is still valid, it revokes the approval rather than trusting it. Expect a re-sign-off after a crash, not a silent pass.
- Automatic crash restart is still not implemented. Duo restarts Austin and Tony when it starts, and `Ctrl+R` / `Ctrl+Y` restart an exited agent, but a dead Duo Core needs a manual `duo --resume`.
- Session files are per-machine and per-repository-path; the state is not portable across machines or across a moved checkout.
- The Duo composer is currently a single-line editor. Use native Pi mode for rich/multiline direct agent interaction.
- Summary panes currently show structured assistant completions, peer messages, connection/phase events, and live working/idle state; they do not yet reproduce every token or rich tool card.
- A frame is redrawn from scratch at up to about 60 FPS; there is no partial-damage or diff-based update. This is intentional for a UI of this size and keeps redraw correctness simple.
- Duo owns direct PTYs for both interactive Pi processes; SIGWINCH propagates terminal size to both, even while detached. Native attach is fullscreen takeover, not an embedded xterm emulator.
- Exited Pi processes can be manually restarted with Ctrl+R (Austin) or Ctrl+Y (Tony), retaining their worktrees and bridge identity. Running agents cannot be restarted; automatic crash restart is not implemented.
- Worktrees are preserved when Duo exits.
- Duo does not merge the final integration branch into the human's original branch automatically.

## Development validation

```bash
go test ./...
go build ./cmd/duo
```

The test suite includes Git worktree/integration tests and direct PTY supervisor tests.

## v0.4 next steps

v0.4.0 made sessions durable. Still open: configurable agent names/roles, per-agent model/provider selection, a configurable integration strategy, and a polished worktree cleanup workflow. Automatic crash restart of Duo itself remains out of scope until the durable session semantics have seen real use.

1. Better streaming summaries/tool-event cards in Austin/Tony panes.
2. Improve native PTY replay beyond the recent raw output buffer.
3. Session persistence/resume.
4. Better composer editing/history/multiline paste.
5. Session persistence and recovery after Duo restarts.
