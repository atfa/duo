# Duo

> 一个通过隔离 Git worktree 协调两个平级 Coding Agent 的终端工具。

**Duo 让两个 coding agent 以平级伙伴的方式协作，而不是把一个 Agent 设为 Planner、另一个设为 subordinate worker。** 可从以下三种工作流启动，并可在运行中随时切换，无需重启 Agent：

- **Fast（默认）。** Austin 负责实现，Tony 独立验证那个确切的 commit。`RUNNING → VERIFY → DONE`，没有共享 Plan、没有双重签字——大多数任务走这条路。
- **Goal（`duo --mode goal`）。** 完整的协商式工作流：共享 Plan、隔离 worktree、交叉 Review、双重签字，`PLAN → EXECUTE → REVIEW → INTEGRATE → DONE`。
- **Step（`duo --mode step`）。** 先制定有序队列，再让每个条目按自己的 **Fast** 或 **Goal** 策略执行；每一步通过后即落地到你的分支，步骤之间无需人工再次输入任务。

三种模式下，Agent 都会在不停下的前提下实时互发消息、在隔离的 Git worktree 中各自工作，最终被验证的成果会交付回你启动 Duo 的那个仓库。

运行时由 Go 协调核心、轻量 driver 插件和可选的 Agent bridge 组成。Duo 只强制那些确实需要确定性协调的东西——身份、路由、模式与阶段流转、验证、签字、Git 证据——其余部分留给模型自然地自行协作。



## 快速开始

```bash
cd /path/to/git/repo
duo                 # FAST（默认），gotui 仪表盘
duo --mode goal     # 完整协商式工作流
duo --mode step     # 复杂任务：先列队列，再逐步落地
```

然后在 composer 里输入任务，按 `Enter`。**Fast** 下只发送给 Austin；**Goal** 与 **Step** 下会同时发送给 Austin 和 Tony，让双方共同设计计划或队列。

- **Fast** 下，Austin 在自己的 worktree 中实现并 commit，然后调用 `duo_set_status` 请求验证；Tony 用 `duo_set_verification` 验证那个确切的 commit，给出通过或一个具体问题。之后 Duo 交付被验证的 commit 并标记 `DONE`。
- **Goal** 下，Austin 和 Tony 同时收到任务，用 `duo_send` 与 `duo_set_plan` 商定共享计划，之后由 Duo Core 推进 `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE`。
- **Step** 下，双方先用 `duo_set_steps` 商定有序队列、用 `duo_set_plan` 写计划说明，并给每个条目标记 `[fast]` 或 `[goal]`。Fast 条目由 Austin 实现、Tony 验证；Goal 条目由双方共同实现、交叉审查并签署。Duo 会逐步落地并自动推进队列。

运行要求：

