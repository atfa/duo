# Duo

> 一个为 Pi 设计的双平级 Coding Agent Runtime。

**Duo 让两个 Pi coding agent 以平级伙伴的方式协作，而不是把一个 Agent 设为 Planner、另一个设为 subordinate worker。** 两个 Agent 可以共同讨论计划、实时互发消息、在隔离的 Git worktree 中执行、交叉 Review，并在集成前共同签字确认。

当前版本为 **v0.4.6**，提供集成式双栏 TUI、原生 Pi 终端切换、动态端口和会话隔离，实现了可崩溃恢复的持久化会话（`duo --resume`），并会把最终集成结果安全交付回你启动 Duo 的原始仓库。

## v0.4.6 TUI Help 与可用性

- `Ctrl+/` 打开/关闭完整、可滚动的 Duo Help；支持 `↑/↓`、`j/k`、`PgUp/PgDn`、`Home/End` 和 `Esc`。
- 主界面 composer 明确为 `Duo → Austin >`，状态独占固定一行，footer 精简为 `Enter Send · Ctrl+A/T Native · Ctrl+/ Help · Ctrl+Q Quit`。
- 原生 Pi 中 `Ctrl+/` 仍直接交给 Pi，不会打开 Duo Help。

| 快捷键 | 操作 |
| --- | --- |
| `Ctrl+/` | 打开/关闭 Duo Help |
| `Enter` | 向 Austin 发送任务/消息 |
| `Ctrl+A` / `Ctrl+T` | 打开 Austin/Tony 原生 Pi |
| `Ctrl+Q` | 退出 Duo 并保留 session |

## v0.4.5 Resume 协作唤醒

- **恢复协作动量**：`duo --resume` 会恢复两个 Pi session，并在各自 bridge 重新连接时主动发送与当前 phase 对应的恢复提示；无需再输入 `continue` 才让协作重新开始。人类 composer 仍然只发送给 Austin。

从子目录启动时，Duo 仍以外层 Git repository 作为 branch、worktree 和 delivery 的 Git 边界，但 Austin/Tony 会从各自 worktree 中对应的启动子目录开始工作。例如 `cd repo/packages/web && duo` 的 Agent 默认工作目录是 `packages/web`。

## v0.4.4 启动目录工作 Scope

- **保留启动目录 scope**：Git ownership 仍是 repository root；Austin/Tony 则从启动目录对应的 repository-relative 目录开始。scope 会持久化，resume 与 restart 后不变。

## v0.4.3 测试生命周期与发布验证

- **确定性的测试生命周期**：Coordinator E2E 测试会在临时目录清理前，显式等待 server、client、后台任务和 Git worktree 完成退出与清理。
- **发布验证加固**：main 与 tag CI 继续用完整测试和 vet 作为四平台发布归档的门禁。

## v0.4.2 可靠性修复

- **最终批准为边沿触发**：INTEGRATE 中重复的 `ready=true` 是幂等状态更新，不会重复发起交付。
- **交付串行且失败关闭**：同一会话一次只执行一个交付事务；同一 Final HEAD 的 `applied` checkpoint 不会退回 `pending`；关键 checkpoint 写入失败时绝不触碰原始仓库。
- **Release 有测试门禁**：tag 发布必须先通过测试、`go vet` 和二进制构建，才能上传归档文件。

## v0.4.1 已实现

- **DONE 意味着“已交付”**：INTEGRATE 双方签字只记录最终批准，不再直接结束；Duo 先把整个最终集成 HEAD 交付回你的原始仓库，然后才把会话标记为 `DONE`。
- **只做 fast-forward**：交付只允许快进。仓库有未提交改动、处于其他分支、历史已分叉或处于 detached HEAD 时，Duo 会拒绝执行，并绝不会对你的仓库运行 `reset --hard`、`checkout -f`、`clean`、`merge --no-ff` 或 `rebase`。
- **`duo apply [session-id]`**：交付受阻时会话保持 INTEGRATE、双方签字保留，并写入 `pending` 交付 checkpoint，同时打印重试命令。仓库中只有一个待交付会话时，`duo apply` 可省略参数。
- **崩溃安全**：最终批准和待交付 checkpoint 会在触碰 Git 之前先持久化，因此快进与 DONE 写入之间崩溃可在下次 `duo --resume` 或 `duo apply` 时对账恢复。
- **最终树清洁**：INTEGRATE 提示词要求 Austin 清理仅用于协作的临时产物，并要求 Tony 做仓库卫生审查，避免这些文件被交付。
- **兼容 v0.4.0 会话**：v0.4.0 已经标记 `DONE` 的会话仍可用 `duo apply` 完成交付。

