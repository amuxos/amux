package hub

import (
	"context"
	"strings"

	"github.com/amuxos/amux/internal/executionauth"
	"github.com/amuxos/amux/internal/feishumsg"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type feishuMessageSender interface {
	ProfileName() string
	HasAgentMessageBinding(targetID, sessionName string) (bool, error)
	SendAgentMessage(context.Context, string, string, feishumsg.Message) (feishumsg.Result, error)
}

func (s *hubServer) SendFeishuMessage(ctx context.Context, req *pb.SendFeishuMessageRequest) (*pb.SendFeishuMessageResponse, error) {
	msg := feishumsg.Message{ReceiveID: req.GetReceiveId(), ReceiveIDType: req.GetReceiveIdType(), ReplyToMessageID: req.GetReplyToMessageId(), ReplyInThread: req.GetReplyInThread(), Text: req.GetText(), Format: req.GetFormat(), UUID: req.GetUuid()}
	if err := msg.Validate(); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if msg.UUID == "" {
		msg.UUID = uuid.NewString()
	}
	targets := s.currentTargets()
	targetID, session := sessionByID(targets, strings.TrimSpace(req.GetSessionId()))
	if session == nil || strings.TrimSpace(req.GetSessionId()) == "" {
		return nil, status.Error(codes.PermissionDenied, feishumsg.Copy("session"))
	}
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return nil, err
	}
	decision := executionauth.New(nil).CallerSessionAccess(actor, executionauth.SessionFacts{ID: session.Id, TargetID: targetID, Name: session.Name, LaunchID: session.LaunchId}, sessionFactsFromTargets(targets))
	if !decision.Allowed {
		return nil, status.Error(codes.PermissionDenied, feishumsg.Copy("session"))
	}
	var sender feishuMessageSender
	for _, candidate := range s.feishuMessageSenders {
		bound, err := candidate.HasAgentMessageBinding(targetID, session.Name)
		if err != nil {
			return nil, status.Error(codes.Unavailable, feishumsg.Copy("lookup", err))
		}
		if !bound {
			continue
		}
		if sender != nil {
			return nil, status.Error(codes.FailedPrecondition, feishumsg.Copy("ambiguous"))
		}
		sender = candidate
	}
	if sender == nil {
		return nil, status.Error(codes.FailedPrecondition, feishumsg.Copy("unbound"))
	}
	result, err := sender.SendAgentMessage(ctx, targetID, session.Name, msg)
	if err != nil {
		return nil, status.Error(codes.Unavailable, feishumsg.Copy("request_failed", msg.UUID, err))
	}
	return &pb.SendFeishuMessageResponse{MessageId: result.MessageID, ChatId: result.ChatID, ThreadId: result.ThreadID, BotProfile: sender.ProfileName(), Uuid: msg.UUID}, nil
}
