<a id="agent-and-adapter-product-model"></a>

# Agent 与 Adapter 产品模型

[English](agent-adapter-product-model.md) | [简体中文](agent-adapter-product-model.zh-CN.md)

Codex、Claude 和 Pi 是公共集成。Adapter 描述可执行文件发现、启动配置、协议能力
和工作区配置布局。默认使用原生 Agent 的认证与模型设置；也可以通过 Profile Pack
显式配置外部模型 Provider。
公共 Provider 平台是 `openai-compat`，必须显式提供 `base_url`：Codex 使用 Responses，
Pi 使用 Chat Completions。附加 Provider 平台、默认端点和 Pi 兼容设置属于注册的发行版
扩展；公共 Pi 渲染器消费这些配置。Runtime 模型建议不是允许列表。
显式原生模型名直接传给所选 Agent，不据此推断 Provider、端点或凭据。
思考强度仍受 Adapter 支持的值约束。

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
普通 CLAUDE.md 指令仍然加载。Pi 使用 CLI 协议：已审查的核心没有需要关闭的内置
自动长期记忆。用户扩展可以自行实现记忆，这不在上述结论范围内；amux 不关闭无关
扩展。其它 Provider 明确提示记忆不可用，不会因为传输兼容就假定支持 Codex 的
记忆控制。不修改全局 Agent 配置和原生记忆文件。

private-home 和 Linux DAC 启动将 `CODEX_HOME`、`PI_CODING_AGENT_DIR` 固定在自身
HOME 下，并清空继承的 `CLAUDE_CONFIG_DIR`，保留该 HOME 内默认的 `.claude/` 和
`.claude.json` 布局。Claude SDK 继承 sidecar 的有效
启动环境；Provider 不得在 backend 完成隔离后重新覆盖 Runtime 的原始环境。
内置 Agent 的 Linux DAC 启动不修改服务用户的工作区信任状态；Pi 使用单次启动的
`--approve`，除非调用者显式指定信任参数。private-home 启动继续使用自身原生
信任文件。
参见[原生兼容性证据](../reports/2026-09-17-native-memory-compatibility.zh-CN.md)。
