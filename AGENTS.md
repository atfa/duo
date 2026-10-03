# Project Working Agreements

- Keep code simple: reuse existing code, the standard library, and platform features first.
- Do not add unrequested abstractions, dependencies, configuration, or redundant design.
- Make the smallest root-cause diff; preserve existing behavior outside the fix.
- Leave one focused runnable test for non-trivial logic.
- Conserve tokens and communicate concisely, without skipping necessary understanding or verification.

## Useful entry points

- `cmd/duo/`: CLI dispatch, flags and user-facing help.
- `internal/project/`: authoritative workflow state and transitions.
- `internal/coordinator/`: message handling, gates and phase changes.
- `internal/driver/`, `plugins/`: driver plugin contract and built-in drivers.
- `internal/workspace/`, `internal/delivery/`, `internal/recovery/`: Git isolation, safe handoff and resume reconciliation.
- `internal/tui/`: terminal UI projection; it must not become a second source of project state.
- `pi-extension/`, `opencode-extension/`: agent-side bridge adapters.

Read [architecture](docs/architecture.md) for boundaries/invariants, the relevant [protocol](docs/driver-plugin-protocol.md) or [bridge protocol](docs/duo-bridge-protocol.md) when changing those interfaces, and the [plugin guide](docs/plugin-development-guide.md) when adding a driver.

## Verification

- Go changes: `go test ./...`, `go vet ./...`, `go build ./cmd/...` (or `make check`).
- Pi extension changes: `cd pi-extension && bun test`.
- Run focused package tests while iterating; run the relevant full checks before handing off.
