package agentprofile

import (
	"fmt"
	"io/fs"
	"sync"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/extensionstate"
	"github.com/amuxos/amux/internal/task"
)

// ProjectionFile is a provider-owned workspace file produced by a customization.
type ProjectionFile struct {
	Path   string
	Render func(Profile) string
}

// PiProviderConfig supplies a distribution provider's connection and model
// compatibility settings to the shared Pi workspace renderer.
type PiProviderConfig struct {
	BaseURL     string
	API         string
	ModelCompat map[string]bool
}

type BuiltinMetadata struct {
	Description string
	Agents      string
	SkillDirs   []string
	Features    []string
}

type RuntimeCatalogSource struct {
	Ref         string
	DisplayName string
	Models      []string
	// Capabilities optionally declares per-model reasoning capabilities for a
	// distribution runtime whose models are not uniformly capable. A model with
	// a declared capability keeps that declaration verbatim; a model without one
	// falls back to the shared per-protocol defaults below. A runtime whose
	// catalog is discovered at runtime declares here rather than relying on the
	// name heuristics, which cannot see that runtime's real capabilities.
	Capabilities map[string]RuntimeModelCapability
	// Variants declares additional model refs backed by the same runtime model
	// but a different backend variant.
	Variants []RuntimeModelVariant
}

// RuntimeModelCapability is the declared reasoning surface of one runtime model.
// Presence in RuntimeCatalogSource.Capabilities is the declaration: a nil
// SupportedReasoningEfforts therefore means the model accepts no effort
// selection, not that the protocol defaults apply.
type RuntimeModelCapability struct {
	// SupportedReasoningEfforts is the exact effort enum the model accepts.
	SupportedReasoningEfforts []string
	// DefaultReasoningEffort is the effort the model uses when none is selected.
	DefaultReasoningEffort string
}

// RuntimeModelVariant is one backend variant of a native runtime model. A
// runtime may expose an alternate backend of a model (for example a
// larger-context one) that trades cost or quota for capability; the variant is
// selected by name at launch.
type RuntimeModelVariant struct {
	// Ref is the user-facing model ref (for example "GPT-5.6-Sol-Max").
	Ref string
	// AgentModel is the underlying runtime model name (for example "GPT-5.6-Sol").
	AgentModel string
	// BackendVariant is the runtime's backend variant identifier.
	BackendVariant string
}

// Customization is installed once, before starting the application. Shared
// profile parsing, selection, snapshots and projection ordering stay in core.
type Customization struct {
	Builtin                          fs.FS
	BuiltinMetadata                  map[string]BuiltinMetadata
	DefaultProfile                   string
	ManagedLocalDefaultAgent         string
	Catalog                          []byte
	RuntimeCatalog                   []RuntimeCatalogSource
	AllowUnlistedNativeRuntimeModels bool
	RuntimeModelUsesBinaryThinking   func(string) bool
	ValidateProvider                 func(string, *Provider) error
	PermissionModeArgs               func(string, string) ([]string, error)
	PiProviderConfig                 func(Profile) (PiProviderConfig, error)
	AppendDefaultProviderArgs        func(Profile, string, []string, []string) []string
	AppendUpstreamProviderArgs       func(Profile, string, []string) []string
	LaunchProcessArgs                func(string, []string) []string
	ProviderProjectionFiles          func(Profile) []ProjectionFile
	MaterializeConfig                func(Profile, string) (string, error)
	InstructionsFile                 func(string) string
	SkillProjectionDir               func(string) string
	MaterializeMCP                   func(string, *task.Sandbox, adapter.Placement, string, string, string, string) ([]string, error)
}

var customization Customization
var customizationMu sync.Mutex
var customizationRegistered, customizationFrozen bool

// FreezeCustomization closes startup registration before concurrent requests.
func FreezeCustomization() {
	customizationMu.Lock()
	defer customizationMu.Unlock()
	customizationFrozen = true
}

func RegisterCustomization(next Customization) (err error) {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	customizationMu.Lock()
	defer customizationMu.Unlock()
	if customizationRegistered || customizationFrozen {
		return fmt.Errorf("profile customization registration is closed")
	}
	if next.Builtin == nil || len(next.BuiltinMetadata) == 0 || next.DefaultProfile == "" || next.ManagedLocalDefaultAgent == "" {
		return fmt.Errorf("profile customization requires a builtin pack, metadata and defaults")
	}
	packs, err := loadCatalogPacks(next.Catalog)
	if err != nil {
		return fmt.Errorf("profile customization catalog: %w", err)
	}
	for _, source := range next.RuntimeCatalog {
		if _, err := adapter.Get(source.Ref); err != nil {
			return err
		}
	}
	if err := validateRuntimeCatalogSources(next.RuntimeCatalog); err != nil {
		return err
	}
	oldCustomization, oldFS, oldMetadata := customization, builtinProfileFS, builtinCompatibility
	customization, builtinProfileFS = next, next.Builtin
	builtinCompatibility = make(map[string]builtinCompatibilityMetadata, len(next.BuiltinMetadata))
	for name, metadata := range next.BuiltinMetadata {
		builtinCompatibility[name] = builtinCompatibilityMetadata{
			description: metadata.Description, agents: metadata.Agents,
			skillDirs: cloneStrings(metadata.SkillDirs), features: cloneStrings(metadata.Features),
		}
	}
	defer func() {
		if err != nil {
			customization, builtinProfileFS, builtinCompatibility = oldCustomization, oldFS, oldMetadata
		}
	}()
	profiles, err := loadBuiltinProfiles()
	if err != nil {
		return err
	}
	if _, ok := profiles[next.DefaultProfile]; !ok {
		return fmt.Errorf("default builtin profile %q is missing", next.DefaultProfile)
	}
	sources := make([]runtimeCatalogSource, 0, len(next.RuntimeCatalog))
	for _, source := range next.RuntimeCatalog {
		sources = append(sources, runtimeCatalogSource{
			ref:          source.Ref,
			displayName:  source.DisplayName,
			models:       cloneStrings(source.Models),
			capabilities: cloneRuntimeModelCapabilities(source.Capabilities),
			variants:     cloneRuntimeModelVariants(source.Variants),
		})
	}
	builtinProfiles, catalogPacks, runtimeCatalogSources = profiles, packs, sources
	allowUnlistedNativeRuntimeModels = next.AllowUnlistedNativeRuntimeModels
	DefaultProfileRef = BuiltinNamespace + "/" + next.DefaultProfile
	ManagedLocalDefaultAgent = next.ManagedLocalDefaultAgent
	customizationRegistered = true
	return nil
}

func ConfiguredModel(profile Profile) string          { return configuredModel(profile) }
func CodexConfigValue(arg, key string) (string, bool) { return codexConfigValue(arg, key) }
func ReplaceStructuredModelArg(args []string, model string) []string {
	return replaceStructuredModelArg(args, model)
}
func ProjectionRoot(kind string, sandbox *task.Sandbox) (string, error) {
	return rootFor(kind, sandbox)
}
func WriteProfileFile(path, content string) error { return writeProfileFile(path, content) }
func MCPEnvironment(home, session, token, tokenFile string) map[string]string {
	return mcpEnv(home, session, token, tokenFile)
}
func MCPCommand(home string) string { return amuxCommand(home) }
