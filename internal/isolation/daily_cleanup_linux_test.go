//go:build linux

package isolation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/cleanup"
)

func TestHelperDailyCleanupPreservesReferencedAndRunningWorkspaces(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cfg := HelperConfig{SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"), PrincipalRoot: filepath.Join(root, "principals"), ServiceUID: os.Getuid(), ServiceGID: os.Getgid(), UIDRangeStart: max(os.Getuid(), 1), UIDRangeEnd: max(os.Getuid(), 1) + 10, AllowUnprivileged: true, SkipAccountProvision: true}
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, cfg) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client := NewHelperClient(cfg.SocketPath)
	waitHelperReady(t, client)
	mapping := AccountMapping{PrincipalID: "d7eaf95f-4c18-43b2-81fd-f5f013ac901d", Account: "amux_0123456789ab", UID: max(os.Getuid(), 1), GID: max(os.Getuid(), 1)}
	paths, err := client.PrepareRun(ctx, mapping, "exec_active", "launch-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := client.Start(ctx, mapping, LaunchSpec{Path: "/bin/sleep", Args: []string{"300"}, Env: []string{"PATH=/usr/bin:/bin"}, Dir: paths.Workspace, Context: LaunchContext{LaunchID: "launch-cleanup"}})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	defer handle.Kill(context.Background())
	workspaceRoot := filepath.Dir(paths.Workspace)
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, path := range []string{paths.Workspace, filepath.Join(workspaceRoot, "orphan"), filepath.Join(workspaceRoot, "referenced")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	refs := cleanup.References{Workspaces: []string{filepath.Join(workspaceRoot, "referenced")}}
	if _, err := client.DailyCleanup(ctx, mapping, DailyCleanupRequest{}); err == nil {
		t.Fatal("missing authoritative references accepted")
	}
	if _, err := client.DailyCleanup(ctx, mapping, DailyCleanupRequest{References: &refs, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, "orphan")); err != nil {
		t.Fatal("preview deleted data")
	}
	steps, err := client.DailyCleanup(ctx, mapping, DailyCleanupRequest{References: &refs})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if len(s.Errors) > 0 {
			t.Fatalf("cleanup: %+v", steps)
		}
	}
	for _, path := range []string{paths.Workspace, filepath.Join(workspaceRoot, "referenced")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("live workspace removed: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, "orphan")); !os.IsNotExist(err) {
		t.Fatal("orphan retained")
	}
}
