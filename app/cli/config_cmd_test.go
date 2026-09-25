package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/config"
)

func replaceCLIConfigFileForTest(cfg cliConfigFile) error {
	return config.UpdateConfigFile(defaultConfigPath(), func(current map[string]any) error {
		clear(current)
		for key, value := range cfg {
			current[key] = value
		}
		return nil
	})
}

func TestConfigJSONPreservesTypedValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	path := filepath.Join(home, ".amux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[runtime]\nauto_start = true\nmax_sessions = 7\n"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"show", "--json"}, {"get", "runtime.auto_start", "--json"}} {
		cmd := newJSONCommandTestRoot(newConfigCmd())
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(append([]string{"config"}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("config %v invalid JSON %q: %v", args, out.String(), err)
		}
		if got["schema"] == "" {
			t.Fatalf("config %v missing schema: %#v", args, got)
		}
		if len(args) > 0 && args[0] == "get" {
			if value, ok := got["value"].(bool); !ok || !value {
				t.Fatalf("config get value = %#v, want boolean true", got["value"])
			}
		}
	}
}

func TestSystemConfigUsesCanonicalCommand(t *testing.T) {
	root := newRootCmd()
	canonical, _, err := root.Find([]string{"system", "config"})
	if err != nil || canonical.Name() != "config" {
		t.Fatalf("find system config: found=%v err=%v", canonical, err)
	}
	if canonical.Parent() == nil || canonical.Parent().Name() != "system" {
		t.Fatalf("system config parent = %v, want system", canonical.Parent())
	}
	assertRootCommandNotRegistered(t, root, "config")
	for _, child := range []string{"path", "show", "get", "set", "unset"} {
		found, _, err := root.Find([]string{"system", "config", child})
		if err != nil || found.Name() != child {
			t.Fatalf("missing system config %s: found=%v err=%v", child, found, err)
		}
	}
}

func TestConfigSetGetUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	set := newConfigCmd()
	set.SetArgs([]string{"set", "hub.addr", "127.0.0.1:7000"})
	if err := set.Execute(); err != nil {
		t.Fatalf("config set: %v", err)
	}

	var out bytes.Buffer
	get := newConfigCmd()
	get.SetOut(&out)
	get.SetArgs([]string{"get", "hub.addr"})
	if err := get.Execute(); err != nil {
		t.Fatalf("config get: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "127.0.0.1:7000" {
		t.Fatalf("config get output = %q", got)
	}

	unset := newConfigCmd()
	unset.SetArgs([]string{"unset", "hub.addr"})
	if err := unset.Execute(); err != nil {
		t.Fatalf("config unset: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "127.0.0.1:7000") {
		t.Fatalf("config still contains old value:\n%s", data)
	}
}

func TestValidateHubAddressAcceptsSupportedListenerForms(t *testing.T) {
	for _, address := range []string{":7100", "0.0.0.0:7100", "[::]:7100"} {
		t.Run(address, func(t *testing.T) {
			if err := validateHubAddress(address); err != nil {
				t.Fatalf("validateHubAddress(%q): %v", address, err)
			}
		})
	}
	for _, address := range []string{"hub.example.com", "hub.example.com:0", "hub.example.com:70000"} {
		t.Run("reject "+address, func(t *testing.T) {
			if err := validateHubAddress(address); err == nil {
				t.Fatalf("validateHubAddress(%q) succeeded, want validation error", address)
			}
		})
	}
}

func TestConfigSetPreservesExistingSections(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	path := filepath.Join(home, ".amux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[runtime]\nname = \"devbox\"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "hub.addr", "127.0.0.1:7000"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "[runtime]") || !strings.Contains(text, "name = 'devbox'") && !strings.Contains(text, "name = \"devbox\"") {
		t.Fatalf("runtime section not preserved:\n%s", text)
	}
}

func TestConfigSetRuntimeIDDefaultsNameToID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "runtime.id", "devbox"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set runtime.id: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"id = 'devbox'", "name = 'devbox'"} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestConfigSetRuntimeIDDoesNotOverrideExplicitName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	path := filepath.Join(home, ".amux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[runtime]\nname = \"pretty\"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "runtime.id", "devbox"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set runtime.id: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "name = 'pretty'") && !strings.Contains(text, "name = \"pretty\"") {
		t.Fatalf("runtime.name should be preserved:\n%s", text)
	}
}

