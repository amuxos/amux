package mux

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

const (
	brokerTurnProtocolV3 = 3
	brokerTurnReplayV1   = 1
	brokerCapabilityWait = 300 * time.Millisecond
	brokerUpgradeAckWait = 300 * time.Millisecond
	brokerRebindAckWait  = 5 * time.Second
	brokerFrameWriteWait = 2 * time.Second
	brokerTurnSteerV1    = 1
	brokerSteerWait      = 15 * time.Second
	// A turn-steer ack starting with this prefix reports that the provider
	// may already have taken the input (session.ErrSteerOutcomeUnknown).
	brokerSteerOutcomeUnknownPrefix = "steer outcome unknown: "
)

type brokerCapabilitiesFrame struct {
	TurnCorrelationVersion int `json:"turn_correlation_version,omitempty"`
	TurnReplayVersion      int `json:"turn_replay_version,omitempty"`
}

func writeBrokerFrame(conn net.Conn, kind byte, payload []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(brokerFrameWriteWait)); err != nil {
		return err
	}
	err := writeACPFrame(conn, kind, payload)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

type brokerUpgradeFrame struct {
	Version int `json:"version"`
}

type brokerTurnDispatchFrame struct {
	TurnToken string            `json:"turn_token"`
	Input     session.UserInput `json:"input"`
	Control   string            `json:"control,omitempty"`
}

type brokerEmissionFrame struct {
	TurnToken string          `json:"turn_token,omitempty"`
	Sequence  uint64          `json:"sequence,omitempty"`
	Emission  json.RawMessage `json:"emission"`
}

func marshalBrokerEmissionFrame(turnToken string, sequence uint64, emission agentstream.ProducerEmission) ([]byte, error) {
	emissionJSON, err := agentstream.MarshalProducerEmission(emission)
	if err != nil {
		return nil, err
	}
	return json.Marshal(brokerEmissionFrame{TurnToken: turnToken, Sequence: sequence, Emission: emissionJSON})
}

type brokerTurnSteerFrame struct {
	TurnToken string            `json:"turn_token"`
	Input     session.UserInput `json:"input"`
}

type brokerTurnRebindFrame struct {
	TurnToken     string `json:"turn_token"`
	AfterSequence uint64 `json:"after_sequence"`
}

type turnSinkEntry struct {
	sink         func(agentstream.ProducerEmission) bool
	lastSequence uint64
}

func installTurnSink(sinks *map[string]*turnSinkEntry, turnToken string, afterSequence uint64, sink func(agentstream.ProducerEmission) bool) (*turnSinkEntry, error) {
	if *sinks == nil {
		*sinks = make(map[string]*turnSinkEntry)
	}
	if _, exists := (*sinks)[turnToken]; exists {
		return nil, fmt.Errorf("%w: %s", session.ErrTurnSinkActive, turnToken)
	}
	entry := &turnSinkEntry{sink: sink, lastSequence: afterSequence}
	(*sinks)[turnToken] = entry
	return entry, nil
}

func uninstallTurnSink(sinks map[string]*turnSinkEntry, turnToken string, entry *turnSinkEntry) {
	if sinks[turnToken] == entry {
		delete(sinks, turnToken)
	}
}

// routeBrokerEmission validates the shared correlated-delivery contract while
// the caller holds its event mutex. The returned sink must be invoked only
// after releasing that mutex.
func routeBrokerEmission(sinks map[string]*turnSinkEntry, frame brokerEmissionFrame, protocol string) (*turnSinkEntry, agentstream.ProducerEmission, error) {
	emission, err := agentstream.UnmarshalProducerEmission(frame.Emission)
	if err != nil {
		return nil, agentstream.ProducerEmission{}, fmt.Errorf("decode correlated %s producer emission: %w", protocol, err)
	}
	if frame.Sequence == 0 {
		return nil, agentstream.ProducerEmission{}, fmt.Errorf("correlated %s emission for %q has no sequence", protocol, frame.TurnToken)
	}
	entry := sinks[frame.TurnToken]
	if entry == nil {
		return nil, agentstream.ProducerEmission{}, fmt.Errorf("%s emission for unbound turn %q", protocol, frame.TurnToken)
	}
	want := entry.lastSequence + 1
	if frame.Sequence != want {
		return nil, agentstream.ProducerEmission{}, fmt.Errorf("%s emission sequence for %q = %d, want %d", protocol, frame.TurnToken, frame.Sequence, want)
	}
	entry.lastSequence = frame.Sequence
	if emission.IsTerminal() {
		delete(sinks, frame.TurnToken)
	}
	emission.Origin.TransportSeq = frame.Sequence
	return entry, emission, nil
}

type brokerBufferedEmission struct {
	sequence uint64
	payload  []byte
}

