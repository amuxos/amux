package mux

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

// brokerTurnClient owns correlated delivery and ACK/rebind ordering for both
// broker protocols. Metadata presentation stays in the concrete Proc adapters.
// All control writes share writeMu; sinks run outside eventMu so completion
// can re-enter the Proc without deadlocking.
type brokerTurnClient struct {
	protocol   brokerTurnWire
	pid        int
	conn       net.Conn
	socketPath string

	writeMu sync.Mutex
	ackCh   chan error
	done    chan struct{}

	eventMu                sync.Mutex
	eventSink              func(agentstream.ProducerEmission) bool
	pendingEvents          []agentstream.ProducerEmission
	capabilitiesReady      chan struct{}
	capabilitiesReadyOnce  sync.Once
	turnCorrelationVersion int
	turnReplayVersion      int
	turnCorrelation        bool
	turnSinks              map[string]*turnSinkEntry
}

type brokerTurnWire struct {
	name                      string
	dispatch, rebind, upgrade byte
}

var acpTurnWire = brokerTurnWire{"ACP", acpFrameTurnDispatch, acpFrameTurnRebind, acpFrameUpgrade}
var structuredTurnWire = brokerTurnWire{"structured", structuredFrameTurnDispatch, structuredFrameTurnRebind, structuredFrameUpgrade}

func (p *brokerTurnClient) turnCorrelationEnabled() bool {
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	return p.turnCorrelation
}

func (p *brokerTurnClient) DispatchTurn(ctx context.Context, req session.TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
	if strings.TrimSpace(req.TurnToken) == "" {
		return fmt.Errorf("turn token is required")
	}
	if sink == nil {
		return fmt.Errorf("turn event sink is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.turnCorrelationEnabled() {
		return fmt.Errorf("%s broker does not support correlated turns", p.protocol.name)
	}
	p.eventMu.Lock()
	entry, err := installTurnSink(&p.turnSinks, req.TurnToken, 0, sink)
	if err != nil {
		p.eventMu.Unlock()
		return err
	}
	p.eventMu.Unlock()
	payload, err := json.Marshal(brokerTurnDispatchFrame{TurnToken: req.TurnToken, Input: req.Input, Control: req.Control})
	if err == nil {
		p.writeMu.Lock()
		err = writeACPFrame(p.conn, p.protocol.dispatch, payload)
		if err == nil {
			err = waitBrokerAckContext(ctx, p.conn, p.ackCh, p.protocol.name+" broker")
		}
		p.writeMu.Unlock()
	}
	if err != nil {
		p.removeTurnSink(req.TurnToken, entry)
	}
	return err
}

func (p *brokerTurnClient) removeTurnSink(turnToken string, entry *turnSinkEntry) {
	p.eventMu.Lock()
	uninstallTurnSink(p.turnSinks, turnToken, entry)
	p.eventMu.Unlock()
}

// RebindTurn restores only the sequenced durability gap in a surviving broker.
// It installs the recovered sink before replay and never dispatches input again.
func (p *brokerTurnClient) RebindTurn(turnToken string, afterTransportSeq uint64, sink func(agentstream.ProducerEmission) bool) error {
	if strings.TrimSpace(turnToken) == "" {
		return fmt.Errorf("turn token is required")
	}
	if sink == nil {
		return fmt.Errorf("turn event sink is required")
	}
	p.eventMu.Lock()
	if !p.turnCorrelation {
		p.eventMu.Unlock()
		return fmt.Errorf("%s broker does not support correlated turns", p.protocol.name)
	}
	if p.turnReplayVersion < brokerTurnReplayV1 {
		p.eventMu.Unlock()
		return fmt.Errorf("%w: %s broker", session.ErrTurnReplayUnsupported, p.protocol.name)
	}
	entry, err := installTurnSink(&p.turnSinks, turnToken, afterTransportSeq, sink)
	if err != nil {
		p.eventMu.Unlock()
		return err
	}
	p.eventMu.Unlock()

	payload, err := json.Marshal(brokerTurnRebindFrame{TurnToken: turnToken, AfterSequence: afterTransportSeq})
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), brokerRebindAckWait)
		defer cancel()
		p.writeMu.Lock()
		err = writeACPFrame(p.conn, p.protocol.rebind, payload)
		if err == nil {
			err = waitBrokerAckContext(ctx, p.conn, p.ackCh, p.protocol.name+" broker")
		}
		p.writeMu.Unlock()
	}
	if err != nil {
		p.removeTurnSink(turnToken, entry)
	}
	return err
}

func (p *brokerTurnClient) negotiateTurnCorrelation(ctx context.Context) error {
	timer := time.NewTimer(brokerCapabilityWait)
	defer timer.Stop()
	select {
	case <-p.capabilitiesReady:
	case <-ctx.Done():
		_ = p.conn.Close()
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("%s broker does not advertise correlated turns", p.protocol.name)
	}
	p.eventMu.Lock()
	version := p.turnCorrelationVersion
	p.eventMu.Unlock()
	if version < brokerTurnProtocolV3 {
		return fmt.Errorf("%s broker turn correlation version %d is unsupported", p.protocol.name, version)
	}

	payload, err := json.Marshal(brokerUpgradeFrame{Version: brokerTurnProtocolV3})
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := writeACPFrame(p.conn, p.protocol.upgrade, payload); err != nil {
		return fmt.Errorf("upgrade %s broker turn correlation: %w", p.protocol.name, err)
	}
	ackCtx, cancel := context.WithTimeout(ctx, brokerUpgradeAckWait)
	defer cancel()
	if err := waitBrokerAckContext(ackCtx, p.conn, p.ackCh, p.protocol.name+" broker"); err != nil {
		return fmt.Errorf("upgrade %s broker turn correlation: %w", p.protocol.name, err)
	}
	p.eventMu.Lock()
	p.turnCorrelation = true
	p.eventMu.Unlock()
	return nil
}

func (p *brokerTurnClient) deliverCorrelated(frame brokerEmissionFrame) error {
	p.eventMu.Lock()
	entry, emission, err := routeBrokerEmission(p.turnSinks, frame, p.protocol.name)
	p.eventMu.Unlock()
	if err != nil {
		return err
	}
	entry.sink(emission)
	return nil
}