- Git
- macOS 或 Linux —— Duo 直接持有两个 Agent 的 PTY，不需要 `script` 包装
- `PATH` 上至少装有一种受支持的 Agent CLI：[Pi](#agent-drivers)、[agy](#agent-drivers) 或 [opencode](#agent-drivers)
- 可选：`PATH` 上的 `sqlite3`，用于在工作预览中显示 `agy` 的 token 用量

## 安装

**发布版二进制**（macOS/Linux，amd64/arm64；安装 `~/.local/bin/duo`、每个 driver 对应的一个启动 shim 与插件可执行文件，以及 Agent 侧 bridge）：

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

**从源码安装**（先跑测试，再装同样的这些部件）：

```bash
./scripts/install.sh
```

装完 bridge 后请重启所有正在运行的 Pi 与 opencode 进程。从源码构建还需要 Go 1.24+。

## 协作如何运作

会话可启动为 Fast（默认）、Goal 或 Step 模式，并可随时用 `/mode fast|goal|step`（`/escalate` 是切换至 Goal 的简写）切换。运行中的一轮不能一半按一种工作流、一半按另一种：切换会重置当前计划、队列、阶段与所有签字，并按新模式的第一阶段重新开始；worktree 中已有的 commit 会保留。若当前处于 `DONE`，切换只改变下一个任务使用的模式，不会重新打开已完成的一轮。三种模式共用同一套隔离 worktree、Git 证据和交付流程；区别在于交付前需要多少协商。

### Fast（默认）

```text
RUNNING → VERIFY → DONE
```

| 阶段 | 发生什么 | 推进条件 |
|---|---|---|
| **RUNNING** | Austin 在自己的 worktree 中实现并 commit。Tony 只读，可通过 `duo_send` 被征询意见。 | Austin 在 worktree clean 时调用 `duo_set_status ready=true`。 |
| **VERIFY** | Tony 独立检查 Austin 的那个确切 commit。 | `duo_set_verification` 给 `passed`（→ 交付 → `DONE`），或 `issue_found` 并附一个具体 note（→ `RUNNING`）。 |
| **DONE** | 被验证的 commit 已交付进你的仓库。 | 交付成功。 |

Fast 没有共享 Plan，也没有双重签字。一次 `passed` 绑定到当时被请求验证的那个确切 commit；任何新 commit 都会使其失效并把会话退回 `RUNNING`。只有 Austin 会写进被交付的成果——Tony 的 worktree 永远不会被交付。

**切换模式**：人类可在 composer 中输入 `/mode goal`、`/escalate [理由]` 或 `/mode step`，Agent 也可调用 `duo_escalate`，无需重启任一 Agent。在 gotui 中，`/mode` 和 `/escalate` 先切换模式，再询问 **Resume work?**；选择 **Not now** 会让两个 Agent 保持空闲，直到人类提交下一个任务，同时暂停 harness watchdog 的自动提醒。运行中的 Fast → Goal 保留原有的升级行为：会话进入 `PLAN`，用升级原因初始化共享计划，Tony 变为活跃协作者，已有 commit 无损保留。其他切换会按新模式的门禁开始新一轮。

**暂停与切换驱动**：`/pause` 只登记暂停请求，不会打断正在执行的回合。Duo 会扣住新的提示、watchdog 提醒和 peer 消息，并在 Austin 与 Tony 都到达空闲回合边界前显示 `PAUSING`；都停下后 header 显示 `PAUSED`，footer 指明仍在工作的 Agent。`/unpause` 只取消尚未完成的暂停。完全暂停后，`/continue` 是唯一恢复工作的入口：它发送一份 resume prompt，携带当前阶段、计划或队列，以及暂停期间扣住的消息。`/driver` 可在首个任务前、完全暂停时或 `DONE` 后使用，界面与模型选择器一致（`Tab` 切换 Agent、输入过滤、`Enter` 应用、`Esc` 关闭）。切换驱动只重启该 Agent 的进程，不会恢复工作。新驱动的对话从空开始——原生会话状态属于旧驱动——因此暂停期间切换后，`/continue` 会让它读取仓库 `.duo/logs/<session>.md` 与 `.duo/logs/<session>-<agent>.md` 日志来恢复完整上下文。

### Goal（`duo --mode goal`）

```text
PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

| 阶段 | 发生什么 | 推进所需证据 |
|---|---|---|
| **PLAN** | 双方协商一份共享 Plan。允许 provisional 修改。 | 双方批准**同一个** Plan 版本。 |
| **EXECUTE** | 各自在自己的 Git worktree 中独立工作。 | 各自 worktree clean，并记录 commit SHA。 |
| **REVIEW** | 各自 Review 对方的那个 commit。 | 各自批准自己实际 Review 过的那个 peer HEAD。 |
| **INTEGRATE** | Tony 的分支被合并进 Austin 的 integration worktree。 | 双方批准同一个 clean 的 integrated Austin HEAD。 |
| **DONE** | 被批准的产物已交付回你的仓库。 | 交付成功。 |

### Step（`duo --mode step`）

适合一个值得分步落地的大型任务。队列就是契约：

```text
PLAN → (每一步：EXECUTE → REVIEW → INTEGRATE → 落地该步) → DONE
```

| 阶段 | Fast 策略步骤 | Goal 策略步骤 | 推进所需证据 |
|---|---|---|---|
| **PLAN** | 双方共同设计有序队列与文字计划。 | 相同。 | 双方批准**同一个**非空队列版本。每个条目标记 `[fast]` 或 `[goal]`，未标记默认为 Fast。 |
| **EXECUTE** | Austin 只实现当前步骤，Tony 对产物只读。 | 双方都是共同作者，各自在自己的 worktree 实现。 | Fast：Austin 在 HEAD 干净时调用 `duo_set_status ready=true`。Goal：双方都签字。 |
| **REVIEW** | Tony 验证 Austin 的确切 HEAD。 | 双方交叉审查对方的确切 HEAD，并签署 `REVIEW`。 | Fast：`duo_set_verification passed` 落地；`issue_found` 退回 EXECUTE。Goal：双方签字。 |
| **INTEGRATE** | Duo 落地已验证的 HEAD 并推进队列。 | Duo 把 Tony 的分支合并进 Austin 的分支，双方审查并签署集成后的 HEAD；非最后一步在双方签字后落地。 | Fast：交付成功。Goal：双方对同一个干净的集成 HEAD 签字。 |
| **DONE** | 所有步骤都已落地。 | 相同。 | **最后一步**无论采用哪种策略，都必须由双方签署。 |

每个步骤都必须是可提交的增量，其测试命令在该步骤自己的 HEAD 上通过。队列与游标由 Duo 持有——Agent 绝不打勾；已落地的步骤不会被删除或重排；若某步过大，只能在队列末尾追加拆分后的新步骤。

Fast 步骤保留较轻的单验证者门禁：Tony 对确切 HEAD 的独立检查足以保证安全，而让 Austin 为自己的 `INTEGRATE` 共同签字只是自我批准。Goal 步骤使用完整重流程——双方独立实现、交叉审查、合并并对集成结果双签——因为这是双方在 PLAN 中为该步骤选择的策略。最后一步始终保留双签，确保交付前双方都接受完成后的队列。每一步落地后都会进入你启动 Duo 的分支；对 Fast 步骤，Tony 的 worktree 会被快进到落地后的 HEAD，因此他审查和构建的始终是你实际收到的树，而不是过期版本。

三种模式里，阶段都是**checkpoint，不是行为牢笼**：Duo 不会阻止 Agent 提前读代码或提前 Review；阶段只决定"推进需要哪些证据"。

`DONE` 是**这一轮的终点，不是整个会话的终点**。在 Duo composer 里再提交一个任务会为新一轮重新打开会话——Fast 回到 `RUNNING`，Goal 回到 `PLAN`，Step 回到 `PLAN` 并使用新队列——并清掉上一轮已完成的交付 checkpoint，让后续任务可以重新验证、独立交付。而通过 `Ctrl+A` / `Ctrl+T` 直接进入某个 Agent 自己的会话则绕过 Duo，不经过 composer；但在 Fast 模式下这条路依然可恢复：Austin 在那里提交了新代码并请求验证时，Duo 会基于新的 HEAD 开启新一轮，而不是拒绝该请求。若重复请求仍然指向已交付的那个 HEAD，则会被拒绝，避免误发把已交付的会话重新打开。

任何模式下，一轮走到 `DONE`，Duo 都会请 Austin 给你做一次总结：过程实际是怎么走的，以及仓库现在多了什么。Austin 用普通消息回答，因此这份总结在时间线里显示为 `Austin → Human`，和发给你自己的消息一样加粗。每轮只请求一次；下一轮会为自己的交付再总结一次。

Goal 里 Plan 一旦更新就产生新版本，并**使双方签字同时失效**，所以反复改措辞是有可见代价的。Goal 的签字绑定到一个确切的 commit：对方推了新 commit，你之前的 Review 就过期，必须重做。

冲突会显式保留给 Austin 处理，不会被静默覆盖。

## Duo 工具

Austin 和 Tony 共用一套随 Pi bridge 安装的小工具面。这是它们影响共享状态的唯一通道。

| Tool | 适用模式 | 作用 |
|---|---|---|
| `duo_send` | 所有 | 在双方都继续工作的同时，给 Peer 发一条重要的实时消息。用于发现、疑问、冲突和提案，不是日常进度播报。 |
| `duo_set_status` | 所有 | 为当前门禁签字（`ready: true`）或撤销签字（`ready: false`）。Fast 下 Austin 用它请求验证，Tony 必须改用 `duo_set_verification`。Goal 下双方签署每个阶段。Step 下双方批准队列；Fast 步骤使用 Tony 的验证结论，Goal 步骤使用双方签字。 |
| `duo_set_verification` | Fast + Step（仅 Tony） | 对正在 Review 的 Austin 确切 commit 报告 `passed` 或 `issue_found`。`issue_found` 必须附具体 note。Step 下仅适用于 `[fast]` 步骤；`passed` 会落地该步骤并推进队列。 |
| `duo_set_plan` | Goal + Step | 创建或整体替换共享 Plan。每次调用产生新版本并重置双方签字。Fast 下不可用。 |
| `duo_set_steps` | Step | 创建或整体替换步骤队列（markdown 复选框列表），每个条目标记 `[fast]` 或 `[goal]`。每次调用产生新版本并重置双方签字。打勾由 Duo 维护。 |
| `duo_escalate` | 所有 | 携带原因切换至 Goal 模式。Fast → Goal 保留升级时生成的计划；其他切换按 Goal 门禁开始新一轮。 |
| `duo_status` | 所有 | 读取权威的 mode、phase、验证状态、Plan、步骤队列与游标、签字，以及两个 worktree 的 branch、路径、HEAD、clean 状态和 ahead 数。 |

Duo Core 强制这些门禁而不是信任模型：Fast 拒绝 `duo_set_plan`，Fast 下 Tony 的 `duo_set_status` 会被拒绝并提示改用 `duo_set_verification`。由于任一 Agent 都可能在不重启的情况下切换模式，bridge 在所有模式下注册全部工具，且不持有任何项目真相。Pi 事件转成 Duo 活动，Pi 工具转成 Duo 请求，Duo 消息转成 Pi 的 steer。

## 终端界面

Duo 默认且唯一内置的终端前端是 **gotui 仪表盘**：直接运行 `duo` 即可打开。它把分栏工作流/Agent 布局与可切换的时间线合在一起；架构、布局和截图见 [gotui 前端指南](./docs/gotui.md)。老的手搓 TUI 已保存在 `legacy-tui` Git 分支中，仅作参考，`main` 不再构建或安装它。

### gotui 仪表盘

直接运行 `duo` 即打开 gotui；没有单独的 UI 选择参数。

gotui 默认打开 **分栏仪表盘**：侧边栏显示阶段链、验证/交付状态、worktree 与两张 Agent 卡片；Austin 和 Tony 的会话左右并列；下方是 Duo/人类会话区。按 `F2`，或点击 `Split` / `Timeline` 标签，可切换到单一时间线。

**gotui 按键**

| 按键 | 操作 |
|---|---|
| `Enter` | 把 composer 内容发送给 Austin（Fast）或两个 Agent（Goal / Step） |
| `Ctrl+Enter` / `Shift+Enter` / `Alt+Enter` / `Ctrl+J`\* | 在 composer 中插入换行 |
| `@` | 在词首补全仓库中的已跟踪文件；`↑`/`↓` 选择，`Tab`/`Enter` 插入，`Esc` 关闭 |
| `F2` / 点击 `Split` 或 `Timeline` 标签 | 在分栏仪表盘与单一时间线之间切换 |
| `Ctrl+P` | 显示/隐藏工作流侧边栏 |
| `F1` / `Ctrl+/` / `Alt+H` | 打开帮助 |
| `Ctrl+O` / `Alt+O` | 打开会话详情与 Git diffstat |
| `Ctrl+M` / `Alt+M` | 打开模型与思考强度选择器 |
| `Ctrl+G` | 切换消息时间戳 |
| `Ctrl+A` / `Alt+A` | 进入 Austin 自己的 Agent 会话 |
| `Ctrl+T` / `Alt+T` | 进入 Tony 自己的 Agent 会话 |
| `Ctrl+]` / `Ctrl+\` / `Ctrl+】` | 从 Agent 自己的会话返回 gotui |
| `Ctrl+R` / `Ctrl+Y` | 若 Austin / Tony 进程已退出或失败，重启它 |
| `Ctrl+X` | 复制 Austin 最新发给人类的消息到剪贴板 |
| `Ctrl+V` | 把系统剪贴板中的 PNG 粘贴为临时图片路径；macOS 使用 `pngpaste`（用 `brew install pngpaste` 安装），Linux 使用 `wl-paste` 或 `xclip`。若没有图片或工具不可用，请用 `Ctrl+A`/`Ctrl+T` 进入原生会话粘贴 |
| `Shift+Tab` | 在模型选择器中直接循环调整当前选中 Agent 的思考强度；主界面不绑定它，因为无法确定目标 Agent |
| `Ctrl+Q` / `Ctrl+D` | 退出 Duo 并保留 session |
| `←` / `→`，`Alt+←` / `Alt+→` | 按字符或按词移动 composer 光标 |
| `Home` / `End` | 移到 composer 当前行的开头/结尾 |
| `Ctrl+U` / `Ctrl+K`，`Ctrl+W` | 删除到当前行开头/结尾，或删除前一个词 |
| `Backspace` / `Del` | 删除前一个/后一个 composer 字符 |
| `↑` / `↓` | 在 composer 各行间移动或选择文件提及；在首行/末行时改为回溯历史任务 |
| `Tab` | 插入四个空格；斜杠或文件面板打开时确认当前项 |
| `PgUp` / `PgDn` | 滚动当前会话窗格（或打开中的弹窗） |
| 鼠标滚轮 / 拖拽 | 滚动指针所在窗格，或划选并复制会话文本 |
| `Esc` | 关闭弹窗、收起斜杠或文件面板，或清除选择/状态提示 |

**Composer 输入。** `Enter` 发送；`Ctrl+Enter`、`Shift+Enter`（终端能区分时）、`Alt+Enter` 和 `Ctrl+J` 插入换行，粘贴的文字也会保留换行。在词首输入 `@` 可补全仓库中的已跟踪文件；`↑`/`↓` 选择，`Tab`/`Enter` 插入。`Ctrl+V` 会把系统剪贴板中的 PNG 读入私有临时文件，并把该路径插入 composer 供 Agent 读取；若没有图片或剪贴板工具不可用，用 `Ctrl+A`/`Ctrl+T` 进入 Agent 自己的终端粘贴。

[gotui 指南](./docs/gotui.md#captures)中的确定性 tcell 截图覆盖仪表盘、时间线、斜杠面板、帮助、详情、模型选择器、暂停状态、驱动选择器、确认对话框和窄终端。

### 斜杠命令（Slash Commands）

在 composer 中输入 `/` 即可实时弹出交互式斜杠命令选择菜单与说明。使用 `↑` / `↓` 循环上下选择候选命令，按 `Tab` 补全命令名，按 `Enter` 确认执行（无参命令直接触发，有参命令如 `/escalate` 和 `/mode` 自动补全前缀便于继续输入参数），按 `Esc` 随时关闭菜单。

| 命令 | 说明 |
|---|---|
| `/escalate [reason]` | 把当前会话切换为 Goal 模式（`/mode goal` 的简写） |
| `/mode` | 打印当前会话模式 |
| `/mode fast\|goal\|step` | 切换会话模式。运行中的一轮会按新模式的第一阶段重置；`DONE` 时切换则作用于下一个任务 |
| `/model` | 打开 Austin/Tony 模型与思考强度选择器（等同于 `Ctrl+M`） |
| `/pause` | 等双方当前回合结束后暂停；期间新提示、peer 消息和 watchdog 提醒都会被扣住 |
| `/unpause` | 取消尚未完成的暂停 |
| `/continue` | 用一份包含状态上下文的 resume prompt 恢复已完全暂停的会话 |
| `/driver` | 首个任务前、完全暂停时或 `DONE` 后可用，为 Austin 或 Tony 打开驱动选择器（`Tab` 切换 Agent，`Enter` 应用） |
| `/overview` | 切换会话总览与 Git 变更统计详情（等同于 `Ctrl+O`） |
| `/preview` | 展开或折叠 gotui 的工作流侧边栏（等同于 `Ctrl+P`） |
| `/timestamps` | 切换时间线消息时间戳显示（等同于 `Ctrl+G`） |
| `/copy` | 复制 Austin 最新发给人类的消息到剪贴板（等同于 `Ctrl+X`） |
| `/help` | 打开 Duo 交互式帮助大屏（等同于 `Ctrl+/`） |
| `/status` | 打印当前权威的模式、阶段与 worktree 状态 |
| `/clear` | composer 里只有这条命令本身，因此已经是空的；Duo 会强制整屏重绘并给出提示 |
| `/quit` 或 `/exit` | 退出 Duo 并保留当前会话状态 |

未知命令会作为错误提示。`//text` 与绝对路径（例如 `/usr/bin/python`）会当作任务发送（Fast 发给 Austin；Goal 与 Step 发给双方）。

