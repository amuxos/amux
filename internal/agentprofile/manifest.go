package agentprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
)

const packManifestRelPath = "dist/agent-pack.manifest.json"
const supportedPackManifestSchemaVersion = "v1.1"

type packManifest struct {
	SchemaVersion string                           `json:"schema_version"`
	PackNamespace string                           `json:"pack_namespace"`
	Profiles      map[string]packManifestProfile   `json:"profiles"`
	Providers     map[string]packManifestProvider  `json:"providers"`
	Namespaces    map[string]packManifestNamespace `json:"namespaces"`
}

type packManifestProfile struct {
	Namespace       string                `json:"namespace"`
	Type            string                `json:"type"`
	Model           string                `json:"model"`
	SupportedModels []string              `json:"supported_models"`
	Description     string                `json:"description"`
	Providers       []string              `json:"providers"`
	Instructions    []string              `json:"instructions"`
	Skills          []string              `json:"skills"`
	Subagents       []string              `json:"subagents"`
	Envs            []string              `json:"envs"`
	EnvRefs         []string              `json:"env_refs"`
	Extends         *string               `json:"extends"`
	Permission      packProfilePermission `json:"permission"`
}

type packManifestProvider struct {
	Platform        string            `json:"platform"`
	WireAPI         string            `json:"wire_api,omitempty"`
	BaseURL         string            `json:"base_url"`
	UpstreamModel   string            `json:"upstream_model"`
	APIKeyEnv       string            `json:"api_key_env"`
	APIKeyLiteral   string            `json:"api_key_literal,omitempty"`
	ContextWindow   int               `json:"context_window"`
	ReadTimeout     string            `json:"read_timeout"`
	MaxTokens       int               `json:"max_tokens"`
	Description     string            `json:"description"`
	PromptHint      string            `json:"prompt_hint"`
	ByAzure         *bool             `json:"by_azure"`
	DisableFailover *bool             `json:"disable_failover"`
	Thinking        map[string]string `json:"thinking"`
}

type packManifestNamespace struct {
	Profiles []string `json:"profiles"`
}

// LoadPackManifest reads an expert-agents agent-pack manifest file. The v1.1
// contract is intentionally explicit so amux never falls back to source TOML
// when a manifest is present.
func LoadPackManifest(path string) (packManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return packManifest{}, err
	}
	return parsePackManifest(path, data)
}

func parsePackManifest(path string, data []byte) (packManifest, error) {
	var manifest packManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("parse agent pack manifest %s: %w", path, err)
	}
	if strings.TrimSpace(manifest.SchemaVersion) != supportedPackManifestSchemaVersion {
		return manifest, fmt.Errorf("unsupported agent pack manifest schema_version %q in %s; supported: %s", strings.TrimSpace(manifest.SchemaVersion), path, supportedPackManifestSchemaVersion)
	}
	if strings.TrimSpace(manifest.PackNamespace) == "" {
		return manifest, fmt.Errorf("agent pack manifest %s pack_namespace is required", path)
	}
	if len(manifest.Profiles) == 0 {
		return manifest, fmt.Errorf("agent pack manifest %s profiles must contain at least one profile", path)
	}
	if manifest.Providers == nil {
		manifest.Providers = map[string]packManifestProvider{}
	}
	if manifest.Namespaces == nil {
		manifest.Namespaces = map[string]packManifestNamespace{}
	}
	return manifest, nil
}

type manifestProfileSource struct {
	namespace        string
	revision         string
	source           Source
	packPath         string
	profilePath      string
	preferredTargets []string
	loadContents     func([]string) (string, error)
}

func packManifestPath(pack Pack) string {
	return filepath.Join(pack.MaterializedPath, filepath.FromSlash(packManifestRelPath))
}

func loadPackManifestForPack(pack Pack) (packManifest, error) {
	path := packManifestPath(pack)
	manifest, err := LoadPackManifest(path)
	if err != nil {
		if os.IsNotExist(err) {
			return packManifest{}, fmt.Errorf("agent pack manifest %s is required", path)
		}
		return packManifest{}, err
	}
	return manifest, nil
}

func ValidatePackManifest(pack Pack) error {
	manifest, err := loadPackManifestForPack(pack)
	if err != nil {
		return err
	}
	if _, err := listManifestProfiles(pack, manifest); err != nil {
		return fmt.Errorf("validate agent pack manifest %s: %w", packManifestPath(pack), err)
	}
	return nil
}

func listManifestProfiles(pack Pack, manifest packManifest) ([]Profile, error) {
	return listManifestProfilesFromSource(manifestSourceForPack(pack), manifest)
}

