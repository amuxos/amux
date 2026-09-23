# 安装环境的重启与更新

[English](maintenance.md) | [简体中文](maintenance.zh-CN.md)

普通用户在飞书里工作，运维者负责主机、原生 Agent 登录和服务生命周期。
先阅读[安装](../tutorials/installation.zh-CN.md)与
[机器人接入](../tutorials/first-feishu-bot.zh-CN.md)。

## 变更前检查健康状态

负责人在私聊 `/amux` 管理入口查看系统健康摘要，使用重启和检查更新操作。
群和话题控制不授予系统维护权限。也可在主机运行：

```sh
amux system status
amux system doctor
amux system restart --plan
```

重启可能中断当前工作。先检查计划、完成工作，再执行 `amux system restart`。
显式 `--force` 会绕过活动工作阻止条件，不是日常必须添加的选项。

## 更新二进制

`amux update --restart`

默认公共发行清单会校验下载包；自定义归档直链不提供同样的校验，详见
[公共发行](public-releases.zh-CN.md)。更新保留安装状态，不替换原生 Agent 的登录，
也不自动升级原生 Agent。

## 服务托管与数据

支持的主机在 macOS 使用 launchd、在 Linux 使用 systemd。不支持的主机使用
显式前台 `amuxd` 和 `amuxlet` 进程，通过各二进制的 `--help` 查看启动选项，
不会自动选择另一个 supervisor。`~/.amux` 保存运行状态、配置和凭据。
破坏性维护前备份相关状态，源码 checkout 放在该目录之外。安装器 `--clean` 会删除
amux 数据，并要求重新接入机器人。

远端 worker 在各自主机执行，连接和目标选择见
[远端 Runtime](../guides/remote-runtime.zh-CN.md)，按现象定位见
[排障指南](troubleshooting.zh-CN.md)。
