package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/executionauth"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// resolveScheduleCreator uses the credential's bound execution, then the exact
// active Turn receipt. It never searches for a session's most recent speaker.
func (s *hubServer) resolveScheduleCreator(ctx context.Context) (schedule.Creator, error) {
	actor, err := authenticatedActor(ctx, s.principals)
	if err != nil {
		return schedule.Creator{}, err
	}
	if actor == nil || s.principals == nil {
		return schedule.Creator{}, nil
	}
	if actor.Provider == principal.ProviderFeishu {
		return s.creatorFromAlias(actor.Scope, actor.Subject, "actor:"+actor.AuthenticationContextID)
	}
	if actor.Delegated && strings.HasPrefix(actor.Subject, "relaunch:") {
		target, name, ok := strings.Cut(strings.TrimPrefix(actor.Subject, "relaunch:"), ":")
		if !ok || s.scheduleRuntime == nil {
			return schedule.Creator{}, nil
		}
		msg, e := s.scheduleRuntime.Route(ctx, target, pb.TaskMethodResolveParentTurn, &pb.ActiveTurnRequest{Name: name})
		if e != nil {
			return schedule.Creator{}, nil
		}
		var active pb.ActiveTurnResponse
		if json.Unmarshal(msg.Payload, &active) != nil || !active.Active || active.TurnId == "" {
			return schedule.Creator{}, nil
		}
		links, e := bridge.LookupTurnLinks(bridge.DefaultDBPath(), bridge.TurnLinkQuery{TurnID: active.TurnId, SessionID: active.SessionId})
		if e != nil {
			return schedule.Creator{}, nil
		}
		var result schedule.Creator
		for _, link := range links {
			if link.TargetID != target || link.SessionName != name || link.OpenID == "" {
				continue
			}
			c, e := s.creatorFromAlias(link.BridgeID, link.OpenID, "turn:"+active.TurnId)
			if e != nil {
				return schedule.Creator{}, nil
			}
			if result.OpenID != "" && (result != c) {
				return schedule.Creator{}, nil
			}
			result = c
		}
		return result, nil
	}
	// Human CLI credentials can be associated only when there is exactly one
	// verified Feishu alias. Agent credentials without a Turn are not sufficient.
	if actor.Delegated {
		if s.scheduleTasks == nil {
			return schedule.Creator{}, nil
		}
		for _, target := range s.currentTargets() {
			for _, session := range target.Sessions {
				if session.LaunchId == actor.Subject && session.OwnerTaskId != "" {
					raw, e := s.scheduleTasks.GetSubtaskJSON(ctx, session.OwnerTaskId)
					if e != nil {
						return schedule.Creator{}, nil
					}
					var run task.TaskRun
					if json.Unmarshal(raw, &run) != nil || run.LaunchID != actor.Subject {
						return schedule.Creator{}, nil
					}
					return s.creatorFromTaskAncestry(ctx, run, 0)
				}
			}
		}
		return schedule.Creator{}, nil
	}
	aliases, e := s.principals.ListAliases()
	if e != nil {
		return schedule.Creator{}, e
	}
	var result schedule.Creator
	for _, a := range aliases {
		if a.PrincipalID == actor.ActorPrincipalID && a.Provider == principal.ProviderFeishu {
			if result.OpenID != "" {
				return schedule.Creator{}, nil
			}
			result, e = s.creatorFromAlias(a.Scope, a.Subject, "actor:"+actor.AuthenticationContextID)
			if e != nil {
				return schedule.Creator{}, e
			}
		}
	}
	return result, nil
}
func (s *hubServer) creatorFromAlias(bot, open, source string) (schedule.Creator, error) {
	p, e := s.principals.ResolveAlias(principal.ProviderFeishu, bot, open)
	if e != nil {
		return schedule.Creator{}, e
	}
	if p.Kind != principal.KindHuman || p.Status != principal.StatusActive {
		return schedule.Creator{}, errors.New("creator unavailable")
	}
	return schedule.Creator{PrincipalID: p.ID, BotID: bot, OpenID: open, Source: source}, nil
}

