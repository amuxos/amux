package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) Goal(ctx context.Context, request *pb.GoalRequest) (*pb.GoalResponse, error) {
	sess, err := s.mgr.Get(request.GetName())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if request.ExpectedSessionId != "" && request.ExpectedSessionId != sess.Snapshot().ID {
		return nil, status.Error(codes.FailedPrecondition, "goal_stale")
	}
	if request.GetAction() != "get" {
		if err := ownerGuard(sess); err != nil {
			return nil, err
		}
	}
	if err := s.ensureSessionActive(sess); err != nil {
		return nil, err
	}
	if !sess.GoalSupported() {
		return &pb.GoalResponse{Supported: false}, nil
	}
	goal, err := sess.Goal(ctx, session.GoalRequest{Action: request.Action, Objective: request.Objective, TokenBudget: request.TokenBudget})
	if errors.Is(err, session.ErrGoalUnsupported) {
		return &pb.GoalResponse{Supported: false}, nil
	}
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	var encoded []byte
	if goal != nil {
		encoded, err = json.Marshal(goal)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return &pb.GoalResponse{Supported: true, GoalJson: encoded}, nil
}
