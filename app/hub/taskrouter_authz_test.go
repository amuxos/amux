package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/agentinstance"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// authzTestPrincipals builds a store with an owner and an intruder, and returns
// the owner's identified identity plus both principals.
func authzTestPrincipals(t *testing.T) (*principal.Store, principal.ExecutionIdentity, principal.Principal, principal.Principal) {
	t.Helper()
	store, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	owner, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	intruder, err := store.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "intruder"})
	if err != nil {
		t.Fatal(err)
	}
	identity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: owner.ID,
		EffectivePrincipalID:   owner.ID,
		Source:                 principal.SourceMCP,
	}
	return store, identity, owner, intruder
}

func TestAuthorizeExecutionReadRejectsUnauthenticatedOnIdentified(t *testing.T) {
	store, identity, _, _ := authzTestPrincipals(t)
	r := &taskRouter{principals: store, hub: &fakeRouterHub{targets: isolatingTargets("t1")}}
	err := r.authorizeExecutionRead(authlessCtx(), identity, "", "task")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated (err=%v)", status.Code(err), err)
	}
}

func TestAuthorizeExecutionReadRejectsForeignPrincipal(t *testing.T) {
	store, identity, _, intruder := authzTestPrincipals(t)
	cred, err := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	r := &taskRouter{principals: store, hub: &fakeRouterHub{targets: isolatingTargets("t1")}}
	err = r.authorizeExecutionRead(actorCtx(cred.Token), identity, "", "task")
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}

func TestAuthorizeExecutionReadAllowsOwner(t *testing.T) {
	store, identity, owner, _ := authzTestPrincipals(t)
	cred, err := store.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	r := &taskRouter{principals: store}
	if err := r.authorizeExecutionRead(actorCtx(cred.Token), identity, "", "task"); err != nil {
		t.Fatalf("owner read = %v, want nil", err)
	}
}

func TestAuthorizeExecutionReadAllowsLegacyHost(t *testing.T) {
	r := &taskRouter{principals: nil}
	if err := r.authorizeExecutionRead(authlessCtx(), principal.LegacyHostIdentity(principal.SourceMCP), "", "task"); err != nil {
		t.Fatalf("legacy-host read = %v, want nil", err)
	}
}

func TestAuthorizeExecutionReadAllowsNilStore(t *testing.T) {
	r := &taskRouter{principals: nil}
	identity := principal.ExecutionIdentity{IdentityMode: principal.IdentityModeIdentified, EffectivePrincipalID: "x"}
	if err := r.authorizeExecutionRead(authlessCtx(), identity, "", "task"); err != nil {
		t.Fatalf("nil-store read = %v, want nil", err)
	}
}

// authlessCtx is a context carrying no actor credential.
func authlessCtx() context.Context { return context.Background() }

type fakeStreamSubtaskServer struct {
	pb.AmuxTask_StreamSubtaskServer
	ctx  context.Context
	sent []*pb.SubtaskEvent
}

func (f *fakeStreamSubtaskServer) Context() context.Context { return f.ctx }
func (f *fakeStreamSubtaskServer) Send(ev *pb.SubtaskEvent) error {
	f.sent = append(f.sent, ev)
	return nil
}

func TestGetSubtaskRejectsForeignPrincipal(t *testing.T) {
	store, identity, _, intruder := authzTestPrincipals(t)
	cred, err := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	taskJSON, _ := json.Marshal(map[string]any{
		"id":                 "task-owned",
		"execution_identity": identity,
	})
	hub := &fakeRouterHub{
		targets: isolatingTargets("t1"),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			if method != pb.TaskMethodGet {
				return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
			}
			return respJSON(t, &pb.GetSubtaskResponse{TaskJson: taskJSON}), nil
		},
	}
	index := newTaskIndex()
	index.put("task-owned", taskPlacement{TargetID: "t1"})
	r := newTaskRouter(hub, &fakeScheduler{}, index, nil)
	r.principals = store

	_, err = r.GetSubtask(actorCtx(cred.Token), &pb.GetSubtaskRequest{Id: "task-owned"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetSubtask by foreign principal: code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}

func TestGetSubtaskAllowsOwner(t *testing.T) {
	store, identity, owner, _ := authzTestPrincipals(t)
	cred, err := store.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	taskJSON, _ := json.Marshal(map[string]any{
		"id":                 "task-owned",
		"execution_identity": identity,
	})
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.GetSubtaskResponse{TaskJson: taskJSON}), nil
		},
	}
	index := newTaskIndex()
	index.put("task-owned", taskPlacement{TargetID: "t1"})
	r := newTaskRouter(hub, &fakeScheduler{}, index, nil)
	r.principals = store

	if _, err := r.GetSubtask(actorCtx(cred.Token), &pb.GetSubtaskRequest{Id: "task-owned"}); err != nil {
		t.Fatalf("GetSubtask by owner: %v", err)
	}
}

