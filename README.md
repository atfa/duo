# Duo

**Two peer coding agents, coordinated in isolated Git worktrees and delivered back to your repository.**

Duo offers three workflows:

- **Fast** (default): Austin implements; Tony independently verifies the exact commit; Duo delivers it.
- **Goal** (`duo --mode goal`): both agents agree on a plan, work independently, cross-review, and sign off before delivery.
- **Step** (`duo --mode step`): both agents agree on an ordered queue; each step uses Fast or Goal policy and lands before the next begins.

Duo owns coordination, verification gates, Git evidence, and safe delivery. Agents work in separate Git worktrees; the result is delivered to the branch from which you started Duo.

**Remote control.** Type `/qrcode` and scan it with a phone to follow both agents, send a task, and switch mode, driver or model from anywhere; the relay stores ciphertext only. See [Remote control](#remote-control) and [docs/remote.md](docs/remote.md).

## Quick start

```bash
cd /path/to/git/repository
duo                     # Fast
duo --mode goal         # negotiated collaboration
duo --mode step         # incremental queue
```

Duo runs on macOS and Linux and requires Git plus at least one supported agent CLI. It opens the gotui dashboard; enter a task and press `Enter`. Open `/help` (or press `F1`) for the in-app command and keyboard reference.

## Install

**Latest published release v0.13.2** (macOS/Linux, amd64/arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

Or install from source (Go 1.24+; the script runs the Go test suite):

```bash
./scripts/install.sh
```

v0.14.0, which adds remote control, is on `main` ahead of the next release archive; the source install builds it.

Restart running Pi or OpenCode processes after installing their bridges.

## Agent drivers

Built-in drivers are `pi`, `agy`, `opencode`, and `codex`. Choose one for both agents with `--agent`, or choose each separately with `--austin-driver` and `--tony-driver`. `duo plugins` lists available drivers, including plugins discovered in `~/.duo/plugins/` or on `PATH`; the legacy plugin executable names `duo-driver-<name>` and `duo-<name>` are also accepted. `agy` token metrics in the work preview require `sqlite3` on `PATH`.

Agents run with the permissions of the current user. Git worktrees separate their normal repository edits but are **not security sandboxes**. Codex runs with its approval and sandbox checks bypassed for unattended work; use it only on a trusted or otherwise isolated host. See [Security](./SECURITY.md).

## Agent tools and MCP

Agent bridges provide eight Duo tools for peer messages, shared status, plans and steps, verification, gate signatures, escalation, and safe self-restart: `duo_send`, `duo_status`, `duo_set_status`, `duo_set_verification`, `duo_set_plan`, `duo_set_steps`, `duo_escalate`, and `duo_restart_duo`. Tool availability depends on the current workflow. `duo mcp-server` exposes all eight coordination tools over JSON-RPC: `duo_send`, `duo_status`, `duo_set_status`, `duo_set_verification`, `duo_set_plan`, `duo_set_steps`, `duo_escalate`, and `duo_restart_duo`.

## Configuration

Settings can be stored globally in `~/.duo/config.json` or per repository in `.duo/config.json` (or `.duo.json`). Command-line options take precedence over environment and file defaults. Common environment variables:

| Variable | Purpose |
|---|---|
| `DUO_MODE` | Default mode for new sessions (`fast`, `goal`, `step`). |
| `DUO_TEST_COMMAND` | Test command to run before accepting verification. |
| `DUO_REPO` | Repository or subdirectory to launch against. |
| `DUO_DRIVER` | Default driver for both agents. |
| `DUO_WORKTREE_ROOT` | Location for the isolated agent worktrees. |
| `DUO_HARNESS` | Enable or disable the idle/stall watchdog. |
| `DUO_OBSERVER` | Enable or disable the optional observer. |
| `DUO_OBSERVER_ENDPOINT` / `DUO_OBSERVER_MODEL` | Observer API endpoint and model. |
| `DUO_OBSERVER_API_KEY` | Observer API key; keep the value in the environment, not config. |

The optional LLM observer is disabled by default and advisory only: it cannot message agents, sign gates, or change workflow state. It calls an OpenAI-compatible Chat Completions API. Observer settings are described below; for CLI options, see `duo --help` and `duo mcp-server --help`.

### Optional observer

In the gotui dashboard, use `/observer` to open observer settings. Press `Ctrl+E` (or `/reports`) when you want to know what the agents are doing: it opens the status report and asks the observer for a fresh one. If no observer model is configured, `Ctrl+E` opens settings instead. On first run, the settings panel is offered only when the observer has never been configured: no observer section in config, no key file, and no `DUO_OBSERVER*` environment setting. It starts with `http://127.0.0.1:4000/v1` and model `exec`; edit the fields directly, then press `Enter` to save or `Esc` (**Later**) to save `observer: {"enabled": false}` in the project config and prevent the prompt from appearing again. You can open settings at any time afterward.

In the settings panel, use `Tab`/`Shift+Tab` to move between fields, `Space` (or `y`/`n`) to toggle Enabled, and `Enter` to save; `Esc` closes without saving. In the first-run prompt, `Esc` saves **Later** instead. `Ctrl+U` clears the current field. Leaving the API key blank keeps the saved key. In the reports panel, `R` asks for a fresh report, `↑`/`↓` scroll and `Esc` closes it.

The non-secret settings can be stored in the `observer` section of `~/.duo/config.json` or the repository's `.duo/config.json`. A project `observer` section replaces the global observer section as a whole; its fields are not merged with the global section. For example:

```json
{
  "observer": {
    "enabled": true,
    "endpoint": "http://127.0.0.1:4000/v1",
    "model": "exec",
    "apiKeyEnv": "MY_LLM_API_KEY"
  }
}
```

The settings panel stores a newly entered API key only in `~/.duo/observer.key` as **plaintext** (the key file is not encrypted or stored in a keychain); a newly created `~/.duo` directory uses mode `0700`, and the key file uses mode `0600`. A blank key leaves the saved key unchanged. The key field shows what you type in plaintext so a mistyped key is visible; a saved key is never loaded back into the field. Keep API keys out of config files; config may contain the environment-variable name in `apiKeyEnv`, never the key itself. Key lookup precedence is `DUO_OBSERVER_API_KEY`, then the environment variable named by `observer.apiKeyEnv`, then the key file. `DUO_OBSERVER`, `DUO_OBSERVER_ENDPOINT`, and `DUO_OBSERVER_MODEL` override the saved enabled, endpoint, and model values, respectively; the settings panel identifies environment-controlled values.

Observer configuration keys are:

| Key | Meaning |
|---|---|
| `enabled` | Enable observer requests; defaults to `false`. |
| `endpoint` | HTTP(S) base URL for an OpenAI-compatible Chat Completions API, for example `http://127.0.0.1:4000/v1`. Duo appends `/chat/completions`; do not include that path in the configured URL. |
| `model` | Model name sent to the observer API. |
| `apiKeyEnv` | Optional environment-variable name to read the API key from. |
| `systemPrompt` | Observer instructions; an empty or omitted value uses Duo's built-in prompt. |
| `maxEvents` | Maximum recent messages one report reads; defaults to `40`. |
| `timeoutSeconds` | Request timeout; defaults to `60` seconds. |
| `maxTokens` | Maximum response tokens; defaults to `800`. |

The observer runs only when you ask for it: it has no timer and writes nothing to the timeline. `Ctrl+E` or `/reports` sends one request built from the last `maxEvents` messages (newest last, bounded to roughly 6,000 characters), a live snapshot of both agents taken when the request runs — busy or idle, the tool each is running with its arguments and elapsed time, the text it is currently writing, the tools it just finished, and its context usage — and the previous report, which is only continuity context.

Each report has three sections, in the interface language: what the agents just finished (at most three bullets, newest first, never an accumulating history), what each agent is doing right now (taken only from the live snapshot, so a finished tool is never described as running, and an idle agent is reported with what it finished last), and the next steps the agents explicitly stated. A final `Needs your decision:` line appears only when a human decision actually blocks them. Reports appear only in the `Ctrl+E` panel, never on the timeline, and the panel keeps the last three. While the model answers, the panel shows the request's elapsed time; `R` asks again.

A missing key is explained by the panel itself; timeouts, empty answers, and non-2xx responses are recorded in the session log and shown as diagnostics in the panel, and none of them interrupt coordination. A report cut off by `maxTokens` still appears, with the truncation reported as a diagnostic rather than silently losing its later sections. With no report yet, the panel says whether the observer is disabled, missing a key, or simply has not been asked.

## Remote control

Run Duo, give it a task, then type `/qrcode`. Scan the code with a phone: the
page shows Austin's and Tony's state and recent activity, sends a new task,
starts a fresh session, refreshes the observer's report, and changes what runs —
the workflow mode, either agent's driver, or its model. The relay is a
Cloudflare Worker that stores ciphertext only, so prompts and repository paths
stay on your machine; the pairing secret travels in the QR code's URL fragment,
which a browser never sends to a server.

A pairing lives in `~/.duo/remote.json` (`0600`) for 8 hours and reconnects by
itself, so `/new` keeps the phone connected. Closing the QR panel does not
disconnect the phone: only `/remote off`, the pairing expiring, or Duo exiting
ends it. `/remote` shows the status, `/remote off` revokes it. Configure the relay with a `remote` section in the
config file or `DUO_REMOTE_URL`; the default is `https://remote.xduo.dev`, and
the Worker in `remote/` is yours to deploy. See [docs/remote.md](docs/remote.md).

## Resume and delivery

`duo resume [session-id]` restores a saved session; `duo sessions` lists sessions and the snapshot schema each one was read with (`2→3` means an older session was upgraded). Resume keeps the session's recorded mode; legacy sessions without one resume as Goal, and `DUO_MODE` does not override a resumed session. After approval, Duo delivers by fast-forwarding the original branch only. If delivery is blocked, resolve the cause and retry with `duo apply`; on a repository whose base branch is protected, `duo apply --branch <name>` puts the verified result on a new branch instead of moving yours (`--keep` preserves the Duo worktrees); `--push` also updates the branch's upstream, refusing before it moves anything if the remote has moved; `--pr` does the same and then opens the pull request with `gh`, or prints the same body when `gh` is not installed. `duo run -m fast|goal|step "task" [--json] [--timeout 90m]` runs one round without a terminal, which is what CI and batch scripts need: it submits the task, waits for DONE, and exits 0 when the artifact was delivered, 3 when it was not, and 124 when the timeout stopped the round. `duo status [session-id]` prints the authoritative session state — phase, mode, plan version, queue, signatures and each agent's HEAD, dirtiness and ahead count — and `duo status --json` prints the same facts as JSON for scripts. `duo logs` lists transcripts; `duo clean` removes completed session data and worktrees. Each command supports `--help`.

Resuming never sends anything to the agents: the transcripts and state are restored, both agents start idle, and the automatic idle nudges stay quiet. Read the record first; when you want the agents to continue, run `/resume` in the dashboard. Submitting a task always wakes them, and `/resume` is the only way to wake them without a new task. Fast, Goal, and Step mode behave the same way. A finished (DONE) session has nothing to resume, so submit a task to start a new round instead.

## Documentation

- [gotui dashboard and frontend internals](./docs/gotui.md)
- [architecture and coordination boundaries](./docs/architecture.md)
- [remote control and the phone console](./docs/remote.md)
- [remote control wire protocol](./docs/remote-protocol.md)
- [driver plugin protocol](./docs/driver-plugin-protocol.md)
- [plugin development guide](./docs/plugin-development-guide.md)
- [security model and delivery boundary](./SECURITY.md)

For the complete CLI reference, run `duo --help` and `duo <command> --help`.

## Development

Run `make check` for Go tests, vet, and command builds. Pi bridge changes also require `cd pi-extension && bun test`.
