# Writing a Duo Driver Plugin

You are adding support for a coding agent to [Duo](https://github.com/atfa/duo), a
tool that runs two AI coding agents in separate Git worktrees and has them plan,
build, review and deliver a change together. You are going to write a plugin so Duo
can drive your agent.

**You do not need to understand Duo.** You do not need to read Duo's source. You do
not need its Go module. If your agent has a CLI, you can write this plugin in an
afternoon.

The normative reference is the [Driver Plugin Protocol v1](driver-plugin-protocol.md).
This guide is the tutorial. Where they disagree, the protocol document wins.

- [What you are building](#what-you-are-building)
- [Before you start](#before-you-start)
- [Step 1: answer `describe`](#step-1-answer-describe)
- [Step 2: answer `probe`](#step-2-answer-probe)
- [Step 3: answer `prepare`](#step-3-answer-prepare)
- [Step 4: add an Agent Adapter, if your agent has an API](#step-4-add-an-agent-adapter-if-your-agent-has-an-api)
- [Step 5: models and thinking](#step-5-models-and-thinking)
- [Step 6: test it](#step-6-test-it)
- [Step 7: install it](#step-7-install-it)
- [Checklist](#checklist)
- [The mistakes that have actually shipped](#the-mistakes-that-have-actually-shipped)

When using an AI coding agent, have it follow this guide in order and verify each
stage before moving on. In particular, do not skip the capability contract, state
round-trip, or real-binary checks below.

## What you are building

Duo runs two agents, Austin and Tony, each in its own worktree, and has them
collaborate: Austin plans, both build, they review each other, and the agreed result
is merged and delivered back to your repository.

Duo does not know anything about coding agents. It has no idea what a "model" is
for your agent, whether your agent can be resumed, or what flags it takes. **Your
plugin tells it.** That is the entire job.

Concretely, your plugin is one executable that answers questions:

| question | why Duo asks it |
|---|---|
| Who are you, and what can you do? | so Duo knows how to treat your agent |
| Can I run your agent right now? | so Duo can say "install the CLI" instead of failing |
| What command line should I run? | so Duo never has to know your flags |
| What conversation was I in? | so a resumed session continues instead of restarting |
| What models can I choose from? | so the model picker works |
| What reasoning levels exist? | so the thinking picker works |

Duo starts your plugin as a background process and talks to it over stdin/stdout.
Separately, your plugin may observe the running agent and report what it is doing —
that is your choice, and the most common reason a plugin is worth writing well.

## Before you start

You need three things, and nothing else:

1. **Your agent's CLI.** How to launch it, how to resume a session, how to list
   models, how to set a reasoning level.
2. **A way to observe it**, if it has an extension API, a plugin API, a log file, or
   anything else you can read. This is optional.
3. **About an hour per feature.**

You do not need Duo's source code. You do need its protocol document, which is the
first link at the top.

### Pick a name

Your driver name is `<name>`, and your executable must be `duo-plugin-<name>`.
`codex`, `claude`, `qoder` are all fine. `duo` and `duo-mcp-server` are reserved.

### Pick a language

Any language. The wire format is newline-delimited JSON on stdin/stdout, so Python,
TypeScript, Rust and Go are all equally viable. If you write Go, the reference
implementation below is directly usable and there is an SDK in Duo's repository at
`internal/driver` plus a runnable example at `cmd/duo-plugin-example`. If you write
something else, this guide plus the protocol document is all you need.

## Step 1: answer `describe`

This is the most important method and the one to get right first. Duo reads nothing
else about your agent, and it refuses to start a session whose manifest it does not
understand.

Start here, and add the rest as you implement it:

```json
{
  "protocol": 1,
  "name": "example",
  "version": "1.0.0",
  "description": "Example coding agent",
  "agent": {"cli": "example-agent", "defaultCommand": "example-agent"},
  "defaultModel": "",
  "modelReference": "qualified",
  "capabilities": {
    "resume": "client",
    "models": false,
    "thinking": false,
    "liveModelSwitch": false,
    "liveThinkingSwitch": false,
    "activity": false,
    "contextUsage": false,
    "tokenRate": false,
    "bridge": "none",
    "mcp": false,
    "ptyFallback": true,
    "liveSteering": false,
    "selfReports": false
  }
}
```

Every field is described in the [protocol
document](driver-plugin-protocol.md#the-manifest). Three decisions to make now:

**`resume`** — who owns the conversation identity?

- `client` — your agent accepts a session id you choose. Then mint one on the first
  run and replay it. This is the easy case.
- `server` — your agent assigns the id itself and rejects one it never issued. Then
  send nothing until you have learned a real id.
- `none` — no resume at all. The user loses their conversation on restart. Use this
  only if you genuinely have no option.

**`modelReference`** — does your agent accept `provider/id`, or only a bare id?
Declare `qualified` or `bare`. If you get this backwards the model picker shows names
your agent will reject.

**`defaultModel`** — return `""` unless you have a real reason. An empty value means
your agent resolves its own default, which is what a human wants. A guessed id makes
most agents abort with "Model not found".

Then read the capability table and apply **the one rule**:

> Every capability must change something Duo does. If Duo never branches on it, it is
> descriptive metadata, not a capability, and declaring it is a lie that will be
> believed.

Leave a capability `false` until the corresponding code works. A false capability
costs the user a feature; a false *positive* capability costs them a feature that
appears to exist and does nothing.

## Step 2: answer `probe`

Your agent CLI might not be installed. That is not an error — it is information.

```json
{"available": true, "agentPath": "/usr/local/bin/example-agent"}
```

or

```json
{"available": false, "reason": "the example-agent CLI is not on PATH"}
```

The reason is required and is shown to the user verbatim. "Command not found" with no
explanation is the failure mode you are avoiding.

Do not return an error here. An error makes Duo report a broken plugin when the truth
is a missing dependency.

## Step 3: answer `prepare`

This is where all your agent's specifics live, and where the rest of your plugin's
value is. Duo asks for a command line; you build one.

```json
{
  "command": "example-agent --model 'acme/model' --session-id \"$EXAMPLE_SESSION\"",
  "env": {"EXAMPLE_SESSION": "abc-123"},
  "state": {"sessionId": "abc-123"},
  "sessionIdentity": "abc-123",
  "cleanup": ["/tmp/example-austin.log"],
  "notices": []
}
```

### The command line

Duo runs your `command` as `sh -lc "exec <command>"` in a pseudo-terminal. So it is a
**shell command line**, not an argv array.

Three rules, each of which has cost somebody a session:

1. **Quote for the shell, not for Go or Python.** Use single quotes, or better, put
   the value in `env` and reference `"$VAR"`. A model id containing `$` or a
   backtick will otherwise be expanded or executed.

2. **An unset value adds no flag at all.** If no model is selected, do not emit
   `--model ''`. That is not "no flag"; it is a flag whose argument is the empty
   string, and most CLIs reject it.

3. **Respect the operator's `baseCommand`.** If they configured
   `example-agent --config /tmp/mine.json`, that is deliberate. Add to it; do not
   replace it.

Start with the minimum that works, then add:

```go
func (p plugin) Prepare(req driver.LaunchRequest) (*driver.LaunchPlan, error) {
    var prior state
    if len(req.State) > 0 {
        if err := json.Unmarshal(req.State, &prior); err != nil {
            // Report it. Silently treating unreadable state as a first run turns a
            // resume into a new conversation that looks like it worked.
            return nil, &driver.RPCError{Code: driver.CodeInvalid, Message: err.Error()}
        }
    }

    base := strings.TrimSpace(req.BaseCommand)
    if base == "" {
        base = "example-agent" // your agent.defaultCommand
    }

    // ResumeClient: mint once, replay forever.
    id := prior.SessionID
    if id == "" {
        id = newID()
    }

    command := base
    if model := strings.TrimSpace(req.Model); model != "" && !hasFlag(command, "--model") {
        command += " --model " + shellQuote(model)
    }
    if id != "" && !hasFlag(command, sessionFlags) {
        command += ` --session-id "$EXAMPLE_SESSION"`
    }

    raw, err := json.Marshal(state{SessionID: id})
    if err != nil {
        return nil, err
    }
    return &driver.LaunchPlan{
        Command:         command,
        Env:             map[string]string{"EXAMPLE_SESSION": id},
        State:           raw,
        SessionIdentity: id,
    }, nil
}
```

Match flags **exactly**, never by substring. A test for `-c` as a substring also
matches `--config`, and an agent that silently loses its `--config` is a bug you will
chase for a day.

### Resume, and the one that bites

`state` is yours. Duo stores the JSON you return and hands it back verbatim. It never
reads it, never migrates it, never changes its schema. Put whatever you need in it.

**One compatibility requirement.** Sessions written by Duo 0.9.0 migrate to
`{"sessionId":"…"}` under the one word every driver shares, because Duo does not know
your agent's vocabulary. Accept `sessionId` as the identity, in addition to whatever
spelling you prefer:

```go
type state struct {
    ConversationID string `json:"conversationId,omitempty"` // yours, and what you write
    SessionID      string `json:"sessionId,omitempty"`      // read-only, for compatibility
}

func (s state) conversation() string {
    if s.ConversationID != "" {
        return s.ConversationID
    }
    return s.SessionID
}
```

If you declare `resume: server`, this is the mistake that makes your driver look
broken while appearing to work: an agent that rejects an unissued id opens a *new*
conversation, you persist the rejected id, you replay it next launch, and the agent
starts fresh every single time. Nothing reports an error. The symptom is "it never
resumes".

So: send **no** identity until you have a real one.

And once you have one, you have to be able to *report* it, because `prepare` has
already returned by the time your Agent Adapter learns anything. Declare `resume:
server` and you must implement `state`:

```go
type Plugin struct {
    mu          sync.Mutex
    conversation string // learned from the Agent Adapter, after prepare
}

func (p *Plugin) State() (json.RawMessage, error) {
    p.mu.Lock()
    defer p.mu.Unlock()
    return json.Marshal(state{ConversationID: p.conversation})
}
```

Keep it in memory, set it when the Adapter reports, and return whatever you know
now. Prefer it over the stored blob in `prepare` too, so a restart inside one
session resumes instead of forking. `duo plugin test` fails a driver that declares
`resume: server` and does not answer `state`.

### cleanup and notices

`cleanup` lists paths Duo deletes before your next launch. Use it for anything your
agent writes that a stale value could be misread from — a log file, a reported session
id. This is the generic replacement for hard-coded paths.

`notices` are operator-facing diagnostics discovered *while preparing this launch*.
They belong here rather than in `describe` because they are a property of the
machine right now: a helper tool may be installed between the two calls, and a static
string would be a claim Duo cannot keep true. Both shipped drivers report a missing
helper this way.

## Step 4: add an Agent Adapter, if your agent has an API

This is optional and it is where the difference between "works" and "pleasant to use"
is. Without an adapter, the user sees a terminal and Duo can only write to it. With
one, Duo shows what the agent is doing, injects prompts mid-turn, and can switch
models without a restart.

Three levels, pick what your agent supports:

| your agent has | adapter | declare |
|---|---|---|
| nothing readable | none. `ptyFallback: true` covers you. | `bridge: "none"` |
| a log or transcript file | watch it, and report activity | `bridge: "plugin"` |
| an extension or plugin API running inside it | connect from inside the agent | `bridge: "agent"` |

The [Duo bridge protocol](duo-bridge-protocol.md) is the whole reference. If your
agent has a plugin API — and most modern coding agents do — the shape is the same as
both shipped adapters: read `DUO_ACTIVE` (be completely inert without it), read the
bridge address from `DUO_HOST`/`DUO_PORT`/`DUO_SESSION`/`DUO_TOKEN`, connect, send
`hello`, then report `activity` as things happen and deliver inbound prompts.

**The one-endpoint rule.** Duo keeps exactly one bridge connection per agent, and a
second connection replaces and closes the first. So a driver has exactly one
endpoint: inside the agent, or the plugin process. Never both, or they will evict
each other in a loop and the agent will appear to connect and drop repeatedly.

**What each capability buys you**, and the failure if you claim it wrongly:

| declare | effect | if untrue |
|---|---|---|
| `liveSteering: true` | peer messages arrive mid-turn | they queue until the turn ends |
| `liveModelSwitch: true` | a new model applies without a restart | the model silently does not change |
| `liveThinkingSwitch: true` | same for reasoning effort | same |
| `selfReports: true` | Duo trusts your adapter to announce itself | the agent appears missing when it is working |
| `activity: true` | your adapter reports what the agent is doing | Duo shows nothing but a terminal |
| `contextUsage`, `tokenRate` | Duo shows those figures | — |

`liveSteering` and the two `live*Switch` flags are the ones that cause silent
failures. Claim them only once you handle the corresponding message.

## Step 5: models and thinking

Once `describe` says `models: true`, Duo calls your `models` method and shows a
picker.

```json
{"models": [
  {"provider":"acme","id":"large","displayName":"Acme Large","contextWindow":200000,"thinking":true,"images":true},
  {"provider":"acme","id":"small","contextWindow":128000}
]}
```

This is where the code Duo used to own comes from. Ask your agent for its catalog,
parse whatever it prints, and return this shape. An empty catalog is an error, not an
empty picker — the user needs to be told the CLI failed rather than shown a blank
list.

Same for `thinking`:

```json
{"levels": ["off", "low", "medium", "high"], "default": "medium"}
```

Return the levels your agent actually has, in escalation order.

## Step 6: test it

Three levels, cheapest first. All three have caught real bugs.

**Test the methods directly.** Fast, and catches most logic errors. If your language
has a test framework, this is ninety percent of the value.

```go
func TestFirstRunSendsNoIdentity(t *testing.T) {
    plan, err := plugin{}.Prepare(driver.LaunchRequest{BaseCommand: "example-agent"})
    if err != nil {
        t.Fatal(err)
    }
    if strings.Contains(plan.Command, "--session-id") {
        t.Errorf("a first run was handed an identity the agent never saw: %q", plan.Command)
    }
}

func TestResumeReplaysTheStoredIdentity(t *testing.T) {
    first, _ := plugin{}.Prepare(driver.LaunchRequest{BaseCommand: "example-agent"})
    again, _ := plugin{}.Prepare(driver.LaunchRequest{BaseCommand: "example-agent", State: first.State})
    // The second launch must reattach, not start over.
}
```

**Test the manifest against your implementations.** The cheapest possible guard, and
worth having because the failure is invisible:

```go
func TestDeclaredCapabilitiesAreImplemented(t *testing.T) {
    var h driver.Handler = plugin{}
    man, _ := h.Describe()
    if man.Capabilities.Models {
        if _, ok := h.(driver.ModelLister); !ok {
            t.Error("capabilities.models is declared but the models method is not implemented")
        }
    }
    if man.Capabilities.Thinking {
        if _, ok := h.(driver.ThinkingProvider); !ok {
            t.Error("capabilities.thinking is declared but the thinking method is not implemented")
        }
    }
}
```

**Test the built binary over its real pipes.** This is the only level that can catch a
disagreement about the wire format, and it is the level at which two of Duo's own
plugins were found broken. Build the executable, run it, write JSON to its stdin, read
JSON from its stdout:

```go
func TestBinarySpeaksTheProtocol(t *testing.T) {
    bin := build(t)                            // your executable
    c, err := driver.Start(ctx, "example", bin, nil, nil)
    if err != nil {
        t.Fatal(err)
    }
    defer c.Close()

    man, err := c.Describe(ctx)                // must not error
    if err != nil {
        t.Fatal(err)
    }
    // assert your capabilities survived the JSON round trip

    plan, err := c.Prepare(ctx, driver.LaunchRequest{BaseCommand: "example-agent"})
    if err != nil {
        t.Fatal(err)
    }
    if plan.Command == "" {
        t.Error("prepare returned no command")
    }
}
```

You can also do this by hand in one line, and it is worth doing before anything else:

```sh
echo '{"protocol":1,"id":1,"method":"describe"}' | ./duo-plugin-example
```

**Then run it for real.** In a scratch repository:

```sh
duo plugins            # your plugin should be listed
duo --agent example    # start a session
```

Watch the session log. Start a session, stop it, `duo --resume`, and confirm your
agent continued the same conversation. Resume is the feature users notice last and
miss most.

## Step 7: install it

```sh
mkdir -p ~/.duo/plugins
cp ./duo-plugin-example ~/.duo/plugins/
duo plugins     # listed, with the path that will actually run
```

`~/.duo/plugins` takes priority over `$PATH`, so you can develop a plugin in place
without uninstalling anything.

Then hand it to someone: a plugin is one executable plus, if your agent has an
extension API, whatever that extension needs installed. That is the entire
distribution story.

## Checklist

- [ ] Executable is named `duo-plugin-<name>`; not `duo`, `duo-mcp-server` or `duo-plugin-test`
- [ ] `describe` returns `protocol: 1`, a name, and a `resume` mode
- [ ] Every declared capability has working code behind it
- [ ] `modelReference` matches whether your agent accepts a provider prefix
- [ ] `probe` reports unavailability with a reason instead of erroring
- [ ] `prepare` returns a shell command line, quoted, preserving `baseCommand`
- [ ] An unset model or effort adds no flag at all
- [ ] `prepare` accepts `sessionId` in state, for Duo 0.9 compatibility
- [ ] `resume: server` sends no identity until a real one is known
- [ ] `resume: server` implements `state` and reports the learned identity
- [ ] A restart replays the stored state and reattaches
- [ ] Corrupt state is reported, not treated as a first run
- [ ] `cleanup` lists anything a stale value could be misread from
- [ ] Adapter is inert unless `DUO_ACTIVE=1`
- [ ] Exactly one bridge endpoint, inside the agent or the plugin process
- [ ] The built binary has been driven over its real pipes
- [ ] `duo --agent <name>` starts, resumes, and delivers

## The mistakes that have actually shipped

Every one of these is in Duo's own history, and every one was silent.

Before implementation, establish a baseline: run the reference plugin contract,
build `cmd/duo-plugin-example`, and run `duo plugin test` against that binary. Find
the `Handler` and manifest types by name in `internal/driver`; line numbers drift.
For a new driver, keep the sequence small: `describe`, `probe`, `prepare`, learned
state if needed, optional model/thinking methods, then an Agent Adapter only when
the agent exposes a usable API. Run `duo plugin test <binary> -v` and inspect its
individual checks.

One environment-dependent trap deserves a separate rule: a model-catalog test must
distinguish a broken plugin from a CLI that is absent or unauthenticated. Probe the
CLI directly and skip only when that CLI cannot enumerate models; never use the
plugin's own failing `Models()` result to justify skipping its test.

For a release-quality check, run `gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test ./... -count=1`, then build and contract-test the plugin binary. For a
non-trivial state or catalog behavior, deliberately break that behavior once and
confirm the relevant check fails.

**Declaring a capability whose method is missing.** A method with a subtly different
signature compiles, passes every unit test, and answers `unsupported` at runtime. The
model picker came up empty with nothing reporting an error. Declare the capability
only when the method is implemented, and assert it in a test.

**Emitting a flag for an unset value.** `--model ''` looks like a harmless no-op and
is a flag whose argument is the empty string. Most CLIs reject it.

**Replaying an identity the agent never issued.** Produces a new conversation on every
launch, with no error anywhere. This is what `resume: server` exists to prevent.

**Learning an identity with nowhere to put it.** `prepare` runs before the agent
starts, so a `resume: server` driver that does not implement `state` cannot persist
the id it learns afterwards. Every launch opens a new conversation, and every other
check still passes. The contract suite now fails this driver.

**Matching flags by substring.** `-c` matches `--config`. The agent silently loses
its configuration, or its session, depending on which flag you were guarding.

**Quoting for your language instead of for the shell.** `"$USER"` expands. `` `id` ``
runs.

**Reporting a static diagnostic from `describe`.** True when it was written, false by
the time it is read. Report it from `prepare`.

**Two bridge endpoints.** They evict each other forever and the agent appears to
connect and drop.

**Swallowing an unreadable resume state.** A resume turns into a fresh conversation
that looks like it worked.

---

Next: [Driver Plugin Protocol v1](driver-plugin-protocol.md) for the full reference ·
[Duo bridge protocol](duo-bridge-protocol.md) for the Agent Adapter
