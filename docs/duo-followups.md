# Three prompts for Duo

Each prompt states facts that have already been verified, so the agents spend
their time on the change and its proof rather than on re-deriving the diagnosis.
Three were written on 2026-10-02, after v0.9.0.

---

## 1. A resumed session can sit silent with both agents idle forever

```
给 duo 修一个恢复死锁：会话 resume 之后，如果两个 agent 都没有任何动作，
没有人会被叫醒，必须由人手动发消息才能继续。

复现条件：session 中 Austin 用 agy（或任何不走 socket bridge 的 driver），
在 EXECUTE/REVIEW 阶段 Ctrl+Q 退出，然后 duo --resume。恢复后两个 agent 都启动，
但都不动，于是永远不动。

已验证的事实，不需要你重新调查：

- harness 的触发逻辑本身是对的，internal/harness/harness.go:126 已经有
  bothIdle := !austin.Busy && !tony.Busy 这类判断，IdleThreshold 默认 15 秒。
  问题不在判断，在于它前面还有三道门。

- 门 1（harness.go:99）：if !state.Started { return }。state.Started 有两处被置位：
  coordinator.go:564（收到 agent 的 agent_start activity）和 coordinator.go:350
  （agent 连接时）。而 agy 的 agent_start activity 只在 watcher 看到 transcript 里的
  USER_INPUT 步骤时才发出（internal/agent/agy_watcher.go:500），也就是说只有 agent
  先动过才会发。没人动 → Started 保持 false → harness 直接 return。
  这可能是本次卡死的直接原因，但我没有 100% 确认，请你先自己验证是哪道门。

- 门 2：resume wake 也只发给"连上了 bridge"的 agent。
  coordinator.go:484 wakeResumedAgent 由 bridge connect 触发，每个 agent 一次。
  agy 通过进程内回调上报（agent.FuncActivitySink，internal/agent/agy_watcher.go:30），
  不经过 socket，所以永远不产生 bridge_connect，也就永远收不到 resume wake。
  证据：一次真实 session 的 events.jsonl 里，bridge_connect 只出现过 Tony（opencode），
  Austin（agy）从头到尾没有。

- 门 3（harness.go:113）：austin.HumanAttached || tony.HumanAttached 会让 harness
  静默。这个只在人按 Ctrl+A/T 进入 agent 原生 TUI 时才置位（internal/tui/app.go:386），
  普通观察 preview 不会置位，所以这条不是原因，但请一并确认。

- 实测数据：一次 resume 后 08:00:51 恢复，08:10:16 才发出 resume wake to Tony，
  中间 9 分 25 秒完全静默，没有任何 harness 事件、没有 nudge、没有 activity。

要求：

1. 先用一次可复现的最小实验确认"到底是哪道门卡住"，再动手。把结论写进 commit
   message。我列的三个门里门 1 是推测，门 2 是已确认的事实。
2. 修完之后，resume 之后即使 agent 完全没动，Duo 也必须在合理的阈值内自己推动
   一次，不要等人类输入。注意区分两种情况：真的需要人介入（比如在等 VERIFY 结果）
   和只是没人动。这两者不能混为一谈。
3. resume wake 不应该只发给有 bridge 的 agent。agy 这类 in-process 上报的 driver
   也要能被唤醒，但要注意只发一次，不要重复打断。
4. 不要把 RecoveryGrace、IdleThreshold 调大来"掩盖"问题。那是延迟，不是修复。
5. 如果某道门必须保留（比如 INTEGRATE 双方已签字后不该再催），保留它并加注释
   说明为什么。

不要做：不要为了让测试通过而给 harness 加特例分支；不要改 harness 的语义判断
（bothIdle / activeWork / quietFor 这套逻辑本身是对的）。

验证要求：

- 加一个测试，构造"resume 后两个 agent 都没有任何 activity"的 tracker 状态，
  断言 harness 会发出一次 nudge。RecoveryGrace 之内不应该发，之后应该发。
- 加一个测试证明 agy 这种没有 bridge_connect 的 agent 也能收到 resume wake。
- 用真实 session 跑一次端到端验证，把 duo.log 的时间戳贴进 commit message。
```

---

## 2. A missing optional tool is detected with a syscall on every event and every frame

