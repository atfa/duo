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

## v0.4 — durability and configurability

- [ ] persistent session state
- [ ] resume after Duo Core restart
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
- automatic merging into the user's original branch;
- adding complex task graphs before they are proven necessary.
