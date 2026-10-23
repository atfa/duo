# Duo 开发 TODO（P0 / P1 / P2）

来源：Austin + Tony 双 agent 对代码库的逐条勘察（基线 main @ 538989f，2026-10-23），每条的位置均为实测 file:line。

**用法**
- 勾选 `- [ ]` → `- [x]` 表示完成，并在同行追加 `完成：<SHA> · <日期>`；**条目永不删除**，保留历史。
- 建议按 P0 → P1 → P2 顺序开发；标注了依赖的条目先做依赖项。
- 每条的完成判据以其 **验收** + **验证** 命令通过为准（Go 侧 `make check` 含 gofmt；桥侧 `cd pi-extension && bun test`）。
- 提交信息引用条目 ID（如 `T1: ...`）。

---

## P0

### T1 MCP 桥补齐 duo_set_steps 与 duo_restart_duo
- [ ] 完成：

**位置** `internal/mcp/server.go:30-103`（仅六个工具 Name：duo_status/send/set_status/set_verification/set_plan/escalate）、`:436-502`（executeTool switch）；`plugins/codex/codex.go:74`（`MCP: true`，以 `mcp_servers.duo.args=["mcp-server"]` 接线）；参数校验参照 `pi-extension/tools/steps.ts:17`、`restart.ts:15`；测试落点 `cmd/duo/mcp_test.go`
**问题** MCP server 只注册 6 个工具，而 STEP 模式提示词（`internal/coordinator/coordinator.go:885/891`）命令双方 agent 用 `duo_set_steps` 发布队列 → codex（及任何 MCP-only 驱动）的 agent 收到指令却调不到工具，STEP 的 PLAN 阶段无法完成，队列永远发不出来。
**预期状态** MCP `tools/list` 返回 8 个工具；codex 会话可完成 STEP PLAN 发布队列；`duo_restart_duo` 可被调用，脏树时按现有规则拒绝。
**验收** `cmd/duo/mcp_test.go` 新增 `TestMCPSetStepsPublishesQueue`、`TestMCPRestartRefusesDirtyWorktree`；同步 `docs/duo-bridge-protocol.md` 与 README 工具数（6→8）。
**依赖** 无
**验证** make check
**负责人** Austin

### T2 驱动 capability 声明：提示词只宣传 agent 调得到的工具
- [ ] 完成：

**位置** `internal/driver/contract.go`（Manifest 无 `tools` 字段）；`internal/coordinator/phase_prompts.go:211/229`（英文 STEP PLAN 提示词硬编码 duo_set_steps）、`:342/360`（中文版）；`internal/coordinator/coordinator.go:885/891`
**问题** 提示词按模式生成、不感知 agent 真实工具集：MCP-only 驱动收到命令它调用 `duo_set_steps`/`duo_restart_duo` 的指令（T1 补齐前根本不存在），未来新增精简桥的驱动同样会被宣传调不到的工具，agent 浪费轮次试错。
**预期状态** driver manifest 声明 `tools` 能力集，coordinator 按 capability 生成提示词；MCP-only 驱动的 STEP 提示词只含其实际工具集；`duo plugins test` 校验声明与真实注册一致。
**验收** `internal/driver` 新增 manifest 校验测试（声明与注册不一致即失败）；四个内置 plugin 补齐声明；`phase_prompts` 单测断言按 capability 裁剪。
**依赖** T1
**验证** make check
**负责人** Austin（协议）/ Tony（插件与文档）

### T3 `duo doctor` 环境自检命令
- [ ] 完成：

