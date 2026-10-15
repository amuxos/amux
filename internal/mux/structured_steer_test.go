package mux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

// steerTestProc is a broker provider that only implements steering.
type steerTestProc struct {
	session.Proc
	err   error
	steer []session.TurnSteerRequest
}

func (p *steerTestProc) SteerTurn(_ context.Context, req session.TurnSteerRequest) error {
	if p.err != nil {
		return p.err
	}
	p.steer = append(p.steer, req)
	return nil
}

func steerFramePayload(t *testing.T, token string) []byte {
	t.Helper()
	payload, err := json.Marshal(brokerTurnSteerFrame{TurnToken: token, Input: session.TextUserInput("use dry-run")})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestStructuredBrokerSteersOnlyTheAdmittedTurn(t *testing.T) {
	proc := &steerTestProc{}
	b := &structuredBroker{proc: proc}
	if got := b.turnSteerVersion(); got != brokerTurnSteerV1 {
		t.Fatalf("turnSteerVersion = %d, want %d", got, brokerTurnSteerV1)
	}
	b.turnAdmission.turnToken = "turn-1"
	if ack := b.steerCorrelatedTurn(steerFramePayload(t, "turn-2")); ack != "steer target turn is not active" {
		t.Fatalf("ack = %q, want rejection of another turn", ack)
	}
	if ack := b.steerCorrelatedTurn(steerFramePayload(t, "turn-1")); ack != "" {
		t.Fatalf("ack = %q, want success", ack)
	}
	if len(proc.steer) != 1 || proc.steer[0].TurnToken != "turn-1" || proc.steer[0].Input.Text() != "use dry-run" {
		t.Fatalf("provider steers = %+v", proc.steer)
	}

	proc.err = fmt.Errorf("%w: no claude turn in flight", session.ErrSteerUnavailable)
	if ack := b.steerCorrelatedTurn(steerFramePayload(t, "turn-1")); ack == "" || strings.HasPrefix(ack, brokerSteerOutcomeUnknownPrefix) {
		t.Fatalf("ack = %q, want a plain rejection", ack)
	}
	proc.err = fmt.Errorf("%w: context deadline exceeded", session.ErrSteerOutcomeUnknown)
	if ack := b.steerCorrelatedTurn(steerFramePayload(t, "turn-1")); !strings.HasPrefix(ack, brokerSteerOutcomeUnknownPrefix) {
		t.Fatalf("ack = %q, want the unknown-outcome prefix", ack)
	}

	if got := (&structuredBroker{proc: struct{ session.Proc }{}}).turnSteerVersion(); got != 0 {
		t.Fatalf("turnSteerVersion without a steering provider = %d, want 0", got)
	}
}

func newSteerClient(t *testing.T, version int) (*StructuredBrokerProc, net.Conn) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	p := &StructuredBrokerProc{
		brokerTurnClient: brokerTurnClient{
			protocol:          structuredTurnWire,
			conn:              clientConn,
			ackCh:             make(chan error, 1),
			done:              make(chan struct{}),
			capabilitiesReady: make(chan struct{}),
			turnSteerVersion:  version,
			turnSinks: map[string]*turnSinkEntry{
				"turn-1": {sink: func(agentstream.ProducerEmission) bool { return true }},
			},
		},
	}
	go p.readLoop()
	return p, serverConn
}

func TestStructuredBrokerProcSteerClassifiesBrokerAcks(t *testing.T) {
	for _, tc := range []struct {
		name string
		ack  string
		want error
	}{
		{"accepted", "", nil},
		{"rejected", "steer target turn is not active", session.ErrSteerUnavailable},
		{"unknown provider outcome", brokerSteerOutcomeUnknownPrefix + "steer outcome unknown: timeout", session.ErrSteerOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, server := newSteerClient(t, brokerTurnSteerV1)
			frames := make(chan byte, 1)
			go func() {
				kind, _, err := readACPFrame(server)
				if err != nil {
					return
				}
				frames <- kind
				_ = writeACPFrame(server, structuredFrameAck, []byte(tc.ack))
			}()
			err := p.SteerTurn(context.Background(), session.TurnSteerRequest{TurnToken: "turn-1", Input: session.TextUserInput("x")})
			if kind := <-frames; kind != structuredFrameTurnSteer {
				t.Fatalf("frame kind = %x, want turn steer", kind)
			}
			if tc.want == nil {
				if err != nil {
					t.Fatalf("SteerTurn = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("SteerTurn = %v, want %v", err, tc.want)
			}
			if errors.Is(tc.want, session.ErrSteerUnavailable) && errors.Is(err, session.ErrSteerOutcomeUnknown) {
				t.Fatalf("SteerTurn = %v, a rejection must not be an unknown outcome", err)
			}
		})
	}
}

func TestStructuredBrokerProcSteerLostAckHasUnknownOutcome(t *testing.T) {
	p, server := newSteerClient(t, brokerTurnSteerV1)
	go func() {
		_, _, _ = readACPFrame(server)
		_ = server.Close()
	}()
	err := p.SteerTurn(context.Background(), session.TurnSteerRequest{TurnToken: "turn-1", Input: session.TextUserInput("x")})
	if !errors.Is(err, session.ErrSteerOutcomeUnknown) {
		t.Fatalf("SteerTurn = %v, want ErrSteerOutcomeUnknown", err)
	}
}

func TestStructuredBrokerProcSteerRejectsLocally(t *testing.T) {
	p, _ := newSteerClient(t, 0)
	if err := p.SteerTurn(context.Background(), session.TurnSteerRequest{TurnToken: "turn-1"}); !errors.Is(err, session.ErrSteerUnavailable) {
		t.Fatalf("old broker: SteerTurn = %v, want unavailable", err)
	}
	p, _ = newSteerClient(t, brokerTurnSteerV1)
	if err := p.SteerTurn(context.Background(), session.TurnSteerRequest{TurnToken: "turn-9"}); !errors.Is(err, session.ErrSteerUnavailable) {
		t.Fatalf("unbound turn: SteerTurn = %v, want unavailable", err)
	}
}
