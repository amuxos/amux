package runtime

import (
	"context"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyCleanupPreservesLiveDataAndPersistsIdempotency(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", dir)
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	srv := &server{mgr: mgr, amuxDir: dir, cleanupDB: db, cleanupTarget: "test"}
	runner, err := task.NewRunner(task.Options{AmuxDir: dir, Store: task.NewSQLStore(db), Control: newTaskController(srv, "test"), Artifacts: taskArtifactPruner{srv: srv}})
	if err != nil {
		t.Fatal(err)
	}
	srv.cleanupTasks = runner
	srv.taskExists = runner.TaskExists
	live, err := mgr.Create("live", "codex")
	if err != nil {
		t.Fatal(err)
	}
	dead, err := mgr.Create("dead", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if err = mgr.SetArchived(dead, "test"); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "workspaces", "feishu", "group")
	orphan := filepath.Join(root, "orphan")
	if err = os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(orphan, "data"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(orphan, old, old)
	preview, err := srv.Cleanup(context.Background(), &pb.CleanupRequest{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Reports[0].DryRun {
		t.Fatal("not a preview")
	}
	if _, err = mgr.Get(dead.ID); err != nil {
		t.Fatal("preview pruned session")
	}
	if _, err = os.Stat(orphan); err != nil {
		t.Fatal("preview removed workspace")
	}
	result, err := srv.Cleanup(context.Background(), &pb.CleanupRequest{RunId: "daily/test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mgr.Get(live.ID); err != nil {
		t.Fatal("live session lost")
	}
	if _, err = mgr.Get(dead.ID); err == nil {
		t.Fatal("archived session retained")
	}
	if _, err = os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan not removed")
	}
	after, err := mgr.Create("after", "codex")
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetArchived(after, "test")
	cached, err := srv.Cleanup(context.Background(), &pb.CleanupRequest{RunId: "daily/test"})
	if err != nil {
		t.Fatal(err)
	}
	if cached.Reports[0].FinishedAt != result.Reports[0].FinishedAt {
		t.Fatal("not cached")
	}
	if _, err = mgr.Get(after.ID); err != nil {
		t.Fatal("duplicate run deleted newer data")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = srv.Cleanup(context.Background(), &pb.CleanupRequest{RunId: "bad-db"}); err == nil {
		t.Fatal("closed DB accepted")
	}
}
