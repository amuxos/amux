// Package distribution supplies deployment-specific defaults to shared code.
package distribution

var (
	LegacyConsoleFallback = false
	CodexCompatibleLabel  = "Codex"
	CodexCompatibleChoice = "Codex"
	CodexCompatibleGroup  = "Codex"
	SupportsResponsesChat = false
	LatestReleaseURL      = "https://github.com/amuxos/amux-releases/releases/latest/download/latest.json"
	ContractReleaseURL    = ""
	BrandURL              = "https://amuxos.com/"
	BrandFooterMarkdown   = "<font color='grey'>Powered by</font> [amux](" + BrandURL + ")"
	LegacyBrandFooter     = "Powered by amux"
	DelegationProtocol    = "urn:amux:protocol:feishu-external-bot-delegation:v1"
)
