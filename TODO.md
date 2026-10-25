# Duo 开发计划（P0 / P1 / P2）

**本文件即 PLAN。** 仓库没有 PLAN.md，计划就是本文件；不要另建一份计划文档，也不要改名。

本版基线：**main @ f9a33e2785（2026-10-25）**。上一版基线是 `538989f`（2026-10-23）；之后落地了 v0.14.0 / v0.14.1 的全部内容（远程手机页与命令、`duo apply --branch/--push/--pr`、headless `duo run` + `duo status --json`、schema 3 迁移、成本账本、attention 通知、交付/恢复崩溃边界），上一版 P0/P1 条目已全部完成（见历史）。本轮为计划校准：只改本文件，不改代码；每条的性能数字都在本机实测，`file:line` 均对 `f9a33e2785` 复核过。

## 开发原则（2026-10-25 定）

1. 最小根因改动，复用现有代码/标准库/平台能力；不加未被要求的抽象、依赖、配置。
2. **性能只为实测瓶颈立项。** 条目必须带可复现的命令/基准和数字；没有数字的"性能问题"不立项，也不许以性能为名加缓存层、异步化、批量写、分段加载。
3. **稳定性只覆盖已验证的故障边界**（如 T16 的交付/恢复崩溃边界）。不为极小概率情况堆设计：没有第二份状态机、没有兜底副本、没有自动重试框架。
4. **不用配置项换行为。** 一个开关就是一份新契约（文档、持久化、两侧测试），只在该行为确实按人来变时加；"可能在某种流程下更好"不算理由。
5. 不新增第三方依赖，除非标准库做不了。

## 代码质量现状（f9a33e2785 实测）

- 规模：311 个 `.go`（其中 150 个非测试文件）/ 77,221 行（非测试 41,438 行）；161 个 `_test.go`、894 个 `Test`、0 个 `Benchmark`（测试:非测试行数 ≈ 0.9:1）。
- 门禁：`go vet ./...` 干净、`gofmt -l .` 为空、非测试代码 0 处 `TODO`/`FIXME`、只有 1 处 `panic`（`cmd/duo/config.go:780`，生成会话失败即不可继续）。
- 集中度：最大的三个文件是 `internal/coordinator/coordinator.go` 2744 行、`internal/session/service.go` 1494 行、`internal/gotui/app.go` 1038 行。
- 返回值忽略：227 处 `_ =`（`grep -rn '_ = ' --include=*.go . | grep -v _test.go`；多为“日志/清理失败不影响主流程”，未逐条审计）。
- 驱动插件重复：`plugins/agy|codex|opencode` 的 `Prepare` 各 93/106/102 行、`pi` 51 行；共享 helper 已在 `internal/driver/cmdline.go:15/22/76`（`ShellQuote`/`HasFlag`/`AppendFlag`），codex 的 MCP 配置属该 CLI 自身差异。
- 判断：**质量改进只走有实测证据的 T21/T22/T23**。不为行数做拆分重构（`coordinator.go` 拆文件是纯搬家，审查与冲突成本 > 收益）、不为 227 处 `_ =` 立项、不给插件做基类抽象——这三条同时列进非目标。

## 用法

- 勾选 `- [ ]` → `- [x]` 表示完成，同行追加 `完成：<SHA> · <日期>`；**条目永不删除**，完成后压缩进"历史"一节保留 ID 与 SHA。
- 按 P0 → P1 → P2 顺序开发；标注了依赖的条目先做依赖项。
- 完成判据 = 条目 **验收** + **验证** 命令通过（Go 侧 `make check` 含 gofmt；桥侧 `cd pi-extension && bun test`，`cd opencode-extension && bun test`，`cd remote && node --test`）。
- 提交信息引用条目 ID（如 `T21: ...`）。
- 性能条目必须同时给出：测量命令、基线数字、目标数字。

## 状态总览

| 优先级 | 内容 | 条目 |
| --- | --- | --- |
| P0 | 门禁缺口（122 个前端用例无 CI） | T22 |
| P1 | 实测性能（运行时 1 条、开发循环 1 条） | T21、T23 |
| P2 | 功能规划（按价值排序） | T20、T24、T25、T13、T14、T11、T19、T15、T18 |
| 非目标 | 刻意不做，附实测依据 | 见"非目标"一节 |