func TestStreamSubtaskRejectsForeignPrincipal(t *testing.T) {
	store, identity, _, intruder := authzTestPrincipals(t)
	cred, err := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	taskJSON, _ := json.Marshal(map[string]any{
		"id":                 "task-owned",
		"execution_identity": identity,
	})
	hub := &fakeRouterHub{
		targets: isolatingTargets("t1"),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.GetSubtaskResponse{TaskJson: taskJSON}), nil
		},
		streamFn: func(_, method string, _ interface{}, _ func(*pb.TunnelMsg) error) error {
			return nil
		},
	}
	index := newTaskIndex()
	index.put("task-owned", taskPlacement{TargetID: "t1"})
	r := newTaskRouter(hub, &fakeScheduler{}, index, nil)
	r.principals = store

	stream := &fakeStreamSubtaskServer{ctx: actorCtx(cred.Token)}
	err = r.StreamSubtask(&pb.StreamSubtaskRequest{Id: "task-owned"}, stream)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("StreamSubtask by foreign principal: code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}

func TestStreamSubtaskAllowsOwner(t *testing.T) {
	store, identity, owner, _ := authzTestPrincipals(t)
	cred, err := store.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	taskJSON, _ := json.Marshal(map[string]any{
		"id":                 "task-owned",
		"execution_identity": identity,
	})
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.GetSubtaskResponse{TaskJson: taskJSON}), nil
		},
		streamFn: func(_, method string, _ interface{}, fn func(*pb.TunnelMsg) error) error {
			return fn(respJSON(t, &pb.SubtaskEvent{EventJson: []byte(`{"status":"running"}`)}))
		},
	}
	index := newTaskIndex()
	index.put("task-owned", taskPlacement{TargetID: "t1"})
	r := newTaskRouter(hub, &fakeScheduler{}, index, nil)
	r.principals = store

	stream := &fakeStreamSubtaskServer{ctx: actorCtx(cred.Token)}
	if err := r.StreamSubtask(&pb.StreamSubtaskRequest{Id: "task-owned"}, stream); err != nil {
		t.Fatalf("StreamSubtask by owner: %v", err)
	}
	if len(stream.sent) != 1 || string(stream.sent[0].EventJson) != `{"status":"running"}` {
		t.Fatalf("streamed events = %+v, want one running event", stream.sent)
	}
}

func TestListSubtasksFiltersOtherPrincipalsTasks(t *testing.T) {
	store, identity, owner, intruder := authzTestPrincipals(t)
	ownerCred, err := store.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	ownerTask, _ := json.Marshal(map[string]any{"id": "task-mine", "execution_identity": identity})
	otherIdentity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: intruder.ID,
		EffectivePrincipalID:   intruder.ID,
		Source:                 principal.SourceMCP,
	}
	otherTask, _ := json.Marshal(map[string]any{"id": "task-theirs", "execution_identity": otherIdentity})
	legacyTask, _ := json.Marshal(map[string]any{"id": "task-legacy"})
	hub := &fakeRouterHub{
		targets: isolatingTargets("t1"),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			if method != pb.TaskMethodList {
				return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
			}
			return respJSON(t, &pb.ListSubtasksResponse{TasksJson: [][]byte{ownerTask, otherTask, legacyTask}}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), nil)
	r.principals = store

	out, err := r.ListSubtasks(actorCtx(ownerCred.Token), &pb.ListSubtasksRequest{})
	if err != nil {
		t.Fatalf("ListSubtasks: %v", err)
	}
	ids := taskIDsInList(t, out.TasksJson)
	if !contains(ids, "task-mine") {
		t.Fatalf("own task filtered out: %v", ids)
	}
	if contains(ids, "task-theirs") {
		t.Fatalf("other principal's task visible: %v", ids)
	}
	if !contains(ids, "task-legacy") {
		t.Fatalf("legacy-host task filtered out for owner: %v", ids)
	}

	// A caller with no credential sees the legacy-host rows but none of the
	// identified rows — an empty identified view, not an error, not the rows.
	out, err = r.ListSubtasks(authlessCtx(), &pb.ListSubtasksRequest{})
	if err != nil {
		t.Fatalf("ListSubtasks unauthenticated: %v", err)
	}
	ids = taskIDsInList(t, out.TasksJson)
	if len(ids) != 1 || !contains(ids, "task-legacy") {
		t.Fatalf("unauthenticated list = %v, want only [task-legacy]", ids)
	}
}

