# Duo 下一阶段开发计划：可插拔 Agent 驱动架构与 Google Antigravity (`agy`) 支持

> 本文档为下一阶段架构解耦与插件体系的本地实施计划，记录设计方案、分层契约与开发步骤。待全阶段开发完成并验收后可予以删除。

---

## 一、背景与设计目标

### 1. 现状痛点
- 当前 Duo 内部与 Pi CLI 强耦合：`internal/agent` 直接拼接 `pi` 命令行参数、硬编码 `pi-extension` 目录路径、并直接管理特定于 Pi 的进程启动与状态感知。
- 无法接入如 Google Antigravity CLI (`agy`)、Claude Code CLI、Aider 等其他主流 Coding Agent。
- 无法实现异构对等组合（Heterogeneous Pairing，例如 Austin 使用代码理解能力强的 `agy`，Tony 使用跑单元测试和漏洞扫描快且成本低的轻量 `pi` 模型）。

### 2. 核心架构目标
1. **Duo Core 与具体 Agent 运行时彻底解耦**：Duo Core 仅负责 Git Worktree 隔离、状态机判定（Fast / Goal）、确定性测试门禁、合并交付与 TUI 展示。
2. **两层解耦模型（MCP 工具面 + Driver 感知面）**：
   - **工具能力面（标准化）**：基于标准 MCP（Model Context Protocol）暴露 Duo 状态机工具，零侵入适配任意支持 MCP 的 Agent。
   - **感知与生命周期面（归一化事件流）**：抽象 `AgentDriver` 接口，不同引擎各取所长（Pi 走 Socket 推送，`agy` 走 `transcript.jsonl` 文件观察），向上统一归一化为 Go `<-chan AgentEvent`。
3. **渐进式演进为插件机制**：支持将不同 Agent 的适配器作为独立可分发的子命令（如 `duo-pi`、`duo-agy`）独立维护。

---

## 二、系统架构设计方案

### 1. 分层架构图

```text
┌────────────────────────────────────────────────────────┐
│                        Duo Core                        │
│  (State Machine, Git Worktrees, Test Gates, TUI, CLI)  │
└───────────────┬────────────────────────┬───────────────┘
                │                        │
       【感知与生命周期层】                【状态机工具能力层】
       AgentDriver 接口                  内置 Local MCP Server
       (进程 / PTY / 事件流 / Steer)       (duo_send, duo_status, duo_set_status,
                │                         duo_set_verification, duo_escalate)
      ┌─────────┴─────────┐                      │
      ▼                   ▼                      ▼
  [PiDriver]         [AgyDriver]        [~/.duo/mcp.json]
   (Socket 推)        (Transcript 观察)          │
      │                   │                      │
      ▼                   ▼                      ▼
   Pi CLI            Google Antigravity      任意支持 MCP
 (pi-extension)         (agy CLI)           的 Agent 运行时
```

### 2. 统一抽象接口定义 (`internal/driver/driver.go`)

```go
package driver

import (
	"context"
	"os"

	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/workspace"
)

// AgentEvent 归一化的 Agent 实时感知事件
type AgentEvent struct {
	Agent       protocol.AgentID
	Status      string        // "working", "thinking", "idle", "exited", "failed"
	ToolName    string        // 当前正在运行的工具名，如 "duo_send"
	ToolArgs    string        // 工具参数概要
	ToolDur     int64         // 工具耗时 (毫秒)
	ToolStatus  string        // "ok", "error"
	ThinkingLvl string        // 思考强度: "off", "low", "medium", "high"
	StreamTail  string        // 流式生成的文本末尾片段
	ErrorMsg    string        // 错误提示
}

// AgentDriver 所有 Agent 运行时的通用抽象接口
type AgentDriver interface {
	// ID 返回驱动标识，例如 "pi", "agy"
	ID() string

	// Start 启动指定角色在对应 Worktree 中的 Agent 实例，并与 allocated PTY 绑定
	Start(ctx context.Context, role protocol.AgentID, ws workspace.Workspace, ptyFile *os.File) error

	// Stop 停止 Agent 进程并清理相关资源
	Stop() error

	// Events 返回标准化的感知事件只读通道（供 TUI 工作预览带与 Harness 消费）
	Events() <-chan AgentEvent

	// SendSteer 向运行中的 Agent 动态注入消息（同行消息或 Duo 系统引导）
	SendSteer(ctx context.Context, text string) error

	// Models 返回该 Agent 当前配置/支持的模型信息（可选）
	CurrentModel() (model string, thinking string)
}
```

### 3. Google Antigravity CLI (`agy`) 适配机制