---

## P0

### T22 CI 补齐前端测试与 gofmt 门禁
- [ ] 完成：

**位置** `.github/workflows/ci.yml`（矩阵 ubuntu×2 + macos，步骤只有 `go test ./...`、`go vet ./...`、`go build ./cmd/duo`）；`Makefile` 的 `check` 目标已含 `fmt: gofmt -l .`；三套前端测试 `pi-extension/*.test.ts`（21 用例）、`opencode-extension/lifecycle.test.ts`（14 用例）、`remote/test/*.test.js`（87 用例，`npm run check` = `node --test` + `node --check`）。
**问题** **122** 个前端用例从未进门禁（21 + 14 + 87）：本地 `bun test` / `node --test` 全绿，但 CI 只跑 Go，改坏 `pi-extension/lifecycle.ts` 或远程页面协议不会被任何门禁拦住。`gofmt` 同理，只在本地 `make check` 生效，CI 能合入未格式化代码。`opencode-extension/package.json` 甚至没有 `test` 脚本，跑法靠人记。
**预期状态** CI 增加三步（不新增 lint 工具、不引入项目依赖）：① `oven-sh/setup-bun@v2` 后跑 `bun test`（pi-extension、opencode-extension；后者 `package.json` 补 `"test": "bun test"`）；② `actions/setup-node@v4` 固定 `node-version: 22` 后跑 `node --test`（remote/）——`remote/test/harness.js:5` 用 `node:sqlite` 的 `DatabaseSync`，需要 Node ≥22，不能赌 runner 自带的 `node` 版本；③ `gofmt -l .` 非空即失败。两个 setup action 是 CI 工具，不算项目依赖。矩阵只在 ubuntu 上跑前端测试，避免重复三遍。
**验收** 临时改坏一条 TS 断言 → CI 变红（PR 上验证一次即可回退）；`gofmt -l .` 为空；`make check` 与 CI 结论一致。
**依赖** 无
**验证** `make check` + 一次 CI 运行
**负责人** Tony

---

## P1（性能，必须有数字）

### T21 转录增量渲染：追加一条消息不再全量重建
- [x] 完成：dbc8ace · 2026-10-25

**位置** `internal/gotui/transcript.go:70`（`SetEntries` 只按序号短路）、`:158`（`rebuildLines` 全量重跑 markdown + linkify + 换行）、`internal/gotui/text.go:104`/`:108`（超宽词逐 rune `+=` 拼接，O(n²)）；触发链 `internal/gotui/messages.go:89`（`addLabeled`）→ `internal/gotui/app.go:1015`（`appendEntry`，窗口上限 `:1025` 共 400 条）；每帧 `internal/gotui/app.go:672`（`updateWidgets` 写 4 个 transcript：timeline + 三个 pane）。
**问题** 每来一条消息，timeline 与消息所属 pane 的 transcript 各自从头重解析全部 400 条（4 个 transcript 每帧都会被调用 `SetEntries`，但只有内容真的变了的那两个会重建）。实测（400 条、100 列 × 40 行、无时间戳、直接调 `rebuildLines`）：0.7–1KB 正文 = **8.6–19.5ms / 11.5–26.7MB**（随 markdown/链接密度变化）。一条消息 ≈ 两份重建 = **10–40ms 停顿 + 20–50MB 分配**，不是毫秒级抖动。会话越长越贵，到 400 条上限前随条目数线性增长。这是本轮勘察中**唯一实测成立的运行时性能缺陷**。
**基准口径（重要）** 基准必须每次迭代真正进入 `rebuildLines`：重复传同一份列表会被 `SetEntries` 的序号短路（`internal/gotui/transcript.go:70`）且 `rebuild()` 在宽高未变且 `v.lines` 非空时直接返回（`internal/gotui/transcript.go:132`），测出来的是空转。写基准时交替传两份只有末条 seq 不同的列表，或每次追加新条目。条目必须写明：条目数 / 正文字节与密度 / 列×行 / 时间戳开关 / pathRoots / 是否追加第 N 条。
**预期状态** 渲染行按 `entry.seq + 宽度 + 时间戳开关 + pathRoots 版本` 缓存：追加只渲染新条目，窗口淘汰的条目连同缓存一起丢弃（缓存规模 = 当前窗口，不随会话增长）。`wrapRuns` 的超宽词改用 `strings.Builder` 只在同基准确认有增益时做，否则不动。不引入第二份消息状态，不加异步渲染/后台协程/脏标记框架。
**验收** 新增 `BenchmarkTranscriptAppend`（400 条 × 1KB @100 列 × 40 行，追加第 401 条，且每轮真实重建）：目标**追加一条消息 <200µs、分配 <200KB**（基线 8.6–19.5ms / 11.5–26.7MB；单条 1KB 正文渲染实测 49µs / 69KB，所以 timeline + 所属 pane 两份约 100µs / 140KB，目标留一倍余量）；`go test ./internal/gotui` 全绿（含中文双宽、链接、选区、滚动、滚动条既有用例）；缓存内存不随会话增长。
**依赖** 无
**验证** `make check` + `go test ./internal/gotui -run '^$' -bench TranscriptAppend -benchmem`
**负责人** Tony

