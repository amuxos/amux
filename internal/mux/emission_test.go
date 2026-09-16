package mux

import (
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/providertest"
)

type testProducerEmission = agentstream.ProducerEmission
type testEmission = providertest.Emission

func captureEmission(emission testProducerEmission) testEmission {
	return providertest.Capture(emission)
}

func testAssistantEmission(text string) testProducerEmission {
	return testProducerEmission{Turn: agentstream.AssistantDelta{Text: text}}
}

func testTerminalEmission(outcome agentstream.Outcome, code, message string) testProducerEmission {
	terminal := agentstream.Terminal{Outcome: outcome, Code: code, Message: message}
	return testProducerEmission{Terminal: &terminal}
}

func testMetadataEmission(agentSessionID, effectiveModel string) testProducerEmission {
	return testProducerEmission{
		Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", NativeType: "session_metadata"},
		Session: agentstream.SessionMetadataChanged{AgentSessionID: agentSessionID, EffectiveModel: effectiveModel},
	}
}

func testBrokerEmissionFrame(turnToken string, sequence uint64, emission testProducerEmission) brokerEmissionFrame {
	encoded, _ := agentstream.MarshalProducerEmission(emission)
	return brokerEmissionFrame{TurnToken: turnToken, Sequence: sequence, Emission: encoded}
}

func capturedBrokerEmission(frame brokerEmissionFrame) testEmission {
	emission, _ := agentstream.UnmarshalProducerEmission(frame.Emission)
	return captureEmission(emission)
}
