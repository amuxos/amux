package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/agentinstance"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/executionauth"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// taskRouterHub is the subset of *tunnel.Hub the router needs. It is an interface
// so the router is unit-testable with a fake hub (mirrors reserveRouter).
type taskRouterHub interface {
	Route(ctx context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error)
	RouteStream(ctx context.Context, targetID, method string, payload interface{}, fn func(*pb.TunnelMsg) error) error
	ListTargets() []*pb.Target
}

// taskScheduler is the subset of *registry.Scheduler the router needs.
type taskScheduler interface {
	Acquire(req registry.ScheduleRequest) (*registry.Placement, error)
	Commit(p *registry.Placement) error
	Release(p *registry.Placement) error
}

// taskRouter implements pb.AmuxTaskServer as the amuxd control plane: it places
// each subtask on a runtime via the registry scheduler (reserve -> commit
// admission) and routes the AmuxTask call to that runtime's in-process control
// plane over the reverse tunnel, recording task_id -> placement so later
// Get/Cancel/Respond/Stream reach the right runtime.
//
// The task.Runner stays on the amuxlet: amuxd never touches the child session
// RPCs, so owner enforcement is preserved (the runner drives the child session
// in-process). amuxd is the task-truth (the placement index); the amuxlet is the
// session-truth.
type taskRouter struct {
	pb.UnimplementedAmuxTaskServer
	hub                 taskRouterHub
	sched               taskScheduler
	index               *taskIndex
	instances           *agentinstance.Store
	principals          *principal.Store
	instanceSubmitMu    sync.Mutex
	instanceSubmitLocks map[string]*instanceSubmitLock
	// resolveProfile rewrites a profile-ref spec to its base agent and attaches the
	// ResolvedProfile BEFORE scheduling (the scheduler must see the base type).
	// Default resolver reads the hub host's agent-pack registry; tests override.
	resolveProfile func(spec *task.Spec) error
	// credentialProvider resolves env-only credential requirements before
	// scheduling so runtime credential allowlists still see the actual profiles.
	credentialProvider func() (task.CredentialProvider, error)
	// isolationGlobalEnv contains only the hub installation's configured shared
	// launch values. Values never enter ScheduleRequest or routed task specs; the
	// matching runtime reads its own copy of the same operator configuration.
	isolationGlobalEnv []string
	// parentNotify delivers background-task completion messages to the
	// parent session (experimental, gated by steer_foreground).
	parentNotify *parentNotifier
}

type scheduledSubmitContextKey struct{}

type instanceSubmitLock struct {
	mu   sync.Mutex
	refs int
}

func newTaskRouter(hub taskRouterHub, sched taskScheduler, index *taskIndex, instances *agentinstance.Store) *taskRouter {
	return &taskRouter{
		hub:                 hub,
		sched:               sched,
		index:               index,
		instances:           instances,
		instanceSubmitLocks: make(map[string]*instanceSubmitLock),
		resolveProfile: func(spec *task.Spec) error {
			return agentprofile.ResolveTaskSpec(spec, agentprofile.DefaultPackRegistryPath())
		},
		credentialProvider: defaultHubCredentialProvider,
		parentNotify:       &parentNotifier{},
	}
}

func (r *taskRouter) lockInstanceSubmit(instanceID string) func() {
	r.instanceSubmitMu.Lock()
	lock := r.instanceSubmitLocks[instanceID]
	if lock == nil {
		lock = &instanceSubmitLock{}
		r.instanceSubmitLocks[instanceID] = lock
	}
	lock.refs++
	r.instanceSubmitMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		r.instanceSubmitMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(r.instanceSubmitLocks, instanceID)
		}
		r.instanceSubmitMu.Unlock()
	}
}

func (r *taskRouter) claimPersistentInstanceIdentity(spec task.Spec) (func(), error) {
	claim, err := r.instances.ClaimIdentity(spec.AgentInstanceID, agentinstance.InstanceIdentity{
		AgentType:        spec.Agent,
		AgentProfile:     resolvedProfileRefForInstance(spec.ResolvedProfile),
		ModelRef:         resolvedModelRefForInstance(spec),
		PermissionPolicy: effectiveTaskPermissionPolicy(spec),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	var conflict *agentinstance.IdentityConflictError
	if errors.As(err, &conflict) {
		if conflict.Field == "permission policy" {
			return nil, status.Error(codes.InvalidArgument, agentprofile.TaskPermissionResumeError(conflict.Existing, conflict.Requested).Error())
		}
		return nil, status.Errorf(codes.InvalidArgument,
			"persistent agent %s is fixed to %s %q; follow-up requested %q",
			spec.AgentInstanceID, conflict.Field, conflict.Existing, conflict.Requested)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "claim persistent agent identity %s: %v", spec.AgentInstanceID, err)
	}
	if !claim.Changed {
		return nil, nil
	}
	return func() {
		if err := r.instances.RollbackIdentityClaim(claim); err != nil {
			fmt.Fprintf(os.Stderr, "hub: rollback persistent agent identity %s: %v\n", spec.AgentInstanceID, err)
		}
	}, nil
}

func (r *taskRouter) SubmitScheduledTask(ctx context.Context, specJSON []byte) (string, error) {
	var spec task.Spec
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "decode scheduled task spec: %v", err)
	}
	if err := normalizeScheduledTaskSpec(&spec); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if r.resolveProfile != nil {
		if err := r.resolveProfile(&spec); err != nil {
			return "", status.Errorf(codes.InvalidArgument, "resolve agent profile: %v", err)
		}
	}
	if err := validateManagedEvaluationTask(spec, true); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := normalizeResolvedScheduledTaskSpec(&spec); err != nil {
		return "", status.Errorf(codes.InvalidArgument, "%v", err)
	}
	resolvedSpecJson, err := json.Marshal(spec)
	if err != nil {
		return "", status.Errorf(codes.Internal, "encode resolved scheduled task spec: %v", err)
	}
	ctx = context.WithValue(ctx, scheduledSubmitContextKey{}, true)
	resp, err := r.SubmitSubtask(ctx, &pb.SubmitSubtaskRequest{SpecJson: resolvedSpecJson})
	if err != nil {
		return "", err
	}
	id := taskIDFromJSON(resp.TaskJson)
	if id == "" {
		return "", errors.New("submitted task response did not include task id")
	}
	return id, nil
}

