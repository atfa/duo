# Duo

> 一个为 Pi 设计的双平级 Coding Agent Runtime。

**Duo 让两个 Pi coding agent 以平级伙伴的方式协作，而不是把一个 Agent 设为 Planner、另一个设为 subordinate worker。** 会话在启动时固定为以下两种工作流之一：

- **Fast（默认）。** Austin 负责实现，Tony 独立验证那个确切的 commit。`RUNNING → VERIFY → DONE`，没有共享 Plan、没有双重签字——大多数任务走这条路。
- **Goal（`duo --mode goal`）。** 完整的协商式工作流：共享 Plan、隔离 worktree、交叉 Review、双重签字，`PLAN → EXECUTE → REVIEW → INTEGRATE → DONE`。

两种模式下，Agent 都会在不停下的前提下实时互发消息、在隔离的 Git worktree 中各自工作，最终被验证的成果会交付回你启动 Duo 的那个仓库。

运行时由一个 Go 协调核心加一层刻意做薄的 Pi bridge 组成。Duo 只强制那些确实需要确定性协调的东西——身份、路由、模式与阶段流转、验证、签字、Git 证据——其余部分留给模型自然地自行协作。

- **默认 Fast，需要时 Goal。** Fast 保留安全边界——worktree、Git 证据、验证后交付——同时去掉繁文缛节；Goal 为更大的任务加上协商式规划与双重签字。
- **平级，而非层级。** 没有固定的 Planner。任何一方都可以反对，分歧本身就是流程的正常部分。
- **证据优先于口头声明。** 一个干净的 commit SHA 比 Agent 说"做完了"更有价值。验证和签字都是对 Git 校验过的，不是被无条件相信的。
- **隔离，且绝不破坏。** Austin 和 Tony 从不共用工作树；Duo 也不会改写你启动它的那个仓库的历史。
- **持久。** 会话能扛住崩溃，恢复后模式、worktree 和 Pi 对话身份都还在。

当前版本：**v0.5.1** — 完整历史见 [CHANGELOG.md](./CHANGELOG.md)。

## 快速开始

```bash
cd /path/to/git/repo
duo              # FAST（默认）
duo --mode goal  # 完整协商式工作流
```

然后在 composer 里输入任务，按 `Enter`。composer 始终只发送给 **Austin**。

- **Fast** 下，Austin 在自己的 worktree 中实现并 commit，然后调用 `duo_set_status` 请求验证；Tony 用 `duo_set_verification` 验证那个确切的 commit，给出通过或一个具体问题。之后 Duo 交付被验证的 commit 并标记 `DONE`。
- **Goal** 下，Austin 用 `duo_send` 唤醒 Tony，两人商定一份共享 Plan，之后由 Duo Core 推进 `PLAN → EXECUTE → REVIEW → INTEGRATE → DONE`。

运行要求：

- Git
- `pi` 在 `PATH` 上（可用 `DUO_PI_COMMAND` 覆盖）
- macOS 或 Linux —— Duo 直接持有两个 Pi 的 PTY，不需要 `script` 包装

## 安装

**发布版二进制**（macOS/Linux，amd64/arm64；安装 `~/.local/bin/duo` 与 Pi bridge）：

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

**从源码安装**（先跑测试，再装同样的两部分）：

```bash
./scripts/install.sh
```

装完 bridge 后请重启所有正在运行的 Pi 进程。从源码构建还需要 Go 1.22+。

## Duo 与常见多 Agent 框架的区别

常见模式：

```text
Planner
 ├─ Worker A
 └─ Worker B
```

Duo：

