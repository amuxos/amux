package config

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

//go:generate go run ../../cmd/amux-config-doc --language en --output ../../docs/reference/configuration.md
//go:generate go run ../../cmd/amux-config-doc --language zh-CN --output ../../docs/reference/configuration.zh-CN.md

// ConfigOption is the documentation and discovery contract for one config.toml key.
// Keep runtime config reads and this catalog in sync; config_reference_test.go
// rejects undocumented keys and scripts/check.sh rejects generated-doc drift.
type ConfigOption struct {
	Section     string
	Key         string
	Type        string
	Default     string
	Constraints string
	Description string
	Managed     bool
	Sensitive   bool
}

// ConfigOptions is the single source for config discovery and generated docs.
// <profile>, <provider>, and <label> denote dynamic path segments.
func ConfigOptions() []ConfigOption {
	return append([]ConfigOption(nil), configOptions...)
}

// ManagedConfigKeys returns keys accepted by `amux system config get/set/unset`.
func ManagedConfigKeys() []string {
	out := make([]string, 0, len(configOptions))
	for _, option := range configOptions {
		if option.Managed {
			out = append(out, option.Key)
		}
	}
	return out
}

var configOptions = distributionConfigOptions([]ConfigOption{
	{Section: "Runtime 清理", Key: "maintenance.daily_cleanup.enabled", Type: "boolean", Default: "true", Description: "每日自动清理普通话题和用户隔离话题的运行数据，并向 Bot 管理员私聊报告结果；系统级开关，保存后即时生效。", Managed: true},
	{Section: "Runtime 清理", Key: "maintenance.daily_cleanup.at", Type: "string", Default: "06:17", Constraints: "HH:MM", Description: "每日清理时间，使用 Hub 本地时区；错过时间后在恢复运行时补一次。", Managed: true},
	{Section: "Runtime 清理", Key: "maintenance.daily_cleanup.protected_paths", Type: "string array", Default: "[]", Constraints: "absolute paths", Description: "每日清理始终保留的共享或长期工作区绝对路径，作用于所有 Runtime。", Managed: true},
	{Section: "飞书实验功能", Key: "bridge.feishu.group_settings.enabled", Type: "boolean", Default: "false", Description: "群独立配置实验总开关，影响本实例全部助手；关闭时保留群设置和会话，已配置独立策略的群暂停新任务，未配置群和私聊沿用助手设置。修改后重启生效。", Managed: true},
	{Section: "飞书实验功能", Key: "bridge.feishu.presentation.completion_notification_enabled", Type: "boolean", Default: "false", Description: "长会话完成通知实验总开关，影响本实例全部助手；关闭时不新增成功、等待补充或失败提醒。修改后重启生效。", Managed: true},
	{Section: "飞书实验功能", Key: "bridge.feishu.experimental.steer_foreground", Type: "boolean", Default: "false", Description: "前台协调/忙时注入实验总开关：开启后，任务执行中同一发送者的追加消息注入当前回合（不再中断重来），单例和话题会话注入前台/后台协调说明（首轮及每 20 轮）；关闭时忙时消息沿用中断并重新提交。环境变量 AMUX_FEISHU_BUSY_INPUT=interrupt 可强制关闭注入。修改后重启生效。", Managed: true},
	{Section: "飞书实验功能", Key: "bridge.feishu.experimental.show_background_tasks", Type: "boolean", Default: "false", Description: "后台任务进度卡片实验开关：开启后，飞书会话发起的每个后台任务（子任务 / 后台 Agent 实例）单独发送实时进度卡片；默认关闭，后台过程不发卡片，由前台 Agent 汇报结果。可在飞书实验功能卡片中切换，保存后立即生效。", Managed: true},
	{Section: "飞书实验功能", Key: "memory.enabled", Type: "boolean", Default: "false", Description: "Agent 记忆实验总开关，由 Hub 统一控制所有 Runtime，无需 Agent Profile；非隔离时全局共享，用户隔离时按有效 Principal 保存。关闭阻止读写并保留文件，已有会话保留原生记忆抑制。修改后重启生效。", Managed: true},
	{Section: "Hub 与调度", Key: "hub.addr", Type: "string", Default: "127.0.0.1:7100", Constraints: "host:port", Description: "amuxd hub 地址。", Managed: true},
	{Section: "Hub 与调度", Key: "hub.token", Type: "string", Default: "空", Description: "Hub 与 runtime 共享鉴权令牌。", Managed: true, Sensitive: true},
	{Section: "Hub 与调度", Key: "hub.allowed_targets", Type: "string array", Default: "[]", Description: "允许注册到 hub 的 target ID 白名单；空表示不限制。", Managed: true},

	{Section: "Runtime", Key: "runtime.id", Type: "string", Default: "主机名", Description: "Runtime 稳定标识。", Managed: true},
	{Section: "Runtime", Key: "runtime.name", Type: "string", Default: "runtime.id", Description: "Runtime 展示名称。", Managed: true},
	{Section: "Runtime", Key: "runtime.auto_start", Type: "boolean", Default: "false", Description: "安装或系统启动时是否自动启动 runtime。", Managed: true},
	{Section: "Runtime", Key: "runtime.idle_timeout_seconds", Type: "integer seconds", Default: "0", Constraints: "0 或 >=60", Description: "旧版默认休眠阈值；新配置优先使用 runtime.session_reclaim。", Managed: true},
	{Section: "Runtime", Key: "runtime.max_sessions", Type: "integer", Default: "100（runtime 应用）", Constraints: ">=0", Description: "整机并发 Session 上限；0 由 runtime 使用默认值。", Managed: true},
	{Section: "Runtime", Key: "runtime.max_concurrency", Type: "integer", Default: "10（runtime 应用）", Constraints: ">=0", Description: "每种 Agent 的并发上限；0 由 runtime 使用默认值。", Managed: true},
	{Section: "Runtime", Key: "runtime.labels.<label>", Type: "string map entry", Default: "无", Description: "调度标签，例如 pool、repo_cache。", Managed: true},

	{Section: "Runtime 清理", Key: "runtime.task_cleanup.enabled", Type: "boolean", Default: "true", Description: "是否自动清理终态 task 历史。"},
	{Section: "Runtime 清理", Key: "runtime.task_cleanup.terminal_history_ttl_seconds", Type: "integer seconds", Default: "604800（7 天）", Constraints: ">0", Description: "终态 task 历史保留时间。"},
	{Section: "Runtime 清理", Key: "runtime.task_cleanup.sweep_interval_seconds", Type: "integer seconds", Default: "3600", Constraints: ">0", Description: "终态 task 清理扫描周期。"},
	{Section: "Runtime 清理", Key: "runtime.broker_reap.enabled", Type: "boolean", Default: "true", Description: "是否定期回收孤儿 session broker 进程及其文件（session 已删除或不再运行、或为重复 broker）。"},
	{Section: "Runtime 清理", Key: "runtime.broker_reap.sweep_interval_seconds", Type: "integer seconds", Default: "300", Constraints: ">0", Description: "孤儿 broker 回收扫描周期。"},

	{Section: "Session 回收", Key: "runtime.session_reclaim.enabled", Type: "boolean", Default: "true", Description: "Session 分阶段回收总开关。"},
	{Section: "Session 回收", Key: "runtime.session_reclaim.sweep_interval_seconds", Type: "integer seconds", Default: "600", Constraints: ">0", Description: "Session 回收扫描周期。"},
	{Section: "Session 回收", Key: "runtime.session_reclaim.local.sleep_after_seconds", Type: "integer seconds", Default: "0", Constraints: "0 或 >=60", Description: "本地 Session 自动休眠阈值。"},
	{Section: "Session 回收", Key: "runtime.session_reclaim.local.archive_after_seconds", Type: "integer seconds", Default: "0", Constraints: "0 或 >=60", Description: "本地 Session 自动归档阈值。"},
	{Section: "Session 回收", Key: "runtime.session_reclaim.local.prune_after_seconds", Type: "integer seconds", Default: "0", Constraints: "0 或 >=60", Description: "本地 Session 自动删除阈值。"},
	{Section: "Session 回收", Key: "runtime.session_reclaim.feishu.sleep_after_seconds", Type: "integer seconds", Default: "172800（48 小时）", Constraints: "0 或 >=60", Description: "飞书 Session 自动休眠阈值。"},
	{Section: "Session 回收", Key: "runtime.session_reclaim.feishu.archive_after_seconds", Type: "integer seconds", Default: "0", Constraints: "0 或 >=60", Description: "飞书 Session 自动归档阈值；默认关闭，显式正值可开启。"},
	{Section: "Session 回收", Key: "runtime.session_reclaim.feishu.prune_after_seconds", Type: "integer seconds", Default: "0", Constraints: "0 或 >=60", Description: "飞书 Session 自动删除本地记录与 replay 的阈值；默认关闭，显式正值可开启，不会删除飞书消息。"},

	{Section: "CLI 与 MCP", Key: "cli.default_target", Type: "string", Default: "local", Description: "CLI 默认 target。", Managed: true},
	{Section: "CLI 与 MCP", Key: "mcp.enabled", Type: "boolean", Default: "true", Description: "是否启用内置 amux MCP。", Managed: true},
	{Section: "CLI 与 MCP", Key: "mcp.auto_inject", Type: "boolean", Default: "true", Description: "是否向普通 run Session 自动投影内置 MCP。", Managed: true},

	{Section: "隔离执行", Key: "isolation.global_env", Type: "KEY=VALUE string array", Default: "[]", Constraints: "合法且不重复的环境变量名", Description: "注入所有 identified 隔离 execution 的 installation-level 共享环境；使用 isolation global-env 命令管理，同名显式 launch env 优先，修改后需重启 system。", Sensitive: true},

	{Section: "飞书基础", Key: "bridge.feishu.notify_chat_id", Type: "string", Default: "空", Description: "兼容用飞书通知群 ID。", Managed: true},
	{Section: "飞书基础", Key: "bridge.feishu.prompt.shared_thread_enabled", Type: "boolean", Default: "false", Description: "是否把共享 thread 历史注入 Prompt。", Managed: true},
	{Section: "飞书基础", Key: "bridge.feishu.prompt.topic_root_enabled", Type: "boolean", Default: "true", Description: "是否在新话题首轮注入根消息。", Managed: true},
	{Section: "飞书基础", Key: "bridge.feishu.prompt.shared_thread_template_file", Type: "path", Default: "空", Description: "共享 thread Prompt 模板文件。", Managed: true},
	{Section: "飞书基础", Key: "bridge.feishu.document_comments.enabled", Type: "boolean", Default: "false", Description: "云文档评论入口安装级开关。", Managed: true},
	{Section: "飞书基础", Key: "bridge.feishu.presentation.thinking_process", Type: "enum", Default: DefaultFeishuThinkingProcess, Constraints: "expanded、collapsed、hidden", Description: "安装级处理过程展示方式；Profile 未显式配置时继承。", Managed: true},
	{Section: "飞书基础", Key: "bridge.feishu.presentation.completion_notification_after_seconds", Type: "integer seconds", Default: strconv.Itoa(DefaultFeishuCompletionNotificationAfterSeconds), Constraints: ">=0；0 禁用本功能全部私聊提醒", Description: "普通任务成功、等待补充或失败且耗时达到阈值后，向当前轮发起人发送可跳回原话题的私聊卡片，不在话题内追加通知；不判断已读状态，智能体自动触发不提醒。", Managed: true},
	{Section: "智能体协作", Key: "bridge.feishu.bot_to_bot.enabled", Type: "boolean", Default: "false", Description: "是否接收 shared workspace 群内外部智能体的结构化显式 @。", Managed: true},
	{Section: "智能体协作", Key: "bridge.feishu.bot_to_bot.max_turns_per_thread", Type: "integer", Default: "10", Constraints: ">0", Description: "每个群话题可接受的智能体协作轮数上限。", Managed: true},

	{Section: "群值守", Key: "bridge.feishu.group_watch.enabled", Type: "boolean", Default: "false", Description: "群值守安装级开关；允许有正文或有效图片、文件、合并转发素材的根消息自动触发。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.chat_ids", Type: "string array", Default: "[]", Description: "允许自动触发的群 chat_id。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.sender_types", Type: "string array", Default: "[user, app]", Constraints: "user、app", Description: "允许自动触发的消息发送者类型。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.chat_sender_types.<chat_id>", Type: "string array", Default: "继承 sender_types", Constraints: "chat_id 必须也在 chat_ids 中；值为 user、app", Description: "指定单个值守群允许自动触发的发送者类型；未配置的群使用 sender_types。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.poll_interval_seconds", Type: "integer seconds", Default: "15", Constraints: "5-300", Description: "其它 App 消息的历史补偿轮询周期。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.lookback_seconds", Type: "integer seconds", Default: "60", Constraints: "5-3600 且不小于轮询周期", Description: "历史补偿回看窗口。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.dedup.enabled", Type: "boolean", Default: "false", Description: "是否在创建 Session 和回复卡片前抑制同群内 App 发送的重复报警。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.dedup.window_seconds", Type: "integer seconds", Default: strconv.Itoa(DefaultFeishuGroupWatchDedupWindowSeconds) + "（30 分钟）", Constraints: fmt.Sprintf("60-%d", MaxFeishuGroupWatchDedupWindowSeconds), Description: "App 报警语义去重窗口，从上一次允许创建 Session 的同类报警开始计算；按发送 App、Rule ID 与标题（提取不到时按规范化可见内容）生成指纹。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.max_active_sessions_per_chat", Type: "integer", Default: strconv.Itoa(DefaultFeishuGroupWatchMaxActiveSessionsPerChat), Constraints: fmt.Sprintf("1-%d", MaxFeishuGroupWatchActiveSessionsPerChat), Description: "当前智能体每个值守群允许的群值守活跃 Session 上限。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.idle_timeout_seconds", Type: "integer seconds", Default: strconv.Itoa(DefaultFeishuGroupWatchIdleTimeoutSeconds) + "（3 小时）", Constraints: fmt.Sprintf("60-%d", MaxFeishuGroupWatchIdleTimeoutSeconds), Description: "群值守新 Session 的自动休眠阈值。", Managed: true},
	{Section: "群值守", Key: "bridge.feishu.group_watch.thinking_process", Type: "enum", Default: DefaultFeishuThinkingProcess, Constraints: "expanded、collapsed、hidden", Description: "安装级群值守处理过程展示方式。", Managed: true},

	{Section: "飞书 Workspace 默认值", Key: "bridge.feishu.workspace_defaults.agent", Type: "string", Default: defaultWorkspaceProfileRef, Description: "新 workspace 的默认 Agent Profile。", Managed: true},
	{Section: "飞书 Workspace 默认值", Key: "bridge.feishu.workspace_defaults.target", Type: "string", Default: "空（自动选择）", Description: "新 workspace 的默认 target。", Managed: true},
	{Section: "飞书 Workspace 默认值", Key: "bridge.feishu.workspace_defaults.cwd", Type: "path", Default: "空", Description: "旧兼容 cwd；自然入口仍使用独立空 workspace。", Managed: true},
	{Section: "飞书 Workspace 默认值", Key: "bridge.feishu.workspace_defaults.model", Type: "string", Default: "空", Description: "新 workspace 的默认模型覆盖。", Managed: true},
	{Section: "飞书 Workspace 默认值", Key: "bridge.feishu.workspace_defaults.args", Type: "string array", Default: "[]", Description: "新 workspace 的默认 Agent 参数。", Managed: true},
	{Section: "飞书 Workspace 默认值", Key: "bridge.feishu.workspace_defaults.idle_timeout_seconds", Type: "integer seconds", Default: "0", Constraints: "0 或 >=60", Description: "新 workspace Session 的休眠阈值。", Managed: true},
	{Section: "飞书 Workspace 默认值", Key: "bridge.feishu.claude_models", Type: "string array", Default: "[]", Description: "飞书模型选择器为 Claude 运行引擎额外列出的模型名，按声明顺序追加在内置 opus、sonnet、haiku 之后，便于选择最新模型。修改后重启生效。", Managed: true},

	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.workspace_defaults.agent", Type: "string", Default: "继承安装级", Description: "Profile 级 workspace 默认专家。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.workspace_defaults.target", Type: "string", Default: "继承安装级", Description: "Profile 级 workspace 默认 target。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.workspace_defaults.cwd", Type: "path", Default: "继承安装级", Description: "Profile 级兼容 cwd。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.workspace_defaults.model", Type: "string", Default: "继承安装级", Description: "Profile 级 workspace 默认模型。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.workspace_defaults.args", Type: "string array", Default: "继承安装级", Description: "Profile 级 workspace 默认参数。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.workspace_defaults.idle_timeout_seconds", Type: "integer seconds", Default: "继承安装级", Constraints: "0 或 >=60", Description: "Profile 级 workspace 休眠阈值。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.interaction.mode", Type: "enum", Default: "thread", Constraints: "thread、singleton", Description: "未保存群独立设置时的消息处理方式；群值守要求对应群的有效模式为 thread。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.presentation.thinking_process", Type: "enum", Default: "继承安装级（collapsed）", Constraints: "expanded、collapsed、hidden", Description: "当前助手普通任务的处理过程展示方式。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.presentation.completion_notification_after_seconds", Type: "integer seconds", Default: "继承安装级（60）", Constraints: ">=0；0 禁用本功能全部私聊提醒", Description: "当前助手普通任务的完成提醒阈值。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.bot_e2e.sender_open_ids", Type: "string array", Default: "[]", Description: "允许通过 @Bot /e2e 发起 owner-authorized 测试输入的 Bot/App sender open_id。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.bot_e2e.sender_roles", Type: "map[string]string", Default: "{}", Constraints: "owner、shared", Description: "把受信测试 Bot sender open_id 映射到其 /e2e 请求投影身份（owner 或 shared）；不提供 unauthorized，未命中保持 owner。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.document_comments.enabled", Type: "boolean", Default: "继承安装级", Description: "Profile 级云文档评论开关。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.document_comments.agent_profile_ref", Type: "string", Default: "空（继承助手）", Description: "云文档评论新 Session 使用的专家。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.document_comments.runtime_ref", Type: "string", Default: "空（继承助手或兼容旧配置）", Description: "云文档评论新 Session 使用的运行引擎。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.document_comments.model_ref", Type: "string", Default: "空（运行引擎默认）", Description: "云文档评论新 Session 使用的模型，按所选运行引擎校验；未覆盖运行引擎时遵循专家模型策略，未声明 supported_models 则允许原生模型名。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.document_comments.reasoning_effort", Type: "string", Default: "空（运行引擎默认）", Description: "云文档评论新 Session 使用的思考强度。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.bot_to_bot.enabled", Type: "boolean", Default: "继承安装级", Description: "Profile 级智能体协作开关。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.bot_to_bot.max_turns_per_thread", Type: "integer", Default: "继承安装级（10）", Constraints: ">0", Description: "Profile 级单话题智能体协作轮数上限。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_settings.<chat_id>.enabled", Type: "boolean", Default: "true", Description: "本群值守开关，与群值守总开关同时开启才接收自动任务。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_settings.<chat_id>.agent_profile_ref", Type: "string", Default: "空（继承本群普通会话）", Description: "本群值守专家；空值使用本群普通会话的执行配置。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_settings.<chat_id>.runtime_ref", Type: "string", Default: "空（继承本群普通会话）", Description: "本群自定义值守的运行引擎；自定义专家时必填。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_settings.<chat_id>.model_ref", Type: "string", Default: "空（运行引擎默认）", Description: "本群值守模型，按所选运行引擎校验。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_settings.<chat_id>.reasoning_effort", Type: "string", Default: "空（运行引擎默认）", Description: "本群值守思考强度，按所选运行引擎和模型校验。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_settings.<chat_id>.thinking_process", Type: "enum", Default: "继承群值守默认值", Constraints: "expanded、collapsed、hidden", Description: "本群值守任务的处理过程展示方式。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_settings.<chat_id>.max_active_sessions_per_chat", Type: "integer", Default: "继承群值守默认值", Constraints: "1-500", Description: "本群值守活跃 Session 上限。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_settings.<chat_id>.idle_timeout_seconds", Type: "integer seconds", Default: "继承群值守默认值", Constraints: "60-604800", Description: "本群新值守 Session 的自动休眠阈值。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_settings.<chat_id>.version", Type: "integer", Default: "0", Constraints: "非负整数", Description: "本群值守设置版本，用于拒绝过期卡片写入。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.enabled", Type: "boolean", Default: "继承安装级", Description: "Profile 级群值守开关；纯图片、纯文件和无正文合并转发也可自动触发。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_ids", Type: "string array", Default: "继承安装级", Description: "Profile 级值守群白名单。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.sender_types", Type: "string array", Default: "继承安装级", Constraints: "user、app", Description: "Profile 级发送者类型。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.chat_sender_types.<chat_id>", Type: "string array", Default: "继承安装级", Constraints: "chat_id 必须也在该 Profile 的有效 chat_ids 中；值为 user、app", Description: "Profile 级单值守群发送者类型覆盖；未覆盖的群使用当前 Profile 的 sender_types。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.poll_interval_seconds", Type: "integer seconds", Default: "继承安装级", Constraints: "5-300", Description: "Profile 级历史轮询周期。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.lookback_seconds", Type: "integer seconds", Default: "继承安装级", Constraints: "5-3600", Description: "Profile 级历史回看窗口。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.dedup.enabled", Type: "boolean", Default: "继承安装级", Description: "Profile 级 App 报警语义去重开关。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.dedup.window_seconds", Type: "integer seconds", Default: fmt.Sprintf("继承安装级（%d）", DefaultFeishuGroupWatchDedupWindowSeconds), Constraints: fmt.Sprintf("60-%d", MaxFeishuGroupWatchDedupWindowSeconds), Description: "Profile 级 App 报警语义去重窗口。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.max_active_sessions_per_chat", Type: "integer", Default: fmt.Sprintf("继承安装级（%d）", DefaultFeishuGroupWatchMaxActiveSessionsPerChat), Constraints: fmt.Sprintf("1-%d", MaxFeishuGroupWatchActiveSessionsPerChat), Description: "Profile 级每群活跃 Session 上限。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.idle_timeout_seconds", Type: "integer seconds", Default: fmt.Sprintf("继承安装级（%d）", DefaultFeishuGroupWatchIdleTimeoutSeconds), Constraints: fmt.Sprintf("60-%d", MaxFeishuGroupWatchIdleTimeoutSeconds), Description: "Profile 级群值守自动休眠阈值。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.agent_profile_ref", Type: "string", Default: "空（继承助手）", Description: "群值守新 Session 使用的专家；空值继承该群有效默认专家；自定义配置与 Runtime 独立。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.runtime_ref", Type: "string", Default: "空（继承助手）", Description: "群值守新 Session 使用的运行引擎；继承任务配置时使用该群有效默认 Runtime，自定义配置时必填。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.model_ref", Type: "string", Default: "空（运行引擎默认）", Description: "群值守新 Session 使用的模型，按所选运行引擎校验；未覆盖运行引擎时遵循专家模型策略，未声明 supported_models 则允许原生模型名。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.reasoning_effort", Type: "string", Default: "空（运行引擎默认）", Description: "群值守新 Session 使用的思考强度，必须受所选运行引擎和模型支持。", Managed: true},
	{Section: "飞书智能体 Profile", Key: "bridge.feishu.profiles.<profile>.group_watch.thinking_process", Type: "enum", Default: "继承安装级群值守（collapsed）", Constraints: "expanded、collapsed、hidden", Description: "当前助手群值守任务的处理过程展示方式，与普通任务独立配置。", Managed: true},

	{Section: "Structured Provider", Key: "provider.structured.turn_idle_timeout_seconds", Type: "integer seconds", Default: strconv.Itoa(defaultTurnIdleTimeoutSeconds), Constraints: "0 禁用，负数回默认，最大 604800", Description: "Structured provider 与 ACP turn 无有效活动超时；等待权限及无进度契约的 ACP 工具期间暂停。"},
	{Section: "Structured Provider", Key: "provider.structured.turn_hard_cap_seconds", Type: "integer seconds", Default: strconv.Itoa(defaultTurnHardCapSeconds), Constraints: "0 禁用，负数回默认，最大 604800", Description: "Structured provider 与 ACP 单 turn 绝对时长上限，包含等待权限及工具的时间。"},
	{Section: "Structured Provider", Key: "provider.structured.tool_idle_timeout_seconds", Type: "integer seconds", Default: strconv.Itoa(defaultToolIdleTimeoutSeconds), Constraints: "0 禁用，负数回默认，最大 604800", Description: "Structured provider 工具无进度超时；ACP 尚无可验证的工具进度契约，使用 turn hard cap。"},
	{Section: "Structured Provider", Key: "provider.structured.repeat_tool_call_limit", Type: "integer count", Default: strconv.Itoa(defaultRepeatToolCallLimit), Constraints: "0 禁用，负数回默认，最大 1000", Description: "Structured provider 与 ACP 同一 turn 内连续相同工具调用次数上限；ACP 只计不同调用 ID 且具备完整工具身份和输入的调用。"},
})

