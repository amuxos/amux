package runtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// taskServer implements pb.AmuxTaskServer, the Phase 1 distributed-task control
// plane, by delegating to an in-process task.Runner. Task/Spec/Event records
// travel as opaque JSON so the transport stub never depends on internal/task.
//
// Agent-instance (Phase 3) RPCs delegate to the taskController, which drives
// the instance's child session in-process (bypassing the session owner-guard).
type taskServer struct {
	pb.UnimplementedAmuxTaskServer
	runner *task.Runner
	ctrl   *taskController
	// resolveProfile rewrites a profile-ref spec to its base agent for the local
	// (no-hub) path. When a hub resolved upstream, ResolvedProfile is set and this
	// is a no-op. nil => no resolution (tests / raw base agents).
	resolveProfile func(spec *task.Spec) error
	// resolveParentTurn is wired to the local session manager, but exposed only
	// through the task control plane so amuxd never drives child-session RPCs.
	resolveParentTurn func(context.Context, *pb.ActiveTurnRequest) (*pb.ActiveTurnResponse, error)
}

func newTaskServer(runner *task.Runner, ctrl *taskController) *taskServer {
	if ctrl != nil && runner != nil {
		ctrl.cancelTask = runner.Cancel
	}
	return &taskServer{runner: runner, ctrl: ctrl}
}

func (s *taskServer) ResolveParentTurn(ctx context.Context, req *pb.ActiveTurnRequest) (*pb.ActiveTurnResponse, error) {
	if s.resolveParentTurn == nil {
		return nil, status.Error(codes.Unimplemented, "parent turn resolution not available on this runtime")
	}
	return s.resolveParentTurn(ctx, req)
}

func (s *taskServer) SubmitSubtask(_ context.Context, req *pb.SubmitSubtaskRequest) (*pb.SubmitSubtaskResponse, error) {
	var spec task.Spec
	if err := json.Unmarshal(req.SpecJson, &spec); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode spec: %v", err)
	}
	if s.resolveProfile != nil {
		if err := s.resolveProfile(&spec); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "resolve agent profile: %v", err)
		}
	}
	// The launch id and actor credential travel outside the spec: the spec is
	// persisted as spec_json, and the credential must never be persisted.
	run, err := s.runner.Submit(spec, req.LaunchId, req.AgentActorToken, req.AgentActorTokenExpiresAt)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return &pb.SubmitSubtaskResponse{TaskJson: mustJSON(run)}, nil
}

func (s *taskServer) GetSubtask(_ context.Context, req *pb.GetSubtaskRequest) (*pb.GetSubtaskResponse, error) {
	run, err := s.runner.Get(req.Id)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &pb.GetSubtaskResponse{TaskJson: mustJSON(run)}, nil
}

func (s *taskServer) ListSubtasks(_ context.Context, _ *pb.ListSubtasksRequest) (*pb.ListSubtasksResponse, error) {
	runs, err := s.runner.List()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	out := &pb.ListSubtasksResponse{TasksJson: make([][]byte, 0, len(runs))}
	for _, r := range runs {
		out.TasksJson = append(out.TasksJson, mustJSON(r))
	}
	return out, nil
}

