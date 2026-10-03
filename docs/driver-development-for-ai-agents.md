# Writing a Duo driver, for AI coding agents

This is the same task as [`plugin-development-guide.md`](plugin-development-guide.md),
arranged for an agent that will not read prose twice. It exists because every
substantive bug in this protocol shipped with a green test suite — the protocol is
easy to get *nearly* right and hard to notice when it is wrong.

Read [`driver-plugin-protocol.md`](driver-plugin-protocol.md) for the normative spec.
Read this for the order to work in, the decisions to make, and the failures you are
most likely to hit.

---

## 0. Read this first: what Core must never learn

The entire point of the protocol is that **Core does not know your agent's name**.
Every one of these is a regression, not a style preference:

| Never | Because |
|---|---|
| Add your driver's name to anything under `internal/` or `cmd/duo/` | Core must not branch on which driver it is running |
| Have Core build your command line | `prepare` returns the finished line; Core has no per-agent builder |
| Have Core parse your `state` blob | It is opaque. Store it verbatim, hand it back verbatim |
| Have Core read your agent's config file | That is your plugin's job, where your agent's vocabulary lives |
| Shell out from Core to your agent | `Models`, `Thinking`, `State` and `Prepare` are the only channels |
| Import `internal/agent` from your plugin | You implement `driver.Handler`, nothing else |

If you catch yourself adding a `switch` on a driver name, stop: the answer belongs in
your manifest.

## 1. Preconditions — verify, do not assume

Run these before writing any code. Each one is cheap now and expensive later.

```bash
# 1. The reference plugin builds and passes the contract. This is your working
#    baseline and your positive control: if it ever fails, the suite is broken,
#    not your driver.
go test ./internal/driver/ -run TestContractPassesForTheReferencePlugin -count=1

# 2. See the contract run against a real plugin binary, so you know what passing
#    looks like before you produce anything.
go build -o /tmp/duo-plugin-example ./cmd/duo-plugin-example
go build -o /tmp/duo ./cmd/duo && /tmp/duo plugin test /tmp/duo-plugin-example -v

# 3. Know the interfaces you are implementing against. Line numbers drift; grep
#    for the type name rather than trusting them.
sed -n '1,75p' internal/driver/serve.go        # Handler, StateProvider, Serve
sed -n '131,215p' internal/driver/protocol.go # Capabilities, AgentInfo, Manifest, ProbeResult
```

## 2. Decide before you code — the capability table

Almost every mistake in this protocol is a wrong answer here. Work down the list and
answer each question explicitly.

| Question about your agent | If yes | You must |
|---|---|---|
| Does it assign its own session/conversation ID? | `resume: server` | **Implement `State()`** (below). Getting this wrong silently opens a new conversation on every launch |
| Does it adopt an ID you hand it? | `resume: client` | Mint it on first run, store it in `state`, replay it |
| Can it be resumed at all? | `resume: none` | Store nothing |
| Can Core inject a prompt mid-turn over a bridge? | `liveSteering: true` | Must also be `bridge: agent` |
| Does a prompt reach it only by typing at the terminal? | `ptyFallback: true` | Otherwise Core has no way to deliver a prompt and the contract will fail you |
| Can it enumerate models? | `models: true` | Implement `Models()` |
| Does it have reasoning-effort levels? | `thinking: true` | Implement `Thinking()` |
| Can it take `--model` in place, without a restart? | `liveModelSwitch: true` | Otherwise Core restarts it, which is what all three shipped drivers do |
| Same, for reasoning effort? | `liveThinkingSwitch: true` | |
| Does its Agent Adapter attach to Duo's bridge by itself? | `selfReports: true` | If false, Core announces at launch and your attach is expected later |
| Does it report provider and id separately, and want `provider/id`? | `modelReference: qualified` | If it wants the bare id, use `bare` — Core will not guess |

`modelReference` deserves care: it is how Core joins what you report into what your
picker shows. Getting it wrong shows the wrong string in the UI and sends the wrong
thing to your CLI.

### `resume: server` is the one that bites

If your agent assigns its own ID, `prepare` cannot know it — `prepare` runs *before*
the agent starts. So you need somewhere to report it later:

```go
func (p *Plugin) State() (json.RawMessage, error) {
    p.mu.Lock()
    defer p.mu.Unlock()
    return json.Marshal(state{SessionID: p.learned})
}
```

Hold the learned ID in memory, set it when your Agent Adapter reports, and return it
from `State()`. Core calls `State()` when it writes a snapshot, which is the only
moment a mid-session discovery can still reach disk.

Return whatever you know **now**, and prefer the learned value over whatever was in
the blob. Never record a value Core sent you as something you *learned* — that turns
replay material into a fake discovery, and it survives long enough to be believed.

`duo plugin test` fails a driver that declares `resume: server` and does not answer
`state`. That check exists because its absence once hid a fork-on-every-launch behind
a fully green suite.

## 3. Work in this order

Do not write the whole plugin then test it. Each step below is independently
verifiable, and the contract runner will tell you which step is wrong.

1. **`main.go` with `describe`.** `driver.Serve(os.Stdin, os.Stdout, plugin{})` plus
   a manifest answering the table in §2. Build it and run `duo plugin test` — it
   will fail `probe`, and that is progress, not a problem.
2. **`probe`.** Report whether the CLI exists and say why not when it does not. An
   unavailable probe with no reason is a contract failure.
