package hub

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/coordination"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCoordinationServerUsesBackendAsOnlyLifecycleTruth(t *testing.T) {
	t.Parallel()
	store := coordination.NewMemRouteStore()
	catalog := testCoordinationCatalog()
	backend := &fakeCoordinationBackend{status: coordination.StatusPending}
	server, err := newCoordinationServer(store, catalog, testCoordinationBackends(backend))
	if err != nil {
		t.Fatalf("newCoordinationServer: %v", err)
	}
	server.newID = func() string { return "delegation-1" }
	server.now = func() time.Time { return time.Unix(100, 0).UTC() }

	created, err := server.Delegate(context.Background(), &pb.DelegateRequest{CollaboratorId: catalog.descriptor.ID, Input: "review", InitiatorRef: "session:main", ScopeRef: "turn:1", IdempotencyKey: "request-1"})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if !created.Created || created.Snapshot.Delegation.Status != "pending" || backend.createCalls != 1 {
		t.Fatalf("created = %#v calls=%d", created, backend.createCalls)
	}

	backend.status = coordination.StatusSucceeded
	backend.result = &coordination.ResultEnvelope{Summary: "fresh backend result", TrustLevel: coordination.TrustInternal}
	got, err := server.GetDelegation(context.Background(), &pb.GetDelegationRequest{DelegationId: "delegation-1"})
	if err != nil {
		t.Fatalf("GetDelegation: %v", err)
	}
	if got.Snapshot.Delegation.Status != "succeeded" || got.Snapshot.Delegation.Result.Summary != "fresh backend result" {
		t.Fatalf("got = %#v", got)
	}

	replayed, err := server.Delegate(context.Background(), &pb.DelegateRequest{CollaboratorId: catalog.descriptor.ID, Input: "review", InitiatorRef: "session:main", ScopeRef: "turn:1", IdempotencyKey: "request-1"})
	if err != nil {
		t.Fatalf("replay Delegate: %v", err)
	}
	if replayed.Created || backend.createCalls != 1 || replayed.Snapshot.Delegation.Id != "delegation-1" {
		t.Fatalf("replayed = %#v calls=%d", replayed, backend.createCalls)
	}
}

func TestCoordinationServerPassesInitiatorScopeToCatalog(t *testing.T) {
	catalog := testCoordinationCatalog()
	backend := &fakeCoordinationBackend{status: coordination.StatusPending}
	server, err := newCoordinationServer(coordination.NewMemRouteStore(), catalog, testCoordinationBackends(backend))
	if err != nil {
		t.Fatal(err)
	}
	server.newID = func() string { return "delegation-scoped" }
	initiator := "amux-session:session-current-group"
	if _, err := server.ListCollaborators(context.Background(), &pb.ListCollaboratorsRequest{InitiatorRef: initiator}); err != nil {
		t.Fatal(err)
	}
	if catalog.listScope.InitiatorRef != initiator {
		t.Fatalf("list scope = %+v", catalog.listScope)
	}
	if _, err := server.Delegate(context.Background(), &pb.DelegateRequest{
		CollaboratorId: catalog.descriptor.ID, Input: "review", InitiatorRef: initiator, ScopeRef: "turn:1",
	}); err != nil {
		t.Fatal(err)
	}
	if catalog.resolveScope.InitiatorRef != initiator {
		t.Fatalf("resolve scope = %+v", catalog.resolveScope)
	}
}

func TestCoordinationServerRejectsIdempotencyCollisionAndReleasesFailedClaim(t *testing.T) {
	t.Parallel()
	store := coordination.NewMemRouteStore()
	catalog := testCoordinationCatalog()
	backend := &fakeCoordinationBackend{status: coordination.StatusPending}
	server, _ := newCoordinationServer(store, catalog, testCoordinationBackends(backend))
	ids := []string{"delegation-1", "delegation-2", "delegation-3", "delegation-4"}
	server.newID = func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}
	request := &pb.DelegateRequest{CollaboratorId: catalog.descriptor.ID, Input: "review", InitiatorRef: "session:main", ScopeRef: "turn:1", IdempotencyKey: "request-1"}
	if _, err := server.Delegate(context.Background(), request); err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	collision := *request
	collision.Input = "different work"
	if _, err := server.Delegate(context.Background(), &collision); err == nil {
		t.Fatal("idempotency collision was accepted")
	}

	backend.createErr = errors.New("no capacity")
	failed := &pb.DelegateRequest{CollaboratorId: catalog.descriptor.ID, Input: "retryable", InitiatorRef: "session:main", ScopeRef: "turn:2", IdempotencyKey: "request-2"}
	if _, err := server.Delegate(context.Background(), failed); err == nil {
		t.Fatal("backend failure was accepted")
	}
	backend.createErr = nil
	if _, err := server.Delegate(context.Background(), failed); err != nil {
		t.Fatalf("retry after released claim: %v", err)
	}
}

