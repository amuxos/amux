package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/amuxos/amux/internal/extensionstate"
)

// Extension supplies configuration metadata and defaults for a distribution.
type Extension struct {
	Options            []ConfigOption
	AgentExecutables   map[string]string // app-server agent type -> display name
	StructuredDefaults map[string]func(StructuredProviderConfig) StructuredProviderConfig
}

var extensionRegistered bool
var structuredExtensions = map[string]func(StructuredProviderConfig) StructuredProviderConfig{}

func RegisterExtension(ext Extension) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if extensionRegistered {
		return fmt.Errorf("configuration extension already registered")
	}
	seen := map[string]bool{}
	for _, option := range ext.Options {
		if strings.TrimSpace(option.Key) == "" || option.Type == "" || seen[option.Key] {
			return fmt.Errorf("invalid or duplicate extension configuration key %q", option.Key)
		}
		seen[option.Key] = true
	}
	for name, defaults := range ext.StructuredDefaults {
		if name == "" || name == "codex" || name != strings.TrimSpace(name) || defaults == nil {
			return fmt.Errorf("invalid extension structured provider %q", name)
		}
	}
	for name, label := range ext.AgentExecutables {
		if name == "" || name == "codex" || strings.ContainsAny(name, ". /\\\t\r\n") || label == "" {
			return fmt.Errorf("invalid executable integration %q", name)
		}
	}
	for name, label := range ext.AgentExecutables {
		agentExecutables[name] = label
	}
	for _, option := range ext.Options {
		found := false
		for i := range configOptions {
			if configOptions[i].Key == option.Key {
				configOptions[i] = option
				found = true
				break
			}
		}
		if !found {
			configOptions = append(configOptions, option)
		}
	}
	for name, defaults := range ext.StructuredDefaults {
		structuredExtensions[name] = defaults
	}
	extensionRegistered = true
	return nil
}

func SupportsStructuredConfig(name string) bool { return supportsStructuredConfig(name) }

func structuredConfigProviderNames() string {
	names := []string{"`codex`"}
	for name := range structuredExtensions {
		names = append(names, "`"+name+"`")
	}
	sort.Strings(names)
	return strings.Join(names, "、")
}