```text
            用户
             │
             ▼
           Austin
             │ 唤醒
             ▼
Austin  ◄──────────►  Tony
   │       实时通信       │
   └──────────┬───────────┘
              ▼
 FAST： RUNNING → VERIFY → DONE
 GOAL： PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

没有 Planner 来分发任务：用户和 Austin 对话。Goal 模式里 Austin 唤醒 Tony，两人直接互发消息，任何一方都不能代替对方签字。Fast 模式里 Austin 是 driver，Tony 是只读 verifier，只能通过或拒绝正在被 Review 的那个确切 commit。

Duo Core 负责的是一小组可靠的"制度"：模式、阶段、验证、签字、成果证据、worktree、harness、集成与交付。至于怎么讨论、怎么分工、要不要先做个实验，仍由 Austin 和 Tony 自主决定。

## 协作如何运作

会话在启动时固定一种**模式**，整个生命周期不变。两种模式共用同一套隔离 worktree、Git 证据和交付流程；区别在于交付前需要多少协商。

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

两种模式里，阶段都是**checkpoint，不是行为牢笼**：Duo 不会阻止 Agent 提前读代码或提前 Review；阶段只决定"推进需要哪些证据"。

`DONE` 是**这一轮的终点，不是整个会话的终点**。在 Duo composer 里再提交一个任务会为新一轮重新打开会话——Fast 回到 `RUNNING`，Goal 回到 `PLAN`——并清掉上一轮已完成的交付 checkpoint，让后续任务可以重新验证、独立交付。而通过 native Pi（`Ctrl+A` / `Ctrl+T`）直接和某个 Agent 对话则**故意不这么做**：那条路径绕过 Duo，阶段保持不变。

Goal 里 Plan 一旦更新就产生新版本，并**使双方签字同时失效**，所以反复改措辞是有可见代价的。Goal 的签字绑定到一个确切的 commit：对方推了新 commit，你之前的 Review 就过期，必须重做。

冲突会显式保留给 Austin 处理，不会被静默覆盖。

## Duo 工具

Austin 和 Tony 共用一套随 Pi bridge 安装的小工具面。这是它们影响共享状态的唯一通道。

| Tool | 适用模式 | 作用 |
|---|---|---|
| `duo_send` | 两种 | 在双方都继续工作的同时，给 Peer 发一条重要的实时消息。用于发现、疑问、冲突和提案，不是日常进度播报。 |
| `duo_set_status` | 两种 | Goal 下为当前阶段签字（`ready: true`）或撤销签字（`ready: false`）。Fast 下 Austin 用 `ready: true` 请求验证；Tony 的签字会被拒绝并给出指引。 |
| `duo_set_verification` | Fast（仅 Tony） | 对正在 Review 的 Austin 确切 commit 报告 `passed` 或 `issue_found`。`issue_found` 必须附具体 note。 |
| `duo_set_plan` | Goal | 创建或整体替换共享 Plan。每次调用产生新版本并重置双方签字。Fast 下不可用。 |
| `duo_status` | 两种 | 读取权威的 mode、phase、验证状态、Plan、签字，以及两个 worktree 的 branch、路径、HEAD、clean 状态和 ahead 数。 |

Duo Core 强制这些门禁而不是信任模型：Fast 拒绝 `duo_set_plan`，Fast 下 Tony 的 `duo_set_status` 会被拒绝并提示改用 `duo_set_verification`。Bridge 是一层薄适配器——Pi 事件转成 Duo 活动，Pi 工具转成 Duo 请求，Duo 消息转成 Pi 的 steer。它刻意不持有任何项目真相。

## 终端界面

Duo 的主界面是一条会话 **时间线**：单一时间顺序的消息流，每条消息都显示在说话者自己那一侧——Austin、人类与 Duo 系统消息靠左，Tony 的消息靠右——上方是按说话方着色的 `说话方 → 接收方` 标题行：发送给人类的消息用说话方本色的高亮粗体，Agent 之间与系统消息则保持暗色。系统通知、harness 提示、错误与验证结论也一律标注方向（如 `Duo → Human`、`Duo → Tony`），不会只显示一个名字。双方使用相同的气泡宽度（约占整行四分之三），因此判断谁在说话看的是消息所在的一侧，而不是更粗的标题样式；Tony 的消息如果一行就能显示完整，会靠右边缘对齐，需要换行的消息则固定左缩进对齐。顶部边框行显示 Duo 解析出的 Git 仓库目录（不是启动目录），无需打开 Help 就能确认本轮的目标仓库。人类任务、peer 消息、验证结论与 Duo 系统消息按到达顺序交织排列，当前模式、阶段、验证/Plan 状态与瞬时反馈位于时间线下方。

一次构建可能很久，而时间线只在 Agent 完成一条消息时变化。因此时间线下方为每个 Agent 保留一条 **工作预览**：标题行是 Agent、带动画的状态与本轮已运行时长——状态 spinner 与 `[↗]` 附着按钮现在都在这里；下方各行显示"此刻在做什么"（正在跑的工具及其已运行时间，或正在思考所用的模型与思考级别）、本轮最近的工具轨迹（✓/✗ 与耗时）、本轮最近一次错误、以及正在流式输出的文本尾巴。工具失败或 provider 报错会立刻在那里显示，等待时不必靠猜。`Ctrl+P` 可以收起这条预览带，把行数还给时间线；终端太矮时会自动隐藏。

| 按键 | 操作 |
|---|---|
| `Enter` | 把 composer 内容发送给 Austin |
| `Ctrl+Enter` / `Shift+Enter`\* | 在 composer 中插入换行 |
| `Ctrl+A` | 进入 Austin 的原生 Pi |
| `Ctrl+T` | 进入 Tony 的原生 Pi |
| `Ctrl+]` / `Ctrl+\` / `Ctrl+】` | 从原生 Pi 返回 Duo |
| `Ctrl+R` / `Ctrl+Y` | 若 Austin / Tony 进程已退出或失败，重启它 |
| `Ctrl+/` | 打开或关闭 Duo Help |
| `Ctrl+P` | 显示或隐藏 Austin/Tony 的工作预览带 |
| `Ctrl+M` / `Alt+M` | 打开 Austin 或 Tony 的模型选择器 |
| `Ctrl+O` | 切换会话总览：worktree、验证或 Plan、交付状态与 Git 变更 diffstat |
| `Ctrl+G` | 切换消息时间戳 |
| `Shift+Tab` | 循环切换目标 Agent 的 Pi 思考强度 |
| `Ctrl+Q` | 退出 Duo 并保留 session |
| `←` / `→` | 移动 composer 光标 |
| `Backspace` | 删除前一个字符 |
| `PgUp` / `PgDn` | 向上/向下滚动会话时间线 |
| 鼠标滚轮悬停在时间线上 | 滚动更早的消息 |
| 鼠标在时间线上拖拽选择 | 选中文本并复制到剪贴板 |

时间线上的每条消息标题默认带上 `HH:MM:SS` 时间（`Austin → Tony · 14:30:05`），一次往来可以完整阅读；`Ctrl+G` 可隐藏或恢复这些时间。

composer 支持多行，一次最多显示四行。原生接管是全屏接管：在 Pi 里，`/model`、`/settings`、`/tree` 以及所有 Pi 快捷键都归 Pi 管。

模型选择器（`Ctrl+M`）列出 Duo 启动 Pi 所用的同一套安装（`pi --list-models`）上报的模型目录。输入即可过滤，用 `↑`/`↓`（或 `PgUp`/`PgDn`、`Home`/`End`）移动，用 `Tab` 在 Austin 与 Tony 之间切换目标，用 `Shift+Tab` 循环该 Agent 的思考强度，用 `Enter` 应用模型并关闭，或用 `Space` 应用模型但不关闭——这样一次打开就能同时设好模型和思考强度。切换是实时的：Pi 保留对话，并把模型与思考强度记入 session 记录，因此重启或 resume（`Ctrl+R`/`Ctrl+Y`）后仍然有效。`▶` 标记选择器光标，`●` 标记目标 Agent 当前使用的模型。

Agent 输出按轻量 markdown 渲染：标题、引用、链接以及粗体/斜体/行内代码都有样式，markdown 表格会画出对齐的真实边框。表格宽度超过时间线时，Duo 会折行最宽的单元格，而不是截断内容；折行的列表项会保持悬挂缩进，续行对齐在条目正文下方。

鼠标划选复制支持终端标准 OSC 52 转义序列（在远程 SSH、tmux 以及 Ghostty/WezTerm/Alacritty/Kitty 等现代终端中原生直通）并自动回退至宿主系统剪贴板工具（macOS 下 `pbcopy`、Wayland 下 `wl-copy`、X11 下 `xclip`/`xsel`）。

Help 是一个完整的大屏视图，用 `↑`/`k`、`↓`/`j`、`PgUp`、`PgDn`、`Home`/`g`、`End`/`G` 滚动，用 `Esc` 或 `Ctrl+/` 关闭。在原生 Pi 中 `Ctrl+/` 仍直接交给 Pi，不会打开 Duo Help。

\* 只有当终端能明确区分上报该组合键时才会插入换行（`\x1b[13;2u` CSI-u 或 Duo 启用 modifyOtherKeys 后的 `\x1b[27;2;13~`）。若你的终端把 `Shift+Enter` 发成裸 `\r`，它会**提交**而不是换行——这种情况请改用 `Ctrl+Enter`（到达时是 `\n`）。`Ctrl+M` 有同样的限制：只有支持上述模式的终端才会把它上报成带 Ctrl 的 `m` 键（`\x1b[27;5;109~` 或 `\x1b[109;5u`），其余终端上与 `Enter` 无法区分——这种情况请用 `Alt+M`。

## 配置

每一项都有可用默认值，`duo` 不需要任何配置就能跑。

| 变量 | 默认值 | 含义 |
|---|---|---|
| `DUO_MODE` | `fast` | 会话模式：`fast` 或 `goal`。命令行 `--mode`/`-m` 优先；`--resume` 保持持久化的模式。 |
| `DUO_TEST_COMMAND` | （无） | 自动化测试门禁命令，在验证/交付前确定性执行（命令行 `--test-cmd` 优先）。 |
| `DUO_REPO` | 当前目录 | 启动时针对的仓库或子目录。命令行路径参数优先。 |
| `DUO_SESSION` | 时间戳 + 随机 hex | 会话 id，格式 `YYYYMMDD-HHMMSS-xxxxxxxx`。 |
| `DUO_WORKTREE_ROOT` | `~/.duo/worktrees/<repo>-<hash>/<session>` | 创建 Austin/Tony worktree 的位置。 |
| `DUO_BASE_REF` | `HEAD` | worktree 从哪个 ref 切出。 |
| `DUO_PI_COMMAND` | `pi` | 启动每个 Pi Agent 所用的命令。 |
| `DUO_LISTEN` | `127.0.0.1:0` | Bridge 监听地址（默认由系统分配端口）。 |
| `DUO_HARNESS` | `true` | 启用 idle/stall 看门狗，把失去动力的协调推回来。 |
| `DUO_HARNESS_IDLE_SECONDS` | `15` | 静默多久算 idle。 |
| `DUO_HARNESS_STALL_SECONDS` | `300` | 多久没有进展算 stall。 |
| `DUO_HARNESS_COOLDOWN_SECONDS` | `30` | 两次 harness 提醒之间的最小间隔。 |
| `DUO_HARNESS_RESUME_GRACE_SECONDS` | `45` | `--resume` 之后的额外宽限期，避免把重连误判成 stall。 |

启动非默认 Pi 命令：

```bash
DUO_PI_COMMAND='pi --some-flag' duo
```

### 配置文件

你可以在全局 `~/.duo/config.json` 或项目级 `.duo/config.json`（或 `.duo.json`）中持久化默认配置：

```json
{
  "mode": "fast",
  "testCommand": "go test ./...",
  "agents": {
    "austin": {
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
    "stallSeconds": 300
  }
}
```

优先级顺序：显式命令行参数（`--mode`, `--test-cmd`）> 环境变量（`DUO_*`）> 仓库 `.duo/config.json` > 全局 `~/.duo/config.json` > 内置默认值。

## 数据存放在哪里

```text
~/.duo/sessions/<repo-id>/<session-id>/
    state.json      mode、phase、验证/Plan、签字、证据、worktree 路径与分支、Pi session id
    events.jsonl    阶段、签字、bridge 与 merge 事件的诊断日志，
                    同时保存 resume 时回放进 TUI 的 pane 记录
    duo.log         生命周期输出（session token 写入前已脱敏）
    lock           持有属主 PID 与 hostname 的 advisory flock

~/.duo/worktrees/<repo>-<hash>/<session>/
    austin/         Austin 的 worktree —— 同时也是 integration worktree
    tony/           Tony 的 worktree
```

会话目录以仅属主权限创建，且不含凭据；但它确实描述了你项目的状态，详见 [SECURITY.md](./SECURITY.md)。

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
duo --resume              # 恢复本仓库未完成的那个会话
duo -r                    # 同上
duo --resume <id>         # 恢复指定会话
duo --resume=<id>         # 同上
```

裸 `duo` 永远启动一个**新**会话，并拒绝覆盖未完成的会话。`--resume` 在恰好只有一个未完成会话时自动选中它；有多个时要求你给 id，而不是猜。

Resume 会读回持久化快照，**拿 Git 验证它**，并在启动任何进程之前撤销所有已无法证明的签字或验证。模式随会话持久化：`--resume` 保持它，没有记录 mode 的旧会话按 Goal 恢复，且 `DUO_MODE` 在 resume 时被忽略（显式传入冲突的 `--mode` 会报错）。对账后的状态会在 Agent 启动前先写回，因此启动过程中崩溃不会复活被撤销的批准、也不会重放一次 merge。每个重连的 Agent 会收到一条与模式和阶段对应的唤醒消息；重复重连不会重复投递。每个 pane 最近的 200 条输出会被回放进 TUI，所以恢复后的会话一开始就能看到之前的对话。

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
duo apply --session <id>     # 同上
duo apply -s <id>            # 同上
duo apply --session=<id>     # 同上
```

`duo apply` 从不启动 Agent，并复用同一套安全规则——它不会强行推进一个被阻塞的交付。

## 查看与清理会话

Duo 将会话快照持久化在 `~/.duo/sessions/`，将隔离的 Git 工作树存放在 `~/.duo/worktrees/`。你可以随时列出会话或清理磁盘空间：

```bash
duo sessions                 # 查看当前仓库的所有会话
duo sessions --all           # 查看所有仓库的会话
duo clean                    # 清理当前仓库已完成（DONE）的会话
duo clean <session-id>       # 清理指定的某个会话
duo clean --all-repos        # 清理所有仓库中已完成的会话
duo clean --force            # 连同未完成会话一起清理（自动跳过当前运行中的活跃会话）
duo clean --dry-run          # 仅预览将被清理的内容，不修改磁盘
```

清理会话会自动安全移除对应的 Git 工作树（`git worktree remove --force`）、执行 `git worktree prune` 修剪、删除临时分支（`duo/<session>/*`），并删除对应的会话快照目录。正在被另一个运行中的 Duo 进程持有的会话会被 `flock` 锁保护，自动予以跳过。

## 真实流程记录

### Fast（默认）

一段精简的 Fast 模式记录：

```text
用户 → Austin

Austin 实现 → commit 4c1a9f2          （RUNNING）
Austin：duo_set_status ready=true
  → VERIFY，验证绑定到 4c1a9f2

Tony 检查 4c1a9f2
Tony：duo_set_verification issue_found
  "src/cache.ts 仍会缓存一次失败的查询，所以重试永远不会发生"
  → RUNNING

Austin 修复 → commit 8e02b1d
Austin：duo_set_status ready=true
  → VERIFY，绑定到 8e02b1d

Tony：duo_set_verification passed
  → 交付把你的分支 fast-forward 到 8e02b1d
DONE
```

### Goal

一段 v0.2 时期成功运行的精简记录，被测对象是一个宠物医院小应用 —— **不是本仓库**。下面的 commit 号属于那个应用，在本仓库里 `git show` 会失败：

```text
用户 → Austin

Austin → Tony：
"用户认为 UI 老旧。我有初步判断，请你独立分析，不要直接附和。"

Tony → Austin：
"我不同意大改字体/徽章。真正的问题更像点阵、装饰圆、圆角层级和阴影。"

Shared Plan v1
Austin ✓
Tony   ✓

PLAN → EXECUTE
Austin 修改 → commit 7bf559e
Tony 对该 commit Review ✓

EXECUTE → REVIEW → INTEGRATE
Austin ✓ integrated HEAD
Tony   ✓ same HEAD

DONE
```

这里最重要的设计原则是：**Phase 是 checkpoint，不是行为牢笼。** Tony 可以提前看 diff，Austin 也可以在 PLAN 阶段提前做 prototype；Duo 只在"正式共识和正式成果"处设置硬边界。Fast 模式遵循同一原则，只是仪式更少：Tony 随时可以检查 commit，但只有一次 `duo_set_verification` 的结果才能放行交付。

更完整的记录见 [docs/demo.md](./docs/demo.md)。

## 已知限制

Duo 仍是实验性运行时。简要说：Agent 拓扑固定为两个名为 Austin 和 Tony 的 Agent；Pi 是目前唯一支持的 Agent runtime；会话模式在启动时固定，不支持运行时切换或自动升级到 Goal；Fast 是单写者，Tony 永不向被交付的成果 commit；恢复无法重建 Agent 的*推理过程*，只能恢复其状态；Duo Core 自身崩溃后不会自动重启；会话绑定机器与仓库路径，不可迁移。

完整清单，以及刻意划为非目标（non-goal）的部分，见 [docs/known-limitations.md](./docs/known-limitations.md)。

## 开发

```bash
make check     # go test ./... && go vet ./... && go build ./cmd/duo
```

或逐项执行：

```bash
go test ./...
go vet ./...
go build ./cmd/duo
```

测试覆盖 Git worktree 与集成行为、PTY 监管、持久会话对账、交付并发，以及阶段流转规则。CI 在 Ubuntu 与 macOS 上分别用 Go 1.22.x 和 Go stable 跑一遍；推送 `v*` tag 会构建并发布四平台归档。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/architecture.md](./docs/architecture.md) | 组件边界、为什么用 worktree 而不是锁、为什么 PLAN 不是写锁。 |
| [docs/known-limitations.md](./docs/known-limitations.md) | 完整限制与明确的非目标。 |
| [docs/demo.md](./docs/demo.md) | 一次真实双 Agent 运行的精简记录。 |
| [docs/publishing.md](./docs/publishing.md) | 发布与仓库设置说明。 |
| [CHANGELOG.md](./CHANGELOG.md) | 版本历史。 |
| [ROADMAP.md](./ROADMAP.md) | 项目方向。 |
| [CONTRIBUTING.md](./CONTRIBUTING.md) | 需要保持的设计原则，以及可以从哪里入手。 |
| [SECURITY.md](./SECURITY.md) | 信任模型、Git 安全边界、磁盘数据、漏洞披露。 |

英文版本见 [README.md](./README.md)。

## License

MIT — 见 [LICENSE](./LICENSE)。
