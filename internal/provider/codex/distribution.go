package codex

func supportedProvider(name string) bool {
	return name == "codex" || supportedDistributionProvider(name)
}
