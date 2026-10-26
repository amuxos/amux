<a id="distributed-task-model"></a>

# 分布式 Task 模型

[English](distributed-task-model.md) | [简体中文](distributed-task-model.zh-CN.md)

## Task、TaskRun 与 Agent Instance

本 API 中的 Task 是通过 Hub 准入的一次有边界的执行，具体记录为 TaskRun，包含 Task ID、
子 Session 与 Turn 的关联、状态和结果。普通飞书对话输入是 Session Turn，不一定创建
TaskRun；界面出现“任务”一词本身不代表 Task 历史中有一条记录。

临时 Task 为当次交办使用子执行。持久 Agent Instance 可依次承载多个 TaskRun，保留
稳定的 `agent_instance_id`、固定 Runtime、工作区和 HOME 引用以及 Provider 恢复句柄。
只复用 Profile 引用不会复用实例。实例状态（`active`、`idle`、`archived`）、Session
状态和 TaskRun 状态是不同维度。TaskRun 终态为 `succeeded`、`failed`、`cancelled`、`timed_out`。

`instance_followup` 提交工作并触发执行；`instance_send` 只写入信箱，等后续唤醒时交付。
`instance_steer` 把纠正加入实例当前运行的 Turn；没有可接收的 Turn 时回落到信箱，无法
确认是否已投递时则不写入信箱。信箱输入在下一次 Task 提示之前交付，`instance_followup`
复用仍在运行的子 Session 时也是如此。
中断实例会取消其当前所属 TaskRun，并保留实例。Task 接收明确输入和获授权资源，不默认
获取父会话全部聊天历史。父子身份记录保留来源，但不合并上下文。详见
[Session 模型](current/session-product-model.zh-CN.md)。

续跑省略启动身份时，先恢复实例已保存的 Profile、引擎和模型，再解析默认值，
不会重新选择发行版默认引擎；显式身份变更仍受实例不可切换执行身份的约束。

## 调度与执行

Hub 认证请求、冻结执行身份、选择合适的 Runtime，并在派发前预留容量。
选中 Runtime 的 Task runner 在本地执行 child Session，在进程内强制校验所有权。
Hub 路由请求并维护 Task 到 target 的真相映射，不直接驱动 child Session。
终态 Task 通过 reconciliation 释放预留容量；未提交的预留还会按 TTL 过期作为兜底。

Task 是有界执行，拥有规范化终态结果、结构化结果、事件日志和文件变更证据。
Agent Instance 持续存在并接受后续工作。取消和中断始终与正在运行的执行关联。
权限决策必须显式作出，并绑定到提出请求的 Task 或 Instance。
Instance 中断通过 child 的 owner TaskRun 进入取消流程：runner 记录取消意图，将该
次运行结束为 `cancelled`，并保留可恢复实例。不得依赖直接中断 child 后 Provider
产出终态事件。MCP 通过生成的工具 Schema 暴露 Profile、Task 和 Persistent Instance。
任务提交、持久实例创建与定时任务定义的 MCP 输入通过 `extra_args` 提供原生引擎参数，
`permission_policy` 统一控制 Task 的原生启动权限和审批回复。原生权限参数按该策略归一化，其他参数保留顺序；
MCP 回读展示已保存的 Runtime、模型与原生参数。

Credential Profile 标识启动凭据。Profile 声明所需环境变量，只为有效 Principal
物化这些变量。凭据不得持久化到 Session 快照、结果 payload 或公共事件文本。
显式执行覆盖必须按 Profile 策略和 Runtime 能力验证。

Task 的执行选择由 amux 管理，不属于 v2 Profile 内容。显式 Runtime、模型优先；
从 Session 提交的 Task 在同引擎时沿用该 Session 的当前模型与思考强度；继承的
执行选择无法在子 Profile 中解析时保留调用方自己的解析结果。
未指定 Runtime 时，builtin 快捷入口采用自己的启动预设，其余专家采用发行版默认
builtin 的启动预设。继承 Runtime 时，未指定的模型采用预设模型；没有配置模型时
使用所选引擎的原生默认值。这些默认值不替换专家内容、权限策略或原生参数。
公共发行版的 Runtime 模型建议不是允许列表：未列出的原生模型名仍原样传给所选
Agent，不据此推断 Provider、端点或凭据。Adapter 校验继续生效。