func manifestSourceForPack(pack Pack) manifestProfileSource {
	source := SourcePack
	if pack.Namespace == ManagedLocalPackNamespace && pack.ManagedBy == ManagedPackOwnerAmux {
		source = SourceLocal
	}
	return manifestProfileSource{
		namespace:   pack.Namespace,
		revision:    pack.CurrentRevision,
		source:      source,
		packPath:    pack.MaterializedPath,
		profilePath: packManifestPath(pack),
		loadContents: func(refs []string) (string, error) {
			return loadPackContents(pack.MaterializedPath, refs)
		},
	}
}

func listManifestProfilesFromSource(source manifestProfileSource, manifest packManifest) ([]Profile, error) {
	names := make([]string, 0, len(manifest.Profiles))
	for name := range manifest.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Profile, 0, len(names))
	for _, name := range names {
		profile, err := manifestProfileToProfileFromSource(source, manifest, name)
		if err != nil {
			return nil, err
		}
		out = append(out, profile)
	}
	return out, nil
}

func loadManifestPackProfile(pack Pack, manifest packManifest, name string) (Profile, bool, error) {
	if _, ok := manifest.Profiles[name]; !ok {
		return Profile{}, false, nil
	}
	profile, err := manifestProfileToProfileFromSource(manifestSourceForPack(pack), manifest, name)
	if err != nil {
		return Profile{}, false, err
	}
	return profile, true, nil
}

func manifestProfileToProfileFromSource(source manifestProfileSource, manifest packManifest, name string) (Profile, error) {
	entry := manifest.Profiles[name]
	name = strings.TrimSpace(name)
	if name == "" {
		return Profile{}, fmt.Errorf("agent pack manifest %s contains an empty profile key", source.profilePath)
	}
	agentType := strings.TrimSpace(entry.Type)
	if agentType == "" {
		return Profile{}, fmt.Errorf("profile %s type is required", name)
	}
	if _, err := adapter.Get(agentType); err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", name, err)
	}
	model := strings.TrimSpace(entry.Model)
	providers := cleanStrings(entry.Providers)
	for _, ref := range providers {
		if _, err := requireProviderModelRef(ref); err != nil {
			return Profile{}, fmt.Errorf("profile %s: %w", name, err)
		}
	}
	modelPolicyEnforced := entry.SupportedModels != nil
	supportedModels := []string(nil)
	modelOptions := map[string]ModelOption(nil)
	if modelPolicyEnforced {
		supportedModels = cleanStrings(entry.SupportedModels)
		if model != "" && !stringSliceContains(supportedModels, model) {
			return Profile{}, fmt.Errorf("profile %s default model %q is not in supported_models", name, model)
		}
		_, qualified, err := parseProviderModelRef(model)
		if err != nil {
			return Profile{}, fmt.Errorf("profile %s: %w", name, err)
		}
		if qualified && !stringSliceContains(providers, model) {
			return Profile{}, fmt.Errorf("profile %s provider-backed default model %q must also be listed in providers for v1.1 compatibility", name, model)
		}
		modelOptions, err = manifestModelOptions(manifest, supportedModels)
		if err != nil {
			return Profile{}, fmt.Errorf("profile %s: %w", name, err)
		}
		for ref, option := range modelOptions {
			if err := validateProvider(agentType, option.Provider); err != nil {
				return Profile{}, fmt.Errorf("profile %s model %q: %w", name, ref, err)
			}
		}
	}
	// model is optional for adapter-default usage: a profile may run on the
	// agent's login/default model. It is only required for provider-backed models, since
	// it is the selection key into manifest.providers.
	if model == "" && len(providers) > 0 {
		return Profile{}, fmt.Errorf("profile %s model is required when providers are declared", name)
	}
	// instructions is optional: a profile may carry no agent-facing
	// instruction file (e.g. a coder profile that relies entirely on
	// provider-builtin behavior or skills). Empty instructions yields an empty
	// AgentsContent, matching builtin-profile behavior and the documented
	// "agents optional" authoring rule.
	instructions := cleanStrings(entry.Instructions)
	for _, providerName := range providers {
		if _, ok := manifest.Providers[providerName]; !ok {
			return Profile{}, fmt.Errorf("profile %s references unknown provider %q", name, providerName)
		}
	}
	provider, err := manifestProviderForProfile(manifest, entry)
	if modelPolicyEnforced && model != "" {
		if option, ok := modelOptions[model]; ok {
			provider = cloneProvider(option.Provider)
		}
	}
	if err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", name, err)
	}
	if err := validateProvider(agentType, provider); err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", name, err)
	}
	defaultArgs, err := permissionModeArgs(agentType, entry.Permission)
	if err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", name, err)
	}
	if source.loadContents == nil {
		return Profile{}, fmt.Errorf("agent pack manifest %s has no content loader", source.profilePath)
	}
	agentsContent, err := source.loadContents(instructions)
	if err != nil {
		return Profile{}, err
	}
	extends := ""
	if entry.Extends != nil {
		extends = strings.TrimSpace(*entry.Extends)
	}
	requires := []string{agentType}
	return Profile{
		Namespace:           source.namespace,
		Name:                name,
		Revision:            source.revision,
		Type:                agentType,
		Model:               model,
		ConfiguredModel:     model,
		Extends:             extends,
		Description:         strings.TrimSpace(entry.Description),
		Agents:              strings.Join(instructions, ", "),
		AgentsContent:       agentsContent,
		Skills:              cleanStrings(entry.Skills),
		Subagents:           cleanStrings(entry.Subagents),
		Envs:                cleanStrings(entry.Envs),
		EnvRefs:             cleanStrings(entry.EnvRefs),
		DefaultArgs:         defaultArgs,
		Source:              source.source,
		Writable:            source.source == SourceLocal,
		PackPath:            source.packPath,
		ProfilePath:         source.profilePath,
		Provider:            provider,
		SupportedModels:     supportedModels,
		ModelOptions:        modelOptions,
		ModelPolicyEnforced: modelPolicyEnforced,
		Placement: Placement{
			PreferredTargets: cloneStrings(source.preferredTargets),
			Requires:         Requirements{AgentTypes: requires},
		},
	}, nil
}

