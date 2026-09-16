package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/spf13/viper"
)

func TestGetHubUsesHubAddr(t *testing.T) {
	path := writeConfig(t, `[hub]
addr = "127.0.0.1:7000"
`)
	requireLoad(t, path)

	got := config.GetHub()
	if got.Addr != "127.0.0.1:7000" {
		t.Fatalf("Addr = %q, want 127.0.0.1:7000", got.Addr)
	}
}

func TestLoadDefaultPathUsesAMUXHome(t *testing.T) {
	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)
	if err := os.WriteFile(filepath.Join(amuxHome, "config.toml"), []byte("[hub]\naddr = \"127.0.0.1:17100\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	viper.Reset()
	if err := config.Load(""); err != nil {
		t.Fatalf("Load default: %v", err)
	}
	if got := config.GetHub().Addr; got != "127.0.0.1:17100" {
		t.Fatalf("hub addr = %q, want AMUX_HOME config", got)
	}
}

func TestGetRuntimeUsesRuntimeName(t *testing.T) {
	path := writeConfig(t, `[runtime]
id = "rt-1"
name = "devbox"
auto_start = true
idle_timeout_seconds = 600
`)
	requireLoad(t, path)

	got := config.GetRuntime()
	if got.ID != "rt-1" || got.Name != "devbox" || !got.AutoStart || got.IdleTimeoutSeconds != 600 {
		t.Fatalf("runtime = %+v, want id/name/auto_start/idle_timeout_seconds", got)
	}
}

func TestGetMCPDefaultsEnabledAndAutoInject(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)

	got := config.GetMCP()
	if !got.Enabled || !got.AutoInject {
		t.Fatalf("mcp = %+v, want enabled and auto inject by default", got)
	}
}

func TestGetMCPReadsExplicitBooleans(t *testing.T) {
	path := writeConfig(t, `[mcp]
enabled = false
auto_inject = false
`)
	requireLoad(t, path)

	got := config.GetMCP()
	if got.Enabled || got.AutoInject {
		t.Fatalf("mcp = %+v, want both disabled", got)
	}
}

