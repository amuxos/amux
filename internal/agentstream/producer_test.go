package agentstream_test

import (
	"errors"
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestProducerEmissionRoundTripsTurnPayloadWithoutCanonicalIdentity(t *testing.T) {
	emission := agentstream.ProducerEmission{
		Origin: agentstream.Origin{
			Actor:      agentstream.ActorAgent,
			Provider:   "codex",
			NativeType: "item/agentMessage/delta",
			NativeID:   "native-message-1",
			NativeSeq:  7,
		},
		Turn: agentstream.AssistantDelta{Text: "hello"},
	}

	encoded, err := agentstream.MarshalProducerEmission(emission)
	if err != nil {
		t.Fatalf("MarshalProducerEmission: %v", err)
	}
	decoded, err := agentstream.UnmarshalProducerEmission(encoded)
	if err != nil {
		t.Fatalf("UnmarshalProducerEmission: %v", err)
	}

	if decoded.Origin != emission.Origin {
		t.Fatalf("origin = %+v, want %+v", decoded.Origin, emission.Origin)
	}
	payload, ok := decoded.Turn.(agentstream.AssistantDelta)
	if !ok || payload.Text != "hello" {
		t.Fatalf("turn payload = %#v, want AssistantDelta hello", decoded.Turn)
	}
	if decoded.Terminal != nil || decoded.Session != nil {
		t.Fatalf("unexpected extra producer scopes: %+v", decoded)
	}
}

func TestProducerEmissionRoundTripsTerminal(t *testing.T) {
	emission := agentstream.ProducerEmission{
		Origin: agentstream.Origin{Actor: agentstream.ActorSystem, Provider: "acp", NativeType: "prompt_response"},
		Terminal: &agentstream.Terminal{
			Outcome: agentstream.OutcomeError,
			Code:    "acp_prompt_failed",
			Message: "prompt exploded",
		},
	}

	encoded, err := agentstream.MarshalProducerEmission(emission)
	if err != nil {
		t.Fatalf("MarshalProducerEmission: %v", err)
	}
	decoded, err := agentstream.UnmarshalProducerEmission(encoded)
	if err != nil {
		t.Fatalf("UnmarshalProducerEmission: %v", err)
	}
	if decoded.Terminal == nil || *decoded.Terminal != *emission.Terminal {
		t.Fatalf("terminal = %+v, want %+v", decoded.Terminal, emission.Terminal)
	}
	if decoded.Turn != nil || decoded.Session != nil {
		t.Fatalf("unexpected extra producer scopes: %+v", decoded)
	}
}

func TestProducerEmissionRoundTripsSessionPayload(t *testing.T) {
	emission := agentstream.ProducerEmission{
		Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Provider: "claude", NativeType: "session.id"},
		Session: agentstream.SessionMetadataChanged{AgentSessionID: "native-session-1", EffectiveModel: "claude-opus"},
	}

	encoded, err := agentstream.MarshalProducerEmission(emission)
	if err != nil {
		t.Fatalf("MarshalProducerEmission: %v", err)
	}
	decoded, err := agentstream.UnmarshalProducerEmission(encoded)
	if err != nil {
		t.Fatalf("UnmarshalProducerEmission: %v", err)
	}
	metadata, ok := decoded.Session.(agentstream.SessionMetadataChanged)
	if !ok || metadata.AgentSessionID != "native-session-1" || metadata.EffectiveModel != "claude-opus" {
		t.Fatalf("session payload = %#v", decoded.Session)
	}
}

func TestProducerEmissionRejectsAmbiguousScope(t *testing.T) {
	_, err := agentstream.MarshalProducerEmission(agentstream.ProducerEmission{
		Turn:     agentstream.AssistantDelta{Text: "hello"},
		Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeSuccess},
	})
	if !errors.Is(err, agentstream.ErrInvalidPayload) {
		t.Fatalf("error = %v, want ErrInvalidPayload", err)
	}
}
