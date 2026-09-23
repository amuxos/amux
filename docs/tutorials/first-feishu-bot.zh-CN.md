# 接入第一个飞书机器人

[English](first-feishu-bot.md) | [简体中文](first-feishu-bot.zh-CN.md)

完成本教程后，发给机器人的消息会交给本机 Agent 执行，回答再回到飞书。
如果运维者已经提供可用机器人，直接阅读[在飞书中协作](../guides/feishu-conversations.zh-CN.md)。
接入者需要已安装并登录的原生 Agent（Codex、Claude 或 Pi；Claude 还需要 Node.js 22+），
以及可交互终端和能够完成
官方应用注册流程的飞书账号；组织策略可能要求管理员批准。

## 1. 配置机器人和对话方式

第一个机器人先使用持续复用同一对话的 singleton 模式，下载公共安装脚本：

```sh
curl -fsSL https://github.com/amuxos/amux-releases/releases/latest/download/install.sh -o /tmp/amux-install.sh
bash /tmp/amux-install.sh --runtime codex --feishu --interaction-mode singleton   --feishu-profile assistant --session-name feishu-assistant
```

选择本机已安装的 Agent。该命令也会安装或更新 amux，保留已有数据，但可能中断当前工作，
请先完成正在执行的任务。按照终端中的官方二维码注册步骤操作，并完成流程要求的应用
权限或发布步骤。配置没有完成时，不要跳过继续。

`assistant` 是 **Bot Profile** 的名字，`builtin/codex` 是 **Agent Profile**。
安装器保存机器人凭据，创建或复用兼容的命名 Session，并完成绑定，不会发送测试消息。

## 2. 检查系统并打开机器人管理

```sh
amux system status
amux session list
```

确认系统健康且存在 `feishu-assistant`。在飞书中打开机器人的私聊，发送 `/amux`
查看管理入口，确认所选 Agent 和对话方式。负责人操作只对已配置的负责人开放，
不要在群聊中寻找仅限负责人私聊使用的系统管理操作。

## 3. 发送一个小请求

在私聊中发送“请用一句话介绍你可以帮助我做什么”。应先看到进度，再看到最终回答。
没有卡片时检查机器人配置与连接；有卡片但执行失败时检查 Runtime 上的 Agent。
具体见[排障指南](../operations/troubleshooting.zh-CN.md)。

## 4. 决定后续群协作方式

singleton 继续同一个绑定会话；thread 模式为话题创建独立对话。新机器人如果主要用于
话题协作，在安装器中选择 `--interaction-mode thread`。改变对话方式不会自动把已有
对话变成不同用户之间隔离的工作区。

邀请同事前先阅读[群设置与访问控制](../guides/feishu-groups.zh-CN.md)。谁能使用机器人、
消息进入哪段对话，以及 Linux 用户隔离，是三个独立选择。精确的路由和权限规则见
[Bridge 模型](../current/feishu-bridge-product-model.zh-CN.md)。
