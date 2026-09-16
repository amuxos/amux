package command

import (
	"slices"
	"strings"
	"testing"
)

func TestClassifySessionCommands(t *testing.T) {
	tests := []struct {
		name  string
		scope Scope
		input string
		want  Command
	}{
		{name: "fresh run", scope: ScopeFreshTopic, input: "/run builtin/codex -n demo", want: CommandRun},
		{name: "fresh skill needs binding", scope: ScopeFreshTopic, input: "/skill", want: CommandSkill},
		{name: "active status", scope: ScopeActiveTopic, input: "/status", want: CommandStatus},
		{name: "active queue clear", scope: ScopeActiveTopic, input: "/queue clear", want: CommandQueueClear},
		{name: "active natural interrupt", scope: ScopeActiveTopic, input: "停一下", want: CommandInterrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.scope, tt.input); got != tt.want {
				t.Fatalf("Classify(%q, %q) = %q, want %q", tt.scope, tt.input, got, tt.want)
			}
		})
	}
}

func TestBindingControlPolicy(t *testing.T) {
	for _, command := range []Command{CommandBind, CommandUnbind, CommandRestart, CommandInterrupt, CommandStop, CommandCompact, CommandClear, CommandQueueClear, CommandQueueCancel} {
		if !command.RequiresBindingControl() {
			t.Errorf("%q should require binding control", command)
		}
	}
	for _, command := range []Command{CommandNone, CommandAmux, CommandHelp, CommandStatus, CommandQueue, CommandSkill} {
		if command.RequiresBindingControl() {
			t.Errorf("%q should not require binding control", command)
		}
	}
}

func TestSingletonDisposition(t *testing.T) {
	tests := map[Command]SingletonDisposition{
		CommandStatus:    SingletonControl,
		CommandInterrupt: SingletonControl,
		CommandRestart:   SingletonControl,
		CommandCompact:   SingletonControl,
		CommandClear:     SingletonControl,
		CommandSkill:     SingletonSkill,
		CommandRun:       SingletonConflict,
		CommandBind:      SingletonConflict,
		CommandStop:      SingletonConflict,
		CommandHelp:      SingletonUnhandled,
	}
	for command, want := range tests {
		if got := command.SingletonDisposition(); got != want {
			t.Errorf("%q singleton disposition = %v, want %v", command, got, want)
		}
	}
}

func TestResolvePublicCommandsByScope(t *testing.T) {
	tests := []struct {
		name  string
		scope Scope
		input string
		want  Command
	}{
		{name: "private amux", scope: ScopePrivate, input: "/amux", want: CommandAmux},
		{name: "group amux", scope: ScopeGroup, input: "@amux /amux", want: CommandAmux},
		{name: "fresh help", scope: ScopeFreshTopic, input: "/help", want: CommandHelp},
		{name: "active interrupt", scope: ScopeActiveTopic, input: "/interrupt", want: CommandInterrupt},
		{name: "active skill", scope: ScopeActiveTopic, input: "/skill", want: CommandSkill},
		{name: "active compact", scope: ScopeActiveTopic, input: "/compact", want: CommandCompact},
		{name: "active clear", scope: ScopeActiveTopic, input: "/clear", want: CommandClear},
		{name: "active stop natural alias", scope: ScopeActiveTopic, input: "结束任务", want: CommandStop},
		{name: "group collaboration on", scope: ScopeGroup, input: "开启协作模式", want: CommandCollaborationOn},
		{name: "group collaboration off", scope: ScopeGroup, input: "切回负责人模式", want: CommandCollaborationOff},
		{name: "group watch on", scope: ScopeGroup, input: "开启群值守", want: CommandGroupWatchOn},
		{name: "group watch off", scope: ScopeGroup, input: "关闭群值守", want: CommandGroupWatchOff},
		{name: "fresh topic group watch on", scope: ScopeFreshTopic, input: "开启群值守", want: CommandGroupWatchOn},
		{name: "fresh topic group watch off", scope: ScopeFreshTopic, input: "关闭群值守", want: CommandGroupWatchOff},
		{name: "group settings alias", scope: ScopeGroup, input: "查看设置", want: CommandAmux},
		{name: "group default expert alias", scope: ScopeGroup, input: "查看默认专家", want: CommandAmux},
		{name: "group legacy default assistant alias", scope: ScopeGroup, input: "查看默认助手", want: CommandAmux},
		{name: "private group command rejected", scope: ScopePrivate, input: "开启协作模式", want: CommandNone},
		{name: "private group watch command rejected", scope: ScopePrivate, input: "开启群值守", want: CommandNone},
		{name: "fresh interrupt rejected", scope: ScopeFreshTopic, input: "/interrupt", want: CommandNone},
		{name: "amux subcommand is compatibility", scope: ScopePrivate, input: "/amux settings", want: CommandNone},
		{name: "run is compatibility", scope: ScopeFreshTopic, input: "/run builtin/claude -n demo", want: CommandNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.scope, tt.input); got != tt.want {
				t.Fatalf("Resolve(%q, %q) = %q, want %q", tt.scope, tt.input, got, tt.want)
			}
		})
	}
}

