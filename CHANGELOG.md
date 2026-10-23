# Changelog

All notable project milestones are documented here.

## Unreleased

### Human attention

- When a Duo session stops until a human acts, the terminal says so: a merge
  conflict, a result that could not be applied to the repository, a Fast
  verification nobody answers, and Austin's final report each ring the terminal
  (BEL) and raise a desktop notification through OSC 777, so the signal reaches
  someone who is in another tab. The text follows `/language`, the in-dashboard
  toast is unchanged, and `"notify": false` in the configuration silences it.
- The four situations are an event with a reason code, not prose, so a frontend
  localises the wording and a notification fits one line. The workflow state stays
  where it was: the event is a projection of it.

### Diagnosis

- `duo doctor [repository]` answers "why will not it start" in one command: git
  present and new enough, the repository and its dirty state, the branch a result
  delivers to, the worktree root, the configuration files, each configured Driver
  Plugin with its agent CLI and protocol contract, the bridge each driver needs
  installed, and the observer. Every line carries a stable code and a fix, a warn
  is something a session runs through, and a fail exits non-zero.
- A session that fails to start now names `duo doctor` on the way out, so a first
  run on a machine missing a driver CLI is a self-service fix instead of a bug
  report.

### Driver capabilities

- A Driver Plugin declares the Duo coordination tools its launch makes reachable
  to its agent in `capabilities.tools`. Core cuts that agent's prompts to the
  declared set: a clause instructing it to call a tool the driver did not declare
  is removed before delivery, in both languages, so an agent no longer spends a
  turn looking for a tool it cannot reach.
- The declaration is checked, not trusted. `duo plugins test` fails a plugin that
  names a tool Duo does not register, or that declares tools while
  `capabilities.mcp` is false, and prints the declared set when it passes.
- A driver that declares nothing keeps every tool in its prompts: the field is
  additive, so a plugin written before it exists loses wording it can act on.

### Mode switching

- `Ctrl+S` / `Alt+S`, `/mode` without an argument, and a click on the header mode
  badge open one FAST/GOAL/STEP panel with the current mode marked, a one-line
  purpose per mode, and `↑`/`↓` plus `Enter` to switch; `Esc` cancels.
- A switch that would reset the round asks first and names exactly what is
  discarded: the shared plan version, the Step queue and its landed count, the
  signatures, and Fast verification evidence. Keeping the current mode is the
  default and `Esc` sends nothing; Fast → Goal escalation and a DONE round stay
  immediate.
- `duo --help` now names the Ctrl+S panel, the reset confirmation and F3 /plan,
  so the first surface a human reads no longer describes mode switching as a
  slash command only.

### Plan & steps

- `F3` or `/plan` opens a read-only PLAN & STEPS overlay with the shared plan as
  Markdown, the mode, phase and plan version, and the full Step queue with
  landed, current and pending markers. It scrolls like help and details and
  closes with `Esc`.
- The overlay refreshes when a step lands or changes review state without a new
  plan version, and clamps its viewport when the content gets shorter instead of
  going blank.
- A round without a plan (Fast, or DONE after the plan was cleared) no longer
  advertises `plan v0` beside “no shared plan”.

### MCP bridge

- `duo mcp-server` now advertises all eight Duo coordination tools, adding
  `duo_set_steps` and `duo_restart_duo`. An MCP-only driver (codex declares MCP
  with no bridge) can publish the STEP queue in PLAN and ask for the guarded
  between-steps restart, neither of which it could reach before.
- Both tools send the same wire fields the Pi bridge sends: the raw Markdown
  queue with an `append` marker, and a restart reason plus its `install` flag. A
  blank queue or reason is refused at the bridge, and a refusal from Duo Core — a
  dirty worktree, for example — comes back to the agent as a tool error.

### Chinese interface

- Status feedback now goes through the translation table: send, clear, copy,
  sidebar and timestamp toggles, driver and model changes, image paste, restart,
  view preferences and the `/status` fallback all follow `/language zh`.
- The mode panel, the destructive-switch confirmation and the plan overlay are
  translated as well; protocol names, paths, ids and core errors stay verbatim.

### Fixes

- The header reserves the mode badge's actual width, so `PAUSED` no longer
  overwrites the end of the branch name.
- The interactive help no longer lists `Ctrl+O` twice.

## v0.13.2 — 2026-10-22

### Product site

- xduo.dev ships: English and Simplified Chinese pages share one design system,
  brand assets, sitemap and a strict CSP applied by `_headers` on Cloudflare
  Workers static assets.
- The local preview command resolves `site/public` from the repository root, so
  the documented command no longer answers 404 when run inside `site/`.

### On-demand observer report

- `Ctrl+E` and `/reports` ask the observer for one fresh report built from the
  recent messages plus a live snapshot of both agents; `R` asks again.
- Reports render only in the report panel, in three sections (just finished,
  doing now, next) and the last three are kept, so they no longer bury the
  timeline. The panel clamps its viewport when a shorter report arrives, and
  explains a disabled observer, a missing key or an empty history.
- `observer.maxEvents` (default 40) replaces the removed `debounceSeconds`; the
  observer still has no timer and writes nothing to the timeline.

## v0.13.1 — 2026-10-22

- Updated the release version and corrected the interactive help so its commands,
  keyboard shortcuts and workflow guidance match the current gotui behavior.

## v0.13.0 — 2026-10-22

### Cooperative pause and driver switching

- `/pause` holds new work (prompts, peer messages and watchdog nudges) until
  Austin and Tony finish their current turn, then reports `PAUSED`; `/unpause`
  cancels an incomplete request and `/continue` resumes a fully paused session
  with one state-rich prompt.
- Before the first task, while fully paused, or after `DONE`, `/driver` switches
  an agent's driver without resuming it. The replacement process starts idle
  and, after `/continue`, reads the repository `.duo` transcripts to recover
  context that the previous driver's native session cannot carry across.
- Pause intent is persisted, so a resumed session stays paused until the human
  explicitly continues, and agent launches while paused never receive an
  automatic resume wake.

### Self-upgrade reliability

- `duo step-loop` resolves the repository like `duo sessions` and `duo apply`
  (positional path, `DUO_REPO`, then the working directory) once a real upgrade
  request exists, so a request written from a subdirectory or an explicit path is
  found, while `--version` and `--help` still pass straight through. Resumes keep
  the original launch directory, so a subdirectory's language and test command
  survive the restart.
- `duo_restart_duo` is refused while either agent worktree has uncommitted
  changes, naming the worktree and path; a restart can no longer discard work
  that was never committed.
- `duo --version` reports the build commit from `debug.ReadBuildInfo()` and marks
  a binary built from a dirty checkout. `.duo/upgrade.json` records
  `builtCommit`/`builtModified`, and `duo step-loop` prints the requesting build.
- `duo step-loop` refuses to install from a checkout with uncommitted changes and
  prints the install-source HEAD next to the request's build identity.
- The Chinese README now documents the gotui mode-switch **Resume work?** prompt
  and **Not now** behavior.

### gotui becomes the terminal frontend

- The gotui dashboard is now the default and only bundled terminal frontend, so
  `duo` opens it directly. The old hand-written TUI is preserved on the
  `legacy-tui` Git branch and is no longer built or installed from `main`; the
  UI-selection flag has been removed.
- `/language zh|en` now translates the gotui chrome live, not just Duo-authored
  messages, and the choice survives a restart through `.duo/config.json`.
- The `/mode` command column is sized from the longest command, so
  `/mode [fast|goal|step]` is visible in full.
- A model chosen in `/model` is written to `.duo/config.json` so the next launch
  in that repository starts with the same model. Thinking levels selected in the
  same picker are persisted with the agent's driver and model as well.
- `scripts/install.sh` now builds the gotui-based `duo` with the rest of the
  commands, so a source install and the release archives ship the same frontend.

### Runtime mode switching and per-step policies

- Fast, Goal and Step can now be switched at any time with `/mode fast|goal|step`
  or `duo_escalate`, without restarting either agent. A switch resets the current
  plan, queue, phase and signatures; switching while `DONE` changes the mode of
  the next task without reopening the finished round. Fast → Goal keeps the
  historical escalation-seeded plan.
- Step is now a meta-workflow: each queue item is tagged `[fast]` or `[goal]`.
  A Fast item has Austin implement and Tony verify; a Goal item runs the full
  dual-agent EXECUTE/REVIEW/INTEGRATE chain. Untagged items default to Fast, the
  queue is always dual-approved in PLAN, and the final item always needs both
  signatures.
- Both adapters register every Duo tool in every mode and read the live mode
  from Duo Core, so a switch changes the next prompt without a process restart.
  The harness acknowledges every submitted task as soon as required sends
  succeed, and the opencode context hook removes the interactive `question` tool
  so an unattended Duo session cannot stall on a human prompt.
- Step phase and resume prompts now explain the current item's policy, and the
  gotui help, slash palette, README and captures reflect the three switchable modes.

### Session selection and xduo inspection

- `duo resume` now offers a numbered choice when several inactive sessions exist
  and a terminal is available. The list includes completed sessions and clearly
  labels their status; resuming a completed session waits for a follow-up task
  before reopening its workflow.
