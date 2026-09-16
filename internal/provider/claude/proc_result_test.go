package claude

import (
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/provider"
)

func TestProviderResultBecomesTypedProducerPayload(t *testing.T) {
	ev := provider.Event{
		Provider: "claude",
		Type:     provider.EventResult,
		Result:   &provider.TurnResult{Text: "final answer", CostUSD: 0.01},
	}
	emission, ignore, err := emissionFromSidecarEvent(ev)
	if err != nil || ignore {
		t.Fatalf("emissionFromSidecarEvent = (%+v, %v, %v)", emission, ignore, err)
	}
	result, ok := emission.Turn.(agentstream.ResultObserved)
	if !ok || result.Text != "final answer" || result.CostUSD != 0.01 {
		t.Fatalf("result payload = %#v", emission.Turn)
	}
}
