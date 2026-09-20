package agentprofile

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	agentpacks "github.com/amuxos/amux/agent-packs"
)

const BuiltinNamespace = "builtin"

// DefaultProfileRef is the single installation-wide default used by
// user-facing CLI entry points when the caller does not choose a profile.
// Keep it canonical so persisted task specs and guidance never depend on the
// bare-name compatibility accepted by ResolveBuiltin.
var DefaultProfileRef = BuiltinNamespace + "/" + defaultBuiltinProfile

const builtinProfilePath = "agent-packs/builtin/" + packManifestRelPath

type Source string

const SourceBuiltin Source = "builtin"
const SourceLocal Source = "local"
const SourcePack Source = "pack"

type Profile struct {
	Namespace          string
	Name               string
	Revision           string
	Type               string
	Model              string
	ConfiguredModel    string
	Extends            string
	Description        string
	Agents             string
	AgentsContent      string
	Skills             []string
	Subagents          []string
	SkillDirs          []string
	SnapshotSkillFiles []ProfileSkillFile
	// ExternalSkillDirs maps a declared skill name to a disk directory on the
	// executing host from which an installed Git-backed pack overrides the
	// embedded source (priority: external pack > embedded). Runtime-only: it is
	// never persisted to pack manifests, profile snapshots, or the pb spec; the
	// amuxlet side re-injects it via AttachLocalPackPath. Key order is
	// deterministic per attach so multi-pack conflicts resolve by pack namespace
	// lexicographic order (ListPacks is already namespace-sorted).
	ExternalSkillDirs        map[string]string
	DefaultArgs              []string
	DefaultEnv               []string
	Envs                     []string
	EnvRefs                  []string
	MCPConfig                string
	PermissionPolicy         string
	Source                   Source
	Writable                 bool
	PackPath                 string
	ProfilePath              string
	Provider                 *Provider
	SupportedModels          []string
	ModelOptions             map[string]ModelOption
	CredentialEnvSuggestions map[string]string
	ModelPolicyEnforced      bool
	Placement                Placement
	// Snapshot-only execution selection. These fields do not belong to the
	// Agent Pack manifest and do not participate in Profile identity.
	ExecutionRuntimeRef string
	ReasoningEffort     string
}

type ProfileSkillFile struct {
	Skill string
	Path  string
	Data  []byte
	Mode  uint32
}

type Provider struct {
	Ref               string
	CredentialProfile string
	Platform          string
	WireAPI           string
	APIKeyEnv         string
	// APIKeyLiteral, when set, replaces api_key_env at render time with the
	// literal value and suppresses any credential-store lookup for the key.
	// Intended for shared internal-gateway credentials that every principal may
	// use without per-user secret setup.
	APIKeyLiteral   string
	BaseURL         string
	UpstreamModel   string
	ContextWindow   int
	ReadTimeout     string
	MaxTokens       int
	Description     string
	PromptHint      string
	ByAzure         *bool
	DisableFailover *bool
	Thinking        map[string]string
}

type ModelKind string

const (
	ModelKindDefault  ModelKind = "default"
	ModelKindNative   ModelKind = "native"
	ModelKindProvider ModelKind = "provider"
)

type ModelOption struct {
	Ref         string
	Kind        ModelKind
	AgentModel  string
	ProviderRef string
	Provider    *Provider
	// BackendVariant selects a runtime backend variant of AgentModel (see
	// RuntimeModelOption.BackendVariant).
	BackendVariant string
}

type ResolvedModelSelection struct {
	Ref         string
	Kind        ModelKind
	AgentModel  string
	ProviderRef string
	Provider    *Provider
	// BackendVariant selects a runtime backend variant of AgentModel.
	BackendVariant string
}

type Placement struct {
	PreferredTargets []string
	Requires         Requirements
}

type Requirements struct {
	AgentTypes   []string
	RuntimeTools []string
	Features     []string
	Credentials  []string
}

func (p Profile) Ref() string {
	if p.Namespace == "" {
		return p.Name
	}
	return p.Namespace + "/" + p.Name
}

func (p Profile) PinnedRef() string {
	ref := p.Ref()
	if p.Revision == "" {
		return ref
	}
	return ref + "@" + p.Revision
}

func Builtins() []Profile {
	out := make([]Profile, 0, len(builtinProfiles))
	for _, profile := range builtinProfiles {
		out = append(out, cloneProfile(profile))
	}
	return out
}

func ResolveBuiltin(ref string) (Profile, bool) {
	name, ok := builtinName(ref)
	if !ok {
		return Profile{}, false
	}
	profile, ok := builtinProfiles[name]
	if !ok {
		return Profile{}, false
	}
	return cloneProfile(profile), true
}

var builtinProfileFS fs.FS = agentpacks.Builtin()

var builtinProfiles = mustLoadBuiltinProfiles()

type builtinCompatibilityMetadata struct {
	description string
	agents      string
	skillDirs   []string
	features    []string
}

func mustLoadBuiltinProfiles() map[string]Profile {
	profiles, err := loadBuiltinProfiles()
	if err != nil {
		panic(err)
	}
	return profiles
}