- xduo can search its timeline and navigate matches from the keyboard.
- An agent's tracked changes are available as a bounded, scrollable diff
  preview. The host caches the preview asynchronously and marks truncated text.
- Agent cards show whether their driver can change models without restarting.

### xduo desktop usability

- The task composer wraps long lines and supports keyboard selection, including
  Shift with the arrow, Home and End keys.
- The timeline renders lightweight Markdown, supports message selection and
  navigation, and copies the selected message's original text.
- The session overview shows Git diffstat, readiness and verification evidence.
  Work previews show elapsed time and keep the current step visible in long lists.
- Switching a model on a driver that requires a process restart asks for explicit
  confirmation and warns that the agent's current turn will be interrupted.
- Keyboard help is searchable, and a slash-command palette offers common GUI
  actions while keeping ordinary task text available to send.
- Desktop launchers offer to resume an unfinished session in the chosen
  repository. A window smoke script and platform checklist cover decorations.
- Automatic Git diffstat refresh disables repository-configured fsmonitor commands.

## v0.12.0 — 2026-10-16

### xduo desktop release

The graphical frontend now ships in the Linux and macOS release archives for
amd64 and arm64. Linux installs a desktop launcher and icon; macOS includes an
`xduo.app` bundle with Duo and its agent helpers. The launcher asks which Git
repository to open, so it also works when started from a desktop menu or Finder.
The window uses native decorations and follows the system light or dark theme.

- The task composer supports IME composition, Unicode cursor movement, mouse
  selection, multiline tasks and persistent history shared with the terminal UI.
- The session overview shows each agent's worktree branch, HEAD and path. The
  timeline can show timestamps and copy Austin's latest message to the human.
  Keyboard help and a second confirmation for closing unfinished sessions make
  the window safer to use during a long task.
- The session host exposes bounded structured agent activity and the real model
  catalogs reported by each agent's driver. xduo can show work in progress and
  send model and thinking changes through the existing human session commands.
  Model listing now inherits the agent's PATH, so user-installed CLIs remain
  available when the catalog is queried.
- CI builds and tests the Rust renderer on Linux and macOS. The release workflow
  builds native xduo packages for both architectures on each platform.

Direct attachment to an agent's native terminal remains a future feature. It
needs a real terminal transport and emulator to preserve interactive agent
behavior; agent sessions remain accessible through the terminal interface.

## v0.11.0 — 2026-10-16

### GPUI graphical frontend — `duo gui` (MVP)

A second frontend, and the first one that is not a terminal. `duo gui` starts the
same session `duo` does and hands it to a native window built with
[GPUI](https://gpui.rs), Zed's Rust UI framework. The window renders the session —
mode, phase, agent state, the shared timeline — and sends back the verbs a human
performs.

- **Core, the terminal interface and the graphical one stay isolated.** Duo Core is
  unchanged: `internal/session` is still the only seam a frontend drives, and it
  knows nothing about a terminal or a window. The graphical frontend is two pieces
  that never import each other's world — `internal/guiipc` projects a session onto a
  small NDJSON protocol, and the `gpui/` crate (`duo-gpui`) draws it. A parser-level
  test pins that boundary the same way `internal/tui` already pins its own.
- **The GUI protocol document is the frozen contract.** One
  direction carries a full `View` plus timeline `event`s; the other carries five
  human commands. It deliberately has no gate verb: signing a phase is authorized
  by an agent's live bridge connection, and a graphical frontend is the human, not
  an agent.
- **The MVP is narrower than the TUI on purpose**: submit a task, escalate to Goal,
  restart an agent, switch language. No agent PTY stream, model picker, plan editor
  or mouse, and no way to render a session's restart request: a session that asks to
  continue on a new binary waits for the human to close the window, and `duo gui`
  then records the upgrade request and exits with the same code the terminal
  interface uses. The smallest window that is genuinely usable is the right first
  one.
- **Build it with `make gpui`** (`cargo build --release` in `gpui/`). The crate pins
  `libc = "=0.2.179"`: gpui's transitive `xattr 0.2.3` reads `libc::ENOATTR`, which
  libc stopped defining in 0.2.180, and without the pin the crate does not build on a
  current toolchain. Its build tree is `gpui/.target` rather than cargo's default,
  because `go test ./...` walks the repository and fails if it touches a Cargo
  target directory while cargo is rewriting it. `scripts/gui-smoke.sh` is where the
  graphical frontend is checked end to end: the fake frontend over the real host and
  the renderer's protocol tests run headless, and the window feed runs when a display
  is available. Rust is not part of `make check`, because CI carries no Rust
  toolchain.

### Step mode — `duo --mode step`

For one complex task whose increments are each worth an independent review and each
worth landing. The Goal phase chain runs **once per step** of a durable queue, and
every step is landed on your branch as it is verified, with no human task entry
between steps. See [docs/architecture.md](docs/architecture.md) for the boundaries
and [docs/self-upgrade.md](docs/self-upgrade.md) for the restart loop this enables.

```text
PLAN → (per step: EXECUTE → REVIEW → INTEGRATE → land the step) → DONE
```

- **The queue is the contract.** Agents co-design an ordered queue with
  `duo_set_steps` (a markdown checkbox list) alongside the prose design in
  `duo_set_plan`, and both approve it before EXECUTE. Duo parses the list once into
  a structured array that is the single source of truth, renders it back wherever a
  prompt needs one, and owns the cursor: agents never tick a box, a landed step is
  never removed or reordered, and a queue update creates a new version and resets
  both signatures.
- **Each step is gated by one HEAD-bound verification.** Austin implements and
  commits exactly the current step and puts that HEAD up for review; Tony reports
  `passed` with `duo_set_verification`, which lands the step and advances the queue,
  or `issue_found` with a concrete note, which returns the step to EXECUTE. A new
  commit invalidates a verdict, as in Fast mode.
- **Dual sign-off is kept where it buys real agreement**: the queue in PLAN, and the
  final step. Signing every step twice would add cost without adding an independent
  check — Austin signing his own INTEGRATE is self-approval.
- **The verifier always reviews what you received.** Because each landed step goes to
  the branch you launched Duo from, Duo fast-forwards Tony's worktree to the landed
  head after every step. Without it the verifier would review and build a tree that
  never reached you, and each step's merge would re-conflict against the original
  session base. The target is the landed step's own head, never Austin's live HEAD,
  so in-flight work for the next step is never presented as landed.
- **A restart is a step action, not a special case.** The queue is durable and
  re-injected on resume, so a session can be picked up by a different Duo process —
  including a newly installed binary — and continue at the correct step.
- `duo_set_steps` is available in Step mode only; `duo_set_plan` and
  `duo_set_verification` are enabled for Step alongside their existing modes;
  `duo_escalate` stays Fast-only, because Step is chosen at start rather than
  escalated into. `SchemaVersion` stays 2: the queue is additive optional state, so a
  new binary can still read a session an older one wrote.
- **A Step session now tells its agents the real mode.** `modeEnv` folded every mode
  but Fast into Goal, so a Step session handed its adapters `DUO_MODE=goal` while
  its driver plugins saw `DUO_MODE=step`. An adapter registers its tool set from
  that value, and the Goal set has no `duo_set_steps`, so a Step session could not
  publish the queue it requires and stayed in PLAN. Every mode now reaches both the
  adapter and the driver unchanged.

  A session already running on the old binary cannot be repaired in place: its
  agents are the processes that already read the wrong value. It can still be
  unblocked, because publishing the queue is an agent-side verb — the agent sends
  `set_steps` to the bridge itself, with its own identity and token, over the same
  connection Duo's own MCP client uses (`clientType: "mcp"`, which carries the
  identity without evicting the running adapter). That is a workaround for one
  stuck session rather than a second interface: installing this fix and resuming is
  the path that leaves nothing behind, and the agents of a resumed session are told
  the real mode from the start.

## v0.10.2 — 2026-10-15

One change: the language Duo speaks can now be set while a session runs, from
the composer, and it survives the session.

### `/language`

- **`/language zh` and `/language en` set the language of a running session.**
  Duo has taken its language from `--language` and the config file since long
  ago, which meant changing it required leaving the session, editing the file
  and relaunching. The command is validated (`zh` or `en`, nothing else), and
  `/language` with no argument reports the language actually in force.
- **The choice is written to the repository's `.duo/config.json`.** The project
  layer overrides the global one, so a resumed session, and every session
  started in the repository afterwards, use the language that was chosen —
  the same layering the model picker already persists to. The write is atomic
  and preserves every other key in the file; a failed write is reported, not
  swallowed.
- **What switches when.** Coordinator prompts to the agents and the human
  switch immediately. An agent process keeps the `DUO_LANGUAGE` it was
  launched with until it restarts, and the harness keeps the language of its
  startup; both pick the new one up on the agent's next restart.

## v0.10.1 — 2026-10-15

Two changes to the report Austin writes when a session finishes — what it is
asked for and how the interface marks it — and one to what happens when Duo is
launched and then left without a task.

### A session that never started

