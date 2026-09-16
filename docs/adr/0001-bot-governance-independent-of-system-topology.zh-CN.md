<a id="make-bot-governance-explicit-and-independent-of-system-topology"></a>

# 显式配置 Bot 治理，并与系统拓扑独立

[English](0001-bot-governance-independent-of-system-topology.md) | [简体中文](0001-bot-governance-independent-of-system-topology.zh-CN.md)

> 从原仓库主干保留的设计记录。文中的日期、阶段、源码行号和实现判断属于记录当时；当前行为以 [CLI 产品模型](../current/cli-product-model.zh-CN.md) 及相关领域产品模型为准。
> 源码路径相对于公共仓库根目录；原 `cmd/amux`、`cmd/amuxd`、`cmd/amuxlet` 的实现分别迁至 `app/cli`、`app/hub`、`app/runtime`，`cmd/*` 仅保留发行入口。

每个飞书 Bot Profile 显式选择 `personal` 或 `team` 治理模式。
Personal 将该 Bot 的能力限制给 owner；Team 允许授权成员使用它暴露的能力。
飞书活动不会隐式改变治理模式。将活动的 singleton Bot 从 Personal 改为 Team
需要确认，因为既有上下文将变为共享。

治理模式不是安装级部署模式。一个 amux system 可以托管一个专属 Bot Profile，
也可以托管多个独立治理的 Bot Profile；需要更强的进程、数据或故障域隔离时，
operator 也可以部署独立系统。无论采用哪种拓扑，系统生命周期权限都属于
system owner/operator。
