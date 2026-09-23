<a id="canonical-agent-event-timeline-owns-agent-turn-lifecycle"></a>

# 由 Canonical Agent Event Timeline 拥有 Agent Turn 生命周期

[English](agent-event-timeline.md) | [简体中文](agent-event-timeline.zh-CN.md)

> 从原仓库主干保留的设计记录。文中的日期、阶段、源码行号和实现判断属于记录当时；当前行为以 [Session 模型](../current/session-product-model.zh-CN.md)与 [Agent / Adapter 模型](../current/agent-adapter-product-model.zh-CN.md)为准。
> 源码路径相对于公共仓库根目录；原 `cmd/amux`、`cmd/amuxd`、`cmd/amuxlet` 的实现分别迁至 `app/cli`、`app/hub`、`app/runtime`，`cmd/*` 仅保留发行入口。

状态：已接受；持久化 Timeline、第一方消费者迁移、旧生命周期清理与直接 typed producer emission 均已实现。

日期：2026-08-27

<a id="context"></a>

## 背景

amux 支持 ACP Agent 和多个 structured Provider。它们的原生协议在事件名称、终态
信号、结果位置、权限请求、重试通知和 Provider 原生 turn ID 上并不一致。
规范化这些差异是不可避免的业务复杂度；在每个内部层反复规范化则是额外复杂度。

本决策之前，同一个逻辑 turn 可能依次表现为 `provider.Event`、`session.Event`、
broker frame、扁平 protobuf `AttachEvent`、`agentui.Event` 和 `TaskEvent`。
Session 还会分配 Turn 身份，并根据 event kind、Provider event type、state 与 stop
reason 的组合推断终态。Task 和展示消费者随后又重复解释其中部分信息。

这不仅是重复实现。一次 structured-broker 终态在跨进程时丢失了 typed Provider
事件，Session 无法识别该帧为终态，turn gate 因而没有关闭，下一条队列输入可能
永久等待。临时修复给 state frame 加入 Provider event type，并在 Runtime 侧重建
事件。补丁虽有必要，却说明生命周期真相没有唯一所有者。

本设计只取代早期 structured-provider watchdog 设计中的终态所有权与传播机制；
当前 Provider 规则由 [Agent / Adapter 产品模型](../current/agent-adapter-product-model.zh-CN.md)
维护。原有 timeout、activity、user-wait 和 tool-wait 策略仍然有效；其到期路径仍清理
producer 自己的 watchdog/turn 状态，并通过精确的 per-turn capability 发出显式 typed
终态。Session 将其映射到准确的 canonical Turn。Watchdog 清理不属于 Session 完成屏障。

原共享 `provider.Event` / `session.Event` 链是包含可选字段的兼容信封，不拥有
canonical Turn 身份、顺序或终态幂等性。它已从 producer 到 Session 的路径删除；
Claude 原生 sidecar JSON DTO 仍局限在其 Provider decoder 内。

同时，amux 有三种刻意区分的历史：

- Canonical Session Timeline 是持久化且与 Provider 无关的记录，用于结构化 Agent turn 审查和基于 cursor 的交付。
- Session 可见文本 replay 是派生的对话交接视图，不是实时事件流或生命周期权威。
- `TaskEvent` 是只追加、脱敏、持久化的 TaskRun 流，拥有自己的 sequence、cursor 和 replay 契约。

它们不是三个竞争的当前状态来源。Session 与 TaskRun 快照仍是控制面当前状态的
权威，Timeline 记录可观察的 Agent 行为。把全部状态替换为 event-sourced store 会
远超审查需求，并迫使进程、socket、授权租约和 Task 控制状态进入不自然的 replay 模型。

<a id="decision"></a>

## 决策

<a id="1-add-a-session-owned-agent-event-timeline"></a>

### 1. 增加由 Session 拥有的 Agent Event Timeline

每个 Session 组合一个 `internal/agentstream` Timeline，作为 Agent turn 事件的
canonical、Provider 无关的实时真相。它拥有：

- amux Turn 身份和事件顺序；
- Session 用于接收 typed producer emission 的精确 per-turn capability；
- typed event 校验；
- 每个被接受 turn 恰好一个 canonical `TurnFinished`；
- 终态幂等性，以及对终态之后事件的拒绝；
- 实时订阅和有界的 Session 策略投影能力。

