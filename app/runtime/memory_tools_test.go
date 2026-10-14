package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/session"
)

func TestMemoryToolsMaterializeExecutableAndMinimalConfig(t *testing.T) {
	service := t.TempDir()
	binary := filepath.Join(service, "amux")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$AMUX_HOME\" \"$AMUX_HUB_ADDR\" \"$AMUX_ACTOR_TOKEN\" \"$AMUX_MEMORY_BINDING\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stage, err := stageMemoryTools(binary, "hub.example:7100")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	principal := t.TempDir()
	workspace, run, home := filepath.Join(principal, "workspace"), filepath.Join(principal, "run"), filepath.Join(principal, "home")
	for _, dir := range []string{workspace, run, home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	var bundle bytes.Buffer
	if err := isolation.WriteMaterializeBundle(&bundle, []isolation.BundleSource{{Source: stage, Prefix: "agent-config"}}); err != nil {
		t.Fatal(err)
	}
	if err := isolation.ApplyMaterializeBundle(&bundle, workspace, run, home); err != nil {
		t.Fatal(err)
	}
	toolHome := filepath.Join(run, "agent-config", "memory")
	projected := filepath.Join(toolHome, "bin", "amux")
	info, err := os.Stat(projected)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("projected executable: %v %v", info, err)
	}
	data, err := os.ReadFile(filepath.Join(toolHome, "config.toml"))
	if err != nil || !strings.Contains(string(data), `addr = "hub.example:7100"`) || strings.Contains(string(data), "token") {
		t.Fatalf("projected config: %s (%v)", data, err)
	}
	// Removing the service-side source proves execution no longer depends on
	// access to the service user's 0700 installation directory.
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	env := memoryToolEnvironment([]string{memoryprotocol.BindingEnv + "=principal:user", "AMUX_ACTOR_TOKEN=actor"}, toolHome, config.HubConfig{Addr: "hub.example:7100"})
	env = append(env, "PATH="+filepath.Dir(projected)+":/usr/bin:/bin", "HOME="+home)
	cmd := exec.Command("/bin/sh", "-c", "amux memory show MEMORY.md")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("projected CLI: %v: %s", err, out)
	}
	if string(out) != toolHome+"\nhub.example:7100\nactor\nprincipal:user\n" {
		t.Fatalf("CLI transport/scope environment = %s", out)
	}
}

func TestMemoryTaskCLIUsesInstallationEvenWithRestrictedPATH(t *testing.T) {
	s, _, _ := memoryTestServer(t)
	args, env, err := s.prepareAmuxTools(context.Background(), session.Snapshot{ID: "task", AgentType: "pi"}, nil, []string{"PATH=/unavailable", memoryprotocol.BindingEnv + "=global", "AMUX_ACTOR_TOKEN=task-actor"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 || !strings.HasPrefix(launchEnvValue(env, "PATH"), filepath.Join(s.amuxDir, "bin")+string(os.PathListSeparator)) || launchEnvValue(env, "AMUX_HOME") != s.amuxDir || launchEnvValue(env, "AMUX_ACTOR_TOKEN") != "task-actor" {
		t.Fatalf("task memory CLI env: %v %v", args, env)
	}
}

func TestMemoryToolsMissingExecutableIsExplicit(t *testing.T) {
	_, err := memoryExecutable(t.TempDir(), []string{"PATH=/nonexistent"})
	if err == nil || !strings.Contains(err.Error(), "memory tools unavailable") {
		t.Fatalf("missing CLI error = %v", err)
	}
}

func TestMemoryMCPConfigCarriesScopedTransportWithoutNativeHome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-mcp.json")
	env := []string{"HOME=/native", "CODEX_HOME=/private-codex", "SECRET=unrelated", "AMUX_HOME=/projected", "AMUX_HUB_ADDR=hub:7100", "AMUX_HUB_TOKEN=transport", "AMUX_ACTOR_TOKEN=actor", memoryprotocol.BindingEnv + "=principal:user"}
	if err := writeMemoryMCPConfig(path, "/projected/bin/amux", "session", env); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	server := config.Servers["amux"]
	if server.Command != "/projected/bin/amux" || server.Env["AMUX_ACTOR_TOKEN"] != "actor" || server.Env["AMUX_HUB_TOKEN"] != "transport" || server.Env[memoryprotocol.BindingEnv] != "principal:user" || server.Env["AMUX_SESSION_ID"] != "session" {
		t.Fatalf("MCP config = %s", data)
	}
	for _, key := range []string{"HOME", "CODEX_HOME", "SECRET"} {
		if _, ok := server.Env[key]; ok {
			t.Fatalf("unrelated environment %s projected", key)
		}
	}
}
