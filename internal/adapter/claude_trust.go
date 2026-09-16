package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureWorkspaceTrusted pre-accepts Claude Code's "Is this a project you
// trust?" dialog for cwd by writing hasTrustDialogAccepted into the config's
// per-project entry. Without this, an interactive `claude` launched in a fresh
// amux workspace blocks on the trust dialog: injected input is echoed back and
// the model is never invoked.
//
// The trust state lives in <config>/.claude.json keyed by the directory's real
// (symlink-resolved) path — the same key Claude Code itself uses. Config
// location follows Claude Code: $CLAUDE_CONFIG_DIR if set, else $HOME. Both are
// read from env so the path matches what the launched agent will see.
//
// Best-effort: any error is returned for logging but must not block launch.
func (a *ClaudeAdapter) EnsureWorkspaceTrusted(cwd string, env []string) error {
	cfgPath := claudeConfigPath(env)
	if cfgPath == "" {
		return nil // no HOME/CLAUDE_CONFIG_DIR to anchor the config
	}

	key := cwd
	if rp, err := filepath.EvalSymlinks(cwd); err == nil {
		key = rp
	}

	cfg := map[string]any{}
	data, err := os.ReadFile(cfgPath)
	switch {
	case err == nil:
		if len(data) > 0 {
			if uErr := json.Unmarshal(data, &cfg); uErr != nil {
				// Don't clobber a config we can't parse.
				return fmt.Errorf("parse %s: %w", cfgPath, uErr)
			}
		}
	case os.IsNotExist(err):
		// Fresh config; we'll create it below.
	default:
		return fmt.Errorf("read %s: %w", cfgPath, err)
	}

	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	entry, _ := projects[key].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	if accepted, ok := entry["hasTrustDialogAccepted"].(bool); ok && accepted {
		return nil // already trusted; avoid racing Claude's own writes
	}
	entry["hasTrustDialogAccepted"] = true
	projects[key] = entry
	cfg["projects"] = projects

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude config: %w", err)
	}
	if dir := filepath.Dir(cfgPath); dir != "" {
		_ = os.MkdirAll(dir, 0700)
	}
	// Write atomically so a concurrent Claude read never sees a partial file.
	tmp, err := os.CreateTemp(filepath.Dir(cfgPath), ".claude.json.*")
	if err != nil {
		return fmt.Errorf("temp claude config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write claude config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close claude config: %w", err)
	}
	_ = os.Chmod(tmpName, 0600)
	if err := os.Rename(tmpName, cfgPath); err != nil {
		return fmt.Errorf("replace %s: %w", cfgPath, err)
	}
	return nil
}

// claudeConfigPath returns the path to Claude Code's .claude.json for the given
// environment, or "" if it cannot be located.
func claudeConfigPath(env []string) string {
	if dir := envValue(env, "CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	if home := envValue(env, "HOME"); home != "" {
		return filepath.Join(home, ".claude.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".claude.json")
	}
	return ""
}

// CleanupWorkspaceTrust removes the trust entry for cwd from Claude Code's
// .claude.json. Called when a session is archived so the config does not grow
// unbounded. Best-effort.
func (a *ClaudeAdapter) CleanupWorkspaceTrust(cwd string, env []string) error {
	cfgPath := claudeConfigPath(env)
	if cfgPath == "" {
		return nil
	}

	key := cwd
	if rp, err := filepath.EvalSymlinks(cwd); err == nil {
		key = rp
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", cfgPath, err)
	}

	cfg := map[string]any{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil // can't parse, don't touch
		}
	}

	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		return nil
	}
	if _, ok := projects[key]; !ok {
		return nil // not present
	}
	delete(projects, key)
	cfg["projects"] = projects

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude config: %w", err)
	}
	if dir := filepath.Dir(cfgPath); dir != "" {
		_ = os.MkdirAll(dir, 0700)
	}
	tmp, err := os.CreateTemp(filepath.Dir(cfgPath), ".claude.json.*")
	if err != nil {
		return fmt.Errorf("temp claude config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write claude config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close claude config: %w", err)
	}
	_ = os.Chmod(tmpName, 0600)
	if err := os.Rename(tmpName, cfgPath); err != nil {
		return fmt.Errorf("replace %s: %w", cfgPath, err)
	}
	return nil
}

// envValue returns the value of key from a KEY=VALUE slice, last entry winning
// (matching how exec applies a duplicated environment).
func envValue(env []string, key string) string {
	prefix := key + "="
	val := ""
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			val = e[len(prefix):]
		}
	}
	return val
}
