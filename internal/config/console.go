package config

import (
	"os"
	"strings"

	"github.com/spf13/viper"
)

const DefaultConsolePort = 7892

// ConsoleConfig holds the installation-wide temporary Web console settings.
type ConsoleConfig struct {
	Port      int
	PublicURL string
}

// GetConsolePort is installation-wide, not per Bot or browser.
func GetConsolePort() int { return consolePortFrom(viper.GetViper()) }

func GetConsolePublicURL() string { return consolePublicURLFrom(viper.GetViper()) }

// LoadConsoleFrom reads console.* settings directly from path without mutating
// the process-global viper instance. Missing files return defaults.
func LoadConsoleFrom(path string) (ConsoleConfig, error) {
	v := viper.New()
	if _, err := os.Stat(path); err == nil {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return ConsoleConfig{}, err
		}
	} else if !os.IsNotExist(err) {
		return ConsoleConfig{}, err
	}
	return ConsoleConfig{
		Port:      consolePortFrom(v),
		PublicURL: consolePublicURLFrom(v),
	}, nil
}

func consolePortFrom(v *viper.Viper) int {
	port := v.GetInt("console.port")
	if port < 1 || port > 65535 {
		return DefaultConsolePort
	}
	return port
}

func consolePublicURLFrom(v *viper.Viper) string {
	return strings.TrimSpace(v.GetString("console.public_url"))
}