func TestGetBridgeFeishuBotE2EForProfile(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.profiles.default.bot_e2e]
sender_open_ids = ["ou_javis", "", "ou_javis", "ou_runner"]
sender_roles = { "ou_shared" = "shared", "ou_bad" = "unauthorized", "ou_upper" = "OWNER" }
`)
	requireLoad(t, path)

	got := config.GetBridgeFeishuBotE2EForProfile("default")
	if fmt.Sprint(got.SenderOpenIDs) != "[ou_javis ou_runner]" {
		t.Fatalf("bot E2E sender open ids = %v, want normalized allowlist", got.SenderOpenIDs)
	}
	if got.SenderRoles["ou_shared"] != "shared" {
		t.Fatalf("sender_roles[ou_shared] = %q, want shared, got %v", got.SenderRoles["ou_shared"], got.SenderRoles)
	}
	// Duplicate/invalid roles are preserved verbatim so the bridge can reject
	// them loudly at routing time instead of silently treating them as owner.
	if got.SenderRoles["ou_upper"] != "owner" {
		t.Fatalf("sender_roles[ou_upper] = %q, want lowercased owner", got.SenderRoles["ou_upper"])
	}
	if got.SenderRoles["ou_bad"] != "unauthorized" {
		t.Fatalf("sender_roles[ou_bad] = %q, want preserved invalid role for fail-closed rejection", got.SenderRoles["ou_bad"])
	}
	if inherited := config.GetBridgeFeishuBotE2EForProfile("other"); len(inherited.SenderOpenIDs) != 0 {
		t.Fatalf("other profile bot E2E config = %+v, want empty without installation fallback", inherited)
	}
}

func TestParseSenderRolesCLI(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "entries",
			raw:  "ou_a=shared,ou_b=owner",
			want: map[string]string{"ou_a": "shared", "ou_b": "owner"},
		},
		{
			name: "lowercase normalization",
			raw:  "ou_a=SHARED",
			want: map[string]string{"ou_a": "shared"},
		},
		{
			name: "JSON map",
			raw:  `{"ou_a":"shared"}`,
			want: map[string]string{"ou_a": "shared"},
		},
		{
			name: "empty",
			raw:  "",
			want: map[string]string{},
		},
		{
			name:    "malformed entry",
			raw:     "ou_a",
			wantErr: true,
		},
		{
			name:    "malformed JSON",
			raw:     `{"ou_a":`,
			wantErr: true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := config.ParseSenderRolesCLI(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseSenderRolesCLI(%q) = %v, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSenderRolesCLI(%q): %v", tt.raw, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParseSenderRolesCLI(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Fatalf("ParseSenderRolesCLI(%q)[%q] = %q, want %q", tt.raw, k, got[k], v)
				}
			}
		})
	}
}

func TestGetIsolationDefaultsDisabled(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)

	got, err := config.GetIsolation()
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Backend != "auto" || got.UIDRangeStart != 700000 || got.GCIdleDays != 0 {
		t.Fatalf("isolation = %+v", got)
	}
	if !filepath.IsAbs(got.HelperSocket) || !filepath.IsAbs(got.MappingDB) {
		t.Fatalf("isolation paths must be absolute: %+v", got)
	}
}

func TestGetIsolationReadsAndValidates(t *testing.T) {
	path := writeConfig(t, `[isolation]
enabled = true
backend = "linux-dac"
uid_range_start = 710000
uid_range_end = 719999
gc_idle_days = 30
helper_socket = "/tmp/amux-helper.sock"
mapping_db = "/tmp/amux-mappings.db"
principal_root = "/tmp/amux-principals"
global_env = ["GLM_API_KEY=shared-key", "HTTP_PROXY=http://proxy.internal:8080"]

[isolation.hardening]
pid_namespace = false
mount_namespace = false
seccomp = false
`)
	requireLoad(t, path)

	got, err := config.GetIsolation()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Backend != "linux-dac" || got.UIDRangeStart != 710000 || got.UIDRangeEnd != 719999 {
		t.Fatalf("isolation = %+v", got)
	}
	if want := []string{"GLM_API_KEY=shared-key", "HTTP_PROXY=http://proxy.internal:8080"}; !slices.Equal(got.GlobalEnv, want) {
		t.Fatalf("global env = %q, want %q", got.GlobalEnv, want)
	}
	if got.Hardening.PIDNamespace || got.Hardening.MountNamespace || got.Hardening.Seccomp {
		t.Fatalf("hardening = %+v", got.Hardening)
	}
}

func TestGetIsolationRejectsInvalidGlobalEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
	}{
		{name: "missing equals", env: `['GLM_API_KEY']`},
		{name: "invalid name", env: `['GLM-API-KEY=value']`},
		{name: "duplicate", env: `['GLM_API_KEY=first', 'GLM_API_KEY=second']`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, "[isolation]\nglobal_env = "+tc.env+"\n")
			requireLoad(t, path)
			if _, err := config.GetIsolation(); err == nil {
				t.Fatalf("GetIsolation accepted global_env %s", tc.env)
			}
		})
	}
}

func TestValidateIsolationDoesNotExposeInvalidGlobalEnvValue(t *testing.T) {
	const secret = "must-not-appear"
	err := config.ValidateIsolation(config.IsolationConfig{
		Backend:   "auto",
		GlobalEnv: []string{"INVALID-NAME=" + secret},
	})
	if err == nil {
		t.Fatal("ValidateIsolation accepted an invalid global env name")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("ValidateIsolation exposed global env value: %v", err)
	}
}

func TestActiveIsolationGlobalEnvRequiresEnabledNonPassthrough(t *testing.T) {
	base := config.IsolationConfig{Backend: "auto", GlobalEnv: []string{"GLM_API_KEY=shared"}}
	if got := config.ActiveIsolationGlobalEnv(base); len(got) != 0 {
		t.Fatalf("disabled global env = %v, want none", got)
	}
	base.Enabled = true
	if got := config.ActiveIsolationGlobalEnv(base); !slices.Equal(got, base.GlobalEnv) {
		t.Fatalf("active global env = %v, want %v", got, base.GlobalEnv)
	}
	base.Backend = "passthrough"
	if got := config.ActiveIsolationGlobalEnv(base); len(got) != 0 {
		t.Fatalf("passthrough global env = %v, want none", got)
	}
}

func TestLoadIsolationFromReadsAnotherHomeWithoutTouchingGlobalViper(t *testing.T) {
	global := writeConfig(t, `[isolation]
enabled = false
`)
	requireLoad(t, global)

	service := writeConfig(t, `[isolation]
enabled = true

[isolation.hardening]
pid_namespace = true
mount_namespace = false
seccomp = false
`)
	got, err := config.LoadIsolationFrom(service)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Hardening.PIDNamespace || got.Hardening.MountNamespace || got.Hardening.Seccomp {
		t.Fatalf("service hardening = %+v", got.Hardening)
	}
	// The caller's own config must be untouched by reading someone else's.
	current, err := config.GetIsolation()
	if err != nil {
		t.Fatal(err)
	}
	if current.Enabled {
		t.Fatal("reading a service config leaked into the process-global config")
	}
}

func TestLoadIsolationFromMissingFileFallsBackToDefaults(t *testing.T) {
	got, err := config.LoadIsolationFrom(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("missing config must stay disabled")
	}
	if !got.Hardening.PIDNamespace || !got.Hardening.MountNamespace || !got.Hardening.Seccomp {
		t.Fatalf("hardening defaults = %+v", got.Hardening)
	}
}

func TestGetIsolationRejectsInvalidValues(t *testing.T) {
	path := writeConfig(t, `[isolation]
backend = "container"
uid_range_start = 1000
gc_idle_days = -1
`)
	requireLoad(t, path)
	if _, err := config.GetIsolation(); err == nil {
		t.Fatal("expected invalid isolation config")
	}
}

func TestLegacyOrchestratorAndSandboxAreIgnored(t *testing.T) {
	path := writeConfig(t, `[orchestrator]
address = "127.0.0.1:7777"

[sandbox]
id = "old-id"
name = "old-name"
`)
	requireLoad(t, path)

	if got := config.GetHub().Addr; got != "" {
		t.Fatalf("legacy hub addr = %q, want ignored", got)
	}
	rt := config.GetRuntime()
	if rt.ID == "old-id" || rt.Name == "old-name" {
		t.Fatalf("legacy runtime was used: %+v", rt)
	}
}

func TestGetBridgeFeishuPrompt(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.prompt]
shared_thread_enabled = false
topic_root_enabled = false
shared_thread_template_file = "~/.amux/prompts/feishu.md"
`)
	requireLoad(t, path)

	got := config.GetBridgeFeishuPrompt()
	if got.SharedThreadEnabled == nil || *got.SharedThreadEnabled {
		t.Fatalf("SharedThreadEnabled = %v, want false pointer", got.SharedThreadEnabled)
	}
	if got.TopicRootEnabled {
		t.Fatal("TopicRootEnabled = true, want false")
	}
	if got.SharedThreadTemplateFile != "~/.amux/prompts/feishu.md" {
		t.Fatalf("SharedThreadTemplateFile = %q, want configured path", got.SharedThreadTemplateFile)
	}
}

