package adapter

import (
	"slices"
	"testing"
)

func TestPublicRegistryContainsOnlySupportedAgents(t *testing.T) {
	want := []string{"claude", "codex", "pi"}
	if got := Names(); !slices.Equal(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
	for _, name := range want {
		if _, err := Get(name); err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
	}
	for _, name := range []string{"unknown"} {
		if _, err := Get(name); err == nil {
			t.Fatalf("Get(%q) accepted unsupported public agent", name)
		}
		if IsCodexCompatible(name) {
			t.Fatalf("private agent %q has public protocol capabilities", name)
		}
	}
}