// RenderConfigurationReference renders the checked-in configuration reference.
func RenderConfigurationReference() []byte {
	var out bytes.Buffer
	out.WriteString("<!-- Code generated by make config-doc; DO NOT EDIT. -->\n\n")
	out.WriteString("# amux 配置参考\n\n")
	out.WriteString("本文档覆盖 `~/.amux/config.toml` 中由 amux 代码读取的配置。通用配置元数据位于公共核心的 `internal/config/reference.go`，发行版通过扩展接口注册自己的配置元数据。修改配置键、默认值或约束时应同步修改所属 module 的元数据，并在相应 module 中运行 `make config-doc`。`scripts/check.sh` 会检查遗漏和生成文件漂移。\n\n")
	out.WriteString("常用命令：\n\n```bash\namux system config path\namux system config get <key>\namux system config set <key> <value>\namux system config unset <key>\n```\n\n")
	out.WriteString("“CLI 可管理”专指通用 `amux system config set/get/unset`；标为“否”的键可能由说明中列出的专用命令管理，否则需要直接编辑 TOML。Profile 键中的 `<profile>` 替换为实际飞书智能体 Profile 名；Provider 键中的 `<provider>` 只支持 " + structuredConfigProviderNames() + "。敏感值不会在本文档中给出示例。\n\n")
	sections := make([]string, 0)
	seen := map[string]bool{}
	for _, option := range configOptions {
		if !seen[option.Section] {
			seen[option.Section] = true
			sections = append(sections, option.Section)
		}
	}
	for _, section := range sections {
		fmt.Fprintf(&out, "## %s\n\n", section)
		out.WriteString("| 配置键 | 类型 | 默认值 | 约束 | CLI 可管理 | 说明 |\n")
		out.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		for _, option := range configOptions {
			if option.Section != section {
				continue
			}
			managed := "否"
			if option.Managed {
				managed = "是"
			}
			defaultValue := option.Default
			if option.Sensitive {
				defaultValue = "敏感值，不展示"
			}
			fmt.Fprintf(&out, "| `%s` | %s | %s | %s | %s | %s |\n",
				escapeMarkdownCell(option.Key), escapeMarkdownCell(option.Type), escapeMarkdownCell(defaultValue),
				escapeMarkdownCell(valueOrDash(option.Constraints)), managed, escapeMarkdownCell(option.Description))
		}
		out.WriteByte('\n')
	}
	out.WriteString("## 不属于 config.toml 的状态\n\n")
	out.WriteString("飞书 App Secret 与 legacy-host credential 保存在 `$AMUX_HOME/secrets/`；identified Agent credential 按 Principal 保存在 `$AMUX_HOME/principals/<principal-id>/secrets/`。workspace Session quota、thread binding 与群值守 binding 等运行状态保存在 Bridge/Runtime SQLite 中。它们不是 `config.toml` 配置键，不应写入本文件。\n")
	return out.Bytes()
}

func valueOrDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func escapeMarkdownCell(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", "<br>")
}
