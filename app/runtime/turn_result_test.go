package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/turnresult"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestRuntimeGetTurnResultCompletedFailedRunningAndOversized(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr := session.NewManager(db)
	sess, err := mgr.Create("chat", "codex")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	srv := &server{mgr: mgr, cleanupDB: db}
	ctx := context.Background()

	// 1. Completed turn with thinking + intermediate narration + authoritative ResultObserved.
	turn1, err := sess.AgentTimeline().StartTurn(ctx, agentstream.StartRequest{
		TurnID: "turn-done",
		Origin: agentstream.Origin{Actor: agentstream.ActorUser, Channel: "cli", LaunchID: "launch-1"},
	}, func(context.Context, *agentstream.Turn) error { return nil })
	if err != nil {
		t.Fatalf("StartTurn 1: %v", err)
	}
	if _, err := turn1.Emit(ctx, agentstream.ThinkingDelta{Text: "internal thinking"}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}
	if _, err := turn1.Emit(ctx, agentstream.AssistantDelta{Text: "working..."}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}
	if _, err := turn1.Emit(ctx, agentstream.ResultObserved{Text: "Final answer"}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}
	if _, err := turn1.Finish(ctx, agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}

	got, err := srv.GetTurnResult(ctx, &pb.GetTurnResultRequest{SessionId: sess.ID, TurnId: "turn-done"})
	if err != nil {
		t.Fatalf("GetTurnResult done: %v", err)
	}
	if !got.Found || !got.Terminal || got.ExecutionStatus != turnresult.ExecutionCompleted ||
		got.OutputStatus != turnresult.OutputAvailable || got.OutputContent != "Final answer" || got.LaunchId != "launch-1" {
		t.Fatalf("unexpected done response: %+v", got)
	}

	// Oversized check does not truncate.
	oversized, err := srv.GetTurnResult(ctx, &pb.GetTurnResultRequest{SessionId: sess.ID, TurnId: "turn-done", MaxContentBytes: 5})
	if err != nil {
		t.Fatalf("GetTurnResult oversized: %v", err)
	}
	if oversized.OutputStatus != turnresult.OutputUnavailable || oversized.OutputReason != turnresult.ReasonResultTooLarge || oversized.OutputContent != "" {
		t.Fatalf("unexpected oversized response: %+v", oversized)
	}

	// 2. Failed turn hides partial answer.
	turn2, err := sess.AgentTimeline().StartTurn(ctx, agentstream.StartRequest{TurnID: "turn-fail"}, func(context.Context, *agentstream.Turn) error { return nil })
	if err != nil {
		t.Fatalf("StartTurn 2: %v", err)
	}
	if _, err := turn2.Emit(ctx, agentstream.AssistantMessage{Text: "partial text"}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}
	if _, err := turn2.Finish(ctx, agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: "provider_timeout"}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}
	failed, err := srv.GetTurnResult(ctx, &pb.GetTurnResultRequest{Name: "chat", TurnId: "turn-fail"})
	if err != nil {
		t.Fatalf("GetTurnResult fail: %v", err)
	}
	if !failed.Terminal || failed.ExecutionStatus != turnresult.ExecutionFailed || failed.ExecutionCode != "provider_timeout" ||
		failed.OutputStatus != turnresult.OutputUnavailable || failed.OutputContent != "" {
		t.Fatalf("unexpected failed response: %+v", failed)
	}

	// 3. Running turn stays non-terminal and withholds output.
	turn3, err := sess.AgentTimeline().StartTurn(ctx, agentstream.StartRequest{TurnID: "turn-running"}, func(context.Context, *agentstream.Turn) error { return nil })
	if err != nil {
		t.Fatalf("StartTurn 3: %v", err)
	}
	if _, err := turn3.Emit(ctx, agentstream.AssistantDelta{Text: strings.Repeat("x", 10)}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}
	running, err := srv.GetTurnResult(ctx, &pb.GetTurnResultRequest{SessionId: sess.ID, TurnId: "turn-running"})
	if err != nil {
		t.Fatalf("GetTurnResult running: %v", err)
	}
	if !running.Found || running.Terminal || running.ExecutionStatus != turnresult.ExecutionRunning || running.OutputContent != "" {
		t.Fatalf("unexpected running response: %+v", running)
	}

	// 4. Missing turn reports Found=false.
	missing, err := srv.GetTurnResult(ctx, &pb.GetTurnResultRequest{SessionId: sess.ID, TurnId: "turn-missing"})
	if err != nil {
		t.Fatalf("GetTurnResult missing: %v", err)
	}
	if missing.Found {
		t.Fatalf("expected Found=false, got %+v", missing)
	}
}