// SubmitSubtask schedules the task onto a runtime, routes the submit, and on
// success commits the admission reservation and records the placement. The
// reservation is released if scheduling-then-routing fails before commit.
func (r *taskRouter) SubmitSubtask(ctx context.Context, req *pb.SubmitSubtaskRequest) (*pb.SubmitSubtaskResponse, error) {
	var spec task.Spec
	markIdentityAccepted := func() {}
	if err := json.Unmarshal(req.SpecJson, &spec); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode spec: %v", err)
	}
	if err := r.hydratePersistentFollowupIdentity(&spec); err != nil {
		return nil, err
	}
	if spec.Agent == "" {
		return nil, status.Errorf(codes.InvalidArgument, "agent is required")
	}
	trustedScheduledIdentity, _ := ctx.Value(scheduledSubmitContextKey{}).(bool)
	if strings.TrimSpace(spec.ParentTurnID) != "" {
		return nil, status.Error(codes.InvalidArgument, "parent_turn_id is server-resolved")
	}
	if spec.ExecutionIdentity != nil && !trustedScheduledIdentity {
		return nil, status.Error(codes.InvalidArgument, "execution_identity is server-resolved")
	}
	identity := principal.LegacyHostIdentity(principal.SourceMCP)
	submitParent, hasSubmitParent := r.parentSession(spec.ParentSessionID)
	if strings.TrimSpace(spec.ParentSessionID) != "" && !hasSubmitParent {
		return nil, status.Error(codes.NotFound, "parent session not found")
	}
	if hasSubmitParent {
		expectedParentTaskID := strings.TrimSpace(submitParent.OwnerTaskID)
		providedParentTaskID := strings.TrimSpace(spec.ParentTaskID)
		if providedParentTaskID != "" && providedParentTaskID != expectedParentTaskID {
			return nil, status.Error(codes.InvalidArgument, "parent_task_id does not match parent session owner")
		}
		spec.ParentTaskID = expectedParentTaskID
	}
	var actor *principal.ActorContext
	if !trustedScheduledIdentity {
		var actorErr error
		actor, actorErr = authenticatedActor(ctx, r.principals)
		if actorErr != nil {
			return nil, actorErr
		}
	}
	var parent *executionauth.SessionFacts
	if hasSubmitParent {
		parent = &executionauth.SessionFacts{ID: submitParent.SessionID, Identity: submitParent.Identity}
	}
	var savedIdentity *principal.ExecutionIdentity
	if saved, ok := r.savedInstanceIdentity(spec); ok {
		savedIdentity = &saved
	}
	var scheduledIdentity *principal.ExecutionIdentity
	executionStateRef := "task:pending"
	if trustedScheduledIdentity && spec.ExecutionIdentity != nil {
		scheduledIdentity = spec.ExecutionIdentity
		executionStateRef = "scheduled-task:" + uuid.NewString()
	}
	identityResult := executionauth.New(r.principals).FreezeTaskIdentity(executionauth.FreezeTaskRequest{
		Actor:             actor,
		RunAs:             spec.RunAs,
		ParentTaskID:      spec.ParentTaskID,
		ExecutionStateRef: executionStateRef,
		Parent:            parent,
		SavedIdentity:     savedIdentity,
		ScheduledIdentity: scheduledIdentity,
		IsolationActive:   r.fleetIsolationActive(),
	})
	if !identityResult.Decision.Allowed {
		return nil, taskIdentityDecisionError(identityResult.Decision, hasSubmitParent, submitParent.Identity)
	}
	identity = identityResult.Identity
	// Conversation background work inherits a finalized backend. Scheduled
	// definitions normally clear this pin so each trigger can select a backend.
	if preserve, _ := ctx.Value(conversationSubmitContextKey{}).(bool); preserve && trustedScheduledIdentity && scheduledIdentity != nil {
		identity.IsolationBackend = scheduledIdentity.IsolationBackend
		identity.Source = scheduledIdentity.Source
	}
	var parentEngine, parentModel, parentEffort string
	if hasSubmitParent {
		active, err := r.activeParentTurn(ctx, submitParent)
		if err != nil {
			return nil, err
		}
		if !active.GetActive() || strings.TrimSpace(active.GetTurnId()) == "" {
			return nil, status.Error(codes.FailedPrecondition, "parent session has no active turn")
		}
		spec.ParentTurnID = active.GetTurnId()
		parentEngine = strings.TrimSpace(submitParent.AgentType)
		parentModel = strings.TrimSpace(active.GetEffectiveModel())
		parentEffort = strings.TrimSpace(active.GetReasoningEffort())
	}
	spec.ExecutionIdentity = &identity

	if r.resolveProfile != nil {
		pristine := spec
		if err := r.resolveProfile(&spec); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "resolve agent profile: %v", err)
		}
		r.inheritParentExecution(&spec, pristine, parentEngine, parentModel, parentEffort)
	}
	if err := validateManagedEvaluationTask(spec, false); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	// Re-marshal so only the server-resolved identity/profile travels to the
	// runtime; the runtime never interprets the caller's run_as alias.
	if config.SupportsAgentExecutable(spec.Agent) {
		spec.AgentExecutable = config.GetAgentExecutable(spec.Agent)
	}
	b, merr := json.Marshal(spec)
	if merr != nil {
		return nil, status.Errorf(codes.Internal, "encode resolved spec: %v", merr)
	}
	req = &pb.SubmitSubtaskRequest{SpecJson: b}

	// Mint a delegated actor credential for an identified launch so the task's
	// agent reaches the control plane as the principal it already runs as, not
	// anonymously. launch_id is generated here (not by the amuxlet) so the
	// credential binds to this specific launch and a re-issue revokes the prior
	// token; it travels on the wire only, never into the persisted spec.
	if identity.Identified() && r.principals != nil {
		launchID := "launch-" + uuid.NewString()
		token, expiresAt := mintAgentActorToken(r.principals, identity, launchID)
		req.LaunchId = launchID
		req.AgentActorToken = token
		req.AgentActorTokenExpiresAt = expiresAt
	}
	if spec.Mode == task.ModePersistent && spec.AgentInstanceID != "" && r.instances != nil {
		unlock := r.lockInstanceSubmit(spec.AgentInstanceID)
		defer unlock()
		rollbackIdentity, err := r.claimPersistentInstanceIdentity(spec)
		if err != nil {
			return nil, err
		}
		identityAccepted := false
		if rollbackIdentity != nil {
			defer func() {
				if !identityAccepted {
					rollbackIdentity()
				}
			}()
		}
		markIdentityAccepted = func() { identityAccepted = true }
	}

	// Resolve a sticky target for persistent agent instances: route a follow-up
	// task back to the target hosting the instance's durable environment. The
	// scheduler still runs Reserve -> Commit admission on the chosen target, so
	// capacity semantics are unchanged; sticky only biases the placement.
	schedReq, err := r.scheduleRequestFromSpec(ctx, spec)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if spec.Mode == task.ModePersistent && spec.AgentInstanceID != "" {
		schedReq.StickyTargetID = r.resolveStickyTarget(spec.AgentInstanceID)
	}

	placement, err := r.sched.Acquire(schedReq)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrNoMatchingRuntime):
			return nil, status.Errorf(codes.Unavailable, "no runtime can run this task (agent/isolation/workspace/selector): %v", err)
		case errors.Is(err, registry.ErrNoCapacityAnywhere):
			return nil, status.Errorf(codes.ResourceExhausted, "%v", err)
		default:
			return nil, status.Errorf(codes.Internal, "schedule: %v", err)
		}
	}

	resp, rerr := r.hub.Route(ctx, placement.TargetID, pb.TaskMethodSubmit, req)
	if rerr != nil {
		// Not committed: the reservation TTL is the backstop if release fails.
		_ = r.sched.Release(placement)
		return nil, status.Errorf(codes.Internal, "route submit to %s: %v", placement.TargetID, rerr)
	}
	markIdentityAccepted()
	out := &pb.SubmitSubtaskResponse{}
	if jerr := json.Unmarshal(resp.Payload, out); jerr != nil {
		_ = r.sched.Release(placement) // still uncommitted; TTL bounds a failed release
		return nil, status.Errorf(codes.Internal, "decode submit response: %v", jerr)
	}

	// The runtime accepted the task (its runner persisted the pending TaskRun and
	// is starting the child session in-process). Confirm the reservation now —
	// this routes within the reservation TTL because the runner's Submit returns
	// before the agent process starts. If Commit loses the race anyway, the task
	// is still running; record it and let the reconciler reconcile accounting
	// rather than fail the user's submit.
	_ = r.sched.Commit(placement)

	if id := taskIDFromJSON(out.TaskJson); id != "" {
		r.index.put(id, taskPlacement{TargetID: placement.TargetID, ReservationId: placement.ReservationId, ParentSessionID: spec.ParentSessionID, AgentInstanceID: spec.AgentInstanceID})
		r.watchForParentNotify(spec, id)
	}

	// Record (or refresh) the durable agent instance so a later follow-up task
	// can sticky-route to the same target. Best-effort: a store miss on a
	// persistent spec creates the record; an existing record is refreshed with
	// the chosen target. Provider-session id is filled later by the amuxlet
	// reporting path (Phase 4); until then the record carries target only.
	if spec.Mode == task.ModePersistent && spec.AgentInstanceID != "" {
		r.recordPersistentInstance(spec, placement.TargetID)
	}
	return out, nil
}

