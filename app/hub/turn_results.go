package hub

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/executionauth"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	"github.com/amuxos/amux/internal/turnresult"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func turnResultPolicyFromConfig(cfg config.TurnResultsConfig) turnresult.Policy {
	return turnresult.Policy{
		TTL:            time.Duration(cfg.TTLSeconds) * time.Second,
		PendingTimeout: time.Duration(cfg.PendingTimeoutSeconds) * time.Second,
		MaxEntries:     cfg.MaxEntries,
		MaxTotalBytes:  cfg.MaxTotalBytes,
		MaxContentSize: cfg.MaxContentBytes,
	}.Normalize()
}

func initTurnResultStore(cfg config.TurnResultsConfig, path string) (*turnresult.Store, error) {
	if !cfg.Enabled {
		if err := turnresult.ClearFile(path); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return turnresult.OpenStore(path, turnResultPolicyFromConfig(cfg))
}

func (s *hubServer) turnResultsEnabled() bool {
	if s.turnResults == nil {
		return false
	}
	if s.turnResultsConfig != nil {
		return s.turnResultsConfig().Enabled
	}
	return true
}

func (s *hubServer) runTurnResultSweeper(ctx context.Context) {
	if s == nil || s.turnResults == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.turnResults.Sweep(now); err != nil {
				log.Warn().Err(err).Msg("hub: sweep turn results")
			}
		}
	}
}

func (s *hubServer) hasFeishuBinding(targetID, sessionName string) bool {
	if s == nil || s.turnResultBindingCheck == nil {
		return false
	}
	return s.turnResultBindingCheck(targetID, sessionName)
}