提交过的任务会追加到 `~/.duo/history`（可用 `DUO_HISTORY_FILE` 覆盖），因此 `↑`/`↓` 的历史回溯在重启或 resume 之后依然有效。

模型选择器（`Ctrl+M`；若终端把 Ctrl+M 识别成 Enter，可用 `Alt+M`）列出**目标 Agent 自己那个 driver** 所上报的模型目录——即 Duo 启动该 Agent 所用的同一套安装（`pi --list-models`、`agy models`、`opencode models`），因此同一会话里 Austin 与 Tony 可以各自使用不同目录。输入即可过滤，用 `↑`/`↓`（或 `PgUp`/`PgDn`、`Home`/`End`）移动。用 `Tab` 在 Austin 与 Tony 之间切换；用 `Space` 选择模型但不关闭；用 `Shift+Tab` 直接提升当前 Agent 的思考强度，无需切换窗口；`Ctrl+T` 仍可切换到显式的 Models / Thinking 标签；用 `Enter` 应用模型（在 Thinking 标签中则提升强度）；用 `Esc` 关闭。模型行会在模型名后显示当前档次的原始值（`off`、`minimal`、`low`、`medium`、`high`、`xhigh`、`max`）。关闭选择器后仍保留当前 Agent。模型和思考强度会与对应 driver 一起写入 `.duo/config.json`，下一次在该仓库启动时自动加载。在 `pi` 上切换是实时的：Pi 保留对话，并把模型与思考强度记入 session 记录，因此重启或 resume（`Ctrl+R`/`Ctrl+Y`）后仍然有效。`agy` 与 `opencode` 会在 Agent 启动时应用模型和思考强度，因此 Duo 会重启该 Agent——对话仍会保留（session id 已持久化），但正在进行的工作会被打断。`▶` 标记选择器光标，`●` 标记目标 Agent 当前使用的模型。

