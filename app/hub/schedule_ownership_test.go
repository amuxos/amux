package hub

import (
	"context"
	"testing"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func actorCtx(token string) context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("x-amux-actor-token", token))
}

// agentCaller mirrors a Feishu launch: a principal of its own, reaching the hub
// with the delegated credential its agent carries. Two of these are two Feishu
// users, which is the boundary a schedule has to respect.
func agentCaller(t *testing.T, principals *principal.Store, name string) context.Context {
	t.Helper()
	p, err := principals.Create(principal.Principal{
		Kind: principal.KindHuman, DisplayName: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := principals.IssueAgentCredential(p.ID, "launch-"+name, principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	return actorCtx(cred.Token)
}

func sessionScheduleRequest(name string) *pb.CreateScheduleRequest {
	return &pb.CreateScheduleRequest{
		Name:        name,
		Spec:        "every 30m",
		TargetId:    "t1",
		SessionName: "s1",
		Prompt:      "any news?",
		CreatedBy:   "mcp",
	}
}

func ownershipTestServer(t *testing.T) *hubServer {
	t.Helper()
	return &hubServer{
		schedule:   newScheduleTestStore(t),
		principals: agentTokenTestStore(t),
		// The ownership rule (rule four) applies only while an isolating
		// runtime is registered, so fixtures asserting the narrowing declare
		// one. Tests for the relaxed no-isolation behavior build their own
		// server without this.
		listTargets: func() []*pb.Target { return isolatingTargets("t1") },
	}
}

func mustCreateSchedule(t *testing.T, srv *hubServer, ctx context.Context, name string) int64 {
	t.Helper()
	created, err := srv.CreateSchedule(ctx, sessionScheduleRequest(name))
	if err != nil {
		t.Fatalf("CreateSchedule(%s): %v", name, err)
	}
	if created.Error != "" {
		t.Fatalf("CreateSchedule(%s): %s", name, created.Error)
	}
	return created.Id
}

// One hub serves every Feishu user, so an unfiltered list handed each of them
// the whole installation's schedules — including the ones they could then
// delete.
func TestListSchedulesShowsOnlyTheCallersOwnSchedules(t *testing.T) {
	srv := ownershipTestServer(t)
	alice := agentCaller(t, srv.principals, "alice")
	bob := agentCaller(t, srv.principals, "bob")
	aliceID := mustCreateSchedule(t, srv, alice, "alice standup")
	bobID := mustCreateSchedule(t, srv, bob, "bob standup")

	listed, err := srv.ListSchedules(bob, &pb.ListSchedulesRequest{})
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	var ids []int64
	for _, item := range listed.Schedules {
		ids = append(ids, item.Id)
	}
	if len(ids) != 1 || ids[0] != bobID {
		t.Fatalf("bob sees schedules %v, want only his own (%d)", ids, bobID)
	}

	listed, err = srv.ListSchedules(alice, &pb.ListSchedulesRequest{})
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(listed.Schedules) != 1 || listed.Schedules[0].Id != aliceID {
		t.Fatalf("alice sees %+v, want only her own (%d)", listed.Schedules, aliceID)
	}
}

func TestGetScheduleRefusesAnotherPrincipalsSchedule(t *testing.T) {
	srv := ownershipTestServer(t)
	alice := agentCaller(t, srv.principals, "alice")
	bob := agentCaller(t, srv.principals, "bob")
	id := mustCreateSchedule(t, srv, alice, "alice standup")

	if _, err := srv.GetSchedule(bob, &pb.GetScheduleRequest{Id: id}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetSchedule error = %v (code %v), want PermissionDenied", err, status.Code(err))
	}
	if _, err := srv.GetSchedule(alice, &pb.GetScheduleRequest{Id: id}); err != nil {
		t.Fatalf("owner cannot read her own schedule: %v", err)
	}
}

func TestDeleteScheduleRefusesAnotherPrincipalsSchedule(t *testing.T) {
	srv := ownershipTestServer(t)
	alice := agentCaller(t, srv.principals, "alice")
	bob := agentCaller(t, srv.principals, "bob")
	id := mustCreateSchedule(t, srv, alice, "alice standup")

	if _, err := srv.DeleteSchedule(bob, &pb.DeleteScheduleRequest{Id: id}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("DeleteSchedule error = %v (code %v), want PermissionDenied", err, status.Code(err))
	}
	// A refusal that still deleted the row would be worse than no check at all.
	if _, err := srv.GetSchedule(alice, &pb.GetScheduleRequest{Id: id}); err != nil {
		t.Fatalf("schedule did not survive the refused delete: %v", err)
	}
	if _, err := srv.DeleteSchedule(alice, &pb.DeleteScheduleRequest{Id: id}); err != nil {
		t.Fatalf("owner cannot delete her own schedule: %v", err)
	}
}

func TestSetScheduleEnabledRefusesAnotherPrincipalsSchedule(t *testing.T) {
	srv := ownershipTestServer(t)
	alice := agentCaller(t, srv.principals, "alice")
	bob := agentCaller(t, srv.principals, "bob")
	id := mustCreateSchedule(t, srv, alice, "alice standup")

	_, err := srv.SetScheduleEnabled(bob, &pb.SetScheduleEnabledRequest{Id: id, Enabled: false})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("SetScheduleEnabled error = %v (code %v), want PermissionDenied", err, status.Code(err))
	}
	if _, err := srv.SetScheduleEnabled(alice, &pb.SetScheduleEnabledRequest{Id: id, Enabled: false}); err != nil {
		t.Fatalf("owner cannot disable her own schedule: %v", err)
	}
}

func TestUpdateScheduleRefusesAnotherPrincipalsSchedule(t *testing.T) {
	srv := ownershipTestServer(t)
	alice := agentCaller(t, srv.principals, "alice")
	bob := agentCaller(t, srv.principals, "bob")
	id := mustCreateSchedule(t, srv, alice, "alice standup")

	renamed := "bob's now"
	_, err := srv.UpdateSchedule(bob, &pb.UpdateScheduleRequest{Id: id, Name: &renamed})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("UpdateSchedule error = %v (code %v), want PermissionDenied", err, status.Code(err))
	}
	got, err := srv.GetSchedule(alice, &pb.GetScheduleRequest{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Schedule.Name != "alice standup" {
		t.Fatalf("schedule name = %q, want it untouched by the refused update", got.Schedule.Name)
	}
}

// An installation that never enrolled anyone attributes nothing, so scoping
// there would hide every schedule from the only user there is. Local CLI-only
// usage has to keep working exactly as before.
func TestSchedulesStaySharedWhereNobodyIsIdentified(t *testing.T) {
	srv := &hubServer{schedule: newScheduleTestStore(t)}
	ctx := context.Background()
	id := mustCreateSchedule(t, srv, ctx, "nightly")

	listed, err := srv.ListSchedules(ctx, &pb.ListSchedulesRequest{})
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(listed.Schedules) != 1 {
		t.Fatalf("listed %d schedules, want the one that exists", len(listed.Schedules))
	}
	if _, err := srv.GetSchedule(ctx, &pb.GetScheduleRequest{Id: id}); err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if _, err := srv.DeleteSchedule(ctx, &pb.DeleteScheduleRequest{Id: id}); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
}

// Schedules created before this rule existed carry no owner. Nobody can prove
// they own them, so they stay reachable rather than becoming undeletable.
func TestSchedulesWithoutAnOwnerRemainOperable(t *testing.T) {
	srv := ownershipTestServer(t)
	stored, err := srv.schedule.Create(schedule.Task{
		Name: "legacy nightly", ScheduleType: schedule.ScheduleTypeSessionPrompt,
		TargetID: "t1", SessionName: "s1", Prompt: "any news?",
		Spec: "every 30m", Kind: schedule.KindInterval, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := stored.ID
	alice := agentCaller(t, srv.principals, "alice")

	listed, err := srv.ListSchedules(alice, &pb.ListSchedulesRequest{})
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(listed.Schedules) != 1 || listed.Schedules[0].Id != legacy {
		t.Fatalf("listed %+v, want the unowned schedule %d", listed.Schedules, legacy)
	}
	if _, err := srv.DeleteSchedule(alice, &pb.DeleteScheduleRequest{Id: legacy}); err != nil {
		t.Fatalf("unowned schedule is not deletable: %v", err)
	}
}

// Dropping the credential must not be a way around the rule.
func TestUnidentifiedCallerCannotReachAnOwnedSchedule(t *testing.T) {
	srv := ownershipTestServer(t)
	alice := agentCaller(t, srv.principals, "alice")
	id := mustCreateSchedule(t, srv, alice, "alice standup")

	listed, err := srv.ListSchedules(context.Background(), &pb.ListSchedulesRequest{})
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(listed.Schedules) != 0 {
		t.Fatalf("an unidentified caller listed %+v, want nothing owned", listed.Schedules)
	}
	_, err = srv.DeleteSchedule(context.Background(), &pb.DeleteScheduleRequest{Id: id})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("DeleteSchedule error = %v (code %v), want PermissionDenied", err, status.Code(err))
	}
}

// newPrincipal returns a principal and a caller acting as it.
func newPrincipal(t *testing.T, principals *principal.Store, name string) (string, context.Context) {
	t.Helper()
	p, err := principals.Create(principal.Principal{
		Kind: principal.KindHuman, DisplayName: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := principals.IssueAgentCredential(p.ID, "launch-"+name, principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID, actorCtx(cred.Token)
}

func identifiedSessionFleet(sessionName, principalID string) func() []*pb.Target {
	return func() []*pb.Target {
		return []*pb.Target{{
			Id:           "t1",
			Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"},
			Sessions: []*pb.Session{{
				Name: sessionName,
				ExecutionIdentity: &pb.ExecutionIdentity{
					IdentityMode:           string(principal.IdentityModeIdentified),
					OriginActorPrincipalId: principalID,
					EffectivePrincipalId:   principalID,
				},
			}},
		}}
	}
}

func identifiedSession(id, name, launchID, principalID string) *pb.Session {
	return &pb.Session{
		Id: id, Name: name, LaunchId: launchID, State: "running",
		ExecutionIdentity: &pb.ExecutionIdentity{
			IdentityMode:           string(principal.IdentityModeIdentified),
			OriginActorPrincipalId: principalID,
			EffectivePrincipalId:   principalID,
		},
	}
}

func scheduleRows(t *testing.T, srv *hubServer) []schedule.Task {
	t.Helper()
	rows, err := srv.schedule.List(schedule.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCreateSessionPromptScheduleAuthorizesVisibleOwnSession(t *testing.T) {
	srv := ownershipTestServer(t)
	aliceID, alice := newPrincipal(t, srv.principals, "alice")
	srv.listTargets = func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Sessions: []*pb.Session{
			identifiedSession("alice-session", "s1", "launch-alice", aliceID),
		}}}
	}

	id := mustCreateSchedule(t, srv, alice, "alice standup")
	row, err := srv.schedule.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.OwnerPrincipalID != aliceID {
		t.Fatalf("owner = %q, want %q", row.OwnerPrincipalID, aliceID)
	}
}

func TestCreateSessionPromptScheduleRefusesAnotherPrincipalsSession(t *testing.T) {
	srv := ownershipTestServer(t)
	_, alice := newPrincipal(t, srv.principals, "alice")
	bobID, _ := newPrincipal(t, srv.principals, "bob")
	srv.listTargets = func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"}, Sessions: []*pb.Session{
			identifiedSession("bob-session", "s1", "launch-bob", bobID),
		}}}
	}

	_, err := srv.CreateSchedule(alice, sessionScheduleRequest("cross-principal"))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("CreateSchedule error = %v (code %v), want PermissionDenied", err, status.Code(err))
	}
	if rows := scheduleRows(t, srv); len(rows) != 0 {
		t.Fatalf("refused create stored rows: %+v", rows)
	}
}

func TestCreateSessionPromptScheduleAllowsSamePrincipalSibling(t *testing.T) {
	srv := ownershipTestServer(t)
	aliceID, alice := newPrincipal(t, srv.principals, "alice")
	srv.listTargets = func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"}, Sessions: []*pb.Session{
			identifiedSession("caller-session", "caller", "launch-alice", aliceID),
			identifiedSession("other-session", "s1", "launch-other", aliceID),
		}}}
	}

	id := mustCreateSchedule(t, srv, alice, "same-principal-sibling")
	row, err := srv.schedule.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.OwnerPrincipalID != aliceID {
		t.Fatalf("owner = %q, want %q", row.OwnerPrincipalID, aliceID)
	}
}

