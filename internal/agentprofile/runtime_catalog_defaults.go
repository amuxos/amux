package agentprofile

var allowUnlistedNativeRuntimeModels = true

func runtimeModelUsesBinaryThinking(model string) bool {
	return customization.RuntimeModelUsesBinaryThinking != nil && customization.RuntimeModelUsesBinaryThinking(model)
}

var runtimeCatalogSources = []runtimeCatalogSource{
	{ref: "codex", displayName: "Codex"},
	{ref: "claude", displayName: "Claude Code", models: []string{"opus", "sonnet", "haiku"}},
	{ref: "pi", displayName: "Pi"},
}