Agent 输出按轻量 markdown 渲染：标题、引用、链接以及粗体/斜体/行内代码都有样式，markdown 表格会画出对齐的真实边框。表格宽度超过时间线时，Duo 会折行最宽的单元格，而不是截断内容；折行的列表项会保持悬挂缩进，续行对齐在条目正文下方。

鼠标划选复制支持终端标准 OSC 52 转义序列（在远程 SSH、tmux 以及 Ghostty/WezTerm/Alacritty/Kitty 等现代终端中原生直通）并自动回退至宿主系统剪贴板工具（macOS 下 `pbcopy`、Wayland 下 `wl-copy`、X11 下 `xclip`/`xsel`）。

gotui 用 `F1`、`Ctrl+/` 或 `Alt+H` 打开 Help 弹窗，用 `↑`/`↓`、`PgUp`/`PgDn`、`Home`/`End` 滚动，用 `Esc` 关闭；它是仪表盘快捷键与工作流的精简参考。完整用法、全部环境变量以及每个命令的详细说明见 `duo --help`。

\* 只有当终端能明确区分上报该组合键时才会插入换行（`\x1b[13;2u` CSI-u 或 Duo 启用 modifyOtherKeys 后的 `\x1b[27;2;13~`）。若你的终端把 `Shift+Enter` 发成裸 `\r`，它会**提交**而不是换行——这种情况请改用 `Ctrl+Enter` 或 `Ctrl+J`（始终插入换行）。`Ctrl+M` 有同样的限制：只有支持上述模式的终端才会把它上报成带 Ctrl 的 `m` 键（`\x1b[27;5;109~` 或 `\x1b[109;5u`），其余终端上与 `Enter` 无法区分——这种情况请用 `Alt+M`。

