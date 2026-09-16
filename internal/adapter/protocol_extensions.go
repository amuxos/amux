package adapter

import (
	"fmt"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/extensionstate"
)

// ProtocolRegistration describes the broker mapping for an additional protocol.
type ProtocolRegistration struct {
	ProviderID      string
	WatchdogKey     string
	CodexCompatible bool
}

var protocolMu sync.RWMutex
var registeredProtocols = make(map[Protocol]ProtocolRegistration)

func RegisterProtocol(protocol Protocol, registration ProtocolRegistration) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if strings.TrimSpace(string(protocol)) == "" || strings.TrimSpace(registration.ProviderID) == "" {
		return fmt.Errorf("protocol registration requires a protocol and provider ID")
	}
	switch protocol {
	case ProtocolACP, ProtocolClaudeSDKSidecar, ProtocolCodexAppServer, ProtocolPiRPC:
		return fmt.Errorf("protocol %q is already registered", protocol)
	}
	protocolMu.Lock()
	defer protocolMu.Unlock()
	if _, exists := registeredProtocols[protocol]; exists {
		return fmt.Errorf("protocol %q is already registered", protocol)
	}
	registeredProtocols[protocol] = registration
	return nil
}

func distributionCodexCompatible(protocol Protocol) bool {
	protocolMu.RLock()
	defer protocolMu.RUnlock()
	return registeredProtocols[protocol].CodexCompatible
}

func distributionStructuredProvider(protocol Protocol) (id, watchdogKey string) {
	protocolMu.RLock()
	defer protocolMu.RUnlock()
	registration := registeredProtocols[protocol]
	return registration.ProviderID, registration.WatchdogKey
}
