<a id="use-native-service-supervision"></a>

# 使用原生服务管理

[English](0002-use-native-service-supervision.md) | [简体中文](0002-use-native-service-supervision.zh-CN.md)

> 从原仓库主干保留的设计记录。文中的日期、阶段、源码行号和实现判断属于记录当时；当前行为以 [CLI 产品模型](../current/cli-product-model.zh-CN.md) 及相关领域产品模型为准。
> 源码路径相对于公共仓库根目录；原 `cmd/amux`、`cmd/amuxd`、`cmd/amuxlet` 的实现分别迁至 `app/cli`、`app/hub`、`app/runtime`，`cmd/*` 仅保留发行入口。

新 amux 安装在 macOS 使用 launchd，在 Linux 使用 systemd，维持 amux system
运行并执行生命周期操作。不支持原生服务管理器的宿主使用显式前台模式。
现有 PID 管理的 daemon 路径仅保留用于迁移兼容，不作为自动回退。
这样可避免多个 supervisor 相互竞争，并使重启和恢复状态保持权威。
