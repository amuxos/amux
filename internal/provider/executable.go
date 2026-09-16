package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveExecutableFromEnvPATH resolves a relative executable name using the
// last PATH entry in env, matching exec.Cmd's last-value-wins environment
// semantics. When env does not contain PATH, the caller may still delegate
// resolution to os/exec by using the returned name unchanged.
func ResolveExecutableFromEnvPATH(name string, env []string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || filepath.IsAbs(name) || strings.ContainsRune(name, filepath.Separator) {
		return name, nil
	}
	pathValue := ""
	hasExplicitPATH := false
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], "PATH=") {
			pathValue = strings.TrimPrefix(env[i], "PATH=")
			hasExplicitPATH = true
			break
		}
	}
	if !hasExplicitPATH {
		return name, nil
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable %q not found in launch env PATH", name)
}