### T23 测试墙钟：把固定超时从测试路径里拿掉
- [ ] 完成：

**位置** `internal/driver/client.go:20`（`defaultTimeout = 20s`）、`:26`（`StartTimeout = 90s`）；`internal/driver/contract_test.go:133`（用 `sleep 30` 脚本撞默认超时）、`:287`（`TestLegacyShimIsDistinguishedFromABrokenPlugin`）；`internal/remote/client.go:345`（`keepalivePeriod = 30s`）、`:347`（`readIdleTimeout = 90s`）、`:351`（`pushInterval = 1s`）。
**问题** `time go test -count=1 ./...` 墙钟实测 **60.1s**；关键路径是 internal/driver **59.6s**（`internal/driver/contract_test.go:133` 的 `sleep 30` 脚本撞 20s 默认超时，`:287` 的 `TestLegacyShimIsDistinguishedFromABrokenPlugin` 再等 27.3s），internal/remote 17.1s 与 plugins/agy 14.1s 与之并行（因此不能相加当墙钟）；driver 修好后这两个成为新关键路径。改一行代码要等一分钟，CI 每个任务再等一遍。
**预期状态** 超时与间隔变成可注入字段：driver client 的单次/启动超时，remote client 的 push/keepalive 间隔；测试传 1s 量级。**生产默认值一个都不改**（20s/90s、1s/30s/90s），不新增配置项或环境变量。
**验收** `time go test ./... -count=1` 墙钟 **<20s**（三处都改后预期 ~15s）；新增断言：注入值确实生效、生产默认仍是 20s/90s（`TestContractUsesInjectedTimeout` 一类）；全仓测试结论与改前一致。
**依赖** 无
**验证** `time go test ./... -count=1` + `make check`
**负责人** Tony

---

## P2（功能规划，按价值排序）

每条写清"用户是谁 / 缺什么 / 最小实现是什么 / 明确不做什么"。

### T20 Claude Code 驱动插件
- [ ] 完成：

**位置** `plugins/`（现有 agy/codex/opencode/pi）、`internal/driver/registry.go:12-16`（规范名 `duo-plugin-<name>`，`:15 executablesFor`）、`docs/plugin-development-guide.md`。
**用户/缺口** 用 Claude Code 的人目前只能改用 pi/opencode 或自己写插件，这是最常被要求的 agent CLI。
**最小实现** 按插件指南加 `plugins/claude` + `cmd/duo-plugin-claude`：identity、prompt 注入、活动上报、resume 状态，capabilities 按该 CLI 的真实能力声明（不得虚报 tools/usage/liveModelSwitch）。需要一键启动时再按 pi/opencode/agy 的现有形态加 `cmd/duo-claude` shim，不提前建。不新增 Core 分支、不加新配置层；若桥协议确有缺口，缺口单列条目再改协议。
**验收** `duo plugins test` 全项通过；`plugins/claude` 结构与 `plugins/codex` 同级并有契约测试；`internal/coordinator` 无改动（或缺口另案）。
**依赖** T2（已完成：capability 声明）
**验证** `make check`
**负责人** Tony

