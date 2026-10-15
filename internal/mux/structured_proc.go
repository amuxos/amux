package mux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

type StructuredBrokerProc struct {
	brokerTurnClient
	metadata              agentstream.ProducerEmission
	inputModalities       []string
	inputCapabilitySource string
	goalSupported         bool
	goalAdmitter          session.GoalTurnAdmitter
}

type structuredCapabilitiesFrame struct {
	InputModalities        []string `json:"input_modalities"`
	Source                 string   `json:"source"`
	TurnCorrelationVersion int      `json:"turn_correlation_version,omitempty"`
	TurnReplayVersion      int      `json:"turn_replay_version,omitempty"`
	GoalSupported          bool     `json:"goal_supported,omitempty"`
	TurnSteerVersion       int      `json:"turn_steer_version,omitempty"`
}

func (p *StructuredBrokerProc) PID() int { return p.pid }

func (p *StructuredBrokerProc) InputCapabilities() ([]string, string) {
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	return append([]string(nil), p.inputModalities...), p.inputCapabilitySource
}

func (p *StructuredBrokerProc) Interrupt() error {
	return p.writeControlFrame(structuredFrameInterrupt, nil)
}

// SteerTurn injects input into the broker's active correlated turn. Brokers
// started by an older amuxlet do not advertise steering and are reported as
// unavailable, so the runtime queues the input instead.
func (p *StructuredBrokerProc) SteerTurn(_ context.Context, req session.TurnSteerRequest) error {
	p.eventMu.Lock()
	version := p.turnSteerVersion
	_, bound := p.turnSinks[req.TurnToken]
	p.eventMu.Unlock()
	if version < brokerTurnSteerV1 {
		return fmt.Errorf("%w: structured broker does not support steering", session.ErrSteerUnavailable)
	}
	if !bound {
		return fmt.Errorf("%w: turn %q is not bound to this broker", session.ErrSteerUnavailable, req.TurnToken)
	}
	payload, err := json.Marshal(brokerTurnSteerFrame{TurnToken: req.TurnToken, Input: req.Input})
	if err != nil {
		return err
	}
	err = p.writeControlFrame(structuredFrameTurnSteer, payload)
	if err == nil {
		return nil
	}
	var ack brokerAckError
	if errors.As(err, &ack) && !strings.HasPrefix(string(ack), brokerSteerOutcomeUnknownPrefix) {
		return fmt.Errorf("%w: %v", session.ErrSteerUnavailable, err)
	}
	// The broker may have steered the turn before the ack was lost, or it
	// reported an unknown provider outcome itself.
	return fmt.Errorf("%w: %v", session.ErrSteerOutcomeUnknown, err)
}

// brokerAckError is an error the broker reported in its ack frame, as opposed
// to a transport failure on the way to or from the broker.
type brokerAckError string

func (e brokerAckError) Error() string { return string(e) }

func (p *StructuredBrokerProc) ReconfigureExecution(model, reasoningEffort string) error {
	payload, err := json.Marshal(structuredReconfigureFrame{Model: model, ReasoningEffort: reasoningEffort})
	if err != nil {
		return err
	}
	return p.writeControlFrame(structuredFrameReconfigure, payload)
}

func (p *StructuredBrokerProc) CancelUserInput(permissionID string) error {
	payload, err := json.Marshal(structuredPermissionResponseFrame{PermissionId: permissionID, Cancel: true})
	if err != nil {
		return err
	}
	return p.writeControlFrame(structuredFramePermissionResp, payload)
}

func (p *StructuredBrokerProc) RespondPermission(permissionID, optionID string) error {
	payload, err := json.Marshal(structuredPermissionResponseFrame{PermissionId: permissionID, OptionId: optionID})
	if err != nil {
		return err
	}
	return p.writeControlFrame(structuredFramePermissionResp, payload)
}

func (p *StructuredBrokerProc) RespondPermissionMulti(permissionID string, optionIDs []string) error {
	payload, err := json.Marshal(structuredPermissionResponseFrame{PermissionId: permissionID, OptionIds: optionIDs})
	if err != nil {
		return err
	}
	return p.writeControlFrame(structuredFramePermissionResp, payload)
}

