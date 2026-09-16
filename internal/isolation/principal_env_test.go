package isolation

import (
	"slices"
	"strings"
	"testing"
)

func envValue(t *testing.T, env []string, key string) (string, bool) {
	t.Helper()
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
	}
	return "", false
}

// The launch environment starts as the service user's own environment. Anything
// describing *that* user is wrong for a process running as the principal.
func TestPrincipalLaunchEnvRewritesIdentity(t *testing.T) {
	paths := PrincipalPaths{Home: "/var/lib/amux/principals/amux_ab/home"}
	env := principalLaunchEnv([]string{
		"HOME=/home/service",
		"USER=service",
		"LOGNAME=service",
		"PWD=/home/service",
	}, "amux_ab", paths, "/var/lib/amux/principals/amux_ab/workspace/exec_1")

	for key, want := range map[string]string{
		"HOME":            paths.Home,
		"USER":            "amux_ab",
		"LOGNAME":         "amux_ab",
		"PWD":             "/var/lib/amux/principals/amux_ab/workspace/exec_1",
		"XDG_CONFIG_HOME": paths.Home + "/.config",
		"XDG_CACHE_HOME":  paths.Home + "/.cache",
	} {
		got, ok := envValue(t, env, key)
		if !ok || got != want {
			t.Fatalf("%s = %q (present=%v), want %q", key, got, ok, want)
		}
	}
}

// These name resources owned by the service user: an ssh-agent socket, a session
// bus and a /run/user/<uid> directory the principal has no access to. Leaving
// them set only sends the agent at doors it cannot open.
func TestPrincipalLaunchEnvDropsServiceUserRuntimeRefs(t *testing.T) {
	env := principalLaunchEnv([]string{
		"SSH_AUTH_SOCK=/tmp/ssh-XXXX/agent.1",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus",
		"XDG_RUNTIME_DIR=/run/user/1001",
		"LANG=en_US.UTF-8",
	}, "amux_ab", PrincipalPaths{Home: "/principals/amux_ab/home"}, "/work")

	for _, key := range []string{"SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"} {
		if value, ok := envValue(t, env, key); ok {
			t.Fatalf("%s survived as %q", key, value)
		}
	}
	if value, ok := envValue(t, env, "LANG"); !ok || value != "en_US.UTF-8" {
		t.Fatalf("unrelated variable was disturbed: %q (present=%v)", value, ok)
	}
}

func TestPrincipalLaunchEnvKeepsOneEntryPerKey(t *testing.T) {
	env := principalLaunchEnv([]string{"HOME=/home/service", "USER=service"},
		"amux_ab", PrincipalPaths{Home: "/principals/amux_ab/home"}, "/work")

	for _, key := range []string{"HOME", "USER", "LOGNAME", "PWD", "AMUX_ISOLATION_BACKEND"} {
		count := 0
		for _, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s appears %d times, want exactly 1: %v", key, count, env)
		}
	}
}

// An empty working directory means the caller did not pin one; inventing a PWD
// would be worse than leaving the variable off.
func TestPrincipalLaunchEnvOmitsPWDWithoutWorkingDirectory(t *testing.T) {
	env := principalLaunchEnv([]string{"PWD=/home/service"},
		"amux_ab", PrincipalPaths{Home: "/principals/amux_ab/home"}, "")

	if value, ok := envValue(t, env, "PWD"); ok {
		t.Fatalf("PWD survived as %q with no working directory", value)
	}
	if slices.Contains(env, "PWD=") {
		t.Fatalf("PWD was set to an empty value: %v", env)
	}
}