### T24 远程解除挂起：pause / unpause / continue / resume
- [x] 完成：c9e4e88 · 2026-10-25

**位置** `internal/remote/protocol.go:44-59`（命令只有 `task`、`new_session`、`observer_report`、`switch_mode`、`set_driver`、`catalog`、`set_model`）、`internal/remote/client.go:23`（`Sink` 接口）、`cmd/duo/remote.go:404-709`（`remoteSink`）、`docs/remote-protocol.md:269`（`cmd` 列表）；已有动词与映射：`internal/session/command.go:45-55`（`Pause`/`Unpause`/`Continue`/`ResumeWork`）、`internal/session/service.go:872-877`（`Do` 里的 switch）；TUI 已用同一批动词 `internal/gotui/slash.go:28-31`、`internal/gotui/commands.go:221-233`。
**用户/缺口** 人离开键盘后，Duo 的挂起无法从手机解除：`/pause` 之后没人能 `/continue`；恢复的会话没人 `/resume`；而手机只能发新任务、切模式、换驱动、换模型。远程存在的意义正是"人不在键盘前"。
**最小实现** 四个新命令（`pause`/`unpause`/`continue`/`resume_work`）：`Sink` 加四个方法，host 直接映射到既有 `session` 命令，页面加暂停/继续按钮，复用现有 MAC、nonce、限流；`docs/remote-protocol.md` 同步命令表。**明确不做**：远程签 `set_plan`/`set_ready`/`set_verification`——签名是 agent 活连接授权的证据链（见 `internal/session/command.go` 顶部注释），不看 diff 就签是脚枪；也不做远程 diff 浏览。不新增会话状态、不新增协议字段。**中止本轮不单列动词**：会话层没有 abort（挂起是 pause 族），中止当前工作是重启该 agent（`RestartAgent`，TUI 的 Ctrl+R 用的就是它）；只有出现真实需求时才把 `restart_agent` 映射进本条的 Sink，不为此新增语义。
**验收** `internal/remote` 新增 `TestPauseCommandReachesSink`、`TestUnknownCommandRefused`；`cmd/duo` 新增 `TestRemotePauseMapsToSessionPause`；`remote/test` 覆盖页面按钮发出的命令体与 MAC；协议文档命令表更新。
**依赖** 无
**验证** `make check` + `cd remote && node --test`
**负责人** Tony

### T25 成本上限：超阈值提醒一次
- [ ] 完成：

**位置** `internal/coordinator/usage.go:17`（`handleUsage` 只累加不约束）、`:28`（`UsageLedger`）；配置机制 `.duo/config.json`（`internal/workspace/project_config.go:224` `SaveProjectObserver` 已示范按 section 写同一文件）；通知通道 `internal/events`（`KindAttention`，T8 已完成）。
**用户/缺口** 把 Duo 放着跑长任务的人，只有在事后 `/cost` 才看到花了多少钱，没有"到此为止"的提醒。
**最小实现** 一个可配置总额（`maxCostUSD`，0/缺省=关）＋ 达到阈值时发**一次** attention，并把总额/已用写进 `/cost` 面板；不引入策略引擎，不做分阶段预算、成本预测、自动降级模型、自动暂停。持久化沿用既有 `UsageLedger`，不新增状态字段。
**验收** `internal/coordinator` 新增 `TestCostCeilingRaisesAttentionOnce`、`TestCostCeilingDisabledByDefault`；resume 后不重复刷屏（每个会话一次）；不改 `/cost` 既有输出结构。
**依赖** T9（已完成）
**验证** `make check`
**负责人** Austin

### T13 转录搜索
- [x] 完成：b84bf50 + fe394d5 · 2026-10-25

