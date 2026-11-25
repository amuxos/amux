# Session 与对话产品模型

[English](session-product-model.md) | [简体中文](session-product-model.zh-CN.md)

状态：当前实现。本文定义对话身份、飞书绑定和输入生命周期，与
[Agent 契约](agent-adapter-product-model.zh-CN.md)、
[Bridge 契约](feishu-bridge-product-model.zh-CN.md)和 [Task 模型](../distributed-task-model.zh-CN.md)配套。

## 对象与身份

Session 是 Runtime 持有的受管理对话，通过 target 和 Session 身份寻址，保留启动设置、
工作区、执行身份和可恢复的 Provider 历史。Agent Profile 是启动定义，不是 Session 身份。
原生 Provider 的对话 ID 是恢复句柄，不是 amux Session ID。重启可以更换进程而保留 Session。

重启和自动唤醒沿用已保存的执行设置。专家或引擎身份缺失时，先从启动快照恢复；
没有专家身份时，采用已注册的 `builtin/default`，否则采用发行版默认内置专家。
没有引擎身份时，采用发行版默认启动引擎。这些兜底不继承快捷入口的跳过权限确认设置。
已保存的模型、思考强度、权限设置和工作区继续生效。专家源包被删除不使已有执行快照失效；
已记录的引擎不可用时仍报错，不自动切换引擎。若引擎身份完全丢失，不能安全地复用原生
Provider 历史，恢复时使用 amux 历史交接创建新的 Provider 对话。

新建 Session 未显式提供原生权限参数时默认使用原生 bypass。CLI、MCP 和飞书入口一致，
与专家来源无关。解析后的默认值写入启动记录供重启和恢复使用；已有 Session 沿用已保存的执行设置。

Turn 是 Session 队列接收后派发的一次输入。多条飞书消息可能合并为一次提交；一条消息
也可能只是触发控制操作或增加上下文，不启动 Turn。规范 Timeline 持有 TurnID、TurnSeq，
每轮恰有一个 TurnFinished。出现结果文本本身不是完成信号。一个 Session 可以包含多轮 Turn。

飞书普通追问默认不抢占当前工作。活跃 Turn 期间收到的纯文本输入，先在默认两秒窗口内
与连续消息合并，然后注入或排队：

- **注入（steer）**：发送者正是当前运行 Turn 全部输入的那一位发送者时，输入固定到该
  TurnID 并加入当前 Turn。注入不创建新 Turn；该 Turn 在 Provider 处理完最后一条注入
  输入后结束，仍然恰有一个 TurnFinished。
- **排队**：其余情况一律作为下一个 Turn 的普通输入排队，包括群里其他成员的消息、
  group-watch 告警、Bot 或其他没有发送者的输入、不知道当前运行的 Turn、队列中已有
  更早的输入、Turn 正在被中断、Turn 授权不一致，以及 Provider 不支持注入。排队的
  追问不会抢占。
- **不投递**：Provider 可能已经收下输入但没有确认（超时或传输中断）时，本次提交失败，
  不再排队第二份，以免执行两次。

中断 Turn 也会取消注入其中的输入。对 Claude，CLI 中仍在排队的注入输入一开始运行就会
被中止，下一个 Turn 开头会附带一段提示，列出被取消的输入。设置
`AMUX_FEISHU_BUSY_INPUT=interrupt` 可恢复旧约定：Bridge 立即请求中断，再以抢占方式
提交合并后的输入。带材料的输入不经过文本合并。这与不抢占前序输入、排队执行的
`compact` / `clear` 控制不同。中断和上下文清理都不回滚已经完成的工具副作用。

TaskRun 是通过 Task 准入创建的一次有边界的子执行。普通飞书请求可以直接驱动 Session
的一轮输入，而完全不创建 TaskRun。界面把进行中的工作称为“任务”，不代表已经存在
TaskRun 记录。TaskRun 记录子 Session 及实际接受的精确子 Turn；Agent Instance 可以
跨多次 TaskRun 存在，每次运行有独立 Task ID。

## 飞书绑定的作用域

| 绑定 | 复用规则 | 边界 |
| --- | --- | --- |
| 话题 | 活跃绑定话题中接受的输入进入对应 Session | 新独立话题使用另一绑定 |
| 机器人 singleton | 符合条件的私聊或群聊输入复用 Bot Profile 绑定的 Session | 不自动按群或用户拆分 |
| 群 singleton | 已启用独立配置群的有效输入进入该群当前 Session | 按机器人和群确定作用域，与机器人 singleton 分开 |

