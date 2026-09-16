<a id="cli-product-model"></a>

# CLI 产品模型

[English](cli-product-model.md) | [简体中文](cli-product-model.zh-CN.md)

主要命令组为 `setup`、`session`、`task`、`agent`、`schedule` 和 `system`。
Session、Task 和 Schedule 使用 Hub 控制面。每个 Runtime 在本机执行工作，并通过
出站 tunnel 连接 Hub。本地使用需要 Hub，但不要求飞书 Bridge。

Target 标识 Runtime 的位置。Session 是与一个智能体持续进行的对话；Task 是有结果的
有界执行。Profile 引用选择 Agent 和启动策略。支持的内置 Profile 为 `builtin/codex`、
`builtin/claude` 和 `builtin/pi`，默认是 `builtin/codex`。
显式指定 Runtime 时，即使模型名不在 Runtime 建议列表中，也原样传给原生 Agent。
没有 Runtime 覆盖时，Profile 显式声明的 `supported_models` 仍是模型允许列表。

所有 Session 使用相同的行式终端 UI。提交的文本进入 Session 队列。
Detach 只结束 UI 连接，不结束 Session。原始字节输入和终端尺寸调整不是 transport
操作。重启、恢复和模型切换遵循所选 Agent 的能力。
兼容 Codex 的 Provider 原生历史缺失时，重启、恢复和自动唤醒使用可用的 amux 对话
记录恢复，同时保留 Session 身份、工作区与执行设置。

`amux setup local` 配置本地 Hub 和 Runtime，在支持的宿主上使用原生服务管理。
`amux install --source <directory>` 安装本地二进制。
`amux update --source <directory> --restart` 更新已有安装，无需重复 setup。
默认不配置发行包或 authoring runtime 的下载源；远端来源必须由 operator 显式提供。
可选 authoring 能力不可用时，核心安装仍可继续。

配置发现与生成文档描述可用的配置键。通用配置命令在写入前拒绝未知键。
Hub 不可用时，Agent Pack、凭据操作和本机系统恢复仍可工作。
隔离和凭据访问遵循 Task 模型。凭据命令示例使用公共 Provider 平台；
发行版可在启动注册时追加自身 Provider 集成的示例。
Persistent Instance 的中断同样遵循 Task 模型：它取消当前 owner TaskRun，保留实例的
恢复能力，不依赖 Provider 的 interrupt 确认来判断该次执行的最终状态。

Task 事件 JSON 流通过 `kind=usage` 暴露数值型 `usage` 对象，包括 child 最新的规范化
快照，以及可选的 Provider thread `cumulative_*` 累计值。`task logs --json` 保留该对象；
纯文本日志不逐条打印用量更新。累计范围由 Task 模型定义。

生成的 CLI 参考是完整的命令与选项契约，包含平台限制。修改命令元数据后需重新生成。

应用实现位于可导入的 `app/cli`、`app/hub` 和 `app/runtime` 包。
`cmd` 入口选择公共默认值。独立发行版 module 可以注册扩展后调用相同应用入口；
它依赖本 module，并拥有自己的配置、发行源与附加命令行为。