- **Leaving Duo without giving it a task now leaves nothing behind.** Launching
  Duo and walking away had already created two worktrees, two branches, a session
  record and three transcript files in the user's repository, and left a resumable
  session in `duo sessions` offering a round that contained nothing. Ending such a
  session now removes all of it, so the repository and the disk are exactly as they
  were before Duo was launched, and the session does not appear in `duo sessions` at
  all — there is nothing to resume, and `duo resume` must not be pointed at it. The
  exit message says so instead of naming a preserved session.
- **Git, not the session's own record, decides whether it is safe.** Two conditions
  must hold, and the second is the one that makes this safe. The session must not
  have started, which is the workflow's own record that no task was submitted and no
  agent ever began a turn — a session the human worked in through an attached
  terminal has started too, because the agent's first turn is what flips the flag.
  Git must also report no work: no uncommitted changes and no commit on top of the
  base commit in either worktree. That second check exists because the first is only
  as good as the drivers' reporting, and a driver that never announces a turn would
  otherwise make a session with real work in it look untouched. Asking Git costs two
  commands per worktree and makes the discard safe for a driver nobody has audited.
  Anything unreadable is treated as carrying work, and a failure never fails the
  teardown: the session is logged and kept, and `duo clean` remains the way to
  remove it by hand.
- **`duo clean` and the session teardown now share one removal.** The worktree,
  branch and directory removal moved into `internal/workspace`, because it is an
  irreversible operation and two implementations of one is one more than a project
  should have.

### The final report

- **Austin is asked three questions instead of for a narration.** The request
  asked for "the process" and "the results", which a report can satisfy while
  telling the human nothing they could not read off the transcript they had
  already watched. It now asks, in order: what the repository now has that it did
  not have before; where Austin and Tony agreed and where they did not, with each
  disagreement attributed and settled by the evidence; and whether having two
  agents improved the work at all, to be answered yes or no in the first sentence
  with the option of saying it produced nothing. A disagreement Austin overruled
  Tony on has to be stated plainly with its reason, and a correction accepted from
  Tony has to be named. Both language versions carry the same three questions, and
  a test holds them to it.
- **The report is marked so it cannot be mistaken for an ordinary message.** Tony
  writes his own account of a finished round without being asked, and it is a good
  account; the two were easy to tell apart only by reading them. Austin now opens
  the report with a title box drawn from `=` and `|`, and the interface paints the
  star on the header, the rules Austin drew and the body under them in the human's
  own green rather than Austin's cyan, because what makes this one different is
  that Duo asked for it. The rule is also the marker: a line of equals signs is
  what the interface looks for, it is the one line of the box that carries no text
  and so reads the same in every language, and the interface's markdown renderer
  leaves it alone. A report that arrives without its rule is still readable — it is
  simply not marked, which is the failure mode worth having, because a
  misrecognised report would assert something untrue.

## v0.10.0 — 2026-10-15

A minor release because three of these changes what Duo *does* rather than only
what it reports: a dead agent is now always visible, an agent that has wedged is
now caught even while it claims to be working, and quitting a live session now
asks twice.

### Agents

- **An agent that stops being watched no longer disappears silently.** The tail of
  a failed process was captured only for a non-zero exit, and the exit was reported
  only when that tail was non-empty. A CLI that gives up during bootstrap often
  exits 0 right after printing its own diagnosis, so a session could sit in `VERIFY`
  waiting on an agent that no longer existed, with nothing but `exited [Restart]`
  in a preview pane to explain it. `ExitEvent` and `LifecycleEvent` now carry
  `Requested`, so the observer can tell a shutdown Duo asked for from a death, and
  every exit Duo did not ask for reaches the timeline — with the driver's own output
  when there is any, and an explicit statement when there is not.
- **A private escape sequence no longer reaches the operator's error.** The ANSI
  stripper matched CSI sequences whose parameters were digits, `;` and `?`, which
  is not the whole range ECMA-48 reserves for parameter bytes. A sequence such as
  `ESC [ > 4 ; 0 m`, written by any agent that enables the kitty keyboard
  protocol, reached the reported reason as the literal text `[>4;0m`. The parameter
  class is now the full `0x30`–`0x3F` range.
- **The watchdog reaches an agent whose bridge has not connected yet.** Harness
  messages went to the socket server, and the server's reachability check asks only
  whether a bridge client is registered. Codex starts its Duo MCP bridge the first
  time it calls a Duo tool, so before that moment it held no client at all and every
  nudge and every copilot escalation was dropped on the floor: a session whose only
  agent could not be reached simply stopped progressing, with a nudge count that
  never moved. The monitor is now handed the coordinator, which already owns the
  decision of whether a message goes over a bridge or into the agent's terminal, and
  whose reachability check counts a running driver. Because the coordinator records
  injected prompts, a harness message also stops being mistaken for something the
  agent said itself.

### Watchdog

- **A Fast stall is caught even when the agent still calls itself busy.** The Fast
  path returned as soon as an agent reported activity, which is the one case the
  stall threshold exists for: an agent silent past the threshold while reporting
  itself busy is stuck, not slow. Goal mode already made this distinction; Fast did
  not, so a wedged driver could hold a session open indefinitely without Duo saying
  a word. A hard stall now outranks what the agent says about itself, for the
  verifier as well as the driver. The reason text that says an agent has been silent
  for an unusually long time was unreachable — it required being quiet *and* inside
  the idle threshold at once — and is now reachable in both languages.

### Interface

- **Quitting a session that still has work in it asks first.** `Ctrl+Q` closed an
  unfinished session outright, and the only warning was a line in the exit message
  the operator had to read carefully: the work was safe in a worktree they were not
  looking at, which is exactly the state where pressing `Ctrl+Q` again — once to
  read something, once to leave — is most likely. The first `Ctrl+Q` or `Ctrl+D`
  now warns and arms a three-second window; a second one inside it quits, and an
  unanswered warning lapses on its own. A session nobody has started and a session
  that has already delivered both quit on the first press, so the guard cannot
  become the reason a finished round cannot be closed.

## v0.9.1 — 2026-10-12

### Driver plugins

- **`activity` and `mcp` are no longer capabilities.** Both sat in `capabilities` while Core read neither, so the protocol's own rule — every capability must change something Core does, or it is "a lie that will be believed" — was being broken by four fields, and a plugin author following the development guide was told Duo acted on them. Core handles activity messages identically for every driver and cannot decide anything from them, and an agent reaches Duo's coordination tools through the launch command `prepare` returns, not through the manifest. Both moved to the manifest itself as descriptive metadata, where declaring one costs nothing and promises nothing. The wire change is safe in both directions: Go ignores unknown JSON keys, so a plugin still sending `capabilities.activity` describes itself identically, and a plugin reading the new location ignores an old key rather than failing.
- **`contextUsage` and `tokenRate` are now capabilities Duo keeps.** Core showed whatever figures arrived on an activity message regardless of what the driver declared, so a driver reporting a context window it could not actually maintain — or a rate computed from something other than its own output — put a number on screen that the operator had no way to distrust. Each figure is now gated on the capability that promises it, which is what the field always claimed to mean. A driver that omits both is unaffected: it reported nothing before and reports nothing now.
- **The capability rule is enforced rather than stated.** `internal/driver/decoupling_test.go` reflects over `Capabilities` and fails when a field is declared that no Core file reads, so the next field cannot be added without Core either branching on it or the field becoming descriptive metadata in the same review. A companion guard reads `docs/` and fails when a document names `capabilities.<key>` for a key that is no longer a capability — the first move left `docs/duo-bridge-protocol.md` still telling adapter authors to set `capabilities.mcp`, which only a verifier reading the prose caught.
- **Adding a driver no longer means editing the install scripts or the release workflow.** `install.sh`, `install-release.sh` and `release.yml` each named every driver binary to build or install, so a fourth driver had to be written into three files that had no other reason to know any driver existed. All three derive the list from `go list ./cmd/...`, producing exactly the binaries that shipped before — verified name-for-name — and `install-release.sh` installs whatever the release archive contains. A new guard keeps a driver binary out of those files.
- **Codex startup, resume and reporting are more reliable.** The Codex driver now starts without a background daemon, reads its model catalog correctly, reports activity and assistant replies to Duo, wakes once with the saved conversation identity after resume, and preserves project command/profile settings. PTY I/O no longer holds the session lock while blocked, so status and Stop remain responsive.
- **Goal mode gives both agents the task and supports attaching to native agent sessions.** Austin and Tony receive the work directly, and the interface can attach to each driver's own terminal session; the Help and keybinding text describes this consistently.

### Interface

