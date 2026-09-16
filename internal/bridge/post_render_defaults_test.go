package bridge

import (
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/distribution"
)

func TestPublicBrandFooterLinksToHomepage(t *testing.T) {
	if distribution.BrandURL != "https://amuxos.com/" {
		t.Fatalf("public brand URL = %q", distribution.BrandURL)
	}
	elements := renderAmuxBrandFooterElements()
	if len(elements) != 2 || elements[1].Content != "<font color='grey'>Powered by</font> [amux](https://amuxos.com/)" {
		t.Fatalf("public footer = %+v", elements)
	}
	if got := normalizeAgentResultText("Result\n\n---\nPowered by amux"); got != "Result" {
		t.Fatalf("result with legacy public footer = %q", got)
	}
	card := renderExternalBotDelegationCard("ou-example-target", "ou-example-callback", "example task")
	if !strings.Contains(card, "urn:amux:protocol:feishu-external-bot-delegation:v1") || strings.Contains(card, "https://amuxos.com/") {
		t.Fatalf("public delegation card does not retain its independent protocol marker: %s", card)
	}
}