func TestConfigSetRuntimeIdleTimeoutSeconds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "runtime.idle_timeout_seconds", "120"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set runtime idle timeout: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "idle_timeout_seconds = 120") {
		t.Fatalf("config missing idle_timeout_seconds:\n%s", text)
	}
}

func TestConfigSetIdleTimeoutRejectsTooSmallPositiveValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "runtime.idle_timeout_seconds", "30"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "0 or at least 60 seconds") {
		t.Fatalf("config set error = %v, want idle timeout validation", err)
	}
}

func TestConfigSetBridgeFeishuPromptValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	enabled := newConfigCmd()
	enabled.SetArgs([]string{"set", "bridge.feishu.prompt.shared_thread_enabled", "false"})
	if err := enabled.Execute(); err != nil {
		t.Fatalf("config set shared_thread_enabled: %v", err)
	}

	topicRoot := newConfigCmd()
	topicRoot.SetArgs([]string{"set", "bridge.feishu.prompt.topic_root_enabled", "false"})
	if err := topicRoot.Execute(); err != nil {
		t.Fatalf("config set topic_root_enabled: %v", err)
	}

	file := newConfigCmd()
	file.SetArgs([]string{"set", "bridge.feishu.prompt.shared_thread_template_file", "~/.amux/prompts/feishu.md"})
	if err := file.Execute(); err != nil {
		t.Fatalf("config set shared_thread_template_file: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"shared_thread_enabled = false",
		"topic_root_enabled = false",
		"shared_thread_template_file = '~/.amux/prompts/feishu.md'",
	} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestConfigSetBridgeFeishuThinkingProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "bridge.feishu.presentation.thinking_process", config.FeishuThinkingProcessCollapsed},
		{"set", "bridge.feishu.group_watch.thinking_process", config.FeishuThinkingProcessHidden},
		{"set", "bridge.feishu.profiles.coder.presentation.thinking_process", config.FeishuThinkingProcessHidden},
		{"set", "bridge.feishu.profiles.coder.group_watch.thinking_process", config.FeishuThinkingProcessExpanded},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"[bridge.feishu.presentation]",
		"thinking_process = 'collapsed'",
		"[bridge.feishu.group_watch]",
		"thinking_process = 'hidden'",
		"[bridge.feishu.profiles.coder.presentation]",
		"[bridge.feishu.profiles.coder.group_watch]",
	} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("thinking process config missing %q:\n%s", want, text)
		}
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.coder.presentation.thinking_process", "invalid"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "expanded") ||
		!strings.Contains(err.Error(), "collapsed") || !strings.Contains(err.Error(), "hidden") {
		t.Fatalf("invalid thinking process error = %v, want enum validation", err)
	}
}

func TestConfigSetBridgeFeishuCompletionNotificationThreshold(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "bridge.feishu.presentation.completion_notification_after_seconds", "90"},
		{"set", "bridge.feishu.profiles.coder.presentation.completion_notification_after_seconds", "0"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"completion_notification_after_seconds = 90",
		"completion_notification_after_seconds = 0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("completion notification config missing %q:\n%s", want, text)
		}
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.presentation.completion_notification_after_seconds", "--", "-1"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "non-negative integer") {
		t.Fatalf("negative completion notification threshold error = %v", err)
	}
}

func TestConfigSetBridgeFeishuDocumentCommentsEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.document_comments.enabled", "true"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set document comments enabled: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "enabled = true") {
		t.Fatalf("document comments config missing:\n%s", data)
	}
}

func TestConfigSetBridgeFeishuProfileGroupWatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "bridge.feishu.group_watch.chat_sender_types.oc_global", "app"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.enabled", "true"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.chat_ids", "oc_alarm oc_incident"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.sender_types", "user app"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.chat_sender_types.oc_alarm", "app"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.chat_sender_types.oc_incident", "user app"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.poll_interval_seconds", "10"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.lookback_seconds", "60"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.dedup.enabled", "true"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.dedup.window_seconds", "1800"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.max_active_sessions_per_chat", "50"},
		{"set", "bridge.feishu.profiles.group_watch.group_watch.idle_timeout_seconds", "10800"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"[bridge.feishu.group_watch.chat_sender_types]",
		"oc_global = ['app']",
		"[bridge.feishu.profiles.group_watch.group_watch]",
		"enabled = true",
		"chat_ids = ['oc_alarm', 'oc_incident']",
		"sender_types = ['user', 'app']",
		"[bridge.feishu.profiles.group_watch.group_watch.chat_sender_types]",
		"oc_alarm = ['app']",
		"oc_incident = ['user', 'app']",
		"poll_interval_seconds = 10",
		"lookback_seconds = 60",
		"[bridge.feishu.profiles.group_watch.group_watch.dedup]",
		"window_seconds = 1800",
		"max_active_sessions_per_chat = 50",
		"idle_timeout_seconds = 10800",
	} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("group_watch config missing %q:\n%s", want, text)
		}
	}
}