func TestListAgentInstancesFiltersOtherPrincipalsInstances(t *testing.T) {
	store, identity, owner, intruder := authzTestPrincipals(t)
	ownerCred, err := store.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: intruder.ID,
		EffectivePrincipalID:   intruder.ID,
		Source:                 principal.SourceMCP,
	}
	instanceStore := newTestAgentInstanceStore(t)
	if _, err := instanceStore.Put(agentinstance.Instance{
		ID: "inst-mine", AgentType: "claude", Status: agentinstance.StatusActive,
		OwnerSessionID: "sess-owner", ExecutionIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instanceStore.Put(agentinstance.Instance{
		ID: "inst-theirs", AgentType: "claude", Status: agentinstance.StatusActive,
		OwnerSessionID: "sess-intruder", ExecutionIdentity: otherIdentity,
	}); err != nil {
		t.Fatal(err)
	}
	// No ExecutionIdentity: the store scan defaults it to legacy-host, the
	// prevalent row type in existing deployments — visible to every caller.
	if _, err := instanceStore.Put(agentinstance.Instance{
		ID: "inst-legacy", AgentType: "claude", Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	hub := &fakeRouterHub{
		targets: isolatingTargets("t1"),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), instanceStore)
	r.principals = store

	out, err := r.ListAgentInstances(actorCtx(ownerCred.Token), &pb.ListAgentInstancesRequest{})
	if err != nil {
		t.Fatalf("ListAgentInstances: %v", err)
	}
	ids := instanceIDsInList(out.Instances)
	if len(ids) != 2 || !contains(ids, "inst-mine") || !contains(ids, "inst-legacy") {
		t.Fatalf("owner sees %v, want [inst-mine inst-legacy]", ids)
	}
	if contains(ids, "inst-theirs") {
		t.Fatalf("other principal's instance visible: %v", ids)
	}

	// A caller with no credential sees the legacy-host row but none of the
	// identified rows — an empty identified view, not an error, not the rows.
	out, err = r.ListAgentInstances(authlessCtx(), &pb.ListAgentInstancesRequest{})
	if err != nil {
		t.Fatalf("ListAgentInstances unauthenticated: %v", err)
	}
	ids = instanceIDsInList(out.Instances)
	if len(ids) != 1 || !contains(ids, "inst-legacy") {
		t.Fatalf("unauthenticated list = %v, want only [inst-legacy]", ids)
	}
}

func instanceIDsInList(insts []*pb.AgentInstance) []string {
	ids := make([]string, 0, len(insts))
	for _, inst := range insts {
		ids = append(ids, inst.Id)
	}
	return ids
}

// An invalid credential is not a missing one: lists surface the
// Unauthenticated error instead of silently returning an empty list.
func TestListSubtasksInvalidCredentialErrors(t *testing.T) {
	store, _, _, _ := authzTestPrincipals(t)
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.ListSubtasksResponse{}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), nil)
	r.principals = store

	_, err := r.ListSubtasks(actorCtx("bogus-token"), &pb.ListSubtasksRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("ListSubtasks invalid credential: code = %v, want Unauthenticated (err=%v)", status.Code(err), err)
	}
}

func TestListAgentInstancesInvalidCredentialErrors(t *testing.T) {
	store, _, _, _ := authzTestPrincipals(t)
	r := newTaskRouter(&fakeRouterHub{
		targets: targets(),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
		},
	}, &fakeScheduler{}, newTaskIndex(), newTestAgentInstanceStore(t))
	r.principals = store

	_, err := r.ListAgentInstances(actorCtx("bogus-token"), &pb.ListAgentInstancesRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("ListAgentInstances invalid credential: code = %v, want Unauthenticated (err=%v)", status.Code(err), err)
	}
}