Provider 和 ACP 实现直接发出不含 canonical 身份的 `agentstream.ProducerEmission`：
一个 typed turn payload、显式终态或 Session payload，附原生来源信息。
Session 把 turn payload 和终态传给捕获的精确 Turn；只有 Timeline 分配 canonical
身份与顺序。Task 直接订阅 typed Timeline 事件；CLI/MCP/web/飞书使用 Follow/Tail，
`agentui` 映射 canonical 事件。Session 从已经接受的 canonical 值派生元数据、权限、
活动和可见对话视图，不反向构造兼容事件，也不产生第二条实时流。

Timeline 不拥有 Session 授权、权限策略、watchdog 配置、进程控制、Task 状态、展示
策略或可见文本 replay 存储；这些职责留在原有模块。它是结构化 Agent 历史的持久化
来源，不是全部 Session 行为的来源。

外部边界是一个具体 Timeline，小接口围绕启动 turn、发布 Session 级状态和按 cursor
读取。持久化存储位于窄的内部 `Journal` 接口后；Session 将具体 Timeline 与 Runtime
SQLite store 组合。不引入全局 event bus，也不让消费者依赖数据库 Schema。

<a id="2-amux-owns-turn-identity-and-dispatch-correlation"></a>

### 2. amux 拥有 Turn 身份与派发关联

Queue 和 Timeline 分配 canonical TurnID 与 TurnSeq。Provider message ID、thread ID、
原生 turn ID 和原生 sequence 只作为来源信息，不能替换或覆盖 amux 身份。

启动 turn 是一个操作：Timeline 创建 Turn 并发出 `TurnStarted`，Session 安装绑定到
该 Turn 的 producer capability，然后用不透明 amux token 派发。
进程内 driver 通过 callback 返回 typed emission。ACP 和 Structured broker v3 都使用
版本化信封，每个关联 emission 返回不透明 token，以及 broker 分配的、turn 内单调
递增 sequence。Session 将每个 typed emission 映射到捕获 Turn 的 Emit 或 Finish。
Broker sequence 仅用于重连 replay，不是 Provider sequence，也不替代 canonical
`SessionID + Epoch + Seq`。Provider 原生 turn ID 不足以承担此角色，因为 queue
admission 与生命周期身份由 amux 拥有。支持 replay 的 broker 公告版本化
`turn_replay_version`，按 token 分配 sequence，并在尝试 socket 交付前把不可变
emission 放入进程本地 replay buffer。Broker frame 携带不可变 producer emission，
绝不携带 canonical SessionID、TurnID、Cursor 或 timestamp。

每个进程内 driver 都必须提供此 capability。它只在以下情况有效：协议严格单 turn
且终态是最后一帧，或者能够按原生 turn 身份路由复用事件。
共享 `EventSinks` 不为有歧义的流推断关联。Broker 必须公告并成功协商 correlated-turn
协议后才接受连接。仍存活的旧 broker 会被拒绝，不能通过 Session 级 Prompt frame
驱动 Provider。

<a id="3-dispatch-failure-is-an-observable-terminal"></a>

### 3. 派发失败也是可观察终态

`StartTurn` 创建 canonical Turn 后，driver 派发失败必须结束同一个 Turn：

```text
TurnFinished{outcome=error, code=submit_failed}
```

只关闭内部 gate 而不发终态，会形成不完整 turn：队列已经推进，Task、Attach 或飞书
却永远看不到已接受提交为何结束。

如果 per-turn capability 在派发返回错误前已接受 producer 终态，以已接受终态为准。
Timeline 不产生第二个终态。

<a id="4-turnfinished-is-the-only-terminal"></a>

### 4. `TurnFinished` 是唯一终态

Canonical 生命周期规则：

```text
TurnStarted 是被接受 turn 的第一个事件。
TurnFinished 是该 turn 最后一个被接受的事件。
ResultObserved 携带结果内容，绝不关闭 turn。
Diagnostic、Provider 错误通知、工具失败和 usage 本身不是终态。
Provider completion、watchdog 和 interrupt 路径必须通过准确的 correlated sink 发出显式终态，由 Session 映射为 Finish。
Session 拥有的 process-exit 路径可以直接 Finish 精确 Turn。
```

