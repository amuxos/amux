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

// ResponsesChatCommand builds arguments for a distribution-owned supervisor in
// the runtime executable. Core retains the launch backend, identity and lifecycle.
type ResponsesChatCommand func(ResponsesChatOptions, []string) ([]string, error)

var responsesChatCommand ResponsesChatCommand

func RegisterResponsesChatCommand(command ResponsesChatCommand) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if command == nil {
		return fmt.Errorf("responses-chat command builder is required")
	}
	structuredProviderMu.Lock()
	defer structuredProviderMu.Unlock()
	if responsesChatCommand != nil {
		return fmt.Errorf("responses-chat command builder is already registered")
	}
	responsesChatCommand = command
	return nil
}

func responsesChatCommandArgs(options ResponsesChatOptions, args []string) ([]string, error) {
	structuredProviderMu.RLock()
	command := responsesChatCommand
	structuredProviderMu.RUnlock()
	if command == nil {
		return nil, fmt.Errorf("responses-chat command builder is not registered")
	}
	result, err := command(options, append([]string(nil), args...))
	if err != nil {
		return nil, err
	}
	if len(result) == 0 || strings.TrimSpace(result[0]) == "" {
		return nil, fmt.Errorf("responses-chat command builder returned an empty command")
	}
	return result, nil
}
