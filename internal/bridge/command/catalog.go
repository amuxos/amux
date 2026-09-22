// Package command owns the Feishu text-command interface and stable policies.
//
// Routing and help rendering both read this catalog. Help-only entries are the
// intentional user-facing surface; hidden compatibility and rescue commands
// are classified here without appearing in help or native discovery.
package command

import (
	"strings"

	"github.com/amuxos/amux/internal/distribution"
)

type Scope string

const (
	ScopePrivate     Scope = "private"
	ScopeGroup       Scope = "group"
	ScopeFreshTopic  Scope = "fresh_topic"
	ScopeActiveTopic Scope = "active_topic"
)

type Command string

const (
	CommandNone             Command = ""
	CommandAmux             Command = "amux"
	CommandHelp             Command = "help"
	CommandCollaborationOn  Command = "collaboration_on"
	CommandCollaborationOff Command = "collaboration_off"
	CommandGroupWatchOn     Command = "group_watch_on"
	CommandGroupWatchOff    Command = "group_watch_off"
	CommandBind             Command = "bind"
	CommandRun              Command = "run"
	CommandWorkspace        Command = "workspace"
	CommandStatus           Command = "status"
	CommandUnbind           Command = "unbind"
	CommandRestart          Command = "restart"
	CommandSkill            Command = "skill"
	CommandInterrupt        Command = "interrupt"
	CommandStop             Command = "stop"
	CommandCompact          Command = "compact"
	CommandClear            Command = "clear"
	CommandQueue            Command = "queue"
	CommandQueueClear       Command = "queue_clear"
	CommandQueueCancel      Command = "queue_cancel"
	CommandUnsupported      Command = "unsupported"
)

func (c Command) RequiresBindingControl() bool {
	switch c {
	case CommandUnbind, CommandStop, CommandInterrupt, CommandRestart, CommandCompact,
		CommandClear, CommandBind, CommandQueueClear, CommandQueueCancel:
		return true
	default:
		return false
	}
}

type SingletonDisposition uint8

const (
	SingletonUnhandled SingletonDisposition = iota
	SingletonControl
	SingletonSkill
	SingletonConflict
)

func (c Command) SingletonDisposition() SingletonDisposition {
	switch c {
	case CommandStatus, CommandInterrupt, CommandRestart, CommandCompact, CommandClear:
		return SingletonControl
	case CommandSkill:
		return SingletonSkill
	case CommandRun, CommandBind, CommandStop:
		return SingletonConflict
	default:
		return SingletonUnhandled
	}
}

type HelpItem struct {
	ID          Command
	SectionID   string
	Section     string
	Command     string
	Description string
}

// NativeSlashCommand is an application-level command shown by Feishu's input
// box command picker. Selecting an entry still goes through the normal
// context-sensitive router and authorization checks.
type NativeSlashCommand struct {
	Command     string
	Description string
	I18n        map[string]string
	IconKey     string
}

type nativeSlashMetadata struct {
	description string
	i18n        map[string]string
	iconKey     string
}

type definition struct {
	id                 Command
	command            string
	section            string
	sectionID          string
	aliases            map[Scope][]string
	description        map[Scope]string
	dynamicDescription map[Scope]func() string
	nativeSlash        *nativeSlashMetadata
}

