package cli

import (
	"strings"

	"github.com/amuxos/amux/internal/config"
)

func distributionConfigReadAllowed(key string) bool {
	parts := strings.Split(key, ".")
options:
	for _, option := range config.ConfigOptions() {
		pattern := strings.Split(option.Key, ".")
		if len(parts) != len(pattern) {
			continue
		}
		for i, segment := range pattern {
			switch segment {
			case "<profile>", "<label>":
				if !configProfileNamePattern.MatchString(parts[i]) {
					continue options
				}
			case "<provider>":
				if !config.SupportsStructuredConfig(parts[i]) {
					continue options
				}
			default:
				if parts[i] != segment {
					continue options
				}
			}
		}
		return true
	}
	return false
}

func parseDistributionConfigValue(key, raw string) (any, bool, error) {
	if parse := distributionHooks.ParseConfigValue; parse != nil {
		return parse(key, raw)
	}
	return nil, false, nil
}