第一个被接受的终态具有权威性。重复同一终态是幂等操作，返回原事件，不分配新
sequence。第二个结果冲突时返回 `ErrTerminalConflict` 并保留首个终态；当前 Timeline
不自动追加诊断。已结束 Turn 的后续 emission 被拒绝，不会重新绑定到新 Turn。

稳定的 canonical outcome 为 `success`、`error`、`cancelled` 和 `interrupted`。
Provider 特定原因在 producer 协议边界规范化为稳定的 amux code 和诊断文本。
Task 直接把 canonical 终态映射为 `TerminalOutcome`，不检查 Provider stop reason
或原生 DTO。

<a id="5-session-performs-a-synchronous-completion-barrier"></a>

### 5. Session 执行同步完成屏障

Timeline 接受 `TurnFinished` 后，Turn 的可观察事件生命周期完成，但这本身不表示
Session 已准备好派发下一条输入。

队列推进前，Session 针对精确 Turn 身份同步执行完成屏障。在 MR1–3 中，屏障清理
canonical turn 的授权和待处理权限状态，完成后队列才可启动下一项。
Provider watchdog 在 producer 终态路径停止并清理自身状态；Session 元数据持久化
仍沿用原有路径，不属于该屏障。

屏障不是异步 Timeline 订阅者。缓慢或失败的 Attach、Task、飞书消费者不能阻止
生命周期清理，旧 Turn 的完成也不能清理新 Turn 的状态。
Interrupt 被接受后仍可在 Provider 报告终态前立即撤销授权，因为这是 Session 拥有的安全策略。

<a id="6-persist-the-timeline-before-delivery-preserve-derived-conversation-replay"></a>

### 6. 先持久化 Timeline 再交付，保留派生对话 replay

每个 Session 有一个不可变 Timeline epoch，随 Session 创建并在 amuxlet 重启后保留。
SQLite 严格按 sequence 存储封闭的 canonical event union。事件必须先追加并提交，
才进入内存历史或唤醒订阅者。追加失败时 Timeline 进入故障状态，不消耗 sequence，
拒绝后续写入，关闭订阅者并要求 Session 停止 producer。amux 不能继续产生未记录的事件后缀。

Cursor 在 transport 边界是不透明值，精确标识 `SessionID + Epoch + Seq`。
读取分为三种显式语义：

- **History**：返回 cursor 之后已提交的事件，然后到达 EOF。
- **Follow**：返回 cursor 之后已提交的历史，跨过一个无缺口的 caught-up 边界，再等待未来提交的事件。
- **Tail**：忽略读取开始时已有的历史，只等待未来提交的事件。

Attach 必须选择其中一种显式模式，并暴露 caught-up watermark；旧 `replay` 字段已
删除，其 wire number 保留。History 是只读交付操作：重放旧 canonical 事件绝不能
重新派发 Provider 输入、回答权限、调用工具或重复其它副作用。

Runtime 恢复时，Timeline 在暴露 Session 前校验完整 journal，恢复准确的 event/turn
sequence 和 sticky metadata，并将未完成 Turn 保留为活动 Turn。
若 producer 进程已退出，Session 追加一个
`TurnFinished{outcome=interrupted, code=runtime_restart}`，不重新派发旧输入。
未知事件版本或 kind、信封不匹配、sequence 缺口或无效 turn 生命周期都拒绝恢复。

如果 broker 在 Runtime 重启后仍存活且支持有序 replay，会把断线窗口 emission
保留在按不透明 turn token 和 broker sequence 索引的进程本地 buffer 中。
交付连接断开只清除当前 owner，不清除该 buffer。恢复的 Runtime 从持久化 Timeline
journal 中该活动 Turn 的已提交 canonical 事件取得最大 broker sequence，作为
`after_sequence`，然后调用 `rebind(turn_token, after_sequence)`。
Broker 按序重放 `sequence > after_sequence` 的 buffer emission，无缝转入实时交付，
不再次 dispatch。曾被有意忽略、因此没有 canonical journal 事件的 producer frame
可能重放后再次被忽略；replay 本身不得执行 Provider 或工具副作用。
这个进程本地 replay 接口不是第二份持久化日志，也不定义逐帧持久化 ACK 协议。
首版保留活动或最近完成 Turn 的 buffer，直到不同 Turn 的 dispatch 替代它；
不会静默淘汰重连所需的事件后缀。

仍存活的旧 broker 若未公告 replay 能力，恢复会拒绝执行，而不是容忍可能的缺口
进行 rebind，或重新派发输入。

