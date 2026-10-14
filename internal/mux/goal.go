package mux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

type brokerGoalResult struct {
	Goal  *agentstream.GoalState `json:"goal"`
	Error string                 `json:"error,omitempty"`
}

type brokerGoalAdmission struct {
	NativeTurnID string `json:"native_turn_id"`
	TurnToken    string `json:"turn_token,omitempty"`
	Error        string `json:"error,omitempty"`
}

type brokerGoalOffer struct {
	request session.GoalTurnRequest
	owner   *brokerConn
	done    chan brokerGoalAdmission
	expired bool
}

func (p *StructuredBrokerProc) GoalSupported() bool {
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	return p.goalSupported
}

func (p *StructuredBrokerProc) SetGoalTurnAdmitter(fn session.GoalTurnAdmitter) {
	p.eventMu.Lock()
	p.goalAdmitter = fn
	p.eventMu.Unlock()
}

func (p *StructuredBrokerProc) EnableGoalTurns() {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := writeBrokerFrame(p.conn, structuredFrameGoalReady, nil); err != nil {
		_ = p.conn.Close()
	}
}

func (p *StructuredBrokerProc) Goal(ctx context.Context, request session.GoalRequest) (*agentstream.GoalState, error) {
	if !p.GoalSupported() {
		return nil, session.ErrGoalUnsupported
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", p.socketPath)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if err := writeACPFrame(conn, structuredFrameGoal, payload); err != nil {
		return nil, err
	}
	for {
		kind, payload, err := readACPFrame(conn)
		if err != nil {
			return nil, err
		}
		if kind != structuredFrameGoalResult {
			continue
		}
		var result brokerGoalResult
		if err := json.Unmarshal(payload, &result); err != nil {
			return nil, err
		}
		if result.Error != "" {
			return result.Goal, errors.New(result.Error)
		}
		return result.Goal, nil
	}
}

func (b *structuredBroker) handleGoalControl(conn *brokerConn, payload []byte) {
	var request session.GoalRequest
	var result brokerGoalResult
	controller, supported := b.proc.(session.GoalController)
	if err := json.Unmarshal(payload, &request); err != nil {
		result.Error = err.Error()
	} else if !supported || !controller.GoalSupported() {
		result.Error = session.ErrGoalUnsupported.Error()
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		goal, err := controller.Goal(ctx, request)
		cancel()
		result.Goal = goal
		if err != nil {
			result.Error = err.Error()
		}
	}
	encoded, _ := json.Marshal(result)
	_ = conn.write(structuredFrameGoalResult, encoded)
}