func TestGetBridgeFeishuPromptDefaults(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)

	got := config.GetBridgeFeishuPrompt()
	if got.SharedThreadEnabled == nil || *got.SharedThreadEnabled {
		t.Fatalf("SharedThreadEnabled = %v, want false pointer", got.SharedThreadEnabled)
	}
	if !got.TopicRootEnabled {
		t.Fatal("TopicRootEnabled = false, want true")
	}
	if got.SharedThreadTemplateFile != "" {
		t.Fatalf("SharedThreadTemplateFile = %q, want empty", got.SharedThreadTemplateFile)
	}
}

func TestGetBridgeFeishuBotToBotDefaults(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)

	got := config.GetBridgeFeishuBotToBotForProfile("default")
	if got.Enabled || got.MaxTurnsPerThread != 10 {
		t.Fatalf("bot_to_bot defaults = %+v", got)
	}
}

func TestGetBridgeFeishuBotToBotForProfileOverridesGlobal(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.bot_to_bot]
enabled = true
max_turns_per_thread = 7

[bridge.feishu.profiles.reviewer.bot_to_bot]
enabled = false
max_turns_per_thread = 2
`)
	requireLoad(t, path)

	global := config.GetBridgeFeishuBotToBotForProfile("default")
	if !global.Enabled || global.MaxTurnsPerThread != 7 {
		t.Fatalf("global bot_to_bot = %+v", global)
	}
	override := config.GetBridgeFeishuBotToBotForProfile("reviewer")
	if override.Enabled || override.MaxTurnsPerThread != 2 {
		t.Fatalf("profile bot_to_bot = %+v", override)
	}
}

func TestGetBridgeFeishuBotToBotRejectsNonPositiveTurnLimit(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.bot_to_bot]
enabled = true
max_turns_per_thread = 0
`)
	requireLoad(t, path)

	if got := config.GetBridgeFeishuBotToBotForProfile("default").MaxTurnsPerThread; got != 10 {
		t.Fatalf("max turns = %d, want 10", got)
	}
}

func TestGetBridgeFeishuBotToBotDefaultProfileCanOverrideGlobal(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.bot_to_bot]
enabled = false

[bridge.feishu.profiles.default.bot_to_bot]
enabled = true
`)
	requireLoad(t, path)

	global := config.GetBridgeFeishuBotToBotForProfile("other")
	if global.Enabled {
		t.Fatalf("explicit global bot_to_bot disable = %+v", global)
	}
	got := config.GetBridgeFeishuBotToBotForProfile("default")
	if !got.Enabled {
		t.Fatalf("default profile bot_to_bot = %+v", got)
	}
}

func TestGetBridgeFeishuDocumentCommentsDefaultsDisabled(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)

	if got := config.GetBridgeFeishuDocumentComments(); got.Enabled {
		t.Fatalf("document comments = %+v, want disabled", got)
	}
}

func TestGetBridgeFeishuDocumentCommentsReadsEnabled(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.document_comments]
enabled = true
`)
	requireLoad(t, path)

	if got := config.GetBridgeFeishuDocumentComments(); !got.Enabled {
		t.Fatalf("document comments = %+v, want enabled", got)
	}
}

func TestGetBridgeFeishuDocumentCommentsForProfileOverridesGlobal(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.document_comments]
enabled = true

