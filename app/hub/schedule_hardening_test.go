package hub

import (
	"context"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/sessionlaunch"
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
	}
	template := &pb.Session{
		Name:               "template-s1",
		AgentProfile:       "builtin/codex",
		AgentType:          "codex",
		IdleTimeoutSeconds: 900,
		ExecutionIdentity:  principal.ToProtoIdentity(origIdentity),
	}
	srv := &hubServer{principals: principals}
	req := &sessionlaunch.StartRequest{TargetID: "t1", Name: "sched-1-20260601-100000-1"}
	if err := srv.prepareScheduledNewSession(context.Background(), schedule.Task{ID: 1, OwnerPrincipalID: alice.ID}, template, req); err != nil {
		t.Fatalf("prepareScheduledNewSession: %v", err)
	}
	if req.ExecutionIdentity == nil || req.ExecutionIdentity.Source != string(principal.SourceSchedule) {
		t.Fatalf("execution identity = %+v, want source=schedule", req.ExecutionIdentity)
	}
	if req.ExecutionIdentity.EffectivePrincipalId != alice.ID || req.LaunchID == "" || req.AgentActorToken == "" {
		t.Fatalf("expected refreshed identity, launch id, and actor token: %+v", req)
	}
	if req.Launch.ProfileSpec == nil || req.Launch.AgentProfile != "builtin/codex" {
		t.Fatalf("expected resolved builtin/codex profile spec, got %+v", req.Launch)
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