func (s *hubServer) frozenSessionIdentityJSON(targetID, sessionRef string) string {
	sess := sessionByRef(s.currentTargets(), targetID, sessionRef)
	if sess == nil || sess.ExecutionIdentity == nil {
		return ""
	}
	identity := principal.FromProtoIdentity(sess.ExecutionIdentity)
	if !identity.Identified() {
		return ""
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (s *hubServer) registerSubmittedTurn(targetID, sessionName, source string, resp *pb.SubmitResponse) {
	if s == nil || !s.turnResultsEnabled() || resp == nil || strings.TrimSpace(resp.GetTurnId()) == "" {
		return
	}
	kind := normalizeSubmitSourceKind(source)
	_, err := s.turnResults.RegisterSubmission(turnresult.SubmissionInput{
		Ref: turnresult.Ref{
			TargetID:    targetID,
			SessionID:   resp.GetSessionId(),
			SessionName: sessionName,
			LaunchID:    resp.GetLaunchId(),
			TurnID:      resp.GetTurnId(),
		},
		Source: turnresult.Source{
			Kind: kind,
		},
		DeliveryExpected:      s.hasFeishuBinding(targetID, sessionName),
		ExecutionIdentityJSON: s.frozenSessionIdentityJSON(targetID, sessionName),
		Now:                   time.Now(),
	})
	if err != nil {
		log.Warn().Err(err).Str("turn_id", resp.GetTurnId()).Msg("hub: register submitted turn result")
	}
}

func (s *hubServer) observeScheduledExecution(t schedule.Task, r schedule.Execution) {
	if s == nil || !s.turnResultsEnabled() {
		return
	}
	now := time.Now()
	scheduleType := t.ScheduleType
	if scheduleType == "" {
		scheduleType = schedule.ScheduleTypeSessionPrompt
	}
	runMode := ""
	if scheduleType == schedule.ScheduleTypeSessionPrompt {
		runMode = t.RunMode
		if runMode == "" {
			runMode = schedule.RunModeReuseSession
		}
	}
	ref := turnresult.Ref{
		TargetID:    r.TargetID,
		SessionID:   r.SessionID,
		SessionName: r.SessionName,
		LaunchID:    r.LaunchID,
		TurnID:      r.TurnID,
	}
	src := turnresult.Source{
		Kind:         turnresult.SourceSchedule,
		ScheduleID:   t.ID,
		ExecutionID:  r.ID,
		ScheduleType: scheduleType,
		RunMode:      runMode,
		ScheduledAt:  r.ScheduledAt,
		TaskID:       r.TaskID,
	}
	identityJSON := ""
	if r.TargetID != "" && r.SessionName != "" {
		identityJSON = s.frozenSessionIdentityJSON(r.TargetID, r.SessionName)
	}
	deliveryExpected := scheduleType == schedule.ScheduleTypeSessionPrompt &&
		runMode == schedule.RunModeReuseSession &&
		r.TurnID != "" &&
		s.hasFeishuBinding(r.TargetID, r.SessionName)

	if !r.Terminal {
		if _, err := s.turnResults.RegisterSubmission(turnresult.SubmissionInput{
			Ref:                   ref,
			Source:                src,
			DeliveryExpected:      deliveryExpected,
			OwnerPrincipalID:      t.OwnerPrincipalID,
			ExecutionIdentityJSON: identityJSON,
			Now:                   now,
		}); err != nil {
			log.Warn().Err(err).Int64("schedule_id", t.ID).Int64("execution_id", r.ID).Msg("hub: register scheduled turn result")
		}
		return
	}

	execStatus := mapScheduleExecutionStatus(r.Status)
	output := turnresult.Output{
		Format:  "markdown",
		Content: r.Result,
	}
	if execStatus == turnresult.ExecutionCompleted && r.TargetID != "" && r.TurnID != "" && (r.SessionID != "" || r.SessionName != "") {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		rtResp, err := s.fetchRuntimeTurnResult(ctx, r.TargetID, r.SessionID, r.SessionName, r.TurnID)
		cancel()
		if err == nil && rtResp != nil && rtResp.GetFound() && rtResp.GetTerminal() {
			if rtResp.GetSessionId() != "" {
				ref.SessionID = rtResp.GetSessionId()
			}
			if rtResp.GetSessionName() != "" {
				ref.SessionName = rtResp.GetSessionName()
			}
			if rtResp.GetLaunchId() != "" {
				ref.LaunchID = rtResp.GetLaunchId()
			}
			output = turnresult.Output{
				Status:  rtResp.GetOutputStatus(),
				Reason:  rtResp.GetOutputReason(),
				Format:  "markdown",
				Content: rtResp.GetOutputContent(),
			}
		}
	}

	if _, err := s.turnResults.RecordExecutionOutput(turnresult.ExecutionInput{
		Ref:    ref,
		Source: src,
		Execution: turnresult.Execution{
			Status:     execStatus,
			Code:       r.Reason,
			StartedAt:  r.StartedAt,
			FinishedAt: r.FinishedAt,
		},
		Output:                output,
		OwnerPrincipalID:      t.OwnerPrincipalID,
		ExecutionIdentityJSON: identityJSON,
		Now:                   now,
	}); err != nil {
		log.Warn().Err(err).Int64("schedule_id", t.ID).Int64("execution_id", r.ID).Msg("hub: record scheduled turn result")
	}
}

// GetTurnResult returns the bounded, read-only structured result for one Turn
// or scheduled execution. It never re-runs an agent or resends IM messages.
func (s *hubServer) GetTurnResult(ctx context.Context, req *pb.AmuxdGetTurnResultRequest) (*pb.AmuxdGetTurnResultResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if !s.turnResultsEnabled() {
		return &pb.AmuxdGetTurnResultResponse{
			Status: turnresult.StatusUnavailable,
			Reason: turnresult.ReasonDisabled,
		}, nil
	}

	hasSchedule := req.GetScheduleId() > 0
	hasFeishu := strings.TrimSpace(req.GetFeishuMessageId()) != ""
	hasSessionTurn := strings.TrimSpace(req.GetTurnId()) != ""

	selectors := 0
	if hasSchedule {
		selectors++
	}
	if hasFeishu {
		selectors++
	}
	if hasSessionTurn {
		selectors++
	}
	if selectors != 1 {
		return nil, status.Error(codes.InvalidArgument, "specify exactly one of (session+turn_id), feishu_message_id, or schedule_id")
	}
	if hasSessionTurn && strings.TrimSpace(req.GetSessionId()) == "" && strings.TrimSpace(req.GetSessionName()) == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id or session_name is required with turn_id")
	}

	now := time.Now()
	lookupFn := func() (turnresult.LookupResult, error) {
		switch {
		case hasSchedule:
			return s.turnResults.LookupByScheduleExecution(req.GetScheduleId(), req.GetExecutionId(), now)
		case hasFeishu:
			return s.turnResults.LookupByFeishuMessage(req.GetBridgeId(), req.GetFeishuMessageId(), now)
		default:
			return s.turnResults.LookupBySessionTurn(req.GetTargetId(), req.GetSessionId(), req.GetSessionName(), req.GetTurnId(), now)
		}
	}

	lookup, err := lookupFn()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup turn result: %v", err)
	}

	if lookup.Record == nil {
		// Enforce caller authorization even on not_found so cross-principal
		// callers cannot probe whether another principal's resource exists.
		if hasSchedule && s.schedule != nil {
			if _, authErr := s.authorizeScheduleAccess(ctx, s.schedule, req.GetScheduleId()); authErr != nil {
				if status.Code(authErr) != codes.NotFound {
					return nil, authErr
				}
			}
		}
		if hasSessionTurn {
			ref := firstNonEmpty(req.GetSessionName(), req.GetSessionId())
			if authErr := s.authorizeSessionRead(ctx, req.GetTargetId(), ref); authErr != nil {
				return nil, authErr
			}
		}
		return &pb.AmuxdGetTurnResultResponse{
			Status: turnresult.StatusNotFound,
		}, nil
	}

	if err := s.authorizeTurnResultRecord(ctx, lookup.Record); err != nil {
		return nil, err
	}

	// On-demand immediate refresh for pending turns whose execution has not yet
	// been observed into turn_results.db (eliminates 20s scheduler polling lag
	// and serves CLI/Desktop/MCP submitted turns immediately after TurnFinished).
	if lookup.Status == turnresult.StatusNotReady && !lookup.Record.ExecutionSettled {
		if refreshed := s.refreshPendingTurnResult(ctx, lookup.Record); refreshed {
			now = time.Now()
			if updated, reErr := lookupFn(); reErr == nil {
				lookup = updated
			}
		}
	}

	return turnResultToPB(lookup), nil
}

