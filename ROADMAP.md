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
- [x] human-controlled final merge

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

## v0.4 — configurability (remaining)

- [ ] configurable agent names/roles
- [ ] configurable model/provider per agent
- [ ] configurable integration strategy
- [ ] improved worktree lifecycle cleanup
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
