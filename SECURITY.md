# Security

## Current trust model

Duo v0.2-alpha is designed for a **trusted local development machine**.

The Go core listens on localhost by default and the Pi extension connects to it over local TCP. v0.2 does not implement authentication or encryption for the Duo protocol and should not be exposed directly to an untrusted network.

## Agent capabilities

Pi coding agents may execute tools and shell commands with the permissions of the user running them. Git worktrees isolate branches and reduce accidental overwrite between Austin and Tony, but they are **not OS sandboxes**.

Run Duo only against repositories and environments where you are comfortable allowing your configured Pi agents to operate.

## Git safety boundary

Duo intentionally does not merge the integrated result into the human's original branch. Review the integrated Duo branch before merging or cherry-picking it yourself.

## Reporting a security issue

Please avoid publishing exploit details in a public issue before maintainers have had a chance to assess them. Use a private GitHub security advisory when available for the repository.