var catalog = []definition{
	{
		id:        CommandAmux,
		command:   "/amux",
		section:   "常用命令",
		sectionID: "common",
		aliases: map[Scope][]string{
			ScopePrivate:     {"/amux"},
			ScopeGroup:       {"/amux", "查看设置", "当前设置", "查看默认专家", "查看默认助手"},
			ScopeFreshTopic:  {"/amux"},
			ScopeActiveTopic: {"/amux"},
		},
		description: map[Scope]string{
			ScopePrivate:     "查看助手状态、设置和系统维护",
			ScopeGroup:       "查看当前群权限",
			ScopeFreshTopic:  "查看如何开始任务",
			ScopeActiveTopic: "查看当前任务状态",
		},
		nativeSlash: &nativeSlashMetadata{
			description: "查看当前对话的 amux 状态与入口",
			i18n: map[string]string{
				"zh_cn": "查看当前对话的 amux 状态与入口",
				"en_us": "Open amux status and actions for this conversation",
			},
			iconKey: "ai-agent_outlined",
		},
	},
	{
		id:        CommandHelp,
		command:   "/help",
		section:   "常用命令",
		sectionID: "common",
		aliases: map[Scope][]string{
			ScopePrivate:     {"/help", "/h", "/?", "帮助"},
			ScopeGroup:       {"/help", "/h", "/?", "帮助"},
			ScopeFreshTopic:  {"/help", "/h", "/?", "帮助"},
			ScopeActiveTopic: {"/help", "/h", "/?", "帮助"},
		},
		description: map[Scope]string{
			ScopePrivate:     "查看私聊使用方法",
			ScopeGroup:       "查看群聊使用方法",
			ScopeFreshTopic:  "查看开始任务方法",
			ScopeActiveTopic: "查看当前任务可用操作",
		},
		nativeSlash: &nativeSlashMetadata{
			description: "查看当前对话的 amux 使用帮助",
			i18n: map[string]string{
				"zh_cn": "查看当前对话的 amux 使用帮助",
				"en_us": "Show amux help for this conversation",
			},
			iconKey: "explanation-ai_outlined",
		},
	},
	{
		id:        CommandCollaborationOn,
		command:   "开启协作模式",
		section:   "群权限",
		sectionID: "access",
		aliases: map[Scope][]string{
			ScopeGroup: {"开启协作模式"},
		},
		description: map[Scope]string{
			ScopeGroup: "仅负责人：允许群成员 @智能体 发起任务",
		},
	},
	{
		id:        CommandCollaborationOff,
		command:   "关闭协作模式",
		section:   "群权限",
		sectionID: "access",
		aliases: map[Scope][]string{
			ScopeGroup: {"关闭协作模式", "切回负责人模式"},
		},
		description: map[Scope]string{
			ScopeGroup: "仅负责人：恢复为只有负责人可以使用",
		},
	},
	{
		id:        CommandGroupWatchOn,
		command:   "开启群值守",
		section:   "群值守",
		sectionID: "watch",
		aliases: map[Scope][]string{
			ScopeGroup:      {"开启群值守"},
			ScopeFreshTopic: {"开启群值守"},
		},
		description: map[Scope]string{
			ScopeGroup:      "仅负责人：确认后自动处理本群的新话题",
			ScopeFreshTopic: "仅负责人：确认后自动处理本群的新话题",
		},
	},
	{
		id:        CommandGroupWatchOff,
		command:   "关闭群值守",
		section:   "群值守",
		sectionID: "watch",
		aliases: map[Scope][]string{
			ScopeGroup:      {"关闭群值守"},
			ScopeFreshTopic: {"关闭群值守"},
		},
		description: map[Scope]string{
			ScopeGroup:      "仅负责人：确认后将本群移出值守",
			ScopeFreshTopic: "仅负责人：确认后将本群移出值守",
		},
	},
	{
		id:        CommandInterrupt,
		command:   "/interrupt",
		section:   "当前任务",
		sectionID: "task",
		aliases: map[Scope][]string{
			ScopeActiveTopic: {"/interrupt", "中断", "停一下"},
		},
		description: map[Scope]string{
			ScopeActiveTopic: "停止当前回答，保留任务",
		},
		nativeSlash: nativeSlash("停止当前回答，保留任务", "Interrupt the current response and keep the session"),
	},
	{
		id:        CommandStop,
		command:   "/stop",
		section:   "当前任务",
		sectionID: "task",
		aliases: map[Scope][]string{
			ScopeActiveTopic: {"/stop", "停止", "结束任务"},
		},
		description: map[Scope]string{
			ScopeActiveTopic: "结束当前任务",
		},
		nativeSlash: nativeSlash("结束当前任务", "Stop the current session"),
	},
	{
		id:        CommandSkill,
		command:   "/skill",
		section:   "当前任务",
		sectionID: "task",
		aliases: map[Scope][]string{
			ScopeActiveTopic: {"/skill"},
		},
		dynamicDescription: map[Scope]func() string{
			ScopeActiveTopic: func() string {
				return "选择当前 " + distribution.CodexCompatibleLabel + " Session 已加载的 Skill"
			},
		},
		nativeSlash: nativeSlash("选择当前 Session 已加载的 Skill", "Choose a skill loaded by the current session"),
	},
	{
		id:        CommandCompact,
		command:   "/compact",
		section:   "当前任务",
		sectionID: "task",
		aliases: map[Scope][]string{
			ScopeActiveTopic: {"/compact"},
		},
		description: map[Scope]string{
			ScopeActiveTopic: "压缩当前上下文并保留关键摘要",
		},
		nativeSlash: nativeSlash("压缩当前上下文并保留关键摘要", "Compact the current context and retain a summary"),
	},
	{
		id:        CommandClear,
		command:   "/clear",
		section:   "当前任务",
		sectionID: "task",
		aliases: map[Scope][]string{
			ScopeActiveTopic: {"/clear"},
		},
		description: map[Scope]string{
			ScopeActiveTopic: "清空当前上下文并开始新对话",
		},
		nativeSlash: nativeSlash("清空当前上下文并开始新对话", "Clear the current context and start a new chat"),
	},
}

