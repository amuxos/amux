package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/turnresult"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHubTurnResultDisabledClearsCacheAndRejectsQueries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "turn_results.db")
	// Pre-populate a cached turn result while enabled.
	enabledCfg := config.TurnResultsConfig{
		Enabled:               true,
		TTLSeconds:            3600,
		PendingTimeoutSeconds: 300,
		MaxEntries:            64,
		MaxTotalBytes:         1 << 20,
		MaxContentBytes:       64 << 10,
	}
	store, err := initTurnResultStore(enabledCfg, dbPath)
	if err != nil || store == nil {
		t.Fatalf("initTurnResultStore(enabled): store=%v err=%v", store, err)
	}
	if _, err := store.RecordExecutionOutput(turnresult.ExecutionInput{
		Ref:       turnresult.Ref{TargetID: "local", SessionID: "s1", SessionName: "chat", TurnID: "t1"},
		Source:    turnresult.Source{Kind: turnresult.SourceCLI},
		Execution: turnresult.Execution{Status: turnresult.ExecutionCompleted, FinishedAt: time.Now().Unix()},
		Output:    turnresult.Output{Content: "cached"},
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	_ = store.Close()

	// Disabling clears cached snapshots and disables reads.
	disabledCfg := enabledCfg
	disabledCfg.Enabled = false
	cleared, err := initTurnResultStore(disabledCfg, dbPath)
	if err != nil || cleared != nil {
		t.Fatalf("initTurnResultStore(disabled): store=%v err=%v", cleared, err)
	}
	srv := &hubServer{turnResults: cleared, turnResultsConfig: func() config.TurnResultsConfig { return disabledCfg }}
	resp, err := srv.GetTurnResult(context.Background(), &pb.AmuxdGetTurnResultRequest{
		TargetId: "local", SessionName: "chat", TurnId: "t1",
	})
	if err != nil {
		t.Fatalf("GetTurnResult(disabled): %v", err)
	}
	if resp.GetStatus() != turnresult.StatusUnavailable || resp.GetReason() != turnresult.ReasonDisabled {
		t.Fatalf("disabled response = %+v, want unavailable/disabled", resp)
	}

	// Re-enabling sees an empty store (previous cache was cleared).
	reopened, err := initTurnResultStore(enabledCfg, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.LookupBySessionTurn("local", "s1", "chat", "t1", time.Now())
	if err != nil || got.Status != turnresult.StatusNotFound {
		t.Fatalf("after disable+re-enable lookup = %+v, err=%v; want not_found", got, err)
	}
}

func TestHubGetTurnResultOnDemandRefreshAndScheduleModes(t *testing.T) {
	trStore, err := turnresult.OpenStore(filepath.Join(t.TempDir(), "turn_results.db"), turnresult.DefaultPolicy())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = trStore.Close() })

	schedStore, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatalf("schedule.OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = schedStore.Close() })

	enabledCfg := config.TurnResultsConfig{Enabled: true, TTLSeconds: 3600, PendingTimeoutSeconds: 300, MaxEntries: 64, MaxTotalBytes: 8 << 20, MaxContentBytes: 1 << 20}
	srv := &hubServer{
		schedule:          schedStore,
		turnResults:       trStore,
		turnResultsConfig: func() config.TurnResultsConfig { return enabledCfg },
		runtimeGetTurnResult: func(_ context.Context, _ string, req *pb.GetTurnResultRequest) (*pb.GetTurnResultResponse, error) {
			return &pb.GetTurnResultResponse{
				Found:           true,
				Terminal:        true,
				SessionId:       req.GetSessionId(),
				SessionName:     req.GetName(),
				LaunchId:        "launch-1",
				TurnId:          req.GetTurnId(),
				ExecutionStatus: turnresult.ExecutionCompleted,
				StartedAt:       100,
				FinishedAt:      120,
				OutputStatus:    turnresult.OutputAvailable,
				OutputContent:   "Live runtime markdown answer",
			}, nil
		},
	}

	// 1. CLI turn registered on submit, refreshed on-demand from Runtime.
	srv.registerSubmittedTurn("local", "cli-chat", "cli", &pb.SubmitResponse{
		Ok: true, SessionId: "sess-cli", LaunchId: "launch-1", TurnId: "turn-cli-1",
	})
	resp, err := srv.GetTurnResult(context.Background(), &pb.AmuxdGetTurnResultRequest{
		TargetId: "local", SessionName: "cli-chat", TurnId: "turn-cli-1",
	})
	if err != nil {
		t.Fatalf("GetTurnResult cli: %v", err)
	}
	if resp.GetStatus() != turnresult.StatusReady || resp.GetResult().GetOutput().GetContent() != "Live runtime markdown answer" {
		t.Fatalf("cli turn result = %+v", resp)
	}

	// 2. Scheduled session-prompt (new-session-per-run, no Feishu binding) -> settles immediately via refresh.
	now := time.Now().Unix()
	next := now + 60
	taskRow, err := schedStore.Create(schedule.Task{
		Name: "nightly", TargetID: "local", SessionName: "tpl", Prompt: "check",
		Spec: "every 1h", Kind: "every", RunMode: schedule.RunModeNewSessionPerRun,
		ScheduleType: schedule.ScheduleTypeSessionPrompt, Enabled: true, NextRunAt: &next,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	exec := schedule.Execution{
		ID: 11, ScheduleID: taskRow.ID, ScheduledAt: now, SubmittedAt: now,
		TargetID: "local", SessionName: "sched-1-run", SessionID: "sess-new", TurnID: "turn-sched-new",
		Status: "submitted", Terminal: false,
	}
	srv.observeScheduledExecution(taskRow, exec)
	schedResp, err := srv.GetTurnResult(context.Background(), &pb.AmuxdGetTurnResultRequest{
		ScheduleId: taskRow.ID, ExecutionId: 0, // latest
	})
	if err != nil {
		t.Fatalf("GetTurnResult schedule: %v", err)
	}
	if schedResp.GetStatus() != turnresult.StatusReady ||
		schedResp.GetResult().GetSource().GetKind() != turnresult.SourceSchedule ||
		schedResp.GetResult().GetSource().GetRunMode() != schedule.RunModeNewSessionPerRun ||
		schedResp.GetResult().GetOutput().GetContent() != "Live runtime markdown answer" ||
		len(schedResp.GetResult().GetDeliveries()) != 0 {
		t.Fatalf("schedule new-session result = %+v", schedResp)
	}

	// 3. Missed scheduled execution is immediately ready with execution=missed, output=unavailable.
	missedExec := schedule.Execution{
		ID: 12, ScheduleID: taskRow.ID, ScheduledAt: now + 60, SubmittedAt: now + 60,
		Status: "missed", Reason: "not_dispatched", Terminal: true, FinishedAt: now + 60,
	}
	srv.observeScheduledExecution(taskRow, missedExec)
	missedResp, err := srv.GetTurnResult(context.Background(), &pb.AmuxdGetTurnResultRequest{
		ScheduleId: taskRow.ID, ExecutionId: 12,
	})
	if err != nil {
		t.Fatalf("GetTurnResult missed: %v", err)
	}
	if missedResp.GetStatus() != turnresult.StatusReady ||
		missedResp.GetResult().GetExecution().GetStatus() != turnresult.ExecutionMissed ||
		missedResp.GetResult().GetOutput().GetStatus() != turnresult.OutputUnavailable {
		t.Fatalf("missed schedule result = %+v", missedResp)
	}
}

func TestHubGetTurnResultEnforcesSchedulePrincipalIsolation(t *testing.T) {
	trStore, err := turnresult.OpenStore(filepath.Join(t.TempDir(), "turn_results.db"), turnresult.DefaultPolicy())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = trStore.Close() })

	schedStore, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatalf("schedule.OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = schedStore.Close() })

	pStore, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatalf("principal.OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = pStore.Close() })

	alice, err := pStore.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Alice"})
	if err != nil {
		t.Fatalf("Create alice: %v", err)
	}
	bob, err := pStore.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "Bob"})
	if err != nil {
		t.Fatalf("Create bob: %v", err)
	}
	aliceCred, err := pStore.IssueAgentCredential(alice.ID, "launch-alice", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatalf("IssueAgentCredential alice: %v", err)
	}
	bobCred, err := pStore.IssueAgentCredential(bob.ID, "launch-bob", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatalf("IssueAgentCredential bob: %v", err)
	}

	next := time.Now().Unix() + 60
	taskRow, err := schedStore.Create(schedule.Task{
		Name: "alice-only", TargetID: "local", SessionName: "s1", Prompt: "secret",
		Spec: "every 1h", Kind: "every", RunMode: schedule.RunModeReuseSession,
		ScheduleType: schedule.ScheduleTypeSessionPrompt, Enabled: true, NextRunAt: &next,
		OwnerPrincipalID: alice.ID,
	})
	if err != nil {
		t.Fatalf("Create schedule: %v", err)
	}

	srv := &hubServer{
		schedule:          schedStore,
		principals:        pStore,
		turnResults:       trStore,
		turnResultsConfig: func() config.TurnResultsConfig { return config.TurnResultsConfig{Enabled: true} },
		listTargets:       func() []*pb.Target { return isolatingTargets("t1") },
	}
	srv.observeScheduledExecution(taskRow, schedule.Execution{
		ID: 99, ScheduleID: taskRow.ID, ScheduledAt: next - 60, SubmittedAt: next - 60,
		Status: "completed", Result: "alice secret output", Terminal: true, FinishedAt: next - 50,
	})

	// Bob must be denied reading Alice's schedule turn result.
	bobCtx := actorCtx(bobCred.Token)
	if _, err := srv.GetTurnResult(bobCtx, &pb.AmuxdGetTurnResultRequest{ScheduleId: taskRow.ID}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Bob GetTurnResult err = %v, want PermissionDenied", err)
	}

	// Alice can read her own schedule turn result.
	aliceCtx := actorCtx(aliceCred.Token)
	got, err := srv.GetTurnResult(aliceCtx, &pb.AmuxdGetTurnResultRequest{ScheduleId: taskRow.ID})
	if err != nil || got.GetStatus() != turnresult.StatusReady || got.GetResult().GetOutput().GetContent() != "alice secret output" {
		t.Fatalf("Alice GetTurnResult = %+v, err=%v", got, err)
	}
}