**位置** `cmd/duo/main.go:21-71`（case 分发：apply/sessions/logs/clean/mcp-server/plugins/step-loop，无 doctor）；`internal/clidoc/clidoc.go:37-92`（commands 表）
**问题** 首跑失败不可诊断：driver CLI 不在 PATH、pi/opencode 扩展未装、config 非法、worktree 残留、base 分支不可交付等故障以 gotui 启动失败或会话内隐晦报错呈现，新用户无法自助定位。
**预期状态** `duo doctor` 一条命令输出逐项诊断（git 版本、仓库脏状态、PATH 上的 driver 与 `duo plugins test` 结果、扩展安装状态、config 合法性、observer 可达性、base 分支可交付性），每条带唯一码与修复建议；gotui 首跑失败时提示运行。
**验收** `cmd/duo/doctor_test.go` 新增 `TestDoctorReportsMissingDriver`、`TestDoctorReportsDirtyRepo`、`TestDoctorReportsBadConfig`（三场景各输出唯一诊断码）；clidoc 注册命令。
**依赖** 无
**验证** make check
**负责人** Austin

### T8 “需要人类”通知（BEL + OSC 777）
- [ ] 完成：

**位置** `internal/gotui/`（全仓 grep 无 BEL/OSC 777 输出）；事件源：`internal/coordinator/coordinator.go`（冲突/delivery 拒绝/验证等待）、DONE 摘要路径 `internal/coordinator/summary.go`
**问题** 冲突、delivery 拒绝、Fast 验证等待、DONE 摘要就绪这些“必须人类行动”的信号只在 dashboard 内呈现；人切到别的终端/标签页就永久漏掉，会话静默挂起。
**预期状态** 上述四类事件触发终端 BEL + OSC 777 桌面通知（iTerm2/kitty/支持 OSC 777 的终端），文案跟随 `/language`，config 可关；dashboard 内 toast 保留。
**验收** `internal/gotui/notify_test.go` 新增 `TestHumanAttentionEmitsBellAndOSC777`（断言输出字节序列）、`TestNotifyDisabledByConfig`；翻译表 `internal/gotui/i18n.go` 补 zh 文案。
**依赖** 无
**验证** make check
**负责人** Tony

### T9 用量与成本账本 + 上下文压力预警
- [ ] 完成：

**位置** `internal/harness/tracker.go:198`（UpdateContext）、`:210`（UpdateRate）——仅写内存 `rt`，退出即丢；`internal/gotui/panels.go:434-436`（侧栏百分比瞬显）；持久化落点 `internal/sessionstore/snapshot.go`
**问题** 驱动上报的 token/上下文数据只存在内存里，会话结束成本即蒸发；人类无法回答“这轮花了多少”，也没有上下文爆仓前的预警，agent 常在压缩边缘才被发现。
**预期状态** 驱动协议上报 token/成本事件，按 agent 与阶段聚合持久化进 snapshot（resume 后存活）；新增 `/cost` 面板，`/overview` 与最终摘要展示，`duo sessions` 退出后可见总计；上下文占用越过阈值（默认 85%，可配置）时 dashboard 标红并向该 agent 发一次定向 nudge。
**验收** `internal/sessionstore` 新增成本聚合持久化测试；`internal/gotui` 新增 `TestCostPanelRendersPerAgentTotals`、`TestContextPressureWarnsOnceAtThreshold`。
**依赖** UI 侧 ← 协议/持久化侧（同一任务内先做协议）
**验证** make check
**负责人** Austin（协议/持久化）/ Tony（UI）

---

## P1

### T7 snapshot 迁移链：升级不再孤立老会话
- [ ] 完成：

**位置** `internal/sessionstore/snapshot.go:18`（`SchemaVersion = 2`）；`internal/sessionstore/store.go:107-108`（版本不符直接返回 `SchemaError`）
**问题** 快照 schema 不兼容时 `duo resume` 直接拒绝，无迁移路径：发布新 schema 的二进制后，用户所有未完成的旧会话永久不可 resume，签署与步骤队列随快照一起作废。
**预期状态** SchemaVersion 升 3 并提供 v2→v3 迁移；resume 对已知旧版本自动迁移；`duo sessions` 显示各会话 schema 版本与兼容性；未知未来版本仍拒绝。
**验收** `internal/sessionstore` 新增 `TestResumeMigratesV2ToV3`（v2 会话升级后可 resume，签署与队列不丢）、`TestResumeRejectsUnknownFutureVersion`。
**依赖** 无
**验证** make check
**负责人** Austin

