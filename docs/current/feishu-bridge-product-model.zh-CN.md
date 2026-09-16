<a id="feishu-bridge-product-model"></a>

# 飞书 Bridge 产品模型

[English](feishu-bridge-product-model.md) | [简体中文](feishu-bridge-product-model.zh-CN.md)

可选 Bridge 拥有飞书/Lark API 行为、消息路由、Bot 命令、thread 与 workspace 绑定、
权限和输出呈现。它不拥有智能体执行，也不改变本地 CLI 的要求。

群值守与文档评论的执行覆盖遵循所选 Runtime 的模型策略。没有 Runtime 覆盖时，
遵循 Profile 模型策略：省略 `supported_models` 允许原生模型名；显式列表将选项限制
在列表内；显式空列表仅允许 Agent 默认模型。CLI 校验与 Bridge admission 使用同一规则。

Operator 通过官方应用注册流程配置 Bot Profile。Setup 申请已实现能力所需的权限和
事件。Personal 与 Team 治理独立于系统托管拓扑控制访问。Owner 操作与普通对话动作区分。

接受的消息映射到工作区和 Session。Session 输入进入队列；thread 上下文有界，并附在
已接受的提交上。智能体进度、工具活动、结果与权限提示均从结构化事件渲染。
Bridge 不从展示文本推断 Provider 语义。

每个绑定的 Session launch 只有一条活动输出流。并发绑定、输入路由和 Session-added
通知复用该流及其 readiness、turn 状态和卡片句柄。重复启动通知不能冻结已有进度卡，
也不能把同一 turn 的结果放到新卡片。Session 或 launch 身份变化、binding 迁移以及
流已结束时仍会建立新 attachment。新建 Session 可从首次 Hub 快照补齐身份而不替换活动流。

主卡与子卡标题使用相同的结构化用量摘要。已知上下文窗口时，最新调用总量显示为
`22.8k tokens (8%)`，否则省略百分比。报告缓存输入时，Provider thread 的原生累计
输入显示为 `累计输入 300k/343k（缓存/总量）`；没有缓存输入值时显示 `累计输入 343k`。
累计输出显示为 `累计输出 12k`，两者单位均为 token。显式零值保留，缺失的累计字段省略，
不从最新调用推断，也不累加更新。上下文占用继续使用最新调用，与累计消耗独立。
可选的首 token 延迟和 token 速度仍包含在摘要中。CardKit 与旧版卡片共用这些规则；
用量更新不会让每个事件帧都触发卡片刷新。

群值守接受允许的根消息，内容可以是文本、有效图片、文件或合并转发。
纯素材消息使用共享解析器的描述作为 Session 输入。已删除消息及无有效文本或素材的
消息不创建 Session。既有发送者、群、去重、容量和防循环规则继续适用。

普通对话、实时群值守事件和历史轮询通过同一路径收集素材。
每条消息拥有自己的收集上下文，轮询批次中附件不会串到其它消息。
素材获取使用当前 Bridge 应用身份和所属群，各入口共享 Session 能力、大小限制、
文件物化和错误提示。仅展开合并转发容器返回的后代消息。
只有飞书明确报告后代与容器属于同一群，才可使用后代原始消息 ID 获取资源；
否则仍以顶层容器 ID 获取。

Bridge 状态按职责分组为 controller、store 和 API port。路由与应用装配仍在 Bridge；
所有权调整保留事件顺序、流生命周期、权限和发行版 hook。

新工作区默认使用 `builtin/codex`。Bot 可以选择可用的 Profile 与 Runtime。
公共品牌指向 amux 官网 `https://amuxos.com/`。委派协议保留稳定标识
`urn:amux:protocol:feishu-external-bot-delegation:v1`，不随官网地址变化。
外部 Bot 发现和新建委派要求与当前群存在活动 binding，不允许跨群发现。
既有委派工作可使用已记录的对端身份完成。

公共发行版不包含旧控制台自动化，也不读取其它工具的网页登录缓存。
Operator 可以使用官方注册流程或手动配置应用。

独立维护的发行版可在 Bridge 首次使用前注册一个扩展，提供 Provider 呈现、协调
Profile 识别、旧默认值的事务性迁移，以及显式控制台 setup 回退。
首次使用后注册不可变，重复注册报错。这些 hook 不改变 Session 执行、canonical
事件、访问校验或默认官方注册流程。发行版特有实现和 Session 缓存路径属于发行版
module；公共 Bridge 无需它们也可独立运行。

