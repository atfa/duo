# Duo

**Two peer Pi coding agents in one terminal.**

Duo runs two Pi coding agents as *peers* rather than as a planner and a subordinate worker, in one of two fixed workflows:

- **Fast (default).** Austin drives the implementation; Tony independently verifies the exact commit. `RUNNING → VERIFY → DONE`, with no shared Plan and no dual sign-off — the quick path for most tasks.
- **Goal (`duo --mode goal`).** The full negotiated workflow: a shared Plan, isolated worktrees, cross-review and dual sign-off, `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE`.

In both modes the agents message each other while both keep working, changes stay in isolated Git worktrees, and the verified result is handed back to the repository you launched Duo from.

The runtime is a Go coordination core plus a deliberately thin Pi bridge. Duo enforces only what benefits from deterministic coordination — identity, routing, mode and phase transitions, verification, signatures, Git evidence — and leaves the models free to collaborate naturally.

- **Fast by default, Goal on request.** Fast keeps the safety boundary — worktrees, Git evidence, verified delivery — while dropping the ceremony; Goal adds negotiated planning and dual sign-off for larger tasks.
- **Peer, not hierarchical.** No fixed planner. Either agent can disagree, and disagreement is a normal part of the flow.
- **Evidence over claims.** A clean commit SHA is worth more than an assistant saying "done". Verification and sign-offs are validated against Git, not trusted.
- **Isolated, never destructive.** Austin and Tony never share a working tree, and Duo will not rewrite the history of the repository you launched it from.
- **Durable.** Sessions survive a crash and can be resumed, including their mode, worktrees and Pi conversation identity.

Current release: **v0.5.0** — see [CHANGELOG.md](./CHANGELOG.md) for the full history.

## Quick start

```bash
cd /path/to/git/repo
duo              # FAST (default)
duo --mode goal  # full negotiated workflow
```

Then type a task in the composer and press `Enter`. The composer always sends to **Austin**.

- In **Fast**, Austin works in its own worktree, commits, and calls `duo_set_status` to request verification; Tony verifies that exact commit with `duo_set_verification` and either passes it or returns a concrete issue. Duo then delivers the verified commit and marks `DONE`.
- In **Goal**, Austin wakes Tony with `duo_send`, they agree a shared Plan, and Duo Core drives `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE` from there.

Requirements:

- Git
- Pi available as `pi` on `PATH` (override with `DUO_PI_COMMAND`)
- macOS or Linux — Duo owns both Pi PTYs directly, so no `script` wrapper is needed

## Install

**Released binaries** (macOS/Linux, amd64/arm64; installs `~/.local/bin/duo` and the Pi bridge):

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

**From source** (runs the test suite first, then installs the same two pieces; requires Go 1.22+):

```bash
./scripts/install.sh
```

Restart any running Pi processes after installing the bridge.

## How Duo differs from a planner/worker setup

The common pattern:

```text
Planner
 ├─ Worker A
 └─ Worker B
```

Duo:

```text
            human
             │
             ▼
          Austin
             │ wakes
             ▼
Austin  ◄──────────►  Tony
   │    live messages │
   └──────────┬──────┘
              ▼
 FAST:  RUNNING → VERIFY → DONE
 GOAL:  PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

There is no planner distributing tasks. The human talks to Austin. In Goal mode Austin wakes Tony and the two peers message each other directly; neither can sign off on the other's behalf. In Fast mode Austin is the driver and Tony is a read-only verifier who can only pass or reject the exact commit under review.

Duo Core owns only a small set of reliable institutions — mode, phases, verification, signatures, evidence, worktrees, the harness, integration and delivery. How the agents discuss, split the work, or whether to prototype first stays theirs to decide.

## How the collaboration works

A session has one fixed **mode** for its whole lifetime. Both modes share the same isolated worktrees, Git evidence and delivery handoff; they differ in how much negotiation is required before delivery.

### Fast (default)

```text
RUNNING → VERIFY → DONE
```

| Phase | What happens | Gate to advance |
|---|---|---|
| **RUNNING** | Austin implements and commits in its own worktree. Tony is read-only and may be asked for advice via `duo_send`. | Austin calls `duo_set_status ready=true` with a clean worktree. |
| **VERIFY** | Tony independently inspects Austin's exact commit. | `duo_set_verification` with `passed` (→ delivery → `DONE`) or `issue_found` plus a concrete note (→ `RUNNING`). |
| **DONE** | The verified commit was delivered to your repository. | Delivery succeeded. |

Fast has no shared Plan and no dual sign-off. A passed verification is bound to the exact commit that was requested; any new commit invalidates it and returns the session to `RUNNING`. Only Austin writes to the delivered artifact — Tony's worktree is never delivered.

### Goal (`duo --mode goal`)

```text
PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