## 配置

每一项都有可用默认值，`duo` 不需要任何配置就能跑。

| 变量 | 默认值 | 含义 |
|---|---|---|
| `DUO_MODE` | `fast` | 会话模式：`fast`、`goal` 或 `step`。命令行 `--mode`/`-m` 优先；`duo resume` 保持持久化的模式。 |
| `DUO_TEST_COMMAND` | （无） | 自动化测试门禁命令，在验证/交付前确定性执行（命令行 `--test-cmd`/`-t` 优先）。 |
| `DUO_REPO` | 当前目录 | 启动时针对的仓库或子目录。命令行路径参数优先。 |
| `DUO_SESSION` | 时间戳 + 随机 hex | 会话 id，格式 `YYYYMMDD-HHMMSS-xxxxxxxx`。 |
| `DUO_WORKTREE_ROOT` | `~/.duo/worktrees/<repo>-<hash>/<session>` | 创建 Austin/Tony worktree 的位置。 |
| `DUO_BASE_REF` | `HEAD` | worktree 从哪个 ref 切出。 |
| `DUO_PI_COMMAND` | `pi` | 启动每个 Pi Agent 所用的命令。 |
| `DUO_DRIVER` | `pi` | 默认 Agent driver：`pi`、`agy` 或 `opencode`。按 Agent 的参数与配置优先。 |
| `DUO_LISTEN` | `127.0.0.1:0` | Bridge 监听地址（默认由系统分配端口）。 |
| `DUO_HARNESS` | `true` | 启用 idle/stall 看门狗，把失去动力的协调推回来。 |
| `DUO_HARNESS_IDLE_SECONDS` | `15` | 静默多久算 idle。 |
| `DUO_HARNESS_STALL_SECONDS` | `300` | 多久没有进展算 stall。 |
| `DUO_HARNESS_COOLDOWN_SECONDS` | `30` | 两次 harness 提醒之间的最小间隔。 |
| `DUO_HARNESS_RESUME_GRACE_SECONDS` | `45` | `duo resume` 之后的额外宽限期，避免把重连误判成 stall。 |
| `DUO_HISTORY_FILE` | `~/.duo/history` | composer 历史任务记录，用 `↑`/`↓` 回溯。 |

