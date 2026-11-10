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
