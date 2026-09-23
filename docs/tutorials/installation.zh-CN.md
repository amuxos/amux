# 安装 amux

[English](installation.md) | [简体中文](installation.zh-CN.md)

完成本教程后，本机的 Hub 和 Runtime 就可以运行 Agent。飞书是可选入口。
公共发行包支持 macOS、Linux 的 arm64 和 amd64 架构。

## 开始前准备

先安装并登录一个原生 Agent：Codex、Claude Code 或 Pi，在它自己的终端中确认能正常
使用。amux 使用该 Agent 的登录状态，不提供模型额度，也不会替你安装 Agent。
Claude 还需要 Node.js 22+。

在 `~/.amux` 之外的普通工作目录操作。安装预编译发行包不需要 Go。
托管服务在 macOS 使用 launchd，在 Linux 使用 systemd；没有这些服务管理器的主机见
[日常运维](../operations/maintenance.zh-CN.md)。

## 安装发行包

下载安装脚本，再选择本机已经安装的 Agent：

```sh
curl -fsSL https://github.com/amuxos/amux-releases/releases/latest/download/install.sh -o /tmp/amux-install.sh
bash /tmp/amux-install.sh --runtime codex
```

按需将 `codex` 换为 `claude` 或 `pi`。要固定版本，添加 `--version vX.Y.Z`，并替换为
真实的发行版本。脚本会配置本机系统并打印 PATH 设置；打开新终端或执行打印的 PATH 设置。

## 确认已经就绪

```sh
amux system status
amux system target list
amux agent doctor builtin/codex
```

系统状态应显示服务健康，目标列表中应出现本机 Runtime。如果使用其他 Agent，将诊断
命令中的 Profile 换为 `builtin/claude` 或 `builtin/pi`。静态诊断不发送模型提示词；
下一篇教程会用一次真实对话验证完整链路。

找不到 `amux` 时先检查 PATH；连接不到 Hub 时先按
[排障指南](../operations/troubleshooting.zh-CN.md)定位，避免反复执行安装。

## 另一种方式：从源码构建

源码构建需要 Go 1.25+，构建 Claude sidecar 还需要 Node/npm：

```sh
git clone https://github.com/amuxos/amux.git
cd amux
bash scripts/install.sh --runtime codex
```

在源码 checkout 内，脚本默认走源码构建。本地二进制、sidecar 和高级安装参数见
[仓库安装说明](../../README.zh-CN.md)。

普通重装保留 amux 数据，但会中断正在执行的工作。`--clean` 会删除 amux 的配置、
凭据、Session 和托管 Pack，不应作为一般启动问题的修复步骤。

下一步：[完成第一次会话](../guides/cli-sessions.zh-CN.md)，或随后
[接入第一个飞书机器人](first-feishu-bot.zh-CN.md)。
