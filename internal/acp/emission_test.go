package acp

import (
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/providertest"
)

type testProducerEmission = agentstream.ProducerEmission
type testEmission = providertest.Emission

func captureEmission(emission testProducerEmission) testEmission {
	return providertest.Capture(emission)
}
