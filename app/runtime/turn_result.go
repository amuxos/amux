package runtime

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/turnresult"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetTurnResult returns the canonical execution status and final adopted output
// for a single Turn in a Session. It is strictly read-only and never infers
// completion from text alone.
func (s *server) GetTurnResult(ctx context.Context, req *pb.GetTurnResultRequest) (*pb.GetTurnResultResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	turnID := strings.TrimSpace(req.GetTurnId())
	if turnID == "" {
		return nil, status.Error(codes.InvalidArgument, "turn_id is required")
	}
	sessionID := strings.TrimSpace(req.GetSessionId())
	sessionName := strings.TrimSpace(req.GetName())
	if sessionID == "" && sessionName == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id or name is required")
	}

	var (
		events      []agentstream.Event
		resolvedID  = sessionID
		resolvedNm  = sessionName
		launchID    string
		loadedEvent bool
	)

	if s.mgr != nil {
		lookupKey := sessionID
		if lookupKey == "" {
			lookupKey = sessionName
		}
		sess, err := s.mgr.Get(lookupKey)
		if err != nil && sessionName != "" && lookupKey != sessionName {
			sess, err = s.mgr.Get(sessionName)
		}
		if err == nil && sess != nil {
			snap := sess.Snapshot()
			resolvedID = snap.ID
			resolvedNm = snap.Name
			launchID = snap.LaunchID
			if s.cleanupDB != nil && resolvedID != "" {
				if dbEvents, dbErr := s.cleanupDB.LoadTurnEvents(ctx, resolvedID, turnID); dbErr == nil && len(dbEvents) > 0 {
					events = dbEvents
					loadedEvent = true
				}
			}
			if !loadedEvent {
				_, sub, readErr := sess.AgentTimeline().Read(ctx, agentstream.ReadRequest{Mode: agentstream.ReadHistory})
				if readErr != nil {
					return nil, status.Errorf(codes.Internal, "read session timeline: %v", readErr)
				}
				for {
					ev, recvErr := sub.Recv(ctx)
					if errors.Is(recvErr, io.EOF) {
						break
					}
					if recvErr != nil {
						sub.Close()
						return nil, status.Errorf(codes.Internal, "read session timeline: %v", recvErr)
					}
					if ev.Turn != nil && ev.Turn.ID == turnID {
						events = append(events, ev)
					}
				}
				sub.Close()
				loadedEvent = true
			}
		}
	}

	if !loadedEvent && s.cleanupDB != nil && resolvedID != "" {
		dbEvents, err := s.cleanupDB.LoadTurnEvents(ctx, resolvedID, turnID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "load turn events: %v", err)
		}
		events = dbEvents
	}

	if len(events) == 0 {
		return &pb.GetTurnResultResponse{
			Found:       false,
			SessionId:   resolvedID,
			SessionName: resolvedNm,
			LaunchId:    launchID,
			TurnId:      turnID,
		}, nil
	}

	maxBytes := int(req.GetMaxContentBytes())
	if maxBytes <= 0 {
		maxBytes = config.DefaultTurnResultsMaxContentBytes
	}
	return summarizeTurnEvents(resolvedID, resolvedNm, launchID, turnID, events, maxBytes), nil
}

func summarizeTurnEvents(sessionID, sessionName, launchID, turnID string, events []agentstream.Event, maxContentBytes int) *pb.GetTurnResultResponse {
	resp := &pb.GetTurnResultResponse{
		Found:           true,
		SessionId:       sessionID,
		SessionName:     sessionName,
		LaunchId:        launchID,
		TurnId:          turnID,
		ExecutionStatus: turnresult.ExecutionRunning,
		OutputStatus:    turnresult.OutputUnavailable,
	}

	var (
		hasResult       bool
		resultText      string
		hasAssistantMsg bool
		assistantMsg    string
		deltaBuf        strings.Builder
	)

	for _, ev := range events {
		if resp.SessionId == "" && ev.SessionID != "" {
			resp.SessionId = ev.SessionID
		}
		if ev.Origin.LaunchID != "" {
			resp.LaunchId = ev.Origin.LaunchID
		}
		switch body := ev.Body.(type) {
		case agentstream.TurnStarted:
			if resp.StartedAt == 0 && !ev.At.IsZero() {
				resp.StartedAt = ev.At.Unix()
			}
		case agentstream.AssistantDelta:
			deltaBuf.WriteString(body.Text)
		case agentstream.AssistantMessage:
			hasAssistantMsg = true
			assistantMsg = body.Text
			deltaBuf.Reset()
		case agentstream.ResultObserved:
			hasResult = true
			resultText = body.Text
		case agentstream.TurnFinished:
			resp.Terminal = true
			if !ev.At.IsZero() {
				resp.FinishedAt = ev.At.Unix()
			}
			resp.ExecutionCode = body.Terminal.Code
			switch body.Terminal.Outcome {
			case agentstream.OutcomeSuccess:
				resp.ExecutionStatus = turnresult.ExecutionCompleted
			case agentstream.OutcomeCancelled:
				resp.ExecutionStatus = turnresult.ExecutionCancelled
			case agentstream.OutcomeInterrupted:
				resp.ExecutionStatus = turnresult.ExecutionInterrupted
			default:
				resp.ExecutionStatus = turnresult.ExecutionFailed
			}
		}
	}

	if !resp.Terminal {
		return resp
	}
	if resp.ExecutionStatus != turnresult.ExecutionCompleted {
		resp.OutputStatus = turnresult.OutputUnavailable
		if resp.ExecutionCode != "" {
			resp.OutputReason = resp.ExecutionCode
		} else {
			resp.OutputReason = turnresult.ReasonExecutionNotCompleted
		}
		return resp
	}

	finalText := ""
	switch {
	case hasResult:
		finalText = resultText
	case hasAssistantMsg:
		finalText = assistantMsg
	default:
		finalText = deltaBuf.String()
	}

	if len(finalText) > maxContentBytes {
		resp.OutputStatus = turnresult.OutputUnavailable
		resp.OutputReason = turnresult.ReasonResultTooLarge
		return resp
	}
	if strings.TrimSpace(finalText) == "" {
		resp.OutputStatus = turnresult.OutputEmpty
		return resp
	}
	resp.OutputStatus = turnresult.OutputAvailable
	resp.OutputContent = finalText
	return resp
}
