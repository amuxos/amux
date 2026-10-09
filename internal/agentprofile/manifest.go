package agentprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const packManifestRelPath = "dist/agent-pack.manifest.json"
const supportedPackManifestSchemaVersion = "v2"

type packManifest struct {
	SchemaVersion string                           `json:"schema_version"`
	PackNamespace string                           `json:"pack_namespace"`
	Profiles      map[string]packManifestProfile   `json:"profiles"`
	Namespaces    map[string]packManifestNamespace `json:"namespaces"`
}

type packManifestProfile struct {
	Namespace    string   `json:"namespace"`
	Description  string   `json:"description"`
	Instructions []string `json:"instructions"`
	Skills       []string `json:"skills"`
	Subagents    []string `json:"subagents"`
	Envs         []string `json:"envs"`
	EnvRefs      []string `json:"env_refs"`
	Extends      *string  `json:"extends"`
}

type packManifestNamespace struct {
	Profiles []string `json:"profiles"`
}

// LoadPackManifest reads an agent-pack manifest file. The versioned
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
	manifest.SchemaVersion = strings.TrimSpace(manifest.SchemaVersion)
	if strings.TrimSpace(manifest.SchemaVersion) != supportedPackManifestSchemaVersion {
		return manifest, profileError("schema_unsupported", strings.TrimSpace(manifest.SchemaVersion), path)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return manifest, err
	}
	for key := range raw {
		if strings.EqualFold(key, "providers") {
			return manifest, profileError("pack_providers_forbidden")
		}
	}
	var entries struct {
		Profiles map[string]map[string]json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return manifest, err
	}
	for name, entry := range entries.Profiles {
		for key := range entry {
			switch strings.ToLower(key) {
			case "type", "model", "providers", "supported_models", "permission":
				return manifest, profileError("execution_field_forbidden", name, key)
			}
		}
	}
	if strings.TrimSpace(manifest.PackNamespace) == "" {
		return manifest, fmt.Errorf("agent pack manifest %s pack_namespace is required", path)
	}
	if len(manifest.Profiles) == 0 {
		return manifest, fmt.Errorf("agent pack manifest %s profiles must contain at least one profile", path)
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
	instructions := cleanStrings(entry.Instructions)
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
	return Profile{
		Namespace:     source.namespace,
		Name:          name,
		Revision:      source.revision,
		Extends:       extends,
		Description:   strings.TrimSpace(entry.Description),
		Agents:        strings.Join(instructions, ", "),
		AgentsContent: agentsContent,
		Skills:        cleanStrings(entry.Skills),
		Subagents:     cleanStrings(entry.Subagents),
		Envs:          cleanStrings(entry.Envs),
		EnvRefs:       cleanStrings(entry.EnvRefs),
		Source:        source.source,
		Writable:      source.source == SourceLocal,
		PackPath:      source.packPath,
		ProfilePath:   source.profilePath,
		Placement: Placement{
			PreferredTargets: cloneStrings(source.preferredTargets),
		},
	}, nil
}

func stringSliceContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
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
