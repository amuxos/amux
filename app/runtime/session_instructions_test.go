package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestSessionInstructionsOutOfBandWithInlineFallback(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	var mu sync.Mutex
	var launchArgs []string
	proc := &serverTestProc{}
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, args, _ []string) error {
			mu.Lock()
			launchArgs = append([]string(nil), args...)
			mu.Unlock()
			sess.SetProc(proc)
			return mgr.SetRunning(sess)
		},
	}
	if _, err := srv.Run(context.Background(), &pb.RunRequest{Name: "fg", AgentType: "claude", SystemInstructions: "[amux 前台协调说明：SYS-A]"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(launchArgs, "\x00")
	if !strings.Contains(joined, "--append-system-prompt\x00") || !strings.Contains(joined, "SYS-A") {
		t.Fatalf("launch args lack system prompt append: %q", launchArgs)
	}
	submit := func(text, instructions string) *pb.SubmitResponse {
		t.Helper()
		resp, err := srv.Submit(context.Background(), &pb.SubmitRequest{Name: "fg", Text: text, Source: "lark", SystemInstructions: instructions, ManageSystemInstructions: true})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	written := func() string {
		var b strings.Builder
		for _, w := range proc.writes {
			b.Write(w)
		}
		return b.String()
	}
	if resp := submit("hello", "[amux 前台协调说明：SYS-A]"); resp.InstructionsDelivery != "system" {
		t.Fatalf("delivery = %q, want system", resp.InstructionsDelivery)
	}
	if strings.Contains(written(), "SYS-A") {
		t.Fatalf("out-of-band instructions leaked into input: %q", written())
	}
	// Changed instructions cannot reach the running launch: inline fallback,
	// persisted for the next launch.
	if resp := submit("again", "[amux 前台协调说明：SYS-B]"); resp.InstructionsDelivery != "inline" {
		t.Fatalf("delivery = %q, want inline", resp.InstructionsDelivery)
	}
	if !strings.Contains(written(), "again\n\n[amux 前台协调说明：SYS-B]") {
		t.Fatalf("inline fallback missing: %q", written())
	}
	snap, _ := mgr.Get("fg")
	if got := readSessionInstructions(srv.amuxDir, snap.ID); !strings.Contains(got, "SYS-B") {
		t.Fatalf("persisted = %q", got)
	}
	// Switch off: cleared for future launches, nothing inline.
	if resp := submit("off", ""); resp.InstructionsDelivery != "" {
		t.Fatalf("delivery = %q, want empty", resp.InstructionsDelivery)
	}
	if got := readSessionInstructions(srv.amuxDir, snap.ID); got != "" {
		t.Fatalf("not cleared: %q", got)
	}
	// Unmanaged submits (CLI, subtask notices) leave the persisted value alone.
	writeSessionInstructions(srv.amuxDir, snap.ID, "KEEP")
	if _, err := srv.Submit(context.Background(), &pb.SubmitRequest{Name: "fg", Text: "cli", Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	if got := readSessionInstructions(srv.amuxDir, snap.ID); got != "KEEP" {
		t.Fatalf("unmanaged submit changed instructions: %q", got)
	}
}

func TestConfigureSessionInstructionsPerProvider(t *testing.T) {
	srv := &server{amuxDir: t.TempDir()}
	writeSessionInstructions(srv.amuxDir, "s1", "PROTO")
	for _, agent := range []string{"claude", "pi"} {
		args, _, hash := srv.configureSessionInstructions(session.Snapshot{ID: "s1", AgentType: agent}, []string{"--append-system-prompt", "PROFILE"}, nil)
		if hash == "" || strings.Join(args, "|") != "--append-system-prompt|PROFILE\n\nPROTO" {
			t.Fatalf("%s args = %q", agent, args)
		}
	}
	args, env, hash := srv.configureSessionInstructions(session.Snapshot{ID: "s1", AgentType: "codex"}, nil, nil)
	if hash == "" || len(args) != 0 || launchEnvValue(env, SessionInstructionsEnv) != "PROTO" {
		t.Fatalf("codex args=%q env=%q", args, env)
	}
	_, env, hash = srv.configureSessionInstructions(session.Snapshot{ID: "none", AgentType: "codex"}, nil, nil)
	if hash != "" || launchEnvValue(env, SessionInstructionsEnv) != "" {
		t.Fatalf("empty instructions applied: %q", env)
	}
}