## v0.4.0 已实现

- **持久化会话**：Phase、Plan 版本、签字、证据、worktree 记录和 Pi 会话身份都会写入 `~/.duo/sessions/<repo-id>/<session-id>/state.json`，采用原子写入（临时文件 → `fsync` → rename → 目录 `fsync`），崩溃后不会留下半个 checkpoint。
- **`duo --resume [session-id]`**：不指定 id 时恢复该仓库唯一未完成的会话；存在多个时列出候选而不是猜测。会话不存在修复选项时也会给出明确提示。
- **Git 是唯一真相**：resume 会重新检查两个 worktree、发现中断中或已完成的 merge，并撤销所有证据已失效的签字。会话不会静默退回到 PLAN，旧签字不会被当成仍然有效。
- **脏 worktree 可恢复**：未提交的修改不会阻塞 resume；Duo 会报告它、只撤销因此失效的签字，并且不会动你的文件。
- **Pi 身份稳定**：通过 `--session-id`，Austin 和 Tony 在重启后仍保留各自的 Pi 对话历史。
- **同一会话单一进程**：每个会话使用 advisory `flock`，并提供诊断日志 `events.jsonl` 和会脱敏 token 的 `duo.log`。
- 修复了 macOS 上 resume 误判自身 worktree 的路径比较 bug（Git 返回 `/private/var/...`，而持久化的是 `/var/...`）。

## v0.3.3 已实现

- **Resize 渲染硬化**：窗口尺寸变化、原生 Pi 返回和布局变化都会先整屏清除，不再在大尺寸变小时残留旧边框和画面。
- **不再虚构终端尺寸**：删除 60×18 的强制 clamp；小于 60×18 时输出受限的 `Terminal too small` 提示，不会换行或滚屏。
- **Synchronized Output**：每帧用 `CSI ?2026 h/l` 包裹，并在写帧期间临时关闭 autowrap。
- **Resize 合并**：连续 SIGWINCH 只保留最新尺寸，PTY 与 TUI 各只更新一次。
- **统一 Renderer Scheduler**：约 60 FPS，只有 dirty 才重绘；idle 时不再周期性 repaint，spinner 只在 Agent 忙时更新。
- **进程状态语义**：区分 `exited` 与 `failed`，Duo 主动停止显示为正常退出。

[English README](./README.md)

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

Duo Core 负责的是少量可靠的“制度”：阶段、签字、成果证据、worktree、harness 和集成。至于如何讨论、如何分工、是否提前做实验，仍由 Austin 和 Tony 自主决定。

## v0.3.2 已实现

- **单一用户入口**：通常只给 Austin 输入任务，Austin 用 `duo_send` 主动唤醒 Tony。
- **实时 Peer Message**：消息以 Pi steer 注入，Peer 即使正在工作也能收到。
- **版本化 Shared Plan**：任一 Agent 更新 Plan 都会产生新版本，并使双方旧签字失效。
- **PLAN 阶段允许乐观执行**：PLAN 不是文件写锁。Agent 可以在自己的 worktree 中调查、试验、跑测试、做 provisional 修改。
- **双 Git worktree 隔离**：Austin/Tony 从同一个 base commit 创建独立 branch/worktree。
- **EXECUTE 成果签字绑定 commit SHA**：只有工作树 clean 才能标记 ready。
- **REVIEW 签字绑定实际 peer HEAD**：对方提交发生变化时，旧签字自动失效。
- **完整生命周期**：`PLAN → EXECUTE → REVIEW → INTEGRATE → DONE`。
- **自动 Integration**：REVIEW 通过后将 Tony 分支合并到 Austin integration branch。
- **安全交付回原始仓库**：INTEGRATE 双方签字后，Duo 把最终集成 HEAD 快进到你的原始分支；不安全时保留双方签字并给出 `duo apply` 重试命令。
- **Harness/Watchdog**：任务未完成但双方都陷入 idle 时，Duo 会主动唤醒 Austin 推进任务。
- **集成式 TUI**：并排显示 Austin/Tony 摘要、运行状态、共享 Plan 和统一输入框。
- **原生 Pi 模式**：`Ctrl+A` / `Ctrl+T` 进入对应 Pi，`Ctrl+]` / `Ctrl+\` 返回 Duo。
- **会话隔离**：每次启动使用动态 localhost 端口和随机 token；普通 Pi 不会激活 Duo bridge。

## 快速开始

要求：Git、可运行的 `pi`，以及至少有一个 commit 的 Git 仓库。

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
cd /path/to/project
duo
```

