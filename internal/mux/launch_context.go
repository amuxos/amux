package mux

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/isolation"
)

func writeBrokerLaunchContextFile(dir, sessionID string, launchContext isolation.LaunchContext) (string, error) {
	path := filepath.Join(dir, sessionID+".launch-context.json")
	data, err := json.Marshal(launchContext)
	if err != nil {
		return "", fmt.Errorf("encode broker launch context: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write broker launch context: %w", err)
	}
	return path, nil
}

// ReadBrokerLaunchContextFile consumes a one-shot launch context file. The
// context is transported out-of-band so resolved principal data is not exposed
// through broker argv or inherited environment variables.
func ReadBrokerLaunchContextFile(path string) (isolation.LaunchContext, error) {
	return readBrokerLaunchContextFile(path)
}

func readBrokerLaunchContextFile(path string) (isolation.LaunchContext, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return isolation.LaunchContext{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return isolation.LaunchContext{}, fmt.Errorf("read broker launch context: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return isolation.LaunchContext{}, fmt.Errorf("remove broker launch context: %w", err)
	}
	var launchContext isolation.LaunchContext
	if err := json.Unmarshal(data, &launchContext); err != nil {
		return isolation.LaunchContext{}, fmt.Errorf("decode broker launch context: %w", err)
	}
	if launchContext.Identity != nil {
		if err := launchContext.Identity.Validate(); err != nil {
			return isolation.LaunchContext{}, fmt.Errorf("validate broker launch identity: %w", err)
		}
	}
	return launchContext, nil
}