### T4 `duo apply --branch <name>`（+ `--keep`）
- [ ] 完成：

**位置** `cmd/duo/apply.go`（零 flag，仅 session-id 参数）；`internal/delivery/delivery.go:65`（Check）、`:159`（Deliver）、`:246`（BranchHead）；`internal/workspace/git.go`
**问题** 交付只会 fast-forward 记录分支：GitHub-flow 用户（main 受保护、走 PR）拿不到任何出口，只能手工 `git branch` 复制结果。
**预期状态** `duo apply --branch <name>` 把已验证 HEAD 推到新分支而不动用户分支；`--keep` 保留 worktree；**无 flag 时本地 fast-forward 仍是唯一默认，SECURITY.md 交付边界不因新 flag 松动**。
**验收** `cmd/duo/delivery_test.go` 新增 `TestApplyToNewBranchLeavesUserBranch`、`TestApplyDefaultStillFastForwards`。
**依赖** 无
**验证** make check
**负责人** Austin

### T5 `duo apply --push`
- [ ] 完成：

**位置** 同 T4（`cmd/duo/apply.go`、`internal/delivery/delivery.go`）
**问题** fast-forward 只动本地仓库；有远端的用户还要手工 push，且手工 push 绕过了 Duo 的只读校验，可能覆盖远端移动。
**预期状态** `--push` 在 fast-forward 后推送 upstream：推送前 `ls-remote` 只读校验远端未移动，非 fast-forward 直接拒绝且不改本地。
**验收** 新增 `TestApplyPushRefusesMovedRemote`（远端已移动时本地与远端均不变）、`TestApplyPushFastForwardsRemote`。
**依赖** T4
**验证** make check
**负责人** Austin

### T6 `duo apply --pr`
- [ ] 完成：

**位置** 同 T4；PR 正文素材来自 `internal/sessionstore`（计划版本、步骤队列、签名 SHA、验证证据）
**问题** 交付结果无法直接进评审流：用户要手工建 PR、手工写评审上下文，Duo 已掌握的证据链（签名、验证、diffstat）全部浪费。
**预期状态** 有 `gh` 时创建 PR，正文由 Duo 评审链自动生成；无 `gh` 时降级打印同样正文与 `git push` 手动链接。**零新增 hosting API 依赖。**
**验收** 新增 `TestApplyPRBodyContainsEvidenceChain`、`TestApplyPRDegradesWithoutGh`（PATH 无 gh 时打印正文，退出码不为失败）。
**依赖** T4
**验证** make check
**负责人** Austin

### T12 headless：`duo run` + `duo status --json`
- [ ] 完成：

**位置** `cmd/duo/main.go:15-71`（无 run/status 命令，duo 必须开 gotui）；`internal/session/service.go:145`（New）、`:163`（Start）——headless seam 已存在，缺 CLI 表面
**问题** CI/脚本/批处理无法使用 duo：会话只能由人在 TUI 里提交任务、盯 gates，无法自动化。
**预期状态** `duo run -m fast|goal|step "task" [--json] [--timeout]` 不开 gotui 跑完整一轮到 DONE，退出码区分交付成功/拒绝/超时；`duo status --json` 输出与 `duo_status` 同源状态（phase/mode/plan 版本/队列/签名/HEAD/dirty/ahead）。
**验收** `cmd/duo` 新增 `TestRunFastModeExitsDelivered`、`TestRunTimeoutExitCode`、`TestStatusJSONMatchesDuoStatus`。
**依赖** 无
**验证** make check
**负责人** Austin

### T16 端到端会话测试补强 + 崩溃边界
- [ ] 完成：

