# 完成第一次会话

[English](cli-sessions.md) | [简体中文](cli-sessions.zh-CN.md)

启动 Agent，发送一个小请求，再重新连接到同一段对话。开始前先完成
[安装](../tutorials/installation.zh-CN.md)。本教程使用 `builtin/codex`，也可以换成本机已安装
Agent 对应的内置 Profile。

## 1. 启动一个有名字的会话

```sh
amux session run builtin/codex --name hello-amux
```

命令打印 `Session started` 后返回。Agent 在 Runtime 上运行，关闭当前终端不会停止它。
如果名字已经存在，直接连接原会话或换个名字，不要重复创建。

## 2. 发送请求并查看回答

```sh
amux session send hello-amux "请用一句话介绍你可以帮助我做什么。"
amux session attach hello-amux
```

时间线中应出现已接收的输入和 Agent 的回答。`send` 只提交工作，不等待回答；
这一步会使用原生 Agent 的模型账号。如果出现权限请求，在终端 UI 中处理。
认证或启动失败时参考[排障指南](../operations/troubleshooting.zh-CN.md)。

## 3. 离开并再次连接

在连接的 UI 中输入 `/detach` 或按 Ctrl-]，然后执行：

```sh
amux session list
amux session attach hello-amux
```

离开 UI 只关闭查看界面，对话仍然保留。下一条消息可以在 UI 中输入，也可以通过
`session send` 提交。

## 4. 停止进程，保留对话

等回答结束后，离开 UI 并执行：

```sh
amux session sleep hello-amux
amux session list
```

Session 应显示为 sleeping。需要继续时执行 `amux session resume hello-amux`，然后
再次 attach。恢复使用该 Agent 支持的历史恢复机制，与创建新对话不同。

下一步：[选择 Session 与 Task](../concepts/execution.zh-CN.md)。
