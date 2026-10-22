# Duo

**两个平级 Coding Agent 在隔离的 Git 工作树中协作，成果交付回你的仓库。**

Duo 提供三种工作流：

- **Fast**（默认）：Austin 负责实现；Tony 独立验证确切的 commit；Duo 负责交付。
- **Goal**（`duo --mode goal`）：双方先就计划达成一致，各自独立工作，交叉审查并签字后交付。
- **Step**（`duo --mode step`）：双方先商定有序队列；每个条目按 Fast 或 Goal 策略执行，落地后才进入下一步。

Duo 负责协调、验证关卡、Git 证据与安全交付。Agent 在各自独立的 Git 工作树中工作，结果交付回你启动 Duo 时所在的分支。

## 快速开始

```bash
cd /path/to/git/repository
duo                     # Fast
duo --mode goal         # 协商式协作
duo --mode step         # 逐步推进的队列
```

Duo 运行于 macOS 与 Linux，需要 Git 以及至少一种受支持的 Agent CLI。启动后会打开 gotui 仪表盘；输入任务并按 `Enter`。应用内的命令与快捷键参考请打开 `/help`（或按 `F1`）。

## 安装

**发布版 v0.13.1**（macOS/Linux，amd64/arm64）：

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

或从源码安装（需要 Go 1.24+；脚本会先运行 Go 测试）：

```bash
./scripts/install.sh
```

安装 bridge 后请重启正在运行的 Pi 或 OpenCode 进程。

## Agent drivers

内置 driver 为 `pi`、`agy`、`opencode` 和 `codex`。用 `--agent` 为双方选择同一个 driver，或用 `--austin-driver` 与 `--tony-driver` 分别指定。`duo plugins` 会列出可用的 driver，包括在 `~/.duo/plugins/` 或 `PATH` 中发现的插件；旧的插件可执行文件名 `duo-driver-<name>` 与 `duo-<name>` 同样受支持。工作预览中的 `agy` token 统计需要 `PATH` 上有 `sqlite3`。

Agent 以当前用户的权限运行。Git 工作树只隔离它们对仓库的常规修改，**不是安全沙箱**。Codex 会跳过审批与沙箱检查以便无人值守工作；只应在可信或另行隔离的主机上使用。参见 [Security](./SECURITY.md)。

## Agent 工具与 MCP

Agent bridge 提供八个 Duo 工具，用于同伴消息、共享状态、计划与步骤、验证、关卡签字、模式升级（escalate）与安全自重启：`duo_send`、`duo_status`、`duo_set_status`、`duo_set_verification`、`duo_set_plan`、`duo_set_steps`、`duo_escalate` 和 `duo_restart_duo`。工具的可用性取决于当前工作流。`duo mcp-server` 通过 JSON-RPC 暴露 MCP 子集：`duo_send`、`duo_status`、`duo_set_status`、`duo_set_verification`、`duo_set_plan` 和 `duo_escalate`。

## 配置

设置可存放在全局的 `~/.duo/config.json`，或按仓库存放在 `.duo/config.json`（也可用 `.duo.json`）。命令行选项优先于环境变量与文件默认值。常用环境变量：

| 变量 | 用途 |
|---|---|
| `DUO_MODE` | 新会话的默认模式（`fast`、`goal`、`step`）。 |
| `DUO_TEST_COMMAND` | 接受验证前运行的测试命令。 |
| `DUO_REPO` | 启动时使用的仓库或子目录。 |
| `DUO_DRIVER` | 双方共用的默认 driver。 |
| `DUO_WORKTREE_ROOT` | 隔离的 Agent 工作树所在位置。 |
| `DUO_HARNESS` | 启用或禁用空闲/停滞看门狗。 |
| `DUO_OBSERVER` | 启用或禁用可选的观察者。 |
| `DUO_OBSERVER_ENDPOINT` / `DUO_OBSERVER_MODEL` | 观察者的 API 端点与模型。 |
| `DUO_OBSERVER_API_KEY` | 观察者 API key；key 只放在环境里，不要写进配置。 |

可选的 LLM 观察者默认关闭且仅作建议：它不能给 Agent 发消息、不能签署关卡，也不能改变工作流状态。它会调用兼容 OpenAI Chat Completions 的 API。观察器设置见下文；CLI 选项见 `duo --help` 与 `duo mcp-server --help`。

### 可选观察器

