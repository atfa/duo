# Duo

**两个平级 Coding Agent 在隔离的 Git 工作树中协作，成果交付回你的仓库。**

Duo 提供三种工作流：

- **Fast**（默认）：Austin 负责实现；Tony 独立验证确切的 commit；Duo 负责交付。
- **Goal**（`duo --mode goal`）：双方先就计划达成一致，各自独立工作，交叉审查并签字后交付。
- **Step**（`duo --mode step`）：双方先商定有序队列；每个条目按 Fast 或 Goal 策略执行，落地后才进入下一步。

Duo 负责协调、验证关卡、Git 证据与安全交付。Agent 在各自独立的 Git 工作树中工作，结果交付回你启动 Duo 时所在的分支。

**远程控制。** 输入 `/qrcode` 并用手机扫码，即可随时查看两个 Agent 的状态、下达任务，并切换 mode、driver 或 model；中继只存储密文。见[远程控制](#远程控制)与 [docs/remote.md](docs/remote.md)。

## 快速开始

```bash
cd /path/to/git/repository
duo                     # Fast
duo --mode goal         # 协商式协作
duo --mode step         # 逐步推进的队列
```

Duo 运行于 macOS 与 Linux，需要 Git 以及至少一种受支持的 Agent CLI。启动后会打开 gotui 仪表盘；输入任务并按 `Enter`。应用内的命令与快捷键参考请打开 `/help`（或按 `F1`）。

## 安装

**最新发布版 v0.13.2**（macOS/Linux，amd64/arm64）：

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

或从源码安装（需要 Go 1.24+；脚本会先运行 Go 测试）：

```bash
./scripts/install.sh
```

v0.14.0（新增远程控制）已在 `main` 上，尚未打包为发布归档；用源码安装即可获得。

安装 bridge 后请重启正在运行的 Pi 或 OpenCode 进程。

## Agent drivers

内置 driver 为 `pi`、`agy`、`opencode` 和 `codex`。用 `--agent` 为双方选择同一个 driver，或用 `--austin-driver` 与 `--tony-driver` 分别指定。`duo plugins` 会列出可用的 driver，包括在 `~/.duo/plugins/` 或 `PATH` 中发现的插件；旧的插件可执行文件名 `duo-driver-<name>` 与 `duo-<name>` 同样受支持。工作预览中的 `agy` token 统计需要 `PATH` 上有 `sqlite3`。

Agent 以当前用户的权限运行。Git 工作树只隔离它们对仓库的常规修改，**不是安全沙箱**。Codex 会跳过审批与沙箱检查以便无人值守工作；只应在可信或另行隔离的主机上使用。参见 [Security](./SECURITY.md)。

## Agent 工具与 MCP

Agent bridge 提供八个 Duo 工具，用于同伴消息、共享状态、计划与步骤、验证、关卡签字、模式升级（escalate）与安全自重启：`duo_send`、`duo_status`、`duo_set_status`、`duo_set_verification`、`duo_set_plan`、`duo_set_steps`、`duo_escalate` 和 `duo_restart_duo`。工具的可用性取决于当前工作流。`duo mcp-server` 通过 JSON-RPC 暴露全部八个协调工具：`duo_send`、`duo_status`、`duo_set_status`、`duo_set_verification`、`duo_set_plan`、`duo_set_steps`、`duo_escalate` 和 `duo_restart_duo`。

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

在 gotui 仪表盘中输入 `/observer` 打开观察器设置；想知道两位 agent 在做什么时，按 `Ctrl+E`（或输入 `/reports`）打开状态报告，并让观察器立刻分析一次。如果还没有配置观察器模型，按 `Ctrl+E` 会自动打开设置。仅当观察器从未配置时（配置中没有 observer 段、没有密钥文件、也没有 `DUO_OBSERVER*` 环境设置），首次运行才会显示设置面板。默认地址为 `http://127.0.0.1:4000/v1`，模型为 `exec`；可直接编辑字段，按 `Enter` 保存，或按 `Esc`（**稍后**）将 `observer: {"enabled": false}` 保存到项目配置中，以后不再提示。此后仍可随时用 `/observer` 打开设置。

在设置面板中，按 `Tab`/`Shift+Tab` 切换字段，按 `Space`（或 `y`/`n`）切换启用状态，按 `Enter` 保存；按 `Esc` 关闭且不保存（首次提示中按 `Esc` 则保存为“稍后”）。按 `Ctrl+U` 清空当前字段。API key 留空会保留已保存的密钥。在报告面板中，按 `R` 重新分析，按 `↑`/`↓` 滚动，按 `Esc` 关闭。

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
| `maxEvents` | 一次报告读取的最近消息条数上限；默认 `40`。 |
| `timeoutSeconds` | 请求超时；默认 `60` 秒。 |
| `maxTokens` | 响应 token 上限；默认 `800`。 |

观察器只在你要求时才运行：没有定时器，也不会往时间线写任何内容。按 `Ctrl+E` 或输入 `/reports` 会发出一次请求，内容由三部分组成：最近 `maxEvents` 条消息（最新的在最后，总量约 6,000 字符）、请求发出时采样的两位 agent 实时状态（忙碌或空闲、正在运行的工具及其参数与已运行时长、当前正在写出的文字、刚结束的几个工具、上下文用量），以及上一份报告（仅用于衔接）。

每份报告按界面语言分三节：刚完成（最多三条，最新的在前，不会累积成历史）、现在正在做（只取自实时状态，因此已结束的工具绝不会被写成正在运行；空闲的 agent 会写明它刚完成了什么），以及接下来（只写 agent 明确说过的下一步）。只有当确实有决策阻塞 agent 时，末尾才会出现一行 `需要你决定：`。报告只出现在 `Ctrl+E` 面板里，绝不进入时间线，面板保留最近三份。模型作答期间面板会显示已用时间；按 `R` 可再次询问。

缺少密钥时面板会直接说明；请求超时、模型返回空内容或 HTTP 状态码非 2xx 时会写入 session 日志并在面板中显示诊断，这些都不会影响协调流程。若报告因 `maxTokens` 被截断，内容仍会显示，同时把截断作为诊断报告，而不是静默丢掉后面的小节。尚无报告时，面板会说明观察器未启用、缺少密钥，还是尚未被请求过。

## 远程控制

启动 Duo 并交代任务后，输入 `/qrcode`。用手机扫码：页面显示 Austin 与 Tony 的
运行状态和最近动态，可以下达新任务、开启新会话、刷新观察者报告，也能改变正在跑的
东西——工作流 mode、任一 Agent 的 driver 或 model。中继是一个
Cloudflare Worker，只存储密文，因此提示词与仓库路径都留在本机；配对密钥位于二维码
URL 的 fragment 中，浏览器不会把它发送给服务器。

配对信息保存在 `~/.duo/remote.json`（权限 `0600`），有效期 8 小时，并会自动重连，
因此 `/new` 之后手机无需重新扫码。关掉二维码面板不会断开手机：只有 `/remote off`、
配对过期、或 Duo 退出才会断开。`/remote` 查看状态，`/remote off` 撤销配对。
可在配置文件的 `remote` 段或 `DUO_REMOTE_URL` 中设置中继地址，默认为
`https://remote.xduo.dev`；`remote/` 目录中的 Worker 可自行部署。见
[docs/remote.md](docs/remote.md)。

## 恢复与交付

`duo resume [session-id]` 恢复已保存的会话；`duo sessions` 列出会话，并显示每个会话读取时的快照 schema（`2→3` 表示旧会话已升级）。恢复会保留会话记录的模式；没有记录模式的旧会话按 Goal 恢复，且 `DUO_MODE` 不会覆盖已恢复的会话。批准后，Duo 只通过快进（fast-forward）原始分支来交付。若交付受阻，排除原因后用 `duo apply` 重试。`duo logs` 列出会话记录；`duo clean` 删除已完成的会话数据与工作树。每个命令都支持 `--help`。

恢复会话不会向 agent 发送任何消息：记录与状态会还原，两个 agent 启动后保持空闲，自动的空闲提醒也不会打扰它们。你可以先安静查看记录；需要继续工作时，在仪表盘执行 `/resume`。提交任务总会唤醒它们，而 `/resume` 是不提交新任务就唤醒它们的唯一方式。Fast、Goal、Step 三种模式行为一致。已完成的（DONE）会话没有可恢复的工作，请提交任务开启新一轮。

## 文档

- [gotui 仪表盘与前端内部机制](./docs/gotui.md)
- [架构与协调边界](./docs/architecture.md)
- [远程控制与手机控制台](./docs/remote.md)
- [远程控制线路协议](./docs/remote-protocol.md)
- [driver 插件协议](./docs/driver-plugin-protocol.md)
- [插件开发指南](./docs/plugin-development-guide.md)
- [安全模型与交付边界](./SECURITY.md)

完整的 CLI 参考请运行 `duo --help` 与 `duo <command> --help`。

## 开发

运行 `make check` 执行 Go 测试、vet 与命令构建。改动 Pi bridge 时还需运行 `cd pi-extension && bun test`。
