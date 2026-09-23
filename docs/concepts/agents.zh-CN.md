# 助手、专家与 Agent

[English](agents.md) | [简体中文](agents.zh-CN.md)

在飞书中，你与机器人代表的**助手**对话，在设置中选择**专家**和运行引擎。
这些选择对应不同的身份：

| 概念 | 含义 | 身份与生命周期 |
| --- | --- | --- |
| Bot Profile | 飞书应用或机器人的连接、负责人、交互设置 | 一份机器人配置，可服务多段对话 |
| Agent Profile（专家） | 指令、资源以及允许的启动配置 | 如 `builtin/codex` 的引用，可重复使用的定义 |
| Agent 类型或引擎 | 执行工作的原生实现 | Codex、Claude 或 Pi，不是某次具体对话 |
| Runtime target | 启动并管理 Agent 的宿主进程 | Target ID，代表机器或执行位置 |
| Session（会话） | 一段受管理的持续对话 | 某 target 上的 Session ID，包含多轮输入 |
| TaskRun | 一次有边界的委派执行 | Task ID，跟踪子 Session、轮次和最终结果 |
| Agent Instance | 可接受后续委派的持久身份 | Instance ID，跨多次 TaskRun 存在 |

两次选择同一专家不会自动复用对话；两个机器人使用同一专家，也不会变成同一个机器人。
一个机器人可以路由到多个 Session。实例 ID 与其中每次执行的 Task ID 始终有区别。

## 改变什么，保留什么

- Session 中的新一轮输入继续使用当前对话上下文，受 Agent 的上下文限制和压缩机制约束。
- 话题模式下，新的独立话题启动自己的 Session；只有明确提供的话题上下文、材料、记忆
  或工作区资源才跨过边界。
- 持久实例上的新 TaskRun 可以复用 Provider 历史和持久环境，但结果和执行状态仍属于新一次运行。
- 更改默认专家影响新的启动，不会迁移所有已有 Session、身份、工作区或凭据。

Agent 的行为还取决于所选模型和可用工具。专家 Profile 不等于凭据、模型订阅或授权。

下一步：[对话与工作](execution.zh-CN.md)、[任务委派](../guides/delegation.zh-CN.md)，
以及维护者使用的 [Agent 模型](../current/agent-adapter-product-model.zh-CN.md)。
