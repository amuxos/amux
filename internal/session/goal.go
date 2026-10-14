package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/sourceframe"
)

var ErrGoalUnsupported = errors.New("native goals are not supported by this runtime")

// GoalRequest is a control operation, never model input. Budgets are optional
// and only set when the caller explicitly supplies one.
type GoalRequest struct {
	Action      string `json:"action"`
	Objective   string `json:"objective,omitempty"`
	TokenBudget *int64 `json:"token_budget,omitempty"`
}

// GoalController is optional. Providers must positively discover native goal
// support; sharing an executable family or wire protocol is not sufficient.
type GoalController interface {
	GoalSupported() bool
	Goal(context.Context, GoalRequest) (*agentstream.GoalState, error)
}

// GoalTurnRequest describes a native continuation before any of its output is
// accepted. The Session, not the provider, allocates its canonical Turn identity.
type GoalTurnRequest struct {
	NativeTurnID string                `json:"native_turn_id"`
	Goal         agentstream.GoalState `json:"goal"`
}

type GoalTurnBinding struct {
	TurnToken string
	Sink      func(agentstream.ProducerEmission) bool
}

type GoalTurnAdmitter func(GoalTurnRequest) (GoalTurnBinding, error)

// NativeGoalTurnProvider must obtain a fresh admission capability for every
// continuation. A denied continuation retains the normal quarantine behavior.
type NativeGoalTurnProvider interface {
	SetGoalTurnAdmitter(GoalTurnAdmitter)
}

// GoalTurnReadyProvider defers restored native turns until their Session owner
// has restored its grant and installed the canonical event sink.
type GoalTurnReadyProvider interface {
	EnableGoalTurns()
}

// GoalGrant persists only the explicit control-plane continuation grant. It
// never grants local-expert authoring or transfers another turn's privileges.
type GoalGrant struct {
	ThreadID  string `json:"thread_id"`
	Objective string `json:"objective"`
	CreatedAt int64  `json:"created_at,omitempty"`
	Enabled   bool   `json:"enabled"`
}

func (s *Session) GoalSupported() bool {
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	goals, ok := proc.(GoalController)
	return ok && goals.GoalSupported()
}

func (s *Session) setGoalGrant(grant *GoalGrant) error {
	s.goalMu.Lock()
	defer s.goalMu.Unlock()
	if s.persistGoalGrant != nil {
		if err := s.persistGoalGrant(grant); err != nil {
			return err
		}
	}
	s.goalGrant = grant
	return nil
}

func (s *Session) Goal(ctx context.Context, request GoalRequest) (*agentstream.GoalState, error) {
	s.runtimeMu.RLock()
	proc := s.proc
	s.runtimeMu.RUnlock()
	goals, ok := proc.(GoalController)
	if !ok || !goals.GoalSupported() {
		return nil, ErrGoalUnsupported
	}
	s.goalControlMu.Lock()
	defer s.goalControlMu.Unlock()
	if request.Action == "get" {
		return goals.Goal(ctx, request)
	}
	current, err := goals.Goal(ctx, GoalRequest{Action: "get"})
	if err != nil {
		return nil, err
	}
	var grant *GoalGrant
	s.goalMu.Lock()
	prior := s.goalGrant
	if s.goalGrant != nil {
		copy := *s.goalGrant
		grant = &copy
	}
	s.goalMu.Unlock()
	switch request.Action {
	case "create", "set":
		if request.Action == "create" && current != nil && !current.IsTerminal() {
			return nil, errors.New("goal_exists")
		}
		threadID := s.Snapshot().AgentSessionID
		if metadata := s.AgentTimeline().Head().Metadata; metadata != nil {
			threadID = metadata.AgentSessionID
		}
		if threadID == "" {
			return nil, errors.New("native conversation identity is unavailable")
		}
		grant = &GoalGrant{ThreadID: threadID, Objective: strings.TrimSpace(request.Objective), Enabled: true}
	case "resume":
		if current == nil {
			return nil, errors.New("goal_missing")
		}
		grant = &GoalGrant{ThreadID: current.ThreadID, Objective: current.Objective, CreatedAt: current.CreatedAt, Enabled: true}
	case "pause", "clear":
		if grant != nil {
			grant.Enabled = false
		}
	case "budget":
	default:
		return nil, fmt.Errorf("unknown goal action %q", request.Action)
	}
	if err := s.setGoalGrant(grant); err != nil {
		return nil, err
	}
	// Release the grant lock across native RPCs: a set/resume can synchronously
	// emit a continuation that needs this grant before the RPC response arrives.
	goal, err := goals.Goal(ctx, request)
	if err != nil {
		if request.Action == "pause" || request.Action == "clear" {
			return goal, err // Keep revocation after an ambiguous native failure.
		}
		return goal, errors.Join(err, s.setGoalGrant(prior))
	}
	if request.Action == "clear" {
		if active := s.AgentTimeline().ActiveTurn(); current == nil && active != nil && active.StartedEvent().Origin.Channel == sourceframe.SourceGoal {
			if err := proc.Interrupt(); err != nil {
				return goal, err
			}
		}
		err = s.setGoalGrant(nil)
	} else if (request.Action == "create" || request.Action == "set" || request.Action == "resume") && goal != nil {
		err = s.bindGoalGrant(grant, goal)
	}
	return goal, err
}

