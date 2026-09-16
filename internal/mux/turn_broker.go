package mux

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

// brokerTurns owns admission and transport replay. broadcastMu serializes
// dispatch, replay and live delivery; mu protects lifecycle state and is never
// held during socket writes. Neither lock owns canonical Session turn policy.
type brokerTurns struct {
	mu            sync.Mutex
	broadcastMu   sync.Mutex
	turnAdmission brokerTurnAdmission[*brokerConn]
	turnReplay    brokerTurnReplay
}

type brokerConn struct {
	conn       net.Conn
	mu         sync.Mutex
	protocolV3 bool
}

func (c *brokerConn) write(kind byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeBrokerFrame(c.conn, kind, payload)
}

func (b *brokerTurns) dispatchCorrelatedTurn(conn *brokerConn, payload []byte, dispatch func(context.Context, session.TurnDispatchRequest, func(agentstream.ProducerEmission) bool) error, emit func(string, agentstream.ProducerEmission)) string {
	var req brokerTurnDispatchFrame
	if err := json.Unmarshal(payload, &req); err != nil {
		return err.Error()
	}
	if strings.TrimSpace(req.TurnToken) == "" {
		return "turn token is required"
	}
	b.broadcastMu.Lock()
	b.mu.Lock()
	if !conn.protocolV3 {
		b.mu.Unlock()
		b.broadcastMu.Unlock()
		return "turn dispatch requires protocol v3"
	}
	if reason := b.turnAdmission.reserveCorrelated(conn, req.TurnToken); reason != "" {
		b.mu.Unlock()
		b.broadcastMu.Unlock()
		return reason
	}
	b.turnReplay.begin(req.TurnToken)
	b.mu.Unlock()
	b.broadcastMu.Unlock()
	err := dispatch(context.Background(), session.TurnDispatchRequest{
		TurnToken: req.TurnToken,
		Input:     req.Input,
		Control:   req.Control,
	}, func(ev agentstream.ProducerEmission) bool {
		emit(req.TurnToken, ev)
		return false
	})
	if err == nil {
		return ""
	}
	b.clearCorrelatedAdmission(conn, req.TurnToken)
	return err.Error()
}

func (b *brokerTurns) clearCorrelatedAdmission(conn *brokerConn, turnToken string) {
	b.broadcastMu.Lock()
	defer b.broadcastMu.Unlock()
	b.mu.Lock()
	b.turnAdmission.clearCorrelated(conn, turnToken)
	b.mu.Unlock()
	b.turnReplay.discard(turnToken)
}

func (b *brokerTurns) rebindCorrelatedTurn(conn *brokerConn, payload []byte, ackKind, emissionKind byte) {
	b.broadcastMu.Lock()
	defer b.broadcastMu.Unlock()

	var req brokerTurnRebindFrame
	if err := json.Unmarshal(payload, &req); err != nil {
		_ = conn.write(ackKind, []byte(err.Error()))
		return
	}
	if strings.TrimSpace(req.TurnToken) == "" {
		_ = conn.write(ackKind, []byte("turn token is required"))
		return
	}
	b.mu.Lock()
	protocolV3 := conn.protocolV3
	b.mu.Unlock()
	if !protocolV3 {
		_ = conn.write(ackKind, []byte("turn rebind requires protocol v3"))
		return
	}
	frames, terminal, err := b.turnReplay.after(req.TurnToken, req.AfterSequence)
	if err == nil && !terminal {
		b.mu.Lock()
		reason := b.turnAdmission.rebindCorrelated(conn, req.TurnToken)
		b.mu.Unlock()
		if reason != "" {
			err = errors.New(reason)
		}
	}
	if err != nil {
		_ = conn.write(ackKind, []byte(err.Error()))
		return
	}
	for _, frame := range frames {
		if err := conn.write(emissionKind, frame); err != nil {
			_ = conn.conn.Close()
			return
		}
	}
	_ = conn.write(ackKind, nil)
}

func (b *brokerTurns) broadcastCorrelatedEventLocked(turnToken string, ev agentstream.ProducerEmission, emissionKind byte, remove func(*brokerConn)) {
	payload, err := b.turnReplay.append(turnToken, ev)
	if err != nil {
		return
	}

	b.mu.Lock()
	var owner *brokerConn
	if b.turnAdmission.turnToken == turnToken {
		owner = b.turnAdmission.owner
	}
	// append may replace an unencodable provider observation with an explicit
	// terminal frame. Release admission according to the exact buffered frame,
	// not the original event that could not cross the broker wire.
	b.turnAdmission.finishEmission(turnToken, b.turnReplay.terminal)
	b.mu.Unlock()
	if owner == nil {
		return
	}
	if err := owner.write(emissionKind, payload); err != nil {
		_ = owner.conn.Close()
		remove(owner)
	}
}