3. **`prepare`.** Build the command line, return `state`, and set `cleanup` for
   anything you create. Check with `no-empty-flag`: never emit a flag for an unset value.
4. **`state`**, if `resume: server`. Do it now, not later — it is the step that
   fails silently.
5. **`models` / `thinking`**, if declared.
6. **An Agent Adapter**, only if your agent has an API you can reach. Without one you
   cannot receive injected prompts, so `liveSteering` is false and delivery is the PTY.
7. **`duo plugin test <your-binary>`** until it is clean, then read every line of the
   verbose output. Do not stop at the exit code.

## 4. Failure catalogue — symptoms you will actually hit

Each of these shipped, in this repository, past a green suite.

| Symptom | Cause | Fix |
|---|---|---|
| `describe: process exited` immediately; a machine with a v0.9-era `duo-<name>` shim is affected | Your driver name collides with a legacy exec shim on `PATH` | Core now skips shims and falls back, but name your plugin `duo-plugin-<name>` so this cannot happen |
| Agent starts, rejects the model, sits on an error screen; your default model is a hardcoded fallback | Core invokes your CLI with **no `HOME`**, so your settings file was unreadable | Core now passes `os.Environ()` plus its own variables. If you shell out to another binary, expect and require the environment to be there |
| `probe` reports "not on PATH" while launch works | Same cause: no `PATH` in the process | Same fix |
| Picker comes up empty | Same cause: `Models()` shells out and finds no CLI | Same fix |
| `no models reported by <cli> --list-models` in CI, but works locally | Your CLI is installed but not authenticated | Gate the assertion on the **CLI itself**, not on your own `Models()` call — see below |
| Every launch opens a new conversation | `resume: server` without `State()`, or `State()` returning the blob unchanged | Implement `State()`; return the learned value |
| A resumed session's ID vanishes after a save | `State()` returned a blob with an empty id | Report at least what your last `prepare` resolved. Omit fields you have nothing for — never send a placeholder |
| A restart opens a *different* conversation and activity stops | `prepare` prefers the stale blob over what you learned | Prefer the learned value in `prepare` too, not only in `State()` |
| `FAIL delivery-path: no delivery path` | You declared neither a receiving bridge nor `ptyFallback` | Fix the manifest, or implement the path you claimed |
| Contract fails only under heavy load, with a model-listing error | Your listing shells out to another CLI and outran the deadline | Report the timeout as a timeout, not as "not implemented". Do not inflate the deadline to hide it |
| A driver named `duo-driver-foo` is refused | Legacy-shim detection is too broad | It matches only the short `duo-<name>` spelling now; `duo-driver-<name>` is a valid plugin name |

### On environment-dependent assertions

The recurring lesson from this codebase: **a test that verifies your plugin must not
quietly verify the machine.** If your catalog assertion needs a signed-in CLI, ask the
CLI directly whether it can enumerate, and skip when it cannot:

```go
out, err := exec.CommandContext(ctx, probe.AgentPath, "--list-models").CombinedOutput()
if err != nil {
    t.Logf("cannot list models on this machine, unverified: %v: %s", err, out)
    return
}
```

Gate on the CLI, never on your own `Models()` call — otherwise a broken `Models()`
excuses itself by looking like an unavailable machine.

## 5. Verify like you mean it

The suite being green is weak evidence. Before you claim done:

```bash
gofmt -l .                    # walk the tree; a file-list glob silently reports nothing
go build ./... && go vet ./...
go test ./... -count=1
go build -o /tmp/duo-plugin-<name> ./cmd/duo-plugin-<name>
/tmp/duo plugin test /tmp/duo-plugin-<name> -v
```

Then, for anything non-trivial, **break it on purpose and watch the test fail.** A
test that has never failed has not been shown to work. Specifically:

- Break `State()` and confirm resume behaviour fails, not just a unit test.
- Make your listing fail and confirm the failure is reported rather than skipped.
- Confirm your `prepare` output for a *second* launch actually carries the identity
  from the first — the disk path is where the last two bugs lived.

## 6. Definition of done

- [ ] `duo plugin test` passes, and you have read the verbose output line by line
- [ ] `resume: server` implies `State()` returning the **learned** value
- [ ] `prepare` prefers learned over stored, so an in-run restart resumes
- [ ] `state` never contains a placeholder; absent fields are omitted
- [ ] Command line carries no flag for an unset value
- [ ] `cleanup` removes everything you create outside the worktree
- [ ] `state` round-trips: `prepare` → launch → learn → save → `prepare` resumes
- [ ] No driver name appears anywhere in `internal/` or `cmd/duo/`
- [ ] You can state which delivery path a prompt takes, and why
- [ ] You broke something on purpose and watched a test fail

## 7. When you are stuck

- **Contract failure?** The check name tells you which step. `RunContract` in
  `internal/driver/contract.go` is the definition of every check.
- **Unsure what a capability means?** `Capabilities` in `internal/driver/protocol.go`
  documents each field at the point of declaration.
- **Reference implementation:** `cmd/duo-plugin-example` is short, correct by
  construction, and is the suite's positive control.
- **Still ambiguous?** Ask rather than guessing. Every capability is a declaration
  about your agent, and a wrong one fails silently at runtime — that is precisely
  the failure mode this protocol exists to eliminate.