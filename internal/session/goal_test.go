package session

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/store"
)

type nativeGoalRecordingProc struct {
	recordingProc
	goalLock sync.Mutex
	goal     *agentstream.GoalState
	resumes  int
}

func TestGoalOfferDoesNotWaitForOrdinaryInputToFinish(t *testing.T) {
	s := &Session{ID: "session", AgentSessionID: "native"}
	p := &nativeGoalRecordingProc{}
	s.AgentSessionID = "native"
	s.SetProc(p)
	if _, err := s.Goal(context.Background(), GoalRequest{Action: "create", Objective: "task"}); err != nil {
		t.Fatal(err)
	}
	s.queueMu.Lock()
	s.queueBusy = true
	s.queueIdle = make(chan struct{})
	s.queueMu.Unlock()
	started := time.Now()
	_, err := s.admitGoalTurn(GoalTurnRequest{NativeTurnID: "native-turn", Goal: *p.goal.Clone()})
	if err == nil || time.Since(started) >= time.Second {
		t.Fatalf("goal waited for or overtook ordinary input: %v", err)
	}
	if s.AgentTimeline().ActiveTurn() != nil {
		t.Fatal("rejected offer created a canonical turn")
	}
}

func (p *nativeGoalRecordingProc) GoalSupported() bool { return true }
func (p *nativeGoalRecordingProc) Goal(_ context.Context, r GoalRequest) (*agentstream.GoalState, error) {
	p.goalLock.Lock()
	defer p.goalLock.Unlock()
	switch r.Action {
	case "create", "set":
		p.goal = &agentstream.GoalState{ThreadID: "native", Objective: r.Objective, Status: "active", TokensUsed: 19}
	case "pause":
		p.goal.Status = "paused"
	case "resume":
		p.goal.Status = "active"
		p.resumes++
	case "clear":
		p.goal = nil
	case "budget":
		p.goal.TokenBudget = r.TokenBudget
	}
	return p.goal.Clone(), nil
}

