# Session、Task 和 Schedule

[English](execution.md) | [简体中文](execution.zh-CN.md)

飞书是交办工作的主要入口。例如，在话题中提出审阅需求会开始 Session 的一轮输入；
继续追问会在同一段对话中增加一轮。只有实际发起委派，才会创建子 TaskRun。
让同一持久专家继续工作，可以在它的 Agent Instance 上创建新的 TaskRun；Schedule 则保存
未来工作的触发规则。聊天中说的“任务”，范围比 Task API 更广。

根据后续如何继续工作来选择执行对象：

| 需求 | 使用什么 | 保留什么 |
| --- | --- | --- |
| 可以回来继续的长期对话 | Session | 对话身份、工作区和历史 |
| 有明确结束条件的任务 | Task | Task ID、状态、事件和结果 |
| 指定时间或周期执行 | Schedule | 向 Session 提交或创建子任务的触发规则 |
| 通过 MCP 复用同一个专家 | 持久 Agent Instance | 跨多次 TaskRun 保留的实例身份 |

`builtin/codex` 这样的 Profile 是启动定义，不是已经运行的对象。target 选择 Runtime
位置。选择 Profile 本身不会创建执行，启动 Session 或 Task 后才开始工作。

## 一段对话可以接受多轮输入

Session 接收的输入进入队列，按照会话策略逐轮执行。你可以查看、离开、继续发送消息，
或中断当前轮次，而不丢弃整个对话。休眠停止进程，但保留可恢复状态。
操作方式见[管理会话](../guides/cli-sessions.zh-CN.md)。

## Task 有完成边界

Task 带有提示词和执行设置，runner 在所选 Runtime 上创建子工作并报告状态与结果。
`--wait` 让调用 CLI 等待任务结束；省略它会返回 Task ID，之后再查询。
权限请求策略和工作区隔离是独立设置，详见 [Task 指南](../distributed-agent-usage.zh-CN.md)。

## Schedule 创建未来的工作

Session 定时规则向已有对话提交提示词，也可配置每次新建 Session。子任务定时规则在
每次触发时创建有边界的工作。触发时会重新检查授权，已保存的规则不能绕过后来撤销的权限。
时间由 Hub 计算，与创建规则的终端无关。详见[定时任务](../guides/automation.zh-CN.md)。

持久 Instance 和 TaskRun 的详细规则见 [Task 模型](../distributed-task-model.zh-CN.md)
与 [MCP 参考](../reference/mcp.zh-CN.md)。
