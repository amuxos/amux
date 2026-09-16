package adapter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureWorkspaceTrusted pre-accepts pi's per-project trust gate for cwd.
//
// pi only loads project-local resources (`.pi/settings.json`, `.pi/skills`,
// `.pi/extensions`) once the project is trusted — otherwise the agent falls
// back to the login/default model and provider-backed Agent Profiles never
// take effect.
//
// Trust state lives in the pi agent dir as `trust.json`, a JSON map of
// canonical cwd -> bool, the same store pi writes when a user accepts the
// project-trust prompt interactively. We resolve the path the same way pi
// does: $PI_CODING_AGENT_DIR/trust.json if PI_CODING_AGENT_DIR is set, else
// $HOME/.pi/agent/trust.json.
//
// The edit is surgical (one key in a JSON map) and atomic (tmp + rename), so
// concurrent pi trust writes are not corrupted. Best-effort: any error is
// returned for logging but must not block launch (the runtime treats it
// non-fatal).
func (a *PiAdapter) EnsureWorkspaceTrusted(cwd string, env []string) error {
	path := piTrustPath(env)
	if path == "" {
		return nil // no HOME/PI_CODING_AGENT_DIR to anchor the store
	}
	key := canonicalCwdKey(cwd)
	trust, err := readPiTrust(path)
	if err != nil {
		return err
	}
	if trust[key] {
		return nil
	}
	trust[key] = true
	return writePiTrustAtomic(path, trust)
}

// CleanupWorkspaceTrust removes cwd from the pi trust store when a session is
// archived, so the store does not grow unbounded. Best-effort.
func (a *PiAdapter) CleanupWorkspaceTrust(cwd string, env []string) error {
	path := piTrustPath(env)
	if path == "" {
		return nil
	}
	key := canonicalCwdKey(cwd)
	trust, err := readPiTrust(path)
	if err != nil {
		return err
	}
	if _, ok := trust[key]; !ok {
		return nil
	}
	delete(trust, key)
	return writePiTrustAtomic(path, trust)
}

// canonicalCwdKey mirrors pi's canonicalizePath(resolvePath(cwd)) used as the
// trust.json key.
func canonicalCwdKey(cwd string) string {
	key := cwd
	if rp, err := filepath.EvalSymlinks(cwd); err == nil && rp != "" {
		key = rp
	}
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	return key
}

// piTrustPath returns the pi agent trust store path for the given environment,
// or "" if it cannot be located. pi resolves its agent dir as
// $PI_CODING_AGENT_DIR when set, otherwise $HOME/.pi/agent.
func piTrustPath(env []string) string {
	if dir := envValue(env, "PI_CODING_AGENT_DIR"); dir != "" {
		return filepath.Join(dir, "trust.json")
	}
	if home := envValue(env, "HOME"); home != "" {
		return filepath.Join(home, ".pi", "agent", "trust.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".pi", "agent", "trust.json")
	}
	return ""
}

func readPiTrust(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		trust := map[string]bool{}
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &trust); err != nil {
				// Malformed trust file (user hand-edit / concurrent write):
				// start fresh rather than failing the launch.
				return map[string]bool{}, nil
			}
		}
		return trust, nil
	case os.IsNotExist(err):
		return map[string]bool{}, nil
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
}

func writePiTrustAtomic(path string, trust map[string]bool) error {
	data, err := json.MarshalIndent(trust, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