func validateManagedEvaluationTask(spec task.Spec, scheduled bool) error {
	ref := ""
	if spec.ResolvedProfile != nil {
		ref = spec.ResolvedProfile.GetRef()
	}
	if !agentprofile.IsManagedEvaluationProfileRef(ref) {
		return nil
	}
	if scheduled {
		return fmt.Errorf("local expert evaluation profiles cannot be scheduled")
	}
	if spec.Mode != "" && spec.Mode != task.ModeEphemeral {
		return fmt.Errorf("local expert evaluation profiles only support ephemeral tasks")
	}
	return nil
}

func (r *taskRouter) savedInstanceIdentity(spec task.Spec) (principal.ExecutionIdentity, bool) {
	if spec.Mode != task.ModePersistent || strings.TrimSpace(spec.AgentInstanceID) == "" || r.instances == nil {
		return principal.ExecutionIdentity{}, false
	}
	instance, err := r.instances.Get(spec.AgentInstanceID)
	if err != nil || instance.ExecutionIdentity.IdentityMode == "" {
		return principal.ExecutionIdentity{}, false
	}
	return instance.ExecutionIdentity, true
}

func taskIdentityDecisionError(decision executionauth.Decision, hasParent bool, parentIdentity principal.ExecutionIdentity) error {
	switch decision.Reason {
	case executionauth.ReasonInvalidSavedIdentity:
		return status.Errorf(codes.InvalidArgument, "saved scheduled execution identity: %v", decision.Err)
	case executionauth.ReasonScheduledAuthorityInactive:
		return status.Errorf(codes.PermissionDenied, "saved scheduled authority is no longer active: %v", decision.Err)
	case executionauth.ReasonParentClaimDenied:
		return status.Errorf(codes.PermissionDenied, "parent session belongs to another principal: %v", decision.Err)
	case executionauth.ReasonPersistentRunAsChange:
		return status.Error(codes.PermissionDenied, "persistent followup cannot change run_as")
	case executionauth.ReasonPersistentFollowupDenied:
		return status.Errorf(codes.PermissionDenied, "persistent followup authorization: %v", decision.Err)
	case executionauth.ReasonActorRequired:
		if hasParent && parentIdentity.Identified() {
			return status.Error(codes.Unauthenticated, "claiming a parent session requires a credential")
		}
		return status.Error(codes.Unauthenticated, "persistent followup requires its parent principal or enrollment")
	case executionauth.ReasonRunAsRequiresActor:
		if hasParent && !parentIdentity.Identified() {
			return status.Error(codes.Unauthenticated, "legacy parent cannot use run_as")
		}
		return status.Error(codes.Unauthenticated, "run_as requires enrollment or parent execution")
	case executionauth.ReasonIdentityResolution:
		if hasParent && parentIdentity.Identified() {
			return status.Errorf(codes.PermissionDenied, "inherit parent execution identity: %v", decision.Err)
		}
		return status.Errorf(codes.PermissionDenied, "resolve execution identity: %v", decision.Err)
	default:
		return status.Error(codes.Internal, "execution authorization returned an unsupported task identity decision")
	}
}

type parentSessionContext struct {
	TargetID    string
	SessionID   string
	SessionName string
	OwnerTaskID string
	AgentType   string
	Identity    principal.ExecutionIdentity
}

func (r *taskRouter) parentSession(sessionID string) (parentSessionContext, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || r.hub == nil {
		return parentSessionContext{}, false
	}
	for _, target := range r.hub.ListTargets() {
		if target == nil {
			continue
		}
		for _, sess := range target.Sessions {
			if sess != nil && sess.Id == sessionID {
				identity := principal.FromProtoIdentity(sess.ExecutionIdentity)
				if identity.IdentityMode == "" {
					identity = principal.LegacyHostIdentity(principal.SourceMCP)
				}
				return parentSessionContext{TargetID: target.Id, SessionID: sess.Id, SessionName: sess.Name, OwnerTaskID: sess.OwnerTaskId, AgentType: strings.TrimSpace(sess.GetAgentType()), Identity: identity}, true
			}
		}
	}
	return parentSessionContext{}, false
}

func (r *taskRouter) activeParentTurn(ctx context.Context, parent parentSessionContext) (*pb.ActiveTurnResponse, error) {
	resp, err := r.hub.Route(ctx, parent.TargetID, pb.TaskMethodResolveParentTurn, &pb.ActiveTurnRequest{Name: parent.SessionName})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "resolve active parent turn: %v", err)
	}
	var out pb.ActiveTurnResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, status.Errorf(codes.Internal, "decode active parent turn: %v", err)
	}
	if out.GetSessionId() != parent.SessionID {
		return nil, status.Error(codes.FailedPrecondition, "active parent turn resolved to a different session")
	}
	return &out, nil
}

// inheritParentExecution re-resolves a subtask with the parent session's
// current execution choices when the caller omitted the model and the child
// runs on the parent's engine. Inherited choices that do not resolve for the
// child profile keep the caller's own resolution, so task defaults can never
// turn a valid submission into a failure.
func (r *taskRouter) inheritParentExecution(spec *task.Spec, pristine task.Spec, parentEngine, parentModel, parentEffort string) {
	if spec == nil || r.resolveProfile == nil || parentEngine == "" || parentModel == "" {
		return
	}
	if strings.TrimSpace(pristine.Model) != "" || strings.TrimSpace(spec.Agent) != parentEngine {
		return
	}
	retry := pristine
	retry.Model = parentModel
	if parentEffort != "" && strings.TrimSpace(retry.ReasoningEffort) == "" {
		retry.ReasoningEffort = parentEffort
	}
	if err := r.resolveProfile(&retry); err != nil {
		// An inherited effort can be invalid for the model the child profile
		// resolved; keep the inherited model alone rather than dropping both.
		if strings.TrimSpace(retry.ReasoningEffort) == strings.TrimSpace(pristine.ReasoningEffort) {
			return
		}
		retry.ReasoningEffort = pristine.ReasoningEffort
		if err := r.resolveProfile(&retry); err != nil {
			return
		}
	}
	*spec = retry
}

// hydratePersistentFollowupIdentity restores the immutable launch identity for
// the public instance_followup shape, which intentionally carries only
// agent_instance_id + task. Explicit identity fields remain authoritative and
// are validated by ClaimIdentity below, preserving the no-switch contract.
func (r *taskRouter) hydratePersistentFollowupIdentity(spec *task.Spec) error {
	if spec == nil || spec.Mode != task.ModePersistent || strings.TrimSpace(spec.AgentInstanceID) == "" || r.instances == nil {
		return nil
	}
	in, err := r.instances.Get(spec.AgentInstanceID)
	if errors.Is(err, sql.ErrNoRows) {
		if strings.TrimSpace(spec.Agent) != "" {
			return nil // A new instance with an explicit launch identity.
		}
		return status.Errorf(codes.NotFound, "agent instance %q not found", spec.AgentInstanceID)
	}
	if err != nil {
		return status.Errorf(codes.Internal, "load persistent agent identity %s: %v", spec.AgentInstanceID, err)
	}
	if spec.PermissionPolicy == "" {
		spec.PermissionPolicy = task.PermissionPolicy(in.PermissionPolicy)
	}
	if strings.TrimSpace(spec.Agent) != "" {
		return nil
	}
	spec.Agent = strings.TrimSpace(in.AgentProfile)
	if spec.Agent == "" {
		spec.Agent = strings.TrimSpace(in.AgentType)
	}
	if strings.TrimSpace(spec.Runtime) == "" {
		spec.Runtime = strings.TrimSpace(in.AgentType)
	}
	if strings.TrimSpace(spec.Model) == "" {
		spec.Model = strings.TrimSpace(in.ModelRef)
	}
	return nil
}

// resolveStickyTarget returns the target id that already hosts the given agent
// instance, or "" if unknown. It consults the durable instance store first
// (survives amuxd restarts) and falls back to the in-memory task index (covers
// the window between instance creation and the first durable write).
func (r *taskRouter) resolveStickyTarget(agentInstanceID string) string {
	if r.instances != nil {
		if in, err := r.instances.Get(agentInstanceID); err == nil && in.TargetID != "" {
			return in.TargetID
		}
	}
	if p, ok := r.index.getInstance(agentInstanceID); ok && p.TargetID != "" {
		return p.TargetID
	}
	return ""
}

