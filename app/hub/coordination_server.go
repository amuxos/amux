package hub

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/coordination"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxCoordinationWait        = 30 * time.Second
	defaultDelegationTimeout   = 35 * time.Minute
	defaultDelegationHopBudget = 10
)

type coordinationServer struct {
	pb.UnimplementedCoordinationServiceServer
	store    coordination.RouteStore
	catalog  coordination.Catalog
	backends map[string]coordination.Backend
	newID    func() string
	now      func() time.Time
}

func newCoordinationServer(store coordination.RouteStore, catalog coordination.Catalog, backends map[string]coordination.Backend) (*coordinationServer, error) {
	if store == nil {
		return nil, errors.New("coordination route store is required")
	}
	if catalog == nil {
		return nil, errors.New("coordination catalog is required")
	}
	if len(backends) == 0 {
		return nil, errors.New("at least one coordination backend is required")
	}
	for kind, backend := range backends {
		if strings.TrimSpace(kind) == "" || backend == nil {
			return nil, errors.New("coordination backend kind and implementation are required")
		}
	}
	return &coordinationServer{store: store, catalog: catalog, backends: backends, newID: uuid.NewString, now: time.Now}, nil
}

func (s *coordinationServer) ListCollaborators(ctx context.Context, request *pb.ListCollaboratorsRequest) (*pb.ListCollaboratorsResponse, error) {
	scope := coordination.CatalogScope{}
	if request != nil {
		scope.InitiatorRef = strings.TrimSpace(request.GetInitiatorRef())
	}
	items, err := s.catalog.List(ctx, scope)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list collaborators: %v", err)
	}
	response := &pb.ListCollaboratorsResponse{Collaborators: make([]*pb.CollaboratorDescriptor, 0, len(items))}
	for _, item := range items {
		response.Collaborators = append(response.Collaborators, collaboratorToPB(item))
	}
	return response, nil
}

