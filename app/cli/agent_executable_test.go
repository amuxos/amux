package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorExecutableUsesExecutionHomeAndRejectsInvalidPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	path := filepath.Join(home, "custom launcher")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveAgentDoctorExecutable("codex", "~/custom launcher")
	if err != nil || got != path {
		t.Fatalf("%q %v", got, err)
	}
	for _, program := range []string{"./relative", "bad\npath", filepath.Join(home, "missing")} {
		if _, err := resolveAgentDoctorExecutable("codex", program); err == nil {
			t.Fatalf("accepted %q", program)
		}
	}
}
