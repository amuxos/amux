package task

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitChangedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	ctx := context.Background()

	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "t@t")
	mustGit(t, dir, "config", "user.name", "t")
	write(t, filepath.Join(dir, "a.txt"), "one\n")
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "init")
	base := trimNL(out(t, dir, "rev-parse", "HEAD"))

	// Modify a tracked file and add an untracked one.
	write(t, filepath.Join(dir, "a.txt"), "one\ntwo\n")
	write(t, filepath.Join(dir, "b.txt"), "new\n")

	files, err := GitChangedFiles(ctx, dir, base)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ChangeType{}
	for _, f := range files {
		got[f.Path] = f.ChangeType
	}
	if got["a.txt"] != ChangeModified {
		t.Errorf("a.txt = %q, want modified", got["a.txt"])
	}
	if got["b.txt"] != ChangeAdded {
		t.Errorf("b.txt = %q, want added", got["b.txt"])
	}
}

func TestGitChangedFilesNonRepoIsEmpty(t *testing.T) {
	files, err := GitChangedFiles(context.Background(), t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty, got %+v", files)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
}

func out(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(b)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
