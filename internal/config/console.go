package config

import (
	"strings"

	"github.com/spf13/viper"
)

const DefaultConsolePort = 7892

// GetConsolePort is installation-wide, not per Bot or browser.
func GetConsolePort() int {
	port := viper.GetInt("console.port")
	if port < 1 || port > 65535 {
		return DefaultConsolePort
	}
	return port
}
func GetConsolePublicURL() string { return strings.TrimSpace(viper.GetString("console.public_url")) }
