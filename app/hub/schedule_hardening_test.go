package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/sessionlaunch"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPrepareScheduledNewSessionRefreshesIdentityAndMintsToken(t *testing.T) {
	principals := agentTokenTestStore(t)
	alice, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	origIdentity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: alice.ID,
		EffectivePrincipalID:   alice.ID,
		Source:                 principal.SourceFeishu,
		ExecutionStateRef:      "feishu:orig",
		IsolationBackend:       principal.BackendLinuxDAC,
	}
	template := &pb.Session{
		Name:               "template-s1",
		AgentProfile:       "builtin/codex",
		AgentType:          "codex",
		Cwd:                "/var/lib/amux/principals/amux_1234/workspace/feishu-orig",
		IdleTimeoutSeconds: 900,
		ExecutionIdentity:  principal.ToProtoIdentity(origIdentity),
	}
	srv := &hubServer{principals: principals}
	req := &sessionlaunch.StartRequest{
		TargetID: "t1",
		Name:     "sched-1-20260601-100000-1",
		Launch: sessionlaunch.StartRequest{}.Launch,
		Workspace: sessionlaunch.WorkspaceRequest{
			Cwd: template.Cwd,
		},
	}
	req.Launch.ExtraArgs = []string{
		"--model", "Seed-2.1-Turbo",
		"-c", `mcp_servers.amux.command="/bin/amux"`,
		"-c", `mcp_servers.amux.env={AMUX_SESSION_ID="old-sess", AMUX_ACTOR_TOKEN="old-tok"}`,
	}
	req.Launch.Env = []string{
		"PATH=/usr/bin",
		"AMUX_ACTOR_TOKEN=old-tok",
		"AMUX_SESSION_ID=old-sess",
		"AMUX_MEMORY_BINDING=old-mem",
	}
	if err := srv.prepareScheduledNewSession(context.Background(), schedule.Task{ID: 1, OwnerPrincipalID: alice.ID}, template, req); err != nil {
		t.Fatalf("prepareScheduledNewSession: %v", err)
	}
	if req.ExecutionIdentity == nil || req.ExecutionIdentity.Source != string(principal.SourceSchedule) {
		t.Fatalf("execution identity = %+v, want source=schedule", req.ExecutionIdentity)
	}
	if req.ExecutionIdentity.EffectivePrincipalId != alice.ID || req.LaunchID == "" || req.AgentActorToken == "" {
		t.Fatalf("expected refreshed identity, launch id, and actor token: %+v", req)
	}
	if req.Launch.ProfileSpec == nil || req.Launch.ProfileSpec.Type != "codex" || req.Launch.ProfileSpec.Model != "Seed-2.1-Turbo" {
		t.Fatalf("expected resolved codex profile spec with type & model, got %+v", req.Launch.ProfileSpec)
	}
	if req.Workspace.Cwd != "" {
		t.Fatalf("expected Linux DAC isolated template Cwd to be cleared, got %q", req.Workspace.Cwd)
	}
	if len(req.Launch.ExtraArgs) != 2 || req.Launch.ExtraArgs[0] != "--model" || req.Launch.ExtraArgs[1] != "Seed-2.1-Turbo" {
		t.Fatalf("expected stale mcp_servers.amux.* args stripped while preserving --model, got %v", req.Launch.ExtraArgs)
	}
	if len(req.Launch.Env) != 1 || req.Launch.Env[0] != "PATH=/usr/bin" {
		t.Fatalf("expected session-scoped AMUX_* env stripped while preserving PATH, got %v", req.Launch.Env)
	}
	if req.Workspace.Cwd != "" {
		t.Fatalf("expected Linux DAC isolated template Cwd to be cleared, got %q", req.Workspace.Cwd)
	}
	if len(req.Launch.ExtraArgs) != 2 || req.Launch.ExtraArgs[0] != "--model" || req.Launch.ExtraArgs[1] != "Seed-2.1-Turbo" {
		t.Fatalf("expected stale mcp_servers.amux.* args stripped while preserving --model, got %v", req.Launch.ExtraArgs)
	}
	if len(req.Launch.Env) != 1 || req.Launch.Env[0] != "PATH=/usr/bin" {
		t.Fatalf("expected session-scoped AMUX_* env stripped while preserving PATH, got %v", req.Launch.Env)
	}
	if req.Workspace.Cwd != "" {
		t.Fatalf("expected Linux DAC isolated template Cwd to be cleared, got %q", req.Workspace.Cwd)
	}
	if len(req.Launch.ExtraArgs) != 2 || req.Launch.ExtraArgs[0] != "--model" || req.Launch.ExtraArgs[1] != "Seed-2.1-Turbo" {
		t.Fatalf("expected stale mcp_servers.amux.* args stripped while preserving --model, got %v", req.Launch.ExtraArgs)
	}
	if len(req.Launch.Env) != 1 || req.Launch.Env[0] != "PATH=/usr/bin" {
		t.Fatalf("expected session-scoped AMUX_* env stripped while preserving PATH, got %v", req.Launch.Env)
	}
	if req.Workspace.Cwd != "" {
		t.Fatalf("expected Linux DAC isolated template Cwd to be cleared, got %q", req.Workspace.Cwd)
	}
	if len(req.Launch.ExtraArgs) != 2 || req.Launch.ExtraArgs[0] != "--model" || req.Launch.ExtraArgs[1] != "Seed-2.1-Turbo" {
		t.Fatalf("expected stale mcp_servers.amux.* args stripped while preserving --model, got %v", req.Launch.ExtraArgs)
	}
	if len(req.Launch.Env) != 1 || req.Launch.Env[0] != "PATH=/usr/bin" {
		t.Fatalf("expected session-scoped AMUX_* env stripped while preserving PATH, got %v", req.Launch.Env)
	}
	// Disabling the principal must cause prepareScheduledNewSession to refuse launch.
	if err := principals.Disable(alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := srv.prepareScheduledNewSession(context.Background(), schedule.Task{ID: 1, OwnerPrincipalID: alice.ID}, template, &sessionlaunch.StartRequest{TargetID: "t1", Name: "sched-1-20260601-100001-2"}); err == nil {
		t.Fatal("expected disabled principal to fail prepareScheduledNewSession")
	}
}

