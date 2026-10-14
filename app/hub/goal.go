package hub

import (
	"context"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func (s *hubServer) Goal(ctx context.Context, request *pb.AmuxdGoalRequest) (*pb.GoalResponse, error) {
	if request.GetAction() == "get" {
		if err := s.authorizeSessionRead(ctx, request.TargetId, request.Name); err != nil {
			return nil, err
		}
	} else if err := s.authorizeSessionMutation(ctx, request.TargetId, request.Name); err != nil {
		return nil, err
	}
	return routeUnary[pb.GoalRequest, pb.GoalResponse](s, ctx, request.TargetId, "Goal", &pb.GoalRequest{
		Name: request.Name, Action: request.Action, Objective: request.Objective, TokenBudget: request.TokenBudget, ExpectedSessionId: request.ExpectedSessionId,
	})
}
