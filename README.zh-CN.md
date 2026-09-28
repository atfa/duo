# Duo

> 一个为 Pi 设计的双平级 Coding Agent Runtime。

**Duo 让两个 Pi coding agent 以平级伙伴的方式协作，而不是把一个 Agent 设为 Planner、另一个设为 subordinate worker。** 两个 Agent 共同商定 Plan、在不停下的前提下实时互发消息、在隔离的 Git worktree 中各自执行、交叉 Review 对方的 commit，并在集成前共同签字。协作结束后，被批准的成果会交付回你启动 Duo 的那个仓库。

运行时由一个 Go 协调核心加一层刻意做薄的 Pi bridge 组成。Duo 只强制那些确实需要确定性协调的东西——身份、路由、Plan 版本、阶段流转、签字、Git 证据——其余部分留给模型自然地自行协作。

- **平级，而非层级。** 没有固定的 Planner。任何一方都可以反对，分歧本身就是流程的正常部分。
- **证据优先于口头声明。** 一个干净的 commit SHA 比 Agent 说"做完了"更有价值。签字是对 Git 校验过的，不是被无条件相信的。
- **隔离，且绝不破坏。** Austin 和 Tony 从不共用工作树；Duo 也不会改写你启动它的那个仓库的历史。
- **持久。** 会话能扛住崩溃，恢复后共享 Plan、worktree 和 Pi 对话身份都还在。

当前版本：**v0.4.7** — 完整历史见 [CHANGELOG.md](./CHANGELOG.md)。

## 快速开始

```bash
cd /path/to/git/repo
duo
```

然后在 composer 里输入任务，按 `Enter`。composer 只发送给 **Austin**。新任务里 Austin 会用 `duo_send` 唤醒 Tony，两人商定 Plan，之后由 Duo Core 推进阶段。

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
 PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

Duo Core 负责的是一小组可靠的"制度"：阶段、签字、成果证据、worktree、harness 和集成。至于怎么讨论、怎么分工、要不要先做个实验，仍由 Austin 和 Tony 自主决定。

## 协作如何运作

```text
PLAN → EXECUTE → REVIEW → INTEGRATE → DONE
```

每个阶段是**checkpoint，不是行为牢笼**。Duo 不会阻止 Agent 提前读代码或提前 Review；阶段只决定"推进需要哪些证据"。

| 阶段 | 发生什么 | 推进所需证据 |
|---|---|---|
| **PLAN** | 双方协商一份共享 Plan。允许 provisional 修改。 | 双方批准**同一个** Plan 版本。 |
| **EXECUTE** | 各自在自己的 Git worktree 中独立工作。 | 各自 worktree clean，并记录 commit SHA。 |
| **REVIEW** | 各自 Review 对方的那个 commit。 | 各自批准自己实际 Review 过的那个 peer HEAD。 |
| **INTEGRATE** | Tony 的分支被合并进 Austin 的 integration worktree。 | 双方批准同一个 clean 的 integrated Austin HEAD。 |
| **DONE** | 被批准的产物已交付回你的仓库。 | 交付成功。 |

Plan 一旦更新就产生新版本，并**使双方签字同时失效**，所以反复改措辞是有可见代价的。签字绑定到一个确切的 commit：对方推了新 commit，你之前的 Review 就过期，必须重做。

冲突会显式保留给 Austin 处理，不会被静默覆盖。

## 四个核心工具

Austin 和 Tony 共用一套随 Pi bridge 安装的小工具面。这是它们影响共享状态的唯一通道。

| Tool | 作用 |
|---|---|
| `duo_send` | 在双方都继续工作的同时，给 Peer 发一条重要的实时消息。用于发现、疑问、冲突和提案，不是日常进度播报。 |
| `duo_set_plan` | 创建或整体替换共享 Plan。每次调用产生新版本并重置双方签字。 |
| `duo_set_status` | 为当前阶段签字（`ready: true`）或撤销自己的签字（`ready: false`），可附 note。 |
| `duo_status` | 读取权威的 phase、Plan、签字，以及两个 worktree 的 branch、路径、HEAD、clean 状态和 ahead 数。 |

Bridge 是一层薄适配器——Pi 事件转成 Duo 活动，Pi 工具转成 Duo 请求，Duo 消息转成 Pi 的 steer。它刻意不持有任何项目真相。

## 终端界面

Duo 并排显示两个 Agent 的连接、进程和工作状态，下方是当前阶段、Plan 与瞬时反馈。