隔离是启动策略和强制执行方式，不是 UI 标签。Runtime admission 在启动前验证平台
支持和工作区访问。强容器/VM 隔离及跨机器高可用仍是目标规格，不是分布式 API
隐含提供的能力。

Profile 消费支持 manifest v2，遵循 Profile 物化到 Task/Adapter 契约的单向依赖。
可选的本地专家编辑需要显式配置兼容的 authoring runtime；不可用时拒绝执行该能力，
原生内置 Profile 仍可运行。

Canonical `ToolChanged` 事件也会投影为持久化 Task `tool` 事件。
其中只包含脱敏后的工具 ID、名称、标题和状态；原始输入、内容、diff、位置和输出
保留在 Agent Timeline。工具活动既不改变 TaskResult，也不决定 Task 完成。

Canonical `UsageObserved` 和 `ResultObserved.Usage` 也会投影为持久化 Task `usage`
事件，携带名为 `usage` 的 JSON 对象。仅保留已知的非负整数 token、context 和 speed
指标。相同快照去重，事件按准确 TurnID 过滤，最终快照先于终态发布。
`TaskResult.usage_json` 保留最终用量。累计字段属于 child Provider thread；
复用 thread 的 Persistent Instance 可能包含较早 TaskRun 的消耗。
它们不是当前 TaskRun 的独占用量，parent 与 child 用量也不相加。

由 `amux-session:<session_id>` 发起的协调委派通过既有 Task admission 与授权保留
parent Session，并解析活动 ParentTurnID。其它发起者不推断 parent。
协调等待在 `tool` 和 `usage` 事件上推进 TaskEvent cursor，不人为生成 status 或文本
事件，也不因纯 tool 或用量更新唤醒 parent Agent。

子 Session 与交互式 Session 使用相同的 [Hub 记忆模型](current/agent-memory-product-model.zh-CN.md)。
新建子会话以有效启动凭据查询记忆范围，独立于 Profile 保存范围前提。Hub 用户隔离开启时，
服务执行使用有效服务 Principal 的记忆；非隔离时所有 Runtime 共享全局记忆。
恢复已有子线程时保留范围绑定；持久实例不能改变有效 Principal。Task 子会话通过记忆 CLI
访问，不额外注入可以递归创建任务的 MCP 工具。能力查询失败时显示告警，该会话不启用
amux 记忆，不回退到全局范围。

## 定时执行与权限边界

创建或更新定时 Task 时，使用与 Task 提交相同的默认值校验省略的执行选择。定义保留
调用方的 Profile 引用、Runtime、模型和原生参数；每次触发时重新解析内容与省略的
默认值，历史任务也采用同一路径。Task 接纳后冻结执行快照，重试继续使用该快照。

Hub 定时规则可以向 Session 投递提示，也可以提交临时子 Task。MCP `schedule_create`
默认（`schedule_type=subtask` 或省略）使用子 Task 路径，历史记录保存 Task ID 和对账后的
执行终态，不承诺把结果发送回原飞书聊天。`ok:task:<id>` 只表示提交成功，最终结果以
Task 记录为准。`schedule_type=session-prompt` 时用显式 `session` session-ref 指定目标
（或回落 `AMUX_SESSION_ID`），解析与 CLI `amux schedule create <session-ref>` 相同，到期
Submit 到该 Session（`Scheduler.deliverReuseSession`），可唤醒 Agent；Hub 记录准确的 Turn，
并以 canonical 终态对账。`ok` / `skipped:offline` 等提交状态与执行完成分开。
重复规则在每次触发尝试后推进，即使投递失败；已消费的一次性规则不重试。停用或删除
Schedule 不会取消已经接受的 Task。实现见 `internal/schedule/spec.go`、
`internal/schedule/scheduler.go` 和 `app/cli/mcp_schedule.go`。