func TestSetScheduleEnabledRecomputesStaleNextRunAndRejectsExpiredOnce(t *testing.T) {
	store := newScheduleTestStore(t)
	srv := &hubServer{schedule: store}

	// 1) Disabled recurring schedule with stale next_run_at in the past.
	stale := time.Now().Add(-48 * time.Hour).Unix()
	recurring, err := store.Create(schedule.Task{
		Name:        "daily-check",
		TargetID:    "t1",
		SessionName: "s1",
		Prompt:      "check",
		Spec:        "every 30m",
		Kind:        schedule.KindInterval,
		Enabled:     false,
		NextRunAt:   &stale,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().Unix()
	resp, err := srv.SetScheduleEnabled(context.Background(), &pb.SetScheduleEnabledRequest{Id: recurring.ID, Enabled: true})
	if err != nil || !resp.Ok {
		t.Fatalf("SetScheduleEnabled recurring = %+v, %v", resp, err)
	}
	got, err := store.Get(recurring.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.NextRunAt == nil || *got.NextRunAt <= before {
		t.Fatalf("re-enabled recurring schedule kept stale next_run_at: enabled=%v next=%v before=%d", got.Enabled, got.NextRunAt, before)
	}

	// 2) Disabled recurring schedule whose next_run_at is still in the future preserves it.
	future := time.Now().Add(2 * time.Hour).Unix()
	futureRow, err := store.Create(schedule.Task{
		Name:        "future-check",
		TargetID:    "t1",
		SessionName: "s1",
		Prompt:      "check",
		Spec:        "every 30m",
		Kind:        schedule.KindInterval,
		Enabled:     false,
		NextRunAt:   &future,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.SetScheduleEnabled(context.Background(), &pb.SetScheduleEnabledRequest{Id: futureRow.ID, Enabled: true}); err != nil {
		t.Fatalf("SetScheduleEnabled future: %v", err)
	}
	gotFuture, err := store.Get(futureRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotFuture.NextRunAt == nil || *gotFuture.NextRunAt != future {
		t.Fatalf("future next_run_at = %v, want preserved %d", gotFuture.NextRunAt, future)
	}

	// 3) Expired one-shot schedule cannot be re-enabled without updating spec.
	onceRow, err := store.Create(schedule.Task{
		Name:        "past-once",
		TargetID:    "t1",
		SessionName: "s1",
		Prompt:      "ping",
		Spec:        "at 2020-01-01 09:00",
		Kind:        schedule.KindOnce,
		Enabled:     false,
		NextRunAt:   nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = srv.SetScheduleEnabled(context.Background(), &pb.SetScheduleEnabledRequest{Id: onceRow.ID, Enabled: true})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expired one-shot enable err = %v (code %v), want FailedPrecondition", err, status.Code(err))
	}
}

func TestScheduleCreateUpdateTimezoneAndRelativeInNotifiesScheduler(t *testing.T) {
	store := newScheduleTestStore(t)
	notified := 0
	srv := &hubServer{schedule: store, schedulerNotify: func() { notified++ }}

	// 1) Create relative "in 10m" schedule in Asia/Shanghai.
	resp, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		Name:        "rel-once",
		TargetId:    "t1",
		SessionName: "s1",
		Prompt:      "ping",
		Spec:        "in 10m",
		Timezone:    "Asia/Shanghai",
	})
	if err != nil || resp.Error != "" || resp.Id == 0 {
		t.Fatalf("CreateSchedule(in 10m) = %+v, %v", resp, err)
	}
	if notified != 1 {
		t.Fatalf("schedulerNotify calls = %d, want 1", notified)
	}
	got, err := srv.GetSchedule(context.Background(), &pb.GetScheduleRequest{Id: resp.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Schedule.Timezone != "Asia/Shanghai" || got.Schedule.Kind != schedule.KindOnce {
		t.Fatalf("saved schedule = %+v", got.Schedule)
	}

	// 2) Update timezone on a daily schedule recomputes next_run_at.
	dailyResp, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		Name:        "morning",
		TargetId:    "t1",
		SessionName: "s1",
		Prompt:      "morning",
		Spec:        "daily 09:00",
		Timezone:    "UTC",
	})
	if err != nil || dailyResp.Error != "" {
		t.Fatalf("CreateSchedule daily = %+v, %v", dailyResp, err)
	}
	newTZ := "Asia/Shanghai"
	upd, err := srv.UpdateSchedule(context.Background(), &pb.UpdateScheduleRequest{
		Id:       dailyResp.Id,
		Timezone: &newTZ,
	})
	if err != nil || upd.Error != "" {
		t.Fatalf("UpdateSchedule timezone = %+v, %v", upd, err)
	}
	if upd.Schedule.Timezone != "Asia/Shanghai" || upd.Schedule.NextRunAt == dailyResp.NextRunAt {
		t.Fatalf("expected timezone update to recompute next_run_at: old=%d new=%+v", dailyResp.NextRunAt, upd.Schedule)
	}
	if notified != 3 {
		t.Fatalf("schedulerNotify calls = %d, want 3", notified)
	}
}

func TestTriggerScheduleRPCAndCompletedStateAndErrorSummary(t *testing.T) {
	store := newScheduleTestStore(t)
	triggeredID := int64(0)
	srv := &hubServer{
		schedule: store,
		scheduleTrigger: func(_ context.Context, id int64) (schedule.Execution, string, error) {
			triggeredID = id
			return schedule.Execution{ID: 77, ScheduleID: id, TaskID: "task-now", TriggerSource: schedule.TriggerSourceManual, Status: "submitted"}, "ok:task:task-now", nil
		},
	}
	future := time.Now().Add(time.Hour).Unix()
	row, err := store.Create(schedule.Task{
		Name:         "nightly",
		ScheduleType: schedule.ScheduleTypeSubtask,
		Spec:         "daily 09:00",
		Kind:         schedule.KindDaily,
		TaskSpecJson: []byte(`{"agent":"builtin/codex","task":"check"}`),
		Enabled:      true,
		NextRunAt:    &future,
	})
	if err != nil {
		t.Fatal(err)
	}
	trigResp, err := srv.TriggerSchedule(context.Background(), &pb.TriggerScheduleRequest{Id: row.ID})
	if err != nil {
		t.Fatalf("TriggerSchedule: %v", err)
	}
	if !trigResp.Ok || trigResp.ExecutionId != 77 || trigResp.TaskId != "task-now" || triggeredID != row.ID {
		t.Fatalf("TriggerSchedule resp = %+v", trigResp)
	}

	// Consumed one-shot reports state=completed in GetSchedule.
	past := time.Now().Add(-time.Minute).Unix()
	once, err := store.Create(schedule.Task{
		Name:        "one-shot",
		TargetID:    "t1",
		SessionName: "s1",
		Prompt:      "done",
		Spec:        "at 2026-01-01 09:00",
		Kind:        schedule.KindOnce,
		Enabled:     true,
		NextRunAt:   &past,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRun(once.ID, past, nil, "completed"); err != nil {
		t.Fatal(err)
	}
	gotOnce, err := srv.GetSchedule(context.Background(), &pb.GetScheduleRequest{Id: once.ID})
	if err != nil {
		t.Fatal(err)
	}
	if gotOnce.Schedule.State != schedule.StateCompleted {
		t.Fatalf("consumed one-shot state = %q, want completed", gotOnce.Schedule.State)
	}

	// Failed subtask with nil Result falls back to run.Error (truncated at 500 runes).
	obs, err := applyTaskRunObservation(schedule.Execution{TaskID: "task-err"}, task.TaskRun{
		ID:     "task-err",
		Status: task.StatusFailed,
		Error:  "runtime launch failed: missing credential",
	})
	if err != nil {
		t.Fatalf("applyTaskRunObservation: %v", err)
	}
	if obs.Status != "failed" || !obs.Terminal || obs.Result != "runtime launch failed: missing credential" {
		t.Fatalf("obs = %+v, want failed with run.Error summary", obs)
	}
}

func TestObserveScheduledSubtaskUnderFleetIsolation(t *testing.T) {
	principals := agentTokenTestStore(t)
	alice, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	id := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: alice.ID,
		EffectivePrincipalID:   alice.ID,
		Source:                 principal.SourceSchedule,
		IsolationBackend:       principal.BackendLinuxDAC,
	}
	runJSON, err := json.Marshal(task.TaskRun{
		ID:                "task-iso-1",
		Status:            task.StatusSucceeded,
		TargetID:          "rt-1",
		ExecutionIdentity: id,
		Result:            &task.TaskResult{Status: task.ResultOK, Summary: "ISOLATED_OK"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := &fakeRouterHub{
		targets: isolatingTargets("rt-1"),
		routeFn: func(_ string, method string, _ interface{}) (*pb.TunnelMsg, error) {
			if method == pb.TaskMethodGet {
				return respJSON(t, &pb.GetSubtaskResponse{TaskJson: runJSON}), nil
			}
			return nil, status.Error(codes.Unimplemented, method)
		},
	}
	idx := newTaskIndex()
	idx.put("task-iso-1", taskPlacement{TargetID: "rt-1"})
	router := newTaskRouter(hub, &fakeScheduler{}, idx, nil)
	router.principals = principals

	// External gRPC GetSubtask without caller credentials must still reject.
	if _, err := router.GetSubtask(context.Background(), &pb.GetSubtaskRequest{Id: "task-iso-1"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("GetSubtask(unauthenticated) err = %v, want Unauthenticated", err)
	}

	// Control-plane schedule observation must succeed, clear stale observation_unavailable reason, and surface the terminal summary.
	ctrl := &bridgeSchedules{reader: &bridgeSubtaskReader{router: router}}
	obs, err := ctrl.ObserveScheduleExecution(context.Background(), schedule.Execution{TaskID: "task-iso-1", Reason: "observation_unavailable"})
	if err != nil {
		t.Fatalf("ObserveScheduleExecution: %v", err)
	}
	if obs.Status != "completed" || !obs.Terminal || obs.Result != "ISOLATED_OK" || obs.Reason != "" {
		t.Fatalf("obs = %+v, want completed/ISOLATED_OK with empty reason", obs)
	}
}
