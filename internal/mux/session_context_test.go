package mux

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

func TestBrokerPreservesUserInputAtDispatchBoundary(t *testing.T) {
	for _, control := range []string{"", "clear"} {
		b := brokerTurns{}
		conn := &brokerConn{protocolV3: true}
		req := brokerTurnDispatchFrame{TurnToken: "turn", Control: control, Input: session.UserInput{Parts: []session.InputPart{{Text: "user\nquestion"}, {Image: &session.ImageInput{Data: []byte{1, 2}, MIMEType: "image/png"}}}}}
		payload, _ := json.Marshal(req)
		called := false
		reason := b.dispatchCorrelatedTurn(conn, payload, func(_ context.Context, got session.TurnDispatchRequest, _ func(agentstream.ProducerEmission) bool) error {
			called = true
			if got.TurnToken != req.TurnToken || got.Control != control {
				t.Fatal("correlation/control changed")
			}
			if !reflect.DeepEqual(got.Input, req.Input) {
				t.Fatal("original input changed or gained a Runtime prefix")
			}
			return nil
		}, func(string, agentstream.ProducerEmission) {})
		if !called || reason != "" {
			t.Fatalf("called=%v reason=%s", called, reason)
		}
	}
}
