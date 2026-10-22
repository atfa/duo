# Duo

**Two peer coding agents, coordinated in isolated Git worktrees and delivered back to your repository.**

Duo offers three workflows:

- **Fast** (default): Austin implements; Tony independently verifies the exact commit; Duo delivers it.
- **Goal** (`duo --mode goal`): both agents agree on a plan, work independently, cross-review, and sign off before delivery.
- **Step** (`duo --mode step`): both agents agree on an ordered queue; each step uses Fast or Goal policy and lands before the next begins.

Duo owns coordination, verification gates, Git evidence, and safe delivery. Agents work in separate Git worktrees; the result is delivered to the branch from which you started Duo.

## Quick start

```bash
cd /path/to/git/repository
duo                     # Fast
duo --mode goal         # negotiated collaboration
duo --mode step         # incremental queue
```

Duo runs on macOS and Linux and requires Git plus at least one supported agent CLI. It opens the gotui dashboard; enter a task and press `Enter`. Open `/help` (or press `F1`) for the in-app command and keyboard reference.

## Install

**Release v0.13.1** (macOS/Linux, amd64/arm64):

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

Or install from source (Go 1.24+; the script runs the Go test suite):

```bash
./scripts/install.sh
```

Restart running Pi or OpenCode processes after installing their bridges.

## Agent drivers

Built-in drivers are `pi`, `agy`, `opencode`, and `codex`. Choose one for both agents with `--agent`, or choose each separately with `--austin-driver` and `--tony-driver`. `duo plugins` lists available drivers, including plugins discovered in `~/.duo/plugins/` or on `PATH`; the legacy plugin executable names `duo-driver-<name>` and `duo-<name>` are also accepted. `agy` token metrics in the work preview require `sqlite3` on `PATH`.

Agents run with the permissions of the current user. Git worktrees separate their normal repository edits but are **not security sandboxes**. Codex runs with its approval and sandbox checks bypassed for unattended work; use it only on a trusted or otherwise isolated host. See [Security](./SECURITY.md).

## Agent tools and MCP

Agent bridges provide eight Duo tools for peer messages, shared status, plans and steps, verification, gate signatures, escalation, and safe self-restart: `duo_send`, `duo_status`, `duo_set_status`, `duo_set_verification`, `duo_set_plan`, `duo_set_steps`, `duo_escalate`, and `duo_restart_duo`. Tool availability depends on the current workflow. `duo mcp-server` exposes the MCP subset over JSON-RPC: `duo_send`, `duo_status`, `duo_set_status`, `duo_set_verification`, `duo_set_plan`, and `duo_escalate`.

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

The optional LLM observer is disabled by default and advisory only: it cannot message agents, sign gates, or change workflow state. Observer settings are described below; for CLI options, see `duo --help` and `duo mcp-server --help`.

### Optional observer

In the gotui dashboard, use `/observer` to open observer settings. Press `Ctrl+E` at any time to read the observer's progress reports; if no observer model is configured, it opens settings instead. On first run, the settings panel is offered only when the observer has never been configured: no observer section in config, no key file, and no `DUO_OBSERVER*` environment setting. It starts with `http://127.0.0.1:4000/v1` and model `exec`; press `Esc` (**Later**) to save `observer: {"enabled": false}` in the project config and prevent the prompt from appearing again. You can open settings at any time afterward.

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

The settings panel stores a newly entered API key only in `~/.duo/observer.key` as **plaintext** (the key file is not encrypted or stored in a keychain); a newly created `~/.duo` directory uses mode `0700`, and the key file uses mode `0600`. A blank key leaves the saved key unchanged. Keep API keys out of config files; config may contain the environment-variable name in `apiKeyEnv`, never the key itself. Key lookup precedence is `DUO_OBSERVER_API_KEY`, then the environment variable named by `observer.apiKeyEnv`, then the key file. `DUO_OBSERVER`, `DUO_OBSERVER_ENDPOINT`, and `DUO_OBSERVER_MODEL` override the saved enabled, endpoint, and model values, respectively; the settings panel identifies environment-controlled values.

Observer configuration keys are:

| Key | Meaning |
|---|---|
| `enabled` | Enable observer requests; defaults to `false`. |
| `endpoint` | HTTP(S) API base URL, for example `http://127.0.0.1:4000/v1`. Duo appends `/chat/completions`. |
| `model` | Model name sent to the observer API. |
| `apiKeyEnv` | Optional environment-variable name to read the API key from. |
| `systemPrompt` | Observer instructions; an empty or omitted value uses Duo's built-in prompt. |
| `maxEvents` | Maximum events combined into one request; defaults to `20`. |
| `debounceSeconds` | Quiet period before new events update the running report; defaults to `3` seconds. |
| `timeoutSeconds` | Request timeout; defaults to `60` seconds. |
| `maxTokens` | Maximum response tokens; defaults to `300`. |

Observer output appears on Duo's timeline with an `Observer: ` prefix and is also available from the `Ctrl+E` or `/reports` panel. After a three-second quiet period, each report summarizes new events together with the previous report, so the view stays current as work progresses. Missing keys, timeouts, and non-2xx responses are recorded only in the session log and do not interrupt coordination.

## Resume and delivery

`duo resume [session-id]` restores a saved session; `duo sessions` lists sessions. Resume keeps the session's recorded mode; legacy sessions without one resume as Goal, and `DUO_MODE` does not override a resumed session. After approval, Duo delivers by fast-forwarding the original branch only. If delivery is blocked, resolve the cause and retry with `duo apply`. `duo logs` lists transcripts; `duo clean` removes completed session data and worktrees. Each command supports `--help`.

## Documentation

- [gotui dashboard and frontend internals](./docs/gotui.md)
- [architecture and coordination boundaries](./docs/architecture.md)
- [driver plugin protocol](./docs/driver-plugin-protocol.md)
- [plugin development guide](./docs/plugin-development-guide.md)
- [security model and delivery boundary](./SECURITY.md)

For the complete CLI reference, run `duo --help` and `duo <command> --help`.

## Development

Run `make check` for Go tests, vet, and command builds. Pi bridge changes also require `cd pi-extension && bun test`.
