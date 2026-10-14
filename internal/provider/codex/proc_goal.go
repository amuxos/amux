package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/amuxos/amux/internal/agentstream"
	providerjsonrpc "github.com/amuxos/amux/internal/provider/jsonrpc"
	"github.com/amuxos/amux/internal/session"
)

func (p *Proc) GoalSupported() bool {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	return p.goalSupported
}

func (p *Proc) SetGoalTurnAdmitter(fn session.GoalTurnAdmitter) {
	p.threadMu.Lock()
	p.goalAdmitter = fn
	p.threadMu.Unlock()
}

func decodeGoalResponse(raw json.RawMessage) (*agentstream.GoalState, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	value, exists := fields["goal"]
	if !exists {
		return nil, session.ErrGoalUnsupported
	}
	var goal *agentstream.GoalState
	if err := json.Unmarshal(value, &goal); err != nil {
		return nil, err
	}
	if goal != nil && (goal.ThreadID == "" || goal.Objective == "" || goal.Status == "") {
		return nil, fmt.Errorf("invalid native goal response")
	}
	return goal, nil
}

func (p *Proc) discoverGoals(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	response, err := p.client.Call(ctx, "thread/goal/get", map[string]any{"threadId": p.snapshotThreadID()})
	if err != nil {
		return
	}
	goal, err := decodeGoalResponse(response.Result)
	if err != nil || (goal != nil && goal.ThreadID != p.snapshotThreadID()) {
		return
	}
	p.observeGoalAt(goal, response.Sequence)
}

func (p *Proc) observeGoal(goal *agentstream.GoalState) { p.observeGoalAt(goal, 0) }

func (p *Proc) observeGoalAt(goal *agentstream.GoalState, sequence uint64) {
	p.threadMu.Lock()
	if goal != nil && goal.ThreadID != p.threadID {
		p.threadMu.Unlock()
		return
	}
	p.goalSupported = true
	if sequence != 0 {
		p.goalWireSeq = sequence
	}
	p.goal = goal.Clone()
	p.threadMu.Unlock()
	nativeType := "thread/goal/updated"
	if goal == nil {
		nativeType = "thread/goal/cleared"
	}
	p.eventSinks.Emit(agentstream.ProducerEmission{
		Session: agentstream.GoalChanged{Supported: true, Goal: goal.Clone()},
		Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.providerName, NativeType: nativeType},
	})
}

// A notification can follow an RPC response before the waiting goroutine is
// scheduled. Never overwrite that newer observation with the older response.
func (p *Proc) callGoal(ctx context.Context, method string, params map[string]any) (*agentstream.GoalState, error) {
	response, err := p.client.Call(ctx, method, params)
	if err != nil {
		return nil, err
	}
	goal, err := decodeGoalResponse(response.Result)
	if err != nil {
		return nil, err
	}
	p.threadMu.Lock()
	if response.Sequence != 0 && p.goalWireSeq > response.Sequence {
		current := p.goal.Clone()
		p.threadMu.Unlock()
		return current, nil
	}
	if goal != nil && goal.ThreadID != p.threadID {
		p.threadMu.Unlock()
		return nil, errors.New("native goal belongs to a different thread")
	}
	p.goalSupported = true
	p.goalWireSeq = response.Sequence
	p.goal = goal.Clone()
	p.threadMu.Unlock()
	p.eventSinks.Emit(agentstream.ProducerEmission{Session: agentstream.GoalChanged{Supported: true, Goal: goal.Clone()}, Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.provider(), NativeType: method}})
	return goal, nil
}

func (p *Proc) registerGoalHandlers() {
	p.onNotification("thread/goal/updated", func(message providerjsonrpc.Message) {
		var notification struct {
			ThreadID string                 `json:"threadId"`
			Goal     *agentstream.GoalState `json:"goal"`
		}
		if json.Unmarshal(message.Params, &notification) != nil || notification.ThreadID != p.snapshotThreadID() || notification.Goal == nil {
			return
		}
		p.observeGoalAt(notification.Goal, message.Sequence)
	})
	p.onNotification("thread/goal/cleared", func(message providerjsonrpc.Message) {
		var notification struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(message.Params, &notification) != nil || notification.ThreadID != p.snapshotThreadID() {
			return
		}
		p.observeGoalAt(nil, message.Sequence)
	})
}

