# Task 操作指南

[English](distributed-agent-usage.md) | [简体中文](distributed-agent-usage.zh-CN.md)

首次本机使用先安装并认证所选原生 Agent，再执行 `amux setup local`。确认控制面和执行位置：

```sh
amux system status
amux system target list
amux agent list
```

提交并等待一个有明确结束条件的任务：

```sh
amux task submit "审查当前工作目录，给出有证据的缺陷清单" --profile builtin/codex --wait
```

`--wait` 使任务成为阻塞任务；阻塞任务不允许 `--permission escalate-to-main`。

省略 `--wait` 得到 task ID，随后可检查状态、跟随事件或取消：

```sh
amux task show <task-id>
amux task logs <task-id>
amux task cancel <task-id>
```

默认 workspace 为临时目录；任务需要代码库时使用 `--workspace git-worktree`
与 `--repo-dir /absolute/path/to/repository`，可用 `--commit <ref>` 指定起点。
`--timeout` 以秒计；`--permission` 可选择 `policy-allow`、`escalate-to-main` 或 `deny-all`。
后者策略只控制权限请求，不能替代 workspace、凭据或操作系统隔离。

收到权限升级事件后，按事件中的 permission ID 明确响应：

```sh
amux task respond <task-id> --permission <permission-id> --allow
```

拒绝时使用 `--deny`。凭据通过 `--credential-profile <name>` 引用，设置方法见
[凭据示例](../examples/03-credential-profiles/README.zh-CN.md)。已识别调用方使用其 Principal
的凭据；服务身份切换通过已授权的 `--run-as` alias，由 hub 解析并冻结身份。

定时或一次性工作使用 Schedule。给会话发定时提示用
`amux schedule create <session-ref> <spec> <prompt>`；独立子任务用
`amux schedule create-task <spec> --agent <profile> --task "<文本>"`。spec 支持
五段 cron 表达式、`every <间隔>`、`daily HH:MM`，单次执行用 `at <时间>`。
每次触发都重新校验权限；一次性 Schedule 触发后即消费，不重试。定时子任务接受
`--permission-policy default|policy-allow|deny-all`，不支持升级到主 Agent。
用 `amux schedule list`、`enable`/`disable`、`update`、`remove` 管理，用
`amux schedule runs` 查看历史触发。

结果保存在 task 历史中。清理前先查看候选：

```sh
amux task prune --older-than 7d --dry-run
```

确认后去掉 `--dry-run` 会删除终态历史。Persistent Instance 的 MCP 工具与普通 task
不同；外部 main agent 的 stdio 接入见 [MCP Host 接入](mcp-external-hosts.zh-CN.md)，完整工具
输入见 [MCP 参考](reference/mcp.zh-CN.md)。本文的模型边界见 [Task 模型](distributed-task-model.zh-CN.md)，
所有 CLI 参数以 [生成参考](reference/cli.zh-CN.md) 为准。
