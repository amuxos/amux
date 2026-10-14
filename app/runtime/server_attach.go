package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *server) Attach(req *pb.AttachRequest, stream pb.Amuxlet_AttachServer) error {
	if req == nil || req.TimelineMode == pb.AttachRequest_TIMELINE_MODE_UNSPECIFIED {
		return status.Error(codes.InvalidArgument, "timeline mode is required")
	}
	sess, err := s.mgr.Get(req.Name)
	if err != nil {
		return status.Errorf(codes.NotFound, "%v", err)
	}
	return attachTimeline(sess, req, stream)
}

func attachTimeline(sess *session.Session, req *pb.AttachRequest, stream pb.Amuxlet_AttachServer) error {
	mode, err := timelineReadMode(req.TimelineMode)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	after, err := agentstream.DecodeCursor(req.AfterCursor)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	head, subscription, err := sess.AgentTimeline().Read(stream.Context(), agentstream.ReadRequest{Mode: mode, After: after})
	if err != nil {
		if errors.Is(err, agentstream.ErrInvalidCursor) {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	defer subscription.Close()
	headCursor, err := agentstream.EncodeCursor(head.Cursor)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	snap := sess.Snapshot()
	sendCommands := func() error {
		if mode != agentstream.ReadTail || head.Commands == nil {
			return nil
		}
		commands, marshalErr := json.Marshal(head.Commands.Commands)
		if marshalErr != nil {
			return status.Error(codes.Internal, marshalErr.Error())
		}
		return stream.Send(&pb.AttachEvent{
			Kind:           pb.AttachEvent_STRUCTURED,
			EventType:      "available_commands",
			StructuredJson: commands,
			EffectiveModel: snap.EffectiveModel,
		})
	}
	sendCaughtUp := func() error {
		var activeTurnStart []byte
		if mode == agentstream.ReadTail && head.ActiveTurnStart != nil {
			activeTurnStart, err = agentstream.MarshalEvent(*head.ActiveTurnStart)
			if err != nil {
				return status.Error(codes.Internal, err.Error())
			}
		}
		if head.Goal != nil {
			encoded, err := json.Marshal(head.Goal)
			if err != nil {
				return err
			}
			if err := stream.Send(&pb.AttachEvent{Kind: pb.AttachEvent_STRUCTURED, EventType: "goal_changed", StructuredJson: encoded, EffectiveModel: snap.EffectiveModel}); err != nil {
				return err
			}
		}
		return stream.Send(&pb.AttachEvent{
			Kind:                pb.AttachEvent_HEARTBEAT,
			Cursor:              headCursor,
			CaughtUp:            true,
			EffectiveModel:      snap.EffectiveModel,
			ActiveTurnStartJson: activeTurnStart,
		})
	}
	caughtUp := mode == agentstream.ReadTail || after.Seq >= head.Cursor.Seq
	if caughtUp {
		if err := sendCaughtUp(); err != nil {
			return err
		}
		if err := sendCommands(); err != nil {
			return err
		}
	}
	for {
		event, err := subscription.Recv(stream.Context())
		if errors.Is(err, io.EOF) {
			if !caughtUp {
				return sendCaughtUp()
			}
			return nil
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return status.Error(codes.FailedPrecondition, err.Error())
		}
		historical := mode != agentstream.ReadTail && event.Cursor.Seq <= head.Cursor.Seq
		out, err := toProtoTimelineEvent(event, historical)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		if err := stream.Send(out); err != nil {
			return err
		}
		if !caughtUp && event.Cursor.Seq >= head.Cursor.Seq {
			if err := sendCaughtUp(); err != nil {
				return err
			}
			caughtUp = true
		}
	}
}

func timelineReadMode(mode pb.AttachRequest_TimelineMode) (agentstream.ReadMode, error) {
	switch mode {
	case pb.AttachRequest_HISTORY:
		return agentstream.ReadHistory, nil
	case pb.AttachRequest_FOLLOW:
		return agentstream.ReadFollow, nil
	case pb.AttachRequest_TAIL:
		return agentstream.ReadTail, nil
	default:
		return "", agentstream.ErrInvalidPayload
	}
}

func toProtoTimelineEvent(event agentstream.Event, historical bool) (*pb.AttachEvent, error) {
	raw, err := agentstream.MarshalEvent(event)
	if err != nil {
		return nil, err
	}
	cursor, err := agentstream.EncodeCursor(event.Cursor)
	if err != nil {
		return nil, err
	}
	out := &pb.AttachEvent{
		Kind:          pb.AttachEvent_STRUCTURED,
		EventType:     string(event.Body.Kind()),
		Cursor:        cursor,
		Historical:    historical,
		CanonicalJson: raw,
		Source:        event.Origin.Channel,
		Provider:      event.Origin.Provider,
	}
	if event.Turn != nil {
		out.TurnId = event.Turn.ID
		out.TurnSeq = int64(event.Turn.Seq)
	}
	bodyJSON, _ := json.Marshal(event.Body)
	out.StructuredJson = bodyJSON
	switch body := event.Body.(type) {
	case agentstream.TurnStarted:
		out.Kind = pb.AttachEvent_OUTPUT
		out.Data = []byte(body.InputSummary)
		out.Text = body.InputSummary
		out.Input = true
		if out.Source == "" {
			out.Source = "user"
		}
	case agentstream.AssistantDelta:
		out.Kind = pb.AttachEvent_OUTPUT
		out.Data = []byte(body.Text)
		out.Text = body.Text
		out.Source = "agent"
	case agentstream.AssistantMessage:
		out.Kind = pb.AttachEvent_OUTPUT
		out.Data = []byte(body.Text)
		out.Text = body.Text
		out.Source = "agent"
	case agentstream.ThinkingDelta:
		out.Kind = pb.AttachEvent_OUTPUT
		out.Text = body.Text
	case agentstream.ToolChanged:
		out.Kind = pb.AttachEvent_OUTPUT
		out.EventType = "tool_" + string(body.Phase)
		out.ToolId = body.Tool.ID
		out.ToolName = body.Tool.Name
		out.ToolTitle = body.Tool.Title
		out.ToolStatus = string(body.Phase)
		out.ToolInputJson = body.Tool.Input
		out.ToolOutputPreview = body.Tool.OutputPreview
		out.ErrorMessage = body.Tool.Error
	case agentstream.PermissionRequested:
		out.Kind = pb.AttachEvent_PERMISSION_REQUEST
		out.PermissionId = body.Request.ID
		out.PermissionPayload, _ = json.Marshal(body.Request)
	case agentstream.UsageObserved:
		out.Kind = pb.AttachEvent_USAGE
		out.UsageJson = body.Usage
	case agentstream.ResultObserved:
		out.Kind = pb.AttachEvent_OUTPUT
		out.Source = "result"
		out.Data = []byte(body.Text)
		out.Text = body.Text
		out.UsageJson = body.Usage
	case agentstream.Diagnostic:
		if body.Severity == agentstream.SeverityError {
			out.Kind = pb.AttachEvent_ERROR
		}
		out.ErrorMessage = body.Message
	case agentstream.TurnFinished:
		out.Kind = pb.AttachEvent_STATE_CHANGE
		out.StopReason = body.Terminal.Message
		switch body.Terminal.Outcome {
		case agentstream.OutcomeSuccess:
			out.State = "turn_done"
		case agentstream.OutcomeCancelled:
			out.State = "turn_done"
			out.StopReason = "cancelled"
		case agentstream.OutcomeInterrupted:
			out.State = "interrupted"
		case agentstream.OutcomeError:
			out.State = "failed"
			if out.StopReason == "" {
				out.StopReason = body.Terminal.Code
			}
		}
	case agentstream.SessionMetadataChanged:
		out.Kind = pb.AttachEvent_HEARTBEAT
		out.EffectiveModel = body.EffectiveModel
	case agentstream.SessionDiagnostic:
		if body.Severity == agentstream.SeverityError {
			out.Kind = pb.AttachEvent_ERROR
		}
		out.ErrorMessage = body.Message
	}
	return out, nil
}
