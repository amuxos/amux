package extensions

import (
	"strings"

	"github.com/amuxos/amux/internal/config"
)

type ConfigOption = config.ConfigOption
type ConfigExtension = config.Extension

func RegisterConfig(ext ConfigExtension) error { return config.RegisterExtension(ext) }
func ConfigOptions() []ConfigOption            { return config.ConfigOptions() }
func RenderConfigReference() []byte            { return config.RenderConfigurationReference() }

// UpdateConfigFile uses the same lock and atomic replacement as public
// configuration saves. The caller supplies distribution-owned keys only.
func UpdateConfigFile(path string, values map[string]any) error {
	return config.UpdateConfigFile(path, func(raw map[string]any) error {
		for key, value := range values {
			parts := strings.Split(key, ".")
			node := raw
			for _, part := range parts[:len(parts)-1] {
				next, ok := node[part].(map[string]any)
				if !ok {
					next = map[string]any{}
					node[part] = next
				}
				node = next
			}
			node[parts[len(parts)-1]] = value
		}
		return nil
	})
}
