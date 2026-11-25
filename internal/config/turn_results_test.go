package config

import (
	"testing"

	"github.com/spf13/viper"
)

func TestGetTurnResultsDefaultsAndOverrides(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	got := GetTurnResults()
	if !got.Enabled {
		t.Fatalf("default Enabled = false, want true")
	}
	if got.TTLSeconds != DefaultTurnResultsTTLSeconds {
		t.Fatalf("default TTLSeconds = %d, want %d", got.TTLSeconds, DefaultTurnResultsTTLSeconds)
	}
	if got.PendingTimeoutSeconds != DefaultTurnResultsPendingTimeoutSeconds {
		t.Fatalf("default PendingTimeoutSeconds = %d, want %d", got.PendingTimeoutSeconds, DefaultTurnResultsPendingTimeoutSeconds)
	}
	if got.MaxEntries != DefaultTurnResultsMaxEntries {
		t.Fatalf("default MaxEntries = %d, want %d", got.MaxEntries, DefaultTurnResultsMaxEntries)
	}
	if got.MaxTotalBytes != DefaultTurnResultsMaxTotalBytes {
		t.Fatalf("default MaxTotalBytes = %d, want %d", got.MaxTotalBytes, DefaultTurnResultsMaxTotalBytes)
	}
	if got.MaxContentBytes != DefaultTurnResultsMaxContentBytes {
		t.Fatalf("default MaxContentBytes = %d, want %d", got.MaxContentBytes, DefaultTurnResultsMaxContentBytes)
	}

	viper.Set("turn_results.enabled", false)
	viper.Set("turn_results.ttl_seconds", 0)
	viper.Set("turn_results.pending_timeout_seconds", 120)
	viper.Set("turn_results.max_entries", 128)
	viper.Set("turn_results.max_total_bytes", int64(16<<20))
	viper.Set("turn_results.max_content_bytes", 512<<10)

	got = GetTurnResults()
	if got.Enabled || got.TTLSeconds != 0 || got.PendingTimeoutSeconds != 120 ||
		got.MaxEntries != 128 || got.MaxTotalBytes != 16<<20 || got.MaxContentBytes != 512<<10 {
		t.Fatalf("override GetTurnResults() = %+v", got)
	}

	// Out-of-range values fall back to safe defaults.
	viper.Set("turn_results.ttl_seconds", 10)
	viper.Set("turn_results.pending_timeout_seconds", 5)
	viper.Set("turn_results.max_entries", 2)
	viper.Set("turn_results.max_total_bytes", int64(100))
	viper.Set("turn_results.max_content_bytes", 100)

	got = GetTurnResults()
	if got.TTLSeconds != DefaultTurnResultsTTLSeconds ||
		got.PendingTimeoutSeconds != DefaultTurnResultsPendingTimeoutSeconds ||
		got.MaxEntries != DefaultTurnResultsMaxEntries ||
		got.MaxTotalBytes != DefaultTurnResultsMaxTotalBytes ||
		got.MaxContentBytes != DefaultTurnResultsMaxContentBytes {
		t.Fatalf("clamped GetTurnResults() = %+v", got)
	}
}