```
给 duo 做一个小的性能修正：sqlite3 的存在性检测现在会在每一条事件和每一帧上
做一次系统调用，应该在启动时判定一次并缓存。

已验证的事实：

- internal/agent/agy_watcher.go:317-323
    var LookPath = exec.LookPath
    func HasSqlite3() bool { _, err := LookPath("sqlite3"); return err == nil }
  LookPath 是包级变量，测试可以 stub，这点保持不变。

- internal/tui/model.go:224 的 route() 里：
    if !a.warnedSqlite3 && a.driverName(event.Agent) == "agy" {
        a.warnSqlite3Once()
    }
  问题：当 sqlite3 存在时（这是绝大多数用户的情况），warnedSqlite3 永远不会被
  置位，所以这个条件每一条事件都为真，每条事件都会走一次 warnSqlite3Once()
  里的 HasSqlite3()，也就是每条事件一次 exec.LookPath 系统调用。

- internal/tui/preview.go:157 的 (a *App) previewUsage 每次渲染也调用
  agent.HasSqlite3()。previewHeader 在 render 里对 Austin 和 Tony 各调一次
  （internal/tui/preview.go:48-49），也就是每帧至少两次系统调用。

要求：

1. 在 App 构造时判定一次，存成字段，route 和 previewUsage 都读这个字段。
   不要缓存成一个包级全局变量 —— 测试需要能 stub，App 字段配合可注入的
   LookPath 就能做到。
2. LookPath 必须仍然是可替换的变量，否则现有那四个 table-driven 测试
   （TestPreviewAgySqlite3DiagnosticsTableDriven 等）会失效。
3. 缓存的时机要考虑：App 可能先于 agent 启动而构造。如果 sqlite3 的存在性
   在运行期不会改变（它不会），构造时判定就是对的；如果你认为有更合适的位置，
   说明理由。

不要做：不要顺手把 HasSqlite3 改成永久缓存的包级变量，那会让测试无法隔离。

验证要求：

- 加一个测试，构造 App 后改变 PATH（或 stub LookPath），断言缓存值不随之后
  的变化改变 —— 证明它确实只判定了一次。
- 现有四个 sqlite3 测试必须仍然全过。
- 在 commit message 里说明改动前后的调用频次：改动前是"每事件 1 次 + 每帧 2 次"，
  改动后是"整个会话 1 次"。
```

---

## 3. The help drift guard does not notice a flag vanishing from one command

```
给 duo 的命令行防漂移测试补一个缺口：某个 flag 从某一条命令的签名里消失时，
现在没有任何测试会失败。

已验证的事实，不需要你重新调查：

- internal/clidoc/clidoc_test.go 的 TestSignaturesCarryEveryParsedFlag 用一个
  硬编码清单 parsedFlags，逐个检查"这个 flag 是否出现在某个签名里"。

- cmd/duo/usage_test.go 的 parsedFlagTokens 确实是用 AST 扫出来的，它遍历
  parserFuncs 里的每个函数，把 flag 收进一个扁平的 seen 集合。

- parserFuncs 是 map[string]bool（cmd/duo/usage_test.go:23），函数名到 true。
  也就是说扫描的时候 decl.Name.Name 就在手上，是知道"这个 flag 是哪个
  parse 函数解析的"，但被丢掉了，直接压平。

- 复现（我实测过）：把 internal/clidoc/clidoc.go 里 sessions 的签名从
  "duo sessions [--all|-a] [repository]" 改成 "duo sessions [repository]"，
  然后跑 go test ./internal/clidoc/ ./cmd/duo/ ./internal/tui/ —— 全部通过。
  原因是 --all 仍然出现在 duo clean 的签名里，全局覆盖检查就放行了。
  结果是 duo sessions --help 不再显示 --all，但 duo sessions --all 仍然可用
  （summary 文字里还提着一句 --all）。

要求：

1. 让扫描保留归属：把 seen 从 set 变成 (命令, flag) 的集合，然后逐条命令检查
  签名里是否带了它自己解析的那些 flag。函数名到命令名的映射要显式写出来，
  不要靠字符串猜。
2. 反方向也要有：签名里出现某个命令并不解析的 flag，同样应该失败。
3. 注意 parseArgs 解析的是主 usage（duo [repository] --mode ... 那一行），
  不是某个子命令。它的归属检查要落在正确的地方。
4. 补一条说明：这个 guard 只能覆盖 cmd/duo 里 parserFuncs 列出的那些解析函数。
  如果某个命令的 flag 是在别处解析的，它不在扫描范围内 —— 这一点要么把函数
  补进 parserFuncs，要么在注释里写清楚边界。不要留一个看起来覆盖了全局、
  实际只覆盖了六处函数的假象。

影响范围要说清楚：这是文档准确性问题，不是功能问题。所有渲染路径都来自
clidoc，所以 --help、各子命令 help、in-app Help 面板之间不会互相矛盾，
它们只会一起漏掉一个 flag。

不要做：不要改成手写一份 flag 清单来"对答案"，那正是 clidoc 想消除的重复。

验证要求：

- 变异测试：删掉 sessions 签名的 [--all|-a]，测试必须失败。
- 变异测试：往某个签名里塞一个该命令不解析的 flag，测试必须失败。
- 变异测试：把某个 flag 从 parserFuncs 覆盖的函数里改名，测试必须失败。
- 改完后确认 go test ./cmd/duo/ ./internal/clidoc/ ./internal/tui/ 全绿。
```