type bridgeSchedules struct {
	server *hubServer
	reader *bridgeSubtaskReader
}

func (c *bridgeSchedules) allowed(bot, open string, e schedule.Entry) bool {
	if c.server.principals == nil || e.Visibility.Creator.BotID != bot || e.Visibility.Creator.OpenID != open {
		return false
	}
	p, err := c.server.principals.ResolveAlias(principal.ProviderFeishu, bot, open)
	if err != nil || p.ID != e.Visibility.Creator.PrincipalID || p.Status != principal.StatusActive || p.Kind != principal.KindHuman {
		return false
	}
	var identity principal.ExecutionIdentity
	if e.Visibility.Creator.AccessIdentityJSON != "" {
		if json.Unmarshal([]byte(e.Visibility.Creator.AccessIdentityJSON), &identity) != nil {
			return false
		}
	}
	// Personal views preserve the frozen execution boundary even when every
	// isolating Runtime is offline or the named Session has been replaced.
	policy := executionauth.New(c.server.principals)
	if identity.Identified() && c.server.principals.AuthorizeEffectivePrincipal(p.ID, identity.EffectivePrincipalID) != nil {
		return false
	}
	if !policy.ScheduleTriggerAccess(p.ID, identity, identity.Identified()).Allowed {
		return false
	}
	if e.Task.ScheduleType == schedule.ScheduleTypeSubtask {
		var spec task.Spec
		if json.Unmarshal(e.Task.TaskSpecJson, &spec) != nil {
			return false
		}
		if spec.ExecutionIdentity != nil {
			identity = *spec.ExecutionIdentity
		}
	} else {
		if live, ok := c.server.sessionExecutionIdentity(e.Task.TargetID, e.Task.SessionName); ok {
			identity = live
		}
	}
	if identity.Identified() && c.server.principals.AuthorizeEffectivePrincipal(p.ID, identity.EffectivePrincipalID) != nil {
		return false
	}
	return policy.ScheduleTriggerAccess(p.ID, identity, identity.Identified()).Allowed
}
func (c *bridgeSchedules) List(_ context.Context, bot, open string) ([]schedule.Entry, error) {
	if err := c.recoverLegacyCreators(); err != nil {
		return nil, err
	}
	all, err := c.server.schedule.Personal(bot, open)
	if err != nil {
		return nil, err
	}
	out := make([]schedule.Entry, 0, len(all))
	for _, e := range all {
		if c.allowed(bot, open, e) {
			e.CreatorName = c.creatorName(bot, open)
			out = append(out, e)
		}
	}
	return out, nil
}
func (c *bridgeSchedules) Get(_ context.Context, bot, open string, id int64) (schedule.Entry, error) {
	if err := c.recoverLegacyCreators(); err != nil {
		return schedule.Entry{}, err
	}
	denied := errors.New("schedule unavailable")
	t, err := c.server.schedule.Get(id)
	if err != nil {
		return schedule.Entry{}, denied
	}
	v, err := c.server.schedule.Visibility(id)
	if err != nil {
		return schedule.Entry{}, denied
	}
	e := schedule.Entry{Task: t, Visibility: v}
	if !c.allowed(bot, open, e) {
		return schedule.Entry{}, denied
	}
	e.CreatorName = c.creatorName(bot, open)
	e.Runs, err = c.server.schedule.VisibleHistory(id)
	return e, err
}

