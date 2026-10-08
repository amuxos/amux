<a id="cli-product-model"></a>

# CLI 产品模型

[English](cli-product-model.md) | [简体中文](cli-product-model.zh-CN.md)

主要命令组为 `setup`、`session`、`task`、`agent`、`schedule`、`memory`、`isolation` 和 `system`。
Session、Task 和 Schedule 使用 Hub 控制面。每个 Runtime 在本机执行工作，并通过
出站 tunnel 连接 Hub。本地使用需要 Hub，但不要求飞书 Bridge。
`local` Target 别名标识配置的本地 Runtime，也适用于 `session list` 过滤，
不要求 Runtime 本身名为 `local`。
`isolation` 命令组管理 Principal 账号、别名、委派授权和隔离 helper 服务；
隔离与凭据访问规则遵循 Task 模型。

`bridge feishu group-settings` 管理本机安装的群默认设置。命令要求开启默认关闭的
`bridge.feishu.group_settings.enabled` 实验；在私聊 `/amux` 的实验功能页开启并重启后
再配置群。关闭实验保留群配置和关联，但独立配置群暂停新任务，重新开启后恢复。
`show <chat-id>` 导出完整 JSON 快照，版本 0 表示尚未保存群配置。
`apply <chat-id> --file <json|->` 校验并保存完整快照，以版本作比较并交换令牌。
`new-conversation <chat-id> --expected-version <n>` 退役群连续会话的默认指针，
已接受的任务继续完成。命令在 Hub 主机操作本地 Bridge 数据库，不跟随远端 Hub 地址。
`--profile` 选择 Bot 作用域，必须匹配 JSON 中的范围；Session 启动时再次校验执行可用性。

Target 标识 Runtime 的位置。Session 是与一个智能体持续进行的对话；Task 是有结果的
有界执行。Profile 引用选择 Agent 和启动策略。支持的内置 Profile 为 `builtin/codex`、
`builtin/claude` 和 `builtin/pi`，默认是 `builtin/codex`。
内置 Profile 使用 v2，只保存内容。amux 调用方为命名快捷入口提供 Runtime、模型和权限默认值；显式执行选择优先，解析后的执行设置冻结到启动快照。外部 v2 Profile 仍需调用方提供 Runtime。
显式指定 Runtime 时，即使模型名不在 Runtime 建议列表中，也原样传给原生 Agent。
没有 Runtime 覆盖时，Profile 显式声明的 `supported_models` 仍是模型允许列表。

`session set-model` 和 MCP `amux.session_set_model` 保留 Session 启动快照中显式
选择的 Runtime，按该 Runtime 的模型策略解析目标模型，同时保留冻结的 Profile 身份
与思考强度，不重新套用源 Profile 的模型允许列表。目标模型不支持已选思考强度时，
在修改运行中 Session 前报错。没有保存 Runtime 覆盖时，模型切换继续遵循 Profile
的模型策略。

所有 Session 使用相同的行式终端 UI。提交的文本进入 Session 队列；
`session queue` 查看待处理输入，`session queue cancel` 和 `session queue clear`
分别移除单条和全部条目。Detach 只结束 UI 连接，不结束 Session。原始字节输入和终端尺寸调整不是 transport
操作。重启、恢复和模型切换遵循所选 Agent 的能力。
兼容 Codex 的 Provider 原生历史缺失时，重启、恢复和自动唤醒使用可用的 amux 对话
记录恢复，同时保留 Session 身份、工作区与执行设置。

`amux setup` 接受 `local`、`hub`、`remote`、`feishu` 和 `feishu-local` 目标；
`amux setup local` 配置本地 Hub 和 Runtime，在支持的宿主上使用原生服务管理。
`amux install --source <directory>` 安装本地二进制。
`amux update --source <directory> --restart` 更新已有安装，无需重复 setup。
公共版本默认从 `amuxos/amux-releases` 的平台清单更新。`AMUX_RELEASE_URL` 可覆盖
来源；发行版仍可使用 `AMUX_SCM_LATEST_URL` 和已有归档格式。清单下载必须在解包前
通过 SHA256 校验。默认不配置 authoring runtime 下载源；可选 authoring 能力不可用
时，核心安装仍可继续。
发行版可通过 CLI 的 `SetupCommands` hook 在 `amux setup` 下注册可选组件的
显式安装子命令。构建命令树或执行核心安装、升级均不会运行这些安装器。