- **`Ctrl+D` quits Duo exactly like `Ctrl+Q`.** Both keys issue the same quit from any view, including Help and the model picker, and preserve the session for `duo --resume`; `Ctrl+D` also works on terminals that swallow `Ctrl+Q` as XON software flow control. The byte `0x04` and the CSI-u and `modifyOtherKeys` encodings are all recognised, and Help, the footer, the `/quit` palette entry and both READMEs list the alias alongside `Ctrl+Q`.
- **Austin's latest message to the human can be copied with `Ctrl+X` or `/copy`.** A long report in the timeline could only be copied by dragging over it, which re-wraps and drops anything already scrolled out of view. The new shortcut reads the entry Austin last addressed to the human straight from the timeline — peer traffic to Tony is skipped — and puts the original, unwrapped text on the clipboard through the same OSC 52 plus native-tool path as a selection. Success and failure are both reported in the status row, so the copy is never silent.
- **Austin summarises the session for the human when it reaches `DONE`.** A finished session ended with Duo's own `DONE` broadcast — delivery facts, branch, SHA — which said nothing about how the work actually went, so the account of a session had to be reconstructed from the transcript. At `DONE`, in Fast and Goal alike, Duo now asks Austin to report the round to the human: how the work proceeded, the decisions and rework along the way, what the repository now has that it did not have before, and anything the human must know before relying on it. Austin answers in a normal message rather than `duo_send`, so the report reaches the timeline as `Austin → Human` — bold, like the human's own mail — instead of as agent traffic or a second copy of the delivery notice. The request is made once per finished round: delivery is retried by design, and a duplicate final signature must not produce two reports of the same round. A follow-up task reopens the session and the new round reports on its own delivery. A delivery blocked by the user's repository is not `DONE` and gets no summary, because the round is not finished.
- **The Help panel and the keybinding reference stop saying "native Pi".** Attaching to an agent with `Ctrl+A` / `Ctrl+T` has worked for every driver since agy and opencode were added, but the wording still named Pi, so it read as though only a pi agent could be attached. The rows now describe attaching to the agent's own session, and the Help section says explicitly that this bypasses Duo — no task is routed through it and the phase machine is untouched — notes that in Fast mode a commit made during an attach is still delivered once Austin requests verification of a new HEAD, and says that the agent's own commands and key bindings apply, whichever CLI it runs. Both READMEs were corrected the same way.

## v0.9.0 — 2026-10-02

A minor release because the surface changed rather than only the behaviour: Duo now writes a readable transcript of every session into the repository, the work preview reports token usage, and `duo logs` lists what it wrote. Alongside those, nine correctness fixes, including two that could lose an agent's conversation identity and one that could make `git revert` in the delivered history unsafe.

### Work preview

- **The work preview reports token usage for every driver.** Each agent's header can show `ctx 45.0k/200.0k` and a live `42 tok/s`. Context usage comes from the driver where it is available — pi's `ctx.getContextUsage()`, and opencode's real `info.tokens` counts on the assistant message. The opencode context *limit* is read from the provider catalog rather than guessed. Generation speed is estimated from streamed output deltas and is shown only while the provider is active, so a stale rate cannot linger. The rate is carried on the existing activity message, so no driver contract changed, and a driver that reports nothing leaves the header as it was.
- **`agy` token usage is read from agy's own conversation database.** agy exposes no usage API, so Duo reads the `gen_metadata` record with the `sqlite3` CLI and decodes the token counts from it. Verified against 23 real agy databases. The `sqlite3` binary is an optional dependency; without it the preview shows a `no sqlite3` marker and Duo says so once, rather than silently showing nothing, while the rest of agy — including its session identity, which does not use sqlite3 — keeps working.
- **opencode's provider span no longer closes on the first text part.** The rate is only rendered while the provider is active, so closing the span as soon as text arrived discarded the very number the span exists to show. It now stays open across streamed text and closes on `step-finish`, or on an idle with no step so a silent agent still stops looking busy.

### Corrected
- **A configuration test no longer depends on the environment it runs in.** Duo exports `DUO_DRIVER`, `DUO_MODE` and the rest of its variables into every agent's environment, and `loadConfig` reads them, so the same test resolved a different configuration depending on whether it ran from a shell or from inside a session. One test asserted that an agent's model was non-empty, which held only because a driver happened to have a default in that environment; an unconfigured opencode deliberately resolves to no model, so the assertion was claiming something about driver defaults rather than about the rule it meant to check. It now asserts on the exact model persisted for the driver still in use, and clears `DUO_*` for its duration. Both directions of the rule are now covered: a model recorded for the driver being switched away from is dropped, and one recorded for the driver still in use is carried through.


- **The v0.7.0 changelog no longer names two environment variables that were never read.** `DUO_AUSTIN_DRIVER` and `DUO_TONY_DRIVER` were documented as a way to pick a per-agent driver, and no code has ever consulted them. The entry now lists the flags and `.duo/config.json` that do work, and says which variable sets both agents.

### Diagnosing a driver that will not start

- **A failed agent now says why.** A CLI driver launched into a PTY merges its stderr into that stream, so a driver that refuses to run says so exactly once, there: `Error: Unknown option: --auto`, `Error: unknown model`. Nothing carried it. The session log recorded only `Tony exited (state=failed): exit status 1`, and the interface said `Tony native Pi session is not running`, which named a symptom rather than the cause. A switch to an unsupported driver took twenty minutes to diagnose with no other evidence available. The tail of what a failed process wrote is now recorded in `duo.log` and shown in the timeline, escape sequences and blank lines removed, truncated to the last few lines and marked when it is truncated so a short tail is not read as the whole story.

### Session identity

- **`agy` conversations are resumed instead of restarted.** agy assigns conversation ids itself and refuses one it has not seen, answering `Conversation <id> not found, ignoring --conversation flag` and opening a different conversation. Duo was sending an id it had invented: the same per-agent UUID map that pi needs, because pi adopts an id it has never seen, was also passed to agy. agy rejected it on every launch, the watcher found no real id in the log to replace it, and the rejected value was what Duo persisted and replayed. An agy agent therefore never resumed its conversation, on any restart. The flag now carries only an id agy reported; the first launch passes none, the watcher learns the conversation agy created, and later launches replay it. Verified against a real agy pair: the first launch raises no warning, the learned id is what gets persisted, and the resumed run does not reject it or open a new conversation.

### Driver selection

- **A model persisted for one driver is no longer handed to another.** `.duo/config.json` records which driver each agent's model was chosen for. Changing a driver on the command line (`--tony-driver pi`) or in config left the old model in place, and only the agy and opencode cases were handled, so switching an agent *to* pi kept a model id pi has never heard of. pi rejects an unknown model and exits before it connects, so the agent never appeared at all.
- **The launch command no longer takes the driver from a flag value.** The agent session decided the driver with `strings.Contains` over the whole command line, so `pi --model opencode/space-bunny-free` was built as an *opencode* command and picked up opencode's `--auto`, which pi rejects with `Error: Unknown option: --auto`. The v0.8.0 fix made the model catalog classify a command by its executable, but the agent session kept the substring check; both now use the one implementation. The model that reaches the command line is the validated one rather than the raw persisted value, so a model that was corrected above cannot reappear in the flag.

### Redundant commits

Goal mode delivers a merge of the two agent branches, so a commit that exists only because an agent cherry-picked its peer's work is redundant once merged. The copy is not harmless: reverting either commit removes the change while the other still claims to add it, and `git bisect` is handed commits that change nothing.

- **The EXECUTE notice says so before an agent can get it wrong.** Each agent already has its own branch and worktree and Duo merges them at INTEGRATE, so the phase notice now states that a commit belongs to the agent's own work and that cherry-picking the peer's is unnecessary, with the reason.
- **Duo reports a change carried by both branches when the session reaches REVIEW.** Changes are matched by patch id, so a cherry-pick that landed elsewhere in the branch, or whose message was rewritten, is still recognised, and two agents that independently wrote the same change are not reported against each other. Git does not record which of two identical patches came first, so the report names both copies and leaves the owner to the agent that knows whether it ran the cherry-pick. It runs at the EXECUTE to REVIEW transition because that is the last point where no signature is bound to either branch, so a copy can still be dropped without revoking anything. Duo reports rather than rewrites: the branch belongs to the agent, and silently rebasing an agent's history would contradict the evidence binding that makes the rest of the session auditable.

### Interface

- **The window header names the mode and tints the frame by it.** `FAST` and `GOAL` are now visible without reading the transcript, and the border colour follows, so the two modes are distinguishable at a glance. The badge is placed at the left edge and the box alignment is unchanged, and the Help panel documents it.

### Command line and help

- **`internal/clidoc` is the single source for the `duo` command line.** `duo --help`, `duo <subcommand> --help` and the in-app Help panel all render from one table, so the three cannot drift apart. Every parsed flag must appear in a signature, every subcommand must have one, and an unknown flag must be rejected rather than silently accepted.
- **The reference is complete and guarded.** Every command, flag and environment variable is documented in both READMEs and in `--help`, the in-app Help panel covers every routed key and slash command, and tests fail if the implementation gains a flag, command or key binding that the help does not mention. Flag spellings are matched as whole tokens, so a value that begins with `-` is not mistaken for a flag.

### Packaging

- **Documented `sqlite3` as an optional runtime dependency for `agy` token metrics.** Reading agy's private database for context token tracking requires the `sqlite3` CLI on `PATH`. When missing, Duo now distinguishes the missing dependency from an empty database, showing a visible `no sqlite3` indicator in the work preview header and emitting a one-time diagnostic notice with install instructions instead of silently omitting token metrics. Other agy features, including session identity, remain fully functional without it.

## v0.8.1 — 2026-10-02

A patch release, because every fix in it is a correctness fix and nothing here changes how a session is driven. It carries eight correctness bugs in the resume and driver paths, a data race in the model picker's own tests, and one new feature: a readable Markdown transcript of every session, written into the repository. The build, the unit suites and the end-to-end suites were all green before and after, so none of these were caught by a failing test: they were found by reading the code and by `go test -race`. Three of them could lose or corrupt session state, or leave an agent dead while the UI claimed otherwise.

