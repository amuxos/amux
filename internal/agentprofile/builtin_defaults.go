package agentprofile

const defaultBuiltinProfile = "codex"

var ManagedLocalDefaultAgent = "codex"

var builtinCompatibility = map[string]builtinCompatibilityMetadata{
	"claude": {description: "Claude Code on local runtime"},
	"codex":  {description: "Codex on local runtime"},
	"pi":     {description: "Pi on local runtime"},
}
