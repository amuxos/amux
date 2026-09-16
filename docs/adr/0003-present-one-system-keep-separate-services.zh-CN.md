<a id="present-one-system-while-keeping-services-separate"></a>

# 呈现一个系统，同时保留独立服务

[English](0003-present-one-system-keep-separate-services.md) | [简体中文](0003-present-one-system-keep-separate-services.zh-CN.md)

> 从原仓库主干保留的设计记录。文中的日期、阶段、源码行号和实现判断属于记录当时；当前行为以 [CLI 产品模型](../current/cli-product-model.zh-CN.md) 及相关领域产品模型为准。
> 源码路径相对于公共仓库根目录；原 `cmd/amux`、`cmd/amuxd`、`cmd/amuxlet` 的实现分别迁至 `app/cli`、`app/hub`、`app/runtime`，`cmd/*` 仅保留发行入口。

Owner 与一个 amux system 交互，但 amuxd 和 amuxlet 仍是独立的原生服务单元。
单机部署同时安装两者，聚合其状态与生命周期操作。同一系统可以托管一个专属
Bot Profile，也可以托管多个独立配置的 Bot Profile。分布式部署则可将 Hub 和
Runtime 放在不同主机上，不改变面向用户的系统模型。
这既避免向用户暴露拓扑，又保留分布式 Runtime 的边界。
