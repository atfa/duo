# Duo v0.4.1

**Two peer Pi coding agents in one terminal.**

Duo v0.4.1 combines the peer collaboration runtime with an integrated terminal UI, isolated local sessions, and durable sessions that survive a crash — and now hands the finished artifact back to the repository you launched it from.

## What changed in v0.4.1

- **DONE means delivered.** Dual sign-off in INTEGRATE records the final approval but no longer declares the work finished. Duo then delivers the entire final integrated HEAD into your original repository and only then marks the session `DONE`.
- **Safe by construction.** Delivery is a fast-forward only. Duo refuses when your repository has uncommitted changes, is on a different branch, has diverged, or is in detached HEAD, and never runs `reset --hard`, `checkout -f`, `clean`, `merge --no-ff` or `rebase` on your repository.
- **`duo apply [session-id]`.** If delivery is blocked, Duo keeps the session in INTEGRATE with both signatures intact, writes a `pending` delivery checkpoint, and prints the exact command to retry. On a repository with one pending delivery, `duo apply` needs no arguments.
- **Crash-safe handoff.** The final approval and pending-delivery checkpoint are persisted before Git is touched, so a crash between the fast-forward and the `DONE` write is reconciled on the next `duo --resume` or `duo apply`.
- **Final-tree hygiene.** The INTEGRATE prompt gives Austin an explicit final-tree cleanup duty and Tony a repository-hygiene review, so collaboration-only artifacts do not ship.
- **v0.4.0 sessions still deliver.** A session already marked `DONE` by v0.4.0 can be handed off with `duo apply`.

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

INTEGRATE sign-off is the **final approval**, not the end of the run. Once both agents sign, Duo delivers the integrated HEAD into the repository you started from; only then does the phase become `DONE`. If delivery is unsafe, the session stays in INTEGRATE with both signatures and tells you how to retry.

## Deliver the final result

When Austin and Tony both sign INTEGRATE, Duo hands the whole final integrated HEAD back to your original repository:

1. it re-checks that your repository has not changed while Duo worked;
2. it fast-forwards your recorded branch to the final integrated HEAD;
3. it records the applied HEAD and only then marks the session `DONE`.

Delivery never rewrites your history. Duo will not run `reset --hard`, `checkout -f`, `clean`, `merge --no-ff` or `rebase` on your repository. It refuses to act when:

- your repository has uncommitted or untracked changes,
- your repository is on a different branch than the one Duo recorded,
- your branch has diverged from the Duo base commit, or
- your repository is in detached HEAD state.

When delivery is blocked, Duo stays in INTEGRATE with both signatures preserved, writes a `pending` delivery checkpoint, and tells you exactly what to do:

```bash
cd /path/to/your/repo
duo apply
```

`duo apply` re-runs the same safe handoff. With no argument it uses the repository's single pending delivery and lists the candidates when there is more than one. It is also how a session marked `DONE` by v0.4.0 is handed off.

If your branch cannot be fast-forwarded, Duo leaves your repository untouched and prints the final HEAD so you can finish the merge or cherry-pick yourself.

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

## Known limitations of v0.4.1

- Duo persists collaboration state and validates it against Git, but it does not reconstruct an agent's *reasoning*. If a crash lands mid-task, the agents resume with their own Pi history and the shared Plan, exactly as a human reopening the terminal would.
- Recovery is conservative by design: when it cannot prove an approval is still valid, it revokes the approval rather than trusting it. Expect a re-sign-off after a crash, not a silent pass.
- Automatic crash restart is still not implemented. Duo restarts Austin and Tony when it starts, and `Ctrl+R` / `Ctrl+Y` restart an exited agent, but a dead Duo Core needs a manual `duo --resume`.
- Session files are per-machine and per-repository-path; the state is not portable across machines or across a moved checkout.
- The Duo composer is currently a single-line editor. Use native Pi mode for rich/multiline direct agent interaction.
- Summary panes currently show structured assistant completions, peer messages, connection/phase events, and live working/idle state; they do not yet reproduce every token or rich tool card.
- A frame is redrawn from scratch at up to about 60 FPS; there is no partial-damage or diff-based update. This is intentional for a UI of this size and keeps redraw correctness simple.
- Duo owns direct PTYs for both interactive Pi processes; SIGWINCH propagates terminal size to both, even while detached. Native attach is fullscreen takeover, not an embedded xterm emulator.
- Exited Pi processes can be manually restarted with Ctrl+R (Austin) or Ctrl+Y (Tony), retaining their worktrees and bridge identity. Running agents cannot be restarted; automatic crash restart is not implemented.
- Worktrees are preserved when Duo exits; automatic worktree deletion is still not implemented.
- Delivery is fast-forward only. Duo will not create a merge commit, rebase or overwrite your history; if your branch diverged or you changed it while Duo worked, Duo stops and leaves your repository untouched. Finish the handoff manually from the printed final HEAD, then re-run `duo apply`.

## Development validation

```bash
go test ./...
go build ./cmd/duo
```

The test suite includes Git worktree/integration tests and direct PTY supervisor tests.

## v0.4.2 next steps

v0.4.1 made the final artifact reach your repository. Still open: configurable agent names/roles, per-agent model/provider selection, a configurable integration strategy, and a polished worktree cleanup workflow. Automatic crash restart of Duo itself remains out of scope until the durable session semantics have seen real use.

1. Better streaming summaries/tool-event cards in Austin/Tony panes.
2. Improve native PTY replay beyond the recent raw output buffer.
3. Polished worktree lifecycle cleanup and pruning.
4. Better composer editing/history/multiline paste.
5. Session persistence and recovery after Duo restarts.