// recordPersistentInstance upserts the durable agent instance record with the
// chosen target. It is best-effort: a missing store (tests / unconfigured hub)
// is a no-op, and a failed write is logged but does not fail the submit.
func (r *taskRouter) recordPersistentInstance(spec task.Spec, targetID string) {
	if r.instances == nil {
		return
	}
	credential, err := r.resolveCredentialPlanProfiles(context.Background(), spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hub: resolve agent instance credentials %s: %v\n", spec.AgentInstanceID, err)
		return
	}
	in := agentinstance.Instance{
		ID:                 spec.AgentInstanceID,
		AgentType:          spec.Agent,
		AgentProfile:       resolvedProfileRefForInstance(spec.ResolvedProfile),
		ModelRef:           resolvedModelRefForInstance(spec),
		PermissionPolicy:   effectiveTaskPermissionPolicy(spec),
		OwnerSessionID:     spec.ParentSessionID,
		TargetID:           targetID,
		CredentialProfile:  credential.LegacyProfile,
		CredentialProfiles: credential.Profiles,
		IsolationMode:      string(spec.Isolation),
		Status:             agentinstance.StatusActive,
	}
	if spec.ExecutionIdentity != nil {
		in.ExecutionIdentity = *spec.ExecutionIdentity
		in.IsolationBackend = spec.ExecutionIdentity.IsolationBackend
	}
	if existing, getErr := r.instances.Get(spec.AgentInstanceID); getErr == nil {
		if in.AgentProfile == "" {
			in.AgentProfile = existing.AgentProfile
		}
		if in.ModelRef == "" {
			in.ModelRef = existing.ModelRef
		}
	}
	if _, err := r.instances.Put(in); err != nil {
		fmt.Fprintf(os.Stderr, "hub: record agent instance %s: %v\n", spec.AgentInstanceID, err)
	}
}

// ReclaimInstancesForOwner archives every still-active persistent agent instance
// owned by ownerSessionID and asks each instance's hosting amuxlet to destroy its
// durable environment. It is called when the owner session enters the archived
// state (the instance follows the owner lifecycle; v0.39).
//
// The durable-store status flip happens first so the instance is hidden from
// instance_list / sticky scheduling even if the cleanup RPC cannot reach the
// target (e.g. it went offline); in that case the sandbox is left for the
// amuxlet-side sweeper to reclaim. Errors per-instance are logged but never
// abort the loop, so one bad target does not spare the remaining instances.
func (r *taskRouter) ReclaimInstancesForOwner(ownerSessionID string) {
	if r.instances == nil || ownerSessionID == "" {
		return
	}
	insts, err := r.instances.GetByOwner(ownerSessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hub: reclaim instances for owner %s: %v\n", ownerSessionID, err)
		return
	}
	for _, in := range insts {
		if in.Status == agentinstance.StatusArchived {
			continue
		}
		if _, err := r.instances.UpdateStatus(in.ID, agentinstance.StatusArchived); err != nil {
			fmt.Fprintf(os.Stderr, "hub: archive instance %s: %v\n", in.ID, err)
			continue
		}
		r.cleanupInstanceOnTarget(context.Background(), in.ID, in.TargetID)
	}
}

// cleanupInstanceOnTarget asks the hosting amuxlet to destroy the instance's
// durable environment. A missing/offline target is best-effort: the store is
// already updated to archived, and the durable sandbox is left in place for
// the amuxlet-side sweeper to reclaim when the target returns or on restart.
func (r *taskRouter) cleanupInstanceOnTarget(ctx context.Context, instanceID, targetID string) {
	if targetID == "" {
		return
	}
	req := &pb.CleanupAgentInstanceRequest{Id: instanceID}
	if _, err := r.hub.Route(ctx, targetID, pb.TaskMethodInstanceCleanup, req); err != nil {
		fmt.Fprintf(os.Stderr, "hub: cleanup instance %s on target %s: %v\n", instanceID, targetID, err)
	}
}

