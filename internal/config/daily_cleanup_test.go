package config

import (
	"github.com/spf13/viper"
	"os"
	"path/filepath"
	"testing"
)

func TestDailyCleanupDefaultPersistenceAndValidation(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.toml")
	viper.SetConfigFile(path)
	s, err := ReadDailyCleanupSettings()
	if err != nil || !s.Enabled || s.At != "06:17" {
		t.Fatalf("defaults=%+v %v", s, err)
	}
	if err := os.WriteFile(path, []byte("[isolation]\nenabled = true\n[other]\nkeep = 'yes'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveDailyCleanupEnabled(false); err != nil {
		t.Fatal(err)
	}
	s, err = ReadDailyCleanupSettings()
	if err != nil || s.Enabled {
		t.Fatalf("saved=%+v %v", s, err)
	}
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	if v.GetString("other.keep") != "yes" {
		t.Fatal("lost unrelated key")
	}
	v.Set("maintenance.daily_cleanup.at", "25:00")
	if _, err := dailyCleanupSettings(v); err == nil {
		t.Fatal("invalid time accepted")
	}
	v.Set("maintenance.daily_cleanup.at", "06:17")
	v.Set("maintenance.daily_cleanup.protected_paths", []string{"relative"})
	if _, err := dailyCleanupSettings(v); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := os.WriteFile(path, []byte("[invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDailyCleanupSettings(); err == nil {
		t.Fatal("corrupt config enabled cleanup")
	}
}
