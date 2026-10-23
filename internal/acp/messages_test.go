package acp

import (
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/i18n"
)

func TestACPDiagnosticLanguagesAndFallback(t *testing.T) {
	for _, tc := range []struct {
		key  string
		args []any
	}{
		{"execution.unsupported", nil},
		{"control.compact_unavailable", nil},
		{"control.unsupported", []any{"native-control"}},
		{"config.unadvertised_value", []any{"model", "native/model-a"}},
		{"config.readback_mismatch", []any{"model", "native/model-a"}},
		{"config.rollback_failed", []any{"native diagnostic"}},
		{"watchdog.turn_idle_timeout", nil},
		{"watchdog.turn_hard_cap", nil},
		{"watchdog.repeat_tool_loop", nil},
	} {
		en := acpCatalog.Format(i18n.English, tc.key, tc.args...)
		zh := acpCatalog.Format(i18n.Chinese, tc.key, tc.args...)
		fallback := acpCatalog.Format(i18n.Locale("unsupported-locale"), tc.key, tc.args...)
		if en == tc.key || zh == tc.key || en == zh || en != fallback {
			t.Fatalf("key %s: en=%q zh=%q fallback=%q", tc.key, en, zh, fallback)
		}
		diagnostic := acpError(tc.key, tc.args...).Error()
		if !strings.Contains(diagnostic, en) || !strings.Contains(diagnostic, zh) {
			t.Fatalf("bilingual diagnostic missing for %s", tc.key)
		}
	}
}