func (s *coordinationServer) Delegate(ctx context.Context, request *pb.DelegateRequest) (*pb.DelegateResponse, error) {
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "delegate request is required")
	}
	if strings.TrimSpace(request.CollaboratorId) == "" || strings.TrimSpace(request.Input) == "" || strings.TrimSpace(request.InitiatorRef) == "" || strings.TrimSpace(request.ScopeRef) == "" {
		return nil, status.Error(codes.InvalidArgument, "collaborator_id, input, initiator_ref, and scope_ref are required")
	}
	if request.TimeoutSeconds < 0 {
		return nil, status.Error(codes.InvalidArgument, "timeout_seconds must not be negative")
	}
	scope := coordination.CatalogScope{InitiatorRef: strings.TrimSpace(request.InitiatorRef)}
	target, err := s.catalog.ResolveTarget(ctx, request.CollaboratorId, scope)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "resolve collaborator: %v", err)
	}
	now := s.now().UTC()
	timeout := defaultDelegationTimeout
	if request.TimeoutSeconds > 0 {
		timeout = time.Duration(request.TimeoutSeconds) * time.Second
	}
	hopBudget := int(request.HopBudget)
	if hopBudget <= 0 {
		hopBudget = defaultDelegationHopBudget
	}
	route := &coordination.Route{
		ID: s.newID(), BackendKind: target.BackendKind,
		CollaboratorID: strings.TrimSpace(request.CollaboratorId), TargetRef: target.TargetRef,
		InitiatorRef: strings.TrimSpace(request.InitiatorRef), ScopeRef: strings.TrimSpace(request.ScopeRef),
		ParentDelegationID: strings.TrimSpace(request.ParentDelegationId), Input: request.Input,
		Deadline: now.Add(timeout), TrustLevel: target.TrustLevel,
		IdempotencyKey: strings.TrimSpace(request.IdempotencyKey), HopCount: 1, HopBudget: hopBudget, CreatedAt: now,
	}
	claimed, created, err := s.store.Claim(ctx, route)
	if err != nil {
		return nil, coordinationStatusError("claim delegation", err)
	}
	backend, backendErr := s.backendFor(claimed)
	if backendErr != nil {
		if created {
			_ = s.store.DeleteUnbound(context.WithoutCancel(ctx), claimed.ID)
		}
		return nil, backendErr
	}
	if created {
		if prebound, ok := backend.(coordination.PreboundBackend); ok {
			backendRef, refErr := prebound.PrebindRef(claimed)
			if refErr != nil {
				_ = s.store.DeleteUnbound(context.WithoutCancel(ctx), claimed.ID)
				return nil, coordinationBackendError("prepare delegation backend", codes.Internal, refErr)
			}
			// Persist the deterministic mapping before Create can emit an
			// external side effect. WithoutCancel closes the request-cancel
			// window between platform acceptance and route durability.
			claimed, err = s.store.Bind(context.WithoutCancel(ctx), claimed.ID, backendRef)
			if err != nil {
				return nil, coordinationStatusError("bind delegation", err)
			}
			createdRef, createErr := backend.Create(ctx, claimed)
			if createErr != nil {
				return nil, coordinationBackendError("create delegation backend", codes.Internal, createErr)
			}
			if createdRef != backendRef {
				return nil, status.Errorf(codes.Internal, "create delegation backend returned ref %q, want pre-bound ref %q", createdRef, backendRef)
			}
		} else {
			backendRef, createErr := backend.Create(ctx, claimed)
			if createErr != nil {
				_ = s.store.DeleteUnbound(context.WithoutCancel(ctx), claimed.ID)
				return nil, coordinationBackendError("create delegation backend", codes.Internal, createErr)
			}
			claimed, err = s.store.Bind(context.WithoutCancel(ctx), claimed.ID, backendRef)
			if err != nil {
				claimed.BackendRef = backendRef
				_ = backend.Cancel(context.WithoutCancel(ctx), claimed)
				return nil, coordinationStatusError("bind delegation", err)
			}
		}
	} else {
		if !sameDelegationRequest(claimed, route) {
			return nil, status.Error(codes.FailedPrecondition, "idempotency key belongs to a different delegation request")
		}
		if claimed.BackendRef == "" {
			return nil, status.Error(codes.Unavailable, "delegation is still being created; retry with the same idempotency key")
		}
		// A pre-bound backend may have been interrupted after its route was
		// persisted but before its lifecycle record was finished. Its Create
		// contract is idempotent and repairs that cross-store window.
		if _, ok := backend.(coordination.PreboundBackend); ok {
			createdRef, createErr := backend.Create(ctx, claimed)
			if createErr != nil {
				return nil, coordinationBackendError("resume delegation backend", codes.Internal, createErr)
			}
			if createdRef != claimed.BackendRef {
				return nil, status.Errorf(codes.Internal, "resume delegation backend returned ref %q, want bound ref %q", createdRef, claimed.BackendRef)
			}
		}
	}
	snapshot, err := backend.Get(ctx, claimed)
	if err != nil {
		return nil, coordinationBackendError("read delegation backend", codes.Internal, err)
	}
	return &pb.DelegateResponse{Snapshot: s.snapshotToPB(claimed, snapshot), Created: created}, nil
}

func (s *coordinationServer) GetDelegation(ctx context.Context, request *pb.GetDelegationRequest) (*pb.GetDelegationResponse, error) {
	if request == nil || strings.TrimSpace(request.DelegationId) == "" {
		return nil, status.Error(codes.InvalidArgument, "delegation_id is required")
	}
	route, err := s.boundRoute(ctx, request.DelegationId)
	if err != nil {
		return nil, err
	}
	backend, err := s.backendFor(route)
	if err != nil {
		return nil, err
	}
	snapshot, err := backend.Get(ctx, route)
	if err != nil {
		return nil, coordinationBackendError("get delegation", codes.Internal, err)
	}
	return &pb.GetDelegationResponse{Snapshot: s.snapshotToPB(route, snapshot)}, nil
}

func (s *coordinationServer) WaitDelegation(ctx context.Context, request *pb.WaitDelegationRequest) (*pb.WaitDelegationResponse, error) {
	if request == nil || strings.TrimSpace(request.DelegationId) == "" {
		return nil, status.Error(codes.InvalidArgument, "delegation_id is required")
	}
	if request.TimeoutSeconds < 0 {
		return nil, status.Error(codes.InvalidArgument, "timeout_seconds must not be negative")
	}
	route, err := s.boundRoute(ctx, request.DelegationId)
	if err != nil {
		return nil, err
	}
	waitFor := time.Duration(request.TimeoutSeconds) * time.Second
	if waitFor <= 0 || waitFor > maxCoordinationWait {
		waitFor = maxCoordinationWait
	}
	waitCtx, cancel := context.WithTimeout(ctx, waitFor)
	defer cancel()
	backend, err := s.backendFor(route)
	if err != nil {
		return nil, err
	}
	snapshot, err := backend.Wait(waitCtx, route, request.FromSeq)
	if err != nil {
		return nil, coordinationBackendError("wait delegation", codes.Internal, err)
	}
	timedOut := errors.Is(waitCtx.Err(), context.DeadlineExceeded) && len(snapshot.Events) == 0 && !snapshot.Delegation.Status.IsTerminal()
	return &pb.WaitDelegationResponse{Snapshot: s.snapshotToPB(route, snapshot), TimedOut: timedOut}, nil
}

