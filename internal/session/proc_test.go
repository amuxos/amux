package session

import (
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestEventSinksKeepSessionEmissionsGlobalAndTurnEmissionsCapabilityBound(t *testing.T) {
	var global, turn []agentstream.ProducerEmission
	var sinks EventSinks
	sinks.SetGlobal(func(ev agentstream.ProducerEmission) bool { global = append(global, ev); return false })
	if err := sinks.BeginTurn(func(ev agentstream.ProducerEmission) bool { turn = append(turn, ev); return false }); err != nil {
		t.Fatal(err)
	}
	sinks.Emit(agentstream.ProducerEmission{Session: agentstream.SessionMetadataChanged{AgentSessionID: "provider-session"}})
	sinks.Emit(agentstream.ProducerEmission{Turn: agentstream.AssistantDelta{Text: "answer"}})
	sinks.Emit(agentstream.ProducerEmission{Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}})
	sinks.Emit(agentstream.ProducerEmission{Turn: agentstream.AssistantDelta{Text: "late"}})

	if len(global) != 2 {
		t.Fatalf("global emissions = %+v", global)
	}
	metadata, ok := global[0].Session.(agentstream.SessionMetadataChanged)
	if !ok || metadata.AgentSessionID != "provider-session" {
		t.Fatalf("global metadata = %#v", global[0].Session)
	}
	late, ok := global[1].Turn.(agentstream.AssistantDelta)
	if !ok || late.Text != "late" {
		t.Fatalf("late global emission = %#v", global[1])
	}
	if len(turn) != 2 {
		t.Fatalf("turn emissions = %+v", turn)
	}
	answer, ok := turn[0].Turn.(agentstream.AssistantDelta)
	if !ok || answer.Text != "answer" || turn[1].Terminal == nil {
		t.Fatalf("turn emissions = %+v", turn)
	}
}

func TestEventSinksSnapshotPendingProducerEmission(t *testing.T) {
	input := []byte(`{"path":"before"}`)
	var sinks EventSinks
	sinks.Emit(agentstream.ProducerEmission{Session: agentstream.ExtensionObserved{
		Namespace: "test",
		Schema:    "v1",
		Payload:   input,
	}})
	copy(input, []byte(`{"path":"after!"}`))

	var got agentstream.ProducerEmission
	sinks.SetGlobal(func(emission agentstream.ProducerEmission) bool {
		got = emission
		return false
	})
	extension, ok := got.Session.(agentstream.ExtensionObserved)
	if !ok || string(extension.Payload) != `{"path":"before"}` {
		t.Fatalf("pending emission = %#v", got.Session)
	}
}
