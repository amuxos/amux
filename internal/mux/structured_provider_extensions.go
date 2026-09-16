package mux

import (
	"fmt"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/extensionstate"
	"github.com/amuxos/amux/internal/session"
)

type StructuredProviderFactory func(StructuredLaunchOptions, config.StructuredProviderConfig) (session.Proc, <-chan []byte, error)

var structuredProviderMu sync.RWMutex
var structuredProviders = make(map[string]StructuredProviderFactory)

func RegisterStructuredProvider(id string, factory StructuredProviderFactory) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if strings.TrimSpace(id) == "" || factory == nil {
		return fmt.Errorf("structured provider registration requires an ID and factory")
	}
	switch id {
	case "codex-app-server", "claude-sdk", "pi-rpc":
		return fmt.Errorf("structured provider %q is already registered", id)
	}
	structuredProviderMu.Lock()
	defer structuredProviderMu.Unlock()
	if _, exists := structuredProviders[id]; exists {
		return fmt.Errorf("structured provider %q is already registered", id)
	}
	structuredProviders[id] = factory
	return nil
}

func startDistributionStructuredProc(opts StructuredLaunchOptions, watchdog config.StructuredProviderConfig) (session.Proc, <-chan []byte, error) {
	structuredProviderMu.RLock()
	factory := structuredProviders[opts.Provider]
	structuredProviderMu.RUnlock()
	if factory == nil {
		return nil, nil, fmt.Errorf("unknown structured provider %q", opts.Provider)
	}
	return factory(opts, watchdog)
}

// StartStructuredProc uses the same admission and launch path as the broker.
func StartStructuredProc(opts StructuredLaunchOptions) (session.Proc, <-chan []byte, error) {
	return startStructuredProc(opts)
}
