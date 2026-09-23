# Security

## Current trust model

Duo v0.4.0 is designed for a **trusted local development machine**.

The Go core listens on an OS-assigned localhost port. Each run gives its Pi processes a random session token, and the core rejects clients with the wrong session, token or agent identity. The local protocol is not encrypted and should not be exposed to an untrusted network.

## Agent capabilities

Pi coding agents may execute tools and shell commands with the permissions of the user running them. Git worktrees isolate branches and reduce accidental overwrite between Austin and Tony, but they are **not OS sandboxes**.

Run Duo only against repositories and environments where you are comfortable allowing your configured Pi agents to operate.

## Git safety boundary

Duo intentionally does not merge the integrated result into the human's original branch. Review the integrated Duo branch before merging or cherry-picking it yourself.

## On-disk session files

Durable sessions write to `~/.duo/sessions/<repo-id>/<session-id>/`:

- `state.json` — phase, Plan, signatures, evidence, worktree paths and branches, and Pi session IDs. It contains no credentials.
- `events.jsonl` — a diagnostic journal of phase, signature, bridge and merge events.
- `duo.log` — lifecycle output. Session tokens are redacted before being written.
- `lock` — an advisory `flock` holding the owner PID and hostname.

The directory is created with owner-only permissions. These files describe your project's state and are worth treating like any other local development artifact; they are not encrypted and are not designed to be shared.

## Reporting a security issue

Please avoid publishing exploit details in a public issue before maintainers have had a chance to assess them. Use a private GitHub security advisory when available for the repository.
