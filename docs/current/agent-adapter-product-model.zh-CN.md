<a id="agent-and-adapter-product-model"></a>

# Agent 与 Adapter 产品模型

[English](agent-adapter-product-model.md) | [简体中文](agent-adapter-product-model.zh-CN.md)

## 产品身份

飞书是主要的对话式产品入口。助手由 Bot Profile 表示，所选专家对应 Agent Profile。
Profile 提供可复用专家内容，Agent 类型是执行引擎实现，Runtime target 是执行位置；
它们都不是某段运行中对话的身份。

Manifest v2 专家要求调用方选择 Runtime，不能声明引擎、模型、Provider、模型白名单或
权限设置。原生执行参数及 Runtime/模型选择属于调用方；省略权限设置时使用原生引擎默认值。
仅接受 v2 manifest，本地专家编辑也使用 v2。解析后的执行设置冻结到会话快照；刷新专家内容
保留这些设置，包括 Provider 绑定及权限参数。
MCP 的 `session_run`、`task_run`、`task_submit` 和 `instance_spawn` 输入将 Runtime、
模型选择与专家引用分开提供。任务提交与持久实例创建通过 `extra_args` 暴露原生执行
参数；定时任务定义保留这些参数供每次触发使用。这些参数用于配置原生引擎，
与 Task 权限策略分开。
CLI 任务提交和定时任务也接受 `--runtime`；定时任务定义保留该选择，供每次触发使用。

Session 持有对话并包含多轮 Turn；持久 Agent Instance 持有跨任务身份和持久执行引用，
每次交办都是独立 TaskRun。原生 Agent 的子 Agent 不自动创建 amux 实例。
详见 [Session 模型](session-product-model.zh-CN.md)和 [Task 模型](../distributed-task-model.zh-CN.md)。
面向用户的术语说明见[助手、专家与 Agent](../concepts/agents.zh-CN.md)。

内置 Profile 同样采用 manifest v2，不包含执行字段。amux 调用方独立维护内置快捷入口的
启动预设，使已有快捷启动继续选择配置的 Runtime、模型和权限；调用方显式设置优先。
外部 v2 专家的 Pack 不拥有执行预设。直接启动会话仍要求调用方提供 Runtime；Task
提交（包括定时 Task 与持久实例创建）省略 Runtime 时，采用发行版默认 builtin 的
启动预设。此回退只提供 Runtime/模型选择，不借用默认专家的内容或权限。
专家发现和内容读取不把预设写回
Profile；仅在启动快照中冻结最终执行设置。

## 引擎与 Provider 行为

Codex、Claude 和 Pi 是公共集成。Adapter 描述可执行文件发现、启动配置、协议能力
和工作区配置布局。默认使用原生 Agent 的认证与模型设置；外部模型 Provider 由发行版
扩展提供的 amux Runtime/模型绑定配置，v2 Profile Pack 不能声明 Provider 设置。
公共 Provider 平台是 `openai-compat`，必须显式提供 `base_url`：Codex 使用 Responses，
Pi 使用 Chat Completions。附加 Provider 平台、默认端点和 Pi 兼容设置属于注册的发行版
扩展；公共 Pi 渲染器消费这些配置。Runtime 模型建议不是允许列表。
显式原生模型名直接传给所选 Agent，不据此推断 Provider、端点或凭据。
思考强度仍受 Runtime 目录为所选模型声明的支持值约束。

发行版可以在 Runtime 目录中为模型提供可选显示名。模型选择和会话摘要使用该名称，
未提供时回退为模型 ref。显示名不改变持久化 ref、Provider 绑定、上游模型名或已有会话。

兼容 Codex 的 Provider 在重启或恢复时明确报告已保存的对话不存在，Runtime 会使用
可用的 amux 对话交接创建一次新的 Provider 对话。amux Session、执行设置和待处理的
handoff 保持完整。其它启动失败仍作为错误处理，不丢弃已保存的 Provider 对话身份。

对兼容 Codex 的 Provider，`provider.structured.repeat_tool_call_limit` 默认允许同一
turn 中连续 50 次相同工具调用。防护按相同调用定义 payload 的唯一 started item
计数；重复帧不累加，不同调用或新 turn 会重置计数。返回内容变化不会重置计数。
达到阈值时，以 `code=repeat_tool_loop` 结束并标记 turn 失败。0 禁用防护，负数使用
Provider 默认值，覆盖值上限为 1000。