**位置** `internal/gotui/slash.go:18`（命令表，无 `/search`）、`internal/gotui/commands.go:63`（`runSlash` 分发）、渲染行 `internal/gotui/transcript.go`。
**用户/缺口** 长会话几百条消息只能肉眼滚动；中文转录同样无法定位。
**最小实现** `/search`（Ctrl+F）在当前 pane 内做大小写不敏感子串匹配，`n`/`N` 跳转并在行内高亮，Esc 退出恢复视口。不做正则、不做跨 pane、不做替换、不存搜索历史。
**验收** `internal/gotui` 新增 `TestSearchHighlightsMatches`、`TestSearchNextWrapsAround`（含 UTF-8 中文用例）。
**依赖** 无
**验证** `make check`
**负责人** Tony

### T14 会话选择器
- [x] 完成：cb17a99 · 2026-10-25

**位置** `internal/sessionstore/store.go:186`（`List`，会话枚举已存在）、`cmd/duo/sessions.go`（CLI 已用）、`internal/gotui/slash.go:18`（无 `/sessions`）。
**用户/缺口** 换回一个历史会话要退出 duo、跑 `duo sessions`、抄 id、再 `duo resume <id>`。
**最小实现** `/sessions` overlay 列出未完成会话（mode/phase/时长/成本），Enter 走与 `duo resume <id>` 相同的路径（退出当前进程并由外层重新启动会话）。不做会话热切换、不做后台多会话守护、不改 `duo sessions` 输出格式。
**验收** `internal/gotui` 新增 `TestSessionsOverlayResumesSelected`；`cmd/duo/sessions_test.go` 回归不变。
**依赖** 无
**验证** `make check`
**负责人** Tony

### T11 `duo export <session>`（仅 Markdown）
- [x] 完成：3f507e4 · 2026-10-25

**位置** `cmd/duo/main.go:18-140`（子命令分发，无 export）、`cmd/duo/main.go:292`（`printUsage`）；素材 `internal/sessionstore`（snapshot、事件 journal、转录）、`internal/delivery`。
**用户/缺口** 一轮协作只能留在 `~/.duo` 的私有格式里，无法归档、分享或离线评审。
**最小实现** `duo export <session-id>` 输出一份自包含 Markdown：时间线、计划/队列版本、双方签名与证据 SHA、交付记录、diffstat、最终摘要。只做 Markdown。**明确不做** `--format html`（避免为导出再实现一套渲染/样式/资源内联）。
**验收** `cmd/duo` 新增 `TestExportContainsEvidenceChain`（断言含签名 SHA 与队列）；导出内容不含绝对路径以外的环境细节。
**依赖** 无
**验证** `make check`
**负责人** Tony

### T19 `duo upgrade`
- [x] 完成：3fd2ce9 + 192b97d · 2026-10-25

**位置** `cmd/duo/stepupgrade.go:232`（`performUpgrade`）、`:279`（`installFromRepo`）、`:320`（`checkSchemaCompatible`）、`internal/version`；原子写参考 `internal/sessionstore/store.go:139`（`atomicWrite`：temp + fsync + rename + dir fsync）。
**用户/缺口** 按 release 安装的用户只能 `curl | bash`：无版本检查、无校验和、无原子替换，装坏二进制会毁掉当前会话的恢复路径。
**最小实现** `duo upgrade [--dry-run]`：查最新 release → 下载本平台二进制 → 校验 release 的 SHA256 → 临时文件 + fsync + rename 原子替换；替换前用现有 `checkSchemaCompatible`（`:320`）确认该二进制能把当前会话带下去，不兼容就拒绝。**不新增重启机制**：当前进程继续跑旧二进制，下次启动生效；在 step-loop 下由现有退出码 77 + 启动器接手（`docs/self-upgrade.md`）。**明确不做**：复制 step-loop 的 git 构建安装路径（那会是第二条安装链路），也不做 beta 通道、本地版本库回滚、自动升级。`step-loop` 的 git 安装逻辑原样不动。
**验收** `cmd/duo` 新增 `TestUpgradeChecksumMismatchKeepsBinary`、`TestUpgradeDryRunReportsOnly`、`TestUpgradeRefusesNewerSchema`；`installFromRepo` 既有测试不变。
**依赖** 无
**验证** `make check`
**负责人** Austin

### T15 人类作废 pending step
- [ ] 完成：