func (s *taskServer) PruneSubtasks(ctx context.Context, req *pb.PruneSubtasksRequest) (*pb.PruneSubtasksResponse, error) {
	statuses := make([]task.Status, 0, len(req.Statuses))
	for _, st := range req.Statuses {
		statuses = append(statuses, task.Status(st))
	}
	result, err := s.runner.Prune(ctx, task.PruneRequest{
		DryRun:    req.DryRun,
		OlderThan: time.Duration(req.OlderThanSeconds) * time.Second,
		Statuses:  statuses,
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return pruneResultToProto(result), nil
}

func (s *taskServer) CancelSubtask(_ context.Context, req *pb.CancelSubtaskRequest) (*pb.CancelSubtaskResponse, error) {
	if err := s.runner.Cancel(req.Id); err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return &pb.CancelSubtaskResponse{Ok: true}, nil
}

func (s *taskServer) RespondSubtaskPermission(_ context.Context, req *pb.RespondSubtaskPermissionRequest) (*pb.RespondSubtaskPermissionResponse, error) {
	err := s.runner.RespondPermission(req.Id, task.PermissionResponse{
		PermissionId: req.PermissionId,
		Allow:        req.Allow,
		OptionId:     req.OptionId,
		Reason:       req.Reason,
	})
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
	}
	return &pb.RespondSubtaskPermissionResponse{Ok: true}, nil
}

func (s *taskServer) StreamSubtask(req *pb.StreamSubtaskRequest, stream pb.AmuxTask_StreamSubtaskServer) error {
	ch, unsub, err := s.runner.Subscribe(req.Id, req.FromSeq)
	if err != nil {
		return status.Errorf(codes.NotFound, "%v", err)
	}
	defer unsub()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(&pb.SubtaskEvent{EventJson: mustJSON(ev)}); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// ListAgentInstances returns the live agent-instance sessions on this runtime
// (the local/no-hub path). On the hub path amuxd answers from its durable
// store instead and only fans out to enrich child-session state; this handler
// is the local fallback and the per-target describe source.
func (s *taskServer) ListAgentInstances(_ context.Context, req *pb.ListAgentInstancesRequest) (*pb.ListAgentInstancesResponse, error) {
	if s.ctrl == nil {
		return &pb.ListAgentInstancesResponse{}, nil
	}
	summaries := s.ctrl.ListInstanceSummaries()
	out := &pb.ListAgentInstancesResponse{Instances: make([]*pb.AgentInstance, 0, len(summaries))}
	for _, sm := range summaries {
		if req.OwnerSessionId != "" && sm.InstanceID != req.OwnerSessionId {
			// OwnerSessionId filter is resolved at the durable store on amuxd; the
			// local path has no owner mapping, so only filter by status below.
		}
		inst := instanceSummaryToProto(sm, s.ctrl)
		if req.Status != "" && inst.Status != req.Status {
			continue
		}
		out.Instances = append(out.Instances, inst)
	}
	return out, nil
}

func instanceSummaryToProto(sm instanceSummary, ctrl *taskController) *pb.AgentInstance {
	inst := &pb.AgentInstance{
		Id:                sm.InstanceID,
		Status:            instanceStatusFromSessionState(sm.State),
		PermissionPolicy:  sm.PermissionPolicy,
		ChildSessionId:    sm.ChildSessionID,
		ChildSessionName:  sm.ChildSessionName,
		ChildSessionState: sm.State,
		ExecutionIdentity: principal.ToProtoIdentity(sm.ExecutionIdentity),
	}
	if ctrl != nil {
		inst.PendingMessages = int32(ctrl.mailbox.pending(sm.InstanceID))
	}
	return inst
}

// instanceStatusFromSessionState maps a live child-session state to the
// instance lifecycle status used by instance_list. A running child is active; a
// sleeping child is idle; anything else (archived/missing) is archived.
func instanceStatusFromSessionState(state string) string {
	switch state {
	case "running":
		return "active"
	case "sleeping":
		return "idle"
	default:
		return "archived"
	}
}

func (s *taskServer) GetAgentInstance(_ context.Context, req *pb.GetAgentInstanceRequest) (*pb.GetAgentInstanceResponse, error) {
	if s.ctrl == nil {
		return nil, status.Error(codes.Unimplemented, "agent instances not available on this runtime")
	}
	sm, ok := instanceSummaryByID(s.ctrl, req.Id)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "agent instance %q not found", req.Id)
	}
	return &pb.GetAgentInstanceResponse{Instance: instanceSummaryToProto(sm, s.ctrl)}, nil
}

func instanceSummaryByID(ctrl *taskController, id string) (instanceSummary, bool) {
	for _, sm := range ctrl.ListInstanceSummaries() {
		if sm.InstanceID == id {
			return sm, true
		}
	}
	return instanceSummary{}, false
}

