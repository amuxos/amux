<a id="cli-product-model"></a>

# CLI 产品模型

[English](cli-product-model.md) | [简体中文](cli-product-model.zh-CN.md)

主要命令组为 `setup`、`session`、`task`、`agent`、`schedule`、`memory`、`isolation`、`console` 和 `system`。

负责人私聊 `/amux` → Web 管理后台页负责开启、关闭系统级临时后台。缺省关闭，
开启后监听 `0.0.0.0:<console.port>`（默认 7892），30 分钟后自动关闭；重复开启不续期。
到期、手动关闭或 Hub 退出都会关闭监听并使所有浏览器会话失效，所有助手共用同一个管理器。
仅向已确认负责人的私聊卡显示服务器访问地址、剩余时间和每次新生成的 32 字符临时密码。
密码仅通过 stdin 传入子进程，不进入命令参数、URL、环境、日志或应用持久化；重新开启轮换密码。
`console.public_url` 可覆盖对外访问 origin，否则使用 Hub 主机 IP 和系统端口。
非回环 HTTP 可用于可信网络，但不加密；公网部署应使用 HTTPS。

`amux console` 保留为运维诊断命令，默认本机监听和 30 分钟寿命；非回环监听必须同时提供
`--password-stdin` 和非回环 `--public-url`。`--duration` 可以缩短但不能超过 30 分钟。
本机未提供密码时兼容五分钟有效的一次性 fragment 配对。临时密码换取 HttpOnly、
SameSite=Strict Cookie，会话有效期不会晚于后台关闭时间。退出登录只撤销当前浏览器会话，
不关闭后台。业务读取和事件流均需认证，写操作还要求精确 Host/Origin 和会话 CSRF token。
浏览器不将秘密写入 localStorage。Agent 和 enrollment actor 凭据不能启动此运维入口。
托管子进程先核对直接父进程、已记录的 Hub PID 及同安装目录的 Hub 可执行文件，
再建立回环运维连接。宿主 HOME 遗留的 enrollment 文件不改变该托管子进程的身份；
显式 Agent/Session 身份仍拒绝启动，单独传入私有托管 flag 不授予权限。
这不表示已有多用户浏览器 RBAC。
托管后台只列出宿主凭据名称及字段名称，不冒用 enrollment 所属 Principal，也不返回凭据值或文件内容。

控制台包含总览、Session/Timeline、TaskRun/持久实例、Schedule、助手/群设置、专家内容/Pack、
凭据元数据以及显式白名单的本地设置。不提供运行节点管理页、宿主 Shell 或任意命令接口。
后端能力列表决定可用操作；助手和群修改复用 Hub 内正在运行的 Bridge 状态切换和版本检查，
包括确认和恢复选择。浏览器不能把模型或权限字段写入仅包含内容的 Profile。
已有 Session 保留执行快照。暂停 Schedule 不取消已接纳工作；接纳与最终执行结果分开显示。
现有桌面 Gateway 和只读 viewer 保留独立的认证与分享契约。
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
有界执行。Profile 引用选择专家内容。支持的内置 Profile 为 `builtin/codex`、
`builtin/claude` 和 `builtin/pi`，默认是 `builtin/codex`。
内置 Profile 使用 v2，只保存内容。amux 调用方为命名快捷入口提供 Runtime、模型和权限默认值；显式执行选择优先，解析后的执行设置冻结到启动快照。外部 v2 Profile 仍需调用方提供 Runtime。
新建 Session 省略权限参数时默认使用原生 bypass，覆盖外部专家和直接引擎启动；显式权限参数优先。
显式指定 Runtime 时，即使模型名不在 Runtime 建议列表中，也原样传给原生 Agent。
Pack 不声明模型允许列表。

`session set-model` 和 MCP `amux.session_set_model` 保留 Session 启动快照中显式
选择的 Runtime，按该 Runtime 的模型策略解析目标模型，同时保留冻结的 Profile 身份
与思考强度，不重新套用源 Profile 的模型允许列表。目标模型不支持已选思考强度时，
在修改运行中 Session 前报错。

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
Linux 更新会同时从同一发行包刷新已安装的隔离 helper 和 materializer，通过 root
或非交互 sudo 执行，保留原 systemd unit 与隔离配置；仍有活跃隔离 launch 时拒绝替换。
空闲 helper 会持有与更新器生命周期绑定的启动保护，避免新会话与重启发生竞态。
不支持该能力的旧 helper 需要在维护窗口先停止后进行一次性迁移；已停止的服务保持停止。
helper 更新失败会在普通服务重启前返回更新失败，消除原因后重试 `amux update` 即可。
未安装 helper 的机器不会因此安装或开启隔离。Linux 发行包必须包含配套 helper。
旧安装器交接给新 CLI 后，安装收尾可从尚未清理的原发行包补齐被跳过的 helper，
但必须先核验包内 CLI 和 Runtime 与已安装文件一致。