// GetSubtask deliberately pays a second TaskMethodGet round-trip inside
// authorizeTaskRead to load the authz identity, rather than authorizing from
// the response fetched below — that keeps the identity loader shared with the
// mutation guard. The race between the two GETs is benign: if the task is
// pruned in between, the second GET simply errors.
func (r *taskRouter) GetSubtask(ctx context.Context, req *pb.GetSubtaskRequest) (*pb.GetSubtaskResponse, error) {
	target, err := r.resolve(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := r.authorizeTaskRead(ctx, target, req.Id); err != nil {
		return nil, err
	}
	resp, rerr := r.hub.Route(ctx, target, pb.TaskMethodGet, req)
	if rerr != nil {
		return nil, status.Errorf(codes.Internal, "%v", rerr)
	}
	out := &pb.GetSubtaskResponse{}
	if jerr := json.Unmarshal(resp.Payload, out); jerr != nil {
		return nil, status.Errorf(codes.Internal, "decode: %v", jerr)
	}
	return out, nil
}

// ListSubtasks fans out to every connected runtime and merges their tasks,
// repopulating the placement index from what it learns (a recovery path after an
// amuxd restart dropped the in-memory index).
func (r *taskRouter) ListSubtasks(ctx context.Context, _ *pb.ListSubtasksRequest) (*pb.ListSubtasksResponse, error) {
	out := &pb.ListSubtasksResponse{}
	for _, t := range r.hub.ListTargets() {
		resp, err := r.hub.Route(ctx, t.Id, pb.TaskMethodList, &pb.ListSubtasksRequest{})
		if err != nil {
			continue // a runtime that can't answer (no task plane / transient) is skipped
		}
		var lr pb.ListSubtasksResponse
		if json.Unmarshal(resp.Payload, &lr) != nil {
			continue
		}
		for _, tj := range lr.TasksJson {
			out.TasksJson = append(out.TasksJson, tj)
			if id, parent := taskRefFromJSON(tj); id != "" {
				// Don't clobber a Submit-recorded entry (which has the reservation id).
				if _, known := r.index.get(id); !known {
					r.index.put(id, taskPlacement{TargetID: t.Id, ParentSessionID: parent})
				}
			}
		}
	}
	filtered, err := r.filterVisibleTasks(ctx, out.TasksJson)
	if err != nil {
		return nil, err
	}
	out.TasksJson = filtered
	return out, nil
}

func (r *taskRouter) PruneSubtasks(ctx context.Context, req *pb.PruneSubtasksRequest) (*pb.PruneSubtasksResponse, error) {
	if err := r.authorizePrune(ctx); err != nil {
		return nil, err
	}
	out := &pb.PruneSubtasksResponse{}
	connected := map[string]bool{}
	for _, t := range r.hub.ListTargets() {
		connected[t.Id] = true
		resp, err := r.hub.Route(ctx, t.Id, pb.TaskMethodPrune, req)
		if err != nil {
			out.Skipped = append(out.Skipped, &pb.SkippedSubtask{TargetId: t.Id, Reason: err.Error()})
			continue
		}
		var pr pb.PruneSubtasksResponse
		if err := json.Unmarshal(resp.Payload, &pr); err != nil {
			out.Skipped = append(out.Skipped, &pb.SkippedSubtask{TargetId: t.Id, Reason: "decode prune response: " + err.Error()})
			continue
		}
		for _, task := range pr.Tasks {
			if task.TargetId == "" {
				task.TargetId = t.Id
			}
			if !req.GetDryRun() {
				if p, ok := r.index.get(task.Id); ok {
					r.index.reclaim(task.Id, p, r.sched.Release)
				}
			}
			out.Tasks = append(out.Tasks, task)
		}
		for _, skipped := range pr.Skipped {
			if skipped.TargetId == "" {
				skipped.TargetId = t.Id
			}
			out.Skipped = append(out.Skipped, skipped)
		}
	}
	for id, p := range r.index.snapshot() {
		if !connected[p.TargetID] {
			out.Skipped = append(out.Skipped, &pb.SkippedSubtask{Id: id, TargetId: p.TargetID, Reason: "target offline"})
		}
	}
	out.DeletedCount = int32(len(out.Tasks))
	return out, nil
}

func (r *taskRouter) CancelSubtask(ctx context.Context, req *pb.CancelSubtaskRequest) (*pb.CancelSubtaskResponse, error) {
	target, err := r.resolve(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := r.authorizeTaskMutation(ctx, target, req.Id); err != nil {
		return nil, err
	}
	resp, rerr := r.hub.Route(ctx, target, pb.TaskMethodCancel, req)
	if rerr != nil {
		return nil, status.Errorf(codes.Internal, "%v", rerr)
	}
	out := &pb.CancelSubtaskResponse{}
	if jerr := json.Unmarshal(resp.Payload, out); jerr != nil {
		return nil, status.Errorf(codes.Internal, "decode: %v", jerr)
	}
	return out, nil
}

func (r *taskRouter) RespondSubtaskPermission(ctx context.Context, req *pb.RespondSubtaskPermissionRequest) (*pb.RespondSubtaskPermissionResponse, error) {
	target, err := r.resolve(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := r.authorizeTaskMutation(ctx, target, req.Id); err != nil {
		return nil, err
	}
	resp, rerr := r.hub.Route(ctx, target, pb.TaskMethodRespond, req)
	if rerr != nil {
		return nil, status.Errorf(codes.Internal, "%v", rerr)
	}
	out := &pb.RespondSubtaskPermissionResponse{}
	if jerr := json.Unmarshal(resp.Payload, out); jerr != nil {
		return nil, status.Errorf(codes.Internal, "decode: %v", jerr)
	}
	return out, nil
}

func (r *taskRouter) StreamSubtask(req *pb.StreamSubtaskRequest, stream pb.AmuxTask_StreamSubtaskServer) error {
	target, err := r.resolve(stream.Context(), req.Id)
	if err != nil {
		return err
	}
	if err := r.authorizeTaskRead(stream.Context(), target, req.Id); err != nil {
		return err
	}
	return r.hub.RouteStream(stream.Context(), target, pb.TaskMethodStream, req, func(msg *pb.TunnelMsg) error {
		ev := &pb.SubtaskEvent{}
		if jerr := json.Unmarshal(msg.Payload, ev); jerr != nil {
			return jerr
		}
		return stream.Send(ev)
	})
}

// ListAgentInstances answers from the durable agent-instance store, enriched
// with live child-session state + mailbox depth fanned out from each
// instance's hosting amuxlet. Instances whose durable record is missing (e.g.
// created before the store was initialized) are still discoverable via the
// per-target ListAgentInstances fan-out below.
func (r *taskRouter) ListAgentInstances(ctx context.Context, req *pb.ListAgentInstancesRequest) (*pb.ListAgentInstancesResponse, error) {
	out := &pb.ListAgentInstancesResponse{}
	if r.instances == nil {
		// No durable store: fall back to fanning out to connected runtimes.
		out = r.listInstancesFanout(ctx, req)
		filtered, err := r.filterVisibleInstances(ctx, out.Instances)
		if err != nil {
			return nil, err
		}
		out.Instances = filtered
		return out, nil
	}
	// Status is applied after live runtime enrichment. Filtering the durable
	// rows first hides records whose child has since moved to sleeping/archived.
	filter := agentinstance.Filter{}
	if req.OwnerSessionId != "" {
		filter.OwnerSessionID = req.OwnerSessionId
	}
	records, err := r.instances.List(filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list agent instances: %v", err)
	}
	var actor *principal.ActorContext
	if r.principals != nil {
		actor, err = authenticatedActor(ctx, r.principals)
		if err != nil {
			return nil, err
		}
	}
	seen := make(map[string]bool, len(records))
	for _, in := range records {
		// The durable record claims this id even when the row is filtered out,
		// so a fan-out copy can never re-enter authorized on the live identity.
		seen[in.ID] = true
		// Authorize the frozen store identity, captured BEFORE live enrichment
		// overwrites the proto's identity: FinalizeExecution refuses to persist
		// a live identity that diverges from frozen, so the frozen value is the
		// source of truth (same preference as authorizeInstanceMutation).
		if !r.visibleExecution(ctx, actor, in.ExecutionIdentity, in.OwnerSessionID) {
			continue
		}
		inst := instanceRecordToProto(in)
		// Enrich with live child-session state from the hosting amuxlet.
		if in.TargetID != "" && r.connected(in.TargetID) {
			if live, err := r.describeInstanceOnTarget(ctx, in.TargetID, in.ID); err == nil {
				applyLiveInstance(inst, live)
				r.persistLiveInstanceIdentity(in.ID, live)
				r.persistLiveInstanceStatus(in, inst)
			}
		}
		if req.Status != "" && inst.Status != req.Status {
			continue
		}
		out.Instances = append(out.Instances, inst)
	}
	// Append instances that exist on a runtime but have no durable record.
	for _, inst := range r.listInstancesFanout(ctx, req).Instances {
		if seen[inst.Id] || inst == nil {
			continue
		}
		// No durable record: the fan-out's reported identity is all this
		// discovered row has to authorize against.
		if !r.visibleExecution(ctx, actor, principal.FromProtoIdentity(inst.ExecutionIdentity), inst.OwnerSessionId) {
			continue
		}
		out.Instances = append(out.Instances, inst)
	}
	return out, nil
}

func (r *taskRouter) persistLiveInstanceStatus(durable agentinstance.Instance, live *pb.AgentInstance) {
	if r.instances == nil || live == nil || live.Status == "" || live.Status == durable.Status {
		return
	}
	if _, err := agentinstance.NormalizeStatus(live.Status); err != nil {
		return
	}
	_, _ = r.instances.UpdateStatus(durable.ID, live.Status)
}

// listInstancesFanout asks every connected runtime for its local agent
// instances and merges the results. Used as the no-store fallback and to
// discover store-less instances.
func (r *taskRouter) listInstancesFanout(ctx context.Context, req *pb.ListAgentInstancesRequest) *pb.ListAgentInstancesResponse {
	out := &pb.ListAgentInstancesResponse{}
	for _, t := range r.hub.ListTargets() {
		resp, err := r.hub.Route(ctx, t.Id, pb.TaskMethodInstanceList, &pb.ListAgentInstancesRequest{
			OwnerSessionId: req.OwnerSessionId,
			Status:         req.Status,
		})
		if err != nil {
			continue
		}
		var lr pb.ListAgentInstancesResponse
		if json.Unmarshal(resp.Payload, &lr) != nil {
			continue
		}
		out.Instances = append(out.Instances, lr.Instances...)
	}
	return out
}

// describeInstanceOnTarget calls the amuxlet-local DescribeAgentInstance over
// the tunnel to enrich a durable instance record with live child-session state.
func (r *taskRouter) describeInstanceOnTarget(ctx context.Context, targetID, instanceID string) (*pb.DescribeAgentInstanceResponse, error) {
	resp, err := r.hub.Route(ctx, targetID, pb.TaskMethodInstanceDescribe, &pb.DescribeAgentInstanceRequest{Id: instanceID})
	if err != nil {
		return nil, err
	}
	var out pb.DescribeAgentInstanceResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func instanceRecordToProto(in agentinstance.Instance) *pb.AgentInstance {
	return &pb.AgentInstance{
		Id:                 in.ID,
		Name:               in.Name,
		AgentType:          in.AgentType,
		OwnerSessionId:     in.OwnerSessionID,
		TargetId:           in.TargetID,
		TargetName:         in.TargetName,
		CredentialProfile:  in.CredentialProfile,
		CredentialProfiles: append([]string(nil), in.CredentialProfiles...),
		AgentProfile:       in.AgentProfile,
		ModelRef:           in.ModelRef,
		PermissionPolicy:   in.PermissionPolicy,
		IsolationMode:      in.IsolationMode,
		HomeRef:            in.HomeRef,
		WorkspaceRef:       in.WorkspaceRef,
		ProviderSessionId:  in.ProviderSessionID,
		Path:               in.Path,
		Status:             in.Status,
		CreatedAt:          in.CreatedAt,
		UpdatedAt:          in.UpdatedAt,
		LastUsedAt:         in.LastUsedAt,
		ExecutionIdentity:  principal.ToProtoIdentity(in.ExecutionIdentity),
	}
}

func resolvedProfileRefForInstance(spec *pb.AgentProfileSpec) string {
	if spec == nil {
		return ""
	}
	if ref := strings.TrimSpace(spec.PinnedRef); ref != "" {
		return ref
	}
	return strings.TrimSpace(spec.Ref)
}

func resolvedModelRefForInstance(spec task.Spec) string {
	if spec.ResolvedProfile != nil {
		if ref := strings.TrimSpace(spec.ResolvedProfile.ModelRef); ref != "" {
			return ref
		}
		if ref := strings.TrimSpace(spec.ResolvedProfile.ConfiguredModel); ref != "" {
			return ref
		}
	}
	return strings.TrimSpace(spec.Model)
}

func applyLiveInstance(inst *pb.AgentInstance, live *pb.DescribeAgentInstanceResponse) {
	if live == nil {
		return
	}
	inst.ChildSessionId = live.ChildSessionId
	inst.ChildSessionName = live.ChildSessionName
	inst.ChildSessionState = live.ChildSessionState
	inst.PendingMessages = live.PendingMessages
	if live.ExecutionIdentity != nil {
		inst.ExecutionIdentity = live.ExecutionIdentity
	}
	// If the durable status is stale, the live child state is more accurate.
	// A durable archived record is authoritative because owner reclamation flips
	// it before best-effort runtime cleanup; a stranded child must not reactivate
	// an instance whose owner lifecycle has ended.
	if inst.Status != agentinstance.StatusArchived && live.ChildSessionState != "" {
		inst.Status = instanceStatusFromLiveState(live.ChildSessionState, inst.Status)
	}
}

func (r *taskRouter) persistLiveInstanceIdentity(id string, live *pb.DescribeAgentInstanceResponse) {
	if r.instances == nil || live == nil || live.ExecutionIdentity == nil {
		return
	}
	identity := principal.FromProtoIdentity(live.ExecutionIdentity)
	if identity.IdentityMode == "" {
		return
	}
	if err := r.instances.FinalizeExecution(id, identity, live.LaunchId); err != nil {
		fmt.Fprintf(os.Stderr, "hub: finalize agent instance identity %s: %v\n", id, err)
	}
}

func instanceStatusFromLiveState(state, durable string) string {
	switch state {
	case "running":
		return "active"
	case "sleeping":
		return "idle"
	case "archived":
		return "archived"
	default:
		return durable
	}
}

// resolveInstanceTarget returns the target id hosting an agent instance:
// durable store first, then the in-memory task index, then a fan-out probe.
func (r *taskRouter) resolveInstanceTarget(ctx context.Context, instanceID string) (string, error) {
	if instanceID == "" {
		return "", status.Error(codes.InvalidArgument, "agent_instance_id is required")
	}
	if t := r.resolveStickyTarget(instanceID); t != "" && r.connected(t) {
		return t, nil
	}
	// Fan-out: ask each connected runtime to describe the instance.
	for _, t := range r.hub.ListTargets() {
		if _, err := r.describeInstanceOnTarget(ctx, t.Id, instanceID); err == nil {
			return t.Id, nil
		}
	}
	return "", status.Errorf(codes.NotFound, "agent instance %q not found on any connected runtime", instanceID)
}

func (r *taskRouter) GetAgentInstance(ctx context.Context, req *pb.GetAgentInstanceRequest) (*pb.GetAgentInstanceResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_instance_id is required")
	}
	if r.instances != nil {
		if in, err := r.instances.Get(req.Id); err == nil {
			// The loader prefers the same durable record, so this authorizes
			// without a redundant live describe.
			if err := r.authorizeInstanceRead(ctx, in.TargetID, req.Id); err != nil {
				return nil, err
			}
			inst := instanceRecordToProto(in)
			if in.TargetID != "" && r.connected(in.TargetID) {
				if live, err := r.describeInstanceOnTarget(ctx, in.TargetID, in.ID); err == nil {
					applyLiveInstance(inst, live)
					r.persistLiveInstanceIdentity(in.ID, live)
					r.persistLiveInstanceStatus(in, inst)
				}
			}
			return &pb.GetAgentInstanceResponse{Instance: inst}, nil
		}
	}
	// Fallback: locate the instance on a runtime and ask it for the local view.
	target, err := r.resolveInstanceTarget(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := r.authorizeInstanceRead(ctx, target, req.Id); err != nil {
		return nil, err
	}
	resp, err := r.hub.Route(ctx, target, pb.TaskMethodInstanceGet, &pb.GetAgentInstanceRequest{Id: req.Id})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	var lr pb.GetAgentInstanceResponse
	if err := json.Unmarshal(resp.Payload, &lr); err != nil {
		return nil, status.Errorf(codes.Internal, "decode: %v", err)
	}
	return &lr, nil
}

func (r *taskRouter) SendAgentMessage(ctx context.Context, req *pb.SendAgentMessageRequest) (*pb.SendAgentMessageResponse, error) {
	target, err := r.resolveInstanceTarget(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := r.authorizeInstanceMutation(ctx, target, req.Id); err != nil {
		return nil, err
	}
	resp, err := r.hub.Route(ctx, target, pb.TaskMethodInstanceSend, req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	var out pb.SendAgentMessageResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, status.Errorf(codes.Internal, "decode: %v", err)
	}
	// Best-effort: refresh the durable instance's last-used time.
	if r.instances != nil {
		_, _ = r.instances.TouchLastUsed(req.Id)
	}
	return &out, nil
}

func (r *taskRouter) InterruptAgentInstance(ctx context.Context, req *pb.InterruptAgentInstanceRequest) (*pb.InterruptAgentInstanceResponse, error) {
	target, err := r.resolveInstanceTarget(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := r.authorizeInstanceMutation(ctx, target, req.Id); err != nil {
		return nil, err
	}
	resp, err := r.hub.Route(ctx, target, pb.TaskMethodInstanceInterrupt, req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	var out pb.InterruptAgentInstanceResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, status.Errorf(codes.Internal, "decode: %v", err)
	}
	return &out, nil
}

func (r *taskRouter) WaitAgentInstance(ctx context.Context, req *pb.WaitAgentInstanceRequest) (*pb.WaitAgentInstanceResponse, error) {
	target, err := r.resolveInstanceTarget(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := r.authorizeInstanceRead(ctx, target, req.Id); err != nil {
		return nil, err
	}
	resp, err := r.hub.Route(ctx, target, pb.TaskMethodInstanceWait, req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	var out pb.WaitAgentInstanceResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, status.Errorf(codes.Internal, "decode: %v", err)
	}
	return &out, nil
}

func (r *taskRouter) DescribeAgentInstance(ctx context.Context, req *pb.DescribeAgentInstanceRequest) (*pb.DescribeAgentInstanceResponse, error) {
	target, err := r.resolveInstanceTarget(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := r.authorizeInstanceRead(ctx, target, req.Id); err != nil {
		return nil, err
	}
	resp, err := r.hub.Route(ctx, target, pb.TaskMethodInstanceDescribe, req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	var out pb.DescribeAgentInstanceResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, status.Errorf(codes.Internal, "decode: %v", err)
	}
	return &out, nil
}

// resolve returns the runtime hosting taskID: index-first, falling back to a
// fan-out probe (TaskGet on each connected runtime) that caches the hit. The
// fan-out path lets Get/Cancel/Respond/Stream keep working after an amuxd restart
// dropped the in-memory index.
func (r *taskRouter) resolve(ctx context.Context, taskID string) (string, error) {
	if p, ok := r.index.get(taskID); ok && r.connected(p.TargetID) {
		return p.TargetID, nil
	}
	for _, t := range r.hub.ListTargets() {
		if _, err := r.hub.Route(ctx, t.Id, pb.TaskMethodGet, &pb.GetSubtaskRequest{Id: taskID}); err == nil {
			r.index.put(taskID, taskPlacement{TargetID: t.Id})
			return t.Id, nil
		}
	}
	return "", status.Errorf(codes.NotFound, "task %q not found on any connected runtime", taskID)
}

func (r *taskRouter) connected(targetID string) bool {
	for _, t := range r.hub.ListTargets() {
		if t.Id == targetID {
			return true
		}
	}
	return false
}

// loadTaskAuthorizationIdentity fetches the identity a task was frozen with,
// plus the parent session it was submitted under, for an authorization check.
func (r *taskRouter) loadTaskAuthorizationIdentity(ctx context.Context, targetID, taskID string) (principal.ExecutionIdentity, string, error) {
	resp, err := r.hub.Route(ctx, targetID, pb.TaskMethodGet, &pb.GetSubtaskRequest{Id: taskID})
	if err != nil {
		return principal.ExecutionIdentity{}, "", status.Errorf(codes.Internal, "load task authorization identity: %v", err)
	}
	var get pb.GetSubtaskResponse
	if err := json.Unmarshal(resp.Payload, &get); err != nil {
		return principal.ExecutionIdentity{}, "", status.Errorf(codes.Internal, "decode task authorization identity: %v", err)
	}
	var saved struct {
		ExecutionIdentity principal.ExecutionIdentity `json:"execution_identity"`
		ParentSessionID   string                      `json:"parent_session_id"`
	}
	if err := json.Unmarshal(get.TaskJson, &saved); err != nil {
		return principal.ExecutionIdentity{}, "", status.Errorf(codes.Internal, "decode task identity: %v", err)
	}
	return saved.ExecutionIdentity, saved.ParentSessionID, nil
}

// authorizeTaskRead guards reading one task.
func (r *taskRouter) authorizeTaskRead(ctx context.Context, targetID, taskID string) error {
	if r.principals == nil {
		return nil
	}
	identity, parentSessionID, err := r.loadTaskAuthorizationIdentity(ctx, targetID, taskID)
	if err != nil {
		return err
	}
	return r.authorizeExecutionRead(ctx, identity, parentSessionID, "task")
}

func (r *taskRouter) authorizeTaskMutation(ctx context.Context, targetID, taskID string) error {
	if r.principals == nil {
		return nil
	}
	identity, parentSessionID, err := r.loadTaskAuthorizationIdentity(ctx, targetID, taskID)
	if err != nil {
		return err
	}
	return r.authorizeExecutionMutation(ctx, identity, parentSessionID, "task")
}

// loadInstanceAuthorizationIdentity resolves the identity an instance was
// frozen with, preferring the durable record and falling back to a live
// describe on its target only when no durable record exists. A present record
// is the source of truth even when its identity is empty: the store scan
// defaults a present-but-empty identity to legacy-host, and FinalizeExecution
// refuses to persist a live identity that diverges from frozen, so such a
// record is genuinely legacy-host and authorizes on the frozen identity
// without a network call — same as the list path. The found gate encodes that
// invariant explicitly rather than relying on the scan default.
func (r *taskRouter) loadInstanceAuthorizationIdentity(ctx context.Context, targetID, instanceID string) (principal.ExecutionIdentity, string, error) {
	var identity principal.ExecutionIdentity
	var ownerSessionID string
	found := false
	if r.instances != nil {
		if instance, err := r.instances.Get(instanceID); err == nil {
			identity = instance.ExecutionIdentity
			ownerSessionID = instance.OwnerSessionID
			found = true
		}
	}
	if !found {
		// No durable record: the live describe is the only identity source.
		live, err := r.describeInstanceOnTarget(ctx, targetID, instanceID)
		if err != nil {
			return principal.ExecutionIdentity{}, "", status.Errorf(codes.Internal, "load agent instance authorization identity: %v", err)
		}
		identity = principal.FromProtoIdentity(live.ExecutionIdentity)
	}
	return identity, ownerSessionID, nil
}

// authorizeInstanceRead guards reading one agent instance.
func (r *taskRouter) authorizeInstanceRead(ctx context.Context, targetID, instanceID string) error {
	if r.principals == nil {
		return nil
	}
	identity, ownerSessionID, err := r.loadInstanceAuthorizationIdentity(ctx, targetID, instanceID)
	if err != nil {
		return err
	}
	return r.authorizeExecutionRead(ctx, identity, ownerSessionID, "agent instance")
}

// authorizePrune keeps fleet-wide prune enrollment-only while principal
// isolation is active because the delete cannot be attributed to one
// principal. Without isolation every task already runs under the same host
// user, so a valid delegated actor has the same maintenance reach as an
// enrollment actor.
func (r *taskRouter) authorizePrune(ctx context.Context) error {
	if r.principals == nil {
		return nil
	}
	actor, err := authenticatedActor(ctx, r.principals)
	if err != nil {
		return err
	}
	decision := executionauth.New(r.principals).PruneAccess(actor, r.fleetIsolationActive())
	if !decision.Allowed {
		if decision.Reason == executionauth.ReasonEnrollmentRequired {
			return status.Error(codes.PermissionDenied, "task prune is restricted to enrollment actors; delegated agent credentials cannot prune — ask the enrolling operator to run it")
		}
		return status.Error(codes.Unauthenticated, "task prune requires enrollment")
	}
	return nil
}

func (r *taskRouter) authorizeInstanceMutation(ctx context.Context, targetID, instanceID string) error {
	if r.principals == nil {
		return nil
	}
	identity, ownerSessionID, err := r.loadInstanceAuthorizationIdentity(ctx, targetID, instanceID)
	if err != nil {
		return err
	}
	return r.authorizeExecutionMutation(ctx, identity, ownerSessionID, "agent instance")
}

// authorizeExecutionRead guards a single-object read. Reads and mutations share
// one decision rule — a caller may not read what it may not mutate — so this
// delegates to the same core as authorizeExecutionMutation. The named entry
// points exist so each call site declares whether it guards a read or a
// mutation.
func (r *taskRouter) authorizeExecutionRead(
	ctx context.Context,
	identity principal.ExecutionIdentity,
	parentSessionID,
	resource string,
) error {
	return r.authorizeExecution(ctx, identity, parentSessionID, resource)
}

// authorizeExecutionMutation guards a single-object mutation.
func (r *taskRouter) authorizeExecutionMutation(
	ctx context.Context,
	identity principal.ExecutionIdentity,
	parentSessionID,
	resource string,
) error {
	return r.authorizeExecution(ctx, identity, parentSessionID, resource)
}

// fleetIsolationActive reports whether the fleet-level identity boundary
// (task/instance ownership authorization and list filtering) applies. It
// mirrors hubServer.fleetIsolationActive, computed from the targets the tunnel
// hub reports: the boundary exists to mirror the per-principal sandbox an
// isolating runtime enforces, so on a fleet with no isolating runtime it has
// nothing to protect and is skipped entirely.
func (r *taskRouter) fleetIsolationActive() bool {
	if r.hub == nil {
		return false
	}
	return targetsIncludeIsolatingRuntime(r.hub.ListTargets())
}

// authorizeExecution is the single decision core for task/instance
// authorization: legacy-host and a missing principal store pass through
// unchanged, an identified object requires an authenticated actor the object
// authorizes (origin/effective, else run_as delegation / service membership).
// The identified-object rule applies only while fleet isolation is active; on
// a fleet with no isolating runtime every identified object is reachable by
// every caller, the pre-rule-six behavior. Explicit invalid credentials are
// still rejected in either mode.
func (r *taskRouter) authorizeExecution(
	ctx context.Context,
	identity principal.ExecutionIdentity,
	parentSessionID,
	resource string,
) error {
	if identity.IdentityMode == "" || identity.IdentityMode == principal.IdentityModeLegacyHost {
		return nil
	}
	if r.principals == nil {
		return nil
	}
	actor, err := authenticatedActor(ctx, r.principals)
	if err != nil {
		return err
	}
	decision := executionauth.New(r.principals).ExecutionAccess(executionauth.ExecutionAccessRequest{
		Actor:           actor,
		Identity:        identity,
		ParentSessionID: parentSessionID,
		Sessions:        r.executionSessionFacts(),
		IsolationActive: r.fleetIsolationActive(),
	})
	if decision.Allowed {
		return nil
	}
	if decision.Kind == executionauth.DenialUnauthenticated {
		return status.Errorf(codes.Unauthenticated, "identified %s access requires enrollment or its parent execution", resource)
	}
	return status.Errorf(codes.PermissionDenied, "%s authorization: %v", resource, decision.Err)
}

func (r *taskRouter) executionSessionFacts() []executionauth.SessionFacts {
	if r.hub == nil {
		return nil
	}
	return sessionFactsFromTargets(r.hub.ListTargets())
}

// visibleExecution is the list counterpart of authorizeExecution: same rule,
// reported as keep/drop instead of an error. legacy-host is always visible,
// and on a fleet without an isolating runtime every row is.
func (r *taskRouter) visibleExecution(ctx context.Context, actor *principal.ActorContext, identity principal.ExecutionIdentity, parentSessionID string) bool {
	if identity.IdentityMode == "" || identity.IdentityMode == principal.IdentityModeLegacyHost {
		return true
	}
	return executionauth.New(r.principals).ExecutionAccess(executionauth.ExecutionAccessRequest{
		Actor:           actor,
		Identity:        identity,
		ParentSessionID: parentSessionID,
		Sessions:        r.executionSessionFacts(),
		IsolationActive: r.fleetIsolationActive(),
	}).Allowed
}

// filterVisibleTasks drops rows outside the caller's ownership only while fleet
// isolation is active. Legacy-host rows and all rows on a non-isolated fleet are
// visible to everyone. A credential that fails authentication is an error, not
// an empty list.
func (r *taskRouter) filterVisibleTasks(ctx context.Context, raws [][]byte) ([][]byte, error) {
	if r.principals == nil {
		return raws, nil
	}
	actor, err := authenticatedActor(ctx, r.principals)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(raws))
	for _, raw := range raws {
		var saved struct {
			ExecutionIdentity principal.ExecutionIdentity `json:"execution_identity"`
			ParentSessionID   string                      `json:"parent_session_id"`
		}
		if err := json.Unmarshal(raw, &saved); err != nil {
			continue
		}
		if r.visibleExecution(ctx, actor, saved.ExecutionIdentity, saved.ParentSessionID) {
			out = append(out, raw)
		}
	}
	return out, nil
}

// filterVisibleInstances drops the fan-out rows a caller may not see. It is
// used only for rows with no durable record (the no-store fallback and
// runtime-discovered instances), where the fan-out's reported identity is all
// there is to authorize against. Durable rows are instead authorized on their
// frozen store identity inside ListAgentInstances before live enrichment,
// because FinalizeExecution refuses to persist a live identity that diverges
// from frozen — the frozen value is the source of truth. A credential that
// fails authentication is an error, not an empty list.
func (r *taskRouter) filterVisibleInstances(ctx context.Context, insts []*pb.AgentInstance) ([]*pb.AgentInstance, error) {
	if r.principals == nil {
		return insts, nil
	}
	actor, err := authenticatedActor(ctx, r.principals)
	if err != nil {
		return nil, err
	}
	out := make([]*pb.AgentInstance, 0, len(insts))
	for _, inst := range insts {
		if inst == nil {
			continue
		}
		if r.visibleExecution(ctx, actor, principal.FromProtoIdentity(inst.ExecutionIdentity), inst.OwnerSessionId) {
			out = append(out, inst)
		}
	}
	return out, nil
}

func (r *taskRouter) scheduleRequestFromSpec(ctx context.Context, spec task.Spec) (registry.ScheduleRequest, error) {
	credential, err := r.resolveCredentialPlanProfiles(ctx, spec)
	if err != nil {
		return registry.ScheduleRequest{}, err
	}
	req := registry.ScheduleRequest{
		AgentType:          spec.Agent,
		Isolation:          string(task.DefaultIsolation(spec.Isolation)),
		WorkspacePolicy:    string(task.DefaultWorkspace(spec.Workspace.Policy)),
		CredentialProfile:  credential.LegacyProfile,
		CredentialProfiles: credential.Profiles,
		AgentInstanceID:    spec.AgentInstanceID,
	}
	if spec.Workspace.Repo != nil {
		req.RepoCacheKey = spec.Workspace.Repo.CacheKey
	}
	if spec.ExecutionIdentity != nil && spec.ExecutionIdentity.Identified() && r.principals != nil {
		effective, err := r.principals.Get(spec.ExecutionIdentity.EffectivePrincipalID)
		if err != nil {
			return registry.ScheduleRequest{}, fmt.Errorf("resolve execution principal placement: %w", err)
		}
		req.RequiredTargetID = effective.EnvironmentTargetID
	}
	return req, nil
}

func (r *taskRouter) resolveCredentialPlanProfiles(ctx context.Context, spec task.Spec) (task.CredentialPlan, error) {
	credential, err := task.ResolveCredentialPlan(spec)
	if err != nil {
		return task.CredentialPlan{}, err
	}
	if spec.ExecutionIdentity != nil && spec.ExecutionIdentity.Identified() {
		credential = task.FilterCredentialPlanForEnv(credential, r.isolationGlobalEnv)
	}
	if !credentialPlanNeedsEnvLookup(credential) {
		return credential, nil
	}
	if r.credentialProvider == nil {
		return task.CredentialPlan{}, fmt.Errorf("credential env lookup requires credential provider")
	}
	provider, err := r.credentialProvider()
	if err != nil {
		return task.CredentialPlan{}, err
	}
	mat, err := task.MaterializeCredentialPlan(ctx, provider, credential, spec.Agent)
	if err != nil {
		return task.CredentialPlan{}, err
	}
	credential.Profiles = credentialProfilesFromMaterial(mat.Profiles)
	credential.LegacyProfile = legacyCredentialProfileFromNames(credential.Profiles)
	return credential, nil
}

func credentialPlanNeedsEnvLookup(plan task.CredentialPlan) bool {
	for _, req := range plan.Requests {
		if req.Profile == "" && len(req.EnvNames) > 0 {
			return true
		}
	}
	return false
}

func credentialProfilesFromMaterial(profiles []string) []string {
	out := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		profile = strings.TrimSpace(profile)
		if profile == "" || containsString(out, profile) {
			continue
		}
		out = append(out, profile)
	}
	return out
}

func legacyCredentialProfileFromNames(profiles []string) string {
	if len(profiles) == 1 {
		return profiles[0]
	}
	return ""
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

// taskIDFromJSON extracts just the task id from an opaque TaskRun JSON payload, so
// the router need not depend on the full task schema for indexing.
func taskIDFromJSON(b []byte) string {
	var t struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(b, &t)
	return t.ID
}

// taskRefFromJSON extracts the task id and parent session id from an opaque
// TaskRun JSON payload.
func taskRefFromJSON(b []byte) (id, parentSessionID string) {
	var t struct {
		ID              string `json:"id"`
		ParentSessionID string `json:"parent_session_id"`
	}
	_ = json.Unmarshal(b, &t)
	return t.ID, t.ParentSessionID
}

func effectiveTaskPermissionPolicy(spec task.Spec) string {
	if spec.PermissionPolicy != "" {
		return string(spec.PermissionPolicy)
	}
	if spec.ResolvedProfile != nil && spec.ResolvedProfile.PermissionPolicy != "" {
		return spec.ResolvedProfile.PermissionPolicy
	}
	return string(task.PolicyAllow)
}
