package setup

import "testing"

func TestOptionsNormalizeDefaults(t *testing.T) {
	opts := Options{Runtime: "codex"}
	if err := opts.Normalize(); err != nil {
		t.Fatal(err)
	}
	if opts.FeishuProfile != "default" {
		t.Fatalf("profile=%q", opts.FeishuProfile)
	}
	if opts.InteractionMode != "singleton" {
		t.Fatalf("mode=%q", opts.InteractionMode)
	}
	if opts.SessionName != "default-main" {
		t.Fatalf("session=%q", opts.SessionName)
	}
	if opts.AgentProfile != "builtin/codex" {
		t.Fatalf("agent=%q", opts.AgentProfile)
	}
}

func TestOptionsNormalizeRequiresRuntime(t *testing.T) {
	if err := (&Options{}).Normalize(); err == nil {
		t.Fatal("expected runtime error")
	}
}