func TestCreateSessionPromptScheduleDefersUnknownTargetOnlyForAuthenticatedCaller(t *testing.T) {
	srv := ownershipTestServer(t)
	aliceID, alice := newPrincipal(t, srv.principals, "alice")
	srv.listTargets = func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"}}}
	}

	id := mustCreateSchedule(t, srv, alice, "deferred-own-target")
	row, err := srv.schedule.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.OwnerPrincipalID != aliceID {
		t.Fatalf("deferred owner = %q, want %q", row.OwnerPrincipalID, aliceID)
	}

	_, err = srv.CreateSchedule(context.Background(), sessionScheduleRequest("unowned-name-squat"))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unidentified deferred create error = %v (code %v), want Unauthenticated", err, status.Code(err))
	}
	if rows := scheduleRows(t, srv); len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("unidentified deferred create changed rows: %+v", rows)
	}
}

func TestCreateSessionPromptScheduleKeepsVisibleLegacyHostCompatibility(t *testing.T) {
	srv := ownershipTestServer(t)
	srv.listTargets = func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{
			Id: "legacy-session", Name: "s1", State: "running",
			ExecutionIdentity: &pb.ExecutionIdentity{IdentityMode: string(principal.IdentityModeLegacyHost)},
		}}}}
	}

	id := mustCreateSchedule(t, srv, context.Background(), "legacy-visible")
	row, err := srv.schedule.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.OwnerPrincipalID != "" {
		t.Fatalf("legacy-host owner = %q, want empty", row.OwnerPrincipalID)
	}
}