func (s *taskServer) SendAgentMessage(ctx context.Context, req *pb.SendAgentMessageRequest) (*pb.SendAgentMessageResponse, error) {
	if s.ctrl == nil {
		return nil, status.Error(codes.Unimplemented, "agent instances not available on this runtime")
	}
	if req.GetSteer() {
		delivery, reason, pending, err := s.ctrl.SteerInstance(ctx, req.Id, req.Source, req.Text)
		if err != nil {
			return nil, err
		}
		return &pb.SendAgentMessageResponse{Ok: true, PendingMessages: int32(pending), Delivery: delivery, SteerFallbackReason: reason}, nil
	}
	pending, err := s.ctrl.SendInstanceMessage(ctx, req.Id, req.Source, req.Text)
	if err != nil {
		return nil, err
	}
	return &pb.SendAgentMessageResponse{Ok: true, PendingMessages: int32(pending), Delivery: "mailbox"}, nil
}

func (s *taskServer) InterruptAgentInstance(ctx context.Context, req *pb.InterruptAgentInstanceRequest) (*pb.InterruptAgentInstanceResponse, error) {
	if s.ctrl == nil {
		return nil, status.Error(codes.Unimplemented, "agent instances not available on this runtime")
	}
	if err := s.ctrl.InterruptInstance(ctx, req.Id); err != nil {
		return nil, err
	}
	return &pb.InterruptAgentInstanceResponse{Ok: true}, nil
}

func (s *taskServer) WaitAgentInstance(ctx context.Context, req *pb.WaitAgentInstanceRequest) (*pb.WaitAgentInstanceResponse, error) {
	if s.ctrl == nil {
		return nil, status.Error(codes.Unimplemented, "agent instances not available on this runtime")
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	activity, state, err := s.ctrl.WaitInstance(ctx, req.Id, timeout)
	if err != nil {
		return nil, err
	}
	return &pb.WaitAgentInstanceResponse{Activity: activity, State: state}, nil
}

func (s *taskServer) DescribeAgentInstance(ctx context.Context, req *pb.DescribeAgentInstanceRequest) (*pb.DescribeAgentInstanceResponse, error) {
	if s.ctrl == nil {
		return nil, status.Error(codes.Unimplemented, "agent instances not available on this runtime")
	}
	return s.ctrl.DescribeInstance(ctx, req.Id)
}

// CleanupAgentInstance destroys the durable environment of a persistent agent
// instance after amuxd has archived it (e.g. its owner session ended). The
// hosting amuxlet stops any live broker, removes the instance-keyed sandbox,
// and clears the mailbox. It is routed from amuxd via TaskMethodInstanceCleanup.
func (s *taskServer) CleanupAgentInstance(ctx context.Context, req *pb.CleanupAgentInstanceRequest) (*pb.CleanupAgentInstanceResponse, error) {
	if s.ctrl == nil {
		return nil, status.Error(codes.Unimplemented, "agent instances not available on this runtime")
	}
	if err := s.ctrl.CleanupInstance(ctx, req.Id); err != nil {
		return nil, err
	}
	return &pb.CleanupAgentInstanceResponse{Ok: true}, nil
}

func pruneResultToProto(result task.PruneResult) *pb.PruneSubtasksResponse {
	out := &pb.PruneSubtasksResponse{DeletedCount: int32(len(result.Tasks))}
	for _, t := range result.Tasks {
		out.Tasks = append(out.Tasks, &pb.PrunedSubtask{
			Id:                  t.ID,
			Status:              string(t.Status),
			TargetId:            t.TargetID,
			ChildSessionId:      t.ChildSessionId,
			ChildSessionName:    t.ChildSessionName,
			DeletedChildSession: t.DeletedChildSession,
			DeletedSandbox:      t.DeletedSandbox,
		})
	}
	for _, s := range result.Skipped {
		out.Skipped = append(out.Skipped, &pb.SkippedSubtask{Id: s.ID, TargetId: s.TargetID, Reason: s.Reason})
	}
	return out
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
