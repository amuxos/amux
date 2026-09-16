package adapter

import (
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/extensionstate"
)

var registeredAdapters = []adapterRegistration{
	{"claude", func() Adapter { return &ClaudeAdapter{} }},
	{"codex", func() Adapter { return &CodexAdapter{} }},
	{"pi", func() Adapter { return &PiAdapter{} }},
}

// Register adds an adapter before application startup. Existing names cannot
// be replaced, including the built-in adapters.
func Register(name string, create func() Adapter) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, " \t\r\n/") || create == nil {
		return fmt.Errorf("adapter registration requires a name and factory")
	}
	adapterMu.Lock()
	defer adapterMu.Unlock()
	for _, entry := range registeredAdapters {
		if entry.name == name {
			return fmt.Errorf("adapter %q is already registered", name)
		}
	}
	registeredAdapters = append(registeredAdapters, adapterRegistration{name, create})
	return nil
}