首个持久化版本的保留策略遵循 Session 所有权：清理或删除 Session 会原子删除其
canonical Timeline 和派生对话消息。没有独立 event TTL、compaction 或跨 Session
全局顺序。Runtime 数据库仅 owner 可访问（`0600`）。只有稳定且经过策略过滤的
canonical union 持久化；原始 Provider frame、`Tool.RawOutput`、原始权限详情和
权限 `ToolCall.RawInput` 均被排除。

Session 元数据/权限记账、活动与可见文本 handoff 都是已接受 `agentstream.Event`
的有界同步投影，不构造另一个 Agent event，不暴露实时订阅者，也不成为另一份终态真相。

<a id="7-keep-taskevent-independent"></a>

### 7. 保持 `TaskEvent` 独立

`agentstream.Event` 描述一个 Session 的 Agent turn，`TaskEvent` 描述一个 TaskRun
的持久化控制面历史。它们在身份、保留、脱敏和交付方面要求不同，保持为独立模型。

Task 保留只追加事件日志、TaskRun 快照、脱敏、输出合并、task deadline 和 cursor
语义，Timeline 不取代这些职责。Task 直接消费 `agentstream`，映射 canonical 终态，
无需 Provider stop-reason helper。Producer 值仍是 Adapter 边界，但旧 v1 生命周期
和未指定模式的 Attach 路径已删除。

<a id="initial-implementation-sequence"></a>

## 初始实施顺序

接受的首轮交付范围明确限制为：

1. **MR1 — canonical 契约与影子一致性。** 添加 typed Timeline 事件、校验与测试，同时保持旧生命周期为权威。
2. **MR2 — per-turn 关联。** 为进程内 driver 将 Session emission capability 绑定到准确 Turn；ACP 与 Structured broker 都采用带 per-turn broker sequence 的版本化不透明 token 信封。在关联可观察且稳定前，权威仍留在旧路径。
3. **MR3 — 生命周期权威与完成屏障。** 关联后的 Session 使用 Timeline 决定终态真相和队列推进，为已有消费者与 replay 保留 canonical-to-legacy 投影。
4. **持久化 Session Timeline。** 交付前将封闭 canonical union 追加到 Session SQLite journal，启动时恢复，加入不透明 cursor 的 History/Follow/Tail 读取，暂时保留未指定的旧 Attach 模式。
5. **第一方消费者迁移。** Task 直接订阅 typed event；CLI/MCP/web 使用 Follow，飞书使用 Tail，`agentui` 消费 canonical event。未指定 Attach 模式保留为隔离的兼容边界。
6. **清理旧生命周期。** 强制关联的 `DispatchTurn`，拒绝旧 broker 和未指定模式的 Attach，然后删除 Session 级实时订阅者、structured replay cache、旧 turn gate 和终态推断。
7. **直接 typed producer emission。** Provider/ACP Proc API 和 broker v3 携带不含 canonical 身份的 typed payload 与显式终态；将接受后的 canonical 事件直接投影到 Session 策略，并删除共享 producer/session 兼容 Adapter。

以下内容明确不在本次实施范围：

- 在 sidecar 协议直接围绕 canonical 语义版本化之前，删除 Claude sidecar 原生 JSON DTO；
- 删除仅用于向可见 replay 追加本地系统通知的小型非 Agent `session.Event`；
- 用事件溯源替换派生对话 replay/handoff；
- 用事件溯源替换 Session 或 TaskRun 控制面快照；
- 持久化原始 Provider frame 或不透明工具/权限 dump；
- 将 TaskEvent 或展示策略并入 Timeline；
- 引入 Kafka、独立事件服务或完整 Session 事件溯源。

<a id="alternatives-considered"></a>

## 考虑过的替代方案

<a id="shared-event-value-object-only"></a>

### 只共享事件值对象

把 `provider.Event` 移到中性包并改成 typed union，是有用的迁移步骤，但作为最终
模块过于浅。Turn 分配、终态推断、队列 gate 和重复完成仍散落在调用方。
它不满足删除检验：删除该模块只会删掉类型，几乎没有行为需要重新散布到其它位置，
因为行为从未被收进接口后面。

<a id="full-durable-event-sourced-session"></a>

### 完整持久化、事件溯源的 Session