### Resume

- **A resume no longer records an integration that never happened.** Recovery decides Tony's work was already merged by asking whether Tony's HEAD is an ancestor of Austin's. That check guarded Austin's side but never Tony's, so an agent sitting on the base commit satisfied it: capturing an artifact only requires a *clean* worktree, not a new commit, and the base commit is an ancestor of every later commit. A session interrupted after Austin reviewed but before Tony committed came back claiming "Tony's work is already merged into Austin", jumped to INTEGRATE, and persisted `MergedTony` pointing at the base commit. Both sides must now have moved off base. The existing test covered only the case where *both* agents were at base, which is the one case the old guard already handled.
- **A hand-edited or truncated `state.json` no longer panics on resume.** `Reconcile` assigns into the ready/notes/evidence maps, and a session file missing those objects decoded cleanly — `Store.Load` validates only the schema version and session id — then crashed on the first write. All three maps are now built before the rules that use them. Duo's own writes always populate them, so this needed a corrupt or hand-edited file to reach.
- **The revocation summary names the agent it actually revoked.** The report said "revoked stale signatures: Tony" for a change that revokes Austin's completion request, matching the note written next to it and the live path that performs it.

### Session state

- **Session identity and the launch command are read under the session lock.** `AgyConversationID`, `OpencodeSessionID` and the command builder read the conversation id and the selected model without taking the mutex, while the agy watcher and the model picker wrote them under it. `composeSnapshot` calls `SessionID` on the transport goroutines on every state save, so the reads genuinely raced. A Go string is a pointer and a length, so a racing read can return a mismatched pair and hand a resume a garbage session id. The sibling `OpencodeSessionID` already took the read lock; the agy path had been missed. `Start` holds the write lock for its whole body, so it uses lock-free variants rather than re-acquiring.
- **The agy watcher watches its own process.** It waited on the session's stopped-channel *field* while `Start` reassigned that field, so after a restart a watcher could latch onto the next run's channel and keep polling a process that was already gone. It now receives the channel for the run it belongs to, the same way the PTY reader already did.

### Model picker and driver

- **A failed model or effort switch is no longer reported as a success.** The model reaches a non-pi driver as a startup flag, and `RestartRunning` stops the process before starting it, so a failed restart left the agent **dead** — not stale — while the status line announced the new model and the picker recorded it as current. The picker header therefore advertised a model and a thinking level the agent never received. Both now report the failure, and both record the new value only once the agent actually has it. Bridge (pi) drivers are unaffected: they take the change live and still record it.
- **The verification verdict shows up in the activity line.** The agy activity summary read the tool argument `verdict`, but both bridge extensions send `result` — `verdict` is the MCP spelling, which is a different transport — so the verdict never rendered. The test that covered it used the same wrong key and stayed green.
- **The activity line for an unhandled tool no longer changes between renders.** The fallback summary walked the argument map in Go's randomized iteration order and returned the first string it found, so the same tool call could display a different argument on each frame.

### Session logs

- **Every session now leaves a readable transcript in the repository.** Each run writes Markdown to `.duo/logs/` in the main repository, named after the session id, which already begins with a sortable timestamp. The file holds every line the interface displayed, in the order it appeared, with the agent's own Markdown passed through unchanged so code blocks survive. Entries are appended as they are displayed rather than written at the end, so an interrupted session still leaves its transcript behind. The files live in the main repository and never in an agent worktree, which is what makes them survive `duo clean` and stay where the user works. `.duo/` is already hidden through `.git/info/exclude`, so a transcript never dirties the repository.
- **Austin and Tony each get their own log.** A session writes `<id>-austin.md` and `<id>-tony.md` beside the full transcript, holding every entry that involves that agent: the task the human gave it, what it said back, and what it exchanged with its peer. Selecting by pane would have been wrong: the interface files a message under its *speaker*, so `Human → Austin` appears in Duo's pane, and a peer message appears in both agents' logs because both of them received it.
- **`duo logs` lists the transcripts**, grouped by session and newest first, with sizes and full paths. The files are never pruned automatically.

## v0.8.0 — 2026-10-01

Duo can now drive **opencode** alongside Pi and agy, and four correctness bugs that were hiding in the driver layer are fixed. Two of them could damage a user's repository or execute unintended commands.

### opencode driver

- **Built-in `opencode` driver** (`duo --agent opencode`), selectable per agent like any other driver. Like Pi it is driven by a real bridge rather than transcript scraping: an opencode plugin (`opencode-extension/`) connects back over the local socket, registers Duo's six coordination tools, maps opencode's event bus onto Duo activity, and injects inbound Duo messages into the live session with `session.promptAsync`, so a peer message or harness nudge reaches the agent mid-turn instead of being typed at a PTY.
- **`opencode-extension/`** shares `protocol.ts`, `transport.ts` and `mode.ts` with `pi-extension` verbatim, because both bridges speak wire protocol version 1; only the host binding differs. The plugin is inert unless `DUO_ACTIVE=1`, so a normal opencode session behaves exactly as it would without Duo installed. Installed to `~/.config/opencode/plugin/duo/`.
- **Session identity is discovered, not chosen.** opencode assigns session ids server-side and rejects `--session` for an id it never issued. On a first run Duo injects no id, the bridge reports the real one, and Duo persists it with the session; every later run, including `--resume` after a crash, passes `--session <id>`.
- **The first task still travels through Duo's PTY fallback**, exactly as it does for agy. opencode's TUI only mints its session on the agent's first input, and a session the plugin creates for itself is never rendered in the TUI the human is watching, so driving one would work while showing an empty pane.
- **Per-driver model catalogs**: `opencode models` is read for the model picker alongside `pi --list-models` and `agy models`. opencode gets no injected default model, because a foreign provider id fails with "Model not found" and opencode already resolves its own; a configured reasoning effort is passed as `--variant`.
- **`duo-opencode`** driver shim, plus release packaging for it and for the opencode plugin.

### Correctness fixes

- **Duo no longer modifies your `.gitignore`.** It used to write `.duo/` into that tracked file, dirtying the repository it was launched in, and then needed ~80 lines of filtering to pretend otherwise. `.git/info/exclude` alone hides `.duo/` in every worktree of the repository without touching anything the user can see in a commit, so the filter is deleted. It also had a data-loss hole: an untracked user `.gitignore` was treated as a clean tree, so delivery could fast-forward over real uncommitted work.
- **Agent commands are shell-quoted instead of Go-escaped.** Values Duo injects were built with `%q`, which is Go escaping, not shell escaping: a model containing `$` was expanded by the shell and one containing a backtick executed it. Values are now single-quoted.
- **agy keeps its conversation id when `DUO_PI_COMMAND` contains `--config`.** The check was `strings.Contains(base, "-c")`, which also matches `--config`, so the id was silently dropped and the agent lost its identity across restarts. Flag detection now matches whole tokens.
- **The opencode bridge reports assistant output instead of echoing your own words.** opencode's parts carry no role, so the bridge inferred who was speaking from a message id. The first text part it saw was always the prompt Duo had just injected as a *user* message, so that prompt was reported back as if the agent had said it, and every genuine assistant message was then discarded as belonging to another message. A session could run for an hour with the agent working and the timeline showing nothing but echoes of the human's task and the harness's own nudges. Roles now come from `message.updated`, which opencode emits before the message's parts; a part whose role is not yet known is dropped rather than guessed. The Pi bridge already filtered on the role and was unaffected.
- **The opencode bridge reports the model call, so the harness stops interrupting a slow agent.** It emitted neither `provider_start` nor `provider_end`, so `ProviderActive` was never set and a long stretch of model thinking looked like a stall. The harness would then nudge, and because a nudge is injected as a new message into the session, it interrupted the work it was meant to check on. The provider span now opens when an assistant message is created and closes on its first part or on idle.
- **Assistant text is accumulated rather than overwritten.** An assistant message routinely interleaves text and tool calls, and keeping only the last text block silently discarded everything before it. Text is now keyed per part, which also leaves a streaming part's cumulative text intact.
- **A child session can no longer hijack the opencode bridge.** opencode creates child sessions for side tasks such as title generation, and their events carry their own id; any such id was adopted as the bridge's session, which would have reported a background helper's output as the agent's. Only the root session is authoritative now.
- **Duo identifies an agent's driver from the executable alone.** The check matched anywhere in the command, so it was decided by a flag *value*: `pi --config /tmp/agy.json` was read as agy. The shipped `duo-agy` and `duo-opencode` bridges were not recognized at all and fell through to pi, so the model picker ran `duo-agy --list-models`, and agy answers an unknown flag with its usage on stderr and no catalog, producing "no models reported". Driver detection now reads the first token, and it also stops a path containing spaces from being cut in half.
- **The launch model is passed provider-qualified.** The model picker handed the driver a bare model id; opencode accepts only `provider/model` and aborts on a bare id, and a bare id can also be ambiguous across every authenticated provider. The picker now passes the qualified reference, which the agy driver strips for itself.
- **A prompt Duo injected is never reported back as the agent speaking.** Core records what it pushed into each agent's session and drops an assistant message that repeats it. The bridge is the component that got this wrong, so the core no longer depends on any single bridge getting it right, and the guard covers every driver.
- **The model picker works again for every driver.** Duo appended the catalog subcommand to the end of the agent's launch command, but `agy` and `opencode` take `models` as a subcommand rather than a flag, so both spellings failed: a trailing `models` is read as the positional project path (`opencode --auto models` tries to open a directory named `models`), and carrying agent flags such as `--model` into the subcommand makes it print its usage instead of a catalog. `Ctrl+M` therefore showed the CLI's help text rather than any models. This has affected agy since v0.7.0; `pi` was never affected because it takes a flag. Those two drivers now reuse only the executable, which still honors a custom binary path, and `pi` keeps appending `--list-models` so its custom config flags survive.
- **A persisted model no longer outlives the driver it was chosen for.** `.duo/config.json` stored a driver and a model per agent but rewrote only the driver, so switching an agent from agy to another driver handed agy's model id to a CLI that rejects it and the agent died at startup. The model is now cleared on a driver change, and a model opencode cannot use is ignored so existing config files repair themselves.

