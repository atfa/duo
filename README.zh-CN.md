# Duo

> 一个为 Pi 设计的双平级 Coding Agent Runtime。

**Duo 让两个 Pi coding agent 以平级伙伴的方式协作，而不是把一个 Agent 设为 Planner、另一个设为 subordinate worker。** 两个 Agent 可以共同讨论计划、实时互发消息、在隔离的 Git worktree 中执行、交叉 Review，并在集成前共同签字确认。

当前版本为 **v0.3.1**，提供集成式双栏 TUI、原生 Pi 终端切换、动态端口和会话隔离。

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

## v0.3.1 已实现

- **单一用户入口**：通常只给 Austin 输入任务，Austin 用 `duo_send` 主动唤醒 Tony。
- **实时 Peer Message**：消息以 Pi steer 注入，Peer 即使正在工作也能收到。
- **版本化 Shared Plan**：任一 Agent 更新 Plan 都会产生新版本，并使双方旧签字失效。
- **PLAN 阶段允许乐观执行**：PLAN 不是文件写锁。Agent 可以在自己的 worktree 中调查、试验、跑测试、做 provisional 修改。
- **双 Git worktree 隔离**：Austin/Tony 从同一个 base commit 创建独立 branch/worktree。
- **EXECUTE 成果签字绑定 commit SHA**：只有工作树 clean 才能标记 ready。
- **REVIEW 签字绑定实际 peer HEAD**：对方提交发生变化时，旧签字自动失效。
- **完整生命周期**：`PLAN → EXECUTE → REVIEW → INTEGRATE → DONE`。
- **自动 Integration**：REVIEW 通过后将 Tony 分支合并到 Austin integration branch。
- **不会自动修改用户原始分支**：最终是否 merge 回去由用户自己决定。
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

Duo 将 Tony merge 到 Austin。冲突显式保留给 Austin 解决，不会静默覆盖。

### DONE

双方对同一个 clean integrated HEAD 签字。用户原分支仍保持原样。

## 四个核心工具

| Tool | 作用 |
|---|---|
| `duo_send` | 给 Peer 发送重要实时消息 |
| `duo_set_plan` | 发布完整 Shared Plan 新版本 |
| `duo_set_status` | 当前阶段签字或撤销签字 |
| `duo_status` | 查询权威 Phase、Plan、签字、证据与 workspace 状态 |

## 当前定位

Duo 已经具备完整双 Agent 协作闭环和集成式 TUI。目前仍固定为两名 Agent，主要面向 macOS/Linux + Git + Pi，尚未实现 session persistence/resume 和 Agent 自动重启。

详细限制见 [docs/known-limitations.md](./docs/known-limitations.md)。

## 下一步

下一阶段重点是 Agent 自动重启、直接 PTY 与 resize、历史滚动、多行输入和持久化恢复。

详见 [ROADMAP.md](./ROADMAP.md)。

## License

MIT。
