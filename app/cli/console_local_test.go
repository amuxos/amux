package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/webconsole"
	"github.com/spf13/cobra"
)

func TestConsoleSettingsWhitelistAndCAS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AMUX_HOME", home)
	content := "[hub]\naddr='127.0.0.1:7100'\ntoken='private-hub-secret'\n[isolation]\nglobal_env=['PRIVATE_TOKEN=hidden-value']\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	b := &consoleBackend{}
	value, err := b.snapshotSettings()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "private-hub-secret") || strings.Contains(string(encoded), "hidden-value") || strings.Contains(string(encoded), "hub.token") {
		t.Fatalf("secret in snapshot: %s", encoded)
	}
	version := value.(map[string]any)["version"]
	request, _ := json.Marshal(map[string]any{"expected_version": version, "values": map[string]string{"maintenance.daily_cleanup.at": "06:20"}})
	updated, err := b.executeSettings("save", request)
	if err != nil {
		t.Fatal(err)
	}
	if updated.(map[string]any)["restart_required"] != true {
		t.Fatal("save did not communicate restart scope")
	}
	if _, err := b.executeSettings("save", request); !errors.Is(err, webconsole.ErrConflict) {
		t.Fatalf("stale version: %v", err)
	} else {
		consoleTestCode(t, err, "version_conflict")
	}
	for _, key := range []string{"hub.token", "isolation.global_env", "agents.codex.executable", "bridge.feishu.prompt.shared_thread_template_file"} {
		req, _ := json.Marshal(map[string]any{"expected_version": updated.(map[string]any)["version"], "values": map[string]string{key: "attacker"}})
		if _, err := b.executeSettings("save", req); !errors.Is(err, webconsole.ErrInvalid) {
			t.Fatalf("accepted dangerous key %s: %v", key, err)
		}
	}
	cfg, err := readCLIConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	token, _ := configValue(cfg, "hub.token")
	if token != "private-hub-secret" {
		t.Fatal("unrelated config changed")
	}
}
func TestConsoleRejectsRemoteListenersAndAgentCredentials(t *testing.T) {
	t.Setenv("AMUX_HOME", t.TempDir())
	t.Setenv("AMUX_ACTOR_TOKEN", "")
	t.Setenv("AMUX_ACTOR_TOKEN_FILE", "")
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	for _, addr := range []string{"0.0.0.0:7892", ":7892", "localhost:7892", "example.com:7892", "192.0.2.1:7892"} {
		if err := runConsole(cmd, addr); err == nil {
			t.Fatalf("accepted %s", addr)
		}
	}
	t.Setenv("AMUX_ACTOR_TOKEN", "agent-test-token")
	if err := runConsole(cmd, "127.0.0.1:0"); err == nil {
		t.Fatal("agent started operator console")
	}
}
func TestConsoleCommandCopyParity(t *testing.T) {
	for _, key := range []string{"console.short", "console.addr", "console.operator_required", "console.listen_invalid", "console.ready", "console.public_url", "console.duration", "console.password_stdin", "console.duration_invalid", "console.password_invalid", "console.remote_auth_required", "console.password_ready"} {
		for _, data := range [][]byte{commandEnglish, commandChinese} {
			var messages map[string]json.RawMessage
			if err := json.Unmarshal(data, &messages); err != nil {
				t.Fatal(err)
			}
			if len(messages[key]) == 0 {
				t.Fatalf("missing %s", key)
			}
		}
	}
}

func TestConsoleSettingsFollowConfigurationMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AMUX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[hub]\naddr='127.0.0.1:7100'\n[runtime.session_reclaim.local]\nsleep_after_seconds=7200\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b := &consoleBackend{}
	snapshot := func() (map[string]map[string]any, string) {
		t.Helper()
		value, err := b.snapshotSettings()
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(value)
		var decoded struct {
			Settings []map[string]any `json:"settings"`
			Version  string           `json:"version"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		rows := map[string]map[string]any{}
		for _, row := range decoded.Settings {
			rows[row["key"].(string)] = row
		}
		return rows, decoded.Version
	}
	rows, version := snapshot()
	for _, key := range []string{"hub.token", "isolation.global_env", "agents.codex.executable", "bridge.feishu.workspace_defaults.cwd"} {
		if _, ok := rows[key]; ok {
			t.Fatalf("%s must stay out of the browser", key)
		}
	}
	for key := range rows {
		if strings.ContainsAny(key, "<>") {
			t.Fatalf("templated key %s belongs to its own page", key)
		}
	}
	experimental := rows["bridge.feishu.experimental.steer_foreground"]
	if experimental == nil || experimental["experimental"] != true || experimental["writable"] != true || experimental["kind"] != "boolean" || experimental["value"] != "false" || experimental["inherited"] != true {
		t.Fatalf("experimental setting = %#v", experimental)
	}
	if label := experimental["section_label"].(map[string]any); label["en"] == label["zh-CN"] || label["en"] == "" {
		t.Fatalf("section label is not bilingual: %#v", label)
	}
	for _, key := range []string{"runtime.session_reclaim.enabled", "runtime.task_cleanup.enabled", "runtime.broker_reap.enabled", "provider.structured.turn_idle_timeout_seconds"} {
		if rows[key] == nil || rows[key]["writable"] != true {
			t.Fatalf("system feature %s is not editable: %#v", key, rows[key])
		}
	}
	if hub := rows["hub.addr"]; hub == nil || hub["writable"] != false || hub["value"] != "127.0.0.1:7100" {
		t.Fatalf("hub.addr = %#v", hub)
	}
	if mode := rows["bridge.feishu.presentation.thinking_process"]; mode["kind"] != "enum" || len(mode["choices"].([]any)) != 3 {
		t.Fatalf("enum setting = %#v", mode)
	}
	save := func(values map[string]string, reset ...string) (any, error) {
		request, _ := json.Marshal(map[string]any{"expected_version": version, "values": values, "reset": reset})
		return b.executeSettings("save", request)
	}
	for name, values := range map[string]map[string]string{
		"read only":     {"hub.addr": "127.0.0.1:9"},
		"bad boolean":   {"bridge.feishu.experimental.steer_foreground": "maybe"},
		"bad enum":      {"bridge.feishu.presentation.thinking_process": "loud"},
		"stage order":   {"runtime.session_reclaim.local.archive_after_seconds": "3600"},
		"stage minimum": {"runtime.session_reclaim.local.prune_after_seconds": "5"},
		"non-positive":  {"runtime.task_cleanup.sweep_interval_seconds": "0"},
	} {
		if _, err := save(values); !errors.Is(err, webconsole.ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	if _, err := save(map[string]string{"bridge.feishu.experimental.steer_foreground": "true", "runtime.session_reclaim.enabled": "true"}, "runtime.session_reclaim.local.sleep_after_seconds"); err != nil {
		t.Fatal(err)
	}
	rows, _ = snapshot()
	if rows["bridge.feishu.experimental.steer_foreground"]["value"] != "true" || rows["runtime.session_reclaim.enabled"]["value"] != "true" {
		t.Fatalf("saved values = %#v %#v", rows["bridge.feishu.experimental.steer_foreground"], rows["runtime.session_reclaim.enabled"])
	}
	if rows["runtime.session_reclaim.local.sleep_after_seconds"]["inherited"] != true {
		t.Fatal("reset did not restore the default")
	}
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "[runtime.session_reclaim.local]") {
		t.Fatalf("reset left an empty table:\n%s", data)
	}
	// Resetting every customized key leaves an empty file, which must still
	// accept a save against the version read from it.
	if err := os.WriteFile(filepath.Join(home, "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, version = snapshot()
	if _, err := save(map[string]string{"mcp.enabled": "false"}); err != nil {
		t.Fatalf("save over an empty file: %v", err)
	}
}
