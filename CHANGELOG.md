# Changelog

All notable project milestones are documented here.

## Unreleased

### Corrected

- **The v0.7.0 changelog no longer names two environment variables that were never read.** `DUO_AUSTIN_DRIVER` and `DUO_TONY_DRIVER` were documented as a way to pick a per-agent driver, and no code has ever consulted them. The entry now lists the flags and `.duo/config.json` that do work, and says which variable sets both agents.

### Redundant commits

Goal mode delivers a merge of the two agent branches, so a commit that exists only because an agent cherry-picked its peer's work is redundant once merged. The copy is not harmless: reverting either commit removes the change while the other still claims to add it, and `git bisect` is handed commits that change nothing.

- **The EXECUTE notice says so before an agent can get it wrong.** Each agent already has its own branch and worktree and Duo merges them at INTEGRATE, so the phase notice now states that a commit belongs to the agent's own work and that cherry-picking the peer's is unnecessary, with the reason.
- **Duo reports a change carried by both branches when the session reaches REVIEW.** Changes are matched by patch id, so a cherry-pick that landed elsewhere in the branch, or whose message was rewritten, is still recognised, and two agents that independently wrote the same change are not reported against each other. Git does not record which of two identical patches came first, so the report names both copies and leaves the owner to the agent that knows whether it ran the cherry-pick. It runs at the EXECUTE to REVIEW transition because that is the last point where no signature is bound to either branch, so a copy can still be dropped without revoking anything. Duo reports rather than rewrites: the branch belongs to the agent, and silently rebasing an agent's history would contradict the evidence binding that makes the rest of the session auditable.

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
  - Per-agent driver selection via CLI (`--austin-driver`, `--tony-driver`, `--driver`) and `agents.austin.driver` / `agents.tony.driver` in `.duo/config.json`. Corrected in v0.8.2: this entry previously also named `DUO_AUSTIN_DRIVER` and `DUO_TONY_DRIVER`, which were documented but never read. `DUO_DRIVER` sets both agents.
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
