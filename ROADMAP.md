# Roadmap

Duo's roadmap is deliberately incremental. The project should add infrastructure only when it improves real peer collaboration.

## v0.2-alpha — headless peer runtime

- [x] thin Pi bridge
- [x] persistent Austin/Tony connections
- [x] live `duo_send` peer messages
- [x] single human entry through Austin
- [x] versioned shared Plan
- [x] dual sign-off
- [x] `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE`
- [x] harness/watchdog recovery
- [x] isolated Git worktrees
- [x] commit-SHA evidence for EXECUTE
- [x] peer-HEAD evidence for REVIEW
- [x] stale-signature revocation
- [x] Tony → Austin integration merge
- [x] human-controlled final merge (superseded in v0.4.1 by the automatic fast-forward handoff)

## v0.3 — terminal UI

Primary goal: make the existing runtime observable and controllable without moving business logic into the UI.

- [x] Austin and Tony side-by-side panes
- [x] one global human composer
- [x] per-agent runtime state (connecting / working / thinking / tool / idle)
- [x] shared phase + Plan version + signatures
- [ ] Git/worktree status and change summary
- [x] peer-message visibility
- [x] harness events and recovery visibility
- [x] clear final integrated branch/commit handoff

## v0.3.1 — runtime hardening

- [x] Pi extension opt-in
- [x] dynamic localhost port per Duo run
- [x] session ID and random-token connection validation
- [x] process-exit detection
- [x] macOS/Linux binary releases and curl installer

## v0.3.2 — runtime / PTY hardening

- [x] manual restart controls for exited agents
- [x] direct PTY ownership and SIGWINCH resize propagation
- [x] random suffix for default session IDs

## v0.3.3 — TUI renderer hardening

- [x] full clear on resize / layout change / alternate-screen re-entry
- [x] real terminal geometry with a bounded too-small notice
- [x] synchronized output and per-frame autowrap protection
- [x] SIGWINCH coalescing and a dirty renderer scheduler
- [x] idle TUI no longer repaints; spinner gated on agent activity
- [x] `exited` vs `failed` process state

## v0.4.0 — durable sessions

After a Duo Core, Austin or Tony crash the user must be able to continue the original task rather than restart at PLAN.

- [x] versioned session snapshot persisted atomically
- [x] `duo --resume [session-id]`
- [x] stable per-agent Pi session identity across restarts
- [x] session lock (advisory `flock`) with stale-holder diagnostics
- [x] reconciliation of checkpoint vs Git truth, revoking stale signatures
- [x] dirty-worktree recovery that never fails and never discards files
- [x] interrupted / already-completed merge detection (no duplicate merge)
- [x] ordered recovery transaction applied before agents start
- [x] diagnostic `events.jsonl` and token-redacting `duo.log`
- [x] Go/Pi bridge protocol version validation
- [x] failure-injection tests for the recovery path

## v0.4.3 — test lifecycle and release validation

- [x] deterministic coordinator E2E teardown
- [x] explicit server/client/worktree lifecycle joins
- [x] main and tagged release validation

## v0.4.2 — delivery reliability

- [x] request/response-synchronized coordinator E2E tests
- [x] edge-triggered, idempotent final approval
- [x] serialized and monotonic delivery checkpoints
- [x] fail-closed critical delivery persistence
- [x] release workflow test gate

## v0.4.1 — deliverable handoff

`DONE` must mean the final Git artifact reached the user, not merely that two agents agreed.

- [x] INTEGRATE dual sign-off records final approval instead of ending the run
- [x] whole final integrated HEAD delivered into the user's original repository
- [x] fast-forward-only safety: refuse dirty, wrong-branch, diverged and detached repositories
- [x] `pending` delivery checkpoint that preserves both signatures
- [x] `duo apply [session-id]` to retry a blocked handoff
- [x] crash-window reconciliation between the fast-forward and the DONE write
- [x] v0.4.0 `DONE` sessions handed off via `duo apply`
- [x] DONE reports target branch, final HEAD and applied HEAD, keeping both signatures
- [x] INTEGRATE final-tree cleanup duty for Austin and hygiene review for Tony

## v0.4.4 — launch-directory working scope

- [x] the directory Duo was launched from becomes Austin and Tony's default working scope
- [x] Git ownership (branches, worktrees, delivery) stays at the repository root
- [x] scope recorded in the durable snapshot and restored across resume and agent restart
- [x] scope reported to each agent through the Pi bridge system prompt

