# Self-upgrade: Duo restarting itself mid-session

Exploration, not a specification. The question was whether Duo can run the loop

> human gives one task → split it into small tasks → develop one → install the new
> version locally → quit the running Duo → start the new Duo → next small task

without a human touching the terminal between steps. The claims below are marked
**measured** (run on this machine, Linux 7.2.9, against the live session that wrote
this document) or **read** (from the source at the cited line).

## Conclusion

The loop is feasible, and most of it already exists: the session state is durable and
restart-safe, `duo resume` re-establishes phase, plan, worktrees and agent sessions,
recovery revalidates signatures against Git, and the harness already grants a grace
period after a resume. What does not exist is the restart itself, and Duo cannot
supply it: the agent that installs the new version is a **descendant** of the Duo it
is upgrading, so it dies with it. The relaunch has to come from a process that is not
Duo.

The minimum that makes the loop work is four small pieces, none of them a refactor:

1. a sentinel exit code from `duo` meaning "relaunch me";
2. one session command that lets Austin request it (the `SetLanguage` shape);
3. a ~15-line wrapper script that owns the terminal and re-execs `duo resume`;
4. a schema guard that refuses a self-upgrade the next binary cannot read.

Recommended policy, given the cost below: upgrade **per delivered release**, not per
micro-task.

## What is already in place

- **State survives a restart.** A snapshot is composed after every mutation
  (`recovery.Compose`) and written atomically with an fsynced temp file plus a rename
  (`sessionstore.Store.Save`), so a kill at any instant leaves a complete checkpoint.
  **read**
- **The lock cannot go stale.** The session lock is an `flock`; the kernel releases it
  when the process exits, so the new Duo can take it the moment the old one is gone
  (`internal/sessionstore/lock.go:48`). **read**
- **Resume is the crash path, and a self-upgrade is a clean crash.** Recovery treats
  Git as ground truth, revokes signatures whose evidence no longer matches, never
  moves a phase backwards, and detects an interrupted integration merge instead of
  repeating it (`internal/recovery/recovery.go:1`). On resume the agents are woken
  with the `[Duo session resumed]` header that tells them not to restart the task
  (`internal/coordinator/resume_prompt.go:36,136`), and the watchdog grants
  `DUO_HARNESS_RESUME_GRACE_SECONDS` (default 45) of extra grace. **read**
- **Duo already prints the relaunch command.** On exit it prints "Duo stopped.
  Session … was preserved; resume it with `duo resume …`" (`cmd/duo/main.go:144`).
  `duo resume` with no id resumes this repository's unfinished session. **read**
- **Quitting is a supported, guarded operation** (`internal/tui/app.go:210`), and the
  session is preserved by it. **read**
- **Agents can already change session state from inside.** The command surface is
  small and extensible: `SubmitTask`, `SetModel`, `RestartAgent`, `SetLanguage`
  (`internal/session/command.go:22-59`). Exit-code plumbing has a precedent:
  `errDeliveryPending` → `os.Exit(1)` (`cmd/duo/main.go:40`). **read**
- **The task queue has a carrier.** In Goal mode the plan is a versioned Markdown
  string persisted in the snapshot (`project.Snapshot.Plan`, `PlanVersion`) and
  re-injected on resume, so "remaining small tasks" survive the restart with no new
  storage. Fast mode has no plan, so the loop belongs to Goal mode. **read**

## The two hard facts

**1. Austin cannot restart the Duo that owns him.** Measured process tree of the
session that wrote this document:

```text
zsh            pts/1  pgid 470882
└─ duo --austin-driver pi --tony-driver pi   pid 470882  pgid 470882  pts/1
   └─ duo-plugin-pi
      └─ pi                                 pid 470924  pgid 470924  pts/2
         └─ bash (the agent's shell)
```

Each agent runs in its own session and PTY (`pty.StartWithSize`,
`internal/agent/session.go:432`), which is why Ctrl+C in the human's terminal does
not reach it — and why Duo must kill it explicitly: `Stop()` sends `SIGTERM` to the
whole process group and `SIGKILL` two seconds later
(`internal/agent/session.go:606-613`). So the agent that asks for the restart is
killed by it, mid-turn, and cannot observe the new version starting. The loop's
continuation must be launched by the new Duo, via the resume wake.