先把每个事件写入 SQLite，再应用 Session、Attach、Task 和飞书 projector，可以提供
强 replay 与 cursor 语义；但从日志重建全部 Session 状态，会让进程、socket、queue
admission、授权租约与 Task 控制状态依赖 replay。接受的设计只持久化 canonical
Agent Timeline，继续以 Session/Task 快照为权威。完整 Session 事件溯源仍被否决。

<a id="keep-providersession-terminal-inference-and-improve-tests"></a>

### 保留 Provider/Session 终态推断并加强测试

为每次转换添加更多测试可以减少局部回归，却仍保留多个真相源。每新增 Provider 或
展示表面，都需要理解 error、state 与 stop reason 的哪些组合构成终态。
这个方案没有解决架构根因。

<a id="consequences"></a>

## 影响

正面影响：

- 队列推进依赖一个显式 typed 终态，而不是分散的推断约定；
- 旧 turn 的延迟 emission 不会污染下一轮；
- 派发失败、watchdog、进程退出和 interrupt 共用可观察的完成模型；
- 结果内容与生命周期完成可以独立演进；
- Provider 特定变化留在 producer 协议边界后；
- Session 完成清理在一个边界变得确定且可测试；
- 已提交历史可跨 Runtime 重启按不透明 cursor 审查或续读，不重放 Provider 副作用；
- 先追加后通知，防止消费者看到尚未记录的事件；
- 后续 Task、Attach 和展示消费者可逐步删除重复映射与终态分支。

成本与风险：

- Per-turn 关联改变 broker 和 driver 的顺序，是迁移中风险最高的部分；
- canonical 授权与待处理权限清理的完成屏障正确之前，不得把队列推进交给 Timeline；producer watchdog 清理必须在终态 emission 前完成；
- 首版为每个 Session 将 canonical 历史加载到内存；超长 Session 未来可能需要有界分页或 compaction，但不能改变 cursor/读取契约；
- 先追加后通知会为每个 canonical 事件增加一次 SQLite 写入；
- 原生 Provider/ACP decoder 仍与协议相关，但输出边界统一为 typed producer emission 契约；
- 部署后仍存活的旧 broker 被拒绝，其运行 Session 可能结束或归档，而非原地迁移。

<a id="verification-invariants"></a>

## 验证不变量

首轮交付测试必须通过 Timeline/Session 边界证明：

- Canonical Turn 身份由 amux 分配，绝不由 Provider ID 决定；
- 派发失败恰好产生一个可观察的 `submit_failed` 终态；
- `ResultObserved` 位于终态之前，且不关闭 gate；
- 可重试诊断和工具失败不会隐式结束 Turn；
- 重复同 outcome 的 finish 幂等，冲突 finish 被拒绝；
- 旧 Turn 的迟到事件被拒绝，绝不分配给下一轮；
- Producer watchdog/interrupt 终态与 Session 进程退出通过各自 Adapter/直接路径汇聚为一个 `TurnFinished`；
- Turn N 的 Session 完成屏障在 Turn N+1 派发前完成；
- 缓慢实时消费者不能阻塞 Provider 进度或生命周期清理；
- 追加先于通知；追加失败使 Timeline 故障，保留最后已提交 sequence，并停止 producer 继续推进；
- History 到达 EOF，Follow 无缺口地从历史转入实时，Tail 只观察订阅后提交的事件；
- 同一 Session epoch 的持久化 cursor 跨 Runtime 重启仍有效，格式错误、指向未来或 epoch 不匹配的 cursor 被拒绝；
- 恢复校验完整生命周期，不重新派发未完成 turn；支持 replay 的存活 broker 从已提交 canonical 历史中的最大 broker sequence 续传而不 dispatch，不支持 replay 的旧 broker 拒绝恢复，已退出 producer 获取一个 `runtime_restart` 终态；
- 清理 Session 时原子移除 Timeline，持久化 JSON 中不含原始 Provider dump；
- 未指定 Attach 模式和旧 broker Prompt frame 均被拒绝；
- TaskEvent 保持独立持久化、有序、脱敏且可 replay。

结构上的完成标准是删除旧实现，而不是仅增加新包。Session 终态推断、broker 终态
重建、消费者特有的 Provider stop-reason 分支，以及 producer/session 兼容 Adapter
都已从 turn 生命周期删除。原生 wire DTO 留在各自协议 decoder 内，不是生命周期
权威或消费者事件流。
