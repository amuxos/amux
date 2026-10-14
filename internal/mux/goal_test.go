package mux

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

func TestBrokerUserInputCancelsPendingGoalOffer(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	owner := &brokerConn{conn: server, protocolV3: true}
	b := &structuredBroker{conns: []*brokerConn{owner}}
	goalDone := make(chan error, 1)
	go func() {
		_, err := b.admitGoalTurn(session.GoalTurnRequest{NativeTurnID: "native-goal", Goal: agentstream.GoalState{ThreadID: "native", Objective: "task", Status: "active"}})
		goalDone <- err
	}()
	kind, _, err := readACPFrame(client)
	if err != nil || kind != structuredFrameGoalOffer {
		t.Fatalf("offer kind=%d error=%v", kind, err)
	}
	payload, _ := json.Marshal(brokerTurnDispatchFrame{TurnToken: "user-turn", Input: session.TextUserInput("important follow-up")})
	var received session.TurnDispatchRequest
	reason := b.brokerTurns.dispatchCorrelatedTurn(owner, payload, func(_ context.Context, req session.TurnDispatchRequest, _ func(agentstream.ProducerEmission) bool) error {
		received = req
		return nil
	}, b.broadcastCorrelatedEvent)
	if reason != "" || received.TurnToken != "user-turn" || received.Input.DisplayText() != "important follow-up" {
		t.Fatalf("accepted input lost: dispatch=%+v reason=%q", received, reason)
	}
	select {
	case err := <-goalDone:
		if err == nil {
			t.Fatal("pending goal offer displaced user input")
		}
	case <-time.After(time.Second):
		t.Fatal("goal offer was not cancelled")
	}
	late, _ := json.Marshal(brokerGoalAdmission{NativeTurnID: "native-goal", TurnToken: "late-goal-token"})
	go b.handleGoalAdmission(owner, late)
	kind, _, err = readACPFrame(client)
	if err != nil || kind != structuredFrameGoalRejected {
		t.Fatalf("late admission not rejected: kind=%d error=%v", kind, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.turnAdmission.turnToken != "user-turn" {
		t.Fatal("late goal admission replaced the user turn")
	}
}

func TestBrokerNativeGoalAdmissionAndCorrelatedReplay(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	owner := &brokerConn{conn: server, protocolV3: true}
	b := &structuredBroker{conns: []*brokerConn{owner}}
	proc := &StructuredBrokerProc{brokerTurnClient: brokerTurnClient{protocol: structuredTurnWire, conn: client, ackCh: make(chan error, 1), done: make(chan struct{}), turnSinks: make(map[string]*turnSinkEntry)}}
	events := make(chan agentstream.ProducerEmission, 4)
	proc.SetGoalTurnAdmitter(func(req session.GoalTurnRequest) (session.GoalTurnBinding, error) {
		if req.NativeTurnID != "native-turn" || req.Goal.Objective != "task" {
			t.Errorf("wrong offer %+v", req)
		}
		return session.GoalTurnBinding{TurnToken: "canonical-turn", Sink: func(e agentstream.ProducerEmission) bool { events <- e; return false }}, nil
	})
	go proc.readLoop()
	go b.handleConn(owner)
	binding, err := b.admitGoalTurn(session.GoalTurnRequest{NativeTurnID: "native-turn", Goal: agentstream.GoalState{ThreadID: "native-thread", Objective: "task", Status: "active"}})
	if err != nil || binding.TurnToken != "canonical-turn" {
		t.Fatalf("binding=%+v %v", binding, err)
	}
	binding.Sink(agentstream.ProducerEmission{Turn: agentstream.AssistantDelta{Text: "progress"}})
	binding.Sink(agentstream.ProducerEmission{Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}})
	for n := 0; n < 2; n++ {
		select {
		case <-events:
		case <-time.After(time.Second):
			t.Fatal("goal output was not correlated")
		}
	}
	b.mu.Lock()
	busy := b.turnAdmission.turnToken != ""
	b.mu.Unlock()
	if busy {
		t.Fatal("goal terminal did not release admission")
	}
	b.broadcastMu.Lock()
	frames, terminal, err := b.turnReplay.after("canonical-turn", 0)
	b.broadcastMu.Unlock()
	if err != nil || !terminal || len(frames) != 2 {
		t.Fatalf("replay frames=%d terminal=%v %v", len(frames), terminal, err)
	}
	var frame brokerEmissionFrame
	if json.Unmarshal(frames[1], &frame) != nil || frame.TurnToken != "canonical-turn" || frame.Sequence != 2 {
		t.Fatalf("wrong replay identity: %+v", frame)
	}
}

func TestBrokerGoalRequiresOneOwnerAndNoOtherAdmission(t *testing.T) {
	request := session.GoalTurnRequest{NativeTurnID: "native", Goal: agentstream.GoalState{Status: "active"}}
	for _, b := range []*structuredBroker{{}, {conns: []*brokerConn{{protocolV3: true}, {protocolV3: true}}}, {brokerTurns: brokerTurns{goalOffering: true}, goalOffer: &brokerGoalOffer{}}} {
		if _, err := b.admitGoalTurn(request); err == nil {
			t.Fatal("unowned/ambiguous/busy continuation accepted")
		}
	}
}
