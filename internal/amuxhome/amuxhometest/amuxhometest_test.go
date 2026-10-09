package amuxhometest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/amuxhome"
)

func TestIsolateReplacesAndRestoresAMUXHome(t *testing.T) {
	inherited := filepath.Join(t.TempDir(), "real-amux-home")
	t.Setenv(EnvVar, inherited)

	restore, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	isolated := amuxhome.Dir()
	if isolated == inherited {
		t.Fatalf("amuxhome.Dir() = inherited %q, want isolated directory", inherited)
	}
	if info, err := os.Stat(isolated); err != nil || !info.IsDir() {
		t.Fatalf("isolated home %q is not a directory: %v", isolated, err)
	}

	restore()
	if got := os.Getenv(EnvVar); got != inherited {
		t.Fatalf("%s after restore = %q, want %q", EnvVar, got, inherited)
	}
	if _, err := os.Stat(isolated); !os.IsNotExist(err) {
		t.Fatalf("isolated home %q still exists after restore: %v", isolated, err)
	}
}

func TestIsolateUnsetsAMUXHomeWhenItWasUnset(t *testing.T) {
	t.Setenv(EnvVar, "")
	if err := os.Unsetenv(EnvVar); err != nil {
		t.Fatal(err)
	}
	restore, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv(EnvVar) == "" {
		t.Fatalf("%s not set while isolated", EnvVar)
	}
	restore()
	if _, ok := os.LookupEnv(EnvVar); ok {
		t.Fatalf("%s still set after restore", EnvVar)
	}
}

func TestIsolateKeepsAMUXHomeForPreservingHelperProcess(t *testing.T) {
	chosen := filepath.Join(t.TempDir(), "parent-test-amux-home")
	t.Setenv(EnvVar, chosen)
	t.Setenv(PreserveEnvVar, "1")

	restore, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if got := amuxhome.Dir(); got != chosen {
		t.Fatalf("amuxhome.Dir() = %q, want preserved %q", got, chosen)
	}
}