启动非默认 Pi 命令：

```bash
DUO_PI_COMMAND='pi --some-flag' duo
```

Duo 通过 `.git/info/exclude` 隐藏自己的 `.duo/` 目录，因此不会修改你的
`.gitignore`，也不会让工作区变脏。

### Agent drivers

Duo 可以驱动你安装的任意一种 Agent CLI。三者使用同一套 Duo 协调协议，
因此模式、工具、证据规则与交付流程完全一致 —— 区别只在于 Duo 如何启动和观测 Agent。

| Driver | 选择方式 | Duo 的通信方式 |
|---|---|---|
| `pi`（默认） | `--agent pi` | Pi bridge 扩展通过本地 socket 回连。完整流式能力：工具活动、peer 消息与模型切换都是实时的。 |
| `agy` | `--agent agy` | Google Antigravity CLI。Duo 观测对话并把消息写进 PTY。Token 用量显示需要 `PATH` 上的 `sqlite3`。 |
| `opencode` | `--agent opencode` | opencode 插件通过本地 socket 回连，与 Pi bridge 同一机制。 |

单 Agent 的 driver 没有对应环境变量：
`DUO_DRIVER` 同时作用于两个 Agent；要分别指定，请用 `--austin-driver` /
`--tony-driver`，或在 `.duo/config.json` 里写 `agents.austin.driver` /
`agents.tony.driver`。

可以为每个 Agent 单独指定，从而混用，例如
`duo --austin-driver opencode --tony-driver pi`：

```bash
duo --agent opencode                       # 两个 Agent 都用 opencode
duo --austin-driver opencode --tony-driver pi
```

也可以在项目级 `.duo/config.json` 中持久化：

```json
{
  "driver": "opencode"
}
```

`opencode` 有两点值得注意：

- **会话身份是探测得到的，而不是指定的。** opencode 的 session id 由服务端分配，
  因此首次运行时 Duo 让 opencode 自己生成一个，从 bridge 读到后随会话持久化。
  之后每次运行（包括崩溃后 `duo resume`）都会带上 `--session <id>`，让 Agent 保留上下文。
- **模型由你决定。** Duo 不会为 opencode 注入默认模型，而是沿用你的 opencode 配置
  或模型目录解析出的结果。在 Duo 中选择的模型会在启动时通过 opencode 的配置环境传入，
  因为它的 TUI 没有 `--model` 参数。

Duo 以自动批准工具调用的方式运行 Agent（opencode 用 `--auto`，agy 用
`--dangerously-skip-permissions`），因为它们要在各自隔离的 worktree 中无人值守地工作。
`duo plugins` 会列出内置 driver，以及在 `~/.duo/plugins/` 或 `PATH` 中发现的
`duo-plugin-<name>` 可执行文件（旧拼写 `duo-driver-<name>` / `duo-<name>` 同样被接受）。

opencode bridge 由安装脚本部署到 `~/.config/opencode/plugin/duo/`。
安装后请重启所有正在运行的 opencode 进程。

### MCP 服务（`duo mcp-server`）

如果某个 Agent CLI 原生支持 Model Context Protocol，它可以通过
`duo mcp-server` 驱动同一套状态机：该命令用 JSON-RPC 2.0 在 stdio 上提供上面的六个
Duo 工具，并把调用转发给当前会话的 bridge：

```bash
duo mcp-server --agent austin --session <session-id> --token <token> --port <port>
duo mcp-server config --agent tony            # 打印 MCP 客户端配置 JSON
```

连接参数默认取自 `DUO_AGENT`、`DUO_SESSION`、`DUO_TOKEN`、`DUO_HOST` 与
`DUO_PORT`——Duo 启动 Agent 时会设置它们，因此在会话内部启动的客户端无需任何参数。
`duo mcp-server config` 只打印可直接粘贴的 `mcpServers` 配置，不启动服务。

### 配置文件

你可以在全局 `~/.duo/config.json` 或项目级 `.duo/config.json`（或 `.duo.json`）中持久化默认配置：

```json
{
  "mode": "fast",
  "language": "zh",
  "testCommand": "go test ./...",
  "driver": "pi",
  "piCommand": "pi",
  "agents": {
    "austin": {
      "command": "pi",
      "driver": "pi",
      "model": "anthropic/claude-3-7-sonnet",
      "thinking": "high"
    },
    "tony": {
      "model": "openai/o3-mini",
      "thinking": "medium"
    }
  },
  "harness": {
    "enabled": true,
    "idleSeconds": 15,
    "stallSeconds": 300,
    "cooldownSeconds": 30
  }
}
```

