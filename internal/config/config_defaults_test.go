package config_test

import (
	"testing"

	"github.com/amuxos/amux/internal/config"
)

func TestPublicStructuredProviderConfig(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		provider string
		want     config.StructuredProviderConfig
	}{
		{"defaults", "", "codex", config.StructuredProviderConfig{900, 21600, 3600, 50}},
		{"global inheritance", "[provider.structured]\nturn_idle_timeout_seconds=60\nrepeat_tool_call_limit=9\n", "codex", config.StructuredProviderConfig{60, 21600, 3600, 9}},
		{"override", "[provider.structured]\nturn_idle_timeout_seconds=60\n[provider.structured.codex]\nturn_idle_timeout_seconds=600\ntool_idle_timeout_seconds=1200\nrepeat_tool_call_limit=11\n", " Codex ", config.StructuredProviderConfig{600, 21600, 1200, 11}},
		{"negative and clamp", "[provider.structured]\nturn_idle_timeout_seconds=60\n[provider.structured.codex]\nturn_idle_timeout_seconds=-1\nturn_hard_cap_seconds=100000000\nrepeat_tool_call_limit=-1\n", "codex", config.StructuredProviderConfig{900, 604800, 3600, 50}},
		{"disabled", "[provider.structured.codex]\nturn_idle_timeout_seconds=0\nrepeat_tool_call_limit=0\n", "codex", config.StructuredProviderConfig{0, 21600, 3600, 0}},
		{"count clamp", "[provider.structured.codex]\nrepeat_tool_call_limit=5000\n", "codex", config.StructuredProviderConfig{900, 21600, 3600, 1000}},
		{"unknown override ignored", "[provider.structured.unknown]\nturn_idle_timeout_seconds=1\n", "unknown", config.StructuredProviderConfig{900, 21600, 3600, 50}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireLoad(t, writeConfig(t, tt.body))
			if got := config.GetStructuredProviderForProvider(tt.provider); got != tt.want {
				t.Fatalf("config = %+v, want %+v", got, tt.want)
			}
		})
	}
}