Task 默认权限策略是 `policy-allow`。Codex 兼容引擎在 process 和 Linux DAC 下，均为
`policy-allow` 和 `escalate-to-main` 使用 `danger-full-access`；前者使用
`approval_policy=never`，后者使用 `approval_policy=on-request`。这两种策略关闭原生沙箱；
process 执行受服务账号的操作系统权限约束，Linux DAC 保留外层 Principal 边界。
Claude 和支持审批的发行版 Adapter 在 process 和 Linux DAC 下均为 `policy-allow`
使用原生 bypass。`deny-all` 使用原生只读或拒绝模式并拒绝审批；`escalate-to-main` 保留原生
审批入口，上报并等待显式响应。无法实现受限策略的引擎在启动前报错，Pi 仅支持
`policy-allow`。原生模式不将 process/private-home 升级为强隔离。

最终参数和策略固化到子会话执行快照。Instance 后续任务沿用已保存策略；修改策略需要
新建 Instance。没有固化 Task/原生统一权限策略的旧子会话快照不复用，需要新建实例。
审批回复失败立即使 Task 失败，不等待整个截止时间。阻塞的 `task_run` 和定时子任务
不支持上报。权限解析与工作区、操作系统隔离及凭据授权分开。实例等待操作超时也不等于
对应 Task 被取消或超时，最终结果仍需查询该 Task。

## 用户问题不属于执行审批

当前 Task 子会话没有业务问题转交通道。因此任何权限策略（含 `policy-allow`、
`deny-all`、`escalate-to-main`）遇到结构化用户问题，都使 Task 明确失败并给出
中英文诊断；不会自动选择首项或末项，也不会进入执行权限上抛。调用方应在可交互
Session 中收集缺失信息后重新提交 Task。此规则同样适用于定时子任务和持久实例。
直接 Session 的业务问题交给附着界面，不由自动审批策略代答；向其投递的定时
prompt 使用相同规则。

## 定时任务执行感知

Hub 在提交前持久化每次触发，并关联对应 Task 或准确的 canonical Turn。提交成功不等于执行完成；暂时无法观测时保留待确认状态，不推断失败或自动补跑。旧的无关联历史不触发主动通知。

飞书个人任务视图及每个操作均校验冻结的创建人、应用范围、有效身份绑定与执行权限。创建人来自可信请求和准确的飞书 Turn 回执，不采用最近发言人、任意收件人或 Bot owner。无法归属的任务不开启主动提醒。既有 operator/CLI 授权保持原约定。状态通知统一由 Bot 私聊发给当前配置的管理员（owner），并标明任务创建人。管理员通知中心展示任务名称、执行状态及通知操作（知道了、静默、重新检查），不授予查看其他创建人的 prompt、结果或控制执行的权限；个人任务视图仍按创建人隔离。

周期任务正常完成不打扰；失败和未执行聚合 60 秒，待确认持续五分钟才提醒。持续异常更新原卡，未确认异常最多每天催办一次；稳定恢复十分钟后关闭异常卡。一次性任务有结束回执。知道了停止催办，静默持续 24 小时，暂停只影响后续触发。默认在调度时区的 22:00–08:00 安静，同一管理员/应用每 30 分钟最多三条新状态消息。

投递状态与执行结果分开记录。持久化 outbox 使用固定 UUID 重试暂时故障（飞书去重窗口为一小时）。230013/230053 可用范围或屏蔽错误暂停投递，显式重新检查后再尝试，不回退给其他收件人。未配置管理员时保留待通知状态并展示投递诊断。更换管理员或迁移旧的创建人投递时，清理旧 outbox、卡片 ID 以及收件人对应的错误、确认和静默状态；执行状态、创建人及任务提醒开关保持不变。仍在持续的异常可通知新管理员，已完成的历史回执不重放。`/amux` 提供个人摘要、分页历史、结果及这些操作，中英文同步。