从源码构建还需要 Go 1.22+，可运行 `./scripts/install.sh`。

例如：

```text
改进当前代码库。

你主要关注正确性、设计和测试；Tony 主要关注性能、复杂度和实现简洁性。
先独立理解问题，然后唤醒 Tony，让他给出独立意见。形成共同 Plan 后再分工执行、
交叉 Review，并推进到最终集成。
```

## 一个已经跑通的真实流程

```text
用户 → Austin

Austin → Tony：
“用户认为 UI 老旧。我有初步判断，请你独立分析，不要直接附和。”

Tony → Austin：
“我不同意大改字体/徽章。真正的问题更像点阵、装饰圆、圆角层级和阴影。”

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

这里最重要的设计原则是：**Phase 是 checkpoint，不是行为牢笼。** Tony 可以提前看 diff，Austin 也可以在 PLAN 阶段提前做 prototype；Duo 只在“正式共识和正式成果”处设置硬边界。

## 生命周期语义

### PLAN

允许调查、原型、测试和 provisional 修改。Shared Plan 表示双方正式同意的工作方案。

### EXECUTE

双方在独立 worktree 工作。`ready=true` 必须绑定 clean commit SHA。

### REVIEW

双方交叉检查 peer artifact。签字绑定实际 peer HEAD；HEAD 改变会使旧签字失效。

### INTEGRATE

Duo 将 Tony merge 到 Austin。冲突显式保留给 Austin 解决，不会静默覆盖。双方签字表示**最终批准**，但不直接结束会话：Duo 随后把最终集成 HEAD 交付回你的原始仓库。

### DONE

只有在交付成功后才进入 DONE：你的原始分支已被快进到最终集成 HEAD。若交付不安全（脏仓库、分支不符、历史分叉、detached HEAD），会话保持 INTEGRATE，双方签字保留，并打印 `duo apply <session-id>` 重试命令。

## 四个核心工具

| Tool | 作用 |
|---|---|
| `duo_send` | 给 Peer 发送重要实时消息 |
| `duo_set_plan` | 发布完整 Shared Plan 新版本 |
| `duo_set_status` | 当前阶段签字或撤销签字 |
| `duo_status` | 查询权威 Phase、Plan、签字、证据与 workspace 状态 |

## 当前定位

Duo 已经具备完整双 Agent 协作闭环、集成式 TUI、可崩溃恢复的持久化会话，以及把最终结果安全交付回原始仓库的能力。目前仍固定为两名 Agent，主要面向 macOS/Linux + Git + Pi；交付仅支持 fast-forward，尚未实现 Duo 自身的自动崩溃重启和跨机器迁移会话。

详细限制见 [docs/known-limitations.md](./docs/known-limitations.md)。

## 下一步

v0.4.1 让 `DONE` 真正代表“最终产物已经回到你的仓库”：INTEGRATE 双方签字后，Duo 以 fast-forward 方式把整个最终集成 HEAD 交付回原始分支，不安全时保留签字并给出 `duo apply` 重试命令；快进与 DONE 写入之间崩溃也能对账恢复。v0.4.0 让会话变持久（可用 `duo --resume` 继续原任务）。后续重点是 Agent 名称/角色与模型配置、集成策略配置、worktree 生命周期清理、历史滚动和多行输入。

详见 [ROADMAP.md](./ROADMAP.md)。

## License

MIT。
