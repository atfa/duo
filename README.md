# Duo

**Two peer coding agents in a terminal, coordinated through isolated Git worktrees.**

Duo runs two coding agents as *peers* rather than as a planner and a subordinate worker, in one of three fixed workflows:

- **Fast (default).** Austin drives the implementation; Tony independently verifies the exact commit. `RUNNING → VERIFY → DONE`, with no shared Plan and no dual sign-off — the quick path for most tasks.
- **Goal (`duo --mode goal`).** The full negotiated workflow: a shared Plan, isolated worktrees, cross-review and dual sign-off, `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE`.
- **Step (`duo --mode step`).** For one complex task that splits into an ordered queue: the Goal phase chain runs **once per step**, and each step is landed on your branch as it is verified, with no human task entry between steps.

In all three modes the agents message each other while both keep working, changes stay in isolated Git worktrees, and the verified result is handed back to the repository you launched Duo from.

The runtime is a Go coordination core plus thin driver plugins and optional agent-side bridges. Duo enforces only what benefits from deterministic coordination — identity, routing, mode and phase transitions, verification, signatures, Git evidence — and leaves the models free to collaborate naturally.

## Quick start

```bash
cd /path/to/git/repo
duo              # FAST (default)
duo --mode goal  # full negotiated workflow
duo --mode step  # one complex task, landed step by step
```

Then type a task in the composer and press `Enter`. In **Fast**, Duo sends it to **Austin**. In **Goal** and **Step**, Duo sends it to **both Austin and Tony** so they can co-design the shared Plan.

- In **Fast**, Austin works in its own worktree, commits, and calls `duo_set_status` to request verification; Tony verifies that exact commit with `duo_set_verification` and either passes it or returns a concrete issue. Duo then delivers the verified commit and marks `DONE`.
- In **Goal**, Austin and Tony receive the task together, agree a shared Plan with `duo_send` and `duo_set_plan`, and Duo Core drives `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE` from there.
- In **Step**, Austin publishes an ordered queue with `duo_set_steps` and a prose design with `duo_set_plan`; both agents approve the queue, then Duo runs the Goal cycle once per step, landing each verified step on your branch and advancing the queue by itself.

Requirements:

- Git
- macOS or Linux — Duo owns both agent PTYs directly, so no `script` wrapper is needed
- At least one supported agent CLI on `PATH`: [Pi](#agent-drivers), [agy](#agent-drivers), or [opencode](#agent-drivers)
- Optional: `sqlite3` on `PATH` for `agy` token metrics in the work preview

## Install

**Released binaries** (macOS/Linux, amd64/arm64; installs `duo`, driver helpers and agent bridges):

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

**From source** (runs the Go test suite, then installs Duo and its driver bridges;
requires Go 1.22+):

```bash
./scripts/install.sh
```

Restart any running Pi or opencode processes after installing the bridges.

## How the collaboration works

A session launches in Fast (default), Goal or Step mode. A Fast session can dynamically escalate to Goal mode on the fly without losing in-flight work or recreating worktrees. All modes share the same isolated worktrees, Git evidence and delivery handoff; they differ in how much negotiation is required before delivery.

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

**Dynamic Escalation**: If a task started in Fast mode turns out to be more complex than expected, either the human (typing `/escalate [reason]` or `/mode goal` in the composer) or an agent (calling `duo_escalate`) can dynamically upgrade the session to Goal mode. The session transitions to `PLAN` with a shared plan v1 seeded with the escalation reason, Tony becomes an active co-developer, and all prior commits are preserved.

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

### Step (`duo --mode step`)

For one complex task that is worth landing in increments. The queue is the contract:

```text
PLAN → (per step: EXECUTE → REVIEW → INTEGRATE → land the step) → DONE
```

| Phase | What happens | Evidence required to advance |
|---|---|---|
| **PLAN** | Agents co-design an ordered queue (Austin publishes it with `duo_set_steps`, the prose design with `duo_set_plan`). | Both approve the *same* queue version, which must be non-empty. |
| **EXECUTE** | Austin implements exactly the current step. Tony is read-only on the artifact. | Austin calls `duo_set_status ready=true` with a committed, clean HEAD. |
| **REVIEW** | Tony verifies that exact HEAD. | `duo_set_verification` with `passed` (→ land the step) or `issue_found` with a concrete note (→ back to `EXECUTE`). |
| **INTEGRATE** | Duo lands the step on your branch, fast-forwards Tony's worktree to the landed head, and advances the queue. | Delivery succeeded. The **final** step additionally needs both agents to sign `INTEGRATE`. |
| **DONE** | Every step has landed and the final one was signed by both agents. | Delivery succeeded. |

Each step must be a committable increment whose test command passes at its own HEAD. Duo owns the queue and its cursor — agents never tick a box, and a landed step is never removed or reordered; a step that turns out to be too big is split by appending to the end of the queue.

The per-step gate is deliberately lighter than Goal's: one HEAD-bound verification instead of dual sign-off per step, because the independent check is what makes the mode worth having and Austin signing his own `INTEGRATE` would be self-approval. The full dual sign-off is kept where it buys real agreement — the queue in `PLAN`, and the final step. Because each landed step goes to the branch you launched Duo from, the verifier's worktree is fast-forwarded to the landed head after every step, so Tony always reviews and builds the tree you actually received rather than a stale one.

In all modes a phase is a **checkpoint, not a behavioral cage**: Duo does not stop an agent from reading code or reviewing a diff early; the phase only decides what evidence is required to advance.

`DONE` is terminal for the round, not for the session. Submitting another task through the Duo composer reopens it for a new round — Fast returns to `RUNNING`, Goal to `PLAN`, Step to `PLAN` with a fresh queue — and clears the finished round's delivery checkpoint so the follow-up can be verified and delivered on its own. Attaching to an agent's own session (`Ctrl+A` / `Ctrl+T`) does not route a task through Duo, but it stays recoverable in Fast mode: when Austin commits new work there and requests verification, Duo opens a fresh round on the new HEAD instead of rejecting the request, so the follow-up can still be verified and delivered. A repeated request that still names the already-delivered HEAD is rejected, so a stray resend never reopens a delivered session.

When a round reaches `DONE` in any mode, Duo asks Austin to summarise the session for you: how the work actually went, and what the repository now has that it did not have before. Austin answers in a normal message, so the report arrives in the timeline as `Austin → Human`, in bold like the human's own mail. The request is made once per finished round, and the next round reports on itself.

A Goal plan update creates a new version and **invalidates both signatures**, so wording churn has a visible cost. In Goal, a sign-off is bound to an exact commit: if the peer pushes a new commit, the previous review is stale and must be repeated.

## The Duo tools

Austin and Tony share a small tool surface installed with the Pi bridge. It is the only channel through which they can affect shared state.

| Tool | Modes | Purpose |
|---|---|---|
| `duo_send` | both | Send an important live message to the peer while both keep working. For findings, questions, conflicts and proposals — not routine progress chatter. |
| `duo_set_status` | all | In Goal and Step, sign the current phase (`ready: true`) or revoke your signature (`ready: false`). In Fast, Austin uses `ready: true` to request verification; a Tony sign-off is rejected with guidance. |
| `duo_set_verification` | Fast + Step (Tony only) | Report `passed` or `issue_found` for Austin's exact commit under review. `issue_found` requires a concrete note. In Step, `passed` lands the step and advances the queue. |
| `duo_set_plan` | Goal + Step | Create or replace the whole shared Plan. Each call makes a new version and resets both signatures. Not available in Fast. |
| `duo_set_steps` | Step | Create or replace the step queue as a markdown checkbox list. Each call makes a new version and resets both signatures. Duo owns the ticks. |
| `duo_escalate` | Fast | Dynamically escalate the session to Goal mode on the fly (switches to PLAN phase and initializes shared plan v1). Step is chosen at start and is not an escalation target. |
| `duo_status` | all | Read the authoritative mode, phase, verification status, Plan, step queue and cursor, signatures, and both worktrees' branch, path, HEAD, cleanliness and ahead count. |

Duo Core enforces these gates rather than trusting the model: Fast rejects `duo_set_plan`, and a Tony `duo_set_status` in Fast is rejected with guidance to use `duo_set_verification`. The bridge is a thin adapter — Pi events become Duo activity, Pi tools become Duo requests, Duo messages become Pi steering. It deliberately holds no project truth of its own.

## Terminal UI

Duo's main view is one conversation **timeline**: a single chronological stream in which each message appears on its speaker's own side — Austin, the human and Duo system lines on the left, Tony's pushed to the right — under a `Sender → Receiver` header. Headers are coloured by speaker: a message addressed to the human is bold in the speaker's own colour, while agent-to-agent and system headers stay dim. System notices, harness notes, errors and verdicts are labeled the same way (`Duo → Human`, `Duo → Tony`, …), never with a bare speaker name. Both speakers share the same bubble width (about three quarters of the row), so the side a block sits on, not a heavier header, tells you who is speaking; a Tony message that fits on one line hugs the right edge, while a wrapped one is left-anchored at its indent. The top border row names the Git repository Duo resolved (never the launch directory), so the target of the session is visible without opening Help. Human tasks, peer messages, verifier verdicts and Duo system messages are interleaved in arrival order, and the current mode, phase, verification/Plan state and transient feedback stay below the timeline.

A build can take a long time, and the timeline only changes when an agent finishes a message. Below it Duo therefore keeps a **work preview** for each agent. Its header names the agent, its driver, its animated state and how long the current turn has been running (`Austin preview (agy) · working · 5s`, `Tony preview (pi) · idle`) — the state spinner and the `[↗]` attach button live here now; the rows below show what it is doing *now* (the running tool and how long it has been running, or the model and thinking level it is thinking with), the turn's recent tool trail with ✓/✗ and durations, the turn's last error, and the tail of the text being streamed. A failed tool or a provider error is visible there immediately, so waiting does not mean guessing. `Ctrl+P` collapses the band when you want the rows back for the timeline; on a short terminal it is hidden automatically.

| Key | Action |
|---|---|
| `Enter` | Send composer text to Austin in Fast, or both agents in Goal |
| `Ctrl+Enter` / `Shift+Enter`\* | Insert a newline in the composer |
| `Ctrl+A` | Attach to Austin's own agent session directly |
| `Ctrl+T` | Attach to Tony's own agent session directly |
| `Ctrl+]` / `Ctrl+\` / `Ctrl+】` | Return from the agent's own session to Duo |
| `Ctrl+R` / `Ctrl+Y` | Restart Austin / Tony if the process exited or failed |
| `Ctrl+/` | Open or close Duo Help |
| `Ctrl+P` | Show or hide the Austin/Tony work preview band |
| `Ctrl+M` / `Alt+M` | Open the model picker for Austin or Tony |
| `Ctrl+O` | Toggle session overview: worktrees, verification or Plan, delivery state, and Git changes diffstat |
| `Ctrl+G` | Toggle message timestamps |
| `Ctrl+X` | Copy Austin's latest message to the human to the clipboard |
| `Shift+Tab` | Cycle the target agent's thinking level |
| `Ctrl+Q` / `Ctrl+D` | Quit Duo and preserve the session |
| `←` / `→` | Move the composer cursor |
| `Alt+←` / `Alt+→` | Move the composer cursor by word |
| `↑` / `↓` | Move between composer lines; recall task history at the first/last line |
| `Home` / `End` | Move to the start or end of the composer line |
| `Ctrl+U` / `Ctrl+K` | Delete to the start or end of the composer line |
| `Ctrl+W` | Delete the previous word |
| `Backspace` / `Del` | Delete the preceding or following composer character |
| `PgUp` / `PgDn` | Scroll the conversation timeline earlier or later |
| Mouse wheel over the timeline | Scroll earlier messages |
| Mouse drag over the timeline | Select text and copy it to the clipboard |
| Mouse click on `[↗]` in a preview header | Attach that agent's own session, the same as `Ctrl+A` / `Ctrl+T` |

Each timeline message header carries its `HH:MM:SS` time by default (`Austin → Tony · 14:30:05`), so one exchange is readable end to end; `Ctrl+G` hides or restores those stamps.

The composer holds multiple lines and shows up to four at a time. Native attach is a fullscreen takeover: inside Pi, `/model`, `/settings`, `/tree` and all Pi shortcuts belong to Pi.

### Slash Commands

Typing `/` in the composer opens an interactive command palette showing available slash commands and descriptions. Use `↑` / `↓` to cycle through selections, `Tab` to autocomplete the command name, `Enter` to select (which executes immediate commands or fills in the command prefix for commands that take arguments), and `Esc` to dismiss the menu.

| Command | Description |
|---|---|
| `/escalate [reason]` | Dynamically upgrade the current Fast mode session to Goal mode |
| `/mode` | Print the current session mode |
| `/mode goal` | Upgrade the session to Goal mode; `/mode fast` is rejected, a session never returns to Fast |
| `/model` | Open the model and thinking level picker (`Ctrl+M`) |
| `/overview` | Toggle the session overview and git diffstat (`Ctrl+O`) |
| `/preview` | Toggle the agent work preview band (`Ctrl+P`) |
| `/timestamps` | Toggle timeline message timestamps (`Ctrl+G`) |
| `/copy` | Copy Austin's latest message to the human to the clipboard (`Ctrl+X`) |
| `/help` | Open the full Duo interactive help screen (`Ctrl+/`) |
| `/status` | Show authoritative session mode, phase, and worktree info |
| `/clear` | The composer only holds this command, so it is already empty; Duo forces a full redraw and says so |
| `/quit` or `/exit` | Exit Duo and preserve session state |

An unknown command is reported as an error. `//text` and an absolute path (e.g. `/usr/bin/python`) are sent as a task (Austin in Fast; both agents in Goal).

Submitted tasks are appended to `~/.duo/history` (override with `DUO_HISTORY_FILE`), so `↑`/`↓` recall survives restarting or resuming Duo.

The model picker (`Ctrl+M`) lists the catalog the *target's own driver* reports for the very installation Duo launched (`pi --list-models`, `agy models`, `opencode models`), so Austin and Tony can sit on different catalogs in the same session. Type to filter, move with `↑`/`↓` (or `PgUp`/`PgDn`, `Home`/`End`), switch the target between Austin and Tony with `Tab`, cycle that agent's thinking level with `Shift+Tab`, and apply the model with `Enter` (apply and close) or `Space` (apply and keep the picker open, so a model and a thinking level can be set in one visit). On `pi` the switch is live: Pi keeps the conversation and records both the model and the thinking level in the session transcript, so a restart or resume (`Ctrl+R`/`Ctrl+Y`) keeps the choice. `agy` and `opencode` take model and effort as startup flags, so Duo restarts that agent instead — the conversation survives, because the session id is persisted, but in-flight work is interrupted. `▶` marks the picker cursor and `●` the target's current model.

Agent output is rendered as lightweight markdown: headings, blockquotes, links and bold/italic/code spans are styled, and markdown tables are drawn with real, aligned borders. A table wider than the timeline is narrowed by wrapping the widest cells instead of truncating them, and a wrapped list keeps a hanging indent so continuation lines stay under the item text.

Mouse selection copies through terminal OSC 52 escape sequences (works seamlessly over SSH, tmux, and modern terminal emulators) alongside native desktop clipboard commands (`pbcopy` on macOS, `wl-copy` on Wayland, `xclip`/`xsel` on X11).

\* A newline is inserted only when the terminal reports the combination distinctly (`\x1b[13;2u` CSI-u or `\x1b[27;2;13~` modifyOtherKeys, which Duo enables). Terminals that send a bare `\r` for `Shift+Enter` will **submit** instead — use `Ctrl+Enter`, which arrives as `\n`, if `Shift+Enter` submits in your terminal. The same limit applies to `Ctrl+M`: it is reported as the `m` key with Ctrl (`\x1b[27;5;109~` or `\x1b[109;5u`) only on terminals that honor those modes, and on the others it is indistinguishable from `Enter` — use `Alt+M` there.

Help is a full alternate-screen view; scroll it with `↑`/`k`, `↓`/`j`, `PgUp`, `PgDn`, `Home`/`g`, `End`/`G`, and close it with `Esc` or `Ctrl+/`. It covers the keybindings, the slash commands and the model picker above, plus a **Command Line** section listing every `duo` subcommand and flag; run `duo --help` for the full usage, every environment variable and each command's own detail.

## Configuration

Everything has a working default; `duo` needs no configuration to run.

| Variable | Default | Meaning |
|---|---|---|
| `DUO_MODE` | `fast` | Session mode: `fast` or `goal`. A CLI `--mode`/`-m` wins; `duo resume` keeps the persisted mode. |
| `DUO_TEST_COMMAND` | (none) | Automated deterministic test command run before verification / delivery (CLI `--test-cmd` / `-t` wins). |
| `DUO_REPO` | current directory | Repository or subdirectory to launch against. A CLI path argument wins. |
| `DUO_SESSION` | timestamp + random hex | Session id, formatted `YYYYMMDD-HHMMSS-xxxxxxxx`. |
| `DUO_WORKTREE_ROOT` | `~/.duo/worktrees/<repo>-<hash>/<session>` | Where the Austin/Tony worktrees are created. |
| `DUO_BASE_REF` | `HEAD` | Ref the worktrees are branched from. |
| `DUO_PI_COMMAND` | `pi` | Command used to launch each Pi agent. |
| `DUO_DRIVER` | `pi` | Default agent driver: `pi`, `agy` or `opencode`. Per-agent flags and config win. |
| `DUO_LISTEN` | `127.0.0.1:0` | Bridge listen address (an OS-assigned port by default). |
| `DUO_HARNESS` | `true` | Enable the idle/stall watchdog that nudges coordination back to life. |
| `DUO_HARNESS_IDLE_SECONDS` | `15` | Quiet period before an agent counts as idle. |
| `DUO_HARNESS_STALL_SECONDS` | `300` | No progress for this long counts as a stall. |
| `DUO_HARNESS_COOLDOWN_SECONDS` | `30` | Minimum gap between harness nudges. |
| `DUO_HARNESS_RESUME_GRACE_SECONDS` | `45` | Extra grace after `duo resume`, so reconnecting is not mistaken for a stall. |
| `DUO_HISTORY_FILE` | `~/.duo/history` | Composer task history recalled with `↑`/`↓`. |

To run a non-default Pi command:

```bash
DUO_PI_COMMAND='pi --some-flag' duo
```

Duo hides its own `.duo/` directory through `.git/info/exclude`, so it never
modifies your `.gitignore` and never leaves your working tree dirty.

### Agent drivers

Duo drives whichever agent CLI you install. All three speak the same Duo
coordination protocol, so the modes, tools, evidence rules and delivery flow are
identical — only how Duo launches the agent and observes it differs.

| Driver | Select with | How Duo talks to it |
|---|---|---|
| `pi` (default) | `--agent pi` | A Pi bridge extension connects back over a local socket. Full streaming: tool activity, peer messages and model switching are live. |
| `agy` | `--agent agy` | Google Antigravity CLI. Duo observes the conversation and writes messages into the PTY. Token usage requires sqlite3 on PATH. |
| `opencode` | `--agent opencode` | An opencode plugin connects back over a local socket, the same way the Pi bridge does. |

There is no environment variable for a per-agent driver: `DUO_DRIVER` sets both,
and to split them use
`--austin-driver` / `--tony-driver` or `agents.austin.driver` / `agents.tony.driver`
in `.duo/config.json`.

Pick one per agent to mix them, for example `duo --austin-driver opencode --tony-driver pi` (or `-A opencode -T pi`).

```bash
duo --agent opencode                       # both agents on opencode
duo --austin-driver opencode --tony-driver pi
```

or persist it per repository in `.duo/config.json`:

```json
{
  "driver": "opencode"
}
```

Two notes specific to `opencode`:

- **Session identity is discovered, not chosen.** opencode assigns session ids
  server-side, so on a first run Duo lets opencode mint one, learns it from the
  bridge, and persists it with the session. Every later run — including
  `duo resume` after a crash — passes `--session <id>` so the agent keeps its
  conversation.
- **The model is yours to choose.** Duo does not inject a default model for
  opencode; it uses whatever your opencode config or catalog already resolves,
  and `--variant` carries a configured reasoning effort.

Codex is available as `--agent codex` or per agent with `--austin-driver codex`
and `--tony-driver codex`. Duo passes model and reasoning selections to the Codex
CLI, configures the `duo` MCP server for workflow tools, and runs Codex with
`--dangerously-bypass-approvals-and-sandbox` so it can work unattended without
folder trust or permission prompts. Codex's sandbox is disabled, so it can access
files outside its worktree; Git worktrees separate normal repository edits but
are not a security boundary. Use this mode only when the host is otherwise trusted
or isolated. Duo passes per-invocation trust overrides for the repository and its
worktree rather than editing Codex's persistent config. Codex may still gate
project-scoped skills. Base commands with conflicting sandbox or approval flags
are rejected.
Codex assigns its own session IDs; the driver learns them from new rollout metadata
for the exact worktree and resumes them with `codex resume <id>`. If multiple Codex
sessions start in the same worktree at once, the driver leaves the ID unset rather
than risk resuming the wrong conversation. Codex has no live activity or prompt
bridge in this integration; workflow tools are provided through MCP and terminal
input remains available.

Duo runs agents with tool calls auto-approved (`--auto` for opencode,
`--dangerously-skip-permissions` for agy) because they work unattended in their
own isolated worktree. `duo plugins` lists the built-in drivers plus any
`duo-plugin-<name>` executable (or its legacy `duo-driver-<name>` / `duo-<name>`
spellings) found in `~/.duo/plugins/` or on `PATH`.

The opencode bridge is installed by the install scripts to
`~/.config/opencode/plugin/duo/`. Restart any running opencode processes after
installing.

### MCP server (`duo mcp-server`)

An agent CLI that speaks Model Context Protocol can drive the same state machine
through `duo mcp-server`, which serves the six Duo tools above over JSON-RPC 2.0
on stdio and forwards them to the running session's bridge:

```bash
duo mcp-server --agent austin --session <session-id> --token <token> --port <port>
duo mcp-server config --agent tony            # print MCP client config JSON
```

Connection parameters default to `DUO_AGENT`, `DUO_SESSION`, `DUO_TOKEN`,
`DUO_HOST` and `DUO_PORT`, which Duo sets for the agents it launches, so a
client started inside a session needs no arguments.
`duo mcp-server config` prints a ready-to-paste `mcpServers` entry instead of serving.

### Configuration files

You can persist settings globally in `~/.duo/config.json` or per-repository in `.duo/config.json` (or `.duo.json`):

```json
{
  "mode": "fast",
  "language": "en",
  "testCommand": "go test ./...",
  "driver": "pi",
  "piCommand": "pi",
  "agents": {
    "austin": {
      "command": "pi",
      "driver": "pi",
      "model": "anthropic/claude-3-7-sonnet",
      "thinking": "high"
    },
    "tony": {
      "model": "openai/o3-mini",
      "thinking": "medium"
    }
  },
  "harness": {
    "enabled": true,
    "idleSeconds": 15,
    "stallSeconds": 300,
    "cooldownSeconds": 30
  }
}
```

`agents.<name>.command` overrides the launch command and `agents.<name>.driver`
the driver for one agent only; the top-level `driver` and `piCommand` set the
defaults both agents start from. Duo appends `--model` and the thinking flag
(`--variant` for opencode) unless the command already carries them.

Precedence: explicit CLI flags (`--mode`, `--language`, `--test-cmd`, `--agent`/`--austin-driver`/`--tony-driver`) > environment variables (`DUO_*`) > repository `.duo/config.json` > global `~/.duo/config.json` > built-in defaults. Set `language` to `zh` or `en`; `duo --language zh` (or `duo -l zh`) overrides the file. This changes Duo-authored messages to agents and the human, not the TUI labels.

Duo also **writes** the repository `.duo/config.json`: on every launch it records
each agent's active driver and model there, so a model chosen in the picker is
remembered on the next run. A model belonging to a previous driver is dropped
when the driver changes, since its id means nothing to the new CLI. Because
`.duo/` is excluded through `.git/info/exclude`, those writes never show up in
`git status`.

## Where Duo keeps things

```text
~/.duo/sessions/<repo-id>/<session-id>/
    state.json      mode, phase, verification/Plan, signatures, evidence, worktree paths/branches, opaque per-driver resume state
    events.jsonl    diagnostic journal of phase, signature, bridge and merge events,
                    plus the pane transcript restored into the TUI on resume
    duo.log         lifecycle output (session tokens are redacted before writing)
    lock            advisory flock holding the owner PID and hostname

~/.duo/worktrees/<repo>-<hash>/<session>/
    austin/         Austin's worktree — also the integration worktree
    tony/           Tony's worktree

~/.duo/config.json  global settings you can edit
~/.duo/history      composer task history (↑/↓ recall)
~/.duo/plugins/     optional `duo-plugin-<name>` executables
```

Session directories are created owner-only and describe your project's state; see [SECURITY.md](./SECURITY.md).

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
duo resume               # resume this repository's unfinished session
duo resume <id>          # resume one specific session
```

A plain `duo` always starts a **new** session and refuses to overwrite an unfinished one. `duo resume` picks the only unfinished session if there is exactly one. When several inactive unfinished sessions exist, an interactive terminal shows a numbered choice with each session's phase, scope and update time. In a non-interactive shell, Duo prints the candidates and asks for `duo resume <id>`.

Resume reloads the persisted snapshot, **proves it against Git**, and revokes any signature or verification that is no longer provable before starting a process. The mode is persisted with the session: `duo resume` keeps it, a legacy session with no recorded mode resumes as Goal, and `DUO_MODE` is ignored on resume (an explicit conflicting `--mode` is an error). Reconciled state is written back before agents start, so a crash during startup cannot resurrect a revoked approval or replay a merge. Each reconnected agent receives one mode- and phase-aware wake-up message; reconnects do not duplicate it. The most recent 200 entries of each pane are replayed into the TUI, so a resumed session starts with its previous conversation visible.

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
```

`duo apply` never starts agents and reuses the same safety rules — it will not force a blocked delivery.

## Manage and clean sessions

Duo persists session snapshots under `~/.duo/sessions/` and isolated Git worktrees under `~/.duo/worktrees/`. You can list sessions and reclaim disk space at any time:

```bash
duo sessions                 # list sessions for this repository
duo sessions --all           # list sessions across all repositories
duo clean                    # clean completed (DONE) sessions for this repository
duo clean <session-id>       # clean one specific session
duo clean --all             # accepted, but plain duo clean already covers every completed session
duo clean --all-repos       # clean completed sessions across all repositories
duo clean --force           # also clean unfinished sessions (skips active running sessions)
duo clean --dry-run         # preview what would be cleaned without modifying disk
duo logs                    # list the Markdown transcripts written to .duo/logs
duo logs <repository>       # list another repository's transcripts
```

`duo clean` cleans every completed (DONE) session of the repository by default, so `--all` currently changes nothing; `--force` is what adds unfinished sessions. `duo sessions` accepts `-a` for `--all`, `duo clean` additionally accepts `-f` for `--force` and `-n` for `--dry-run`, and `duo apply <session-id>` selects a specific session. Every command prints its own usage with `--help` (`duo clean --help`), and `duo <repository> --help` prints the root usage from inside a repository.

Cleaning a session removes its Git worktrees (`git worktree remove --force`), prunes the worktree registry, deletes temporary branches (`duo/<session>/*`), and removes the session snapshot directory. Any session currently locked by an active Duo process is safely skipped.


The remaining commands do not touch sessions: `duo plugins` lists the built-in drivers plus every discovered external plugin, `duo plugins test [<path…>] [--all] [--verbose]` checks one or every driver against the [Driver Plugin Protocol](./docs/driver-plugin-protocol.md), [`duo mcp-server`](#mcp-server-duo-mcp-server) serves the Duo tools over MCP, and `duo version` (`--version`) and `duo help` (`-h`, `--help`) print the version and full usage.

## Limits that affect use

Duo supports two fixed roles, Austin and Tony. Fast can escalate to Goal but cannot return to Fast. A session cannot recover agent reasoning, and Duo Core does not restart itself after a crash; run `duo resume`. Session data is local to the machine and repository path. The shell scripts target macOS/Linux. Delivery safety and local TCP trust boundaries are described in [architecture](./docs/architecture.md) and [SECURITY.md](./SECURITY.md).

## Development

`make check` runs Go tests, vet and command builds. If you change the Pi bridge,
also run `cd pi-extension && bun test`. Pushing a `v*` tag runs release validation
and publishes macOS/Linux archives for amd64/arm64.

## Documentation

- [Architecture and invariants](./docs/architecture.md)
- [Driver plugin protocol](./docs/driver-plugin-protocol.md) and [development guide](./docs/plugin-development-guide.md)
- [Bridge protocol](./docs/duo-bridge-protocol.md)
- [Security boundaries](./SECURITY.md)
- [Release history](./CHANGELOG.md)

## License

MIT — see [LICENSE](./LICENSE).