### Verified against real agents

Beyond the unit and end-to-end suites, this release was exercised by running Duo on its own repository and on a scratch module, with mixed driver pairs:

- **Fast, `opencode` + `pi`** — a documentation task ran the full loop, with the verifier rejecting twice on concrete, evidence-backed findings before passing. A stale verification was automatically revoked when Austin committed past the reviewed SHA, which is the evidence binding doing its job.
- **Fast, `opencode` + `agy`** — Austin found and fixed two real driver-detection bugs in `internal/models` (a flag value could decide the driver; the shipped `duo-agy` bridge was read as pi), reproduced independently and verified against the three real CLIs.
- **Goal, `opencode` + `agy`** — all five phases completed in about seven minutes. The agents negotiated a shared plan, each implemented its own file in its own worktree, cross-reviewed, and Tony's branch merged into Austin's with no conflict. Both final signatures bound to the same integrated commit.

No prompt echo, no spurious harness nudge and no stall appeared in any of these runs. The model catalog was checked against the real CLIs throughout: opencode reports 102 models, agy 14, pi 421.

### Interface

- Every agent now reports as connected, not only the ones with a bridge. A driver whose bridge cannot announce itself is announced when its process starts; previously an opencode agent looked absent for the whole time before its first task.
- The work preview reports a dead process instead of saying `waiting`, which contradicted its own header and hid the reason an agent was stuck.
- `duo plugins` lists the three built-in drivers with aligned columns.

### Packaging

- The version string lives in one place (`internal/version`) and is shared by the CLI, the three driver shims and the embedded MCP server. Duplicating it is what previously let the Pi bridge installer report a version four releases behind.
- `scripts/install.sh`, `scripts/install-release.sh` and the release workflow build and ship `duo`, `duo-pi`, `duo-agy`, `duo-opencode`, `pi-extension/` and `opencode-extension/`.
- Documentation covers the three drivers, per-driver session identity, model catalogs and the fact that model or effort changes restart a CLI-only driver. Verified against the code: `duo sessions -a`, `duo clean -f/-n` and the `duo mcp-server` flags documented in the README all behave as described.

## v0.7.0 — 2026-09-30

- Pluggable Agent Driver Architecture: Refactored Duo agent orchestration into a universal `Driver` interface (`agent.Driver`), abstracting process lifecycle, PTY interaction, and activity monitoring away from Pi-specific assumptions.
- Antigravity Driver (`agy`): Added first-class support for Google Antigravity CLI agents. Operates via live JSONL transcript observation and streaming activity extraction to monitor tools, thinking states, streamed text, and errors seamlessly in the Duo TUI.
- Embedded Stdio MCP Server: Added an embedded Model Context Protocol (MCP) server (`internal/mcp`) exposing standard Duo coordination tools (`duo_send`, `duo_set_status`, `duo_set_plan`, `duo_set_verification`, `duo_escalate`) via JSON-RPC 2.0 stdio transport for MCP-native agents.
- External Driver Plugin Discovery & Standalone Binaries:
  - Added support for external driver plugins following the `duo-driver-<name>` and `duo-<name>` naming conventions in `~/.duo/plugins/` and `$PATH`.
  - Added standalone `duo-pi` and `duo-agy` binaries.
  - New `duo plugins` command lists built-in drivers (`pi`, `agy`) and discovered external plugins with executable paths and readiness status.
  - Per-agent driver selection via CLI (`--austin-driver`, `--tony-driver`, `--driver`) and `agents.austin.driver` / `agents.tony.driver` in `.duo/config.json`. There is no per-agent environment variable: `DUO_DRIVER` sets both agents.
- Multi-driver TUI Experience:
  - Work preview headers for Austin and Tony now prominently indicate the active driver, e.g. `Austin preview (agy) · working · 5s` or `Tony preview (pi) · idle`.
  - The model picker (`Ctrl+M`) displays dynamic, driver-accurate loading status (" Loading models from <driver>…") matching the target agent's driver.
  - Model catalog queries automatically execute the appropriate driver command (`pi --list-models` or `agy models`) and cache catalogs per agent, allowing Austin and Tony to run on different drivers simultaneously.
- Packaging & Distribution: Updated `scripts/install.sh`, `scripts/install-release.sh`, and GitHub Actions release workflows to build, package, and distribute `duo`, `duo-pi`, and `duo-agy`.

## v0.6.0 — 2026-09-30

- Interactive slash command palette and autocomplete: Typing `/` in the composer opens a command palette with real-time fuzzy filtering and command descriptions. Navigate options with `↑` / `↓` (circular selection), press `Tab` to autocomplete, press `Enter` to execute immediately (or fill command arguments for `/escalate` and `/mode`), and press `Esc` to dismiss. Supported commands: `/escalate [reason]`, `/mode <fast|goal>`, `/model`, `/overview`, `/preview`, `/timestamps`, `/help`, `/status`, `/clear`, `/quit`, and `/exit`.
- Dynamic mode escalation: Fast mode sessions can now dynamically escalate to Goal mode on the fly without resetting the session, re-creating worktrees, or losing in-flight commits. Escalation can be triggered by the user via the composer (`/escalate [reason]` or `/mode goal`), or autonomously by agents via the `duo_escalate` tool (or `protocol.MsgEscalate`). The session enters the `PLAN` phase, initializes a shared plan v1 seeded with the escalation reason and prior notes, transitions Tony into an active co-developer and co-signer, and persists immediately so `duo --resume` resumes in Goal mode.
- Deterministic automated test verification gate: Duo can now execute an automated test command (e.g. `go test ./...` or `npm test`) before accepting Austin's verification request in Fast mode, before Tony's final verification, and before delivery in Goal mode. If tests fail, the failure output is reported immediately to Austin with concrete error details, blocking phase advance to `VERIFY` and keeping Tony focused on valid builds. Configure via `--test-cmd <command>`, `DUO_TEST_COMMAND`, or `"testCommand"` in `~/.duo/config.json` / `.duo/config.json`.
- TUI usability enhancements:
  - Fixed keyboard forward delete: The `Del` key (`\x1b[3~`) now correctly deletes the character ahead of the cursor in the composer and model picker filter.
  - Timeline scrolling now supports keyboard navigation with `PgUp` and `PgDn` (half-page scroll per press).
  - Universal cross-platform clipboard copy: timeline selections are emitted using terminal OSC 52 escape sequences (supporting remote SSH, tmux, and modern terminal emulators) alongside native desktop tools (`pbcopy` on macOS, `wl-copy` on Wayland, `xclip`/`xsel` on X11).
  - Persistent composer task history: submitted tasks are appended to `~/.duo/history` (overridable with `DUO_HISTORY_FILE`), so `↑`/`↓` history recall survives restarting or resuming Duo.
  - Session Overview (`Ctrl+O`) diffstat and toggle: the overview now displays a live `git diff --stat` change summary (including untracked files) comparing Austin's worktree to the session base, and pressing `Ctrl+O` again toggles back to the main view.
- Persistent configuration files: Duo now loads global (`~/.duo/config.json`) and repository-level (`.duo/config.json` or `.duo.json`) configuration. You can persist default session mode, custom `piCommand`, harness thresholds, and per-agent model defaults (`agents.austin.model`, `agents.tony.model`) and thinking levels (`agents.austin.thinking`, `agents.tony.thinking`). Explicit CLI flags (`--mode`) and environment variables (`DUO_MODE`, `DUO_PI_COMMAND`) cleanly override config file defaults.
- Session and worktree lifecycle management: `duo sessions` lists active, in-progress and completed sessions for the repository (or across all repositories with `--all`), reporting mode, phase, branch, timestamp, worktree presence and active/delivered status. `duo clean` removes completed (`DONE`) sessions, prunes their Git worktrees, deletes temporary branches and reclaims disk space; `--force` cleans unfinished sessions while safely skipping any session currently locked by a live Duo process, and `--dry-run` previews actions without touching disk.
- A Fast-mode follow-up driven through native Pi (`Ctrl+A`) now has a path back to the peer. The composer already reopened a `DONE` session, but native Pi bypasses Duo, so Austin's completion request was rejected with `project is already DONE` and its new commit stayed stranded in the worktree. A request that names a different Austin HEAD than the delivered round now reopens the round and runs a fresh verification and delivery; a retry that still names the delivered HEAD stays rejected, so a stray resend cannot undo a delivery.