func (s *hubServer) authorizeTurnResultRecord(ctx context.Context, rec *turnresult.Record) error {
	if s.principals == nil || rec == nil {
		return nil
	}
	if rec.Source.ScheduleID > 0 && s.schedule != nil {
		_, err := s.authorizeScheduleAccess(ctx, s.schedule, rec.Source.ScheduleID)
		if err != nil {
			if status.Code(err) != codes.NotFound {
				return err
			}
			// Schedule was deleted after execution: enforce frozen owner boundary.
			actor, actorErr := authenticatedActor(ctx, s.principals)
			if actorErr != nil {
				return actorErr
			}
			decision := executionauth.New(s.principals).ScheduleAccess(actor, rec.OwnerPrincipalID, s.fleetIsolationActive())
			if !decision.Allowed {
				return status.Error(codes.PermissionDenied, "turn result belongs to another principal's schedule")
			}
		}
		return nil
	}

	if rec.Ref.TargetID != "" && (rec.Ref.SessionName != "" || rec.Ref.SessionID != "") {
		ref := firstNonEmpty(rec.Ref.SessionName, rec.Ref.SessionID)
		if sessionByRef(s.currentTargets(), rec.Ref.TargetID, ref) != nil {
			return s.authorizeSessionRead(ctx, rec.Ref.TargetID, ref)
		}
		if rec.ExecutionIdentityJSON != "" && s.fleetIsolationActive() {
			var identity principal.ExecutionIdentity
			if err := json.Unmarshal([]byte(rec.ExecutionIdentityJSON), &identity); err != nil {
				return status.Error(codes.PermissionDenied, "invalid frozen execution identity")
			}
			if identity.IdentityMode != "" && identity.IdentityMode != principal.IdentityModeLegacyHost {
				actor, err := authenticatedActor(ctx, s.principals)
				if err != nil {
					return err
				}
				decision := executionauth.New(s.principals).SessionAccess(executionauth.SessionAccessRequest{
					Actor: actor,
					Target: executionauth.SessionFacts{
						ID:       rec.Ref.SessionID,
						TargetID: rec.Ref.TargetID,
						Name:     rec.Ref.SessionName,
						LaunchID: rec.Ref.LaunchID,
						Identity: identity,
					},
					Sessions:        sessionFactsFromTargets(s.currentTargets()),
					IsolationActive: true,
				})
				if !decision.Allowed {
					return status.Error(codes.PermissionDenied, "turn result is outside caller session scope")
				}
			}
		}
	}
	return nil
}

