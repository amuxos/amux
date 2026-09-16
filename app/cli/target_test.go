package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestFormatTargetListShowsStructuredCapabilitySummary(t *testing.T) {
	targets := []*pb.Target{{
		Id:   "target-1",
		Name: "node-1",
		Capabilities: &pb.TargetCapabilities{
			AgentTypes:   []string{"codex", "claude"},
			RuntimeTools: []string{"git", "go", "node"},
		},
		Sessions: []*pb.Session{{Name: "work", State: "running", AgentType: "codex"}},
	}}

	var out bytes.Buffer
	if err := formatTargetList(&out, targets, false); err != nil {
		t.Fatalf("formatTargetList: %v", err)
	}
	text := out.String()
	for _, want := range []string{"TARGET", "CAPABILITIES", "node-1", "codex,claude,git,go,node"} {
		if !strings.Contains(text, want) {
			t.Fatalf("target list output missing %q:\n%s", want, text)
		}
	}
}

func TestFormatTargetListJSONUsesVersionedEnvelope(t *testing.T) {
	targets := []*pb.Target{{
		Id:      "target-1",
		Name:    "node-1",
		Version: "v1",
		Sessions: []*pb.Session{
			{Name: "work", State: "running", AgentType: "codex"},
			{Name: "old", State: "archived", AgentType: "codex"},
		},
	}}

	var out bytes.Buffer
	if err := formatTargetList(&out, targets, true); err != nil {
		t.Fatalf("formatTargetList JSON: %v", err)
	}
	var got struct {
		Schema  string `json:"schema"`
		Targets []struct {
			ID       string `json:"id"`
			Sessions int    `json:"sessions"`
			Running  int    `json:"running_sessions"`
			Archived int    `json:"archived_sessions"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if got.Schema != "amux.system.target.list.v1" || len(got.Targets) != 1 || got.Targets[0].ID != "target-1" || got.Targets[0].Sessions != 2 || got.Targets[0].Running != 1 || got.Targets[0].Archived != 1 {
		t.Fatalf("target JSON = %#v", got)
	}
}

func TestSystemTargetListUsesCanonicalCommand(t *testing.T) {
	root := newRootCmd()
	canonical, _, err := root.Find([]string{"system", "target", "list"})
	if err != nil || canonical.Name() != "list" {
		t.Fatalf("find system target list: found=%v err=%v", canonical, err)
	}
	assertRootCommandNotRegistered(t, root, "target")
	if got := canonical.Use; got != "list" {
		t.Fatalf("system target list Use = %q, want list", got)
	}
	if canonical.Parent() == nil || canonical.Parent().Name() != "target" || canonical.Parent().Parent() == nil || canonical.Parent().Parent().Name() != "system" {
		t.Fatalf("system target list parent chain = %v -> %v, want target -> system", canonical.Parent(), canonical.Parent().Parent())
	}
}