func TestCoordinationServerForwardsWaitPermissionAndCancel(t *testing.T) {
	t.Parallel()
	store := coordination.NewMemRouteStore()
	route := &coordination.Route{ID: "delegation-1", BackendKind: coordination.BackendKindAmuxTask, CollaboratorID: "agent-profile:team/reviewer@abc", TargetRef: "team/reviewer@abc", CreatedAt: time.Now().UTC()}
	_, _, _ = store.Claim(context.Background(), route)
	route, _ = store.Bind(context.Background(), route.ID, "amux-task:task-1")
	backend := &fakeCoordinationBackend{status: coordination.StatusWaiting, event: coordination.Event{Seq: 4, Kind: coordination.EventPermissionRequested, Status: coordination.StatusWaiting}}
	server, _ := newCoordinationServer(store, testCoordinationCatalog(), testCoordinationBackends(backend))
	waited, err := server.WaitDelegation(context.Background(), &pb.WaitDelegationRequest{DelegationId: route.ID, FromSeq: 3, TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("WaitDelegation: %v", err)
	}
	if waited.Snapshot.NextSeq != 4 || len(waited.Snapshot.Events) != 1 {
		t.Fatalf("waited = %#v", waited)
	}
	_, err = server.InterveneDelegation(context.Background(), &pb.InterveneDelegationRequest{DelegationId: route.ID, RequestId: "permission-1", Decision: "allow"})
	if err != nil {
		t.Fatalf("InterveneDelegation: %v", err)
	}
	if backend.permission.RequestID != "permission-1" {
		t.Fatalf("permission = %#v", backend.permission)
	}
	_, err = server.CancelDelegation(context.Background(), &pb.CancelDelegationRequest{DelegationId: route.ID})
	if err != nil {
		t.Fatalf("CancelDelegation: %v", err)
	}
	if !backend.cancelled {
		t.Fatal("cancel was not forwarded")
	}
}

func TestCoordinationServerRoutesEachDelegationToResolvedBackend(t *testing.T) {
	internal := &fakeCoordinationBackend{status: coordination.StatusRunning}
	external := &fakeCoordinationBackend{status: coordination.StatusRunning}
	catalog := testCoordinationCatalog()
	catalog.descriptor = coordination.CollaboratorDescriptor{ID: "external-bot:reviewer", Name: "reviewer", Kind: coordination.CollaboratorExternalBot, Availability: coordination.AvailabilityBestEffort}
	catalog.target = coordination.ResolvedCollaborator{BackendKind: coordination.BackendKindFeishuBot, TargetRef: "private-feishu-target", TrustLevel: coordination.TrustExternalUntrusted}
	server, err := newCoordinationServer(coordination.NewMemRouteStore(), catalog, map[string]coordination.Backend{
		coordination.BackendKindAmuxTask:  internal,
		coordination.BackendKindFeishuBot: external,
	})
	if err != nil {
		t.Fatal(err)
	}
	server.newID = func() string { return "delegation-external" }
	response, err := server.Delegate(context.Background(), &pb.DelegateRequest{
		CollaboratorId: catalog.descriptor.ID, Input: "review", InitiatorRef: "session:main", ScopeRef: "turn:1",
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if internal.createCalls != 0 || external.createCalls != 1 {
		t.Fatalf("create calls internal=%d external=%d", internal.createCalls, external.createCalls)
	}
	if response.Snapshot.Delegation.CollaboratorId != catalog.descriptor.ID {
		t.Fatalf("collaborator id = %q", response.Snapshot.Delegation.CollaboratorId)
	}
}

func TestCoordinationServerPrebindsExternalRouteAndRecoversIdempotently(t *testing.T) {
	path := t.TempDir() + "/coordination.db"
	store, err := coordination.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	catalog := testCoordinationCatalog()
	catalog.descriptor = coordination.CollaboratorDescriptor{ID: "external-bot:reviewer", Kind: coordination.CollaboratorExternalBot}
	catalog.target = coordination.ResolvedCollaborator{BackendKind: coordination.BackendKindFeishuBot, TargetRef: "private-feishu-target", TrustLevel: coordination.TrustExternalUntrusted}
	backend := &fakePreboundCoordinationBackend{fakeCoordinationBackend: fakeCoordinationBackend{status: coordination.StatusPending}, effects: make(map[string]int)}
	server, err := newCoordinationServer(store, catalog, map[string]coordination.Backend{coordination.BackendKindFeishuBot: backend})
	if err != nil {
		t.Fatal(err)
	}
	server.newID = func() string { return "delegation-external" }
	request := &pb.DelegateRequest{CollaboratorId: catalog.descriptor.ID, Input: "review", InitiatorRef: "session:main", ScopeRef: "turn:1", IdempotencyKey: "request-external"}
	if _, err := server.Delegate(context.Background(), request); err != nil {
		t.Fatalf("first Delegate: %v", err)
	}
	if backend.effects["feishu-exchange:delegation-external"] != 1 || !backend.sawBoundRoute {
		t.Fatalf("effects=%v sawBoundRoute=%v", backend.effects, backend.sawBoundRoute)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Model process recovery of an ambiguous pending platform send. The
	// separate route DB is reopened while backend lifecycle truth survives and
	// becomes failed(delivery_unknown).
	backend.status = coordination.StatusFailed
	store, err = coordination.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err = newCoordinationServer(store, catalog, map[string]coordination.Backend{coordination.BackendKindFeishuBot: backend})
	if err != nil {
		t.Fatal(err)
	}
	server.newID = func() string { return "unused-new-id" }
	replayed, err := server.Delegate(context.Background(), request)
	if err != nil {
		t.Fatalf("replayed Delegate: %v", err)
	}
	if replayed.Created || replayed.Snapshot.Delegation.Status != string(coordination.StatusFailed) {
		t.Fatalf("replayed = %+v", replayed)
	}
	if backend.effects["feishu-exchange:delegation-external"] != 1 {
		t.Fatalf("external effect duplicated: %v", backend.effects)
	}
}

func TestCoordinationServerPreservesBackendStatusCodes(t *testing.T) {
	t.Parallel()
	backendError := fmt.Errorf("task control: %w", status.Error(codes.PermissionDenied, "task access denied"))
	tests := []struct {
		name      string
		configure func(*fakeCoordinationBackend)
		call      func(*coordinationServer) error
	}{
		{
			name:      "delegate create",
			configure: func(backend *fakeCoordinationBackend) { backend.createErr = backendError },
			call: func(server *coordinationServer) error {
				_, err := server.Delegate(context.Background(), &pb.DelegateRequest{CollaboratorId: testCoordinationCatalog().descriptor.ID, Input: "review", InitiatorRef: "session:main", ScopeRef: "turn:1"})
				return err
			},
		},
		{
			name:      "delegate read",
			configure: func(backend *fakeCoordinationBackend) { backend.getErr = backendError },
			call: func(server *coordinationServer) error {
				_, err := server.Delegate(context.Background(), &pb.DelegateRequest{CollaboratorId: testCoordinationCatalog().descriptor.ID, Input: "review", InitiatorRef: "session:main", ScopeRef: "turn:1"})
				return err
			},
		},
		{
			name:      "get",
			configure: func(backend *fakeCoordinationBackend) { backend.getErr = backendError },
			call: func(server *coordinationServer) error {
				_, err := server.GetDelegation(context.Background(), &pb.GetDelegationRequest{DelegationId: "delegation-bound"})
				return err
			},
		},
		{
			name:      "wait",
			configure: func(backend *fakeCoordinationBackend) { backend.waitErr = backendError },
			call: func(server *coordinationServer) error {
				_, err := server.WaitDelegation(context.Background(), &pb.WaitDelegationRequest{DelegationId: "delegation-bound", TimeoutSeconds: 1})
				return err
			},
		},
		{
			name:      "intervene",
			configure: func(backend *fakeCoordinationBackend) { backend.permissionErr = backendError },
			call: func(server *coordinationServer) error {
				_, err := server.InterveneDelegation(context.Background(), &pb.InterveneDelegationRequest{DelegationId: "delegation-bound", RequestId: "permission-1", Decision: "deny"})
				return err
			},
		},
		{
			name:      "cancel",
			configure: func(backend *fakeCoordinationBackend) { backend.cancelErr = backendError },
			call: func(server *coordinationServer) error {
				_, err := server.CancelDelegation(context.Background(), &pb.CancelDelegationRequest{DelegationId: "delegation-bound"})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := coordination.NewMemRouteStore()
			boundRoute := &coordination.Route{ID: "delegation-bound", BackendKind: coordination.BackendKindAmuxTask, CollaboratorID: "agent-profile:team/reviewer@abc", TargetRef: "team/reviewer@abc", CreatedAt: time.Now().UTC()}
			_, _, _ = store.Claim(context.Background(), boundRoute)
			_, _ = store.Bind(context.Background(), boundRoute.ID, "amux-task:task-bound")
			backend := &fakeCoordinationBackend{status: coordination.StatusRunning}
			test.configure(backend)
			server, _ := newCoordinationServer(store, testCoordinationCatalog(), testCoordinationBackends(backend))
			server.newID = func() string { return "delegation-created" }

			err := test.call(server)
			if got := status.Code(err); got != codes.PermissionDenied {
				t.Fatalf("status code = %s, want %s: %v", got, codes.PermissionDenied, err)
			}
		})
	}
}

type fakeCoordinationCatalog struct {
	descriptor   coordination.CollaboratorDescriptor
	target       coordination.ResolvedCollaborator
	listScope    coordination.CatalogScope
	resolveScope coordination.CatalogScope
}

func testCoordinationCatalog() *fakeCoordinationCatalog {
	return &fakeCoordinationCatalog{descriptor: coordination.CollaboratorDescriptor{ID: "agent-profile:team/reviewer@abc", Name: "reviewer", Kind: coordination.CollaboratorInternalAgent, Availability: coordination.AvailabilityConfigured}, target: coordination.ResolvedCollaborator{BackendKind: coordination.BackendKindAmuxTask, TargetRef: "team/reviewer@abc", TrustLevel: coordination.TrustInternal}}
}
func (f *fakeCoordinationCatalog) List(_ context.Context, scope coordination.CatalogScope) ([]coordination.CollaboratorDescriptor, error) {
	f.listScope = scope
	return []coordination.CollaboratorDescriptor{f.descriptor}, nil
}
func (f *fakeCoordinationCatalog) ResolveTarget(_ context.Context, _ string, scope coordination.CatalogScope) (coordination.ResolvedCollaborator, error) {
	f.resolveScope = scope
	return f.target, nil
}

func testCoordinationBackends(backend coordination.Backend) map[string]coordination.Backend {
	return map[string]coordination.Backend{coordination.BackendKindAmuxTask: backend}
}

type fakeCoordinationBackend struct {
	createCalls   int
	status        coordination.Status
	result        *coordination.ResultEnvelope
	event         coordination.Event
	permission    coordination.PermissionResponse
	cancelled     bool
	createErr     error
	getErr        error
	waitErr       error
	permissionErr error
	cancelErr     error
}

type fakePreboundCoordinationBackend struct {
	fakeCoordinationBackend
	effects       map[string]int
	sawBoundRoute bool
}

func (f *fakePreboundCoordinationBackend) PrebindRef(route *coordination.Route) (string, error) {
	return "feishu-exchange:" + route.ID, nil
}

func (f *fakePreboundCoordinationBackend) Create(_ context.Context, route *coordination.Route) (string, error) {
	f.createCalls++
	f.sawBoundRoute = f.sawBoundRoute || route.BackendRef != ""
	if f.effects[route.BackendRef] == 0 {
		f.effects[route.BackendRef]++
	}
	return route.BackendRef, f.createErr
}

func (f *fakeCoordinationBackend) Create(context.Context, *coordination.Route) (string, error) {
	f.createCalls++
	return "amux-task:task-1", f.createErr
}
func (f *fakeCoordinationBackend) Get(_ context.Context, route *coordination.Route) (*coordination.Snapshot, error) {
	return f.snapshot(route, nil, 0), f.getErr
}
func (f *fakeCoordinationBackend) Wait(_ context.Context, route *coordination.Route, from uint64) (*coordination.Snapshot, error) {
	events := []coordination.Event(nil)
	if f.event.Seq > from {
		events = []coordination.Event{f.event}
	}
	return f.snapshot(route, events, from), f.waitErr
}
func (f *fakeCoordinationBackend) RespondPermission(_ context.Context, _ *coordination.Route, response coordination.PermissionResponse) error {
	f.permission = response
	return f.permissionErr
}
func (f *fakeCoordinationBackend) Cancel(context.Context, *coordination.Route) error {
	f.cancelled = true
	return f.cancelErr
}
func (f *fakeCoordinationBackend) snapshot(route *coordination.Route, events []coordination.Event, from uint64) *coordination.Snapshot {
	next := from
	for _, event := range events {
		if event.Seq > next {
			next = event.Seq
		}
	}
	return &coordination.Snapshot{Delegation: &coordination.Delegation{ID: route.ID, Direction: coordination.DirectionOutbound, TargetRef: route.TargetRef, Status: f.status, Result: f.result, CreatedAt: route.CreatedAt}, Events: events, NextSeq: next}
}
