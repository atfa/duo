# Duo

**Two peer Pi coding agents in one terminal.**

Duo runs two Pi coding agents as *peers* rather than as a planner and a subordinate worker. They negotiate a shared Plan, message each other while both keep working, execute in isolated Git worktrees, cross-review each other's commits, and jointly sign off before anything is integrated. When the collaboration finishes, the approved result is handed back to the repository you launched Duo from.

The runtime is a Go coordination core plus a deliberately thin Pi bridge. Duo enforces only what benefits from deterministic coordination — identity, routing, Plan versioning, phase transitions, signatures, Git evidence — and leaves the models free to collaborate naturally.

- **Peer, not hierarchical.** No fixed planner. Either agent can disagree, and disagreement is a normal part of the flow.
- **Evidence over claims.** A clean commit SHA is worth more than an assistant saying "done". Sign-offs are validated against Git, not trusted.
- **Isolated, never destructive.** Austin and Tony never share a working tree, and Duo will not rewrite the history of the repository you launched it from.
- **Durable.** Sessions survive a crash and can be resumed with the shared Plan, worktrees and Pi conversation identity intact.

Current release: **v0.4.7** — see [CHANGELOG.md](./CHANGELOG.md) for the full history.

## Quick start

```bash
cd /path/to/git/repo
duo
```

Then type a task in the composer and press `Enter`. The composer sends to **Austin**. In a fresh task Austin wakes Tony with `duo_send`, they agree a Plan, and Duo Core drives the phases from there.

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
 PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

There is no planner distributing tasks. The human talks to Austin; Austin wakes Tony; after that the two peers message each other directly and neither can sign off on the other's behalf.

Duo Core owns only a small set of reliable institutions — phases, signatures, evidence, worktrees, the harness and integration. How the agents discuss, split the work, or whether to prototype first stays theirs to decide.

## How the collaboration works