[bridge.feishu.profiles.disabled_bot.document_comments]
enabled = false
agent_profile_ref = "team/reviewer"
model_ref = "Example-Model"
`)
	requireLoad(t, path)

	got := config.GetBridgeFeishuDocumentCommentsForProfile("disabled_bot")
	if got.Enabled || got.AgentProfileRef != "team/reviewer" || got.ModelRef != "Example-Model" {
		t.Fatalf("disabled_bot document comments = %+v, want profile execution override", got)
	}
	if got := config.GetBridgeFeishuDocumentCommentsForProfile("inheriting_bot"); !got.Enabled || got.AgentProfileRef != "" || got.ModelRef != "" {
		t.Fatalf("inheriting_bot document comments = %+v, want inherited global true", got)
	}
}

func TestGetBridgeFeishuGroupWatchDefaultsDisabled(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)

	got := config.GetBridgeFeishuGroupWatch()
	if got.Enabled || len(got.ChatIDs) != 0 {
		t.Fatalf("group_watch = %+v, want disabled with empty chat allowlist", got)
	}
	if got.PollIntervalSeconds != 15 || got.LookbackSeconds != 60 {
		t.Fatalf("group_watch intervals = %d/%d, want 15/60", got.PollIntervalSeconds, got.LookbackSeconds)
	}
	if got.MaxActiveSessionsPerChat != 50 || got.IdleTimeoutSeconds != 10800 {
		t.Fatalf("group_watch capacity/idle = %d/%d, want 50/10800", got.MaxActiveSessionsPerChat, got.IdleTimeoutSeconds)
	}
	if fmt.Sprint(got.SenderTypes) != "[user app]" {
		t.Fatalf("group_watch sender types = %v, want [user app]", got.SenderTypes)
	}
}

func TestGetBridgeFeishuGroupWatchForProfileOverridesFields(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.group_watch]
enabled = true
chat_ids = ["oc_global"]
sender_types = ["user"]
poll_interval_seconds = 20
lookback_seconds = 90
max_active_sessions_per_chat = 80
idle_timeout_seconds = 14400

[bridge.feishu.profiles.group_watch.group_watch]
chat_ids = ["oc_alarm", "oc_incident", "oc_alarm"]
sender_types = ["APP", "user", "invalid"]
poll_interval_seconds = 10
max_active_sessions_per_chat = 25
idle_timeout_seconds = 7200
agent_profile_ref = "builtin/codex"
model_ref = "custom/example-model"
`)
	requireLoad(t, path)

	got := config.GetBridgeFeishuGroupWatchForProfile("group_watch")
	if !got.Enabled {
		t.Fatal("profile group_watch Enabled = false, want inherited true")
	}
	if fmt.Sprint(got.ChatIDs) != "[oc_alarm oc_incident]" {
		t.Fatalf("profile group_watch chat ids = %v, want normalized override", got.ChatIDs)
	}
	if fmt.Sprint(got.SenderTypes) != "[app user]" {
		t.Fatalf("profile group_watch sender types = %v, want supported normalized override", got.SenderTypes)
	}
	if got.PollIntervalSeconds != 10 || got.LookbackSeconds != 90 {
		t.Fatalf("profile group_watch intervals = %d/%d, want 10/inherited 90", got.PollIntervalSeconds, got.LookbackSeconds)
	}
	if got.MaxActiveSessionsPerChat != 25 || got.IdleTimeoutSeconds != 7200 {
		t.Fatalf("profile group_watch capacity/idle = %d/%d, want 25/7200", got.MaxActiveSessionsPerChat, got.IdleTimeoutSeconds)
	}
	if got.AgentProfileRef != "builtin/codex" || got.ModelRef != "custom/example-model" {
		t.Fatalf("profile group_watch execution = %q/%q, want custom expert/model", got.AgentProfileRef, got.ModelRef)
	}
}

func TestValidateBridgeFeishuGroupWatchInteractionRejectsSingleton(t *testing.T) {
	groupWatch := config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"oc_alarm"}}
	if err := config.ValidateBridgeFeishuGroupWatchInteraction(groupWatch, config.FeishuInteractionThread); err != nil {
		t.Fatalf("thread compatibility: %v", err)
	}
	err := config.ValidateBridgeFeishuGroupWatchInteraction(groupWatch, config.FeishuInteractionSingleton)
	if err == nil || !strings.Contains(err.Error(), "群值守") || !strings.Contains(err.Error(), "每个话题独立") {
		t.Fatalf("singleton compatibility error = %v, want actionable 群值守 error", err)
	}
}

func TestGetBridgeFeishuWorkspaceDefaults(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.workspace_defaults]
agent = "codex"
target = "local"
cwd = "/repo/amux"
model = "DeepSeek-V4-Flash"
args = ["--yolo", "--profile", "fast"]
idle_timeout_seconds = 180
`)
	requireLoad(t, path)

	got := config.GetBridgeFeishuWorkspaceDefaults()
	if got.Agent != "codex" || got.Target != "local" || got.Cwd != "/repo/amux" || got.Model != "DeepSeek-V4-Flash" || got.IdleTimeoutSeconds != 180 {
		t.Fatalf("workspace defaults = %+v, want codex/local//repo/amux/DeepSeek-V4-Flash/180", got)
	}
	if len(got.Args) != 3 || got.Args[0] != "--yolo" || got.Args[1] != "--profile" || got.Args[2] != "fast" {
		t.Fatalf("Args = %#v, want configured string slice", got.Args)
	}
}

func TestGetBridgeFeishuWorkspaceDefaultsForProfileOverridesGlobal(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.workspace_defaults]
agent = "coco"
target = "local"
cwd = "/repo/global"
model = "global-model"
args = ["--global"]

[bridge.feishu.profiles.coder.workspace_defaults]
agent = "codex"
model = "coder-model"
args = ["--yolo"]
idle_timeout_seconds = 300
`)
	requireLoad(t, path)

	got := config.GetBridgeFeishuWorkspaceDefaultsForProfile("coder")
	if got.Agent != "codex" || got.Target != "local" || got.Cwd != "/repo/global" || got.Model != "coder-model" || got.IdleTimeoutSeconds != 300 {
		t.Fatalf("profile workspace defaults = %+v, want profile agent/args/idle timeout with global target/cwd", got)
	}
	if len(got.Args) != 1 || got.Args[0] != "--yolo" {
		t.Fatalf("Args = %#v, want profile override", got.Args)
	}
}

