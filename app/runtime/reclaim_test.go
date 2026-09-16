package runtime

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/spf13/viper"
)

func newReclaimTestServer(t *testing.T) (*server, *session.Manager) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := session.NewManager(db)
	srv := &server{mgr: mgr, amuxDir: t.TempDir()}
	return srv, mgr
}

func TestDefaultReclaimRetainsFeishuSessions(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	cfg, err := config.GetSessionReclaim()
	if err != nil {
		t.Fatal(err)
	}
	pol := session.ReclaimPolicy{
		SleepAfter:   time.Duration(cfg.Feishu.SleepAfterSeconds) * time.Second,
		ArchiveAfter: time.Duration(cfg.Feishu.ArchiveAfterSeconds) * time.Second,
		PruneAfter:   time.Duration(cfg.Feishu.PruneAfterSeconds) * time.Second,
	}
	srv, mgr := newReclaimTestServer(t)
	now := time.Now()
	for _, tc := range []struct {
		name         string
		state        session.State
		disableSleep bool
		want         session.State
	}{
		{name: "singleton", state: session.StateRunning, disableSleep: true, want: session.StateRunning},
		{name: "thread", state: session.StateRunning, want: session.StateSleeping},
		{name: "sleeping", state: session.StateSleeping, want: session.StateSleeping},
		{name: "archived", state: session.StateArchived, want: session.StateArchived},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess, err := mgr.CreateWithOptions(tc.name, "codex", session.CreateOptions{
				IdleTimeoutSeconds: 0,
				IdleTimeoutSet:     tc.disableSleep,
			})
			if err != nil {
				t.Fatal(err)
			}
			sess.WorkspaceSource = "feishu"
			sess.AgentSessionID = "provider-history"
			sess.State = tc.state
			sess.LastInteractionAt = now.Add(-30 * 24 * time.Hour)
			// A second sweep exercises the sleeping -> archive stage separately.
			srv.reclaimSessions(now, session.ReclaimPolicy{}, pol)
			srv.reclaimSessions(now.Add(24*time.Hour), session.ReclaimPolicy{}, pol)
			got, err := mgr.Get(tc.name)
			if err != nil {
				t.Fatalf("session was pruned: %v", err)
			}
			if got.StateSnapshot() != tc.want {
				t.Fatalf("state = %s, want %s", got.StateSnapshot(), tc.want)
			}
			if got.Snapshot().AgentSessionID != "provider-history" {
				t.Fatal("provider history was lost")
			}
		})
	}
}

func TestReclaimSessionsArchivesAndPrunes(t *testing.T) {
	srv, mgr := newReclaimTestServer(t)
	now := time.Unix(3_000_000, 0)
	pol := session.ReclaimPolicy{SleepAfter: 48 * time.Hour, ArchiveAfter: 60 * time.Hour, PruneAfter: 120 * time.Hour}

	a, err := mgr.Create("a", "claude")
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := mgr.SetRunning(a); err != nil {
		t.Fatalf("set running a: %v", err)
	}
	a.LastInteractionAt = now.Add(-61 * time.Hour) // non-resumable (no AgentSessionID) → archive

	b, err := mgr.Create("b", "claude")
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := mgr.SetRunning(b); err != nil {
		t.Fatalf("set running b: %v", err)
	}
	// Resumable (AgentSessionID set) Running session idle past sleep but before
	// archive → should sleep via the sweep. Set the id directly so we don't bump
	// LastInteractionAt (RecordAgentSessionID calls MarkActivity), then set the
	// idle clock. SetRunning leaves no started process, so sleepSession's Kill is
	// a no-op here (same assumption the "a" archive case relies on).
	b.AgentSessionID = "agent-1"
	b.LastInteractionAt = now.Add(-49 * time.Hour)

	p, err := mgr.Create("p", "claude")
	if err != nil {
		t.Fatalf("create p: %v", err)
	}
	if err := mgr.SetArchived(p, "idle"); err != nil {
		t.Fatalf("set archived p: %v", err)
	}
	p.LastInteractionAt = now.Add(-200 * time.Hour) // already archived, past prune

	o, err := mgr.CreateOwned("o", "claude", "task-1", "")
	if err != nil {
		t.Fatalf("create owned o: %v", err)
	}
	if err := mgr.SetRunning(o); err != nil {
		t.Fatalf("set running o: %v", err)
	}
	o.LastInteractionAt = now.Add(-300 * time.Hour) // task-owned → never

	srv.reclaimSessions(now, pol, pol)

	if got, _ := mgr.Get("a"); got.StateSnapshot() != session.StateArchived {
		t.Fatalf("a state = %v, want archived", got.StateSnapshot())
	}
	if got, _ := mgr.Get("b"); got.StateSnapshot() != session.StateSleeping {
		t.Fatalf("b state = %v, want sleeping", got.StateSnapshot())
	}
	if _, err := mgr.Get("p"); err == nil {
		t.Fatal("p should have been pruned")
	}
	if got, _ := mgr.Get("o"); got.StateSnapshot() == session.StateArchived {
		t.Fatal("task-owned session must not be reclaimed")
	}
}

// TestReclaimSkipsAgentInstanceOwned verifies that a session owned by a
// persistent agent instance is never idle-reclaimed, even when idle past the
// prune threshold. The agent-level lifecycle owns its sleeping/running state.
func TestReclaimSkipsAgentInstanceOwned(t *testing.T) {
	srv, mgr := newReclaimTestServer(t)
	now := time.Unix(3_000_000, 0)
	pol := session.ReclaimPolicy{SleepAfter: 48 * time.Hour, ArchiveAfter: 60 * time.Hour, PruneAfter: 120 * time.Hour}

	inst, err := mgr.CreateOwned("instance-i1", "claude", "task-1", "i1")
	if err != nil {
		t.Fatalf("create instance owned: %v", err)
	}
	if err := mgr.SetRunning(inst); err != nil {
		t.Fatalf("set running: %v", err)
	}
	inst.AgentSessionID = "agent-1"
	inst.LastInteractionAt = now.Add(-300 * time.Hour) // far past prune

	srv.reclaimSessions(now, pol, pol)

	got, _ := mgr.Get("instance-i1")
	if got == nil {
		t.Fatal("instance-owned session was pruned")
	}
	if got.StateSnapshot() == session.StateArchived {
		t.Fatal("instance-owned session must not be archived by idle reclaim")
	}
	if got.StateSnapshot() == session.StateSleeping {
		t.Fatal("instance-owned session must not be put to sleep by idle reclaim either")
	}
}
