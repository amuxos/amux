package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const managedToolsBin = "/usr/local/libexec/amux/tools/current/bin"

func launchPATH(env []string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if path, ok := strings.CutPrefix(env[i], "PATH="); ok {
			return path
		}
	}
	return "/usr/local/bin:/usr/bin:/bin"
}

func withManagedToolsPATH(env []string) []string {
	dirs := filepath.SplitList(launchPATH(env))
	dirs = slices.DeleteFunc(dirs, func(dir string) bool { return dir == managedToolsBin })
	dirs = append([]string{managedToolsBin}, dirs...)
	return replaceLaunchEnv(env, "PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

func lookPathForPrincipal(name string, env []string, mapping AccountMapping) (string, error) {
	var denied *ExecAccessDenial
	for _, dir := range filepath.SplitList(launchPATH(env)) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if denial := CheckExecAccessForUID(candidate, mapping.UID, mapping.GID); denial != nil {
			if denied == nil {
				denied = denial
			}
			continue
		}
		return candidate, nil
	}
	if denied != nil {
		return "", fmt.Errorf("%w: %v", exec.ErrNotFound, denied)
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}