func TestAdvancedHelpAliasRemovedEverywhere(t *testing.T) {
	for _, scope := range []Scope{ScopePrivate, ScopeGroup, ScopeFreshTopic, ScopeActiveTopic} {
		if got := Resolve(scope, "高级帮助"); got != CommandNone {
			t.Fatalf("Resolve(%q, 高级帮助) = %q, want removed alias", scope, got)
		}
	}
}

func TestNativeSlashCommandsMatchInteractionSurface(t *testing.T) {
	tests := []struct {
		name string
		got  []NativeSlashCommand
		want []string
	}{
		{name: "thread global only", got: GlobalNativeSlashCommands(), want: []string{"amux", "help"}},
		{name: "singleton generic session", got: SingletonNativeSlashCommands(false), want: []string{"amux", "help", "status", "restart", "interrupt"}},
		{name: "singleton codex compatible", got: SingletonNativeSlashCommands(true), want: []string{"amux", "help", "status", "restart", "interrupt", "skill", "compact", "clear"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			names := make([]string, 0, len(tt.got))
			for _, command := range tt.got {
				names = append(names, command.Command)
				if command.Description == "" || command.I18n["zh_cn"] == "" || command.I18n["en_us"] == "" || command.IconKey == "" {
					t.Fatalf("incomplete native slash command: %#v", command)
				}
			}
			if !slices.Equal(names, tt.want) {
				t.Fatalf("native slash command names = %#v, want %#v", names, tt.want)
			}
		})
	}
}

func TestManagedNativeSlashCommandsIncludeHistoricalEntriesForCleanup(t *testing.T) {
	got := ManagedNativeSlashCommands()
	names := make([]string, 0, len(got))
	for _, command := range got {
		names = append(names, command.Command)
	}
	want := []string{"amux", "help", "interrupt", "stop", "skill", "compact", "clear", "run", "bind", "status", "restart"}
	if !slices.Equal(names, want) {
		t.Fatalf("managed native slash command names = %#v, want %#v", names, want)
	}
}

func TestHelpListsSupportedCommandsByScope(t *testing.T) {
	tests := []struct {
		scope    Scope
		required []string
	}{
		{scope: ScopePrivate, required: []string{"/amux", "/help", "/run <profile> -n <name>", "/bind <target/session>", "/status", "/restart", "/interrupt", "/stop", "/skill", "/compact", "/clear"}},
		{scope: ScopeGroup, required: []string{"/amux", "/help", "开启协作模式", "关闭协作模式", "开启群值守", "关闭群值守", "/run <profile> -n <name>", "/bind <target/session>"}},
		{scope: ScopeFreshTopic, required: []string{"/amux", "/help", "开启群值守", "关闭群值守", "/run <profile> -n <name>", "/bind <target/session>"}},
		{scope: ScopeActiveTopic, required: []string{"/amux", "/help", "/interrupt", "/stop", "/skill", "/compact", "/clear", "/status", "/restart", "/bind <target/session>"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.scope), func(t *testing.T) {
			items := Help(tt.scope)
			got := make([]string, 0, len(items))
			for _, item := range items {
				got = append(got, item.Command)
			}
			for _, command := range tt.required {
				if !slices.Contains(got, command) {
					t.Fatalf("Help(%q) commands = %q, missing %q", tt.scope, got, command)
				}
			}
			for _, removed := range []string{"/session list", "/target list", "/bot-session status", "/bot-session bind <target/session>", "/bot-session off"} {
				if slices.Contains(got, removed) {
					t.Fatalf("Help(%q) commands = %q, should not contain removed %q", tt.scope, got, removed)
				}
			}
			for _, removed := range []string{"/queue", "/queue clear", "/queue cancel <id>", "/unbind"} {
				if slices.Contains(got, removed) {
					t.Fatalf("Help(%q) commands = %q, should not contain retired %q", tt.scope, got, removed)
				}
			}
			if tt.scope == ScopePrivate {
				for _, removed := range []string{"/amux status", "/amux settings", "/amux help", "/amux onboarding"} {
					if slices.Contains(got, removed) {
						t.Fatalf("Help(%q) commands = %q, should not contain removed %q", tt.scope, got, removed)
					}
				}
			}
			if tt.scope == ScopePrivate || tt.scope == ScopeGroup {
				for _, command := range got {
					if strings.HasPrefix(command, "/workspace") {
						t.Fatalf("Help(%q) commands = %q, should hide advanced workspace command %q", tt.scope, got, command)
					}
				}
			}
		})
	}
}
