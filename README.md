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

Duo runs on macOS, Linux and Windows (Windows 10 1809+ or newer, which is where the ConPTY agent terminals come from) and requires Git plus at least one supported agent CLI. It opens the gotui dashboard; enter a task and press `Enter`. Open `/help` (or press `F1`) for the in-app command and keyboard reference.

On a first run `duo` checks that it can start before it opens the dashboard, and asks before it changes anything. In a directory that is not a Git repository it offers to run `git init`; in a repository with no commit it offers an initial commit; with uncommitted work it offers to commit or stash it. If `~/.duo/config.json` does not exist it offers to create one with English, GOAL mode and the TIMELINE view. With no terminal — a script, CI, `duo run` — it asks nothing and reports the same errors it always did. When none of the four agent CLIs is on `PATH`, Duo refuses to start and names what to install, in a headless round as well.

## Install

**Latest published release v0.14.5** (macOS/Linux/Windows, amd64, plus arm64 on macOS/Linux):

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

On Windows, download `duo_windows_amd64.tar.gz` from the release with `tar -xzf` or any archive tool and put `duo.exe` and `duo-plugin-*.exe` on `PATH` (or in `%USERPROFILE%\.duo\plugins`). The shell installer is a POSIX script and does not run there; `duo upgrade` does, and it replaces the running `duo.exe` in place.

`duo upgrade` installs the same release without the script: it downloads this platform's archive, verifies it against the release's published `SHA256SUMS`, and refuses an archive the release does not account for. It also reads the release tag's session schema before replacing anything, so a new binary that could not read your sessions is refused instead of installed. `duo upgrade --dry-run` reports what it would install; the running process keeps the old binary either way, and the new one takes effect on the next start.

Or install from source (Go 1.24+; the script runs the Go test suite):

```bash
./scripts/install.sh
```

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

`tuiMode` selects the view a session opens on: `timeline` (the default, one conversation) or `split` (Austin and Tony side by side). `F2` toggles it while running, and the interface records that choice in the repository's `.duo/config.json`. A `tuiMode` in `~/.duo/config.json` is only the default for repositories that have not saved a view of their own, so a repository's saved choice always wins. Any other value is a configuration error rather than a silent fall back to the default.

### Optional observer

In the gotui dashboard, use `/observer` to open observer settings. Press `Ctrl+E` (or `/reports`) when you want to know what the agents are doing: it opens the status report and asks the observer for a fresh one. If no observer model is configured, `Ctrl+E` opens settings instead. The first-run terminal guide asks about the observer before the dashboard opens: answering there saves the settings (or, with **Later**, `observer: {"enabled": false}`) in the project config, so the dashboard does not ask a second time; an observer that was ever configured - an observer section in either config file, a key file, or a `DUO_OBSERVER*` variable - is not asked about at all. Without a terminal the panel is offered on first run only when the observer has never been configured: no observer section in config, no key file, and no `DUO_OBSERVER*` environment setting. It starts with `http://127.0.0.1:4000/v1` and model `exec`; edit the fields directly, then press `Enter` to save or `Esc` (**Later**) to save `observer: {"enabled": false}` in the project config and prevent the prompt from appearing again. You can open settings at any time afterward.

In the settings panel, use `Tab`/`Shift+Tab` to move between fields, `Space` (or `y`/`n`) to toggle Enabled, and `Enter` to save; `Esc` closes without saving. In the first-run prompt, `Esc` saves **Later** instead. `Ctrl+U` clears the current field. Leaving the API key blank keeps the saved key. In the reports panel, `R` asks for a fresh report, `?` asks the observer your own question, `↑`/`↓` scroll and `Esc` closes it.

