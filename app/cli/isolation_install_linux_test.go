//go:build linux

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPrepareIsolationHelperDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, strings.TrimPrefix(isolationHelperDir, "/"))
	if err := prepareIsolationHelperDir(root, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upgrade of absent directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "usr")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upgrade created ancestors: %v", err)
	}
	oldUmask := unix.Umask(0o077)
	defer unix.Umask(oldUmask)
	if err := prepareIsolationHelperDir(root, true); err != nil {
		t.Fatal(err)
	}
	for dir := target; dir != root; dir = filepath.Dir(dir) {
		assertHelperDirMode(t, dir, 0o755)
	}

	for _, create := range []bool{true, false} {
		if err := os.Chmod(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := prepareIsolationHelperDir(root, create); err != nil {
			t.Fatal(err)
		}
		assertHelperDirMode(t, target, 0o755)
	}
}

func TestPrepareIsolationHelperDirRejectsUnsafePath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, strings.TrimPrefix(isolationHelperDir, "/"))
	parent := filepath.Dir(target)
	if err := prepareIsolationHelperDir(root, true); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o700, 0o777} {
		if err := os.Chmod(parent, mode); err != nil {
			t.Fatal(err)
		}
		if err := prepareIsolationHelperDir(root, true); err == nil {
			t.Errorf("accepted existing parent with mode %04o", mode)
		}
		assertHelperDirMode(t, parent, mode)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.Chmod(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, target); err != nil {
		t.Fatal(err)
	}
	if err := prepareIsolationHelperDir(root, false); err == nil {
		t.Error("accepted a symlink at the install directory")
	}
	assertHelperDirMode(t, other, 0o700)
}

func assertHelperDirMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %04o, want %04o", path, got, want)
	}
}