Parent 活动卡汇总协作任务，不把其输出混入 parent Agent 的过程面板。
每个新发现的任务在每个 Bridge 进程内创建一张子进度卡，原地更新至终态结果。
标签由短 Profile 名加 Persistent Agent Instance ID 的前八位组成；普通任务使用准确
child Session ID，child 创建前临时回退到 TaskRun ID。Profile 标签去掉命名空间与版本，
缺失时回退到 Agent Type，再回退到 `agent`；身份后缀去掉 `agent-` 或 `task-` 前缀。
卡片显示状态、Profile、耗时和已知的有效模型。身份与模型来自同一 target 上的准确
child Session，不能取自 parent 或输出文本，并在终态清理后继续显示。

子卡只按工具 ID 聚合自身脱敏的结构化工具活动，在独立过程面板显示 TaskEvent 输出。
可附带 child Session 自己的只读查看链接。CardKit 创建失败时回退为一张旧版交互卡，
后续更新继续原地 patch 该卡。

Child 用量仅来自对应 TaskRun 的结构化 `usage` 事件和最终 `TaskResult.usage_json`。
基于 sequence 去重，确保 replay 和终态清理期间保留最新用量。
每张卡展示自身 child Provider thread 的累计快照；persistent child 复用 thread 时，
可能包含较早 TaskRun 的用量，不包含 parent 或其它 child 用量。

Parent turn 期间约每两秒发现一次任务，turn 结束时再做最后一次发现。
每个 watcher 从 sequence 0 重放 TaskEvent，断线后从最后序号继续，不重复事件。
状态、权限和结果立即更新子卡，输出更新按两秒合并。
Watcher 可在 parent turn 结束后继续存在，直至 child Task 终态、超时窗口到期或进程退出。
Parent 摘要保留约三十秒的刷新节奏。

## 群独立配置

群独立配置是实例级实验功能，默认关闭（`bridge.feishu.group_settings.enabled = false`）。
负责人通过私聊 `/amux` → 实验功能开启，选择“保存并重启”。保存只更新配置文件；
运行中的 Hub 保持启动时的开关，重启后生效。关闭时群编辑、旧卡片回调和本机运维
命令不可用。已保存独立设置的群暂停新输入和自动任务，不回退 Bot 默认，也不降低
隔离要求。群配置、Session 关联和已接受任务的输出保留；重新开启并重启后恢复
已保存的策略。未保存独立设置的群和私聊继续沿用既有 Bot 行为。

每个 `(bridge_id, chat_id)` 可独立设置处理方式、Profile、Runtime、模型、思考强度、
过程展示、target、cwd、参数、空闲超时、上下文条数及会话配额。负责人从有效 Bot
和工作区配置创建完整草稿，保存采用原子事务和版本校验。群未保存独立配置时保留
Bot 级行为。群访问模式与话题/连续会话模式、执行隔离分别管理。

新话题使用保存的群执行参数。Runtime 使用 Profile 默认值时也校验并落实显式思考
强度，仍遵循 Profile 的模型/Provider 选择与模型策略。显式 `/run` 选择和绑定 Session
保留自身身份；修改默认值不会改变已有 Session 的 Profile 或执行配置。要求隔离时，
不得静默降级或通过绑定旧执行绕过。

Linux 隔离能力与飞书会话的隔离选择相互独立。新建普通话题和连续会话显式选择
passthrough；隔离话题选择 linux-dac，不可用时拒绝启动。该选择遵循有效群设置，
私聊和未配置群使用 Bot 默认值。启用宿主隔离能力不会自动隔离所有飞书会话。
宿主要求可识别身份时仍保留执行 Principal 和 actor credential；passthrough 只改变
进程隔离方式，不改变执行授权。已有或显式绑定的 Session 保持冻结的执行环境。

群连续会话按 Bot 和群各维护一个 Session。回复、进度、权限请求和产物直接发送到
该群，与 Bot 连续会话的输出方式相同。新输入和显式指向输出消息的回复进入同一
队列。首次输入只作为内部绑定锚点，不作为话题输出目的地。不同群和私聊不共享
Session。改变模式或负责人新开会话会退役旧默认关联，已接受任务继续完成。
回复已退役入口时提示新开话题；普通已有话题关联仍可使用。

启用 Principal 隔离时，群连续会话和 Bot 连续会话都要求 Bot 作用域的
`service:<bot-profile>` alias，以及发起者的有效 service membership。个人话题身份
注册不会自动建立共享执行身份，群的 `shared` 访问模式也不授予 membership。
运维人员应在启用隔离时配置这些前提，见
[Linux 隔离指南](../operations/linux-dac-isolation.zh-CN.md#飞书连续会话的共享身份)。

群值守的资格和继承执行参数遵循目标群策略；显式能力执行覆盖仍须满足群隔离要求。
完整设计及状态转换见[实现规格](../superpowers/specs/2026-09-16-feishu-group-session-defaults-design.zh-CN.md)。
