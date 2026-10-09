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

Turn 是 Session 队列接收后派发的一次输入。多条飞书消息可能合并为一次提交；一条消息
也可能只是触发控制操作或增加上下文，不启动 Turn。规范 Timeline 持有 TurnID、TurnSeq，
每轮恰有一个 TurnFinished。出现结果文本本身不是完成信号。一个 Session 可以包含多轮 Turn。

飞书普通追问可以抢占当前工作。活跃 Turn 期间收到纯文本输入时，Bridge 立即请求中断，
在默认两秒窗口内合并连续消息，再以抢占方式提交；带材料的输入不经过文本合并。
这与不抢占前序输入、排队执行的 `compact` / `clear` 控制不同。中断和上下文清理都不
回滚已经完成的工具副作用。

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