**位置** `internal/coordinator` 已有 4 个 e2e 文件 20 个 Test（`delivery_e2e_test.go`、`fast_e2e_test.go`、`step_e2e_test.go`、`step_goal_e2e_test.go`）；fake driver 基建 `internal/agent/agenttest/driver.go`
**问题** 既有 e2e 在 coordinator 层，session.Service 组合根（驱动插件契约、resume、delivery 交接的整条真实链路）无全模式会话测试；kill -9 mid-delivery 与 journal replay 的崩溃边界无属性测试。
**预期状态** 在既有 coordinator e2e 之上，补 session.Service 级 fake-driver 全模式（含 Fast）会话测试、kill -9 mid-delivery 边界、journal replay 属性测试与交付幂等（重跑不重复移动分支），并接入 CI。
**验收** `internal/session` 新增 `TestSessionServiceFastRoundDeliversWithFakeDriver`、`TestSessionServiceGoalRoundDelivers`、`TestSessionServiceStepQueueLandsAllSteps`、`TestResumeJournalReplayIsIdempotent`（journal 由 `internal/session/resume.go:49` 消费）；`internal/recovery` 新增 `TestReconcileIsIdempotent`（recovery 做的是快照对 Git 证据的 Reconcile，recovery.go:277，与既有 TestReconcile* 命名一致）。
**依赖** T12（共用 fake-driver 基建）
**验证** make check
**负责人** Austin

---

## P2

### T19 `duo upgrade`
- [ ] 完成：

**位置** `cmd/duo/stepupgrade.go:232`（performUpgrade）、`:279`（installFromRepo）、`:304`（checkSchemaCompatible）——可复用逻辑已存在；`internal/version`
**问题** 升级靠 `curl | bash` 脚本手工完成：无版本检查、无 checksum 校验、无原子替换，装坏二进制会直接砖掉当前会话的恢复路径。
**预期状态** `duo upgrade` 查最新 release、校验 checksum、原子替换二进制（`--dry-run` 只报告）；复用 step-loop 的构建身份与脏树拒绝逻辑。
**验收** `cmd/duo` 新增 `TestUpgradeChecksumMismatchKeepsBinary`、`TestUpgradeDryRunReportsOnly`。
**依赖** 无
**验证** make check
**负责人** Austin

### T15 Step 人类控制 + 交付节奏
- [ ] 完成：

**位置** `internal/coordinator/step.go:155`（每步落地即 `deliverFinal`）；`internal/coordinator/coordinator.go:1632`（handleSetSteps，队列 append-only）；`internal/gotui/planoverlay.go`
**问题** 队列发布后人类无法从 dashboard 取消/跳过 pending step（发新队列会撤销双方签署）；每步落地都 fast-forward 用户分支，对想要“一次干净交付”的用户是噪音。
**预期状态** dashboard 可取消/跳过 pending step（带确认，landed/current 步不可动），状态仍由 coordinator 拥有；新增节奏配置：每步落地即交付 vs 队列末一次性交付。
**验收** `internal/coordinator` 新增 `TestHumanCancelsPendingStep`、`TestCancelRefusesCurrentStep`、`TestQueueEndDeliveryCadence`；`internal/gotui/step_ui_test.go` 覆盖确认面板。
**依赖** 无
**验证** make check
**负责人** Tony

### T13 转录搜索
- [ ] 完成：

**位置** `internal/gotui/commands.go:75-199`（slash 命令表无 /search）；转录渲染 `internal/gotui/transcript.go`
**问题** 长会话（数百条消息）无法定位历史消息，只能滚动肉眼找；中文转录无检索。
**预期状态** `/search`（Ctrl+F）在当前 pane 搜索，`n`/`N` 跳转 + 高亮，中文输入可用，Esc 退出恢复视口。
**验收** `internal/gotui` 新增 `TestSearchHighlightsMatches`、`TestSearchNextWrapsAround`（UTF-8 中文用例）。
**依赖** 无
**验证** make check
**负责人** Tony

