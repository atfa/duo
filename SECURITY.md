# Security

## Trust model

Duo is designed for a trusted local development machine. The Go core listens on an
OS-assigned localhost port and gives each run a random session token. It rejects
clients with the wrong session, token or agent identity. The local protocol is not
encrypted and must not be exposed to an untrusted network.

Configured coding agents run with the permissions of the user running Duo and may
execute tools and shell commands. Git worktrees isolate changes between Austin and
Tony, but they are **not OS sandboxes**. Run Duo only in repositories and
environments where those agents may operate.

## Git safety boundary

After the phase-specific approval, Duo delivers by fast-forwarding only the branch
recorded when the session started: Tony's successful verification of Austin's exact
commit in Fast, or both agents' approval of the integrated Austin HEAD in Goal.

Duo checks safety with read-only Git queries before changing the original checkout.
For delivery, its only mutating Git command against that checkout is
`git merge --ff-only <final-head>`. It never creates a merge commit, rebases, or
rewrites history there, and never runs `reset --hard`, `checkout -f`, `clean`, or
`merge --no-ff` against it. Duo does create/remove linked worktrees and temporary
`duo/<session>/austin|tony` branches in the repository's Git directory.

Delivery refuses without changing the checkout if it is dirty, on a different or
detached branch, diverged from the final result, or the result cannot be proven to
derive from the recorded base. A refused handoff remains pending in VERIFY (Fast)
or INTEGRATE (Goal). If you finish the merge yourself, `duo apply` recognizes it
when the final HEAD is an ancestor of your current HEAD. A cherry-pick alone does
not satisfy that check. A no-change task is a no-op when the final commit is already
an ancestor, even if the checkout is dirty.

After a successful fast-forward, Duo re-reads HEAD and fails loudly if it is not
exactly the expected final commit.

Review the delivered commit like any other branch change. Duo guarantees that it
refuses an unsafe handoff; it does not guarantee the agents' work is correct.

## On-disk session files

Durable sessions write to `~/.duo/sessions/<repo-id>/<session-id>/`:

- `state.json` — mode, phase, Plan or verification, signatures, evidence, worktree
  paths/branches and driver session state.
- `events.jsonl` — diagnostic events and the TUI transcript (`tui_entry`, up to 200
  entries per pane), which may contain agent output.
- `duo.log` — lifecycle output with session tokens redacted.
- `lock` — advisory `flock` with the owner PID and hostname.

The directory is owner-only. Files are not encrypted and describe project activity;
treat them as local development artifacts and do not share them casually.

## Reporting a security issue

Please avoid publishing exploit details in a public issue before maintainers have had
a chance to assess them. Use a private GitHub security advisory when available.
