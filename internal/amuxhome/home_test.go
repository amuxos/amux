package amuxhome

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirUsesAMUXHome(t *testing.T) {
	amuxHome := filepath.Join(t.TempDir(), "amux-home")
	t.Setenv("AMUX_HOME", "  "+amuxHome+"  ")

	if got := Dir(); got != amuxHome {
		t.Fatalf("Dir() = %q, want AMUX_HOME %q", got, amuxHome)
	}
	if got := Path("runtime.sock"); got != filepath.Join(amuxHome, "runtime.sock") {
		t.Fatalf("Path() = %q, want under AMUX_HOME", got)
	}
}

func TestDirDefaultsToUserHomeDotAmux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AMUX_HOME", "")
	t.Setenv("HOME", home)

	if got, want := Dir(), filepath.Join(home, ".amux"); got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestDirFallsBackWhenUserHomeUnavailable(t *testing.T) {
	t.Setenv("AMUX_HOME", "")
	oldHome := os.Getenv("HOME")
	t.Setenv("HOME", "")
	t.Cleanup(func() { _ = os.Setenv("HOME", oldHome) })

	if got := Dir(); got == "" {
		t.Fatal("Dir() returned empty path")
	}
}

func TestPrincipalSecretsDirUsesStablePrincipalID(t *testing.T) {
	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)
	const principalID = "11111111-1111-1111-1111-111111111111"

	got, err := PrincipalSecretsDir(principalID)
	if err != nil {
		t.Fatalf("PrincipalSecretsDir: %v", err)
	}
	want := filepath.Join(amuxHome, "principals", principalID, "secrets")
	if got != want {
		t.Fatalf("PrincipalSecretsDir() = %q, want %q", got, want)
	}
}

func TestPrincipalSecretsDirRejectsFilesystemInput(t *testing.T) {
	if _, err := PrincipalSecretsDir("../other"); err == nil {
		t.Fatal("PrincipalSecretsDir accepted unsafe principal id")
	}
}
