package codex

import (
	"context"
	"testing"
)

func TestSessionInstructionThreadParamsMergesWithMemory(t *testing.T) {
	p := &Proc{}
	params := map[string]any{"developerInstructions": "MEMORY"}
	p.sessionInstructionThreadParams(context.Background(), StartOptions{Env: []string{sessionInstructionsEnv + "=PROTO"}}, params)
	if got := params["developerInstructions"]; got != "MEMORY\n\nPROTO" {
		t.Fatalf("developerInstructions = %q", got)
	}
	empty := map[string]any{}
	p.sessionInstructionThreadParams(context.Background(), StartOptions{}, empty)
	if _, ok := empty["developerInstructions"]; ok {
		t.Fatal("set without instructions")
	}
	if mergeDeveloperInstructions("A\n\nPROTO", "PROTO") != "A\n\nPROTO" {
		t.Fatal("duplicate merge")
	}
}
