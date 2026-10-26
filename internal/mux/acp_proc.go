package mux

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/amuxos/amux/internal/acp"
	"github.com/amuxos/amux/internal/agentstream"
)

type ACPBrokerProc struct {
	brokerTurnClient
	inputModalities       []string
	inputCapabilitySource string
	executionReconfigure  bool
}

type acpCapabilitiesFrame struct {
	brokerCapabilitiesFrame
	InputModalities      []string `json:"input_modalities,omitempty"`
	Source               string   `json:"source,omitempty"`
	ExecutionReconfigure bool     `json:"execution_reconfigure,omitempty"`
}

func (p *ACPBrokerProc) PID() int { return p.pid }

func (p *ACPBrokerProc) InputCapabilities() ([]string, string) {
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	return append([]string(nil), p.inputModalities...), p.inputCapabilitySource
}

func (p *ACPBrokerProc) ReconfigureExecution(model, reasoningEffort string) error {
	p.eventMu.Lock()
	supported := p.executionReconfigure
	p.eventMu.Unlock()
	if !supported {
		return acp.ErrExecutionConfigurationUnsupported
	}
	payload, err := json.Marshal(acpExecutionConfigFrame{Model: model, ReasoningEffort: reasoningEffort})
	if err != nil {
		return err
	}
	return p.writeControlFrame(acpFrameReconfigure, payload)
}

func (p *ACPBrokerProc) Interrupt() error {
	return p.writeControlFrame(acpFrameInterrupt, nil)
}

func (p *ACPBrokerProc) RespondPermission(permissionID, optionID string) error {
	payload, err := json.Marshal(acpPermissionResponseFrame{PermissionId: permissionID, OptionId: optionID})
	if err != nil {
		return err
	}
	return p.writeControlFrame(acpFramePermissionResp, payload)
}

func (p *ACPBrokerProc) RespondPermissionMulti(permissionID string, optionIDs []string) error {
	payload, err := json.Marshal(acpPermissionResponseFrame{PermissionId: permissionID, OptionIds: optionIDs})
	if err != nil {
		return err
	}
	return p.writeControlFrame(acpFramePermissionResp, payload)
}

func (p *ACPBrokerProc) writeControlFrame(kind byte, payload []byte) error {
	if p.socketPath == "" {
		p.writeMu.Lock()
		defer p.writeMu.Unlock()
		if err := writeACPFrame(p.conn, kind, payload); err != nil {
			return err
		}
		return p.waitAck()
	}

	conn, err := net.DialTimeout("unix", p.socketPath, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	timeout := 5 * time.Second
	if kind == acpFrameReconfigure {
		timeout = 45 * time.Second
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := writeACPFrame(conn, kind, payload); err != nil {
		return err
	}
	for {
		ackKind, ackPayload, err := readACPFrame(conn)
		if err != nil {
			return err
		}
		if ackKind != acpFrameAck {
			continue
		}
		if len(ackPayload) > 0 {
			return fmt.Errorf("%s", string(ackPayload))
		}
		return nil
	}
}

func (p *ACPBrokerProc) SetEventSink(fn func(agentstream.ProducerEmission) bool) {
	p.eventMu.Lock()
	if fn == nil {
		p.eventSink = nil
		p.pendingEvents = nil
		p.eventMu.Unlock()
		return
	}
	p.eventSink = nil
	for {
		pending := append([]agentstream.ProducerEmission(nil), p.pendingEvents...)
		p.pendingEvents = nil
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

func (p *ACPBrokerProc) emitEvent(ev agentstream.ProducerEmission) {
	p.eventMu.Lock()
	sink := p.eventSink
	if sink == nil {
		p.pendingEvents = append(p.pendingEvents, ev)
		p.eventMu.Unlock()
		return
	}
	p.eventMu.Unlock()
	sink(ev)
}

func (p *ACPBrokerProc) emitCorrelated(emission brokerEmissionFrame) error {
	if emission.TurnToken == "" {
		if emission.Sequence != 0 {
			return fmt.Errorf("uncorrelated ACP emission has sequence %d", emission.Sequence)
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

func (p *ACPBrokerProc) Close() error {
	var firstErr error
	if p.socketPath != "" {
		firstErr = ShutdownACPBroker(p.socketPath)
	}
	if err := p.conn.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (p *ACPBrokerProc) waitAck() error {
	err, ok := <-p.ackCh
	if !ok {
		return fmt.Errorf("acp broker disconnected")
	}
	return err
}

func (p *ACPBrokerProc) readLoop() {
	defer close(p.done)
	defer close(p.ackCh)
	for {
		kind, payload, err := readACPFrame(p.conn)
		if err != nil {
			return
		}
		switch kind {
		case acpFrameEmission:
			var emission brokerEmissionFrame
			if json.Unmarshal(payload, &emission) != nil {
				_ = p.conn.Close()
				return
			}
			if err := p.emitCorrelated(emission); err != nil {
				_ = p.conn.Close()
				return
			}
		case acpFrameCapabilities:
			var capabilities acpCapabilitiesFrame
			if json.Unmarshal(payload, &capabilities) != nil {
				continue
			}
			p.eventMu.Lock()
			p.turnCorrelationVersion = capabilities.TurnCorrelationVersion
			p.turnReplayVersion = capabilities.TurnReplayVersion
			p.inputModalities = append([]string(nil), capabilities.InputModalities...)
			p.inputCapabilitySource = capabilities.Source
			p.executionReconfigure = capabilities.ExecutionReconfigure
			p.eventMu.Unlock()
			p.capabilitiesReadyOnce.Do(func() {
				if p.capabilitiesReady != nil {
					close(p.capabilitiesReady)
				}
			})
		case acpFrameAck:
			var ackErr error
			if len(payload) > 0 {
				ackErr = fmt.Errorf("%s", string(payload))
			}
			p.ackCh <- ackErr
		}
	}
}