func TestConfigSetBridgeFeishuProfileBotE2E(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.default.bot_e2e.sender_open_ids", "ou_javis ou_javis ou_runner"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set bot E2E sender open ids: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"[bridge.feishu.profiles.default.bot_e2e]", "sender_open_ids = ['ou_javis', 'ou_runner']"} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("bot E2E config missing %q:\n%s", want, text)
		}
	}
}

func TestConfigSetBridgeFeishuProfileBotE2ESenderRoles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.default.bot_e2e.sender_roles", "ou_shared=shared,ou_owner=owner"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set bot E2E sender_roles: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "ou_shared") || !strings.Contains(text, "shared") || !strings.Contains(text, "ou_owner") {
		t.Fatalf("sender_roles config missing mapping:\n%s", text)
	}
	// Invalid role value must still be accepted by CLI (the bridge rejects it
	// loudly at routing time); malformed entry must fail.
	cmd2 := newConfigCmd()
	cmd2.SetArgs([]string{"set", "bridge.feishu.profiles.default.bot_e2e.sender_roles", "ou_bad_bot"})
	if err := cmd2.Execute(); err == nil || !strings.Contains(err.Error(), "must be") {
		t.Fatalf("set malformed sender_roles error = %v, want open_id=role validation", err)
	}
}

func TestConfigSetBridgeFeishuProfileGroupWatchExecution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	agentKey := "bridge.feishu.profiles.coder.group_watch.agent_profile_ref"
	runtimeKey := "bridge.feishu.profiles.coder.group_watch.runtime_ref"
	modelKey := "bridge.feishu.profiles.coder.group_watch.model_ref"
	reasoningKey := "bridge.feishu.profiles.coder.group_watch.reasoning_effort"

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", modelKey, "gpt-5.4"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires agent_profile_ref") {
		t.Fatalf("set model without agent error = %v, want dependency validation", err)
	}
	for _, args := range [][]string{
		{"set", agentKey, "builtin/codex"},
		{"set", runtimeKey, "codex"},
		{"set", modelKey, "gpt-5.4"},
		{"set", reasoningKey, "high"},
	} {
		cmd = newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"[bridge.feishu.profiles.coder.group_watch]", "builtin/codex", `runtime_ref = 'codex'`, "gpt-5.4", `reasoning_effort = 'high'`} {
		if !strings.Contains(text, want) {
			t.Fatalf("profile group watch execution config missing %q:\n%s", want, text)
		}
	}

	cmd = newConfigCmd()
	cmd.SetArgs([]string{"unset", agentKey})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unset execution fields first") {
		t.Fatalf("unset agent with model error = %v, want dependency validation", err)
	}
	cmd = newConfigCmd()
	cmd.SetArgs([]string{"set", reasoningKey, "future-effort"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dynamic native effort rejected: %v", err)
	}
	for _, args := range [][]string{{"set", reasoningKey, "high"}, {"set", runtimeKey, "claude"}} {
		cmd = newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	cmd = newConfigCmd()
	cmd.SetArgs([]string{"set", reasoningKey, "invalid"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("set unsupported effort error = %v, want runtime capability validation", err)
	}
}

func TestConfigSetBridgeFeishuProfileDocumentCommentsEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.coder.document_comments.enabled", "false"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set profile document comments enabled: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"[bridge.feishu.profiles.coder.document_comments]", "enabled = false"} {
		if !strings.Contains(text, want) {
			t.Fatalf("profile document comments config missing %q:\n%s", want, text)
		}
	}
}