func TestGetBridgeFeishuWorkspaceDefaultsEmptyConfig(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)

	got := config.GetBridgeFeishuWorkspaceDefaults()
	if got.Agent != "" || got.Target != "" || got.Cwd != "" || got.Model != "" || got.IdleTimeoutSeconds != 0 || len(got.Args) != 0 {
		t.Fatalf("workspace defaults = %+v, want zero values", got)
	}
}

func TestGetBridgeFeishuInteractionModeDefaultsToThread(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)

	got, err := config.GetBridgeFeishuInteractionMode("bot_a")
	if err != nil {
		t.Fatalf("GetBridgeFeishuInteractionMode: %v", err)
	}
	if got != config.FeishuInteractionThread {
		t.Fatalf("mode = %q, want %q", got, config.FeishuInteractionThread)
	}
}

func TestGetBridgeFeishuInteractionModeReadsSingleton(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.profiles.bot_a.interaction]
mode = "singleton"
`)
	requireLoad(t, path)

	got, err := config.GetBridgeFeishuInteractionMode("bot_a")
	if err != nil {
		t.Fatalf("GetBridgeFeishuInteractionMode: %v", err)
	}
	if got != config.FeishuInteractionSingleton {
		t.Fatalf("mode = %q, want %q", got, config.FeishuInteractionSingleton)
	}
}

func TestGetBridgeFeishuInteractionModeRejectsInvalidMode(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.profiles.bot_a.interaction]
mode = "bot-session"
`)
	requireLoad(t, path)

	if _, err := config.GetBridgeFeishuInteractionMode("bot_a"); err == nil {
		t.Fatal("expected invalid interaction mode error")
	}
}

