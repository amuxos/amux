# amux 如何工作

[English](architecture.md) | [简体中文](architecture.zh-CN.md)

amux 让 Agent 对话独立于访问它的终端或聊天窗口持续运行。同一套系统可以在本机运行，
也可以把执行分布到多台机器。

```text
飞书 / CLI / 终端 UI / 外部 MCP Host
                    |
                amuxd（Hub）
                    ^
             Runtime 主动向外连接
                    |
             amuxlet（Runtime）
                    |
         Codex / Claude / Pi 进程
```

## Hub 负责协调，Runtime 负责执行

Hub 跟踪可用 Runtime、路由请求、调度工作并执行控制面的授权检查。Runtime 启动 Agent
进程，管理 Session 和工作区。Task runner 运行在选中的 Runtime 上。
Runtime 通过反向 gRPC 隧道主动连接 Hub。

即使只有一台机器，也需要 Hub 和 Runtime；`amux setup local` 配置的就是这个结构。
使用 CLI、Task、Schedule 不需要飞书。Pack、凭据管理和本机恢复中的部分操作可以在
Hub 不可用时完成。

运维者通过 `amux system` 管理一套系统，Hub 和 Runtime 仍是独立服务。同一安装可托管
多个 Bot Profile，每个机器人的访问策略独立于系统部署位置。能使用机器人不等于拥有
系统维护权限；需要隔离进程、数据或故障影响时，可以部署独立系统。

## 访问界面与对话分开

终端 attach 为已有 Session 提供查看和输入界面，detach 不会终止会话。
可选的飞书 Bridge 将消息映射到 Session，并渲染结构化进度、结果和权限请求。
`amux mcp` 通过 stdio 向外部 Host 提供工具，不是 Hub 上的 HTTP 接口。

## 明确数据和凭据在哪里

安装状态默认保存在 `~/.amux/`，可通过 `AMUX_HOME` 调整。执行工作区和 Agent 进程属于
所选 Runtime；笔记本上的路径不会自动成为远端 worker 上的路径。Agent 的原生登录
必须在执行位置可用。可选的 amux 记忆由 Hub 持有，默认关闭，作用域由 Hub 身份策略决定。

## amux 提供什么能力

Agent Profile 描述启动策略，Pack 分发 Profile 和资源。amux 提供持久 Session、
有边界的 Task、定时任务、路由、授权和统一访问入口；模型访问及许多执行能力仍由底层
Agent 决定。公共内置 Profile 为 `builtin/codex`、`builtin/claude` 和 `builtin/pi`。

继续阅读 [Session、Task 和 Schedule](execution.zh-CN.md)、
[远端 Runtime](../guides/remote-runtime.zh-CN.md)，或维护者使用的
[Agent 行为契约](../current/agent-adapter-product-model.zh-CN.md)。