When an observer model is configured, the first task you submit to a session asks it to give that session a short title (in the task's language, a few words). Session lists — the gotui `/sessions` picker, `duo sessions`, and `duo resume`'s candidate list — show the title when one exists and the session id otherwise. `duo resume <id>` still selects by id; a session is named once, and if the observer cannot produce a title the session keeps its id.

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
| `maxTokens` | Maximum response tokens; defaults to `1000`. |

The observer runs only when you ask for it: it has no timer and writes nothing to the timeline. `Ctrl+E` or `/reports` sends one request built from the human's original request for the round (remembered separately, so it is not lost to the event window), the last `maxEvents` messages (newest last, bounded to roughly 6,000 characters), a live snapshot taken when the request runs — the workflow mode and phase, the shared plan text, the step queue with which steps have landed, each agent's gate sign-off with its note or evidence, the FAST verification verdict, each worktree's short head with commits ahead and dirty state, the files each branch changed with the ones both touched, the session's token and cost totals, the test command, and for each agent whether it is busy or idle, the tool it is running with its arguments and elapsed time, the text it is currently writing, the tools it just finished, and its context usage — and the previous report, which is only continuity context.

Each report has four sections, in the interface language: progress (the task in one sentence, then where the work stands in its decomposition — how many steps, how many done, which is current, what remains — taken only from the live plan and step queue), what the agents just finished (at most three bullets, newest first, never an accumulating history), what each agent is doing right now (taken only from the live snapshot, so a finished tool is never described as running, and an idle agent is reported with what it finished last), and the next steps the agents explicitly stated. A final `Needs your decision:` line appears only when a human decision actually blocks them, and an optional `Watch out:` section appears only when the live snapshot or the recent log shows a concrete concern — such as work claimed without a commit to back it, the same tool failing repeatedly, both agents editing the same files, both idle mid-phase, or a gate deadlock — with at most two bullets, each citing that evidence. Reports appear only in the `Ctrl+E` panel, never on the timeline, and the panel keeps the last three. While the model answers, the panel shows the request's elapsed time; `R` asks again, and `?` asks your own question: type it, press `Enter`, and the observer answers it directly in at most eight short lines instead of writing a full report; your question is shown above its answer in the same history. Empty input or `Esc` asks nothing.

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

`duo resume [session-id]` restores a saved session; `duo sessions` lists sessions and the snapshot schema each one was read with (`2→3` means an older session was upgraded). Resume keeps the session's recorded mode; legacy sessions without one resume as Goal, and `DUO_MODE` does not override a resumed session. After approval, Duo delivers by fast-forwarding the original branch only. If delivery is blocked, resolve the cause and retry with `duo apply`; on a repository whose base branch is protected, `duo apply --branch <name>` puts the verified result on a new branch instead of moving yours (`--keep` preserves the Duo worktrees); `--push` also updates the branch's upstream, refusing before it moves anything if the remote has moved; `--pr` does the same and then opens the pull request with `gh`, or prints the same body when `gh` is not installed. `duo run -m fast|goal|step "task" [--json] [--timeout 90m]` runs one round without a terminal, which is what CI and batch scripts need: it submits the task, waits for DONE, and exits 0 when the artifact was delivered, 3 when it was not, and 124 when the timeout stopped the round. `duo status [session-id]` prints the authoritative session state — phase, mode, plan version, queue, signatures and each agent's HEAD, dirtiness and ahead count — and `duo status --json` prints the same facts as JSON for scripts. `duo logs` lists transcripts; `duo export <session-id>` writes one session's plan, queue, signatures, verification, delivery record and diffstat as Markdown on stdout, so a finished round can be archived or reviewed offline (`--repo <path>` when the id exists in more than one repository); `duo clean` removes completed session data and worktrees. Each command supports `--help`.

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

### xduo：多实例管理

Linux 上安装 tmux，并在仓库中执行 `go install ./cmd/duo ./cmd/xduo`，确保 Go 的 bin 目录在 PATH 中。运行 `xduo` 进入独立的 tmux 管理会话；也可以通过 `xduo --duo /path/to/duo` 或 `XDUO_DUO_PATH` 指定 Duo 可执行文件。已有 tmux 客户端会切换到管理会话，不会嵌套 tmux。

使用方向键选择实例，`n` 输入 Git 项目目录创建实例，Enter 切换到原始 Duo 界面，`x` 关闭实例，`r` 刷新，`q` 退出面板。返回面板时使用 tmux 前缀（默认 Ctrl-b）然后按 `s`，选择 `xduo-manager`。关闭运行中的任务需要确认；管理器先发送 Duo 的正常退出快捷键，等待后才关闭仍存活的会话。退出面板不会关闭 Duo 实例。

注册表和各实例的状态、Remote 配对文件位于 `~/.duo/xduo/`，关闭实例后数据保留。重新启动 xduo 会恢复注册表。状态过期时显示 Unknown，任务阶段和 tmux 存活状态分开显示。每个实例独立配置 Remote；原有 Duo 不设置环境变量时仍使用默认配对文件。高级用法：`DUO_REMOTE_PAIRING_FILE` 指定配对文件，`XDUO_STATUS_FILE` 指定实时状态 JSON 文件。

Duo 和 xduo 的选择项、确认按钮和弹窗关闭按钮支持鼠标点击。终端需要支持鼠标事件；在 tmux 中如点击无效，可在 tmux 前缀后的命令行执行 `set -g mouse on`（或在 tmux 配置中启用）。该设置由用户决定，程序不会自动更改。需要使用终端原生文本选择时，通常可按住 Shift 拖选。
