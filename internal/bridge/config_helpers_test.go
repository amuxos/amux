package bridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/spf13/viper"
)

func loadBridgeTestConfig(t *testing.T, body string) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := config.Load(path); err != nil {
		t.Fatalf("Load config: %v", err)
	}
}
