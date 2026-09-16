package config

func distributionStructuredDefaults(name string, defaults StructuredProviderConfig) StructuredProviderConfig {
	if customize := structuredExtensions[name]; customize != nil {
		return customize(defaults)
	}
	return defaults
}

func supportsStructuredConfig(name string) bool {
	return name == "codex" || structuredExtensions[name] != nil
}
