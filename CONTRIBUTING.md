# Contributing to Duo

Duo is early-stage and intentionally small. Contributions are welcome, but changes should preserve the project's core design: **two capable peer agents, a thin deterministic coordination layer, and explicit Git evidence.**

## Before opening a PR

```bash
go test ./...
go vet ./...
go build ./cmd/duo
```

If you change the Pi extension, also test a real two-Pi session where Tony starts idle and Austin wakes Tony through `duo_send`.

## Design principles

1. **Keep Duo Core authoritative for shared state.** Do not duplicate Plan/phase truth in the Pi extension or future UI.
2. **Prefer evidence over model claims.** A clean commit SHA is better than an assistant saying "done".
3. **Use worktree isolation instead of trying to parse every possible file-writing command.**
4. **Phases are checkpoints, not behavioral cages.** Do not prohibit useful early analysis/review just to keep the timeline aesthetically pure.
5. **Keep the Pi extension thin.** It should translate Pi events/tools to/from Duo, not become a second orchestrator.
6. **Do not auto-merge into the human branch.** Human-controlled final integration is a deliberate boundary.
7. **Avoid complexity without observed need.** Duo should not become a task-management framework merely because one can be built.

## Good first contribution areas

- tests for Git edge cases;
- improved diagnostics;
- worktree cleanup commands;
- documentation;
- future TUI state projection;
- session persistence design.

## Reporting behavior issues

When possible, include:

- Duo version/commit;
- OS;
- Go version;
- Pi version;
- Git version;
- relevant Duo Core logs;
- current phase;
- whether Austin/Tony worktrees were clean;
- minimal reproduction steps.