群独立配置默认关闭。启用并为群配置后，它覆盖机器人消息处理默认值。已有普通话题绑定
仍可寻址；显式创建或绑定不会替换群当前的连续会话。退役群 singleton 会停止向旧会话
提交新输入，但允许已接受工作完成，包括 Bridge 重启后的输出恢复。

协作模式下，活跃话题里未 @机器人的普通讨论被记录为有限上下文，明确提及才触发工作。
负责人准入和绑定控制权限是独立检查。看得到卡片或话题，不代表所有读者都能驱动其 Session。

## 生命周期与用户操作

Session 状态为 `created`、`running`、`idle`、`sleeping`、`archived`，不能据此推断
Turn 状态或 TaskRun 状态。进程是否存在，也不能说明某轮是否在等待工具、权限或结束事件。

| 操作 | 含义 |
| --- | --- |
| 提交或回复 | 向已有对话排队提交新输入 |
| Detach | 关闭查看界面，保留 Session 和执行 |
| Interrupt | 请求中断当前工作，保留 Session 身份 |
| Sleep/resume | 停止或重新启动进程，保留可恢复状态 |
| Restart | 使用原 Session 设置和可用历史重新启动 |
| Compact | 请求支持的 Agent 压缩当前上下文 |
| Clear | 在支持时使用新的原生上下文，保留 Session 绑定 |
| 停止话题 | 终止其 Session 并结束话题绑定 |
| 新开群连续会话 | 退役旧输入指针，不取消已接受的工作 |

机器人级 singleton 模式拒绝话题式 run、bind、stop，提示使用管理入口。Agent 专属控制
可能不可用。清空上下文或改变绑定，不等于删除工作区文件、Hub 记忆或所有历史记录。
已结束话题不会悄悄变成新对话。更改默认专家作用于新启动，已有话题 Session 保持原专家；
singleton 更换专家走需要确认的管理流程。

## 上下文边界

原生对话历史、飞书上下文包、工作区、显式 Task 输入和 Hub 记忆是不同资源，不会因为
机器人、Profile 或用户相同就合并。话题上下文有范围和大小上限，不是整个聊天的导出。
子工作获得明确的任务输入和允许资源，不自动复制父对话的全部内容。

筛选和截断见[话题上下文](feishu-thread-context-model.zh-CN.md)，按 Principal 确定的
作用域见[记忆模型](agent-memory-product-model.zh-CN.md)。

## 实现依据

- [事件时间线设计记录](../maintainers/agent-event-timeline.zh-CN.md)保留 Turn 所有权与回放机制的历史设计理由和备选方案。
- [Session 状态与队列](../../internal/session/session.go)、[Timeline](../../internal/agentstream/timeline.go)。
- [话题决策](../../internal/bridge/inputrouter/router.go)、[路由](../../internal/bridge/router.go)、[机器人 singleton](../../internal/bridge/router_botsession.go)、[群 singleton](../../internal/bridge/router_group_session.go)。
- [控制派发](../../internal/bridge/router_control.go)、[命令策略](../../internal/bridge/command/catalog.go)。
- [TaskRun 字段](../../internal/task/types.go)、[持久实例存储](../../internal/agentinstance/store.go)。

## 原生目标

源码已实现；真实飞书端到端验收待完成。

只把消息开头的裸词 `goal` 识别为正式命令，`/goal` 不是同义入口。
命令包括 `goal <目标>`（创建并启动）、`goal`（查询）、`goal pause`、
`goal resume`、`goal clear`、`goal set <目标>`（显式替换）和
`goal budget <正整数 tokens>`。未完成的目标不能被隐式覆盖，不自动设置预算。
状态、token 用量和执行时间以原生 runtime 为准。恢复保留原生累计用量；
更改目标描述时遵循 provider 的计量语义。清除目标保留会话历史。

能力根据实际运行的 provider 发现。Codex 兼容 app-server 使用
`thread/goal/get` 探测；不支持的方法、关闭的 feature 和无效响应均不开放功能。
其他注册的 structured provider 通过 `pkg/extensions` 导出的 `GoalController`
及 `NativeGoalTurnProvider` 接入。Claude SDK 和 Pi 目前没有原生目标 adapter，
不展示目标帮助或操作。不能根据 Agent 名称判断能力，不注册原生 `/goal` 斜杠菜单。