在 gotui 仪表盘中输入 `/observer` 打开观察器设置；按 `Ctrl+E` 随时查看观察器的进度报告。如果还没有配置观察器模型，按 `Ctrl+E` 会自动打开设置。仅当观察器从未配置时（配置中没有 observer 段、没有密钥文件、也没有 `DUO_OBSERVER*` 环境设置），首次运行才会显示设置面板。默认地址为 `http://127.0.0.1:4000/v1`，模型为 `exec`；可直接编辑字段，按 `Enter` 保存，或按 `Esc`（**稍后**）将 `observer: {"enabled": false}` 保存到项目配置中，以后不再提示。此后仍可随时用 `/observer` 打开设置。

在设置面板中，按 `Tab`/`Shift+Tab` 切换字段，按 `Space`（或 `y`/`n`）切换启用状态，按 `Enter` 保存；按 `Esc` 关闭且不保存（首次提示中按 `Esc` 则保存为“稍后”）。按 `Ctrl+U` 清空当前字段。API key 留空会保留已保存的密钥。在报告面板中，按 `↑`/`↓` 滚动，按 `Esc` 关闭。

非敏感设置可存放在 `~/.duo/config.json` 或仓库的 `.duo/config.json` 的 `observer` 段中。项目中的 `observer` 段会整体替换全局观察器段，不会与全局段逐字段合并。例如：

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

设置面板输入的新 API key 只会以**明文**保存到 `~/.duo/observer.key`（不会加密，也不使用系统密钥链）；新建的 `~/.duo` 目录权限为 `0700`，密钥文件权限为 `0600`。密钥留空会保留已保存的值。密钥输入框会明文显示你正在输入的字符，以便发现输错；已保存的密钥不会回填到该输入框。不要将 API key 写入配置文件；配置中可以保存 `apiKeyEnv` 环境变量名，但绝不保存 key 本身。密钥查找优先级为 `DUO_OBSERVER_API_KEY`、`observer.apiKeyEnv` 指定的环境变量、密钥文件。`DUO_OBSERVER`、`DUO_OBSERVER_ENDPOINT`、`DUO_OBSERVER_MODEL` 分别覆盖已保存的启用状态、端点和模型；设置面板会标明受环境变量控制的值。

观察器配置键如下：

| 配置键 | 含义 |
|---|---|
| `enabled` | 是否启用观察器请求；默认 `false`。 |
| `endpoint` | 兼容 OpenAI Chat Completions 的 HTTP(S) API 基础地址，例如 `http://127.0.0.1:4000/v1`；Duo 会自动追加 `/chat/completions`，配置 URL 时不要包含此路径。 |
| `model` | 发送给观察器 API 的模型名。 |
| `apiKeyEnv` | 可选的 API key 环境变量名。 |
| `systemPrompt` | 观察器指令；留空或省略时使用 Duo 内置提示词。 |
| `maxEvents` | 合并到一次请求中的事件上限；默认 `20`。 |
| `debounceSeconds` | 新事件后更新进度总结前等待的静默时间；默认 `3` 秒。 |
| `timeoutSeconds` | 请求超时；默认 `60` 秒。 |
| `maxTokens` | 响应 token 上限；默认 `300`。 |

观察器会在事件静默三秒后结合上一份报告检查新增事件；仅当有值得展示的新信息时才会发出更新。报告带有 `Observer: ` 前缀，显示在 Duo 时间线上，也可按 `Ctrl+E` 或输入 `/reports` 在报告窗口中查看。报告语言跟随界面语言：界面为中文时观察器会用中文撰写报告。缺少密钥、请求超时或 HTTP 状态码非 2xx 时，只会写入 session 日志，不会影响协调流程。

## 恢复与交付

`duo resume [session-id]` 恢复已保存的会话；`duo sessions` 列出会话。恢复会保留会话记录的模式；没有记录模式的旧会话按 Goal 恢复，且 `DUO_MODE` 不会覆盖已恢复的会话。批准后，Duo 只通过快进（fast-forward）原始分支来交付。若交付受阻，排除原因后用 `duo apply` 重试。`duo logs` 列出会话记录；`duo clean` 删除已完成的会话数据与工作树。每个命令都支持 `--help`。

## 文档

- [gotui 仪表盘与前端内部机制](./docs/gotui.md)
- [架构与协调边界](./docs/architecture.md)
- [driver 插件协议](./docs/driver-plugin-protocol.md)
- [插件开发指南](./docs/plugin-development-guide.md)
- [安全模型与交付边界](./SECURITY.md)

完整的 CLI 参考请运行 `duo --help` 与 `duo <command> --help`。

## 开发

运行 `make check` 执行 Go 测试、vet 与命令构建。改动 Pi bridge 时还需运行 `cd pi-extension && bun test`。
