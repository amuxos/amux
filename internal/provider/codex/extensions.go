package codex

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/extensionstate"
)

// ProviderRegistration admits a compatible app-server implementation. Optional
// modalities are explicit provider defaults when model discovery is unavailable.
type ProviderRegistration struct {
	PrepareNativeModelDiscovery func(context.Context, *StartOptions) error
	DecorateNativeModels        func([]NativeModel) []NativeModel // Distribution-owned metadata, evaluated after discovery.
	InputModalities             []string
	InputCapabilitySource       string
}

var providerMu sync.RWMutex
var registeredProviders = make(map[string]ProviderRegistration)

func RegisterProvider(name string, registration ProviderRegistration) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \t\r\n/") {
		return fmt.Errorf("app-server provider registration requires a name")
	}
	if name == "codex" {
		return fmt.Errorf("app-server provider %q is already registered", name)
	}
	if len(registration.InputModalities) > 0 && strings.TrimSpace(registration.InputCapabilitySource) == "" {
		return fmt.Errorf("app-server provider input modalities require a capability source")
	}
	registration.InputModalities = append([]string(nil), registration.InputModalities...)
	providerMu.Lock()
	defer providerMu.Unlock()
	if _, exists := registeredProviders[name]; exists {
		return fmt.Errorf("app-server provider %q is already registered", name)
	}
	registeredProviders[name] = registration
	return nil
}

func supportedDistributionProvider(name string) bool {
	providerMu.RLock()
	defer providerMu.RUnlock()
	_, exists := registeredProviders[name]
	return exists
}

func (p *Proc) discoverDistributionInputCapabilities() bool {
	providerMu.RLock()
	registration, exists := registeredProviders[p.provider()]
	providerMu.RUnlock()
	if !exists {
		return false
	}
	p.inputModalities = append([]string(nil), registration.InputModalities...)
	p.inputCapabilitySource = registration.InputCapabilitySource
	return true
}