可用 `language` 设置 Duo 内置消息语言（`zh` 或 `en`），默认是 `en`。命令行 `duo --language zh`（或 `duo -l zh`） 会覆盖配置文件；此设置会立即改变 Duo 发给 Austin、Tony 和用户的内置消息以及 gotui 界面文案，并在之后的启动中保持；FAST/GOAL/STEP、Agent 名和阶段名等协议词保留标准拼写。

`agents.<name>.command` 只覆盖这一个 Agent 的启动命令，`agents.<name>.driver`
只覆盖它自己的 driver；顶层的 `driver` 与 `piCommand` 则是两个 Agent 的共同默认。
除非命令里已经带了这些参数，Duo 会把选定的模型与思考强度交给 driver；
opencode 的 TUI 没有 `--model` 参数，因此模型通过它的配置环境传入。

优先级顺序：显式命令行参数（`--mode`/`-m`, `--language`/`-l`, `--test-cmd`/`-t`, `--agent`/`-a`, `--austin-driver`/`-A`, `--tony-driver`/`-T`）> 环境变量（`DUO_*`）> 仓库 `.duo/config.json` > 全局 `~/.duo/config.json` > 内置默认值。

Duo 也会**写入**项目级 `.duo/config.json`：每次启动都把各 Agent 当前生效的 driver
与模型记录进去，因此在选择器里选过的模型下次运行仍然有效。若某个模型属于上一个
driver，切换 driver 时它会被丢弃——那个 id 对新的 CLI 没有意义。由于 `.duo/`
已通过 `.git/info/exclude` 排除，这些写入不会出现在 `git status` 里。

## 数据存放在哪里

```text
~/.duo/sessions/<repo-id>/<session-id>/
    state.json      mode、phase、验证/Plan、签字、证据、worktree 路径与分支、driver 私有的恢复状态
    events.jsonl    阶段、签字、bridge 与 merge 事件的诊断日志，
                    同时保存 resume 时回放进 TUI 的 pane 记录
    duo.log         生命周期输出（session token 写入前已脱敏）
    lock           持有属主 PID 与 hostname 的 advisory flock

~/.duo/worktrees/<repo>-<hash>/<session>/
    austin/         Austin 的 worktree —— 同时也是 integration worktree
    tony/           Tony 的 worktree

~/.duo/config.json  你可以编辑的全局配置
~/.duo/history      composer 历史任务（用 ↑/↓ 回溯）
~/.duo/plugins/     可选的 `duo-plugin-<name>` 可执行文件
```

会话目录以仅属主权限创建，并记录项目状态，详见 [SECURITY.md](./SECURITY.md)。

## 工作 Scope

Duo 把 Git 边界和 Agent 的默认工作目录分开。从子目录启动时，Git 边界仍是仓库根，但该子目录成为 Agent 的默认 cwd：

```bash
cd repo/packages/web
duo
```

此时 Austin/Tony 从各自 worktree 中对应的 `packages/web` 开始工作，而 branch、worktree 与 delivery 的 Git 边界仍是仓库根。

Scope 是**默认工作目录，不是文件系统沙箱**。任务确实需要时，Agent 仍能访问仓库的其他部分。Scope 会记录进会话，并在 resume 时恢复。

## 崩溃后恢复

```bash
duo resume                # 恢复本仓库未完成的会话
duo resume <id>           # 恢复指定会话
```

裸 `duo` 永远启动一个**新**会话，并拒绝覆盖未完成的会话。`duo resume` 在恰好只有一个未完成会话时自动选中它；有多个时要求你给 id，而不是猜。

Resume 会读回持久化快照，**拿 Git 验证它**，并在启动任何进程之前撤销所有已无法证明的签字或验证。模式随会话持久化：`duo resume` 保持它，没有记录 mode 的旧会话按 Goal 恢复，且 `DUO_MODE` 在 resume 时被忽略（显式传入冲突的 `--mode` 会报错）。对账后的状态会在 Agent 启动前先写回，因此启动过程中崩溃不会复活被撤销的批准、也不会重放一次 merge。每个重连的 Agent 会收到一条与模式和阶段对应的唤醒消息；重复重连不会重复投递。每个 pane 最近的 200 条输出会被回放进 TUI，所以恢复后的会话一开始就能看到之前的对话。

恢复刻意保守：当它无法证明某个批准仍然成立时，宁可撤销也不轻信。所以崩溃后请预期要重新签一次，而不是静默放行。

## 交付最终结果

`DONE` 意味着最终结果已交付进你启动 Duo 的那个仓库——Fast 下是被验证的 Austin commit，Goal 下是双方签字的 integrated HEAD。交付**只做 fast-forward**（`git merge --ff-only`）。Duo 从不产生 merge commit、不 rebase、不改写你的历史。若交付被阻，可用 `duo apply` 手动重试。

不修改任何仓库文件的任务是 no-op：一旦最终 commit 已是你 HEAD 的祖先，即使工作树是脏的，交付也会成功并记录 `DONE`。任何真正会移动分支的交付仍受下列安全检查约束。