func (s *Session) bindGoalGrant(grant *GoalGrant, goal *agentstream.GoalState) error {
	s.goalMu.Lock()
	defer s.goalMu.Unlock()
	if s.goalGrant == nil { // A native clear can precede the RPC caller waking.
		return nil
	}
	bound := *grant
	bound.CreatedAt = goal.CreatedAt
	bound.Enabled = !goal.IsTerminal()
	if head := s.AgentTimeline().Head().Goal; head != nil && head.Goal != nil {
		latest := head.Goal
		if latest.ThreadID == goal.ThreadID && latest.Objective == goal.Objective && latest.CreatedAt == goal.CreatedAt && latest.IsTerminal() {
			bound.Enabled = false
		}
	}
	if s.persistGoalGrant != nil {
		if err := s.persistGoalGrant(&bound); err != nil {
			return err
		}
	}
	s.goalGrant = &bound
	return nil
}

func (s *Session) admitGoalTurn(request GoalTurnRequest) (GoalTurnBinding, error) {
	s.goalMu.Lock()
	allowed := s.goalGrant.matches(request.Goal)
	s.goalMu.Unlock()
	if !allowed || request.NativeTurnID == "" || request.Goal.Status != "active" {
		return GoalTurnBinding{}, errors.New("native continuation has no matching active goal grant")
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		s.queueMu.Lock()
		if len(s.queuePending) != 0 {
			s.queueMu.Unlock()
			return GoalTurnBinding{}, errors.New("user input takes priority over goal continuation")
		}
		if !s.queueBusy {
			break
		}
		if !s.queueGoal {
			s.queueMu.Unlock()
			return GoalTurnBinding{}, errors.New("user input takes priority over goal continuation")
		}
		idle := s.queueIdle
		s.queueMu.Unlock()
		if idle == nil {
			return GoalTurnBinding{}, ErrTurnSinkActive
		}
		select {
		case <-idle:
		case <-timer.C:
			return GoalTurnBinding{}, errors.New("goal continuation waited for an unfinished turn")
		}
	}
	s.goalMu.Lock()
	allowed = s.goalGrant.matches(request.Goal)
	s.goalMu.Unlock()
	if !allowed {
		s.queueMu.Unlock()
		return GoalTurnBinding{}, errors.New("goal grant was revoked while waiting")
	}
	s.queueBusy = true
	s.queueGoal = true
	s.queueIdle = make(chan struct{})
	done := make(chan struct{})
	turn, err := s.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{
		InputSummary: request.Goal.Objective,
		Origin:       agentstream.Origin{Actor: agentstream.ActorSystem, Channel: sourceframe.SourceGoal, NativeType: "goal_continuation", NativeID: request.NativeTurnID, LaunchID: s.currentLaunchID()},
	}, func(_ context.Context, turn *agentstream.Turn) error {
		s.turnMu.Lock()
		s.canonicalTurn = turn
		s.canonicalAuth = TurnAuthorization{}
		s.turnMu.Unlock()
		s.waitForCanonicalCompletion(turn, done)
		s.observeCanonicalEvent(turn.StartedEvent())
		return nil
	})
	s.queueMu.Unlock()
	if turn == nil {
		s.advanceInputQueue()
		return GoalTurnBinding{}, err
	}
	go func() { <-done; s.advanceInputQueue() }()
	if err != nil {
		return GoalTurnBinding{}, err
	}
	return GoalTurnBinding{TurnToken: turn.Ref().ID, Sink: func(emission agentstream.ProducerEmission) bool { return s.acceptProducerEmission(turn, emission) }}, nil
}

func (g *GoalGrant) matches(goal agentstream.GoalState) bool {
	return g != nil && g.Enabled && g.ThreadID == goal.ThreadID && g.Objective == goal.Objective && (g.CreatedAt == 0 || g.CreatedAt == goal.CreatedAt)
}

func (s *Session) observeGoalCompletion(goal *agentstream.GoalState) error {
	if goal == nil || !goal.IsTerminal() {
		return nil
	}
	s.goalMu.Lock()
	defer s.goalMu.Unlock()
	grant := s.goalGrant
	if grant == nil || grant.CreatedAt == 0 || !grant.matches(*goal) {
		return nil
	}
	sealed := *grant
	sealed.Enabled = false
	if s.persistGoalGrant != nil {
		if err := s.persistGoalGrant(&sealed); err != nil {
			return err
		}
	}
	s.goalGrant = &sealed
	return nil
}

func encodeGoalGrant(grant *GoalGrant) (string, error) {
	if grant == nil {
		return "", nil
	}
	encoded, err := json.Marshal(grant)
	return string(encoded), err
}

func (s *Session) resumeGoalAfterInput() {
	s.goalControlMu.Lock()
	defer s.goalControlMu.Unlock()
	s.queueMu.Lock()
	idle := !s.queueBusy && len(s.queuePending) == 0
	s.queueMu.Unlock()
	s.goalMu.Lock()
	grant := s.goalGrant
	s.goalMu.Unlock()
	if !idle || grant == nil || !grant.Enabled {
		return
	}
	s.runtimeMu.RLock()
	controller, ok := s.proc.(GoalController)
	s.runtimeMu.RUnlock()
	if !ok || !controller.GoalSupported() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	goal, err := controller.Goal(ctx, GoalRequest{Action: "get"})
	if err != nil || goal == nil || goal.Status != "paused" || goal.ThreadID != grant.ThreadID || goal.Objective != grant.Objective {
		return
	}
	s.queueMu.Lock()
	idle = !s.queueBusy && len(s.queuePending) == 0
	s.queueMu.Unlock()
	s.goalMu.Lock()
	valid := s.goalGrant == grant && grant.Enabled
	s.goalMu.Unlock()
	if idle && valid {
		_, _ = controller.Goal(ctx, GoalRequest{Action: "resume"})
	}
}
