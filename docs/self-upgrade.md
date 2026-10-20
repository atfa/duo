# Self-upgrade: how Duo restarts itself mid-session

**Status: implemented.** STEP mode plus `duo step-loop` (entered through
`scripts/duo-step-loop.sh`) run this loop; the sections below are the design record.
The measurements and the two hard facts still explain why it is built this way, and
the parts that read as recommendations are the pre-implementation notes they were
written as. The question was whether Duo can run the loop

> human gives one task → split it into small tasks → develop one → install the new
> version locally → quit the running Duo → start the new Duo → next small task

without a human touching the terminal between steps. The claims below are marked
**measured** (run on this machine, Linux 7.2.9, against the live session that wrote
this document) or **read** (from the source at the cited line).

## Conclusion

The loop is feasible, and it is now built: `duo --mode step` runs a dual-approved
queue of steps, each one reviewed against an exact HEAD and landed before the next
starts, and `duo step-loop` turns the self-upgrade into one more step — install from
the delivered checkout, resume on the new binary, continue the queue. The one part
Duo cannot supply is the relaunch itself: the agent that installs the new version is
a **descendant** of the Duo it is upgrading, so it dies with it. That is why the
loop lives in `duo step-loop`, outside the session, and why the session requests the
restart with an exit code instead of performing it.

The four pieces the exploration called the minimum, as they actually exist:

1. a sentinel exit code from `duo` (`upgradeExitCode = 77`), written out as
   `errUpgradeRequested` and returned from `run()` so `main` exits with it rather
   than through `log.Fatal`;
2. a session command that lets an agent request it (`RestartDuo`, the `SetLanguage`
   shape) reaching the TUI through `session.View.Restart`;
3. a launcher that owns the terminal, re-execs `duo resume` on the new binary, and
   does not die with the session (`duo step-loop`, entered by
   `scripts/duo-step-loop.sh`);
4. a schema guard that refuses an upgrade the next binary cannot read — and, after
   review caught it, one that compares **before** installing rather than after.

Recommended policy, unchanged: upgrade **per delivered release**, not per micro-task.

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
- **Quitting is a supported, guarded operation** (`internal/gotui/keys.go`), and the
  session is preserved by it. **read**
- **Agents can already change session state from inside.** The command surface is
  small and extensible: `SubmitTask`, `SetModel`, `RestartAgent`, `SetLanguage`
  (`internal/session/command.go:22-59`). Exit-code plumbing has a precedent:
  `errDeliveryPending` → `os.Exit(1)` (`cmd/duo/main.go:40`). **read**
- **The task queue has a carrier.** In Goal mode the plan is a versioned Markdown
  string persisted in the snapshot (`project.Snapshot.Plan`, `PlanVersion`) and
  re-injected on resume, so "remaining small tasks" survive the restart with no new
  storage. Fast mode has no plan; the loop shipped as a third mode (STEP) whose queue
  is structured state (`project.Steps` plus a Duo-owned cursor), because a cursor an
  agent could move by editing plan text would be a second source of truth. **read**

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

## What the implementation looks like

```text
scripts/duo-step-loop.sh            the only process that survives an upgrade
  exec duo step-loop --mode step …  loop:
    run the session                 exit 0  -> finished, stop
                                    exit 77 -> upgrade requested
                                    else    -> stop, show the output
    on 77: read .duo/upgrade.json
           install: check schema, then install from the delivered checkout
           resume:  exec self resume <sessionId>
```

- Austin's per-step action: implement → commit → `duo_set_status ready=true` → Tony
  verifies that HEAD → the step lands on the base branch → the **last action of a
  round** may request the restart.
- The new Duo resumes, the resume wake fires with the STEP resume prompt (which
  re-injects the queue and forbids re-landing a ticked step), and the queue continues
  at the same step.
- The install source is the repository the human launched Duo from, i.e. the branch
  the steps are landing on, never an agent's feature worktree.
- The per-session facts (exit code, session id, schema) stay in Go: a shell script
  would have to parse them out of JSON to act on them.

## Constraints and risks, in order of how much they hurt

1. **Schema bump breaks the chain.** `sessionstore.Load` compares `SchemaVersion` for
   equality and returns a `SchemaError` on any mismatch
   (`internal/sessionstore/store.go:107`), so a new Duo that bumps the schema cannot
   read the state the old one wrote. `duo step-loop` therefore compares the schema
   constant in the delivered source with the one this session was written by before
   it installs anything (`performUpgrade` in `cmd/duo/stepupgrade.go`), and stops with
   a message if they differ — installing first would leave the session with no binary
   that can continue it. A migration path is still the larger, later fix.
2. **Install source.** Installing from Austin's feature worktree puts the running
   coordinator on unmerged code. Install from the delivered `main` only.
3. **Install is not atomic today.** `install.sh` installs the two bridges, runs the
   tests, then builds the binaries. A failure in the middle leaves a mix — new
   bridges over the old coordinator, for instance. Driver protocol
   (`internal/driver/protocol.go:24` = 1) and bridge protocol
   (`pi-extension/protocol.ts:7` = 1) are version-gated, so the mismatch is detected
   rather than silent, and `duo step-loop` treats a failed install as "do not
   restart": it returns instead of resuming.
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
   (`internal/version/version.go`), so a self-built binary reports the version of its
   source, not of the commit it was built from. `.duo/upgrade.json` therefore records
   the reporting binary's version and schema, and the loop installs from the
   repository checkout it was launched in: the commit that reaches the machine is that
   checkout's HEAD, which the request does not record. Recording the built commit is
   the obvious next improvement, not something to read out of `duo --version`.

## What is not proven yet

The exploration ended with a proposed "cheapest spike": exit code + launcher +
schema guard, no install subcommand, no queue automation. That spike is what shipped,
with the queue automation included because STEP mode needed the queue anyway — so
what remains unmeasured is not the design but the cost.

The pieces are tested: the exit-code path, the handoff file, the schema guard **and
its order**, the queue's domain rules, per-step landing over real Git worktrees, the
mid-queue resume, and the verifier sync. What has **not** been run is the whole loop
live — one session that lands a step changing Duo itself, restarts on the installed
binary, and finishes the queue on the other side. That measurement still decides the
practical question: what a restart actually costs in agent context and wall-clock
time, and therefore whether a queue should be sized per release or per micro-task.
