# Security

## Current trust model

Duo is designed for a **trusted local development machine**.

The Go core listens on an OS-assigned localhost port. Each run gives its Pi processes a random session token, and the core rejects clients with the wrong session, token or agent identity. The local protocol is not encrypted and should not be exposed to an untrusted network.

## Agent capabilities

Pi coding agents may execute tools and shell commands with the permissions of the user running them. Git worktrees isolate branches and reduce accidental overwrite between Austin and Tony, but they are **not OS sandboxes**.

Run Duo only against repositories and environments where you are comfortable allowing your configured Pi agents to operate.

## Git safety boundary

When Austin and Tony both sign INTEGRATE, Duo hands the final integrated HEAD back to your repository by **fast-forwarding only the branch Duo recorded when the session started**.

Against your working checkout, the only mutating Git command Duo runs is `git merge --ff-only <final-head>`. Duo does not create a merge commit, does not rebase, and does not rewrite history. It never runs `reset --hard`, `checkout -f`, `clean`, or `merge --no-ff` against your repository. (Duo does create and remove linked worktrees and `duo/<session>/austin|tony` branches inside your repository's Git directory; those do not touch your checkout.)

The fast-forward is verified with read-only Git queries **before** anything is written. Duo refuses, leaves every file untouched, keeps the session in INTEGRATE with both signatures, and records a `pending` delivery when:

- the working tree has uncommitted changes;
- you are on a different branch than the one Duo recorded;
- your HEAD has diverged from the final result, or the final result is not derived from the recorded base commit;
- the repository is on a detached HEAD, or the recorded starting branch is unknown.

After a successful fast-forward, Duo re-reads HEAD and fails loudly if it is not exactly the expected final commit.

Delivery is also a no-op when the final HEAD is already an ancestor of the current HEAD, so a merge you finished yourself is recognized as applied and your own commit is preserved. A cherry-pick is not recognized, because it does not make the final HEAD an ancestor.

Review the delivered commit as you would any other change to your branch. Duo's guarantee is that it will not guess at an unsafe merge — not that the delivered work is correct. When delivery is refused, finish the merge or cherry-pick yourself from the printed final HEAD, then re-run `duo apply`.

## On-disk session files

Durable sessions write to `~/.duo/sessions/<repo-id>/<session-id>/`:

- `state.json` — phase, Plan, signatures, evidence, worktree paths and branches, and Pi session IDs. It contains no credentials.
- `events.jsonl` — a diagnostic journal of phase, signature, bridge and merge events. It also carries the TUI pane transcript (`tui_entry` records, 200 per pane) so the visible history can be replayed on resume; treat it as containing whatever your agents wrote to the panes.
- `duo.log` — lifecycle output. Session tokens are redacted before being written.
- `lock` — an advisory `flock` holding the owner PID and hostname.

The directory is created with owner-only permissions. These files describe your project's state and are worth treating like any other local development artifact; they are not encrypted and are not designed to be shared.

## Reporting a security issue

Please avoid publishing exploit details in a public issue before maintainers have had a chance to assess them. Use a private GitHub security advisory when available for the repository.
