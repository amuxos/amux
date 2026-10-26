package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func oldFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}
func ageDir(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}
func exists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if (err == nil) != want {
		t.Fatalf("%s exists=%v, want %v (%v)", path, err == nil, want, err)
	}
}
func TestWorkspaceCleanupProtectsReferencesAndNeverEscapes(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	oldFile(t, filepath.Join(external, "keep"), "outside")
	for _, name := range []string{"live", "shared", "orphan", "recent"} {
		oldFile(t, filepath.Join(root, name, "data"), "data")
		ageDir(t, filepath.Join(root, name))
	}
	if err := os.Chtimes(filepath.Join(root, "recent"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "new-empty"), 0700); err != nil {
		t.Fatal(err)
	}
	refs := References{Workspaces: []string{filepath.Join(root, "live", "repo")}, ProtectedPaths: []string{filepath.Join(root, "shared")}}
	preview := SweepWorkspaces(context.Background(), root, refs, 1, time.Now(), true)
	if preview.Candidates != 1 || preview.Deleted != 0 || len(preview.Errors) != 0 {
		t.Fatalf("preview=%+v", preview)
	}
	exists(t, filepath.Join(root, "orphan"), true)
	result := SweepWorkspaces(context.Background(), root, refs, 1, time.Now(), false)
	if result.Deleted != 1 || len(result.Errors) != 0 {
		t.Fatalf("result=%+v", result)
	}
	for _, name := range []string{"live", "shared", "recent", "new-empty", "link"} {
		exists(t, filepath.Join(root, name), true)
	}
	exists(t, filepath.Join(root, "orphan"), false)
	exists(t, filepath.Join(external, "keep"), true)
	result = SweepWorkspaces(context.Background(), filepath.Join(root, "link"), References{}, 1, time.Now(), false)
	if len(result.Errors) == 0 {
		t.Fatal("accepted a symlink root")
	}
}
func TestRolloutCleanupPreservesLiveAndRecentAndRemovesArtifacts(t *testing.T) {
	home := t.TempDir()
	p := HomePolicy{Name: "test", RolloutDir: "provider/sessions"}
	live := "11111111-1111-4111-8111-111111111111"
	dead := "22222222-2222-4222-8222-222222222222"
	recent := "33333333-3333-4333-8333-333333333333"
	file := func(id string) string {
		return filepath.Join(home, p.RolloutDir, "2026", "rollout-2026-01-01-"+id+".jsonl")
	}
	for _, id := range []string{live, dead, recent} {
		oldFile(t, file(id), "history")
	}
	os.Chtimes(file(recent), time.Now(), time.Now())
	oldFile(t, file(dead)+".lock", "lock")
	oldFile(t, strings.TrimSuffix(file(dead), ".jsonl")+".artifacts/data", "artifact")
	refs := References{AgentSessionIDs: []string{live}}
	preview := sweepRollouts(context.Background(), home, p, refs, time.Now(), true)
	if preview.Candidates != 3 || preview.Deleted != 0 {
		t.Fatalf("preview=%+v", preview)
	}
	exists(t, file(dead), true)
	result := sweepRollouts(context.Background(), home, p, refs, time.Now(), false)
	if result.Deleted != 3 || len(result.Errors) != 0 {
		t.Fatalf("result=%+v", result)
	}
	exists(t, file(live), true)
	exists(t, file(recent), true)
	exists(t, file(dead), false)
	exists(t, strings.TrimSuffix(file(dead), ".jsonl")+".artifacts", false)
}
func TestTrustCleanupPreservesUnrelatedConfigurationAndBackup(t *testing.T) {
	home := t.TempDir()
	live := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	p := HomePolicy{Name: "test", TrustFile: "provider/config.toml"}
	content := "name = \"keep\"\n\n[projects.\"" + live + "\"]\ntrust = true\n\n[projects.\"" + missing + "\"]\ntrust = true\n\n[other]\nvalue = 42\n"
	path := filepath.Join(home, p.TrustFile)
	oldFile(t, path, content)
	if s := sweepTrust(context.Background(), home, p, true); s.Candidates != 1 || s.Deleted != 0 {
		t.Fatalf("preview=%+v", s)
	}
	exists(t, path+".cleanup.bak", false)
	if s := sweepTrust(context.Background(), home, p, false); s.Deleted != 1 || len(s.Errors) != 0 {
		t.Fatalf("result=%+v", s)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), missing) || !strings.Contains(string(got), live) || !strings.Contains(string(got), "value = 42") {
		t.Fatalf("got %s", got)
	}
	backup, _ := os.ReadFile(path + ".cleanup.bak")
	if string(backup) != content {
		t.Fatal("backup differs")
	}
	oldFile(t, path, "not [valid TOML")
	if s := sweepTrust(context.Background(), home, p, false); len(s.Errors) == 0 {
		t.Fatal("invalid TOML accepted")
	}
}
func TestArtifactsCleanupLeavesUnknownFilesAndLiveIDs(t *testing.T) {
	root := t.TempDir()
	live := "11111111-1111-4111-8111-111111111111"
	dead := "22222222-2222-4222-8222-222222222222"
	for _, name := range []string{live + ".pid", dead + ".log", "unknown"} {
		oldFile(t, filepath.Join(root, name), "x")
	}
	s := SweepSessionArtifacts(context.Background(), root, References{SessionIDs: []string{live}}, time.Now(), false)
	if s.Deleted != 1 {
		t.Fatalf("result=%+v", s)
	}
	exists(t, filepath.Join(root, live+".pid"), true)
	exists(t, filepath.Join(root, "unknown"), true)
}
func TestLogCleanupPreviewAndInodePreservation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.log")
	oldFile(t, path, strings.Repeat("x", 20_000_001))
	before, _ := os.Stat(path)
	s := RotateLogs(context.Background(), root, []string{"runtime.log"}, true)
	if s.Candidates != 1 || s.Deleted != 0 {
		t.Fatalf("preview=%+v", s)
	}
	after, _ := os.Stat(path)
	if before.Size() != after.Size() {
		t.Fatal("preview changed file")
	}
	s = RotateLogs(context.Background(), root, []string{"runtime.log"}, false)
	after, _ = os.Stat(path)
	if s.Deleted != 1 || after.Size() != 5_000_000 || !os.SameFile(before, after) {
		t.Fatalf("result=%+v size=%d", s, after.Size())
	}
}

func TestProtectedRootCoversAllDescendants(t *testing.T) {
	if !Related(string(filepath.Separator), filepath.Join(string(filepath.Separator), "managed", "project")) {
		t.Fatal("protected root ignored")
	}
	if Related("/managed/project", "/managed/project-other") {
		t.Fatal("sibling mistaken for descendant")
	}
}