func TestConfigSetBridgeFeishuProfileDocumentCommentsExecution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "bridge.feishu.profiles.coder.document_comments.agent_profile_ref", "builtin/codex"},
		{"set", "bridge.feishu.profiles.coder.document_comments.runtime_ref", "codex"},
		{"set", "bridge.feishu.profiles.coder.document_comments.model_ref", "gpt-5.4"},
		{"set", "bridge.feishu.profiles.coder.document_comments.reasoning_effort", "high"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"agent_profile_ref = ",
		"builtin/codex",
		"runtime_ref = ",
		"codex",
		"model_ref = ",
		"gpt-5.4",
		"reasoning_effort = ",
		"high",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("profile document execution config missing %q:\n%s", want, text)
		}
	}
}

func TestConfigDocumentCommentsModelRequiresAgentOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	repo := initTestProfilePackRepo(t)
	manifestPath := filepath.Join(repo, "dist", "agent-pack.manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["profiles"].(map[string]any)["coder"].(map[string]any)["supported_models"] = []string{"gpt-5-codex"}
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "declare model policy")
	runAgentPackCmd(t, "add", "team", repo)
	modelKey := "bridge.feishu.profiles.coder.document_comments.model_ref"
	agentKey := "bridge.feishu.profiles.coder.document_comments.agent_profile_ref"

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", modelKey, "example-model"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires agent_profile_ref") {
		t.Fatalf("set model without agent error = %v, want dependency validation", err)
	}
	for _, args := range [][]string{
		{"set", agentKey, "team/coder"},
		{"set", modelKey, "gpt-5-codex"},
	} {
		cmd = newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}
	cmd = newConfigCmd()
	cmd.SetArgs([]string{"unset", agentKey})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unset execution fields first") {
		t.Fatalf("unset agent with model error = %v, want dependency validation", err)
	}

	cmd = newConfigCmd()
	cmd.SetArgs([]string{"set", modelKey, "not-supported"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("set unsupported model error = %v, want supported_models validation", err)
	}

	cmd = newConfigCmd()
	cmd.SetArgs([]string{"set", agentKey, "missing/profile"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "is unavailable") {
		t.Fatalf("set unavailable profile error = %v, want registry validation", err)
	}
}

func TestConfigSetBridgeFeishuWorkspaceDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "bridge.feishu.workspace_defaults.agent", "codex"},
		{"set", "bridge.feishu.workspace_defaults.target", "local"},
		{"set", "bridge.feishu.workspace_defaults.cwd", "/repo/amux"},
		{"set", "bridge.feishu.workspace_defaults.model", "DeepSeek-V4-Flash"},
		{"set", "bridge.feishu.workspace_defaults.args", "--", "--yolo --profile fast"},
		{"set", "bridge.feishu.workspace_defaults.idle_timeout_seconds", "180"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"agent = 'codex'",
		"target = 'local'",
		"cwd = '/repo/amux'",
		"model = 'DeepSeek-V4-Flash'",
		"args = ['--yolo', '--profile', 'fast']",
		"idle_timeout_seconds = 180",
	} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestConfigSetBridgeFeishuBotToBot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "bridge.feishu.bot_to_bot.enabled", "true"},
		{"set", "bridge.feishu.bot_to_bot.max_turns_per_thread", "6"},
		{"set", "bridge.feishu.profiles.reviewer.bot_to_bot.enabled", "false"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"[bridge.feishu.bot_to_bot]", "enabled = true", "max_turns_per_thread = 6", "[bridge.feishu.profiles.reviewer.bot_to_bot]", "enabled = false"} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.bot_to_bot.max_turns_per_thread", "0"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("zero max turns error = %v", err)
	}

	cmd = newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.bot_to_bot.allowed_bot_open_ids", "ou-a"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unsupported config key") {
		t.Fatalf("removed allowlist key error = %v", err)
	}
}

func TestConfigSetBridgeFeishuProfileWorkspaceDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "bridge.feishu.profiles.coder.workspace_defaults.agent", "codex"},
		{"set", "bridge.feishu.profiles.coder.workspace_defaults.target", "devbox"},
		{"set", "bridge.feishu.profiles.coder.workspace_defaults.cwd", "/repo/coder"},
		{"set", "bridge.feishu.profiles.coder.workspace_defaults.model", "gpt-5-codex"},
		{"set", "bridge.feishu.profiles.coder.workspace_defaults.args", "--", "--yolo --profile fast"},
		{"set", "bridge.feishu.profiles.coder.workspace_defaults.idle_timeout_seconds", "240"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"[bridge.feishu.profiles.coder.workspace_defaults]",
		"agent = 'codex'",
		"target = 'devbox'",
		"cwd = '/repo/coder'",
		"model = 'gpt-5-codex'",
		"args = ['--yolo', '--profile', 'fast']",
		"idle_timeout_seconds = 240",
	} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestConfigSetBridgeFeishuProfileWorkspaceDefaultsRejectsUnsafeProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.bad/name.workspace_defaults.agent", "codex"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unsupported config key") {
		t.Fatalf("config set unsafe profile error = %v, want unsupported config key", err)
	}
}

func TestConfigSetFeishuProfileInteractionMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.bot_a.interaction.mode", "singleton"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set interaction mode: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"[bridge.feishu.profiles.bot_a.interaction]",
		"mode = 'singleton'",
	} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}

	bad := newConfigCmd()
	bad.SetArgs([]string{"set", "bridge.feishu.profiles.bot_a.interaction.mode", "bot-session"})
	if err := bad.Execute(); err == nil || !strings.Contains(err.Error(), "must be thread or singleton") {
		t.Fatalf("invalid interaction mode error = %v, want validation", err)
	}
}

func TestConfigSetRejectsSingletonWithGroupWatchProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "bridge.feishu.profiles.bot_a.group_watch.enabled", "true"},
		{"set", "bridge.feishu.profiles.bot_a.group_watch.chat_ids", "oc_alarm"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.bot_a.interaction.mode", "singleton"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "群值守") || !strings.Contains(err.Error(), "每个话题独立") {
		t.Fatalf("config set singleton error = %v, want actionable 群值守 incompatibility", err)
	}
	if text := readTestConfig(t, home); strings.Contains(text, "mode = 'singleton'") || strings.Contains(text, "mode = \"singleton\"") {
		t.Fatalf("config persisted incompatible singleton mode:\n%s", text)
	}
}

func TestConfigSetRejectsGroupWatchChatsForSingletonProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "bridge.feishu.profiles.bot_a.interaction.mode", "singleton"},
		{"set", "bridge.feishu.profiles.bot_a.group_watch.enabled", "true"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.bot_a.group_watch.chat_ids", "oc_alarm"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "群值守") || !strings.Contains(err.Error(), "每个话题独立") {
		t.Fatalf("config set group_watch chats error = %v, want actionable 群值守 incompatibility", err)
	}
	if text := readTestConfig(t, home); strings.Contains(text, "oc_alarm") {
		t.Fatalf("config persisted incompatible GroupWatch chat:\n%s", text)
	}
}

func TestConfigSetReadsAuthoritativeSingletonStateFromBridgeDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("bot_a", &bridge.Secrets{AppId: "cli_bot_a", AppSecret: "secret"}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	if err := bridge.SetBotSessionInDB(bridge.DefaultDBPath(), bridge.BotSession{
		BridgeId: "feishu:cli_bot_a", Enabled: true, TargetID: "local", TargetName: "local", SessionName: "main", AgentType: "codex",
	}); err != nil {
		t.Fatalf("SetBotSessionInDB: %v", err)
	}
	// Deliberately leave interaction.mode absent from config.toml: bridge.db is
	// the authoritative runtime state and must still block GroupWatch activation.
	for _, args := range [][]string{{"set", "bridge.feishu.profiles.bot_a.group_watch.enabled", "true"}} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.bot_a.group_watch.chat_ids", "oc_alarm"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "群值守") || !strings.Contains(err.Error(), "每个话题独立") {
		t.Fatalf("config set with DB singleton error = %v, want 群值守 incompatibility", err)
	}
}

