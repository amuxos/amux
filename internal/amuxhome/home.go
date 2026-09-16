// Package amuxhome owns the default amux data directory resolution.
package amuxhome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Dir returns the amux data directory. AMUX_HOME overrides the default
// ~/.amux path across all binaries.
func Dir() string {
	if dir := strings.TrimSpace(os.Getenv("AMUX_HOME")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".amux"
	}
	return filepath.Join(home, ".amux")
}

func Path(elem ...string) string {
	parts := append([]string{Dir()}, elem...)
	return filepath.Join(parts...)
}

// ConfigPath returns the default config file path.
func ConfigPath() string {
	return Path("config.toml")
}

// SecretsDir returns the default credential/secrets directory.
func SecretsDir() string {
	return Path("secrets")
}

// PrincipalSecretsDir returns the control-plane credential directory for an
// authenticated principal. Principal IDs, rather than aliases or OS account
// names, are the stable security key used in filesystem paths.
func PrincipalSecretsDir(principalID string) (string, error) {
	principalID = strings.TrimSpace(principalID)
	if _, err := uuid.Parse(principalID); err != nil {
		return "", fmt.Errorf("invalid principal id: %w", err)
	}
	return Path("principals", principalID, "secrets"), nil
}