func TestSetBridgeFeishuInteractionModeWritesAndActivates(t *testing.T) {
	path := writeConfig(t, `[hub]
addr = "127.0.0.1:7100"
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	if err := config.SetBridgeFeishuInteractionMode("bot_a", config.FeishuInteractionSingleton); err != nil {
		t.Fatalf("SetBridgeFeishuInteractionMode: %v", err)
	}
	got, err := config.GetBridgeFeishuInteractionMode("bot_a")
	if err != nil {
		t.Fatalf("GetBridgeFeishuInteractionMode: %v", err)
	}
	if got != config.FeishuInteractionSingleton {
		t.Fatalf("mode = %q, want %q", got, config.FeishuInteractionSingleton)
	}

	viper.Reset()
	if err := config.Load(path); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	got, err = config.GetBridgeFeishuInteractionMode("bot_a")
	if err != nil {
		t.Fatalf("GetBridgeFeishuInteractionMode after reload: %v", err)
	}
	if got != config.FeishuInteractionSingleton {
		t.Fatalf("mode after reload = %q, want %q", got, config.FeishuInteractionSingleton)
	}
}

func TestBridgeFeishuThinkingProcessDefaultsCollapsed(t *testing.T) {
	path := writeConfig(t, "")
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	for name, got := range map[string]string{
		"assistant":           config.GetBridgeFeishuPresentation().ThinkingProcess,
		"profile assistant":   config.GetBridgeFeishuPresentationForProfile("bot_a").ThinkingProcess,
		"group watch":         config.GetBridgeFeishuGroupWatch().ThinkingProcess,
		"profile group watch": config.GetBridgeFeishuGroupWatchForProfile("bot_a").ThinkingProcess,
	} {
		if got != config.DefaultFeishuThinkingProcess {
			t.Fatalf("%s thinking process = %q, want default %q", name, got, config.DefaultFeishuThinkingProcess)
		}
	}
}

func TestBridgeFeishuThinkingProcessDefaultsAndProfileOverride(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.presentation]
thinking_process = "collapsed"

[bridge.feishu.group_watch]
thinking_process = "hidden"

[bridge.feishu.profiles.bot_a.presentation]
thinking_process = "hidden"

[bridge.feishu.profiles.bot_a.group_watch]
thinking_process = "collapsed"
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	if got := config.GetBridgeFeishuPresentationForProfile("bot_a").ThinkingProcess; got != config.FeishuThinkingProcessHidden {
		t.Fatalf("bot_a assistant thinking process = %q, want hidden", got)
	}
	if got := config.GetBridgeFeishuPresentationForProfile("bot_b").ThinkingProcess; got != config.FeishuThinkingProcessCollapsed {
		t.Fatalf("bot_b assistant thinking process = %q, want inherited collapsed", got)
	}
	if got := config.GetBridgeFeishuGroupWatchForProfile("bot_a").ThinkingProcess; got != config.FeishuThinkingProcessCollapsed {
		t.Fatalf("bot_a group watch thinking process = %q, want collapsed", got)
	}
	if got := config.GetBridgeFeishuGroupWatchForProfile("bot_b").ThinkingProcess; got != config.FeishuThinkingProcessHidden {
		t.Fatalf("bot_b group watch thinking process = %q, want inherited hidden", got)
	}

	viper.Set("bridge.feishu.presentation.thinking_process", "invalid")
	viper.Set("bridge.feishu.group_watch.thinking_process", "")
	if got := config.GetBridgeFeishuPresentation().ThinkingProcess; got != config.FeishuThinkingProcessCollapsed {
		t.Fatalf("invalid assistant thinking process = %q, want collapsed fallback", got)
	}
	if got := config.GetBridgeFeishuGroupWatch().ThinkingProcess; got != config.FeishuThinkingProcessCollapsed {
		t.Fatalf("invalid group watch thinking process = %q, want collapsed fallback", got)
	}
}

func TestBridgeFeishuCompletionNotificationThresholdDefaultsAndProfileOverride(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.presentation]
completion_notification_after_seconds = 90

[bridge.feishu.profiles.bot_a.presentation]
completion_notification_after_seconds = 5

[bridge.feishu.profiles.bot_off.presentation]
completion_notification_after_seconds = 0
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	if got := config.GetBridgeFeishuPresentationForProfile("bot_a").CompletionNotificationAfterSeconds; got != 5 {
		t.Fatalf("bot_a completion threshold = %d, want 5", got)
	}
	if got := config.GetBridgeFeishuPresentationForProfile("bot_b").CompletionNotificationAfterSeconds; got != 90 {
		t.Fatalf("bot_b completion threshold = %d, want inherited 90", got)
	}
	if got := config.GetBridgeFeishuPresentationForProfile("bot_off").CompletionNotificationAfterSeconds; got != 0 {
		t.Fatalf("bot_off completion threshold = %d, want disabled", got)
	}

	viper.Reset()
	path = writeConfig(t, "")
	requireLoad(t, path)
	if got := config.GetBridgeFeishuPresentation().CompletionNotificationAfterSeconds; got != config.DefaultFeishuCompletionNotificationAfterSeconds {
		t.Fatalf("default completion threshold = %d, want %d", got, config.DefaultFeishuCompletionNotificationAfterSeconds)
	}
}

func TestSetBridgeFeishuThinkingProcessWritesAndActivates(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.presentation]
thinking_process = "expanded"
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	if err := config.SetBridgeFeishuThinkingProcess("bot_a", config.FeishuThinkingProcessCollapsed); err != nil {
		t.Fatalf("SetBridgeFeishuThinkingProcess: %v", err)
	}
	if got := config.GetBridgeFeishuPresentationForProfile("bot_a").ThinkingProcess; got != config.FeishuThinkingProcessCollapsed {
		t.Fatalf("active thinking process = %q, want collapsed", got)
	}

	viper.Reset()
	if err := config.Load(path); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := config.GetBridgeFeishuPresentationForProfile("bot_a").ThinkingProcess; got != config.FeishuThinkingProcessCollapsed {
		t.Fatalf("reloaded thinking process = %q, want collapsed", got)
	}
	if err := config.SetBridgeFeishuThinkingProcess("bot_a", "invalid"); err == nil {
		t.Fatal("invalid thinking process = nil error, want validation failure")
	}
}

func TestSetBridgeFeishuDocumentCommentsWritesAndActivates(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.document_comments]
enabled = true
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	if err := config.SetBridgeFeishuDocumentComments("bot_a", false); err != nil {
		t.Fatalf("SetBridgeFeishuDocumentComments: %v", err)
	}
	if got := config.GetBridgeFeishuDocumentCommentsForProfile("bot_a"); got.Enabled {
		t.Fatalf("document comments = %+v, want profile override false", got)
	}

	viper.Reset()
	if err := config.Load(path); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := config.GetBridgeFeishuDocumentCommentsForProfile("bot_a"); got.Enabled {
		t.Fatalf("document comments after reload = %+v, want profile override false", got)
	}
}

func TestSetBridgeFeishuBotToBotWritesAndActivates(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.bot_to_bot]
enabled = true
max_turns_per_thread = 7
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	if err := config.SetBridgeFeishuBotToBot("bot_a", false); err != nil {
		t.Fatalf("SetBridgeFeishuBotToBot: %v", err)
	}
	got := config.GetBridgeFeishuBotToBotForProfile("bot_a")
	if got.Enabled || got.MaxTurnsPerThread != 7 {
		t.Fatalf("bot_to_bot = %+v, want profile override false with inherited limit", got)
	}

	viper.Reset()
	if err := config.Load(path); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	got = config.GetBridgeFeishuBotToBotForProfile("bot_a")
	if got.Enabled || got.MaxTurnsPerThread != 7 {
		t.Fatalf("bot_to_bot after reload = %+v, want persisted false with inherited limit", got)
	}
}

func TestSetBridgeFeishuDocumentCommentsExecutionWritesAndActivates(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.profiles.bot_a.document_comments]
enabled = true
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	if err := config.SetBridgeFeishuDocumentCommentsExecution(
		"bot_a",
		"team/reviewer",
		"codex",
		"Example-Model",
		"high",
	); err != nil {
		t.Fatalf("SetBridgeFeishuDocumentCommentsExecution: %v", err)
	}
	got := config.GetBridgeFeishuDocumentCommentsForProfile("bot_a")
	if !got.Enabled || got.AgentProfileRef != "team/reviewer" || got.RuntimeRef != "codex" || got.ModelRef != "Example-Model" || got.ReasoningEffort != "high" {
		t.Fatalf("document comments = %+v, want enabled state plus execution override", got)
	}

	viper.Reset()
	if err := config.Load(path); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	got = config.GetBridgeFeishuDocumentCommentsForProfile("bot_a")
	if got.AgentProfileRef != "team/reviewer" || got.RuntimeRef != "codex" || got.ModelRef != "Example-Model" || got.ReasoningEffort != "high" {
		t.Fatalf("document comments after reload = %+v, want persisted execution override", got)
	}

	if err := config.SetBridgeFeishuDocumentCommentsExecution("bot_a", "", "", "", ""); err != nil {
		t.Fatalf("reset execution override: %v", err)
	}
	got = config.GetBridgeFeishuDocumentCommentsForProfile("bot_a")
	if got.AgentProfileRef != "" || got.RuntimeRef != "" || got.ModelRef != "" || got.ReasoningEffort != "" {
		t.Fatalf("document comments after reset = %+v, want inherited execution", got)
	}
}

func TestSetBridgeFeishuGroupWatchWritesCompleteProfileOverride(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.group_watch]
enabled = false
sender_types = ["app"]
poll_interval_seconds = 30
lookback_seconds = 120
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	want := config.BridgeFeishuGroupWatchConfig{
		Enabled:                  true,
		ChatIDs:                  []string{"oc_qa", "oc_alert", "oc_qa"},
		SenderTypes:              []string{"user", "app"},
		PollIntervalSeconds:      15,
		LookbackSeconds:          60,
		MaxActiveSessionsPerChat: 40,
		IdleTimeoutSeconds:       5400,
		AgentProfileRef:          "builtin/codex",
		RuntimeRef:               "codex",
		ModelRef:                 "custom/example-model",
		ReasoningEffort:          "high",
	}
	if err := config.SetBridgeFeishuGroupWatch("bot_a", want); err != nil {
		t.Fatalf("SetBridgeFeishuGroupWatch: %v", err)
	}
	assertGroupWatch := func(stage string) {
		t.Helper()
		got := config.GetBridgeFeishuGroupWatchForProfile("bot_a")
		if !got.Enabled || fmt.Sprint(got.ChatIDs) != "[oc_qa oc_alert]" ||
			fmt.Sprint(got.SenderTypes) != "[user app]" || got.PollIntervalSeconds != 15 || got.LookbackSeconds != 60 ||
			got.MaxActiveSessionsPerChat != 40 || got.IdleTimeoutSeconds != 5400 ||
			got.AgentProfileRef != "builtin/codex" || got.RuntimeRef != "codex" ||
			got.ModelRef != "custom/example-model" || got.ReasoningEffort != "high" {
			t.Fatalf("%s group watch = %+v, want normalized complete override", stage, got)
		}
	}
	assertGroupWatch("active")

	viper.Reset()
	if err := config.Load(path); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	assertGroupWatch("reloaded")
}

func TestSetBridgeFeishuGroupWatchExecutionPreservesTriggerConfig(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.profiles.bot_a.group_watch]
enabled = true
chat_ids = ["oc_alert"]
sender_types = ["user", "app"]
poll_interval_seconds = 15
lookback_seconds = 60
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	if err := config.SetBridgeFeishuGroupWatchExecution(
		"bot_a",
		"builtin/codex",
		"codex",
		"custom/example-model",
		"high",
		config.FeishuThinkingProcessCollapsed,
	); err != nil {
		t.Fatalf("SetBridgeFeishuGroupWatchExecution: %v", err)
	}
	assert := func(stage string) {
		t.Helper()
		got := config.GetBridgeFeishuGroupWatchForProfile("bot_a")
		if !got.Enabled || fmt.Sprint(got.ChatIDs) != "[oc_alert]" ||
			got.AgentProfileRef != "builtin/codex" || got.RuntimeRef != "codex" ||
			got.ModelRef != "custom/example-model" || got.ReasoningEffort != "high" ||
			got.ThinkingProcess != config.FeishuThinkingProcessCollapsed {
			t.Fatalf("%s group watch = %+v, want trigger config plus execution override", stage, got)
		}
	}
	assert("active")
	viper.Reset()
	if err := config.Load(path); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	assert("reloaded")

	if err := config.SetBridgeFeishuGroupWatchExecution("bot_a", "", "", "model-only", "", config.FeishuThinkingProcessExpanded); err == nil {
		t.Fatal("model-only group watch execution = nil error, want validation failure")
	}
}

func TestSetBridgeFeishuDocumentCommentsConcurrentProfilesPreserveAllOverrides(t *testing.T) {
	path := writeConfig(t, `[bridge.feishu.document_comments]
enabled = false
`)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	const profiles = 24
	start := make(chan struct{})
	errs := make(chan error, profiles)
	var wg sync.WaitGroup
	for i := 0; i < profiles; i++ {
		profile := fmt.Sprintf("bot_%02d", i)
		enabled := i%2 == 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- config.SetBridgeFeishuDocumentComments(profile, enabled)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SetBridgeFeishuDocumentComments: %v", err)
		}
	}

	viper.Reset()
	if err := config.Load(path); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	for i := 0; i < profiles; i++ {
		profile := fmt.Sprintf("bot_%02d", i)
		want := i%2 == 0
		if got := config.GetBridgeFeishuDocumentCommentsForProfile(profile).Enabled; got != want {
			t.Fatalf("%s enabled = %v, want %v", profile, got, want)
		}
	}
}

func TestSetBridgeFeishuDocumentCommentsHonorsCrossProcessConfigLock(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)
	t.Cleanup(viper.Reset)

	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("lock config: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- config.SetBridgeFeishuDocumentComments("bot_a", true)
	}()
	select {
	case err := <-done:
		t.Fatalf("config update ignored cross-process lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("unlock config: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetBridgeFeishuDocumentComments: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("config update remained blocked after lock release")
	}
}

func TestGetSessionReclaimFeishuDefaults(t *testing.T) {
	path := writeConfig(t, ``)
	requireLoad(t, path)

	cfg, err := config.GetSessionReclaim()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("Enabled default should be true")
	}
	if cfg.Feishu.SleepAfterSeconds != 172800 || cfg.Feishu.ArchiveAfterSeconds != 0 || cfg.Feishu.PruneAfterSeconds != 0 {
		t.Fatalf("feishu defaults wrong: %+v", cfg.Feishu)
	}
	if cfg.Local.SleepAfterSeconds != 0 || cfg.Local.ArchiveAfterSeconds != 0 || cfg.Local.PruneAfterSeconds != 0 {
		t.Fatalf("local defaults must be all 0: %+v", cfg.Local)
	}
}

func TestGetSessionReclaimPreservesExplicitArchiveAndPrune(t *testing.T) {
	path := writeConfig(t, `
[runtime.session_reclaim.feishu]
sleep_after_seconds = 172800
archive_after_seconds = 216000
prune_after_seconds = 432000
`)
	requireLoad(t, path)

	cfg, err := config.GetSessionReclaim()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Feishu.SleepAfterSeconds != 172800 || cfg.Feishu.ArchiveAfterSeconds != 216000 || cfg.Feishu.PruneAfterSeconds != 432000 {
		t.Fatalf("explicit reclamation policy changed: %+v", cfg.Feishu)
	}
}

func TestGetSessionReclaimOrderValidation(t *testing.T) {
	path := writeConfig(t, `
[runtime.session_reclaim.feishu]
sleep_after_seconds = 200000
archive_after_seconds = 100000
prune_after_seconds = 432000
`)
	requireLoad(t, path)

	if _, err := config.GetSessionReclaim(); err == nil {
		t.Fatal("expected error: sleep > archive")
	}
}

func TestGetSessionReclaimLegacyMapping(t *testing.T) {
	path := writeConfig(t, `
[runtime]
idle_timeout_seconds = 3600
`)
	requireLoad(t, path)

	cfg, err := config.GetSessionReclaim()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cfg.Local.SleepAfterSeconds != 3600 {
		t.Fatalf("legacy idle_timeout should map to local sleep: %+v", cfg.Local)
	}
}

func TestStructuredProviderNegativeMeansDefault(t *testing.T) {
	// Global negative → package default (900); a value above the 7-day cap is
	// clamped to it.
	path := writeConfig(t, `[provider.structured]
turn_idle_timeout_seconds = -1
turn_hard_cap_seconds = 100000000
`)
	requireLoad(t, path)
	got := config.GetStructuredProvider()
	if got.TurnIdleTimeoutSeconds != 900 {
		t.Fatalf("global negative turn idle = %d, want package default 900", got.TurnIdleTimeoutSeconds)
	}
	if got.TurnHardCapSeconds != 7*24*3600 {
		t.Fatalf("over-cap hard cap = %d, want clamp %d", got.TurnHardCapSeconds, 7*24*3600)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireLoad(t *testing.T, path string) {
	t.Helper()
	viper.Reset()
	if err := config.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}