func (b *structuredBroker) admitGoalTurn(request session.GoalTurnRequest) (session.GoalTurnBinding, error) {
	b.mu.Lock()
	if b.goalOffer != nil || b.turnAdmission.turnToken != "" {
		b.mu.Unlock()
		return session.GoalTurnBinding{}, errors.New("broker turn busy")
	}
	var owner *brokerConn
	for _, conn := range b.conns {
		if !conn.protocolV3 {
			continue
		}
		if owner != nil {
			b.mu.Unlock()
			return session.GoalTurnBinding{}, errors.New("ambiguous runtime goal owner")
		}
		owner = conn
	}
	if owner == nil {
		b.mu.Unlock()
		return session.GoalTurnBinding{}, errors.New("no runtime goal owner")
	}
	offer := &brokerGoalOffer{request: request, owner: owner, done: make(chan brokerGoalAdmission, 1)}
	b.goalOffer = offer
	b.goalOffering = true
	b.cancelGoalOffer = func() {
		offer.expired = true
		select {
		case offer.done <- brokerGoalAdmission{NativeTurnID: request.NativeTurnID, Error: "user input takes priority over goal continuation"}:
		default:
		}
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.goalOffer == offer {
			b.goalOffer = nil
			b.goalOffering = false
			b.cancelGoalOffer = nil
		}
		b.mu.Unlock()
	}()
	encoded, err := json.Marshal(request)
	if err != nil {
		return session.GoalTurnBinding{}, err
	}
	if err := owner.write(structuredFrameGoalOffer, encoded); err != nil {
		return session.GoalTurnBinding{}, err
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	accept := func(result brokerGoalAdmission) (session.GoalTurnBinding, error) {
		if result.Error != "" {
			return session.GoalTurnBinding{}, errors.New(result.Error)
		}
		return session.GoalTurnBinding{TurnToken: result.TurnToken, Sink: func(emission agentstream.ProducerEmission) bool {
			b.broadcastCorrelatedEvent(result.TurnToken, emission)
			return false
		}}, nil
	}
	select {
	case result := <-offer.done:
		return accept(result)
	case <-timer.C:
		b.mu.Lock()
		select {
		case result := <-offer.done:
			b.mu.Unlock()
			return accept(result)
		default:
			offer.expired = true
			b.mu.Unlock()
		}
		return session.GoalTurnBinding{}, errors.New("runtime goal admission timed out")
	}
}

func (b *structuredBroker) handleGoalAdmission(conn *brokerConn, payload []byte) {
	var result brokerGoalAdmission
	if json.Unmarshal(payload, &result) != nil {
		return
	}
	b.broadcastMu.Lock()
	b.mu.Lock()
	offer := b.goalOffer
	if offer == nil || offer.expired || offer.owner != conn || offer.request.NativeTurnID != result.NativeTurnID {
		result.Error = "goal admission offer expired"
	} else if result.Error == "" {
		if result.TurnToken == "" {
			result.Error = "goal turn token is required"
		} else {
			result.Error = b.turnAdmission.reserveCorrelated(conn, result.TurnToken)
		}
		if result.Error == "" {
			b.turnReplay.begin(result.TurnToken)
		}
	}
	if offer != nil && offer.owner == conn && offer.request.NativeTurnID == result.NativeTurnID {
		select {
		case offer.done <- result:
		default:
		}
	}
	b.mu.Unlock()
	b.broadcastMu.Unlock()
	if result.Error != "" && result.TurnToken != "" {
		encoded, _ := json.Marshal(result)
		_ = conn.write(structuredFrameGoalRejected, encoded)
	}
}

func (p *StructuredBrokerProc) handleGoalOffer(payload []byte) {
	var request session.GoalTurnRequest
	if json.Unmarshal(payload, &request) != nil {
		return
	}
	p.eventMu.Lock()
	admit := p.goalAdmitter
	p.eventMu.Unlock()
	result := brokerGoalAdmission{NativeTurnID: request.NativeTurnID}
	var binding session.GoalTurnBinding
	if admit == nil {
		result.Error = "runtime goal admission is unavailable"
	} else {
		var err error
		binding, err = admit(request)
		if err != nil {
			result.Error = err.Error()
		} else if binding.TurnToken == "" || binding.Sink == nil {
			result.Error = "invalid runtime goal admission"
		} else {
			p.eventMu.Lock()
			_, err = installTurnSink(&p.turnSinks, binding.TurnToken, 0, binding.Sink)
			p.eventMu.Unlock()
			if err != nil {
				result.Error = err.Error()
				binding.Sink(agentstream.ProducerEmission{Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: "goal_sink_install_failed", Message: err.Error()}})
			} else {
				result.TurnToken = binding.TurnToken
			}
		}
	}
	encoded, _ := json.Marshal(result)
	p.writeMu.Lock()
	err := writeBrokerFrame(p.conn, structuredFrameGoalAdmission, encoded)
	p.writeMu.Unlock()
	if err != nil && result.TurnToken != "" {
		p.rejectGoalTurn(result.TurnToken, err.Error())
	}
}

func (p *StructuredBrokerProc) rejectGoalTurn(token, reason string) {
	p.eventMu.Lock()
	entry := p.turnSinks[token]
	delete(p.turnSinks, token)
	p.eventMu.Unlock()
	if entry != nil {
		entry.sink(agentstream.ProducerEmission{Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "goal_admission_rejected", Message: fmt.Sprint(reason)}})
	}
}