var nativeHelpCommands = []NativeSlashCommand{
	newNativeSlashCommand("run", "创建新的任务 Session", "Start a new task session"),
	newNativeSlashCommand("bind", "接入已有 Session", "Bind this conversation to an existing session"),
	newNativeSlashCommand("status", "查看当前 Session 状态", "Show the current session status"),
	newNativeSlashCommand("restart", "重新启动当前 Session", "Restart the current session"),
}

func nativeSlash(zhCN, enUS string) *nativeSlashMetadata {
	return &nativeSlashMetadata{
		description: zhCN,
		i18n:        map[string]string{"zh_cn": zhCN, "en_us": enUS},
		iconKey:     "ai-agent_outlined",
	}
}

func newNativeSlashCommand(command, zhCN, enUS string) NativeSlashCommand {
	return NativeSlashCommand{
		Command: command, Description: zhCN,
		I18n:    map[string]string{"zh_cn": zhCN, "en_us": enUS},
		IconKey: "ai-agent_outlined",
	}
}

func helpOnly() map[Scope][]HelpItem {
	return map[Scope][]HelpItem{
		ScopePrivate: {
			{SectionID: "entry", Section: "任务入口", Command: "/run <profile> -n <name>", ID: CommandRun, Description: "在当前消息话题创建任务 Session"},
			{SectionID: "entry", Section: "任务入口", Command: "/bind <target/session>", ID: CommandBind, Description: "在当前消息话题接入已有 Session"},
			{SectionID: "task", Section: "当前任务", Command: "/status", ID: CommandStatus, Description: "查看已绑定 Session 状态"},
			{SectionID: "task", Section: "当前任务", Command: "/restart", ID: CommandRestart, Description: "重新启动已绑定 Session"},
			{SectionID: "task", Section: "当前任务", Command: "/interrupt", ID: CommandInterrupt, Description: "停止当前回答，保留任务"},
			{SectionID: "task", Section: "当前任务", Command: "/stop", ID: CommandStop, Description: "结束当前任务"},
			{SectionID: "agent", Section: distribution.CodexCompatibleGroup, Command: "/skill", ID: CommandSkill, Description: "选择当前 Session 已加载的 Skill"},
			{SectionID: "agent", Section: distribution.CodexCompatibleGroup, Command: "/compact", ID: CommandCompact, Description: "压缩上下文并保留关键摘要"},
			{SectionID: "agent", Section: distribution.CodexCompatibleGroup, Command: "/clear", ID: CommandClear, Description: "保留 Session 绑定并切换到全新对话"},
		},
		ScopeGroup: {
			{SectionID: "entry", Section: "任务入口", Command: "/run <profile> -n <name>", ID: CommandRun, Description: "显式创建任务 Session"},
			{SectionID: "entry", Section: "任务入口", Command: "/bind <target/session>", ID: CommandBind, Description: "接入已有 Session"},
		},
		ScopeFreshTopic: {
			{SectionID: "start", Section: "开始任务", Command: "/run <profile> -n <name>", ID: CommandRun, Description: "显式创建任务 Session"},
			{SectionID: "start", Section: "开始任务", Command: "/bind <target/session>", ID: CommandBind, Description: "接入已有 Session"},
		},
		ScopeActiveTopic: {
			{SectionID: "task", Section: "当前任务", Command: "/status", ID: CommandStatus, Description: "查看当前 Session 状态"},
			{SectionID: "task", Section: "当前任务", Command: "/restart", ID: CommandRestart, Description: "重新启动当前 Session"},
			{SectionID: "task", Section: "当前任务", Command: "/bind <target/session>", ID: CommandBind, Description: "改为接入其他 Session"},
		},
	}
}

func Resolve(scope Scope, text string) Command {
	text = normalize(text)
	if text == "" {
		return CommandNone
	}
	for _, item := range catalog {
		for _, alias := range item.aliases[scope] {
			if text == normalize(alias) {
				return item.id
			}
		}
	}
	return CommandNone
}

