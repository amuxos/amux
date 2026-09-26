// Package distribution supplies deployment-specific defaults to shared code.
package distribution

var (
	LegacyConsoleFallback = false
	CodexCompatibleLabel  = "Codex"
	CodexCompatibleChoice = "Codex"
	CodexCompatibleGroup  = "Codex"
	SupportsResponsesChat = false
	LatestReleaseURL      = "https://github.com/amuxos/amux-releases/releases/latest/download/latest.json"
	// ContractReleaseURL is the public SCM release bundle for the managed
	// Agent Pack Contract runtime. Install and update download it when
	// AGENT_PACK_CONTRACT_SCM_URL is unset.
	ContractReleaseURL  = "https://github.com/amuxos/agent-pack-contract-releases/releases/latest/download/agent-pack-contract-scm-latest.tar.gz"
	BrandURL            = "https://amuxos.com/"
	BrandFooterMarkdown = "<font color='grey'>Powered by</font> [amux](" + BrandURL + ")"
	LegacyBrandFooter   = "Powered by amux"
	DelegationProtocol  = "urn:amux:protocol:feishu-external-bot-delegation:v1"
)