func (p *Proc) Goal(ctx context.Context, req session.GoalRequest) (*agentstream.GoalState, error) {
	p.goalControlMu.Lock()
	defer p.goalControlMu.Unlock()
	if !p.GoalSupported() {
		return nil, session.ErrGoalUnsupported
	}
	threadID := p.snapshotThreadID()
	current, err := p.callGoal(ctx, "thread/goal/get", map[string]any{"threadId": threadID})
	if err != nil {
		return nil, err
	}
	if req.Action == "get" {
		return current, nil
	}
	if req.Action == "clear" && current == nil {
		p.observeGoal(nil)
		return nil, nil
	}
	params := map[string]any{"threadId": threadID}
	switch req.Action {
	case "create", "set":
		objective := strings.TrimSpace(req.Objective)
		if objective == "" || utf8.RuneCountInString(objective) > 4000 {
			return nil, errors.New("goal_objective_invalid")
		}
		if req.Action == "create" && current != nil && !current.IsTerminal() {
			return nil, errors.New("goal_exists")
		}
		params["objective"], params["status"] = objective, "active"
	case "pause", "resume", "budget", "clear":
		if current == nil {
			return nil, errors.New("goal_missing")
		}
		if req.Action == "resume" && current.IsTerminal() {
			return nil, errors.New("goal_complete")
		}
		if req.Action == "pause" || req.Action == "clear" {
			params["status"] = "paused"
		}
		if req.Action == "resume" {
			params["status"] = "active"
		}
	case "":
		return nil, errors.New("goal action is required")
	default:
		return nil, fmt.Errorf("unknown goal action %q", req.Action)
	}
	if req.TokenBudget != nil {
		if *req.TokenBudget <= 0 {
			return nil, errors.New("goal_budget_invalid")
		}
		params["tokenBudget"] = *req.TokenBudget
	} else if req.Action == "budget" {
		return nil, errors.New("goal_budget_invalid")
	}
	if req.Action == "set" && current != nil && !current.IsTerminal() {
		if _, err := p.callGoal(ctx, "thread/goal/set", map[string]any{"threadId": threadID, "status": "paused"}); err != nil {
			return nil, err
		}
		if err := p.Interrupt(); err != nil {
			return nil, err
		}
	}
	if current != nil && current.IsTerminal() && req.Action == "pause" {
		return current, nil
	}
	if current != nil && current.IsTerminal() && req.Action == "clear" {
		if _, err := p.client.Call(ctx, "thread/goal/clear", map[string]any{"threadId": threadID}); err != nil {
			return current, err
		}
		p.observeGoal(nil)
		return nil, nil
	}
	goal, err := p.callGoal(ctx, "thread/goal/set", params)
	if err != nil {
		return nil, err
	}
	if req.Action == "pause" || req.Action == "clear" {
		if err := p.Interrupt(); err != nil {
			return goal, err
		}
	}
	if req.Action == "clear" {
		if _, err := p.client.Call(ctx, "thread/goal/clear", map[string]any{"threadId": threadID}); err != nil {
			return goal, err
		}
		p.observeGoal(nil)
		return nil, nil
	}
	return goal, nil
}

// Called on the ordered JSON-RPC notification reader. The admission callback
// must not call provider RPCs: doing so would wait on this same reader.
func (p *Proc) admitGoalTurn(turnID string) bool {
	p.threadMu.Lock()
	goal, admit := p.goal.Clone(), p.goalAdmitter
	eligible := p.goalSupported && goal != nil && goal.Status == "active" && goal.ThreadID == p.threadID && p.turnID == "" && p.pendingTurnStart == nil
	p.threadMu.Unlock()
	if !eligible || admit == nil {
		return false
	}
	binding, err := admit(session.GoalTurnRequest{NativeTurnID: turnID, Goal: *goal})
	if err != nil || binding.TurnToken == "" || binding.Sink == nil {
		return false
	}
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	if p.turnID != "" || p.pendingTurnStart != nil || p.goal == nil || p.goal.Status != "active" || p.goal.Objective != goal.Objective {
		binding.Sink(agentstream.ProducerEmission{Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "goal_admission_changed"}})
		return false
	}
	if err := p.eventSinks.BeginTurn(binding.Sink); err != nil {
		binding.Sink(agentstream.ProducerEmission{Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "goal_sink_busy"}})
		return false
	}
	p.freshArmLocked(turnID)
	return true
}