func (s *hubServer) refreshPendingTurnResult(ctx context.Context, rec *turnresult.Record) bool {
	if rec == nil {
		return false
	}
	now := time.Now()
	if rec.Source.TaskID != "" && s.scheduleTasks != nil {
		raw, err := s.scheduleTasks.GetSubtaskJSON(ctx, rec.Source.TaskID)
		if err != nil || len(raw) == 0 {
			return false
		}
		var run task.TaskRun
		if json.Unmarshal(raw, &run) != nil || !run.Status.IsTerminal() {
			return false
		}
		execStatus := mapTaskStatus(run)
		ref := rec.Ref
		if run.TargetID != "" {
			ref.TargetID = run.TargetID
		}
		if run.ChildSessionId != "" {
			ref.SessionID = run.ChildSessionId
		}
		if run.ChildSessionName != "" {
			ref.SessionName = run.ChildSessionName
		}
		if run.LaunchID != "" {
			ref.LaunchID = run.LaunchID
		}
		if run.TurnID != "" {
			ref.TurnID = run.TurnID
		}
		output := turnresult.Output{Format: "markdown"}
		if run.Result != nil {
			output.Content = run.Result.Summary
		}
		if execStatus == turnresult.ExecutionCompleted && ref.TargetID != "" && ref.TurnID != "" && (ref.SessionID != "" || ref.SessionName != "") {
			if rtResp, rtErr := s.fetchRuntimeTurnResult(ctx, ref.TargetID, ref.SessionID, ref.SessionName, ref.TurnID); rtErr == nil && rtResp != nil && rtResp.GetFound() && rtResp.GetTerminal() {
				output = turnresult.Output{
					Status:  rtResp.GetOutputStatus(),
					Reason:  rtResp.GetOutputReason(),
					Format:  "markdown",
					Content: rtResp.GetOutputContent(),
				}
			}
		}
		var startedAt, finishedAt int64
		if !run.StartedAt.IsZero() {
			startedAt = run.StartedAt.Unix()
		}
		if !run.FinishedAt.IsZero() {
			finishedAt = run.FinishedAt.Unix()
		}
		_, err = s.turnResults.RecordExecutionOutput(turnresult.ExecutionInput{
			Ref:    ref,
			Source: rec.Source,
			Execution: turnresult.Execution{
				Status:     execStatus,
				StartedAt:  startedAt,
				FinishedAt: finishedAt,
			},
			Output: output,
			Now:    now,
		})
		return err == nil
	}

	if rec.Ref.TargetID != "" && rec.Ref.TurnID != "" && (rec.Ref.SessionID != "" || rec.Ref.SessionName != "") {
		rtResp, err := s.fetchRuntimeTurnResult(ctx, rec.Ref.TargetID, rec.Ref.SessionID, rec.Ref.SessionName, rec.Ref.TurnID)
		if err != nil || rtResp == nil || !rtResp.GetFound() || !rtResp.GetTerminal() {
			return false
		}
		ref := rec.Ref
		if rtResp.GetSessionId() != "" {
			ref.SessionID = rtResp.GetSessionId()
		}
		if rtResp.GetSessionName() != "" {
			ref.SessionName = rtResp.GetSessionName()
		}
		if rtResp.GetLaunchId() != "" {
			ref.LaunchID = rtResp.GetLaunchId()
		}
		_, err = s.turnResults.RecordExecutionOutput(turnresult.ExecutionInput{
			Ref:    ref,
			Source: rec.Source,
			Execution: turnresult.Execution{
				Status:     rtResp.GetExecutionStatus(),
				Code:       rtResp.GetExecutionCode(),
				StartedAt:  rtResp.GetStartedAt(),
				FinishedAt: rtResp.GetFinishedAt(),
			},
			Output: turnresult.Output{
				Status:  rtResp.GetOutputStatus(),
				Reason:  rtResp.GetOutputReason(),
				Format:  "markdown",
				Content: rtResp.GetOutputContent(),
			},
			Now: now,
		})
		return err == nil
	}
	return false
}

func (s *hubServer) fetchRuntimeTurnResult(ctx context.Context, targetID, sessionID, sessionName, turnID string) (*pb.GetTurnResultResponse, error) {
	maxBytes := int32(config.DefaultTurnResultsMaxContentBytes)
	if s.turnResults != nil {
		maxBytes = int32(s.turnResults.Policy().MaxContentSize)
	}
	req := &pb.GetTurnResultRequest{
		SessionId:       sessionID,
		Name:            sessionName,
		TurnId:          turnID,
		MaxContentBytes: maxBytes,
	}
	if s.runtimeGetTurnResult != nil {
		return s.runtimeGetTurnResult(ctx, targetID, req)
	}
	return routeUnary[pb.GetTurnResultRequest, pb.GetTurnResultResponse](s, ctx, targetID, "GetTurnResult", req)
}

