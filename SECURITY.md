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
commit in Fast or a Step `[fast]`; both agents' approval of the integrated Austin
HEAD in Goal or a Step `[goal]`. The final Step always requires both agents to sign
off, including when its policy is `[fast]`.

Duo checks safety with read-only Git queries before changing the original checkout.
For delivery, its only mutating Git command against that checkout is
`git merge --ff-only <final-head>`. It never creates a merge commit, rebases, or
rewrites history there, and never runs `reset --hard`, `checkout -f`, `clean`, or
`merge --no-ff` against it. Duo does create/remove linked worktrees and temporary
`duo/<session>/austin|tony` branches in the repository's Git directory.

`duo apply --branch <name>` is the one alternative, for repositories whose base
branch is protected: it creates that branch at the verified final commit with
`git branch` and never checks out, moves or writes the current branch, its HEAD or
its index, so the dirty-tree and current-branch rules above do not apply to it. It
still refuses the branch Duo started on, an invalid branch name, and a branch that
already exists at a different commit; a branch that already points at the artifact
is the idempotent no-op. The Duo worktrees are removed after such a delivery unless
`--keep` asks to preserve them.

`duo apply --push` adds one remote operation. Before anything moves, Duo reads the
upstream with `git ls-remote` and refuses the whole delivery if the remote branch
holds commits the final result does not contain — including commits this
repository has never fetched. After the local delivery lands, the only mutating
command it runs against the remote is `git push <remote> <sha>:<refs/heads/...>`,
never forced, and it reads the remote back afterwards to prove the result. A
refused push leaves the local branch and the remote untouched; a push that fails
after the local landing leaves the artifact delivered and reports why.

`duo apply --pr` adds no hosting dependency of its own. It lands the artifact on a
branch, pushes it exactly as `--push` does, and then asks `gh pr create` to open
the pull request with a body built from evidence Duo already holds locally: the
plan version, the step queue, both signatures with their evidence SHAs, the
verdict, and the diffstat. Duo never talks to a hosting API itself and stores no
hosting credentials. When `gh` is not installed, Duo prints the same body and
exits successfully, leaving the push and the pull request to you.

Delivery refuses without changing the checkout if it is dirty, on a different or
detached branch, diverged from the final result, or the result cannot be proven to
derive from the recorded base. A refused handoff remains pending for retry with
`duo apply`. If you finish the merge yourself, `duo apply` recognizes it when the
final HEAD is an ancestor of your current HEAD. A cherry-pick alone does not satisfy
that check. A no-change task is a no-op when the final commit is already an
ancestor, even if the checkout is dirty.

After a successful fast-forward, Duo re-reads HEAD and fails loudly if it is not
exactly the expected final commit.

Review the delivered commit like any other branch change. Duo guarantees that it
refuses an unsafe handoff; it does not guarantee the agents' work is correct.

## Session recovery

Session snapshots and event logs are local, unencrypted files; they may contain
agent output and project details. On resume, Duo treats Git as ground truth and
revokes approvals whose recorded evidence no longer matches the worktrees. A
signature is not a guarantee that changed or unverifiable work will still be
approved after recovery.

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

## Remote control

A phone can steer a session through a relay that is deliberately content-blind.

- The pairing secret exists only in the QR code's URL fragment and in
  `~/.duo/remote.json`, which is written `0600`. A browser never sends a
  fragment to a server, so the relay never receives the secret.
- The relay stores ciphertext envelopes and `proof`, a keyed hash of the secret.
  Holding `proof` lets an operator join a room; it does not reveal the secret and
  does not allow reading a message.
- A command from the phone carries a MAC over the fields a relay could change
  and a nonce the host issued, so a relay operator cannot inject or replay an
  instruction. The host refuses a bad MAC, a nonce from a state older than the
  last eight it published, a command id it already executed, and an unsupported
  kind, and reports the refusal to the phone.
- The relay can refuse, delay or drop traffic, and it can close the room when a
  free-tier budget is exceeded. None of that can stop the local session.
- A pairing expires after at most 8 hours and can be revoked with `/remote off`,
  which deletes the room on the relay and the pairing file locally.
- A cleartext relay URL is accepted only for a loopback host, so a pairing secret
  cannot be sent over plain HTTP to a remote machine by accident.

The wire format and its limits are in [docs/remote-protocol.md](docs/remote-protocol.md).

## Reporting a security issue

Please avoid publishing exploit details in a public issue before maintainers have had
a chance to assess them. Use a private GitHub security advisory when available.