func TestConfigSetCandidateSingletonOverridesCurrentThreadDBForValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("bot_a", &bridge.Secrets{AppId: "cli_bot_a", AppSecret: "secret"}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	if _, err := bridge.GetBotProfileStateFromDB(bridge.DefaultDBPath(), "feishu:cli_bot_a", config.FeishuInteractionThread); err != nil {
		t.Fatalf("seed thread DB state: %v", err)
	}
	for _, args := range [][]string{
		{"set", "bridge.feishu.profiles.bot_a.group_watch.enabled", "true"},
		{"set", "bridge.feishu.profiles.bot_a.group_watch.chat_ids", "oc_alarm"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.profiles.bot_a.interaction.mode", "singleton"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "群值守") {
		t.Fatalf("candidate singleton error = %v, want 群值守 incompatibility", err)
	}
}

func TestConfigSetGlobalGroupWatchChecksSecretOnlySingletonProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := bridge.SaveProfileSecrets("bot_hidden", &bridge.Secrets{AppId: "cli_hidden", AppSecret: "secret"}); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	if err := bridge.SetBotSessionInDB(bridge.DefaultDBPath(), bridge.BotSession{
		BridgeId: "feishu:cli_hidden", Enabled: true, TargetID: "local", TargetName: "local", SessionName: "main", AgentType: "codex",
	}); err != nil {
		t.Fatalf("SetBotSessionInDB: %v", err)
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.group_watch.enabled", "true"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("enable global GroupWatch before allowlist: %v", err)
	}
	cmd = newConfigCmd()
	cmd.SetArgs([]string{"set", "bridge.feishu.group_watch.chat_ids", "oc_alarm"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "bot_hidden") || !strings.Contains(err.Error(), "群值守") {
		t.Fatalf("global GroupWatch error = %v, want secret-only singleton profile incompatibility", err)
	}
}

func TestConfigSetRuntimeRegistryKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	set := func(k, v string) {
		t.Helper()
		cmd := newConfigCmd()
		cmd.SetArgs([]string{"set", k, v})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config set %s %s: %v", k, v, err)
		}
	}
	set("runtime.max_sessions", "4")
	set("runtime.max_concurrency", "2")
	set("runtime.labels.pool", "local-dev")
	set("hub.allowed_targets", "mac-A mac-B")

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"max_sessions = 4",                     // int, not quoted string
		"max_concurrency = 2",                  // int, not quoted string
		"[runtime.labels]",                     // nested label map
		"pool = 'local-dev'",                   // label value
		"allowed_targets = ['mac-A', 'mac-B']", // string slice
	} {
		if !strings.Contains(text, want) && !strings.Contains(text, strings.ReplaceAll(want, "'", "\"")) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}

	// A non-integer / negative capacity is rejected.
	for _, bad := range [][]string{
		{"set", "runtime.max_sessions", "notanint"},
		{"set", "runtime.max_sessions", "3.5"},
		{"set", "runtime.max_sessions", "--", "-1"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(bad)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "non-negative integer") {
			t.Fatalf("config %v error = %v, want non-negative integer", bad, err)
		}
	}
}

func TestConfigSetMCPKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")

	for _, args := range [][]string{
		{"set", "mcp.enabled", "false"},
		{"set", "mcp.auto_inject", "false"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"[mcp]",
		"enabled = false",
		"auto_inject = false",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}

	var out bytes.Buffer
	get := newConfigCmd()
	get.SetOut(&out)
	get.SetArgs([]string{"get", "mcp.enabled"})
	if err := get.Execute(); err != nil {
		t.Fatalf("config get mcp.enabled: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "false" {
		t.Fatalf("mcp.enabled get = %q, want false", got)
	}
}

func TestConfigSetDoesNotMigrateLegacySections(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	path := filepath.Join(home, ".amux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[orchestrator]\naddress = \"127.0.0.1:7777\"\n\n[sandbox]\nname = \"old\"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"set", "hub.addr", "127.0.0.1:7000"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "[runtime]") ||
		(!strings.Contains(text, "name = \"old\"") && !strings.Contains(text, "name = 'old'")) {
		t.Fatalf("legacy sections should be ignored, not migrated or deleted:\n%s", text)
	}
}

func TestConfigSetMemoryExperimentBoolean(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", t.TempDir())
	for _, value := range []string{"true", "false"} {
		cmd := newConfigCmd()
		cmd.SetArgs([]string{"set", "memory.enabled", value})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(defaultConfigPath())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "enabled = "+value+"\n") {
			t.Fatalf("not a TOML boolean: %s", data)
		}
		bad := newConfigCmd()
		bad.SetArgs([]string{"set", "memory.enabled", "maybe"})
		if err := bad.Execute(); err == nil || !strings.Contains(err.Error(), "must be true or false") {
			t.Fatalf("invalid boolean: %v", err)
		}
		after, err := os.ReadFile(defaultConfigPath())
		if err != nil || !bytes.Equal(data, after) {
			t.Fatalf("invalid value changed settings: %s, %v", after, err)
		}
	}
}