func TestScheduledSessionPromptReauthorizesFrozenOwner(t *testing.T) {
	srv := ownershipTestServer(t)
	aliceID, _ := newPrincipal(t, srv.principals, "alice")
	bobID, _ := newPrincipal(t, srv.principals, "bob")
	aliceSession := identifiedSession("alice-session", "s1", "launch-alice", aliceID)

	if err := srv.authorizeScheduledSessionPrompt(context.Background(), schedule.Task{
		ID: 1, OwnerPrincipalID: aliceID,
	}, aliceSession); err != nil {
		t.Fatalf("owner cannot trigger its own session schedule: %v", err)
	}
	if err := srv.authorizeScheduledSessionPrompt(context.Background(), schedule.Task{
		ID: 2, OwnerPrincipalID: bobID,
	}, aliceSession); err == nil {
		t.Fatal("cross-principal schedule owner was authorized")
	}
}

func TestScheduledSessionPromptRechecksRevokedDelegation(t *testing.T) {
	srv := ownershipTestServer(t)
	aliceID, _ := newPrincipal(t, srv.principals, "alice")
	bobID, _ := newPrincipal(t, srv.principals, "bob")
	delegationID, err := srv.principals.GrantDelegation(aliceID, bobID)
	if err != nil {
		t.Fatal(err)
	}
	bobSession := identifiedSession("bob-session", "s1", "launch-bob", bobID)
	row := schedule.Task{ID: 1, OwnerPrincipalID: aliceID}

	if err := srv.authorizeScheduledSessionPrompt(context.Background(), row, bobSession); err != nil {
		t.Fatalf("active delegation was rejected: %v", err)
	}
	if err := srv.principals.RevokeDelegation(delegationID); err != nil {
		t.Fatal(err)
	}
	if err := srv.authorizeScheduledSessionPrompt(context.Background(), row, bobSession); err == nil {
		t.Fatal("revoked delegation still authorized scheduled injection")
	}
}

