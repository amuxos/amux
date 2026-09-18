package config_test

import (
	"testing"

	"github.com/amuxos/amux/internal/config"
)

func TestMemoryConfigDefaultAndDisable(t *testing.T) {
	for _, test := range []struct {
		name, body string
		enabled    bool
	}{
		{"default", "", false},
		{"disabled", "[memory]\nenabled=false\n", false},
		{"enabled", "[memory]\nenabled=true\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireLoad(t, writeConfig(t, test.body))
			if got := config.GetMemory().Enabled; got != test.enabled {
				t.Fatalf("memory.enabled = %v, want %v", got, test.enabled)
			}
		})
	}
}
