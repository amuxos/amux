package task

import (
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// TestSpecResolvedProfileRoundTrip: the control-plane-only field survives JSON
// (it travels on SpecJson over the tunnel) and is omitted when empty.
func TestSpecResolvedProfileRoundTrip(t *testing.T) {
	empty, _ := json.Marshal(Spec{Agent: "claude", Task: "x"})
	if strings.Contains(string(empty), "resolved_profile") {
		t.Fatalf("empty spec must omit resolved_profile: %s", empty)
	}

	in := Spec{
		Agent: "claude",
		Task:  "x",
		ResolvedProfile: &pb.AgentProfileSpec{
			Name:          "code-reviewer",
			Type:          "claude",
			Agents:        "AGENTS.md",
			AgentsContent: "be terse",
			Skills:        []string{"review"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Spec
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rp := out.ResolvedProfile
	if rp == nil || rp.Type != "claude" || rp.AgentsContent != "be terse" {
		t.Fatalf("round-trip lost fields: %+v", rp)
	}
	if rp.Agents != "AGENTS.md" {
		t.Fatalf("round-trip lost agents ref: %+v", rp)
	}
	if len(rp.Skills) != 1 || rp.Skills[0] != "review" {
		t.Fatalf("round-trip lost skills: %+v", rp.Skills)
	}
}

func TestSpecPreservesLegacyTrustedModeAckAsWireNoOp(t *testing.T) {
	var spec Spec
	if err := json.Unmarshal([]byte(`{"agent":"claude","task":"x","trusted_mode_ack":true}`), &spec); err != nil {
		t.Fatalf("unmarshal legacy spec: %v", err)
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal normalized spec: %v", err)
	}
	if !strings.Contains(string(encoded), `"trusted_mode_ack":true`) {
		t.Fatalf("normalized spec dropped legacy wire field: %s", encoded)
	}
}