| 按键 | 操作 |
|---|---|
| `Enter` | 把 composer 内容发送给 Austin |
| `Ctrl+Enter` / `Shift+Enter`\* | 在 composer 中插入换行 |
| `Ctrl+A` | 进入 Austin 的原生 Pi |
| `Ctrl+T` | 进入 Tony 的原生 Pi |
| `Ctrl+]` / `Ctrl+\` | 从原生 Pi 返回 Duo |
| `Ctrl+R` / `Ctrl+Y` | 若 Austin / Tony 进程已退出或失败，重启它 |
| `Ctrl+/` | 打开或关闭 Duo Help |
| `Ctrl+Q` | 退出 Duo 并保留 session |
| `←` / `→` | 移动 composer 光标 |
| `Backspace` | 删除前一个字符 |
| 鼠标滚轮悬停在某个 pane | 滚动该 Agent 的更早输出 |

composer 支持多行，一次最多显示四行。原生接管是全屏接管：在 Pi 里，`/model`、`/settings`、`/tree` 以及所有 Pi 快捷键都归 Pi 管。

Help 是一个完整的大屏视图，用 `↑`/`k`、`↓`/`j`、`PgUp`、`PgDn`、`Home`/`g`、`End`/`G` 滚动，用 `Esc` 或 `Ctrl+/` 关闭。在原生 Pi 中 `Ctrl+/` 仍直接交给 Pi，不会打开 Duo Help。

\* 只有当终端能明确区分上报该组合键时才会插入换行（`\x1b[13;2u` CSI-u 或 Duo 启用 modifyOtherKeys 后的 `\x1b[27;2;13~`）。若你的终端把 `Shift+Enter` 发成裸 `\r`，它会**提交**而不是换行——这种情况请改用 `Ctrl+Enter`（到达时是 `\n`）。

## 配置

每一项都有可用默认值，`duo` 不需要任何配置就能跑。

| 变量 | 默认值 | 含义 |
|---|---|---|
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

## 数据存放在哪里

```text
~/.duo/sessions/<repo-id>/<session-id>/
    state.json      phase、Plan、签字、证据、worktree 路径与分支、Pi session id
    events.jsonl    阶段、签字、bridge 与 merge 事件的诊断日志
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

Resume 会读回持久化快照，**拿 Git 验证它**，并在启动任何进程之前撤销所有已无法证明的签字。对账后的状态会在 Agent 启动前先写回，因此启动过程中崩溃不会复活被撤销的批准、也不会重放一次 merge。每个重连的 Agent 会收到一条与阶段对应的唤醒消息；重复重连不会重复投递。

恢复刻意保守：当它无法证明某个批准仍然成立时，宁可撤销也不轻信。所以崩溃后请预期要重新签一次，而不是静默放行。

## 交付与 `duo apply`

`DONE` 意味着最终集成的结果已交付进你启动 Duo 的那个仓库。交付**只做 fast-forward**（`git merge --ff-only`）。Duo 从不产生 merge commit、不 rebase、不改写你的历史。

以下情况交付会**拒绝执行**，并让你的仓库完全保持原样：

- 仓库有未提交改动；
- 当前分支与 Duo 启动时的分支不同；
- 当前 HEAD 已与最终 Duo 结果分叉；
- HEAD 处于 detached 状态，或起始分支未知；
- 最终结果并非派生自记录的 base commit。

交付被拒时会话保持在 `INTEGRATE`、双方签字保留，写入一个 `pending` checkpoint，并打印确切的重试命令：

```bash
duo apply                    # 重试本仓库的待交付
duo apply <session-id>       # 重试指定会话
duo apply --session <id>     # 同上
duo apply -s <id>            # 同上
duo apply --session=<id>     # 同上
```

`duo apply` 从不启动 Agent，并复用同一套安全规则——它不会强行推进一个被阻塞的交付。

## 一个已经跑通的真实流程

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

这里最重要的设计原则是：**Phase 是 checkpoint，不是行为牢笼。** Tony 可以提前看 diff，Austin 也可以在 PLAN 阶段提前做 prototype；Duo 只在"正式共识和正式成果"处设置硬边界。

更完整的记录见 [docs/demo.md](./docs/demo.md)。

## 已知限制

Duo 仍是实验性运行时。简要说：Agent 拓扑固定为两个名为 Austin 和 Tony 的 Agent；Pi 是目前唯一支持的 Agent runtime；恢复无法重建 Agent 的*推理过程*，只能恢复其状态；Duo Core 自身崩溃后不会自动重启；会话绑定机器与仓库路径，不可迁移。

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