func (s *coordinationServer) InterveneDelegation(ctx context.Context, request *pb.InterveneDelegationRequest) (*pb.InterveneDelegationResponse, error) {
	if request == nil || strings.TrimSpace(request.DelegationId) == "" || strings.TrimSpace(request.RequestId) == "" {
		return nil, status.Error(codes.InvalidArgument, "delegation_id and request_id are required")
	}
	decision := coordination.PermissionDecision(strings.TrimSpace(request.Decision))
	if decision != coordination.PermissionAllow && decision != coordination.PermissionDeny {
		return nil, status.Error(codes.InvalidArgument, "decision must be allow or deny")
	}
	route, err := s.boundRoute(ctx, request.DelegationId)
	if err != nil {
		return nil, err
	}
	response := coordination.PermissionResponse{RequestID: strings.TrimSpace(request.RequestId), Decision: decision, OptionID: strings.TrimSpace(request.OptionId), Reason: strings.TrimSpace(request.Reason)}
	backend, err := s.backendFor(route)
	if err != nil {
		return nil, err
	}
	if err := backend.RespondPermission(ctx, route, response); err != nil {
		return nil, coordinationBackendError("intervene delegation", codes.FailedPrecondition, err)
	}
	snapshot, err := backend.Get(ctx, route)
	if err != nil {
		return nil, coordinationBackendError("read delegation", codes.Internal, err)
	}
	return &pb.InterveneDelegationResponse{Delegation: s.delegationToPB(route, snapshot.Delegation), Accepted: true}, nil
}

func (s *coordinationServer) CancelDelegation(ctx context.Context, request *pb.CancelDelegationRequest) (*pb.CancelDelegationResponse, error) {
	if request == nil || strings.TrimSpace(request.DelegationId) == "" {
		return nil, status.Error(codes.InvalidArgument, "delegation_id is required")
	}
	route, err := s.boundRoute(ctx, request.DelegationId)
	if err != nil {
		return nil, err
	}
	backend, err := s.backendFor(route)
	if err != nil {
		return nil, err
	}
	snapshot, err := backend.Get(ctx, route)
	if err != nil {
		return nil, coordinationBackendError("get delegation", codes.Internal, err)
	}
	if snapshot.Delegation.Status.IsTerminal() {
		return nil, status.Errorf(codes.FailedPrecondition, "delegation %s is already terminal (%s)", route.ID, snapshot.Delegation.Status)
	}
	if err := backend.Cancel(ctx, route); err != nil {
		return nil, coordinationBackendError("cancel delegation", codes.FailedPrecondition, err)
	}
	return &pb.CancelDelegationResponse{Delegation: s.delegationToPB(route, snapshot.Delegation), Accepted: true}, nil
}

func (s *coordinationServer) backendFor(route *coordination.Route) (coordination.Backend, error) {
	if route == nil {
		return nil, status.Error(codes.Internal, "delegation route is required")
	}
	backend := s.backends[route.BackendKind]
	if backend == nil {
		return nil, status.Errorf(codes.Unavailable, "coordination backend %q is unavailable", route.BackendKind)
	}
	return backend, nil
}

func (s *coordinationServer) boundRoute(ctx context.Context, id string) (*coordination.Route, error) {
	route, err := s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, coordinationStatusError("get delegation route", err)
	}
	if route.BackendRef == "" {
		return nil, status.Error(codes.Unavailable, "delegation is still being created")
	}
	return route, nil
}

func sameDelegationRequest(left, right *coordination.Route) bool {
	return left != nil && right != nil &&
		left.CollaboratorID == right.CollaboratorID &&
		left.TargetRef == right.TargetRef &&
		left.InitiatorRef == right.InitiatorRef &&
		left.ScopeRef == right.ScopeRef &&
		left.ParentDelegationID == right.ParentDelegationID &&
		left.Input == right.Input
}