1. **工具注入机制**：
   - Duo 启动时就地生成一个轻量进程或嵌入式 MCP 服务配置，配置路径传给 `agy` 会话。
   - `agy` 读取配置后，原生加载 `duo_send`、`duo_set_status`、`duo_set_verification`、`duo_escalate`，与模型指令完全无缝打通。
2. **感知机制（观察者模式）**：
   - `agy` 会在执行步骤中向 `<appDataDir>/brain/<conversation-id>/.system_generated/logs/transcript.jsonl` 持续追加 JSON 记录。
   - `AgyDriver` 启动时定位该日志文件，以后台轻量协程（类似 `tail -f`）监听行增量：
     - 读取 `step_index`、`type: "PLANNER_RESPONSE"`、`status`；
     - 提取 `thinking` 内容，更新思考状态；
     - 提取 `tool_calls` 中的工具名与参数，更新工作预览带；
     - 工具完成时计算耗时并更新 ✓/✗。
   - **零入侵、无外部依赖、100% 还原执行细节**。
3. **PTY 直通控制**：
   - 使用 Go `pty.Start` 将 `agy` 作为子进程挂接在虚拟终端上。
   - 用户按 `Ctrl+A` 时，终端直接切入 `agy` 的原生交互大屏。

---

## 三、分阶段实施里程碑（Milestones）

### Milestone 1: 核心解耦与 In-Tree Driver 抽象（已完成）
- [x] 在 `internal/agent` 建立 `Driver` 统一契约，解耦生命周期与 PTY 交互。
- [x] 抽象 `PiSession` 与 `AgySession` 双引擎实现，消除对 `pi` 的唯一硬编码假定。
- [x] 重构 `Manager` 统一管理 `Driver` 接口，提供完全向后兼容的调用规范。
- [x] 在 `cmd/duo` 支持驱动解析与配置（`--driver`, `--agent`, `--austin-driver`, `--tony-driver`，支持异构混搭）。
- [x] 在 `internal/models` 支持 `agy models` 与 `pi --list-models` 统一目录解析。
- [x] 确保全量测试通过（`make check` 100% PASS）。

### Milestone 2: 内置轻量 MCP Tool Server（状态机工具通用化，已完成）
- [x] 实现 Duo 内置的 `internal/mcp/server.go`（基于 JSON-RPC 2.0 stdio，连接 Duo internal bridge）。
- [x] 将当前写在 `pi-extension/tools/*.ts` 中的状态机工具完整映射为通用的 MCP Tools Schema（duo_status, duo_send, duo_set_status, duo_set_verification, duo_set_plan, duo_escalate）。
- [x] 支持通过 `duo mcp-server` 运行标准 stdio MCP 服务，并支持 `--export-config` 导出给外部 Agent CLI 使用。

### Milestone 3: 研发与接入 `AgyDriver`（支持 Google Antigravity）
- [ ] 实现 `internal/driver/agy`：
  - 启动参数编排（工作区、环境变量、会话定位）；
  - `transcript.jsonl` 增量解析与事件流生成；
  - PTY 交互绑定与退出监听。
- [ ] 在 CLI 与配置文件中增加驱动选项：
  - `duo --agent agy`
  - `duo --austin agy --tony pi`（支持异构混合对等）
  - 配置文件 `~/.duo/config.json` 支持 `"agent": "agy"` 或分角指定。
- [ ] 编写专门的集成测试与验证用例。

### Milestone 4: 独立插件体系化（Out-of-Tree Plugin Protocol）
- [ ] 定义可执行插件发现规范：
  - 检查 `~/.duo/plugins/duo-<name>` 与系统 `$PATH`。
- [ ] 实现 `ExternalProcessDriver`，通过子进程拉起第三方插件。
- [ ] 将 `duo-pi` 与 `duo-agy` 打包为独立二进制发布工件，验证外部社区适配器扩展能力。

---

## 四、自测与验收标准（Definition of Done）

1. **向后兼容性（Backward Compatibility）**：
   - 默认不加参数运行 `duo`，行为与当前一致（使用 Pi 驱动），所有现有快捷键与工作流完好无损。
2. **纯键盘交互与体验一致性**：
   - 使用 `agy` 作为后端时，工作预览带（Work Preview）能精确呈现工具耗时、思考级别与流式文本。
   - `Ctrl+A` / `Ctrl+T` 可正常在 Duo 主界面与 `agy` 原生终端之间往返切换。
3. **异构协作有效性**：
   - 验证 `Austin (agy) + Tony (pi)` 组合能够完整跑通 Fast 模式 `RUNNING → VERIFY → DONE` 以及 Goal 模式的协同。