### T14 会话选择器
- [ ] 完成：

**位置** `internal/gotui/commands.go:75-199`（无 /sessions）；`internal/sessionstore/store.go`（会话枚举已存在，`duo sessions` 复用）
**问题** 恢复历史会话必须退出 duo 再跑 `duo resume <id>`，id 还要先跑 `duo sessions` 抄写；多仓库多会话切换成本高。
**预期状态** `/sessions` overlay 列出会话（mode/phase/时长/成本），Enter 直接 resume，Esc 取消。
**验收** `internal/gotui` 新增 `TestSessionsOverlayResumesSelected`；`duo sessions` 输出格式回归不受影响。
**依赖** 无
**验证** make check
**负责人** Tony

### T11 `duo export <session>`
- [ ] 完成：

**位置** `cmd/duo/main.go:15-71`（无 export 命令）；素材源 `internal/sessionstore`（快照、事件 journal、转录）、`internal/delivery`
**问题** 会话档案只存在于 `~/.duo` 内部格式（snapshot JSONL + 转录），无法归档、分享或离线评审一轮协作。
**预期状态** `duo export <session-id> [--format md|html]` 生成自包含档案：时间线、计划/队列版本、签名与证据 SHA、diff、最终摘要。
**验收** `cmd/duo` 新增 `TestExportContainsEvidenceChain`（断言导出含签名 SHA 与队列）、`TestExportHTMLSelfContained`。
**依赖** 无
**验证** make check
**负责人** Tony

### T18 i18n 完整性 lint
- [ ] 完成：

**位置** `internal/gotui/i18n.go`（翻译表存在）；`internal/gotui/status_zh_test.go`（仅覆盖状态反馈）
**问题** zh 覆盖靠人工记忆（上轮就漏过 /status fallback），新面板/新提示随时可能漏翻译，用户混语言界面。
**预期状态** 测试扫描 gotui 用户可见字符串，缺 zh 翻译即失败，防回归。
**验收** `internal/gotui/i18n_test.go` 新增 `TestAllUserVisibleStringsHaveZh`（含豁免清单机制）。
**依赖** 无
**验证** make check
**负责人** Tony

### T20 Claude Code 驱动插件
- [ ] 完成：

**位置** `plugins/`（仅 agy/codex/opencode/pi）；`internal/driver/registry.go:16`（规范名 `duo-plugin-<name>`）；`docs/plugin-development-guide.md`
**问题** 最常被要求的 agent CLI（Claude Code）缺席，用户被迫用 pi/opencode 或自写插件。
**预期状态** 新增 `cmd/duo-plugin-claude` + `duo-claude` shim，按 plugin 协议实现（identity、prompt 注入、活动上报、resume 状态），`duo plugins` 可见，`duo plugins test` 通过。
**验收** `duo plugins test` 全项通过；新增插件契约测试（与 `plugins/codex` 同级结构）。
**依赖** T2（T2 先落地时新插件必须声明 tools 能力集，否则 `duo plugins test` 因声明缺失失败）
**验证** make check
**负责人** Tony

### T17 Windows 切片（不做全量）
- [ ] 完成：

**位置** `internal/agent/session.go:447`（显式拒绝非 darwin/linux）；PTY 层 `internal/terminal`
**问题** Windows 用户完全无法使用 duo；直接打开平台开关会因 PTY/信号/路径假设在运行期碎掉。
**预期状态** `GOOS=windows` 全仓编译与单测全绿 + opencode 驱动（npm 系）ConPTY 原型可跑通一轮 Fast；结论文档明确支持边界；release 产物等切片证明 PTY 层可行再加。
**验收** CI 增加 `GOOS=windows go build ./...` 与 `go vet` 门禁；原型结论文档（明确哪些驱动/功能可用）。
**依赖** 无
**验证** make check（Linux 侧门禁）+ CI windows build job
**负责人** Tony
