package runtime

import (
	"context"
	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"path/filepath"
	"testing"
)

// A set-model caller (CLI/MCP) does not resolve the profile registry; the
// replacement carries only the agent type and the new model args. amuxlet
// re-projects the switch through the profile registry, which for a native
// target still matches the frozen profile identity so the hot-reconfigure
// path applies.
func TestReconfigureExecutionBackfillsProfileFromSnapshot(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("set-model", "codex", dir)
	if err != nil {
		t.Fatal(err)
	}
	oldReq := &pb.RunRequest{
		AgentProfile: "builtin/codex", AgentType: "codex", ExtraArgs: []string{"--model", "gpt-5.5"},
		ProfileSpec: &pb.AgentProfileSpec{Ref: "builtin/codex", Type: "codex", Model: "gpt-5.5", ReasoningEffort: "medium"},
	}
	if err := mgr.RecordLaunchParams(sess, oldReq.ExtraArgs, nil, "cli", "local", oldReq.AgentProfile, "gpt-5.5", launchSpecSnapshot(oldReq)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "native-thread"); err != nil {
		t.Fatal(err)
	}
	proc := &serverTestProc{}
	sess.SetProc(proc)
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}

	srv := &server{
		mgr:                  mgr,
		amuxDir:              dir,
		disableMCPAutoInject: true,
		startSessionContextHook: func(_ context.Context, _ adapter.Adapter, _ *session.Session, _, _ []string) error {
			t.Fatal("relaunch should not run: hot reconfigure expected")
			return nil
		},
	}
	replacement := &pb.RunRequest{AgentType: "codex", ExtraArgs: []string{"--model", "gpt-5.4"}}
	resp, err := srv.reconfigureExecutionAndRelaunch(context.Background(), sess, replacement, true)
	if err != nil {
		t.Fatalf("reconfigure execution: %v", err)
	}
	if resp == nil || resp.Session == nil {
		t.Fatalf("response = %+v", resp)
	}
	if replacement.AgentProfile != "builtin/codex" {
		t.Fatalf("AgentProfile backfill = %q, want %q", replacement.AgentProfile, "builtin/codex")
	}
	if replacement.ProfileSpec == nil || replacement.ProfileSpec.Ref != "builtin/codex" {
		t.Fatalf("ProfileSpec backfill = %+v", replacement.ProfileSpec)
	}
	if proc.closed {
		t.Fatal("process was restarted; hot reconfigure expected after backfill")
	}
	got := sess.Snapshot()
	if got.AgentSessionID != "native-thread" || got.EffectiveModel != "gpt-5.4" {
		t.Fatalf("snapshot = %+v", got)
	}
}