**位置** `internal/coordinator/coordinator.go:1673`（`handleSetSteps`，队列 append-only）、`internal/coordinator/step.go:155`/`:302`/`:388`（每步落地即 `deliverFinal`）、`internal/project/steps.go:16`（`Step` 结构）、`internal/gotui/planoverlay.go`。
**用户/缺口** 队列一旦双双签署就只能等它跑完：人类看错了一步想作废，只能让 agent 再发新队列（会撤销签署）。
**最小实现** 只允许作废**尚未开始**的 pending step：`Step` 加一个 additive 可选字段（`cancelled`，schema 版本不动），session 命令 + dashboard 确认入口各一个，landed/current 直接拒绝并说明原因，被作废的步在队列推进时跳过。**明确不做**：交付节奏配置（见非目标），不加新状态机、不新增持久化文件。
**验收** `internal/coordinator` 新增 `TestHumanCancelsPendingStep`、`TestCancelRefusesCurrentAndLanded`、`TestCancelledStepSkipped`；`internal/gotui/step_ui_test.go` 覆盖确认面板；resume 后作废状态保持。
**依赖** 无
**验证** `make check`
**负责人** Tony

### T18 i18n 完整性检查
- [ ] 完成：

**位置** `internal/gotui/i18n.go`（zh 表）、`internal/gotui/theme.go:58`（`tr`，缺键回退英文）、用户可见文案来源：`internal/gotui/overlay.go:162`（`helpSections`）、`internal/gotui/slash.go:18`（命令名/参数/描述）、notice/status 文案常量；现有覆盖 `internal/gotui/status_zh_test.go`。
**用户/缺口** zh 覆盖靠人工记忆，新面板/新提示随时漏翻译，界面中英混排（上一轮就漏过 `/status`）。
**最小实现** 一个测试遍历上述**用户可见文案来源**（help 段落、命令表、notice/status 常量），断言 zh 表都有对应键；缺了就补翻译。因为只遍历这些来源，"不该翻译的字符串"由构造排除，**不做豁免白名单机制**。
**验收** `internal/gotui/i18n_test.go` 新增 `TestUserVisibleStringsHaveZh`；现有人工覆盖测试保留。
**依赖** 无
**验证** `make check`
**负责人** Tony

---

## 非目标（刻意不做）

下列内容不是"待办"，不要再为它们立项；右边是依据或实测数字。

| 不做的事 | 依据 / 实测 |
| --- | --- |
| Windows 运行时支持（原 T17 全量切片） | `GOOS=windows go build ./...` 现在就有 2 个基础包编不过（依赖它们的一切都跟着失败）：`internal/sessionstore/lock.go:54`（`unix.Flock`）、`internal/agent/session.go:58`（`syscall.Dup`）、`:65-72`（`CloseOnExec`/`SetNonblock`）、`:497`/`:664`（`syscall.Kill`）。这是 ConPTY 级移植，不是门禁补丁；只在有真实用户明确需求时再评估，不写"预研切片/结论文档"。 |
| STEP 交付节奏配置（原 T15 的一半） | 新增一个配置项换一个行为就是过度设计。每步落地即交付是既有不变量（`internal/coordinator/step.go:155`），需要整段交付的人走 `duo apply --branch/--pr`（v0.14.1 已交付）。 |
| 远程签 plan/ready/verification | 签名是 agent 活连接的授权与证据链（`internal/session/command.go` 顶部注释明确排除 gate 动词）。人不在键盘前时，正确的解除方式是 pause/continue（T24），不是替 agent 签字。 |
| i18n 豁免白名单机制 | 只遍历用户可见文案来源即可，缺就补；白名单是给未来自己加的债（T18）。 |
| `--format html` 导出 | Markdown 足够归档/评审；HTML 会引入自带样式与资源内联的第二套渲染（T11）。 |
| 批量任务文件 / 并发多会话 / 插件市场 / 自研 diff 浏览器 | headless `duo run` 已可被脚本串起来；多会话守护与插件市场是独立产品方向，当前没有用户证据。 |
| 新第三方依赖（含 lint 平台） | 现有 9 个直接依赖够用；CI 用 `gofmt` + `go vet` + 各扩展自带测试即可（T22）。 |
| 按行数拆分 `coordinator.go`（2744）/ `service.go`（1494）/ `app.go`（1038） | 纯搬家，审查与冲突成本高于收益；只在这些文件下一次真实改动时顺手拆，不单独立项。 |
| 清理 227 处 `_ =` | 多数是有意忽略（日志/清理失败不影响主流程），逐条审计的收益无法度量；新代码继续显式忽略。 |
| 驱动插件基类/共享框架 | 4 个插件的共同点已在 `internal/driver/cmdline.go:15/22/76`；再抽象只会把 CLI 差异塞进 Core。 |