以下情况交付会**拒绝执行**，并让你的仓库完全保持原样：

- 仓库有未提交改动；
- 当前分支与 Duo 启动时的分支不同；
- 当前 HEAD 已与最终 Duo 结果分叉；
- HEAD 处于 detached 状态，或起始分支未知；
- 最终结果并非派生自记录的 base commit。

如果你自己把分叉解决了，Duo 会认账而不是继续拒绝：只要最终 Duo commit 已成为你 HEAD 的祖先——包括用 `git merge --no-ff` 保留了两条历史——`duo apply` 就把它视为已交付，记录 `DONE`，并且不再移动你的分支。交付被拒时会把这条 merge 命令和重试命令一起打印出来：

```bash
git merge --no-ff <final-head>   # 在你启动 Duo 的那个仓库里执行
duo apply
```

在此之前，交付被拒时会话保持在 `INTEGRATE`（Goal）或 `VERIFY`（Fast），写入一个 `pending` checkpoint，并打印确切的重试命令：

```bash
duo apply                    # 重试本仓库的待交付
duo apply <session-id>       # 重试指定会话
```

`duo apply` 从不启动 Agent，并复用同一套安全规则——它不会强行推进一个被阻塞的交付。

## 查看与清理会话

Duo 将会话快照持久化在 `~/.duo/sessions/`，将隔离的 Git 工作树存放在 `~/.duo/worktrees/`。你可以随时列出会话或清理磁盘空间：

```bash
duo sessions                 # 查看当前仓库的所有会话
duo sessions --all           # 查看所有仓库的会话
duo clean                    # 清理当前仓库已完成（DONE）的会话
duo clean <session-id>       # 清理指定的某个会话
duo clean --all             # 可接受，但裸 duo clean 本来就会清理全部已完成会话
duo clean --all-repos       # 清理所有仓库中已完成的会话
duo clean --force           # 连同未完成会话一起清理（自动跳过当前运行中的活跃会话）
duo clean --dry-run         # 仅预览将被清理的内容，不修改磁盘
duo logs                    # 列出写入 .duo/logs 的 Markdown 会话记录
duo logs <repository>       # 列出另一个仓库的会话记录
```

清理会话会自动安全移除对应的 Git 工作树（`git worktree remove --force`）、执行 `git worktree prune` 修剪、删除临时分支（`duo/<session>/*`），并删除对应的会话快照目录。正在被另一个运行中的 Duo 进程持有的会话会被 `flock` 锁保护，自动予以跳过。

`duo clean` 默认就会清理本仓库全部已完成（DONE）的会话，因此 `--all` 目前不改变任何行为；真正把未完成会话也纳入的是 `--force`。`duo sessions` 接受 `-a`（`--all`）；`duo clean` 另外接受 `-f`（`--force`）与 `-n`（`--dry-run`）；`duo apply <session-id>` 可指定会话。每个命令都支持自己的 `--help`（例如 `duo clean --help`）；在仓库目录里执行 `duo <repository> --help` 同样会打印完整用法。

其余命令不涉及会话：`duo plugins` 列出内置 driver 与所有已发现的外部插件，`duo plugins test [<path…>] [--all] [--verbose]` 按 [Driver 插件协议](./docs/driver-plugin-protocol.md) 检查单个或全部 driver，[`duo mcp-server`](#mcp-服务duo-mcp-server) 通过 MCP 提供 Duo 工具，`duo version`（`--version`）与 `duo help`（`-h`、`--help`）分别打印版本号与完整用法。

## 使用边界

Duo 固定使用 Austin 与 Tony 两个角色。可在 Fast、Goal 与 Step 之间随时切换；运行中的一轮切换后会按新模式的第一阶段重置。崩溃恢复不会重建 Agent 的推理过程；Duo Core 自身崩溃后需手动运行 `duo resume`。会话绑定本机和仓库路径，辅助脚本面向 macOS/Linux。gotui 需要足够大的终端来显示分栏仪表盘，更小的终端会回退到时间线视图；老的手搓 TUI 保留在 `legacy-tui` 分支。交付安全边界与本地 TCP 信任模型见[架构说明](./docs/architecture.md)和 [SECURITY.md](./SECURITY.md)。

## 开发

运行 `make check` 执行 Go 测试、vet 和命令构建。若改动 Pi bridge，也运行
`cd pi-extension && bun test`。gotui 前端另有确定性 SimulationScreen 截图测试，可用以下命令重新生成：

```bash
DUO_GOTUI_CAPTURE_DIR="$PWD/docs/gotui/screens" \
  go test ./internal/gotui -run TestCaptureFrames -count=1
```

推送 `v*` tag 会执行发布验证，并发布 macOS/Linux 的 amd64/arm64 归档。

## 文档

- [架构与关键约束](./docs/architecture.md)
- [gotui 终端前端](./docs/gotui.md)
- [Driver 插件协议](./docs/driver-plugin-protocol.md)与[开发指南](./docs/plugin-development-guide.md)
- [Bridge 协议](./docs/duo-bridge-protocol.md)
- [安全边界](./SECURITY.md)
- [版本历史](./CHANGELOG.md)

## License

MIT — 见 [LICENSE](./LICENSE)。