`scripts/install.sh` 为公共源码构建和本地发行二进制提供统一安装入口。独立脚本
还可下载公共最新版本，或通过 `--version vX.Y.Z` 选择版本。源码 checkout 中仍默认
从源码构建。下载时先解析一次版本，再下载该版本的平台包及校验文件。发行包包含
匹配的 Claude SDK sidecar；Claude 仍需 Node.js 22 或更新版本。脚本检测
已安装的 `codex`、`claude` 和 `pi`，检测到多个时提示选择，并接受 `--runtime`
（`--provider` 为别名）以支持非交互选择。所选 Runtime 对应 `builtin/codex`、
`builtin/claude` 或 `builtin/pi` Profile；缺少 Runtime 或 Profile 无效时在安装前
失败。原生 Agent 自行管理登录。Claude 源码安装还会准备并安装其 SDK sidecar。
脚本配置本地系统和 shell PATH。可选 `--feishu` 先完成 Bot 配置，再检查 Bot
Profile 或绑定 Session。`--interaction-mode singleton` 为安装器默认值，创建并
绑定命名 Session；`thread` 为新对话保存所选 Profile、Runtime 和 Model。
安装不会发送 smoke 提示词或聊天消息。重复运行复用兼容的 singleton 绑定，
拒绝冲突绑定，不替换用户的 Session。

替换已有安装前，脚本将经过验证的二进制及 Claude sidecar 暂存到 amux 数据目录
之外，检查所选 Agent，并停止 Pack autosync 和本地系统。停止失败会中止替换。
普通重复安装保留配置、凭据、Session 和 Pack 数据，启动新二进制，并仅在 autosync
原先运行时恢复它。`--clean` 显式执行清空数据的卸载，再重新配置本地系统；它会
删除 amux 配置、凭据、日志、Session 和托管 Pack 数据，需要重新配置飞书。
原生 Agent 的安装及自身登录信息不受影响。暂存允许安装源二进制或 sidecar 位于
被清理的目录内。脚本拒绝危险的清理根目录，以及位于清理根目录内的 singleton
工作区。源码 checkout 同样必须位于清理根目录之外。卸载停止 Pack autosync、
本地 Runtime/Hub 及已登记的独立 MCP/GUI 服务；停止失败时，在删除二进制、PATH
条目或数据前返回错误。MCP/GUI 进程由其外部宿主负责重新启动。

配置发现与生成文档描述可用的配置键。通用配置命令在写入前拒绝未知键。
Hub 不可用时，Agent Pack、凭据操作和本机系统恢复仍可工作。
隔离和凭据访问遵循 Task 模型。凭据命令示例使用公共 Provider 平台；
发行版可在启动注册时追加自身 Provider 集成的示例。
Persistent Instance 的中断同样遵循 Task 模型：它取消当前 owner TaskRun，保留实例的
恢复能力，不依赖 Provider 的 interrupt 确认来判断该次执行的最终状态。

Task 事件 JSON 流通过 `kind=usage` 暴露数值型 `usage` 对象，包括 child 最新的规范化
快照，以及可选的 Provider thread `cumulative_*` 累计值。`task logs --json` 保留该对象；
纯文本日志不逐条打印用量更新。累计范围由 Task 模型定义。

`amux memory search|show|upsert|forget|export` 通过 Hub 管理 Markdown 记忆，不依赖
Agent Profile 或 Runtime。search 和 show 返回有界 JSON，包含版本和续读信息。
upsert 从 `--file` 或标准输入读取 UTF-8 正文；upsert 和 forget 必须指定
`--expected-revision`（之前读到的 hash，新建用 `absent`）。export 将单份完整文档写到
标准输出，分页期间版本变化则报错。export 仅输出纯文本；search、show、upsert 和
forget 接受 `--json`。命令和 `amux.memory_search|get|upsert|forget`
MCP 工具不接受作用域或 Principal 覆盖参数：Hub 认证调用方并选择作用域。
受管进程将冻结的 `AMUX_MEMORY_BINDING` 作为前置条件传入；缺少绑定的旧受管进程
必须新建对话。关闭记忆后，运行时 MCP 发现不展示记忆工具，直接调用也会拒绝；
关闭不会删除文件。详见[记忆模型](agent-memory-product-model.zh-CN.md)。

生成的 CLI 参考是完整的命令与选项契约，包含平台限制。修改命令元数据后需重新生成。

应用实现位于可导入的 `app/cli`、`app/hub` 和 `app/runtime` 包。
`cmd` 入口选择公共默认值。独立发行版 module 可以注册扩展后调用相同应用入口；
它依赖本 module，并拥有自己的配置、发行源与附加命令行为。

`agent list` 和 Hub 的专家发现共用 registry 中的开发库判断：开发来源元数据、
临时/评估标记或旧的 `-dev` 后缀。`--all` 包含这些专家，`--dev` 仅显示这些专家。
单个 Pack 加载失败不影响其他正常专家；CLI JSON 与 Hub 响应通过 `profiles_error`
返回诊断，CLI 人类可读输出在 stderr 提示。`agent local create --description`
将用途摘要保存到 Pack。

## 离线专家渲染

`amux agent render` 通过 `--runtime` 和 `--model` 独立于专家引用选择执行配置。
外部 v2 专家必须显式指定 Runtime，否则在写入任何输出前报错；内置快捷入口可使用 amux 的调用方默认值。渲染只为所选引擎投影内容，
不会启动引擎；执行权限仍由调用方通过原生启动参数提供。
