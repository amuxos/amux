package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestRelaunchRecoversMissingNativeHistory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		action  string
		handoff string
	}{
		{name: "resume durable transcript", action: "resuming"},
		{name: "restart durable transcript", action: "restarting"},
		{name: "automatic wake durable transcript", action: "waking"},
		{name: "resume undelivered handoff", action: "resuming", handoff: "pending model-switch history"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(filepath.Join(dir, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mgr := session.NewManager(db)
			sess, err := mgr.Create("recover", "codex", dir)
			if err != nil {
				t.Fatal(err)
			}
			extraArgs := []string{"--model", "chosen-model"}
			env := []string{"KEEP=value"}
			if err := mgr.RecordLaunchParams(sess, extraArgs, env, "feishu", "singleton", "builtin/codex", "chosen-model", `{}`); err != nil {
				t.Fatal(err)
			}
			if err := mgr.RecordAgentSessionID(sess, "missing-thread"); err != nil {
				t.Fatal(err)
			}
			if tc.handoff != "" {
				snap := sess.Snapshot()
				snap.HandoffContext = tc.handoff
				if err := mgr.RestoreLaunchSnapshot(sess, snap); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.AppendMessage(sess.ID, "user", []byte("remember the existing conversation")); err != nil {
				t.Fatal(err)
			}
			if err := mgr.SetSleeping(sess); err != nil {
				t.Fatal(err)
			}
			var attempts []string
			srv := &server{mgr: mgr, amuxDir: dir, disableMCPAutoInject: true}
			srv.startSessionContextHook = func(_ context.Context, _ adapter.Adapter, current *session.Session, args, _ []string) error {
				snap := current.Snapshot()
				attempts = append(attempts, snap.AgentSessionID)
				if snap.AgentSessionID == "missing-thread" {
					return errors.New("codex thread/resume: no rollout found for thread id missing-thread")
				}
				if !reflect.DeepEqual(args, extraArgs) || snap.EffectiveModel != "chosen-model" || snap.Cwd != dir || snap.WorkspaceScope != "singleton" {
					t.Fatalf("execution changed during recovery: %+v, args=%v", snap, args)
				}
				if tc.handoff != "" {
					if snap.HandoffContext != tc.handoff {
						t.Fatalf("pending handoff = %q", snap.HandoffContext)
					}
				} else if !strings.Contains(snap.HandoffContext, "remember the existing conversation") {
					t.Fatalf("durable transcript absent from handoff: %q", snap.HandoffContext)
				}
				current.SetProc(&serverTestProc{})
				return mgr.SetRunning(current)
			}
			var resp *pb.RunResponse
			if tc.action == "waking" {
				err = srv.ensureSessionActive(sess)
				resp = &pb.RunResponse{Session: toProtoSession(sess)}
			} else if tc.action == "resuming" {
				resp, err = srv.Resume(context.Background(), &pb.ResumeRequest{Name: sess.Name})
			} else {
				resp, err = srv.Restart(context.Background(), &pb.RestartRequest{Name: sess.Name})
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(attempts, []string{"missing-thread", ""}) || resp.Session.Id != sess.ID || sess.StateSnapshot() != session.StateRunning {
				t.Fatalf("attempts=%v, response=%+v, state=%s", attempts, resp, sess.StateSnapshot())
			}
		})
	}
}

func TestRelaunchMissingHistoryFailureIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name        string
		firstError  string
		wantStarts  int
		wantAgentID string
	}{
		{"authentication error", "codex thread/resume: authentication failed", 1, "saved-thread"},
		{"fresh launch fails", "codex thread/resume: no rollout found for thread id saved-thread", 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(filepath.Join(dir, "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mgr := session.NewManager(db)
			sess, err := mgr.Create("failed-resume", "codex", dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := mgr.RecordAgentSessionID(sess, "saved-thread"); err != nil {
				t.Fatal(err)
			}
			if err := mgr.SetSleeping(sess); err != nil {
				t.Fatal(err)
			}
			starts := 0
			srv := &server{mgr: mgr, amuxDir: dir, disableMCPAutoInject: true}
			srv.startSessionContextHook = func(_ context.Context, _ adapter.Adapter, _ *session.Session, _, _ []string) error {
				starts++
				if starts == 1 {
					return errors.New(tc.firstError)
				}
				return errors.New("fresh provider unavailable")
			}
			_, err = srv.Resume(context.Background(), &pb.ResumeRequest{Name: sess.Name})
			if err == nil || !strings.Contains(err.Error(), tc.firstError) || starts != tc.wantStarts {
				t.Fatalf("error=%v starts=%d", err, starts)
			}
			if snap := sess.Snapshot(); snap.State != session.StateSleeping || snap.AgentSessionID != tc.wantAgentID {
				t.Fatalf("snapshot = %+v", snap)
			}
		})
	}
}