### 实测"不是瓶颈"，禁止为此加缓存/异步/批量/分段加载

| 位置 | 实测（本机，f9a33e2785） | 结论 |
| --- | --- | --- |
| `state.json` 保存（`internal/sessionstore/store.go:121` → `:139` temp + fsync + rename + dir fsync） | 30µs @6KB / 67µs @29KB（200 步计划） | 不批量写、不异步写、不合并写 |
| `duo --version` / `duo sessions` 冷启动 | 1.6–2ms（各 3–5 次） | 不预加载、不加常驻守护 |
| `Service.View()`（`internal/session/service.go:721`：Snapshot + UsageLedger + observerView） | µs 级 | 不加视图缓存层、不加脏标记 |
| `transcript.SetEntries` 无变化短路（`internal/gotui/transcript.go:70`） | 400 条比较 µs 级 | 不改（T21 只处理"有变化"的那条路径） |
| 远程 state 帧 | `internal/remote/protocol.go:19` `MaxPlaintext = 3000`；`internal/remote/client.go:810` `fitState`；push 合并 1 次/秒 | 不做增量 diff 协议、不加帧压缩 |
| TUI 会话条目 | 窗口上限 400（`internal/gotui/app.go:1025`） | 不加分段加载/虚拟列表 |
| 事件 journal 追加 | 每事件一次 `O_APPEND`（`internal/sessionstore/events.go:49`）；恢复时 `TUIEntries`（`:97`）全文件扫描：合成 38MB / 3 万条 journal 的一次性观察 71ms（非可复现基准） | 不做轮转/压缩；恢复超过 1s 再议 |
| 测试体量 | 894 个 `Test`、0 个 `Benchmark` | 只补 T21 一条必要基准，不建基准平台 |

---

## 历史（已完成；压缩为一行，细节见 CHANGELOG）

- **T1** MCP 桥补齐 `duo_set_steps` 与 `duo_restart_duo` — 完成：6cb62de · 2026-10-23
- **T2** 驱动 capability 声明：提示词只宣传 agent 调得到的工具 — 完成：18c8d98 · 2026-10-23
- **T3** `duo doctor` 环境自检命令（13 项检查 + 诊断码 + 修复建议） — 完成：c11225b7e1f1 · 2026-10-23
- **T4** `duo apply --branch <name>`（+ `--keep`） — 完成：5599d12 · 2026-10-24
- **T5** `duo apply --push`（远端预检、非强推、回读验证） — 完成：f6f0dbe · 2026-10-24
- **T6** `duo apply --pr`（无托管依赖，`gh` 缺失时打印 PR 正文） — 完成：e87b15b · 2026-10-24
- **T7** snapshot 迁移链：schema 3 + v2→v3 迁移，升级不孤立老会话 — 完成：b582627 · 2026-10-24
- **T8** "需要人类"通知（BEL + OSC 777，四类 attention 原因码） — 完成：4a9d5f0e95 · 2026-10-23
- **T9** 用量与成本账本 + 上下文压力预警（`/cost`、侧栏、resume 续算） — 完成：5eed06850285 + b4f1f0cbcd · 2026-10-23
- **T12** headless：`duo run` + `duo status --json` — 完成：b12f0c9 + 9415d16 · 2026-10-24
- **T16** 端到端会话测试补强 + 崩溃边界（session.Service 全模式、journal 重放、recovery 幂等） — 完成：bfe1836 + 33607ef + f7f5ee1 · 2026-10-24
- **T17** Windows 全量支持 — 未做，转"非目标·按需求再评估"
