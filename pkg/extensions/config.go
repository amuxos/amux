package extensions

import "github.com/amuxos/amux/internal/config"

type ConfigOption = config.ConfigOption
type ConfigExtension = config.Extension

func RegisterConfig(ext ConfigExtension) error { return config.RegisterExtension(ext) }
func ConfigOptions() []ConfigOption            { return config.ConfigOptions() }
func RenderConfigReference() []byte            { return config.RenderConfigurationReference() }
