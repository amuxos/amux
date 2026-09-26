package winsvc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ServiceName        = "Amux"
	ServiceDisplayName = "Amux Hub"
	ServiceDescription = "Amux hub (amuxd) and runtime (amuxlet) for Feishu bridge and local sessions"
)

// Config is persisted under ProgramData so the LocalSystem service host can
// locate binaries and the installing user's amux home.
type Config struct {
	BinDir      string `json:"bin_dir"`
	AmuxHome    string `json:"amux_home"`
	UserProfile string `json:"user_profile"`
	Username    string `json:"username,omitempty"`
}

func programDataDir() string {
	base := strings.TrimSpace(os.Getenv("PROGRAMDATA"))
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "amux")
}

// ConfigPath returns the service configuration file path.
func ConfigPath() string {
	return filepath.Join(programDataDir(), "service.json")
}

// LoadConfig reads the persisted service configuration.
func LoadConfig() (*Config, error) {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.BinDir) == "" || strings.TrimSpace(cfg.AmuxHome) == "" {
		return nil, fmt.Errorf("incomplete Windows service config at %s", ConfigPath())
	}
	return &cfg, nil
}

// SaveConfig writes the service configuration.
func SaveConfig(cfg Config) error {
	if strings.TrimSpace(cfg.BinDir) == "" {
		return fmt.Errorf("bin_dir is required")
	}
	if strings.TrimSpace(cfg.AmuxHome) == "" {
		return fmt.Errorf("amux_home is required")
	}
	if err := os.MkdirAll(programDataDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), append(data, '\n'), 0644)
}

// RemoveConfig deletes the persisted service configuration.
func RemoveConfig() error {
	err := os.Remove(ConfigPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