// brokerTurnReplay owns the short-lived delivery log for the active (or most
// recently completed) correlated turn. It is intentionally process-local: the
// durable truth remains the Session Timeline, while this buffer bridges a
// runtime disconnect without dispatching provider input twice.
//
// Callers serialize all methods with their broker broadcast mutex.
type brokerTurnReplay struct {
	token    string
	next     uint64
	terminal bool
	frames   []brokerBufferedEmission
}

func (r *brokerTurnReplay) begin(turnToken string) {
	r.token = turnToken
	r.next = 0
	r.terminal = false
	r.frames = nil
}

func (r *brokerTurnReplay) discard(turnToken string) {
	if r.token != turnToken {
		return
	}
	r.token = ""
	r.next = 0
	r.terminal = false
	r.frames = nil
}

func (r *brokerTurnReplay) append(turnToken string, emission agentstream.ProducerEmission) ([]byte, error) {
	if turnToken == "" || r.token != turnToken {
		return nil, fmt.Errorf("unknown replay turn %q", turnToken)
	}
	if r.terminal {
		return nil, fmt.Errorf("turn %q already has a terminal emission", turnToken)
	}
	r.next++
	payload, err := marshalBrokerEmissionFrame(turnToken, r.next, emission)
	if err != nil {
		// A malformed opaque provider field must not silently create a replay
		// gap. Replace the unencodable observation with an explicit terminal so
		// the durable Timeline and queue fail closed.
		terminal := agentstream.ProducerEmission{
			Origin: agentstream.Origin{Actor: agentstream.ActorSystem, Channel: "system", NativeType: "broker_encode_error"},
			Terminal: &agentstream.Terminal{
				Outcome: agentstream.OutcomeError,
				Code:    "broker_encode_error",
				Message: "broker could not encode producer emission",
			},
		}
		payload, err = marshalBrokerEmissionFrame(turnToken, r.next, terminal)
		if err != nil {
			r.next--
			return nil, err
		}
		emission = terminal
	}
	payload = append([]byte(nil), payload...)
	r.frames = append(r.frames, brokerBufferedEmission{sequence: r.next, payload: payload})
	if emission.IsTerminal() {
		r.terminal = true
	}
	return payload, nil
}

func (r *brokerTurnReplay) after(turnToken string, afterSequence uint64) ([][]byte, bool, error) {
	if turnToken == "" || r.token != turnToken {
		return nil, false, fmt.Errorf("unknown replay turn %q", turnToken)
	}
	if afterSequence > r.next {
		return nil, false, fmt.Errorf("replay cursor %d is ahead of turn %q head %d", afterSequence, turnToken, r.next)
	}
	payloads := make([][]byte, 0, len(r.frames))
	for _, frame := range r.frames {
		if frame.sequence > afterSequence {
			payloads = append(payloads, frame.payload)
		}
	}
	return payloads, r.terminal, nil
}

// brokerTurnAdmission serializes correlated turns targeting one provider
// process. C is the broker's per-connection pointer type.
//
// Callers protect this value with the broker lifecycle mutex. A connection
// disappearing does not release admission: the provider turn remains active
// until its terminal event or a failed dispatch proves it never started.
type brokerTurnAdmission[C comparable] struct {
	owner     C
	turnToken string
}

func (a *brokerTurnAdmission[C]) reserveCorrelated(owner C, turnToken string) string {
	if a.turnToken != "" {
		return "another correlated turn is active"
	}
	a.owner = owner
	a.turnToken = turnToken
	return ""
}

func (a *brokerTurnAdmission[C]) clearCorrelated(owner C, turnToken string) {
	if a.owner == owner && a.turnToken == turnToken {
		a.reset()
	}
}

func (a *brokerTurnAdmission[C]) rebindCorrelated(owner C, turnToken string) string {
	if a.turnToken == "" || a.turnToken != turnToken {
		return "correlated turn is not active"
	}
	a.owner = owner
	return ""
}

func (a *brokerTurnAdmission[C]) disconnect(owner C) {
	if a.owner != owner {
		return
	}
	var zero C
	a.owner = zero
}

func (a *brokerTurnAdmission[C]) finishEmission(turnToken string, terminal bool) {
	if terminal && turnToken != "" && a.turnToken == turnToken {
		a.reset()
	}
}

func (a *brokerTurnAdmission[C]) reset() {
	var zero C
	a.owner = zero
	a.turnToken = ""
}

func waitBrokerAckContext(ctx context.Context, conn net.Conn, ackCh <-chan error, disconnected string) error {
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return err
	}
	select {
	case err, ok := <-ackCh:
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = conn.Close()
			return ctxErr
		}
		if !ok {
			return fmt.Errorf("%s disconnected", disconnected)
		}
		return err
	case <-ctx.Done():
		_ = conn.Close()
		return ctx.Err()
	}
}