func (p *StructuredBrokerProc) writeControlFrame(kind byte, payload []byte) error {
	if p.socketPath == "" {
		p.writeMu.Lock()
		defer p.writeMu.Unlock()
		if err := writeACPFrame(p.conn, kind, payload); err != nil {
			return err
		}
		return p.waitAck()
	}
	conn, err := net.Dial("unix", p.socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := writeACPFrame(conn, kind, payload); err != nil {
		return err
	}
	for {
		ackKind, ackPayload, err := readACPFrame(conn)
		if err != nil {
			return err
		}
		if ackKind != structuredFrameAck {
			continue
		}
		if len(ackPayload) > 0 {
			return brokerAckError(ackPayload)
		}
		return nil
	}
}

func (p *StructuredBrokerProc) SetEventSink(fn func(agentstream.ProducerEmission) bool) {
	p.eventMu.Lock()
	if fn == nil {
		p.eventSink = nil
		p.pendingEvents = nil
		p.eventMu.Unlock()
		return
	}
	replayMetadata := p.metadata.Session != nil
	for _, ev := range p.pendingEvents {
		if ev.Session != nil {
			if _, ok := ev.Session.(agentstream.SessionMetadataChanged); !ok {
				continue
			}
			replayMetadata = false
			break
		}
	}
	p.eventSink = nil
	for {
		pending := append([]agentstream.ProducerEmission(nil), p.pendingEvents...)
		p.pendingEvents = nil
		if replayMetadata {
			pending = append([]agentstream.ProducerEmission{p.metadata}, pending...)
			replayMetadata = false
		}
		if len(pending) == 0 {
			p.eventSink = fn
			p.eventMu.Unlock()
			return
		}
		p.eventMu.Unlock()
		for _, ev := range pending {
			fn(ev)
		}
		p.eventMu.Lock()
	}
}

func (p *StructuredBrokerProc) emitEvent(ev agentstream.ProducerEmission) {
	p.eventMu.Lock()
	if goal, ok := ev.Session.(agentstream.GoalChanged); ok {
		p.goalSupported = goal.Supported
	}
	if _, ok := ev.Session.(agentstream.SessionMetadataChanged); ok {
		p.metadata = ev
	}
	sink := p.eventSink
	if sink == nil {
		p.pendingEvents = append(p.pendingEvents, ev)
		p.eventMu.Unlock()
		return
	}
	p.eventMu.Unlock()
	sink(ev)
}

func (p *StructuredBrokerProc) emitCorrelated(emission brokerEmissionFrame) error {
	if emission.TurnToken == "" {
		if emission.Sequence != 0 {
			return fmt.Errorf("uncorrelated structured emission has sequence %d", emission.Sequence)
		}
		decoded, err := agentstream.UnmarshalProducerEmission(emission.Emission)
		if err != nil {
			return err
		}
		p.emitEvent(decoded)
		return nil
	}
	return p.deliverCorrelated(emission)
}

func (p *StructuredBrokerProc) Close() error {
	var firstErr error
	if p.socketPath != "" {
		firstErr = ShutdownStructuredBroker(p.socketPath)
	}
	if err := p.conn.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (p *StructuredBrokerProc) waitAck() error {
	err, ok := <-p.ackCh
	if !ok {
		return fmt.Errorf("structured broker disconnected")
	}
	return err
}

func (p *StructuredBrokerProc) readLoop() {
	defer close(p.done)
	defer close(p.ackCh)
	for {
		kind, payload, err := readACPFrame(p.conn)
		if err != nil {
			return
		}
		switch kind {
		case structuredFrameCapabilities:
			var caps structuredCapabilitiesFrame
			if json.Unmarshal(payload, &caps) != nil {
				continue
			}
			p.eventMu.Lock()
			p.inputModalities = append([]string(nil), caps.InputModalities...)
			p.inputCapabilitySource = caps.Source
			p.turnCorrelationVersion = caps.TurnCorrelationVersion
			p.turnReplayVersion = caps.TurnReplayVersion
			p.goalSupported = caps.GoalSupported
			p.turnSteerVersion = caps.TurnSteerVersion
			p.eventMu.Unlock()
			p.capabilitiesReadyOnce.Do(func() {
				if p.capabilitiesReady != nil {
					close(p.capabilitiesReady)
				}
			})
		case structuredFrameEmission:
			var emission brokerEmissionFrame
			if json.Unmarshal(payload, &emission) != nil {
				_ = p.conn.Close()
				return
			}
			if err := p.emitCorrelated(emission); err != nil {
				_ = p.conn.Close()
				return
			}
		case structuredFrameGoalOffer:
			go p.handleGoalOffer(payload)
		case structuredFrameGoalRejected:
			var result brokerGoalAdmission
			if json.Unmarshal(payload, &result) == nil {
				p.rejectGoalTurn(result.TurnToken, result.Error)
			}
		case structuredFrameAck:
			var ackErr error
			if len(payload) > 0 {
				ackErr = brokerAckError(payload)
			}
			p.ackCh <- ackErr
		}
	}
}