## v0.4.6 — TUI help and usability

- [x] full alternate-screen Help view with content wrapped to terminal width
- [x] keyboard scrolling and resize-safe offset clamping in Help
- [x] native Pi input passthrough preserved, including `Ctrl+/`
- [x] clearer Austin-only composer label, separated transient status, reduced persistent footer hints

## v0.4.7 — scrollable history and multiline composer

- [x] independent mouse-wheel history scrolling per agent pane
- [x] multiline composer with cursor editing, up to four visible lines
- [x] `Ctrl+Enter` / `Shift+Enter` inserts a composer newline
- [x] `Ctrl+A/T/R/Y/Q` and `Ctrl+/` restored while `modifyOtherKeys` is enabled

## v0.5.0 — Fast Mode as the default

Fast Mode makes the safety boundary the default instead of the ceremony: one driver, one independent verifier, verified delivery, no shared Plan and no dual sign-off. The unreleased v0.4.8 notes were folded into this release.

- [x] fixed per-session mode: `fast` (default) or `goal` (`duo --mode goal` / `-m goal`)
- [x] concrete Fast phases: `RUNNING → VERIFY → DONE`, with `issue_found` returning to `RUNNING`
- [x] structured verification bound to the exact Austin commit under review
- [x] Fast has no shared Plan and no dual sign-off; Tony is a read-only verifier
- [x] the verified commit is delivered by the existing fast-forward-only handoff
- [x] mode-aware durable state, recovery, harness, TUI status and Pi prompt
- [x] Goal mode preserves the `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE` workflow unchanged
- [x] markdown rendering in agent panes: styled headings, blockquotes, links and bold/italic/code spans
- [x] wrapped markdown lists keep a hanging indent; nested lists indented by depth
- [x] markdown tables drawn with real borders, aligned to the pane width
- [x] narrow table cells wrapped instead of truncated
- [x] mouse-drag selection in a pane with clipboard copy (`pbcopy`)
- [x] `Ctrl+】` recognized as a return-from-native-Pi sequence
- [x] pane transcript persisted and replayed on `duo --resume`
- [x] delivery recognizes a user-resolved `git merge --no-ff` as already applied
- [x] scrollable, selectable four-row system log between the panes
- [x] sticky pane scroll that keeps position while output grows
- [x] per-agent work preview: running tool and age, model and thinking level, tool trail, last error and stream tail (`Ctrl+P`)
- [x] model picker with live switching through the Pi bridge (`Ctrl+M` / `Alt+M`) and thinking-level cycling (`Shift+Tab`)
- [x] multiline composer with cursor editing and task-history recall, plus `Ctrl+O` session overview and `Ctrl+G` timestamps
- [x] Unicode-property width measurement so emoji and the pane header button no longer shift borders

## v0.5.1 — conversation timeline and directed headers

The main view became one conversation timeline, the frame's top row names the repository, and every message header carries a direction and a per-speaker colour.

- [x] single conversation timeline replacing the two side-by-side panes, with per-speaker anchoring and one shared bubble width
- [x] `HH:MM:SS` stamps on every header, toggled with `Ctrl+G`
- [x] repository directory in the top border row, replacing the pane titles and their attach buttons
- [x] directed, speaker-coloured headers (`Duo → Human`, `Austin → Tony`, …), bold for messages addressed to the human
- [x] animated state word and native-Pi attach button in each work preview header
- [x] short single-line Tony replies hug the right edge like a chat bubble
- [x] model ids containing a space parse correctly from the Pi catalog

## v0.4 — configurability (remaining)

- [ ] configurable agent names/roles
- [x] configurable model/provider and thinking level per agent via config file (`~/.duo/config.json` and `.duo/config.json`)
- [ ] configurable integration strategy
- [x] improved worktree lifecycle cleanup (`duo sessions` and `duo clean`)
- [ ] structured artifact/task metadata if real usage justifies it

## Later / exploratory

- [ ] adapters for non-Pi agent runtimes
- [ ] more than two peers, only if the peer model remains understandable
- [ ] richer policy hooks for review/evidence
- [ ] remote operation with an explicit security model

## Non-goals for now

- replacing Pi's coding-agent runtime;
- building a large hierarchical swarm;
- forcing every agent action through a central scheduler;
- automatic merge commits, rebases or history rewriting in the user's original repository (the v0.4.1 handoff is fast-forward only and refuses to guess);
- adding complex task graphs before they are proven necessary.