// Classify returns the canonical command accepted in a routing scope. It owns
// command vocabulary only; authorization and execution remain Bridge concerns.
func Classify(scope Scope, text string) Command {
	if command := Resolve(scope, text); command != CommandNone {
		return command
	}
	command, ok := slashCommandText(text)
	if !ok {
		return CommandNone
	}
	parts := strings.Fields(strings.ToLower(command))
	if len(parts) == 0 {
		return CommandNone
	}
	if scope == ScopeFreshTopic {
		switch parts[0] {
		case "bind":
			return CommandBind
		case "run":
			return CommandRun
		case "skill":
			return CommandSkill
		default:
			return CommandNone
		}
	}
	if scope != ScopeActiveTopic {
		return CommandNone
	}
	switch parts[0] {
	case "status":
		return CommandStatus
	case "unbind":
		return CommandUnbind
	case "bind":
		return CommandBind
	case "run":
		return CommandRun
	case "workspace":
		return CommandWorkspace
	case "restart":
		return CommandRestart
	case "skill":
		return CommandSkill
	case "schedule", "sched":
		return CommandUnsupported
	case "queue":
		if len(parts) == 1 {
			return CommandQueue
		}
		switch parts[1] {
		case "clear":
			return CommandQueueClear
		case "cancel":
			return CommandQueueCancel
		}
	}
	return CommandNone
}

func Help(scope Scope) []HelpItem {
	var items []HelpItem
	for _, item := range catalog {
		description := strings.TrimSpace(item.description[scope])
		if dynamic := item.dynamicDescription[scope]; dynamic != nil {
			description = strings.TrimSpace(dynamic())
		}
		if description == "" {
			continue
		}
		items = append(items, HelpItem{
			ID:          item.id,
			SectionID:   item.sectionID,
			Section:     item.section,
			Command:     item.command,
			Description: description,
		})
	}
	items = append(items, helpOnly()[scope]...)
	return items
}

// ManagedNativeSlashCommands returns every application-level command whose
// lifecycle amux has managed. It includes entries no longer shown on every
// interaction surface so reconciliation can remove exact stale registrations.
// Callers receive independent maps.
func ManagedNativeSlashCommands() []NativeSlashCommand {
	var commands []NativeSlashCommand
	for _, item := range catalog {
		if item.nativeSlash == nil {
			continue
		}
		i18n := make(map[string]string, len(item.nativeSlash.i18n))
		for locale, description := range item.nativeSlash.i18n {
			i18n[locale] = description
		}
		commands = append(commands, NativeSlashCommand{
			Command:     strings.TrimPrefix(item.command, "/"),
			Description: item.nativeSlash.description,
			I18n:        i18n,
			IconKey:     item.nativeSlash.iconKey,
		})
	}
	for _, command := range nativeHelpCommands {
		i18n := make(map[string]string, len(command.I18n))
		for locale, description := range command.I18n {
			i18n[locale] = description
		}
		command.I18n = i18n
		commands = append(commands, command)
	}
	return commands
}

// GlobalNativeSlashCommands is the stable discovery surface that does not
// depend on a currently bound Session. Thread-mode bots expose only this set.
func GlobalNativeSlashCommands() []NativeSlashCommand {
	return selectNativeSlashCommands("amux", "help")
}

// SingletonNativeSlashCommands returns the discovery surface for the one
// persistent Bot Session. Codex-compatible controls are only advertised when
// the bound Agent can execute them.
func SingletonNativeSlashCommands(codexCompatible bool) []NativeSlashCommand {
	names := []string{"amux", "help", "status", "restart", "interrupt"}
	if codexCompatible {
		names = append(names, "skill", "compact", "clear")
	}
	return selectNativeSlashCommands(names...)
}

func selectNativeSlashCommands(names ...string) []NativeSlashCommand {
	managed := ManagedNativeSlashCommands()
	byName := make(map[string]NativeSlashCommand, len(managed))
	for _, command := range managed {
		byName[command.Command] = command
	}
	commands := make([]NativeSlashCommand, 0, len(names))
	for _, name := range names {
		if command, ok := byName[name]; ok {
			commands = append(commands, command)
		}
	}
	return commands
}

func normalize(text string) string {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) > 0 && strings.HasPrefix(strings.Trim(parts[0], "<>"), "@") {
		parts = parts[1:]
	}
	return strings.Join(parts, " ")
}

func slashCommandText(text string) (string, bool) {
	text = normalize(text)
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "/"))
	return text, text != ""
}