func TestGoalGrantAdmissionPersistenceAndRevocation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := NewManager(db)
	s, err := manager.Create("goal-session", "codex")
	if err != nil {
		t.Fatal(err)
	}
	p := &nativeGoalRecordingProc{}
	s.SetProc(p)
	s.recordProducerEmission(agentstream.ProducerEmission{Session: agentstream.SessionMetadataChanged{AgentSessionID: "native"}})
	ctx := context.Background()
	if _, err := s.Goal(ctx, GoalRequest{Action: "create", Objective: "task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Goal(ctx, GoalRequest{Action: "create", Objective: "replacement"}); err == nil {
		t.Fatal("unfinished goal replaced implicitly")
	}
	restored := NewManager(db)
	if err := restored.LoadFromDB(); err != nil {
		t.Fatal(err)
	}
	resumed, err := restored.Get("goal-session")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.goalGrant == nil || !resumed.goalGrant.Enabled || resumed.goalGrant.ThreadID != "native" {
		t.Fatalf("grant not restored %+v", resumed.goalGrant)
	}
	resumed.SetProc(p)
	request := GoalTurnRequest{NativeTurnID: "native-one", Goal: *p.goal.Clone()}
	binding, err := resumed.admitGoalTurn(request)
	if err != nil {
		t.Fatal(err)
	}
	if binding.TurnToken == "" {
		t.Fatal("missing canonical identity")
	}
	if resumed.canonicalAuth.LocalExpertAuthoring {
		t.Fatal("goal inherited authoring authority")
	}
	binding.Sink(agentstream.ProducerEmission{Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}})
	if _, err := resumed.Goal(ctx, GoalRequest{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	request.NativeTurnID = "native-two"
	if _, err := resumed.admitGoalTurn(request); err == nil {
		t.Fatal("paused grant admitted continuation")
	}
	budget := int64(1000)
	if _, err := resumed.Goal(ctx, GoalRequest{Action: "budget", TokenBudget: &budget}); err != nil {
		t.Fatal(err)
	}
	goal, err := resumed.Goal(ctx, GoalRequest{Action: "resume"})
	if err != nil || goal.TokensUsed != 19 || goal.TokenBudget == nil || *goal.TokenBudget != budget {
		t.Fatalf("accounting changed: %+v %v", goal, err)
	}
	if _, err := resumed.Goal(ctx, GoalRequest{Action: "clear"}); err != nil {
		t.Fatal(err)
	}
	if value, err := db.SessionGoalGrant(s.ID); err != nil || value != "" {
		t.Fatalf("clear left grant %q %v", value, err)
	}
}

func TestGoalOrdinaryInputCanResumeButExplicitPauseCannot(t *testing.T) {
	s := &Session{ID: "session", AgentSessionID: "native"}
	p := &nativeGoalRecordingProc{}
	s.SetProc(p)
	ctx := context.Background()
	if _, err := s.Goal(ctx, GoalRequest{Action: "create", Objective: "task"}); err != nil {
		t.Fatal(err)
	}
	p.goalLock.Lock()
	p.goal.Status = "paused"
	p.goalLock.Unlock() // Native preemption.
	s.resumeGoalAfterInput()
	if p.resumes != 1 {
		t.Fatal("authorized goal did not resume after input")
	}
	if err := s.Interrupt(); err != nil {
		t.Fatal(err)
	}
	s.resumeGoalAfterInput()
	if p.resumes != 1 || s.goalGrant.Enabled {
		t.Fatal("explicit interrupt resumed goal")
	}
}

func TestGoalCompletionRevokesContinuationGrant(t *testing.T) {
	s := &Session{ID: "session", AgentSessionID: "native"}
	p := &nativeGoalRecordingProc{}
	s.AgentSessionID = "native"
	s.SetProc(p)
	if _, err := s.Goal(context.Background(), GoalRequest{Action: "create", Objective: "task"}); err != nil {
		t.Fatal(err)
	}
	goal := p.goal.Clone()
	goal.CreatedAt = 100
	if err := s.bindGoalGrant(s.goalGrant, goal); err != nil {
		t.Fatal(err)
	}
	goal.Status = "complete"
	s.recordProducerEmission(agentstream.ProducerEmission{Session: agentstream.GoalChanged{Supported: true, Goal: goal}, Origin: agentstream.Origin{NativeType: "thread/goal/updated"}})
	goal.Status = "active"
	goal.CreatedAt = 101
	if _, err := s.admitGoalTurn(GoalTurnRequest{NativeTurnID: "new-native-turn", Goal: *goal}); err == nil {
		t.Fatal("completed goal authorized a new goal with the same objective")
	}
}

func TestMaintenanceBlocksNativeContinuationsWithoutRevokingGrant(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := NewManager(db)
	s, err := mgr.Create("goal-maintenance", "codex")
	if err != nil {
		t.Fatal(err)
	}
	p := &nativeGoalRecordingProc{}
	s.AgentSessionID = "native"
	s.SetProc(p)
	if _, err := s.Goal(context.Background(), GoalRequest{Action: "create", Objective: "work"}); err != nil {
		t.Fatal(err)
	}
	mgr.SetMaintenance(true)
	if _, err := s.admitGoalTurn(GoalTurnRequest{NativeTurnID: "next", Goal: *p.goal.Clone()}); err == nil {
		t.Fatal("continuation admitted")
	}
	if s.goalGrant == nil || !s.goalGrant.Enabled {
		t.Fatal("goal grant revoked")
	}
	mgr.SetMaintenance(false)
	binding, err := s.admitGoalTurn(GoalTurnRequest{NativeTurnID: "next", Goal: *p.goal.Clone()})
	if err != nil {
		t.Fatal(err)
	}
	binding.Sink(agentstream.ProducerEmission{Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}})
}
