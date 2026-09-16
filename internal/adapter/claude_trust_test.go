package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readClaudeCfg(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return cfg
}

func trustEntry(t *testing.T, cfg map[string]any, key string) map[string]any {
	t.Helper()
	projects, ok := cfg["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects missing or wrong type: %v", cfg["projects"])
	}
	entry, ok := projects[key].(map[string]any)
	if !ok {
		t.Fatalf("project entry %q missing: %v", key, projects)
	}
	return entry
}

func TestEnsureWorkspaceTrustedCreatesEntry(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "ws")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + home}

	a := &ClaudeAdapter{}
	if err := a.EnsureWorkspaceTrusted(cwd, env); err != nil {
		t.Fatalf("EnsureWorkspaceTrusted: %v", err)
	}

	cfg := readClaudeCfg(t, filepath.Join(home, ".claude.json"))
	// Claude keys trust by the symlink-resolved path.
	key, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		key = cwd
	}
	entry := trustEntry(t, cfg, key)
	if entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("hasTrustDialogAccepted = %v, want true", entry["hasTrustDialogAccepted"])
	}
}

func TestEnsureWorkspaceTrustedPreservesExistingConfig(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "ws")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	key, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		key = cwd
	}
	cfgPath := filepath.Join(home, ".claude.json")
	// Pre-existing config with unrelated fields and another trusted project.
	seed := map[string]any{
		"hasCompletedOnboarding": true,
		"theme":                  "dark",
		"projects": map[string]any{
			"/some/other/dir": map[string]any{
				"hasTrustDialogAccepted": true,
				"history":                []any{"x"},
			},
		},
	}
	data, _ := json.Marshal(seed)
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	a := &ClaudeAdapter{}
	if err := a.EnsureWorkspaceTrusted(cwd, []string{"HOME=" + home}); err != nil {
		t.Fatalf("EnsureWorkspaceTrusted: %v", err)
	}

	cfg := readClaudeCfg(t, cfgPath)
	if cfg["hasCompletedOnboarding"] != true || cfg["theme"] != "dark" {
		t.Fatalf("top-level fields not preserved: %v", cfg)
	}
	// New entry added.
	if e := trustEntry(t, cfg, key); e["hasTrustDialogAccepted"] != true {
		t.Fatalf("new trust entry missing: %v", e)
	}
	// Old entry preserved.
	old := trustEntry(t, cfg, "/some/other/dir")
	if old["hasTrustDialogAccepted"] != true {
		t.Fatalf("existing project entry clobbered: %v", old)
	}
}

func TestEnsureWorkspaceTrustedHonorsConfigDir(t *testing.T) {
	cfgDir := t.TempDir()
	home := t.TempDir()
	cwd := t.TempDir()
	env := []string{"HOME=" + home, "CLAUDE_CONFIG_DIR=" + cfgDir}

	a := &ClaudeAdapter{}
	if err := a.EnsureWorkspaceTrusted(cwd, env); err != nil {
		t.Fatalf("EnsureWorkspaceTrusted: %v", err)
	}

	if _, err := os.Stat(filepath.Join(cfgDir, ".claude.json")); err != nil {
		t.Fatalf("config not written to CLAUDE_CONFIG_DIR: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("config unexpectedly written under HOME: %v", err)
	}
}

func TestEnsureWorkspaceTrustedDoesNotClobberUnparseableConfig(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	cfgPath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(cfgPath, []byte("{not valid json"), 0600); err != nil {
		t.Fatal(err)
	}

	a := &ClaudeAdapter{}
	if err := a.EnsureWorkspaceTrusted(cwd, []string{"HOME=" + home}); err == nil {
		t.Fatal("expected error for unparseable config")
	}
	// Original content must be left intact.
	data, _ := os.ReadFile(cfgPath)
	if string(data) != "{not valid json" {
		t.Fatalf("unparseable config was modified: %q", data)
	}
}