func manifestProviderForProfile(manifest packManifest, profile packManifestProfile) (*Provider, error) {
	model := strings.TrimSpace(profile.Model)
	if model == "" || !stringSliceContains(cleanStrings(profile.Providers), model) {
		return nil, nil
	}
	provider, ok := manifest.Providers[model]
	if !ok {
		return nil, fmt.Errorf("provider-backed model %q is not defined in manifest.providers", model)
	}
	return provider.toProvider(), nil
}

func stringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func (p packManifestProvider) toProvider() *Provider {
	return &Provider{
		CredentialProfile: strings.TrimSpace(p.Platform),
		Platform:          strings.TrimSpace(p.Platform),
		WireAPI:           strings.TrimSpace(p.WireAPI),
		APIKeyEnv:         strings.TrimSpace(p.APIKeyEnv),
		APIKeyLiteral:     strings.TrimSpace(p.APIKeyLiteral),
		BaseURL:           strings.TrimSpace(p.BaseURL),
		UpstreamModel:     strings.TrimSpace(p.UpstreamModel),
		ContextWindow:     p.ContextWindow,
		ReadTimeout:       strings.TrimSpace(p.ReadTimeout),
		MaxTokens:         p.MaxTokens,
		Description:       strings.TrimSpace(p.Description),
		PromptHint:        strings.TrimSpace(p.PromptHint),
		ByAzure:           cloneBoolPtr(p.ByAzure),
		DisableFailover:   cloneBoolPtr(p.DisableFailover),
		Thinking:          cloneStringMap(p.Thinking),
	}
}

func manifestModelOptions(manifest packManifest, refs []string) (map[string]ModelOption, error) {
	options := make(map[string]ModelOption, len(refs))
	for _, ref := range refs {
		option := ModelOption{Ref: ref, Kind: ModelKindNative, AgentModel: ref}
		providerBinding, qualified, err := parseProviderModelRef(ref)
		if err != nil {
			return nil, err
		}
		if qualified {
			providerDef, exists := manifest.Providers[ref]
			if !exists {
				return nil, fmt.Errorf("provider-backed model %q is not defined in manifest.providers", ref)
			}
			provider := providerDef.toProvider()
			provider.Ref = providerBinding
			option.Kind = ModelKindProvider
			option.AgentModel = provider.UpstreamModel
			option.ProviderRef = providerBinding
			option.Provider = provider
		}
		options[ref] = option
	}
	return options, nil
}

func parseProviderModelRef(ref string) (providerBinding string, qualified bool, err error) {
	providerBinding, providerModel, qualified := strings.Cut(ref, "/")
	if !qualified {
		return "", false, nil
	}
	providerBinding = strings.TrimSpace(providerBinding)
	providerModel = strings.TrimSpace(providerModel)
	if providerBinding == "" || providerModel == "" {
		return "", true, fmt.Errorf("provider-backed model ref %q must use <provider-binding>/<model>", ref)
	}
	return providerBinding, true, nil
}

func requireProviderModelRef(ref string) (string, error) {
	providerBinding, qualified, err := parseProviderModelRef(ref)
	if err != nil {
		return "", err
	}
	if !qualified {
		return "", fmt.Errorf("provider-backed model ref %q must use <provider-binding>/<model>", ref)
	}
	return providerBinding, nil
}

func loadPackContents(root string, refs []string) (string, error) {
	var b strings.Builder
	for _, ref := range refs {
		content, err := loadPackContent(root, ref)
		if err != nil {
			return "", err
		}
		b.WriteString(ensureTrailingNewline(content))
	}
	return b.String(), nil
}