Linux 上已安装 helper 时，升级先通过本地 Runtime 协调，再替换二进制。维护期间拒绝
新启动、新输入和新 TaskRun，继续允许权限答复和中断；已接收的队列与 TaskRun 自然
完成，不强制取消。默认等待五分钟（`--drain-timeout`），超时会在替换前终止升级并
恢复接收工作。清空已接收工作后，将运行中的 Session 休眠并持久记录，升级 helper、
完成指定服务重启后，只恢复这批 Session；原本已休眠的会话不唤醒。可续期租约和持久
恢复名单跨 CLI 交接及 Runtime 重启保留；升级进程退出后自动恢复。恢复失败会报告
并重试，不静默丢弃名单。维护期间暂停原生目标的后续轮次准入，保留授权，结束后恢复。
维护协议仅供本地管理，不通过 Hub 或隧道暴露。尚不支持协议的旧 Runtime 仍依赖
helper 的安全保护；有活动进程时需要一次维护升级。helper 的最终保护仍是权威检查，
包括对孤儿进程的保护。

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

本地专家创建和编辑只产出内容型 v2 Pack。创建入口不接受 `--type`、`--model`、
`--permission`；运行设置在启动时选择。旧版 Pack 会报版本不支持，不改写用户文件。

隐藏的运维命令 `agent local convert-v2 --expected-revision <完整 revision>`
显式将托管的 v1.1 Local Pack 转换为 v2。它移除执行字段和 Provider 源文件，
重新编译并校验内容，再通过专家库锁和 revision CAS 发布新提交。
旧提交和已物化的历史快照保持不变。未发布的空专家库只转换源码框架，
不会注册空 Pack；已为 v2 的源码保持同一 revision。安装和常规编辑不调用转换。
运维人员需在启动时独立提供 Runtime、模型和权限等执行设置。

## 每日清理

系统级 `maintenance.daily_cleanup.enabled` 默认开启。Hub 即时读取开关，支持普通话题与用户隔离话题；
按 Hub 本地时区每天 06:17 对每个 Runtime 执行一次。调度不依赖 cron，也不启动 Agent。
结果持久化以避免重启后重复清理；通知失败只重试投递，不重复删除。
`amux system cleanup` 预览相同清理逻辑；`--apply` 执行并通知已配置的 Bot 管理员。
该系统操作只允许宿主管理者调用，不向 Agent 凭据或普通已登记用户开放。

清理复用已归档 Session 和保留七天的终态 Task 生命周期接口；保留仍被引用的工作区、
Provider 线程、近期孤立数据、显式保护路径、凭据，以及运行或休眠中的会话。
不清理全局 `/tmp`；权威状态读取失败时停止相关删除，不通过符号链接越出受管目录。
Provider 特定布局由发行版注册；principal 文件通过现有特权 helper 处理，不授予 Runtime
宽泛 sudo。日志有大小上限，SQLite 空闲页超过 300 MiB 时才压缩。

## 配置 Codex 启动程序

`adapter.codex.executable` 指定兼容 Codex 的启动程序（PATH 中的名称、绝对路径或 `~/` 路径）；留空使用 `codex`。该值是程序路径，不是 shell 命令。Hub 将配置传递给新 Codex Session；模型发现也在选中的 Runtime 上使用同一个程序。Runtime 按执行环境 HOME 展开 `~/`。Session 保存启动程序，重启、恢复和切模型均沿用；修改配置只影响新 Session。缺少该字段的已有 Session 继续使用 `codex`。所有者私聊 `/amux` 的实验功能表单可修改此实例级配置，保存并重启后生效。路径必须在选中的每个 Runtime 上存在，不会修改远端配置文件。

注册的 app-server 集成复用执行程序设置、所有者实验功能表单、执行节点路径解析和 Session 启动路径快照。原生模型发现使用同一个程序，不回退静态原生目录；Provider 和模型服务条目保持独立。

运维者可执行 `amux bridge feishu onboarding --profile <name>` 向已保存的负责人
发送引导卡。加 `--capabilities` 改为发送能力广场，配合 `--enable-daily` 从次日开启
每日能力推荐。不传 `--enable-daily` 时发送广场不改变推荐偏好。
两种方式均不会启动第二个事件消费者、重置配置或重发任务。

## 通过终端访问 MCP 工具

`amux mcp call <tool>` 从标准输入（或 `--input-file`）接收 JSON 对象，
输出 JSON 格式的 MCP 工具结果。当引擎未向模型展示已注册的 amux 工具时，
此命令提供终端调用通道。它使用与 `amux mcp` 相同的启用工具注册表、SDK
Schema 校验、调用者凭据和 Hub 授权，不绕过权限，也不改用本地专家注册表。
同次调用自动拼接传输结果分块；`profile_list` 等业务分页仍需沿 `next_cursor` 查询。
工具错误保留 `isError` 并返回非零退出码。未知工具、禁用 MCP 和无效输入均在执行前失败。

MCP `session_stream` 在未指定目标机器时，从调用者可见的会话列表解析目标。
同名会话存在歧义时要求指定目标，不默认选择第一台匹配的机器。

MCP 定时计划创建、详情和列表保留 Unix 数字时间戳，非零时同时返回 UTC RFC3339
格式的 `next_run_at_iso` / `last_run_at_iso`。助手可直接引用，避免猜时区或心算时间戳。