func loadBuiltinProfiles() (map[string]Profile, error) {
	data, err := fs.ReadFile(builtinProfileFS, packManifestRelPath)
	if err != nil {
		return nil, err
	}
	manifest, err := parsePackManifest(builtinProfilePath, data)
	if err != nil {
		return nil, err
	}
	if got := strings.TrimSpace(manifest.PackNamespace); got != BuiltinNamespace {
		return nil, fmt.Errorf("agent pack manifest %s pack_namespace = %q, want %q", builtinProfilePath, got, BuiltinNamespace)
	}
	source := manifestProfileSource{
		namespace:        BuiltinNamespace,
		source:           SourceBuiltin,
		packPath:         BuiltinNamespace,
		profilePath:      builtinProfilePath,
		preferredTargets: []string{"local"},
		loadContents:     loadBuiltinPackContents,
	}
	loaded, err := listManifestProfilesFromSource(source, manifest)
	if err != nil {
		return nil, err
	}
	profiles := make(map[string]Profile, len(loaded))
	for _, profile := range loaded {
		metadata, ok := builtinCompatibility[profile.Name]
		if !ok {
			return nil, fmt.Errorf("builtin profile %q has no compatibility metadata", profile.Name)
		}
		profile.Description = metadata.description
		if metadata.agents != "" {
			profile.Agents = metadata.agents
		}
		profile.SkillDirs = cloneStrings(metadata.skillDirs)
		profile.Placement.Requires.Features = cloneStrings(metadata.features)
		profiles[profile.Name] = profile
	}
	if len(profiles) != len(builtinCompatibility) {
		return nil, fmt.Errorf("builtin compatibility metadata contains %d profiles, manifest contains %d", len(builtinCompatibility), len(profiles))
	}
	return profiles, nil
}

func loadBuiltinPackContents(refs []string) (string, error) {
	var b strings.Builder
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		clean := path.Clean(ref)
		if path.IsAbs(ref) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return "", fmt.Errorf("profile content ref %q must be relative to builtin pack root", ref)
		}
		data, err := fs.ReadFile(builtinProfileFS, clean)
		if err != nil {
			return "", fmt.Errorf("read profile content %s: %w", ref, err)
		}
		b.WriteString(ensureTrailingNewline(string(data)))
	}
	return b.String(), nil
}

func builtinName(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	parts := strings.Split(ref, "/")
	switch len(parts) {
	case 1:
		return parts[0], parts[0] != ""
	case 2:
		if parts[0] != BuiltinNamespace || parts[1] == "" {
			return "", false
		}
		return parts[1], true
	default:
		return "", false
	}
}

func cloneProfile(profile Profile) Profile {
	profile.Skills = cloneStrings(profile.Skills)
	profile.Subagents = cloneStrings(profile.Subagents)
	profile.SkillDirs = cloneStrings(profile.SkillDirs)
	profile.SnapshotSkillFiles = cloneProfileSkillFiles(profile.SnapshotSkillFiles)
	profile.ExternalSkillDirs = cloneStringMap(profile.ExternalSkillDirs)
	profile.DefaultArgs = cloneStrings(profile.DefaultArgs)
	profile.DefaultEnv = cloneStrings(profile.DefaultEnv)
	profile.Envs = cloneStrings(profile.Envs)
	profile.EnvRefs = cloneStrings(profile.EnvRefs)
	profile.SupportedModels = cloneStrings(profile.SupportedModels)
	profile.CredentialEnvSuggestions = cloneStringMap(profile.CredentialEnvSuggestions)
	if profile.ModelOptions != nil {
		modelOptions := profile.ModelOptions
		profile.ModelOptions = make(map[string]ModelOption, len(modelOptions))
		for ref, option := range modelOptions {
			option.Provider = cloneProvider(option.Provider)
			profile.ModelOptions[ref] = option
		}
	}
	profile.Placement.PreferredTargets = cloneStrings(profile.Placement.PreferredTargets)
	profile.Placement.Requires.AgentTypes = cloneStrings(profile.Placement.Requires.AgentTypes)
	profile.Placement.Requires.RuntimeTools = cloneStrings(profile.Placement.Requires.RuntimeTools)
	profile.Placement.Requires.Features = cloneStrings(profile.Placement.Requires.Features)
	profile.Placement.Requires.Credentials = cloneStrings(profile.Placement.Requires.Credentials)
	profile.Provider = cloneProvider(profile.Provider)
	return profile
}

func cloneProfileSkillFiles(files []ProfileSkillFile) []ProfileSkillFile {
	out := make([]ProfileSkillFile, len(files))
	for i, file := range files {
		out[i] = file
		out[i].Data = append([]byte(nil), file.Data...)
	}
	return out
}

func cloneProvider(provider *Provider) *Provider {
	if provider == nil {
		return nil
	}
	next := *provider
	if provider.ByAzure != nil {
		v := *provider.ByAzure
		next.ByAzure = &v
	}
	if provider.DisableFailover != nil {
		v := *provider.DisableFailover
		next.DisableFailover = &v
	}
	if provider.Thinking != nil {
		next.Thinking = map[string]string{}
		for k, v := range provider.Thinking {
			next.Thinking[k] = v
		}
	}
	return &next
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