Codex 使用 app-server 协议，Claude 使用 Claude Agent SDK sidecar。
Provider 与 ACP 实现直接发出 typed payload。Session 拥有的 canonical Timeline 分配
turn 身份与顺序，并且每个 turn 只接受一个终态事件。Provider message ID 仅保留为
来源信息。观察到结果本身不结束 turn。Session 拥有完成策略和队列推进权。

Session 有明确的生命周期：`created`、`running`、`idle`、`sleeping`、`archived`。
运行中的 Session 只有在空闲超时（不低于 60 秒）到期、队列为空、没有进行中的
轮次且没有待处理权限提示时，才符合自动睡眠条件；睡眠原因记录为手动、空闲超时
或重启失败。睡眠的 Session 保留身份与工作区，收到新输入时唤醒；归档后不再可路由。
重启、恢复以及模型/思考强度的热切换（`set-model` 不重启进程）取决于所选 Agent 的
能力：Codex 支持进程内重配，其它 Agent 通过交接重启完成。

ACP Agent 的输出关闭后，broker 停止接收新请求，等待正在处理的请求收尾后再关闭
客户端连接。待处理的 prompt 结果、终态事件和确认响应保持原有顺序。连接写入有
退出超时保护，避免不响应的客户端阻止 broker 退出；传输失败仍按错误处理。

兼容 Codex 的 Provider 将 `thread/tokenUsage/updated` 规范化为 `UsageObserved`。
`tokenUsage.last` 描述最近一次调用，提供 token 数和上下文窗口使用情况。
可选的 `tokenUsage.total` 提供 Provider thread 的原生累计快照，映射为
`cumulative_input_tokens`、`cumulative_output_tokens`、`cumulative_total_tokens`、
`cumulative_cached_input_tokens`、`cumulative_cache_creation_input_tokens` 和
`cumulative_reasoning_output_tokens`。缺失累计值时省略，既不累加快照，也不从 `last`
推导。最新用量也由 `ResultObserved.Usage` 携带。累计消耗与上下文窗口占用分开，
不跨 Provider thread 或 child Agent 合并。

Provider Adapter 不解释 Bridge 展示文本。可复用终端 UI 只依赖回调和接口，不依赖
内部 Session、transport 或飞书包。CLI 与 Bridge 消费者呈现相同的、已被接受的
canonical event。权限采用结构化请求和关联响应。

公共内置 Pack 恰好包含三个原生 Agent Profile，不嵌入端点、凭据、外部 Skill 或
模型服务绑定。可以配置原生 Responses Provider，但不提供 Responses-to-Chat
兼容网关。不支持的 Agent 和 wire mode 在进程启动或凭据物化之前即失败。

附加发行版在启动应用前，通过 `pkg/extensions` 组合静态注册的 Adapter 与 Provider
factory。注册的 Provider 与内置 Provider 共用 Session Timeline 和隔离契约。
未知 Provider 和重复注册在启动前失败。公共 module 必须能够不依赖任何发行版
module 独立构建和测试。

Codex 原生模型选项由选中的 Runtime 使用执行身份的原生环境，通过
`codex app-server` 的账号接口和分页 `model/list` 动态获取。打开或刷新设置编辑器
会重新获取模型名及推理能力；Runtime 选择器不使用内置 GPT 模型列表。显式 Provider 绑定
独立保留。未登录、Runtime 不可达、目标不唯一或发现失败时，不回退到猜测的模型；
编辑器显示状态并保留已有选择。原生目录是建议而非授权：没有实时能力快照时，
显式输入或已保存的原生模型及推理档位交给 Codex 在执行时校验；实时快照约束
编辑器中的推理选项。可选模型服务与原生 Codex 登录相互独立。

## 按需系统记忆

Hub 启用记忆后，新建 Codex、Claude、Pi 会话会获得固定短协议，不依赖 Agent
Profile。Runtime 将 Hub 派生的绑定冻结为非秘密启动元数据；记忆正文保留在 Hub，
只有显式工具读取的片段进入会话。普通轮次不触发 Runtime 记忆查询。重启和持久
Instance 复用原有绑定；未记录绑定的旧会话需新建会话才能启用。私人绑定必须与有效
执行 Principal 一致，每次工具调用仍由 Hub 按当前策略鉴权。

