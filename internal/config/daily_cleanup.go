package config

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const DailyCleanupEnabledKey = "maintenance.daily_cleanup.enabled"

type DailyCleanupSettings struct {
	Enabled        bool
	At             string
	ProtectedPaths []string
}

func dailyCleanupSettings(v *viper.Viper) (DailyCleanupSettings, error) {
	s := DailyCleanupSettings{Enabled: true, At: "06:17"}
	if v.IsSet(DailyCleanupEnabledKey) {
		s.Enabled = v.GetBool(DailyCleanupEnabledKey)
	}
	if v.IsSet("maintenance.daily_cleanup.at") {
		s.At = v.GetString("maintenance.daily_cleanup.at")
	}
	if _, err := time.Parse("15:04", s.At); err != nil || len(s.At) != 5 {
		return s, fmt.Errorf("maintenance.daily_cleanup.at: expected HH:MM")
	}
	s.ProtectedPaths = v.GetStringSlice("maintenance.daily_cleanup.protected_paths")
	for _, p := range s.ProtectedPaths {
		if !filepath.IsAbs(p) {
			return s, fmt.Errorf("maintenance.daily_cleanup.protected_paths: expected absolute paths")
		}
	}
	return s, nil
}

// ReadDailyCleanupSettings uses a fresh parser so saving the switch takes effect
// without mutating Viper's global state while background readers are running.
func ReadDailyCleanupSettings() (DailyCleanupSettings, error) {
	return ReadDailyCleanupSettingsContext(context.Background())
}

// ReadDailyCleanupSettingsContext reads fresh settings with cancellable lock acquisition.
func ReadDailyCleanupSettingsContext(ctx context.Context) (DailyCleanupSettings, error) {
	if err := lockConfigWriteContext(ctx); err != nil {
		return DailyCleanupSettings{}, err
	}
	defer configWriteMu.Unlock()
	raw, err := readConfigFileForWrite(experimentsConfigPath())
	if err != nil {
		return DailyCleanupSettings{}, err
	}
	if err := ctx.Err(); err != nil {
		return DailyCleanupSettings{}, err
	}
	v := viper.New()
	if err = v.MergeConfigMap(raw); err != nil {
		return DailyCleanupSettings{}, err
	}
	return dailyCleanupSettings(v)
}

func SaveDailyCleanupEnabled(enabled bool) error {
	return SaveDailyCleanupEnabledContext(context.Background(), enabled)
}

// SaveDailyCleanupEnabledContext saves the switch with cancellable lock acquisition.
func SaveDailyCleanupEnabledContext(ctx context.Context, enabled bool) error {
	return UpdateConfigFileContext(ctx, experimentsConfigPath(), func(raw map[string]any) error {
		setNestedConfigValue(raw, strings.Split(DailyCleanupEnabledKey, "."), enabled)
		return nil
	})
}
