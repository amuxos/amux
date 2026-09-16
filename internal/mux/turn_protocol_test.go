package mux

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestBrokerTurnAdmissionSerializesCorrelatedTurns(t *testing.T) {
	type owner struct{ name string }
	first := &owner{name: "first"}
	second := &owner{name: "second"}

	var admission brokerTurnAdmission[*owner]
	if reason := admission.reserveCorrelated(first, "turn-1"); reason != "" {
		t.Fatalf("reserve first turn: %q", reason)
	}
	if reason := admission.reserveCorrelated(second, "turn-2"); reason == "" {
		t.Fatal("second turn was admitted while the first turn was active")
	}
	admission.finishEmission("turn-2", true)
	if reason := admission.reserveCorrelated(second, "turn-2"); reason == "" {
		t.Fatal("wrong-token terminal released the first turn")
	}
	admission.finishEmission("turn-1", true)
	if reason := admission.reserveCorrelated(second, "turn-2"); reason != "" {
		t.Fatalf("reserve second turn after first terminal: %q", reason)
	}
}

func TestBrokerTurnReplayFailsClosedWhenProviderEventCannotEncode(t *testing.T) {
	var replay brokerTurnReplay
	replay.begin("turn-1")
	payload, err := replay.append("turn-1", agentstream.ProducerEmission{Turn: agentstream.ToolChanged{
		Phase: agentstream.ToolPhaseUpdated,
		Tool:  agentstream.Tool{ID: "tool-1", Input: json.RawMessage(`{"unterminated"`)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var emission brokerEmissionFrame
	if err := json.Unmarshal(payload, &emission); err != nil {
		t.Fatal(err)
	}
	legacy := capturedBrokerEmission(emission)
	if emission.Sequence != 1 || legacy.Terminal == nil || legacy.Terminal.Outcome != agentstream.OutcomeError || legacy.Body == nil || legacy.Body.Error == nil || legacy.Body.Error.Code != "broker_encode_error" || !replay.terminal {
		t.Fatalf("fallback emission = %+v replay=%+v", emission, replay)
	}
}

func TestBrokerTurnReplayReturnsOnlyDurabilityGap(t *testing.T) {
	var replay brokerTurnReplay
	replay.begin("turn-1")
	if _, err := replay.append("turn-1", testAssistantEmission("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := replay.append("turn-1", testTerminalEmission(agentstream.OutcomeSuccess, "completed", "")); err != nil {
		t.Fatal(err)
	}

	frames, terminal, err := replay.after("turn-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !terminal || len(frames) != 1 {
		t.Fatalf("after(1) terminal=%v frames=%d, want terminal suffix of one", terminal, len(frames))
	}
	var emission brokerEmissionFrame
	if err := json.Unmarshal(frames[0], &emission); err != nil {
		t.Fatal(err)
	}
	decoded := capturedBrokerEmission(emission)
	if emission.Sequence != 2 || decoded.Terminal == nil || decoded.Terminal.Outcome != agentstream.OutcomeSuccess {
		t.Fatalf("replayed emission = %+v", emission)
	}
	if _, _, err := replay.after("turn-1", 3); err == nil || !strings.Contains(err.Error(), "ahead") {
		t.Fatalf("future replay cursor error = %v", err)
	}
	if _, _, err := replay.after("other-turn", 0); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown replay token error = %v", err)
	}
}