Codex 追加 developer instructions，并用进程/线程配置关闭原生记忆读取及生成，
同时持久设置线程记忆模式为 disabled。不支持这些原生契约的版本在首轮前明确失败。
Claude 合并 append-system-prompt 参数，每次受管启动（包括恢复）都设置
`CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`。它也关闭 Claude 子 Agent 的持久记忆；
普通 CLAUDE.md 指令仍然加载。Pi 启动使用其 JSONL RPC 模式：已审查的核心没有需要
关闭的内置自动长期记忆。用户扩展可以自行实现记忆，这不在上述结论范围内；amux 不关闭无关
扩展。其它 Provider 明确提示记忆不可用，不会因为传输兼容就假定支持 Codex 的
记忆控制。不修改全局 Agent 配置和原生记忆文件。

private-home 和 Linux DAC 启动将 `CODEX_HOME`、`PI_CODING_AGENT_DIR` 固定在自身
HOME 下，并清空继承的 `CLAUDE_CONFIG_DIR`，保留该 HOME 内默认的 `.claude/` 和
`.claude.json` 布局。Claude sidecar 在加载 SDK 前，从自身环境及 SDK 子进程环境
中移除空值 `CLAUDE_CONFIG_DIR`；SDK 将空字符串视为相对目录，并不会采用默认目录。
Linux DAC 启动同时设置 Claude 的 `IS_SANDBOX=1` 兼容标记：helper 校验独立的
非 root 宿主机 UID，即使进程在用户命名空间内看到 UID 0。该标记不启用权限模式，
也不改变隔离加固；private-home 和普通启动不会由 amux 自动添加此标记。
Claude SDK 继承 sidecar 的有效
启动环境；Provider 不得在 backend 完成隔离后重新覆盖 Runtime 的原始环境。
内置 Agent 的 Linux DAC 启动不修改服务用户的工作区信任状态；Pi 使用单次启动的
`--approve`，除非调用者显式指定信任参数。private-home 启动继续使用自身原生
信任文件。
参见[原生兼容性证据](../reports/2026-09-17-native-memory-compatibility.zh-CN.md)。

Linux DAC 物化阶段以目标用户创建 `.codex`，即使没有原生凭据文件也能启动；
保留已有内容，并拒绝符号链接目标。

## 需要登录的模型服务

发行版可独立于执行引擎注册模型服务。选择服务模型保持原执行引擎及其工具执行逻辑。
Hub 按 Principal 管理登录态及上游凭据；包括远端 Runtime 在内的每次模型请求，
都通过现有 AMUX actor 凭据鉴权。Broker 管理与 Agent 同生命周期的回环网关。
模型引用和启动规格不保存凭据或登录 challenge。目录保留上游模型与配置的组合以及
声明的思考能力；未知或不可用的选择明确报错。公共发行版不注册任何模型服务。

发行版模型服务在写入响应前分类错误。Hub 和 broker 网关保留分类后的 HTTP 状态与
安全诊断，包括登录（401）、权限（403）、限流（429）和超时（504）。未知错误只暴露
通用的不可用提示。此协议不携带查看者语言，因此分类诊断同时提供英文和简体中文；
宿主语言设置不改变 JSON 输出。开始流式响应后发生的错误中断流，不伪报成功。

Claude 启动带有 `AMUX_MODEL_SERVICE` 标记时，sidecar 在接受输入前通过 SDK 内存中的
flag-settings 控制接口应用选定网关、凭据、模型别名及上下文/输出限制。这些值优先于
用户、项目、本地设置，保留其它设置和管理策略；凭据不进入命令行参数或设置文件。
应用失败时明确拒绝启动。SDK 结果带 `is_error` 标记，或 subtype 不是 `success` 时，
该轮以失败结束并保留报告的错误信息。

## 发行版定制契约

Provider 校验始终先执行公共必填字段与通信协议规则。发行版只校验额外的
Agent/平台组合；原生 Codex、Pi 的 OpenAI-compatible 规则由公共核心维护。

工作区规则文件和 Skill 使用 Adapter 声明的配置布局。Claude 的附加指令文件等
特殊渲染继续由公共核心处理；发行版不再重复声明普通投影路径。

提供可选 Responses-to-Chat 功能的发行版注册 Runtime 命令构造器。公共核心负责
凭据传递、隔离和进程生命周期；发行版负责 supervisor 命令及参数。公共发行版
仍不提供兼容网关。

配置布局支持工作区 Skill 的 Adapter（包括 Codex）通过 `ConfigLayout` 声明该支持；
MCP 的 `supports_skills` 能力信息据此返回。

Runtime 的模型与 Provider 连接配置由 amux 发行版注册，独立于 builtin Pack 内容。
Runtime 模型目录不读取 Pack 的 Provider 定义，Pack 不得定义 Provider；已有执行快照保留运行时连接配置。