**2. Replacing a running binary is safe, if the write is a rename.** Measured on this
machine:

| write method | result on a running binary |
| --- | --- |
| `cp` | fails, `ETXTBSY` ("文本文件忙") |
| `dd` open-for-write | fails, `ETXTBSY` |
| `go build -o` | succeeds; inode changes (temp file + rename) |
| GNU `install` | succeeds; inode changes |

Against the live coordinator: `go build -o ~/.local/bin/duo ./cmd/duo` replaced the
binary of the running Duo (inode `18119385` → `18120621`), exit 0, and PID 470882
kept running. `scripts/install.sh` builds with `go build -o` and
`scripts/install-release.sh` uses `install -m 0755`, so **both installers already
work while Duo is live**. The running process keeps its old inode; the new binary is
used by the next start.

## Design that fits the existing shape

```text
duo-loop.sh (the only new process; owns the terminal)
  loop:
    duo --mode goal ...                 # exit 0  -> finished, stop
                                        # exit 77 -> upgrade requested
                                        # else    -> stop, show output
    on 77: read .duo/upgrade.json, exec duo resume <id>
```

- Austin's per-task step: develop → commit → `duo_set_status ready=true` → Tony
  verifies → delivery lands on `main` → install from `main` → request the restart as
  the **last action of the round**.
- The new Duo resumes, the resume wake fires, Austin reads the plan's next unchecked
  step and continues.
- Install should be a `duo` subcommand that runs the existing installer body against
  the delivered `main`, so the loop does not shell out to a script path that may
  itself have changed.

## Constraints and risks, in order of how much they hurt

1. **Schema bump breaks the chain.** `sessionstore.Load` compares `SchemaVersion` for
   equality and returns a `SchemaError` on any mismatch
   (`internal/sessionstore/store.go:107`), so a new Duo that bumps the schema cannot
   read the state the old one wrote and the loop stops mid-task. The loop must
   compare the new binary's schema version before installing, and stop with a message
   rather than install an unreadable state. A migration path is the larger, later
   fix.
2. **Install source.** Installing from Austin's feature worktree puts the running
   coordinator on unmerged code. Install from the delivered `main` only.
3. **Install is not atomic today.** `install.sh` installs the two bridges, runs the
   tests, then builds the binaries. A failure in the middle leaves a mix — new
   bridges over the old coordinator, for instance. Driver protocol
   (`internal/driver/protocol.go:24` = 1) and bridge protocol
   (`pi-extension/protocol.ts:7` = 1) are version-gated, so the mismatch is detected
   rather than silent — the loop must treat a failed install as "do not restart".
4. **Every restart costs both agents' context.** The agent processes are killed, and
   the new agents rebuild from `duo_status`, the plan and the transcripts. A restart
   per micro-task pays that cost per micro-task; batching tasks per release pays it
   once.
5. **The loop owns the human's terminal.** In Fast mode the human supplies the next
   task; in this loop the plan does. Unattended runs need the queue written up front,
   and the human's intervention point moves from "next task" to "abort the loop".
6. **Crash vs upgrade are the same path, mostly.** Recovery is Git-ground-truth, so a
   kill between commit and deliver is safe. A kill between install and resume leaves a
   new binary over an old session — resume handles it, schema permitting (risk 1).
7. **Version identity.** The installed version is a compile-time constant
   (`internal/version/version.go`), so a self-built binary from a worktree reports the
   version of its source, not of the commit it was built from. The loop should record
   the built commit in `.duo/upgrade.json`, not trust `duo --version`.

## Cheapest spike

Exit code + wrapper + schema guard, no install subcommand, no queue automation: run
`duo-loop.sh` against one Goal session whose plan has two trivial steps, and measure
what a restart actually costs in agent context and wall-clock time. That number, not
the design, decides whether the loop is worth automating further.
