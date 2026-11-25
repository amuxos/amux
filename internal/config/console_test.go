package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestLoadConsoleFromReadsFreshValuesWithoutMutatingGlobalViper(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("console.port", 9001)
	viper.Set("console.public_url", "https://old.example.com")

	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := LoadConsoleFrom(path)
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if cfg.Port != DefaultConsolePort || cfg.PublicURL != "" {
		t.Fatalf("missing file defaults = %+v", cfg)
	}

	if err := os.WriteFile(path, []byte("[console]\nport = 8123\npublic_url = 'https://fresh.example.com'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConsoleFrom(path)
	if err != nil {
		t.Fatalf("populated file: %v", err)
	}
	if cfg.Port != 8123 || cfg.PublicURL != "https://fresh.example.com" {
		t.Fatalf("populated config = %+v", cfg)
	}
	if GetConsolePort() != 9001 || GetConsolePublicURL() != "https://old.example.com" {
		t.Fatal("LoadConsoleFrom mutated global viper")
	}

	if err := os.WriteFile(path, []byte("[console]\nport = 7892\npublic_url = ''\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConsoleFrom(path)
	if err != nil {
		t.Fatalf("cleared file: %v", err)
	}
	if cfg.Port != 7892 || cfg.PublicURL != "" {
		t.Fatalf("cleared config = %+v", cfg)
	}
}