话题模式的控制操作作用于已绑定的 Session；只有创建目标可以通过正常准入流程
启动并绑定新话题。bot singleton 模式的目标属于跨聊天共享的 bot Session。
输出固定在创建位置，在其他位置查询不会移动输出。显式替换目标会建立新的输出
位置，并使旧卡片按钮失效。创建和替换接受纯文本目标；附件及明确引用的材料应
通过普通跟进消息补充。原生会话历史仍可用，目标控制不会注入飞书话题历史。
原有 mention、负责人和群访问规则继续生效。

每次原生自动续跑都必须匹配持久化的有效目标授权、原生会话及目标描述，并由唯一
runtime owner 接纳。每次续跑分配新的 canonical Turn，使用正常的终止事件、
传输重放和 watchdog，不继承本地专家编写权限。待处理的用户输入优先。
只有匹配持久化输出位置的目标 turn 可以在飞书展示，其他异常 turn 保留隔离。
启动期间的通知等待 owner 接管。暂停先撤销续跑授权，再取消原生执行；清除同时
删除授权和输出位置。普通输入抢占保留目标授权，在输入队列结束后可以恢复因此
暂停的目标；显式中断和 `goal pause` 撤销授权。原生 blocked、usageLimited、
budgetLimited 和 complete 状态不会被自动恢复。owner 断开时不能接纳新的续跑；
如果隔离导致原生目标暂停，重连后使用 `goal resume`。

目标观察使用 session 级 canonical 事件及 sticky 快照，Attach 追平时恢复。
状态卡展示原生用量、预算和时间，覆盖英文与简体中文。卡片修改操作重新检查
权限、当前 Session 身份、输出记录代次和原生目标身份，旧卡片不能控制替换后的
Session 或目标。

Goal 权限请求沿用与该 Turn 回复相同的冻结绑定。已接收的普通输入会原子取消待处理的
goal 接续准入；普通输入执行期间收到的 goal 准入直接拒绝，不等待该输入结束。TAIL
Attach 在原子读取边界携带当前 canonical Turn 的开始快照。Bridge 仅恢复与持久化
目的地和当前绑定匹配的 goal Turn，授权其后续回复与权限请求，不重发之前的输出。

## 单轮结果

单轮结果收集默认开启（`turn_results.enabled=true`）并采用有界保留，可通过设置
`turn_results.enabled=false` 关闭。关闭时查询返回 `unavailable`（`reason=disabled`），
并在 Hub 启动时清空已缓存的单轮结果快照，不影响 Runtime 原有的 canonical timeline。

开启后，Hub 仅为开启之后新产生的 Turn 与定时任务执行记录只读结构化快照，并将三个独立事实分开表达：

- **执行态（`execution.status`）**：严格来自 canonical `TurnFinished`、`TaskRun` 终态或
  Schedule 准入失败（`missed`），绝不从回答文本反推成功。
- **最终输出（`output.status`、`output.content`）**：本轮最终采纳的 Markdown 正文，不含
  思考过程与工具中间进度。非完成态不返回半截正文；正文超过 `turn_results.max_content_bytes`
  （默认 1 MiB）时返回 `unavailable`（`reason=result_too_large`），不静默截断。
- **外部投递（`deliveries[]`）**：仅记录确认送达的外部回执（例如飞书主卡、拆分续卡及
  `amux-artifact` 附件的 `message_id`，以及 `delivered`/`partial`/`failed`/`suppressed`/`unknown`
  状态）。未绑定外部 IM 的轮次（如纯 CLI 会话、`new-session-per-run` 与 `subtask` 定时任务）
  以空 `deliveries` 直接落定，不阻塞 `ready`。

结果受 `turn_results.ttl_seconds`（默认 7 天，`0` 表示不按时间过期）、`turn_results.max_entries`、
`turn_results.max_total_bytes` 与 `turn_results.pending_timeout_seconds`（超时未凑齐转为
`unavailable`/`result_not_settled`）约束。读取始终只读，不会重跑模型、不会补发消息、不延长保留期，
并沿用与普通读取一致的 Principal Session 与 Schedule 权限边界。