## v0.5.1 — 2026-09-29

The main view is now one conversation timeline; the frame's top row names the Git repository, and every message header is a directed, speaker-coloured pair. This release also fixes two Fast-mode paper cuts.

- The main view is now a single conversation **timeline** instead of two side-by-side panes. Human tasks, peer messages (`Austin → Tony`, `Tony → Austin`), Austin's messages to the human, verifier verdicts and Duo system messages are interleaved in arrival order; each message sits on its speaker's own side under a `Sender → Receiver` header. Both speakers use the same bubble width (about three quarters of the row), so the side a block sits on, not a heavier header, shows who is speaking; a Tony message that fits on one line hugs the right edge like a short chat bubble, while a wrapped one is left-anchored at its indent. Every message header carries its `HH:MM:SS` time by default (`Ctrl+G` hides or restores the stamps), so one exchange is readable end to end. A peer message is recorded once, on the sender's side, so the old split-pane `→ Tony: sent` / `← From Austin:` hint lines are gone. The work preview, status rows, composer and every keyboard shortcut are unchanged, and the timeline is one scrollable, selectable region (mouse wheel and drag).

- The top border row is now one title naming the Git repository Duo resolved (never the launch directory), padded with the border dash. The `Austin · idle` / `Tony · working` pane titles and their `[↗]` attach buttons are gone: connection, process and working state, the spinner and the attach buttons all live in the work preview headers, and the header state word now animates while an agent works. The timeline's scroll mark (`↑N` / `▼`) moved to that title row.

- Every timeline header is a directed pair (`Duo → Human`, `Duo → Tony`, `Austin → Tony`, `Austin → Human`, …) rather than a bare speaker name: system notices, harness notes, errors and verdicts carry the same direction, and a resumed journal that recorded a bare name is normalized. Headers are coloured by speaker, and a message addressed to the human is bold in the speaker's own colour while agent-to-agent and system headers stay dim.

Bug fixes:

- A model id containing a space is no longer truncated when the Pi model catalog is parsed, and the Fast `duo_set_status` tool description is mode-aware instead of describing a Goal-style phase sign-off.

## v0.5.0 — 2026-09

Fast Mode is now the default; Goal Mode is the explicit heavy workflow.

- `duo` starts in **Fast** mode: Austin is the driver, Tony is a read-only independent verifier, and the session runs `RUNNING → VERIFY → DONE` with no shared Plan and no dual sign-off.
- `duo --mode goal` / `-m goal` selects the previous `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE` workflow, unchanged. The mode is fixed per session and persisted; a legacy session with no recorded mode resumes as Goal.
- New `duo_set_verification` tool (Tony only, Fast only) reports `passed` or `issue_found` with a concrete note for the exact Austin commit under review. A `passed` result is bound to that commit; any new commit invalidates it and returns the session to `RUNNING`.
- Fast has no `duo_set_plan`, and a Tony `duo_set_status` in Fast is rejected with guidance. Only Austin's verified commit is ever delivered.
- The harness is mode-aware: it nudges the agent that owns the current phase (Austin in `RUNNING`, Tony in `VERIFY`) and escalates a stuck `RUNNING` episode into one diagnosis request to Tony; Goal keeps its both-idle behavior.
- The TUI status line, startup message, Help, resume prompt and Pi system prompt are mode-aware.
- `DUO_MODE` (`fast` or `goal`) configures a new session; `--mode`/`-m` wins, and `--resume` keeps the persisted mode.
- Recovery reconciles Fast verification against Git and deterministically returns `VERIFY → RUNNING` if the verified commit is no longer Austin's HEAD.

TUI readability and handoff polish:

- Agent output is rendered as lightweight markdown: styled headings, blockquotes, links and bold/italic/code spans, and markdown tables drawn with real, aligned borders. A table wider than its pane wraps its widest cells instead of truncating them.
- Mouse drag over a pane selects that agent's text and copies it to the clipboard (`pbcopy` on macOS). Clicking a preview band header still opens that agent's native Pi; a click inside a pane without a drag selects nothing.
- `Ctrl+】` is accepted as a third return-from-native-Pi sequence alongside `Ctrl+]` and `Ctrl+\`.
- A wrapped markdown list keeps a hanging indent, so continuation lines line up under the item text instead of the bullet. Nested lists are indented by their own depth.
- A resumed session replays each pane's most recent 200 entries, persisted as `tui_entry` records in `events.jsonl`, so the previous conversation is visible immediately.
- Delivery recognizes a user-resolved merge: once the final Duo commit is an ancestor of the original HEAD — including after `git merge --no-ff` — `duo apply` treats the handoff as already applied and records `DONE` without moving the branch again. A refused delivery now prints that merge command.
- The Pi bridge no longer reports a compaction abort (`"This operation was aborted"`) as an agent error.
- The system log between the panes is now a real scrollable pane (four rows by default, mouse-wheel and selectable), so harness notes, delivery results and errors no longer vanish after two lines.
- A scrolled pane keeps its place while new output arrives, shows `↑N` with `▼` when unseen output landed below, and no longer re-parses every markdown block on each frame (wrapped lines are cached until the entries or width change).
- A per-agent **work preview** now sits between the panes and the system log, so a long turn is no longer opaque. Its header names the state and how long the current turn has been running; the rows below show what the agent is doing now (the running tool with its age, or the model and thinking level it is thinking with), the turn's recent tool trail with ✓/✗ and durations, the turn's last error, and the tail of the text being streamed. Failures are reported by the bridge (`tool_error`, agent errors) rather than parsed out of the full-screen PTY, so an error that already scrolled off Pi's screen is still visible. `Ctrl+P` collapses the band, and a short terminal hides it automatically so the panes keep their rows.
- The composer gained Home/End, Ctrl+U/Ctrl+K, Ctrl+W, Alt+←/→, line-up/down movement and task-history recall with Up/Down; `Ctrl+G` toggles message timestamps; `Ctrl+O` opens a session overview with the worktrees, the full verification note or shared plan, and the delivery state.
- `Ctrl+M` opens a model picker so the human no longer has to enter native Pi just to switch a model: it lists the catalog of the same Pi installation (`pi --list-models`), filters as you type, switches the target between Austin and Tony with `Tab`, and applies live through the Pi bridge (`pi.setModel`) without restarting the agent or losing the conversation. `Shift+Tab` cycles the target's thinking level, and `●` marks the model each agent is actually on. `Enter` applies the model and closes the picker; `Space` applies it and keeps the picker open, so a model and a thinking level can be set in one visit. Both the model and the thinking level are recorded by Pi in its session transcript, so they survive a restart or resume. `Ctrl+M` is only distinct from `Enter` on terminals that honor modifyOtherKeys/CSI-u, so `Alt+M` is provided as a fallback.
- Wrapping prefers a space boundary so words are no longer split mid-token; markdown links are OSC 8 hyperlinks whose label stays copyable; consecutive messages are separated by a dim rule; and the delivery state is always visible in the status area.
- Emoji are measured as the two terminal columns they occupy. The width table only covered `U+1F300..U+1FAFF`, so common emoji such as ✅, ❌ and ⭐ were counted as one column and every affected pane line overflowed, pushing the divider to the right. Width now follows the Unicode properties: default-emoji (Emoji_Presentation) are two columns, a text-presentation pictograph such as ⚠ or ↗ is one column unless followed by VS16, and zero-width joiners, variation selectors and skin-tone modifiers are zero width. The pane header button (`↗`) no longer shifts the `┬` away from the `│` below it.

Workflow, verification and cost:

- A task submitted through the Duo composer after `DONE` reopens the session for a new round instead of being answered with no path back to the peer: Fast returns to `RUNNING`, Goal to `PLAN`, and the finished round's delivery checkpoint is cleared so a fresh verification and delivery can run. Native Pi attachment (`Ctrl+A` / `Ctrl+T`) still leaves the phase untouched.
- A Fast verifier's `issue_found` verdict is no longer rendered as a red `ERROR`. It is a normal workflow outcome, so it now shows as a warning in the verifier's pane, with a one-line summary in the Duo pane instead of a second full copy of the report.
- Harness nudges inject a compact state block that references the shared plan (`v2, 4.8KB — read it with duo_status`) instead of re-inlining the whole plan text on every nudge. When and how the harness nudges is unchanged; only the repeated multi-KB payload is gone. `duo_status` still returns the full plan.
- The Fast verifier prompt now tells Tony to report its verdict once with `duo_set_verification` and not restate it with `duo_send`, because Duo Core already relays the verdict to Austin. This removes a duplicate copy of every verification report.

## v0.4.7 — 2026-09

TUI scrolling and multiline composer improvements.

- Austin and Tony panes now support independent mouse-wheel history scrolling.
- The composer supports multiple visible lines, cursor editing, and `Ctrl+Enter` or `Shift+Enter` for a newline.
- Restored `Ctrl+A`, `Ctrl+T`, `Ctrl+R`, `Ctrl+Y`, `Ctrl+Q`, and `Ctrl+/` while `modifyOtherKeys` is enabled.

## v0.4.6 — 2026-09

TUI Help and usability polish; no collaboration, recovery, or delivery semantics changed.

- Added a full alternate-screen Help view with wrapped content, keyboard scrolling, resize-safe clamping, and `Ctrl+/` / `Esc` close behavior.
- Kept native Pi input passthrough intact, including `Ctrl+/`; only Duo main TUI handles Help.
- Clarified the Austin-only human composer, separated transient status, and reduced persistent footer hints.

## v0.4.5 — 2026-09

Resume collaboration wake-up.

- A resumed runtime sends each reconnected Pi agent one phase-aware `resume_prompt`, derived from authoritative durable state; reconnects do not duplicate it and fresh sessions keep the Austin-only bootstrap.

## v0.4.4 — 2026-09

Launch-directory working scope.

- Git ownership stays at the repository root while Austin and Tony start in the repository-relative directory Duo was launched from: `cd repo/packages/web && duo` keeps branches, worktrees and delivery at `repo`, and both agents' default working directory at `packages/web` inside their private worktrees.
- The scope is recorded in the durable snapshot and restored across `duo --resume` and agent restart, instead of collapsing back to the repository root.
- The Pi bridge reports each agent's active scope in its system prompt.
- Coordinator tests now wait for the durable delivery checkpoint, removing a teardown race.

## v0.4.3 — 2026-09

Test lifecycle and release validation hardening.

- Coordinator E2E teardown now deterministically joins clients, server goroutines and Git worktree cleanup before temporary directories are removed.
- Main and tagged CI continue to gate the four-platform release archives.

## v0.4.2 — 2026-09

Reliability fixes for delivery and releases.

- Coordinator end-to-end tests now wait for matching request/response acknowledgements instead of treating independent TCP writes as ordered.
- INTEGRATE final approval is edge-triggered; duplicate `ready=true` messages are idempotent and cannot trigger delivery again.
- Each session serializes the complete delivery transaction, rechecks current state under the transaction lock, and treats an applied checkpoint for the same final HEAD as monotonic.
- Critical pending, applied and DONE checkpoints fail closed; a failed pending write prevents any original-repository mutation.
- Tagged release builds now require `go test ./...`, `go vet ./...`, and a normal binary build before archives publish.

## v0.4.1 — 2026-09

Deliverable handoff. INTEGRATE sign-off no longer means only "agreed": `DONE` now means the integrated artifact has been delivered back into the repository the user launched Duo from.

- Final sign-off in INTEGRATE records approval (`readyForDelivery`) but keeps the session in INTEGRATE; `DONE` is a separate, explicit transition that runs after delivery.
- On a dual INTEGRATE sign-off Duo delivers the whole final integrated HEAD into the user's original repository, never a per-file copy.
- Delivery is fast-forward only: Duo refuses a dirty repository, a different checked-out branch, a diverged history or a detached HEAD, and never runs `reset --hard`, `checkout -f`, `clean`, `merge --no-ff` or `rebase` on the user's repository.
- A refused delivery keeps the session in INTEGRATE with both signatures intact, records a `pending` delivery checkpoint, and prints the exact `duo apply <session-id>` command.
- `duo apply [session-id]` retries a blocked delivery with the same safety rules; on a repository with a single pending delivery it needs no arguments.
- The final approval and pending-delivery checkpoint are persisted before Git is touched, so a crash between the fast-forward and the DONE write is reconciled on the next `duo --resume` or `duo apply`.
- A v0.4.0 session already marked `DONE` can still be handed off with `duo apply`, resolving the final HEAD from the recorded delivery, integration head, Austin worktree or Austin branch.
- `DONE` keeps both final signatures and reports the target branch, final HEAD and applied HEAD instead of claiming the original branch was untouched.
- The INTEGRATE prompt now gives Austin an explicit final-tree cleanup duty and Tony a repository-hygiene review, so collaboration-only artifacts do not ship.
- The harness stops nudging once INTEGRATE is dual-signed and delivery is in progress.

## v0.4.0 — 2026-09

Durable Duo sessions and crash recovery. After a Duo Core, Austin or Tony crash Duo can continue the original task instead of silently restarting at PLAN.

- Persisted, versioned session snapshot in `~/.duo/sessions/<repo-id>/<session-id>/state.json`, written atomically (temp file → `fsync` → rename → directory `fsync`).
- An unsupported `schemaVersion` is refused with an explicit error instead of being read best-effort.
- `duo --resume [session-id]` reopens a session. Bare `duo --resume` picks the repository's single unfinished session; when several exist they are listed instead of guessed.
- Existing worktrees are validated and reused, never recreated. A missing worktree fails with a clear message.
- Git is the ground truth: on resume, worktree HEADs, an in-progress merge (`MERGE_HEAD`) and the integration result are re-checked, and every signature whose evidence no longer matches is revoked.
- Recovery never moves a session backwards to PLAN and never repeats a merge that already completed or is still in progress.
- A dirty worktree no longer blocks resume: recovery reports it and revokes only the signatures it invalidates, leaving uncommitted files untouched.
- An advisory `flock` per session, with PID metadata for diagnostics, stops two Duo processes from driving the same session.
- `events.jsonl` holds a diagnostic journal; `duo.log` holds lifecycle output and redacts session tokens.
- Per-agent Pi session identity is stable across restarts via `--session-id`, so Austin and Tony keep their own Pi conversation history. `DUO_PI_COMMAND` is still honoured.
- The bridge protocol version is now shared between the Go core and the Pi extension; mismatched clients are rejected.
- A resumed session gets a harness grace period (`DUO_HARNESS_RESUME_GRACE_SECONDS`, default 45s) so reconnecting agents are not mistaken for stalled ones.
- Fixed a path-comparison bug that made resume reject its own worktrees on macOS, where Git reports `/private/var/...` for a persisted `/var/...` path.

## v0.3.3 — 2026-09

TUI renderer hardening; no agent, persistence, or collaboration changes.

- Fixed resize artifacts (border trails, stale footer, stacked frames) by forcing a full-screen clear on terminal resize, native Pi return, alternate-screen re-entry, renderer start, and any layout change.
- Removed the fake 60×18 minimum terminal size. Layout now uses the real geometry, and sub-minimum terminals get a bounded `Terminal too small` notice that cannot wrap or scroll.
- Wrapped each frame in DEC synchronized output (`CSI ?2026 h/l`) and temporarily disabled autowrap for the frame write.
- Coalesced SIGWINCH bursts into a single latest-size PTY update and one frame, removing redundant redraws while dragging a window.
- Added a dirty/renderer scheduler (~60 FPS): event, input, resize, restart, and native detach all funnel through `markDirty`; idle Duo no longer repaints and the spinner only advances while an agent is busy.
- Distinguished `exited` from `failed` process state; a Duo-initiated stop is reported as a normal exit.

## v0.3.2 — 2026-09

- Replaced the external `script` host with direct Go-owned Austin/Tony PTYs; detached agents continue running and retain recent raw output.
- Propagated SIGWINCH to both PTYs, including detached processes.
- Added manual restart of exited agents (Ctrl+R Austin, Ctrl+Y Tony) without changing worktrees or bridge credentials.
- Added a random hex suffix to automatically generated Duo session IDs.

## v0.3.1 — 2026-09

Runtime hardening and first binary release.

### Fixed

- included the previously ignored `cmd/duo` CLI entrypoint in source releases;
- made the installed Pi bridge opt-in for Duo-launched sessions;
- isolated concurrent Duo runs with dynamic ports, session IDs and random tokens;
- stopped reporting normally exited Pi processes as running;
- stopped the TUI cleanly when terminal input closes;
- aligned CI and documentation with the integrated TUI release.

### Added

- macOS and Linux release archives for amd64 and arm64;
- one-command release installer for the binary and Pi extension.

## v0.3.0-alpha.1 — 2026-09

First integrated terminal UI preview.

### Added

- integrated Duo TUI with Austin/Tony native Pi attach and detach;
- animated agent status and standard ANSI colors;
- full provider and agent error reporting, including HTTP 402/429 responses;
- copy-pasteable Git setup guidance for non-Git and empty repositories;
- terminal mode cleanup after native Pi sessions.

## v0.2-alpha — 2026-09

First public experimental release candidate.

### Added

- two persistent peer Pi agents: Austin and Tony;
- single human entry via Austin;
- live `duo_send` peer messaging;
- versioned shared Plan and dual sign-off;
- lifecycle: PLAN, EXECUTE, REVIEW, INTEGRATE, DONE;
- idle/stall harness that can nudge Austin;
- isolated Git worktrees and per-agent branches;
- commit-SHA evidence for execution completion;
- peer-HEAD evidence for review completion;
- automatic stale-signature revocation;
- Tony → Austin integration merge;
- human-controlled final merge boundary (accurate for v0.2: the automatic fast-forward handoff arrived in v0.4.1, below);
- modular Go core and modular Pi extension.

### Design change from earlier experiments

PLAN is not treated as a global write lock. Agents may make provisional edits in their own worktrees while discussion continues. Formal coordination happens at Plan and artifact sign-off checkpoints.