// Recovery trusts the frozen origin actor, never an execution principal or a
// current Session speaker. A unique alias is required across all applications.
func (c *bridgeSchedules) recoverLegacyCreators() error {
	if c.server.principals == nil {
		return nil
	}
	rows, err := c.server.schedule.UnboundLegacy()
	if err != nil || len(rows) == 0 {
		return err
	}
	aliases, err := c.server.principals.ListAliases()
	if err != nil {
		return err
	}
	for _, row := range rows {
		var spec task.Spec
		if row.ScheduleType != schedule.ScheduleTypeSubtask || json.Unmarshal(row.TaskSpecJson, &spec) != nil || spec.ExecutionIdentity == nil {
			continue
		}
		i := spec.ExecutionIdentity
		if !i.Identified() || i.Validate() != nil || i.Source != principal.SourceSchedule || i.CreationAuthorizationDecisionID == "" || !strings.HasPrefix(i.ExecutionStateRef, "schedule-definition:") {
			continue
		}
		if row.OwnerPrincipalID != "" && row.OwnerPrincipalID != i.OriginActorPrincipalID {
			continue
		}
		var alias principal.Alias
		count := 0
		for _, a := range aliases {
			if a.Provider == principal.ProviderFeishu && a.PrincipalID == i.OriginActorPrincipalID {
				alias = a
				count++
			}
		}
		if count != 1 {
			continue
		}
		creator, err := c.server.creatorFromAlias(alias.Scope, alias.Subject, "upgrade:frozen-origin")
		if err != nil || creator.PrincipalID != i.OriginActorPrincipalID {
			continue
		}
		raw, err := json.Marshal(i)
		if err != nil {
			return err
		}
		creator.AccessIdentityJSON = string(raw)
		if err = c.server.schedule.RecoverCreator(row, creator); err != nil {
			return err
		}
	}
	return nil
}
func (c *bridgeSchedules) Act(ctx context.Context, bot, open string, id int64, action string) error {
	e, err := c.Get(ctx, bot, open, id)
	if err != nil {
		return err
	}
	switch action {
	case "pause":
		_, err = c.server.schedule.SetEnabled(id, false)
	case "resume":
		spec, se := schedule.ParseSpec(e.Task.Spec)
		if se != nil {
			return se
		}
		next, se := schedule.FirstRun(spec, time.Now())
		if se != nil {
			return se
		}
		e.Task.NextRunAt = next
		if _, err = c.server.schedule.Update(e.Task); err == nil {
			_, err = c.server.schedule.SetEnabled(id, true)
		}
	default:
		err = c.server.schedule.ChangeVisibility(id, action, time.Now())
	}
	return err
}
func (c *bridgeSchedules) creatorName(bot, open string) string {
	p, err := c.server.principals.ResolveAlias(principal.ProviderFeishu, bot, open)
	if err != nil {
		return ""
	}
	return p.DisplayName
}
func (c *bridgeSchedules) Store() *schedule.Store { return c.server.schedule }
func (c *bridgeSchedules) Creators(bot string) ([]string, error) {
	aliases, e := c.server.principals.ListAliases()
	if e != nil {
		return nil, e
	}
	var out []string
	for _, a := range aliases {
		if a.Provider == principal.ProviderFeishu && a.Scope == bot {
			out = append(out, a.Subject)
		}
	}
	return out, nil
}

