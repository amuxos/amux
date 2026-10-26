package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/i18n"
)

func TestDailyCleanupConfigTypesAndValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	for _, tc := range []struct{ key, value string }{
		{"maintenance.daily_cleanup.enabled", "false"},
		{"maintenance.daily_cleanup.at", "07:30"},
		{"maintenance.daily_cleanup.protected_paths", "/shared /persistent"},
	} {
		cmd := newConfigCmd()
		cmd.SetArgs([]string{"set", tc.key, tc.value})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := readCLIConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	daily := cfg["maintenance"].(map[string]any)["daily_cleanup"].(map[string]any)
	if enabled, ok := daily["enabled"].(bool); !ok || enabled {
		t.Fatalf("boolean not persisted: %#v", daily)
	}
	if _, ok := daily["protected_paths"].([]any); !ok {
		t.Fatalf("paths not an array: %#v", daily)
	}
	before, err := os.ReadFile(defaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		for _, tc := range []struct{ key, value string }{
			{"maintenance.daily_cleanup.enabled", "maybe"},
			{"maintenance.daily_cleanup.at", "25:00"},
			{"maintenance.daily_cleanup.at", "7:30"},
			{"maintenance.daily_cleanup.protected_paths", "/safe relative"},
		} {
			err := setConfigValueLocale(cfg, tc.key, tc.value, locale)
			if err == nil || strings.Contains(err.Error(), "cleanup.invalid_") {
				t.Fatalf("invalid value accepted/untranslated: %v", err)
			}
		}
	}
	after, err := os.ReadFile(defaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("invalid value modified configuration")
	}
}