func collaboratorToPB(item coordination.CollaboratorDescriptor) *pb.CollaboratorDescriptor {
	capabilities := make([]string, 0, len(item.Capabilities))
	for _, capability := range item.Capabilities {
		capabilities = append(capabilities, string(capability))
	}
	return &pb.CollaboratorDescriptor{Id: item.ID, Name: item.Name, Description: item.Description, Kind: string(item.Kind), Availability: string(item.Availability), Capabilities: capabilities, Skills: append([]string(nil), item.Skills...)}
}

func (s *coordinationServer) snapshotToPB(route *coordination.Route, snapshot *coordination.Snapshot) *pb.CoordinationSnapshot {
	if snapshot == nil {
		return nil
	}
	events := make([]*pb.CoordinationEvent, 0, len(snapshot.Events))
	for _, event := range snapshot.Events {
		events = append(events, coordinationEventToPB(event))
	}
	return &pb.CoordinationSnapshot{Delegation: s.delegationToPB(route, snapshot.Delegation), Events: events, NextSeq: snapshot.NextSeq}
}

func (s *coordinationServer) delegationToPB(route *coordination.Route, item *coordination.Delegation) *pb.CoordinationDelegation {
	if item == nil {
		return nil
	}
	collaboratorID := ""
	if route != nil {
		collaboratorID = route.CollaboratorID
	}
	return &pb.CoordinationDelegation{Id: item.ID, CollaboratorId: collaboratorID, Direction: string(item.Direction), InitiatorRef: item.InitiatorRef, ScopeRef: item.ScopeRef, ParentDelegationId: item.ParentDelegationID, HopCount: int32(item.HopCount), HopBudget: int32(item.HopBudget), Input: item.Input, Status: string(item.Status), DeadlineUnixMs: unixMillis(item.Deadline), Result: coordinationResultToPB(item.Result), Failure: coordinationFailureToPB(item.Failure), CreatedAtUnixMs: unixMillis(item.CreatedAt), UpdatedAtUnixMs: unixMillis(item.UpdatedAt), FinishedAtUnixMs: unixMillis(item.FinishedAt)}
}

func coordinationEventToPB(event coordination.Event) *pb.CoordinationEvent {
	return &pb.CoordinationEvent{Seq: event.Seq, Kind: string(event.Kind), Status: string(event.Status), Content: event.Content, Result: coordinationResultToPB(event.Result), Failure: coordinationFailureToPB(event.Failure), Permission: coordinationPermissionToPB(event.Permission), AtUnixMs: unixMillis(event.At)}
}
func coordinationResultToPB(result *coordination.ResultEnvelope) *pb.CoordinationResult {
	if result == nil {
		return nil
	}
	return &pb.CoordinationResult{Summary: result.Summary, Content: result.Content, ContentType: result.ContentType, DetailsRef: result.DetailsRef, TrustLevel: string(result.TrustLevel), SourceRefs: append([]string(nil), result.SourceRefs...)}
}
func coordinationFailureToPB(failure *coordination.Failure) *pb.CoordinationFailure {
	if failure == nil {
		return nil
	}
	return &pb.CoordinationFailure{Code: string(failure.Code), Message: failure.Message}
}
func coordinationPermissionToPB(permission *coordination.PermissionRequest) *pb.CoordinationPermissionRequest {
	if permission == nil {
		return nil
	}
	return &pb.CoordinationPermissionRequest{Id: permission.ID, Summary: permission.Summary, Tool: permission.Tool, Command: permission.Command, Paths: append([]string(nil), permission.Paths...), Write: permission.Write, Network: permission.Network}
}

func coordinationStatusError(operation string, err error) error {
	if errors.Is(err, coordination.ErrNotFound) {
		return status.Errorf(codes.NotFound, "%s: %v", operation, err)
	}
	if errors.Is(err, coordination.ErrRouteCreating) {
		return status.Errorf(codes.Unavailable, "%s: %v", operation, err)
	}
	if errors.Is(err, coordination.ErrRouteAlreadyBound) {
		return status.Errorf(codes.FailedPrecondition, "%s: %v", operation, err)
	}
	return status.Errorf(codes.Internal, "%s: %v", operation, err)
}

func coordinationBackendError(operation string, fallback codes.Code, err error) error {
	if backendStatus, ok := status.FromError(err); ok {
		return status.Errorf(backendStatus.Code(), "%s: %s", operation, backendStatus.Message())
	}
	return status.Errorf(fallback, "%s: %v", operation, err)
}

func unixMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}