func (c *bridgeSchedules) ObserveScheduleExecution(ctx context.Context, r schedule.Execution) (schedule.Execution, error) {
	if r.TaskID != "" {
		raw, e := c.reader.GetSubtaskJSON(ctx, r.TaskID)
		if e != nil {
			return r, e
		}
		var run task.TaskRun
		if e = json.Unmarshal(raw, &run); e != nil {
			return r, e
		}
		if run.ID != r.TaskID {
			return r, errors.New("task identity mismatch")
		}
		r.Status = "running"
		r.Terminal = run.Status.IsTerminal()
		r.TargetID = run.TargetID
		r.SessionName = run.ChildSessionName
		r.SessionID = run.ChildSessionId
		r.TurnID = run.TurnID
		r.LaunchID = run.LaunchID
		if !run.StartedAt.IsZero() {
			r.StartedAt = run.StartedAt.Unix()
		}
		if !run.FinishedAt.IsZero() {
			r.FinishedAt = run.FinishedAt.Unix()
		}
		switch run.Status {
		case task.StatusSucceeded:
			r.Status = "completed"
		case task.StatusFailed:
			r.Status = "failed"
		case task.StatusTimedOut:
			r.Status = "timed_out"
		case task.StatusCancelled:
			r.Status = "cancelled"
		case task.StatusPending, task.StatusScheduled, task.StatusPreparing:
			r.Status = "submitted"
		}
		if run.Result != nil {
			r.Result = run.Result.Summary
			if run.Status == task.StatusSucceeded && run.Result.Status == task.ResultPartial {
				r.Status = "partial"
			}
			if run.Status == task.StatusSucceeded && run.Result.Status == task.ResultFailed {
				r.Status = "failed"
			}
		}
		return r, nil
	}
	// Only canonical TurnFinished closes a prompt execution. Result text is
	// available for viewing but is never used to infer success.
	seen := false
	err := c.server.scheduleRuntime.RouteStream(ctx, r.TargetID, "Attach", &pb.AttachRequest{Name: r.SessionName, TimelineMode: pb.AttachRequest_HISTORY, AfterCursor: r.Cursor}, func(msg *pb.TunnelMsg) error {
		var ev pb.AttachEvent
		if e := json.Unmarshal(msg.Payload, &ev); e != nil {
			return e
		}
		if len(ev.CanonicalJson) == 0 {
			return nil
		}
		event, e := agentstream.UnmarshalEvent(ev.CanonicalJson)
		if e != nil {
			return e
		}
		if event.SessionID != r.SessionID {
			return errors.New("session identity mismatch")
		}
		r.Cursor = ev.Cursor
		if event.Turn == nil || event.Turn.ID != r.TurnID {
			return nil
		}
		seen = true
		switch body := event.Body.(type) {
		case agentstream.TurnStarted:
			r.Status = "running"
			r.StartedAt = event.At.Unix()
		case agentstream.ResultObserved:
			r.Result = body.Text
		case agentstream.AssistantMessage:
			r.Result = body.Text
		case agentstream.TurnFinished:
			r.Terminal = true
			r.FinishedAt = event.At.Unix()
			r.Reason = body.Terminal.Code
			switch body.Terminal.Outcome {
			case agentstream.OutcomeSuccess:
				r.Status = "completed"
			case agentstream.OutcomeCancelled:
				r.Status = "cancelled"
			default:
				r.Status = "failed"
			}
		}
		return nil
	})
	if err == nil && r.Status == "unknown" {
		if seen || r.StartedAt > 0 {
			r.Status = "running"
		} else {
			r.Status = "submitted"
		}
		r.Reason = ""
	}
	return r, err
}

// ParentTurnID is frozen by task admission, so later activity in the parent's
// session cannot change a descendant's creator. Bound the walk defensively.
func (s *hubServer) creatorFromTaskAncestry(ctx context.Context, run task.TaskRun, depth int) (schedule.Creator, error) {
	if s.schedule != nil {
		if frozen, e := s.schedule.CreatorForTask(run.ID); e == nil {
			live, e := s.creatorFromAlias(frozen.BotID, frozen.OpenID, frozen.Source)
			if e == nil && live.PrincipalID == frozen.PrincipalID {
				return frozen, nil
			}
			return schedule.Creator{}, nil
		}
	}
	if depth >= 16 || run.ParentTurnID == "" {
		return schedule.Creator{}, nil
	}
	links, err := bridge.LookupTurnLinks(bridge.DefaultDBPath(), bridge.TurnLinkQuery{TurnID: run.ParentTurnID, SessionID: run.ParentSessionID})
	if err == nil && len(links) > 0 {
		var found schedule.Creator
		for _, link := range links {
			c, e := s.creatorFromAlias(link.BridgeID, link.OpenID, "turn:"+run.ParentTurnID)
			if e != nil {
				return schedule.Creator{}, nil
			}
			if found.OpenID != "" && found != c {
				return schedule.Creator{}, nil
			}
			found = c
		}
		return found, nil
	}
	if run.ParentTaskID == "" {
		return schedule.Creator{}, nil
	}
	raw, e := s.scheduleTasks.GetSubtaskJSON(ctx, run.ParentTaskID)
	if e != nil {
		return schedule.Creator{}, nil
	}
	var parent task.TaskRun
	if json.Unmarshal(raw, &parent) != nil || parent.ID != run.ParentTaskID || parent.TurnID != run.ParentTurnID {
		return schedule.Creator{}, nil
	}
	return s.creatorFromTaskAncestry(ctx, parent, depth+1)
}
