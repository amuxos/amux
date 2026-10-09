package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIsolationGlobalEnvCommandsRegistered(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"set", "unset", "ls"} {
		found, _, err := root.Find([]string{"isolation", "global-env", name})
		if err != nil || found == nil || found.Name() != name {
			t.Fatalf("missing isolation global-env %s: found=%v err=%v", name, found, err)
		}
	}
}

func TestIsolationGlobalEnvSetListUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	path := filepath.Join(home, ".amux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[runtime]\nname = 'keep-me'\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var allOutput strings.Builder
	run := func(args []string, input string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		cmd := newIsolationGlobalEnvCmd()
		cmd.SetIn(strings.NewReader(input))
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("global-env %v: %v", args, err)
		}
		allOutput.WriteString(out.String())
		allOutput.WriteString(errOut.String())
		return out.String()
	}

	run([]string{"set", "Z_SHARED_TOKEN"}, "z-secret\n")
	run([]string{"set", "GLM_API_KEY"}, "old-glm-secret\n")
	run([]string{"set", "GLM_API_KEY"}, "new-glm-secret\n")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("config mode = %o, want 0600", got)
	}
	cfg, err := readCLIConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := isolationGlobalEnvEntries(map[string]any(cfg))
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := []string{"Z_SHARED_TOKEN=z-secret", "GLM_API_KEY=new-glm-secret"}
	if !slices.Equal(entries, wantEntries) {
		t.Fatalf("global env = %q, want %q", entries, wantEntries)
	}

	list := run([]string{"ls"}, "")
	if want := "ENV\nGLM_API_KEY\nZ_SHARED_TOKEN\n"; list != want {
		t.Fatalf("global-env ls = %q, want %q", list, want)
	}
	run([]string{"unset", "GLM_API_KEY"}, "")
	list = run([]string{"ls"}, "")
	if want := "ENV\nZ_SHARED_TOKEN\n"; list != want {
		t.Fatalf("global-env ls after unset = %q, want %q", list, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "keep-me") {
		t.Fatalf("unrelated config was not preserved:\n%s", data)
	}
	for _, secret := range []string{"z-secret", "old-glm-secret", "new-glm-secret"} {
		if strings.Contains(allOutput.String(), secret) {
			t.Fatalf("command output leaked secret %q: %q", secret, allOutput.String())
		}
	}
}

func TestIsolationGlobalEnvSetPromptsForInteractiveSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	oldInputFD := credentialInputFD
	oldIsTerminal := credentialIsTerminal
	oldReadPassword := credentialReadPassword
	t.Cleanup(func() {
		credentialInputFD = oldInputFD
		credentialIsTerminal = oldIsTerminal
		credentialReadPassword = oldReadPassword
	})
	credentialInputFD = func(io.Reader) (int, bool) { return 99, true }
	credentialIsTerminal = func(fd int) bool { return fd == 99 }
	credentialReadPassword = func(fd int) ([]byte, error) {
		if fd != 99 {
			t.Fatalf("ReadPassword fd = %d, want 99", fd)
		}
		return []byte("interactive-secret"), nil
	}

	var out, errOut bytes.Buffer
	cmd := newIsolationGlobalEnvCmd()
	cmd.SetIn(strings.NewReader("ignored"))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"set", "GLM_API_KEY"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("global-env set: %v", err)
	}
	if !strings.Contains(errOut.String(), "Enter value for GLM_API_KEY: ") {
		t.Fatalf("stderr = %q, want hidden-input prompt", errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "interactive-secret") {
		t.Fatalf("global-env set leaked secret: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestIsolationGlobalEnvSetRejectsValueArgumentBeforeReadingInput(t *testing.T) {
	cmd := newIsolationGlobalEnvCmd()
	cmd.SetIn(errorReader{})
	cmd.SetArgs([]string{"set", "GLM_API_KEY", "must-not-be-an-argument"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "accepts 1 arg") {
		t.Fatalf("global-env set error = %v, want exact-args rejection", err)
	}
}

func TestIsolationGlobalEnvInvalidConfigDoesNotLeakValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	path := filepath.Join(home, ".amux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const secret = "must-not-appear"
	if err := os.WriteFile(path, []byte("[isolation]\nglobal_env = ['INVALID-NAME="+secret+"']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newIsolationGlobalEnvCmd()
	cmd.SetArgs([]string{"ls"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("global-env ls accepted invalid config")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("global-env ls exposed secret: %v", err)
	}
}

func TestSystemConfigGetRejectsIsolationGlobalEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	path := filepath.Join(home, ".amux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const secret = "must-not-appear"
	if err := os.WriteFile(path, []byte("[isolation]\nglobal_env = ['GLM_API_KEY="+secret+"']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newConfigCmd()
	cmd.SetArgs([]string{"get", "isolation.global_env"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "global-env ls") {
		t.Fatalf("config get error = %v, want dedicated-command guidance", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("config get exposed secret: %v", err)
	}

	for _, jsonOut := range []bool{false, true} {
		var out bytes.Buffer
		show := newConfigCmd()
		args := []string{"show"}
		if jsonOut {
			show = newJSONCommandTestRoot(show)
			args = []string{"config", "show", "--json"}
		}
		show.SetOut(&out)
		show.SetArgs(args)
		if err := show.Execute(); err != nil {
			t.Fatalf("config %v: %v", args, err)
		}
		if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), "GLM_API_KEY") || !strings.Contains(out.String(), "[REDACTED]") {
			t.Fatalf("config %v did not safely redact global env: %q", args, out.String())
		}
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("input must not be read")
}