```text
PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

Each phase is a **checkpoint, not a behavioral cage**. Duo does not stop an agent from reading code or reviewing a diff early; the phase only decides what evidence is required to advance.

| Phase | What happens | Evidence required to advance |
|---|---|---|
| **PLAN** | Agents negotiate one shared Plan. Provisional edits are allowed. | Both approve the *same* Plan version. |
| **EXECUTE** | Each works independently in their own Git worktree. | Each worktree is clean, with a recorded commit SHA. |
| **REVIEW** | Each reviews the peer's exact commit. | Each approves the exact peer HEAD reviewed. |
| **INTEGRATE** | Tony's branch is merged into Austin's integration worktree. | Both approve the same clean integrated Austin HEAD. |
| **DONE** | The approved artifact has been delivered back to your repository. | Delivery succeeded. |

A plan update creates a new version and **invalidates both signatures**, so wording churn has a visible cost. A sign-off is bound to an exact commit: if the peer pushes a new commit, the previous review is stale and must be repeated.

## The four Duo tools

Austin and Tony share a small tool surface installed with the Pi bridge. It is the only channel through which they can affect shared state.

| Tool | Purpose |
|---|---|
| `duo_send` | Send an important live message to the peer while both keep working. For findings, questions, conflicts and proposals — not routine progress chatter. |
| `duo_set_plan` | Create or replace the whole shared Plan. Each call makes a new version and resets both signatures. |
| `duo_set_status` | Sign the current phase (`ready: true`) or revoke your signature (`ready: false`), with an optional note. |
| `duo_status` | Read the authoritative phase, Plan, signatures, and both worktrees' branch, path, HEAD, cleanliness and ahead count. |

The bridge is a thin adapter — Pi events become Duo activity, Pi tools become Duo requests, Duo messages become Pi steering. It deliberately holds no project truth of its own.

## Terminal UI

Duo shows both agents side by side with their connection, process and working state, plus the current phase, Plan and transient feedback below the panes.

| Key | Action |
|---|---|
| `Enter` | Send composer text to Austin |
| `Ctrl+Enter` / `Shift+Enter`\* | Insert a newline in the composer |
| `Ctrl+A` | Attach Austin's native Pi |
| `Ctrl+T` | Attach Tony's native Pi |
| `Ctrl+]` / `Ctrl+\` | Return from native Pi to Duo |
| `Ctrl+R` / `Ctrl+Y` | Restart Austin / Tony if the process exited or failed |
| `Ctrl+/` | Open or close Duo Help |
| `Ctrl+Q` | Quit Duo and preserve the session |
| `←` / `→` | Move the composer cursor |
| `Backspace` | Delete the preceding composer character |
| Mouse wheel over a pane | Scroll that agent's earlier output |

The composer holds multiple lines and shows up to four at a time. Native attach is a fullscreen takeover: inside Pi, `/model`, `/settings`, `/tree` and all Pi shortcuts belong to Pi.

\* A newline is inserted only when the terminal reports the combination distinctly (`\x1b[13;2u` CSI-u or `\x1b[27;2;13~` modifyOtherKeys, which Duo enables). Terminals that send a bare `\r` for `Shift+Enter` will **submit** instead — use `Ctrl+Enter`, which arrives as `\n`, if `Shift+Enter` submits in your terminal.

Help is a full alternate-screen view; scroll it with `↑`/`k`, `↓`/`j`, `PgUp`, `PgDn`, `Home`/`g`, `End`/`G`, and close it with `Esc` or `Ctrl+/`.

## Configuration

Everything has a working default; `duo` needs no configuration to run.

| Variable | Default | Meaning |
|---|---|---|
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
    state.json      phase, Plan, signatures, evidence, worktree paths/branches, Pi session ids
    events.jsonl    diagnostic journal of phase, signature, bridge and merge events
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

Resume reloads the persisted snapshot, **proves it against Git**, and revokes any signature that is no longer provable before starting a process. Reconciled state is written back before agents start, so a crash during startup cannot resurrect a revoked approval or replay a merge. Each reconnected agent receives one phase-aware wake-up message; reconnects do not duplicate it.

Recovery is deliberately conservative: when it cannot prove an approval is still valid, it revokes rather than trusts. Expect a re-sign-off after a crash, not a silent pass.

## Deliver the final result

`DONE` means the final integrated result has been delivered into the repository you launched Duo from. Delivery is **fast-forward only** (`git merge --ff-only`). Duo never creates a merge commit, rebases, or rewrites your history. If delivery is blocked, `duo apply` retries it by hand.

Delivery refuses — and leaves your repository completely untouched — when:

- the repository has uncommitted changes;
- it is on a different branch than the one Duo started on;
- the current HEAD has diverged from the final Duo result;
- HEAD is detached, or the starting branch is unknown;
- the final result is not derived from the recorded base commit.

A refused delivery keeps the session in `INTEGRATE` with both signatures intact, records a `pending` checkpoint, and prints the exact retry command:

```bash
duo apply                    # retry the pending delivery for this repository
duo apply <session-id>       # retry one specific session
duo apply --session <id>     # same
duo apply -s <id>            # same
duo apply --session=<id>     # same
```

`duo apply` never starts agents and reuses the same safety rules — it will not force a blocked delivery.

## A trace from a real run

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

The design principle that matters here: **a phase is a checkpoint, not a cage.** Tony may look at the diff early, and Austin may prototype during PLAN. Duo puts hard boundaries only around formal consensus and formal artifacts.

The full record lives in [docs/demo.md](./docs/demo.md).

## Limitations

Duo is an experimental runtime. In short: the agent topology is fixed at two agents named Austin and Tony; Pi is the only supported agent runtime; recovery cannot reconstruct an agent's *reasoning*, only its state; Duo Core itself is not auto-restarted after a crash; and sessions are per-machine and per-repository-path, not portable.

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