func turnResultToPB(lookup turnresult.LookupResult) *pb.AmuxdGetTurnResultResponse {
	resp := &pb.AmuxdGetTurnResultResponse{
		Status: lookup.Status,
		Reason: lookup.Reason,
	}
	if lookup.Result == nil {
		return resp
	}
	r := lookup.Result
	deliveries := make([]*pb.TurnResultDelivery, 0, len(r.Deliveries))
	for _, d := range r.Deliveries {
		msgs := make([]*pb.TurnResultDeliveryMessage, 0, len(d.Messages))
		for _, m := range d.Messages {
			msgs = append(msgs, &pb.TurnResultDeliveryMessage{
				Kind:      m.Kind,
				MessageId: m.MessageID,
				Name:      m.Name,
			})
		}
		deliveries = append(deliveries, &pb.TurnResultDelivery{
			Channel:       d.Channel,
			Status:        d.Status,
			Reason:        d.Reason,
			BridgeId:      d.BridgeID,
			ChatId:        d.ChatID,
			RootMessageId: d.RootMessageID,
			Messages:      msgs,
		})
	}
	resp.Result = &pb.TurnResultPayload{
		Version: int32(r.Version),
		Ref: &pb.TurnResultRef{
			TargetId:    r.Ref.TargetID,
			SessionId:   r.Ref.SessionID,
			SessionName: r.Ref.SessionName,
			LaunchId:    r.Ref.LaunchID,
			TurnId:      r.Ref.TurnID,
		},
		Source: &pb.TurnResultSource{
			Kind:          r.Source.Kind,
			BridgeId:      r.Source.BridgeID,
			ChatId:        r.Source.ChatID,
			RootMessageId: r.Source.RootMessageID,
			MessageIds:    append([]string(nil), r.Source.MessageIDs...),
			ScheduleId:    r.Source.ScheduleID,
			ExecutionId:   r.Source.ExecutionID,
			ScheduleType:  r.Source.ScheduleType,
			RunMode:       r.Source.RunMode,
			ScheduledAt:   r.Source.ScheduledAt,
			TaskId:        r.Source.TaskID,
		},
		Execution: &pb.TurnResultExecution{
			Status:     r.Execution.Status,
			Code:       r.Execution.Code,
			StartedAt:  r.Execution.StartedAt,
			FinishedAt: r.Execution.FinishedAt,
		},
		Output: &pb.TurnResultOutput{
			Status:  r.Output.Status,
			Reason:  r.Output.Reason,
			Format:  r.Output.Format,
			Content: r.Output.Content,
		},
		Deliveries: deliveries,
	}
	return resp
}

func normalizeSubmitSourceKind(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "lark", "feishu":
		return turnresult.SourceFeishu
	case "scheduler", "schedule":
		return turnresult.SourceSchedule
	case "desktop":
		return turnresult.SourceDesktop
	case "mcp":
		return turnresult.SourceMCP
	case "subtask":
		return turnresult.SourceSubtask
	case "goal":
		return turnresult.SourceGoal
	default:
		return turnresult.SourceCLI
	}
}

func mapScheduleExecutionStatus(status string) string {
	switch status {
	case "completed":
		return turnresult.ExecutionCompleted
	case "failed":
		return turnresult.ExecutionFailed
	case "cancelled":
		return turnresult.ExecutionCancelled
	case "interrupted":
		return turnresult.ExecutionInterrupted
	case "missed":
		return turnresult.ExecutionMissed
	case "partial":
		return turnresult.ExecutionPartial
	case "timed_out":
		return turnresult.ExecutionTimedOut
	case "running", "submitted", "submitting":
		return turnresult.ExecutionRunning
	default:
		return turnresult.ExecutionUnknown
	}
}

func mapTaskStatus(run task.TaskRun) string {
	switch run.Status {
	case task.StatusSucceeded:
		if run.Result != nil {
			if run.Result.Status == task.ResultPartial {
				return turnresult.ExecutionPartial
			}
			if run.Result.Status == task.ResultFailed {
				return turnresult.ExecutionFailed
			}
		}
		return turnresult.ExecutionCompleted
	case task.StatusFailed:
		return turnresult.ExecutionFailed
	case task.StatusTimedOut:
		return turnresult.ExecutionTimedOut
	case task.StatusCancelled:
		return turnresult.ExecutionCancelled
	default:
		return turnresult.ExecutionUnknown
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