| Phase | What happens | Evidence required to advance |
|---|---|---|
| **PLAN** | Agents negotiate one shared Plan. Provisional edits are allowed. | Both approve the *same* Plan version. |
| **EXECUTE** | Each works independently in their own Git worktree. | Each worktree is clean, with a recorded commit SHA. |
| **REVIEW** | Each reviews the peer's exact commit. | Each approves the exact peer HEAD reviewed. |
| **INTEGRATE** | Tony's branch is merged into Austin's integration worktree. | Both approve the same clean integrated Austin HEAD. |
| **DONE** | The approved artifact has been delivered back to your repository. | Delivery succeeded. |

In both modes a phase is a **checkpoint, not a behavioral cage**: Duo does not stop an agent from reading code or reviewing a diff early; the phase only decides what evidence is required to advance.

`DONE` is terminal for the round, not for the session. Submitting another task through the Duo composer reopens it for a new round — Fast returns to `RUNNING`, Goal to `PLAN` — and clears the finished round's delivery checkpoint so the follow-up can be verified and delivered on its own. Talking to an agent directly through native Pi (`Ctrl+A` / `Ctrl+T`) deliberately does not do this: that path bypasses Duo and leaves the phase untouched.

A Goal plan update creates a new version and **invalidates both signatures**, so wording churn has a visible cost. In Goal, a sign-off is bound to an exact commit: if the peer pushes a new commit, the previous review is stale and must be repeated.

## The Duo tools

Austin and Tony share a small tool surface installed with the Pi bridge. It is the only channel through which they can affect shared state.

| Tool | Modes | Purpose |
|---|---|---|
| `duo_send` | both | Send an important live message to the peer while both keep working. For findings, questions, conflicts and proposals — not routine progress chatter. |
| `duo_set_status` | both | In Goal, sign the current phase (`ready: true`) or revoke your signature (`ready: false`). In Fast, Austin uses `ready: true` to request verification; a Tony sign-off is rejected with guidance. |
| `duo_set_verification` | Fast (Tony only) | Report `passed` or `issue_found` for Austin's exact commit under review. `issue_found` requires a concrete note. |
| `duo_set_plan` | Goal | Create or replace the whole shared Plan. Each call makes a new version and resets both signatures. Not available in Fast. |
| `duo_status` | both | Read the authoritative mode, phase, verification status, Plan, signatures, and both worktrees' branch, path, HEAD, cleanliness and ahead count. |

Duo Core enforces these gates rather than trusting the model: Fast rejects `duo_set_plan`, and a Tony `duo_set_status` in Fast is rejected with guidance to use `duo_set_verification`. The bridge is a thin adapter — Pi events become Duo activity, Pi tools become Duo requests, Duo messages become Pi steering. It deliberately holds no project truth of its own.

## Terminal UI

Duo shows both agents side by side with their connection, process and working state, plus the current mode, phase, verification/Plan state and transient feedback below the panes.

A build can take a long time, and the pane transcript only changes when an agent finishes a message. Below the panes Duo therefore keeps a **work preview** for each agent: the state word and how long the agent has been quiet, the tool currently running with its arguments, the last error of the turn, and the tail of the text being streamed. A failed tool or a provider error is visible there immediately, so waiting does not mean guessing. `Ctrl+P` collapses the band when you want the rows back for the panes; on a short terminal it is hidden automatically.