func TestScheduledSessionPromptBackfillsLegacyOwnerAtTrigger(t *testing.T) {
	srv := ownershipTestServer(t)
	aliceID, _ := newPrincipal(t, srv.principals, "alice")
	aliceSession := identifiedSession("alice-session", "s1", "launch-alice", aliceID)
	stored, err := srv.schedule.Create(schedule.Task{
		Name: "pre-owner", ScheduleType: schedule.ScheduleTypeSessionPrompt,
		TargetID: "t1", SessionName: "s1", Prompt: "tick",
		Spec: "every 30m", Kind: schedule.KindInterval, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := srv.authorizeScheduledSessionPrompt(context.Background(), stored, aliceSession); err != nil {
		t.Fatalf("legacy row did not derive target owner: %v", err)
	}
	got, err := srv.schedule.Get(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerPrincipalID != aliceID {
		t.Fatalf("backfilled owner = %q, want %q", got.OwnerPrincipalID, aliceID)
	}
}

// A schedule stored before ownership was recorded has no owner of its own, but
// it is not unattributable: a session-prompt schedule drives one session, and
// that session already carries an execution identity. Treating those rows as
// shared left every pre-upgrade schedule operable by everyone — which is the
// case that was actually reported.
func TestLegacySessionScheduleBelongsToItsSessionsPrincipal(t *testing.T) {
	srv := ownershipTestServer(t)
	aliceID, alice := newPrincipal(t, srv.principals, "alice")
	_, bob := newPrincipal(t, srv.principals, "bob")
	srv.listTargets = identifiedSessionFleet("s1", aliceID)

	// Stored directly, the way a pre-upgrade row looks: no owner column value.
	stored, err := srv.schedule.Create(schedule.Task{
		Name: "alice nightly", ScheduleType: schedule.ScheduleTypeSessionPrompt,
		TargetID: "t1", SessionName: "s1", Prompt: "any news?",
		Spec: "every 30m", Kind: "interval", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	listed, err := srv.ListSchedules(bob, &pb.ListSchedulesRequest{})
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(listed.Schedules) != 0 {
		t.Fatalf("bob sees %+v, want none of alice's session schedules", listed.Schedules)
	}
	_, err = srv.SetScheduleEnabled(bob, &pb.SetScheduleEnabledRequest{Id: stored.ID, Enabled: false})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("SetScheduleEnabled error = %v (code %v), want PermissionDenied", err, status.Code(err))
	}
	if _, err := srv.GetSchedule(alice, &pb.GetScheduleRequest{Id: stored.ID}); err != nil {
		t.Fatalf("the session's own principal cannot reach the schedule: %v", err)
	}

	// The derived owner is written back, so the row stops depending on the
	// session still being registered to stay protected.
	row, err := srv.schedule.Get(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.OwnerPrincipalID != aliceID {
		t.Fatalf("stored owner = %q, want it backfilled to %s", row.OwnerPrincipalID, aliceID)
	}
}

// A subtask schedule froze its execution identity at creation, so a pre-upgrade
// row carries its creator even without the owner column.
func TestLegacySubtaskScheduleBelongsToItsFrozenIdentity(t *testing.T) {
	srv := ownershipTestServer(t)
	aliceID, alice := newPrincipal(t, srv.principals, "alice")
	_, bob := newPrincipal(t, srv.principals, "bob")

	stored, err := srv.schedule.Create(schedule.Task{
		Name: "alice review", ScheduleType: schedule.ScheduleTypeSubtask,
		Spec: "every 30m", Kind: "interval", Enabled: true,
		TaskSpecJson: mustTaskSpecJson(t, task.Spec{
			Agent: "builtin/codex", Task: "review",
			ExecutionIdentity: &principal.ExecutionIdentity{
				IdentityMode:           principal.IdentityModeIdentified,
				OriginActorPrincipalID: aliceID,
				EffectivePrincipalID:   aliceID,
			},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = srv.DeleteSchedule(bob, &pb.DeleteScheduleRequest{Id: stored.ID})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("DeleteSchedule error = %v (code %v), want PermissionDenied", err, status.Code(err))
	}
	if _, err := srv.DeleteSchedule(alice, &pb.DeleteScheduleRequest{Id: stored.ID}); err != nil {
		t.Fatalf("the frozen identity's principal cannot delete its own schedule: %v", err)
	}
}

// Only rows with nothing to attribute stay shared: a legacy-host session cannot
// name a principal, so its schedule must not become unreachable.
func TestLegacyHostSessionScheduleStaysShared(t *testing.T) {
	srv := ownershipTestServer(t)
	_, bob := newPrincipal(t, srv.principals, "bob")
	srv.listTargets = func() []*pb.Target {
		return []*pb.Target{{
			Id:           "t1",
			Capabilities: &pb.TargetCapabilities{},
			Sessions: []*pb.Session{{
				Name:              "s1",
				ExecutionIdentity: &pb.ExecutionIdentity{IdentityMode: string(principal.IdentityModeLegacyHost)},
			}},
		}}
	}
	stored, err := srv.schedule.Create(schedule.Task{
		Name: "shared nightly", ScheduleType: schedule.ScheduleTypeSessionPrompt,
		TargetID: "t1", SessionName: "s1", Prompt: "any news?",
		Spec: "every 30m", Kind: "interval", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DeleteSchedule(bob, &pb.DeleteScheduleRequest{Id: stored.ID}); err != nil {
		t.Fatalf("a legacy-host schedule became unreachable: %v", err)
	}
}

// ── no-isolation fleet: schedule ownership is not enforced ──────────────────
//
// Rule four mirrors the per-principal sandbox boundary. On a fleet with no
// isolating runtime there is no such boundary, so the hub returns and accepts
// every schedule regardless of owner — the pre-rule-four behavior. Ownership
// is still recorded, and enforcement resumes automatically once an isolating
// runtime registers.

// nonIsolatingOwnershipTestServer is ownershipTestServer without an isolating
// runtime: the fleet-wide identity boundary is off.
func nonIsolatingOwnershipTestServer(t *testing.T) *hubServer {
	t.Helper()
	return &hubServer{
		schedule:   newScheduleTestStore(t),
		principals: agentTokenTestStore(t),
	}
}

func TestListSchedulesShowsAllSchedulesWithoutIsolation(t *testing.T) {
	srv := nonIsolatingOwnershipTestServer(t)
	alice := agentCaller(t, srv.principals, "alice")
	bob := agentCaller(t, srv.principals, "bob")
	aliceID := mustCreateSchedule(t, srv, alice, "alice standup")
	bobID := mustCreateSchedule(t, srv, bob, "bob standup")

	listed, err := srv.ListSchedules(bob, &pb.ListSchedulesRequest{})
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	var ids []int64
	for _, item := range listed.Schedules {
		ids = append(ids, item.Id)
	}
	if len(ids) != 2 || !containsInt64(ids, aliceID) || !containsInt64(ids, bobID) {
		t.Fatalf("bob sees schedules %v, want both (%d, %d) without isolation", ids, aliceID, bobID)
	}

	// An unidentified caller sees them too.
	listed, err = srv.ListSchedules(context.Background(), &pb.ListSchedulesRequest{})
	if err != nil {
		t.Fatalf("ListSchedules unidentified: %v", err)
	}
	if len(listed.Schedules) != 2 {
		t.Fatalf("unidentified caller listed %d schedules, want both without isolation", len(listed.Schedules))
	}
}

func TestDeleteScheduleAllowsAnotherPrincipalsScheduleWithoutIsolation(t *testing.T) {
	srv := nonIsolatingOwnershipTestServer(t)
	alice := agentCaller(t, srv.principals, "alice")
	bob := agentCaller(t, srv.principals, "bob")
	id := mustCreateSchedule(t, srv, alice, "alice standup")

	if _, err := srv.GetSchedule(bob, &pb.GetScheduleRequest{Id: id}); err != nil {
		t.Fatalf("GetSchedule by another principal without isolation: %v", err)
	}
	if _, err := srv.DeleteSchedule(bob, &pb.DeleteScheduleRequest{Id: id}); err != nil {
		t.Fatalf("DeleteSchedule by another principal without isolation: %v", err)
	}
	if _, err := srv.GetSchedule(alice, &pb.GetScheduleRequest{Id: id}); status.Code(err) != codes.NotFound {
		t.Fatalf("GetSchedule after delete = %v (code %v), want NotFound", err, status.Code(err))
	}
}

func TestCreateSessionPromptScheduleAllowsForeignTargetWithoutIsolation(t *testing.T) {
	srv := nonIsolatingOwnershipTestServer(t)
	_, alice := newPrincipal(t, srv.principals, "alice")
	bobID, _ := newPrincipal(t, srv.principals, "bob")
	srv.listTargets = func() []*pb.Target {
		return []*pb.Target{{Id: "t1", Sessions: []*pb.Session{
			identifiedSession("bob-session", "s1", "launch-bob", bobID),
		}}}
	}

	if _, err := srv.CreateSchedule(alice, sessionScheduleRequest("cross-principal")); err != nil {
		t.Fatalf("CreateSchedule for foreign target without isolation: %v", err)
	}
}

func TestCreateSessionPromptScheduleAllowsUnavailableTargetWithoutOwnerWithoutIsolation(t *testing.T) {
	srv := nonIsolatingOwnershipTestServer(t)
	if _, err := srv.CreateSchedule(context.Background(), sessionScheduleRequest("shared-deferred")); err != nil {
		t.Fatalf("CreateSchedule for unavailable target without isolation: %v", err)
	}
}

func TestScheduledSessionPromptAllowsForeignOwnerWithoutIsolation(t *testing.T) {
	srv := nonIsolatingOwnershipTestServer(t)
	aliceID, _ := newPrincipal(t, srv.principals, "alice")
	bobID, _ := newPrincipal(t, srv.principals, "bob")
	bobSession := identifiedSession("bob-session", "s1", "launch-bob", bobID)

	if err := srv.authorizeScheduledSessionPrompt(context.Background(), schedule.Task{
		ID: 1, OwnerPrincipalID: aliceID,
	}, bobSession); err != nil {
		t.Fatalf("cross-principal scheduled prompt without isolation: %v", err)
	}
}

func containsInt64(xs []int64, want int64) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
