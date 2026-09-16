package mux

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/amuxos/amux/internal/agentstream"
)

type StructuredBrokerProc struct {
	brokerTurnClient
	metadata              agentstream.ProducerEmission
	inputModalities       []string
	inputCapabilitySource string
}

type structuredCapabilitiesFrame struct {
	InputModalities        []string `json:"input_modalities"`
	Source                 string   `json:"source"`
	TurnCorrelationVersion int      `json:"turn_correlation_version,omitempty"`
	TurnReplayVersion      int      `json:"turn_replay_version,omitempty"`
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

func (p *StructuredBrokerProc) ReconfigureExecution(model, reasoningEffort string) error {
	payload, err := json.Marshal(structuredReconfigureFrame{Model: model, ReasoningEffort: reasoningEffort})
	if err != nil {
		return err
	}
	return p.writeControlFrame(structuredFrameReconfigure, payload)
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
			return fmt.Errorf("%s", string(ackPayload))
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
		case structuredFrameAck:
			var ackErr error
			if len(payload) > 0 {
				ackErr = fmt.Errorf("%s", string(payload))
			}
			p.ackCh <- ackErr
		}
	}
}