| Key | Action |
|---|---|
| `Enter` | Send composer text to Austin |
| `Ctrl+Enter` / `Shift+Enter`\* | Insert a newline in the composer |
| `Ctrl+A` | Attach Austin's native Pi |
| `Ctrl+T` | Attach Tony's native Pi |
| `Ctrl+]` / `Ctrl+\` / `Ctrl+】` | Return from native Pi to Duo |
| `Ctrl+R` / `Ctrl+Y` | Restart Austin / Tony if the process exited or failed |
| `Ctrl+/` | Open or close Duo Help |
| `Ctrl+P` | Show or hide the Austin/Tony work preview band |
| `Ctrl+M` / `Alt+M` | Open the model picker for Austin or Tony |
| `Shift+Tab` | Cycle the target agent's Pi thinking level |
| `Ctrl+Q` | Quit Duo and preserve the session |
| `←` / `→` | Move the composer cursor |
| `Backspace` | Delete the preceding composer character |
| Mouse wheel over a pane | Scroll that agent's earlier output |
| Mouse drag over a pane | Select that agent's text and copy it to the clipboard |

The composer holds multiple lines and shows up to four at a time. Native attach is a fullscreen takeover: inside Pi, `/model`, `/settings`, `/tree` and all Pi shortcuts belong to Pi.

The model picker (`Ctrl+M`) lists the catalog Pi reports for the very installation Duo launched (`pi --list-models`). Type to filter, move with `↑`/`↓` (or `PgUp`/`PgDn`, `Home`/`End`), switch the target between Austin and Tony with `Tab`, cycle that agent's thinking level with `Shift+Tab`, and apply the model with `Enter` (apply and close) or `Space` (apply and keep the picker open, so a model and a thinking level can be set in one visit). The switch is live: Pi keeps the conversation and records both the model and the thinking level in the session transcript, so a restart or resume (`Ctrl+R`/`Ctrl+Y`) keeps the choice. `▶` marks the picker cursor and `●` the target's current model.

Agent output is rendered as lightweight markdown: headings, blockquotes, links and bold/italic/code spans are styled, and markdown tables are drawn with real, aligned borders. A table wider than its pane is narrowed by wrapping the widest cells instead of truncating them, and a wrapped list keeps a hanging indent so continuation lines stay under the item text.

Mouse selection copies through the platform clipboard command, which today means `pbcopy` on macOS. On other platforms the selection still highlights but the copy step fails and Duo reports it in the status line.

\* A newline is inserted only when the terminal reports the combination distinctly (`\x1b[13;2u` CSI-u or `\x1b[27;2;13~` modifyOtherKeys, which Duo enables). Terminals that send a bare `\r` for `Shift+Enter` will **submit** instead — use `Ctrl+Enter`, which arrives as `\n`, if `Shift+Enter` submits in your terminal. The same limit applies to `Ctrl+M`: it is reported as the `m` key with Ctrl (`\x1b[27;5;109~` or `\x1b[109;5u`) only on terminals that honor those modes, and on the others it is indistinguishable from `Enter` — use `Alt+M` there.

Help is a full alternate-screen view; scroll it with `↑`/`k`, `↓`/`j`, `PgUp`, `PgDn`, `Home`/`g`, `End`/`G`, and close it with `Esc` or `Ctrl+/`.

## Configuration

Everything has a working default; `duo` needs no configuration to run.

| Variable | Default | Meaning |
|---|---|---|
| `DUO_MODE` | `fast` | Session mode: `fast` or `goal`. A CLI `--mode`/`-m` wins; `--resume` keeps the persisted mode. |
| `DUO_REPO` | current directory | Repository or subdirectory to launch against. A CLI path argument wins. |
| `DUO_SESSION` | timestamp + random hex | Session id, formatted `YYYYMMDD-HHMMSS-xxxxxxxx`. |
| `DUO_WORKTREE_ROOT` | `~/.duo/worktrees/<repo>-<hash>/<session>` | Where the Austin/Tony worktrees are created. |
| `DUO_BASE_REF` | `HEAD` | Ref the worktrees are branched from. |
| `DUO_PI_COMMAND` | `pi` | Command used to launch each Pi agent. |
| `DUO_LISTEN` | `127.0.0.1:0` | Bridge listen address (an OS-assigned port by default). |
| `DUO_HARNESS` | `true` | Enable the idle/stall watchdog that nudges coordination back to life. |
| `DUO_HARNESS_IDLE_SECONDS` | `15` | Quiet period before an agent counts as idle. |
| `DUO_HARNESS_STALL_SECONDS` | `300` | No progress for this long counts as a stall. |
| `DUO_HARNESS_COOLDOWN_SECONDS` | `30` | Minimum gap between harness nudges. |
| `DUO_HARNESS_RESUME_GRACE_SECONDS` | `45` | Extra grace after `--resume`, so reconnecting is not mistaken for a stall. |

To run a non-default Pi command:

```bash
DUO_PI_COMMAND='pi --some-flag' duo
```

## Where Duo keeps things

```text
~/.duo/sessions/<repo-id>/<session-id>/
    state.json      mode, phase, verification/Plan, signatures, evidence, worktree paths/branches, Pi session ids
    events.jsonl    diagnostic journal of phase, signature, bridge and merge events,
                    plus the pane transcript restored into the TUI on resume
    duo.log         lifecycle output (session tokens are redacted before writing)
    lock            advisory flock holding the owner PID and hostname

~/.duo/worktrees/<repo>-<hash>/<session>/
    austin/         Austin's worktree — also the integration worktree
    tony/           Tony's worktree
```

Session directories are created owner-only and contain no credentials, but they do describe your project's state; see [SECURITY.md](./SECURITY.md).

## Working scope

Duo separates the Git boundary from the agents' default working directory. Launching from a subdirectory keeps the repository as the Git boundary but makes that subdirectory the agents' default cwd:

```bash
cd repo/packages/web
duo
```

Austin and Tony then start in the `packages/web` directory inside their own worktrees, while branches, worktrees and delivery keep their Git boundary at `repo`. The mapping is done safely against the worktree root, so a scope cannot escape it.

Scope is a **default working directory, not a filesystem sandbox**. Agents can still reach the rest of the repository when the task genuinely requires it. The scope is recorded in the session and restored on resume, and each agent's active scope is stated in its system prompt.

## Resuming after a crash

```bash
duo --resume             # resume this repository's unfinished session
duo -r                   # same
duo --resume <id>        # resume one specific session
duo --resume=<id>        # same
```

A plain `duo` always starts a **new** session and refuses to overwrite an unfinished one. `--resume` picks the only unfinished session if there is exactly one, and asks for an id if there are several.

Resume reloads the persisted snapshot, **proves it against Git**, and revokes any signature or verification that is no longer provable before starting a process. The mode is persisted with the session: `--resume` keeps it, a legacy session with no recorded mode resumes as Goal, and `DUO_MODE` is ignored on resume (an explicit conflicting `--mode` is an error). Reconciled state is written back before agents start, so a crash during startup cannot resurrect a revoked approval or replay a merge. Each reconnected agent receives one mode- and phase-aware wake-up message; reconnects do not duplicate it. The most recent 200 entries of each pane are replayed into the TUI, so a resumed session starts with its previous conversation visible.

Recovery is deliberately conservative: when it cannot prove an approval is still valid, it revokes rather than trusts. Expect a re-sign-off after a crash, not a silent pass.

## Deliver the final result

`DONE` means the final result has been delivered into the repository you launched Duo from — the verified Austin commit in Fast, or the dual-signed integrated HEAD in Goal. Delivery is **fast-forward only** (`git merge --ff-only`). Duo never creates a merge commit, rebases, or rewrites your history. If delivery is blocked, `duo apply` retries it by hand.

A task that changes no repository files is a no-op: once the final commit is already an ancestor of your HEAD, delivery succeeds and records `DONE` even if the working tree is dirty. For any delivery that would actually move your branch, the safety checks below still apply.

Delivery refuses — and leaves your repository completely untouched — when:

- the repository has uncommitted changes;
- it is on a different branch than the one Duo started on;
- the current HEAD has diverged from the final Duo result;
- HEAD is detached, or the starting branch is unknown;
- the final result is not derived from the recorded base commit.

If you resolve the divergence yourself, Duo recognizes the result instead of refusing: once the final Duo commit is an ancestor of your HEAD — including after a `git merge --no-ff` that keeps both histories — `duo apply` treats the delivery as already applied and records `DONE` without moving your branch again. A refused delivery prints that merge command alongside the retry command:

```bash
git merge --no-ff <final-head>   # from the repository you launched Duo from
duo apply
```

Until then, a refused delivery keeps the session in `INTEGRATE` (Goal) or `VERIFY` (Fast), records a `pending` checkpoint, and prints the exact retry command:

```bash
duo apply                    # retry the pending delivery for this repository
duo apply <session-id>       # retry one specific session
duo apply --session <id>     # same
duo apply -s <id>            # same
duo apply --session=<id>     # same
```

`duo apply` never starts agents and reuses the same safety rules — it will not force a blocked delivery.

## Traces from real runs

### Fast mode (default)

A condensed fast-mode trace:

```text
human → Austin

Austin works → commit 4c1a9f2          (RUNNING)
Austin: duo_set_status ready=true
  → VERIFY, verification bound to 4c1a9f2

Tony inspects 4c1a9f2
Tony: duo_set_verification issue_found
  "src/cache.ts still caches a failed lookup, so the retry never happens"
  → RUNNING

Austin fixes → commit 8e02b1d
Austin: duo_set_status ready=true
  → VERIFY, bound to 8e02b1d

Tony: duo_set_verification passed
  → delivery fast-forwards your branch to 8e02b1d
DONE
```

### Goal mode

A condensed trace from a successful v0.2 run against a small pet-hospital web app — **not this repository**. The commit hash below belongs to that app, so `git show` on it here will fail:

```text
human → Austin

Austin → Tony:
"The user thinks the UI looks dated. I have a tentative read; analyse it
independently, don't just agree with me."

Tony → Austin:
"I disagree with a wholesale font/badge rewrite. The real problems look more
like the bitmap grid, the decorative circles, the corner-radius scale and
the shadows."

Shared Plan v1
Austin ✓
Tony   ✓

PLAN → EXECUTE
Austin edits → commit 7bf559e
Tony reviews that commit ✓

EXECUTE → REVIEW → INTEGRATE
Austin ✓ integrated HEAD
Tony   ✓ same HEAD

DONE
```

The design principle that matters here: **a phase is a checkpoint, not a cage.** Tony may look at the diff early, and Austin may prototype during PLAN. Duo puts hard boundaries only around formal consensus and formal artifacts. In Fast mode the same principle holds with a smaller ceremony: Tony may inspect the commit at any time, but only a `duo_set_verification` result gates delivery.

The full record lives in [docs/demo.md](./docs/demo.md).

## Limitations

Duo is an experimental runtime. In short: the agent topology is fixed at two agents named Austin and Tony; Pi is the only supported agent runtime; the session mode is fixed at launch, with no runtime switching or automatic escalation to Goal; Fast is single-writer, so Tony never commits to the delivered artifact; recovery cannot reconstruct an agent's *reasoning*, only its state; Duo Core itself is not auto-restarted after a crash; and sessions are per-machine and per-repository-path, not portable.

The full, current list — including what is deliberately a non-goal — is in [docs/known-limitations.md](./docs/known-limitations.md).

## Development

```bash
make check     # go test ./... && go vet ./... && go build ./cmd/duo
```

Or individually:

```bash
go test ./...
go vet ./...
go build ./cmd/duo
```

The suite covers Git worktree and integration behavior, PTY supervision, durable session reconcile, delivery concurrency, and phase transition rules. CI runs it on Go 1.22.x and Go stable across Ubuntu and macOS; pushing a `v*` tag builds and publishes the four release archives.

## Documentation

| Document | Contents |
|---|---|
| [docs/architecture.md](./docs/architecture.md) | Component boundaries, why worktrees instead of locks, why PLAN is not a write lock. |
| [docs/known-limitations.md](./docs/known-limitations.md) | Complete limitations and explicit non-goals. |
| [docs/demo.md](./docs/demo.md) | A condensed trace of one real two-agent run. |
| [docs/publishing.md](./docs/publishing.md) | Release and repository notes. |
| [CHANGELOG.md](./CHANGELOG.md) | Release history. |
| [ROADMAP.md](./ROADMAP.md) | Where the project is heading. |
| [CONTRIBUTING.md](./CONTRIBUTING.md) | Design principles to preserve, and where to start. |
| [SECURITY.md](./SECURITY.md) | Trust model, Git safety boundary, on-disk data, disclosure. |

## License

MIT — see [LICENSE](./LICENSE).