func taskIDsInList(t *testing.T, raws [][]byte) []string {
	t.Helper()
	ids := make([]string, 0, len(raws))
	for _, raw := range raws {
		id, _ := taskRefFromJSON(raw)
		ids = append(ids, id)
	}
	return ids
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// instanceAuthzFixture stores one durable instance owned by identity's owner
// and returns the router, its store, and the owner's and intruder's tokens.
func instanceAuthzFixture(t *testing.T) (*taskRouter, *principal.Store, string, string) {
	t.Helper()
	store, identity, owner, intruder := authzTestPrincipals(t)
	instances := newTestAgentInstanceStore(t)
	if _, err := instances.Put(agentinstance.Instance{
		ID: "inst-owned", AgentType: "claude", TargetID: "t1",
		OwnerSessionID: "owner-session", ExecutionIdentity: identity,
		Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	hub := &fakeRouterHub{
		targets: isolatingTargets("t1"),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return respJSON(t, &pb.GetAgentInstanceResponse{Instance: &pb.AgentInstance{Id: "inst-owned"}}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), instances)
	r.principals = store
	ownerTok, _ := store.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	intruderTok, _ := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	return r, store, ownerTok.Token, intruderTok.Token
}

func TestGetAgentInstanceRejectsForeignPrincipal(t *testing.T) {
	r, _, _, intruderTok := instanceAuthzFixture(t)
	_, err := r.GetAgentInstance(actorCtx(intruderTok), &pb.GetAgentInstanceRequest{Id: "inst-owned"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetAgentInstance by foreign principal: code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}

func TestWaitAgentInstanceRejectsForeignPrincipal(t *testing.T) {
	r, _, _, intruderTok := instanceAuthzFixture(t)
	_, err := r.WaitAgentInstance(actorCtx(intruderTok), &pb.WaitAgentInstanceRequest{Id: "inst-owned"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("WaitAgentInstance by foreign principal: code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}

func TestDescribeAgentInstanceRejectsForeignPrincipal(t *testing.T) {
	r, _, _, intruderTok := instanceAuthzFixture(t)
	_, err := r.DescribeAgentInstance(actorCtx(intruderTok), &pb.DescribeAgentInstanceRequest{Id: "inst-owned"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("DescribeAgentInstance by foreign principal: code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}

func TestGetAgentInstanceAllowsOwner(t *testing.T) {
	r, _, ownerTok, _ := instanceAuthzFixture(t)
	if _, err := r.GetAgentInstance(actorCtx(ownerTok), &pb.GetAgentInstanceRequest{Id: "inst-owned"}); err != nil {
		t.Fatalf("GetAgentInstance by owner: %v", err)
	}
}

// instanceAuthzLiveFixture has NO durable record for the instance: the
// authorization identity can only come from the live describe fan-out, which
// the fake answers with an identified identity owned by the fixture's owner.
func instanceAuthzLiveFixture(t *testing.T) (*taskRouter, *principal.Store, string, string) {
	t.Helper()
	store, identity, owner, intruder := authzTestPrincipals(t)
	instances := newTestAgentInstanceStore(t) // empty: the instance is runtime-only
	hub := &fakeRouterHub{
		targets: isolatingTargets("t1"),
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			switch method {
			case pb.TaskMethodInstanceDescribe:
				return respJSON(t, &pb.DescribeAgentInstanceResponse{
					ExecutionIdentity: principal.ToProtoIdentity(identity),
				}), nil
			case pb.TaskMethodInstanceGet:
				return respJSON(t, &pb.GetAgentInstanceResponse{Instance: &pb.AgentInstance{Id: "inst-live"}}), nil
			}
			return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), instances)
	r.principals = store
	ownerTok, _ := store.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	intruderTok, _ := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	return r, store, ownerTok.Token, intruderTok.Token
}

func TestGetAgentInstanceLiveIdentityRejectsForeignPrincipal(t *testing.T) {
	r, _, _, intruderTok := instanceAuthzLiveFixture(t)
	_, err := r.GetAgentInstance(actorCtx(intruderTok), &pb.GetAgentInstanceRequest{Id: "inst-live"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetAgentInstance (live identity) by foreign principal: code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}

func TestGetAgentInstanceLiveIdentityAllowsOwner(t *testing.T) {
	r, _, ownerTok, _ := instanceAuthzLiveFixture(t)
	if _, err := r.GetAgentInstance(actorCtx(ownerTok), &pb.GetAgentInstanceRequest{Id: "inst-live"}); err != nil {
		t.Fatalf("GetAgentInstance (live identity) by owner: %v", err)
	}
}

// A legacy-host instance (durable record, no frozen identity) on an offline
// target is served from the durable record: the guard must not pay a live
// describe that cannot succeed. The frozen record is the source of truth —
// FinalizeExecution refuses to persist a diverging live identity — so
// present-but-empty authorizes without a network call, like the list path.
func TestGetAgentInstanceServesLegacyRecordOffline(t *testing.T) {
	store, _, _, _ := authzTestPrincipals(t)
	instances := newTestAgentInstanceStore(t)
	if _, err := instances.Put(agentinstance.Instance{
		ID: "inst-legacy", AgentType: "claude", TargetID: "t-offline",
		OwnerSessionID: "owner-session", Status: agentinstance.StatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	hub := &fakeRouterHub{
		targets: targets(), // the instance's target is not connected
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), instances)
	r.principals = store
	resp, err := r.GetAgentInstance(authlessCtx(), &pb.GetAgentInstanceRequest{Id: "inst-legacy"})
	if err != nil {
		t.Fatalf("GetAgentInstance legacy record on offline target: %v", err)
	}
	if resp.Instance == nil || resp.Instance.Id != "inst-legacy" {
		t.Fatalf("instance = %+v, want inst-legacy", resp.Instance)
	}
}

// issueEnrollment mints an enrollment credential for id — the Delegated=false
// counterpart of an agent credential, which is what a human CLI caller carries.
func issueEnrollment(t *testing.T, store *principal.Store, id string) string {
	t.Helper()
	enrollment, err := store.CreateEnrollment(principal.ProviderCLI, "local", "cli-caller", id, false)
	if err != nil {
		t.Fatal(err)
	}
	return enrollment.Token
}

func TestPruneSubtasksRejectsDelegatedActorWithIsolation(t *testing.T) {
	store, _, _, intruder := authzTestPrincipals(t)
	cred, _ := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	hub := &fakeRouterHub{targets: isolatingTargets("t1"), routeFn: func(_, m string, _ interface{}) (*pb.TunnelMsg, error) {
		return respJSON(t, &pb.PruneSubtasksResponse{}), nil
	}}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), nil)
	r.principals = store
	_, err := r.PruneSubtasks(actorCtx(cred.Token), &pb.PruneSubtasksRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("PruneSubtasks by delegated actor: code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}

func TestPruneSubtasksAllowsDelegatedActorWithoutIsolation(t *testing.T) {
	store, _, _, intruder := authzTestPrincipals(t)
	cred, _ := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	hub := &fakeRouterHub{targets: targets("t1"), routeFn: func(_, m string, _ interface{}) (*pb.TunnelMsg, error) {
		return respJSON(t, &pb.PruneSubtasksResponse{Tasks: []*pb.PrunedSubtask{{Id: "task-1"}}}), nil
	}}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), nil)
	r.principals = store
	resp, err := r.PruneSubtasks(actorCtx(cred.Token), &pb.PruneSubtasksRequest{})
	if err != nil {
		t.Fatalf("PruneSubtasks by delegated actor without isolation: %v", err)
	}
	if resp == nil || resp.DeletedCount != 1 || len(resp.Tasks) != 1 || resp.Tasks[0].Id != "task-1" {
		t.Fatalf("PruneSubtasks response = %+v, want task-1 deleted", resp)
	}
}

func TestPruneSubtasksAllowsEnrollment(t *testing.T) {
	store, _, owner, _ := authzTestPrincipals(t)
	enrollTok := issueEnrollment(t, store, owner.ID)
	hub := &fakeRouterHub{targets: targets("t1"), routeFn: func(_, m string, _ interface{}) (*pb.TunnelMsg, error) {
		return respJSON(t, &pb.PruneSubtasksResponse{}), nil
	}}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), nil)
	r.principals = store
	if _, err := r.PruneSubtasks(actorCtx(enrollTok), &pb.PruneSubtasksRequest{}); err != nil {
		t.Fatalf("PruneSubtasks by enrollment: %v", err)
	}
}

func TestPruneSubtasksRejectsUnauthenticated(t *testing.T) {
	store, _, _, _ := authzTestPrincipals(t)
	r := newTaskRouter(&fakeRouterHub{targets: targets("t1")}, &fakeScheduler{}, newTaskIndex(), nil)
	r.principals = store
	_, err := r.PruneSubtasks(authlessCtx(), &pb.PruneSubtasksRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("PruneSubtasks unauthenticated: code = %v, want Unauthenticated (err=%v)", status.Code(err), err)
	}
}

// Legacy single-machine deployments run without a principal store: prune
// passes through unchanged.
func TestPruneSubtasksPassesThroughWithoutPrincipalStore(t *testing.T) {
	hub := &fakeRouterHub{targets: targets("t1"), routeFn: func(_, m string, _ interface{}) (*pb.TunnelMsg, error) {
		return respJSON(t, &pb.PruneSubtasksResponse{}), nil
	}}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), nil)
	if _, err := r.PruneSubtasks(authlessCtx(), &pb.PruneSubtasksRequest{}); err != nil {
		t.Fatalf("PruneSubtasks without principal store: %v", err)
	}
}

// ── no-isolation fleet: the identity boundary is skipped ────────────────────
//
// The narrowing above mirrors the per-principal sandbox an isolating runtime
// enforces. On a fleet with no isolating runtime there is no such boundary, so
// task/instance ownership checks and list filters are skipped entirely (the
// pre-rule-six behavior): any caller reaches every identified row.

func TestAuthorizeExecutionReadAllowsForeignPrincipalWithoutIsolation(t *testing.T) {
	store, identity, _, intruder := authzTestPrincipals(t)
	cred, err := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	r := &taskRouter{principals: store, hub: &fakeRouterHub{targets: targets("t1")}}
	if err := r.authorizeExecutionRead(actorCtx(cred.Token), identity, "", "task"); err != nil {
		t.Fatalf("foreign read without isolation = %v, want nil", err)
	}
	if err := r.authorizeExecutionRead(authlessCtx(), identity, "", "task"); err != nil {
		t.Fatalf("unauthenticated read without isolation = %v, want nil", err)
	}
}

func TestAuthorizeExecutionReadRejectsInvalidCredentialWithoutIsolation(t *testing.T) {
	store, identity, _, _ := authzTestPrincipals(t)
	r := &taskRouter{principals: store, hub: &fakeRouterHub{targets: targets("t1")}}
	err := r.authorizeExecutionRead(actorCtx("invalid-token"), identity, "", "task")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("invalid credential without isolation = %v, want Unauthenticated", err)
	}
}

func TestPersistentFollowupAllowsForeignInstanceWithoutIsolation(t *testing.T) {
	store, identity, _, intruder := authzTestPrincipals(t)
	cred, err := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	instances := newTestAgentInstanceStore(t)
	if _, err := instances.Put(agentinstance.Instance{
		ID: "inst-foreign", AgentType: "claude", TargetID: "t1",
		Status: agentinstance.StatusIdle, ExecutionIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}
	var routedIdentity principal.ExecutionIdentity
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(_, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if method != pb.TaskMethodSubmit {
				return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
			}
			var routed task.Spec
			if err := json.Unmarshal(payload.(*pb.SubmitSubtaskRequest).SpecJson, &routed); err != nil {
				return nil, status.Errorf(codes.Internal, "decode routed spec: %v", err)
			}
			if routed.ExecutionIdentity != nil {
				routedIdentity = *routed.ExecutionIdentity
			}
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-followup","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
		return &registry.Placement{TargetID: "t1", ReservationId: "rsv-followup"}, nil
	}}, newTaskIndex(), instances)
	r.principals = store

	raw, err := json.Marshal(task.Spec{
		Agent: "claude", Task: "follow up", Mode: task.ModePersistent, AgentInstanceID: "inst-foreign",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.SubmitSubtask(actorCtx(cred.Token), &pb.SubmitSubtaskRequest{SpecJson: raw}); err != nil {
		t.Fatalf("persistent followup to foreign instance without isolation: %v", err)
	}
	if routedIdentity.EffectivePrincipalID != identity.EffectivePrincipalID {
		t.Fatalf("routed identity = %+v, want saved identity %+v", routedIdentity, identity)
	}
}

func TestListSubtasksShowsAllPrincipalsTasksWithoutIsolation(t *testing.T) {
	store, identity, owner, intruder := authzTestPrincipals(t)
	ownerCred, err := store.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	ownerTask, _ := json.Marshal(map[string]any{"id": "task-mine", "execution_identity": identity})
	otherIdentity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: intruder.ID,
		EffectivePrincipalID:   intruder.ID,
		Source:                 principal.SourceMCP,
	}
	otherTask, _ := json.Marshal(map[string]any{"id": "task-theirs", "execution_identity": otherIdentity})
	hub := &fakeRouterHub{
		targets: targets("t1"), // no isolation capability
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			if method != pb.TaskMethodList {
				return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
			}
			return respJSON(t, &pb.ListSubtasksResponse{TasksJson: [][]byte{ownerTask, otherTask}}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), nil)
	r.principals = store

	out, err := r.ListSubtasks(actorCtx(ownerCred.Token), &pb.ListSubtasksRequest{})
	if err != nil {
		t.Fatalf("ListSubtasks: %v", err)
	}
	ids := taskIDsInList(t, out.TasksJson)
	if !contains(ids, "task-mine") || !contains(ids, "task-theirs") {
		t.Fatalf("list without isolation = %v, want both principals' tasks", ids)
	}
}

func TestGetAndCancelSubtaskAllowForeignPrincipalWithoutIsolation(t *testing.T) {
	store, identity, _, intruder := authzTestPrincipals(t)
	cred, err := store.IssueAgentCredential(intruder.ID, "launch-intruder", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	taskJSON, _ := json.Marshal(map[string]any{
		"id":                 "task-owned",
		"execution_identity": identity,
	})
	hub := &fakeRouterHub{
		targets: targets("t1"), // no isolation capability
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			switch method {
			case pb.TaskMethodGet:
				return respJSON(t, &pb.GetSubtaskResponse{TaskJson: taskJSON}), nil
			case pb.TaskMethodCancel:
				return respJSON(t, &pb.CancelSubtaskResponse{}), nil
			}
			return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
		},
	}
	index := newTaskIndex()
	index.put("task-owned", taskPlacement{TargetID: "t1"})
	r := newTaskRouter(hub, &fakeScheduler{}, index, nil)
	r.principals = store

	if _, err := r.GetSubtask(actorCtx(cred.Token), &pb.GetSubtaskRequest{Id: "task-owned"}); err != nil {
		t.Fatalf("GetSubtask by foreign principal without isolation: %v", err)
	}
	if _, err := r.CancelSubtask(actorCtx(cred.Token), &pb.CancelSubtaskRequest{Id: "task-owned"}); err != nil {
		t.Fatalf("CancelSubtask by foreign principal without isolation: %v", err)
	}
}

func TestListAgentInstancesShowsAllPrincipalsInstancesWithoutIsolation(t *testing.T) {
	store, identity, owner, intruder := authzTestPrincipals(t)
	ownerCred, err := store.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: intruder.ID,
		EffectivePrincipalID:   intruder.ID,
		Source:                 principal.SourceMCP,
	}
	instanceStore := newTestAgentInstanceStore(t)
	if _, err := instanceStore.Put(agentinstance.Instance{
		ID: "inst-mine", AgentType: "claude", Status: agentinstance.StatusActive,
		OwnerSessionID: "sess-owner", ExecutionIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := instanceStore.Put(agentinstance.Instance{
		ID: "inst-theirs", AgentType: "claude", Status: agentinstance.StatusActive,
		OwnerSessionID: "sess-intruder", ExecutionIdentity: otherIdentity,
	}); err != nil {
		t.Fatal(err)
	}
	hub := &fakeRouterHub{
		targets: targets(), // no targets at all, hence no isolating runtime
		routeFn: func(_, method string, _ interface{}) (*pb.TunnelMsg, error) {
			return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), instanceStore)
	r.principals = store

	out, err := r.ListAgentInstances(actorCtx(ownerCred.Token), &pb.ListAgentInstancesRequest{})
	if err != nil {
		t.Fatalf("ListAgentInstances: %v", err)
	}
	ids := instanceIDsInList(out.Instances)
	if !contains(ids, "inst-mine") || !contains(ids, "inst-theirs") {
		t.Fatalf("list without isolation = %v, want both principals' instances", ids)
	}
}
