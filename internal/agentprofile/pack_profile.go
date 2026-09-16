package agentprofile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var providerEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type packProfilePermission struct {
	PermissionMode string   `json:"permission_mode"`
	AllowedTools   []string `json:"allowed_tools"`
	AskTools       []string `json:"ask_tools"`
}

func ResolvePackProfile(registry PackRegistry, ref string) (Profile, bool, error) {
	namespace, name, revision, ok := splitProfileRef(ref)
	if !ok {
		return Profile{}, false, nil
	}
	if namespace == BuiltinNamespace {
		return Profile{}, false, fmt.Errorf("agent profile namespace %q is reserved", BuiltinNamespace)
	}
	pack, ok := registry.Packs[namespace]
	if !ok {
		return Profile{}, false, nil
	}
	if revision != "" && strings.TrimSpace(pack.CurrentRevision) != revision {
		return Profile{}, false, fmt.Errorf("agent profile %s/%s@%s is not materialized; current revision is %s", namespace, name, revision, strings.TrimSpace(pack.CurrentRevision))
	}
	profile, ok, err := loadPackProfile(pack, name)
	if err != nil || !ok {
		if err == nil && !ok {
			if suggestion, suggestionOK := suggestHyphenatedPackProfileName(pack, name); suggestionOK {
				return Profile{}, false, fmt.Errorf("unknown agent profile %q; did you mean %q: %w", ref, namespace+"/"+suggestion, ErrProfileNotFound)
			}
		}
		return Profile{}, ok, err
	}
	return profile, true, nil
}

func ListPackProfiles(registry PackRegistry) ([]Profile, error) {
	var profiles []Profile
	for _, result := range ListPackProfilesByPack(registry) {
		if result.Err != nil {
			return nil, result.Err
		}
		profiles = append(profiles, result.Profiles...)
	}
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Ref() < profiles[j].Ref()
	})
	return profiles, nil
}

type PackProfilesResult struct {
	Namespace string
	Profiles  []Profile
	Err       error
}

// ListPackProfilesByPack preserves each registered Pack's result so callers
// that render a catalog can isolate an unavailable Pack without hiding healthy
// Packs. Callers that require fail-fast behavior should use ListPackProfiles.
func ListPackProfilesByPack(registry PackRegistry) []PackProfilesResult {
	packs := ListPacks(registry)
	results := make([]PackProfilesResult, 0, len(packs))
	for _, pack := range packs {
		profiles, err := listPackProfiles(pack)
		results = append(results, PackProfilesResult{
			Namespace: pack.Namespace,
			Profiles:  profiles,
			Err:       err,
		})
	}
	return results
}

func listPackProfiles(pack Pack) ([]Profile, error) {
	manifest, err := loadPackManifestForPack(pack)
	if err != nil {
		return nil, err
	}
	return listManifestProfiles(pack, manifest)
}

func loadPackProfile(pack Pack, name string) (Profile, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Profile{}, false, nil
	}
	manifest, err := loadPackManifestForPack(pack)
	if err != nil {
		return Profile{}, false, err
	}
	return loadManifestPackProfile(pack, manifest, name)
}

func suggestHyphenatedPackProfileName(pack Pack, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if !strings.Contains(name, "_") {
		return "", false
	}
	candidate := strings.ReplaceAll(name, "_", "-")
	if candidate == name {
		return "", false
	}
	ok, err := packProfileNameExists(pack, candidate)
	return candidate, err == nil && ok
}

func packProfileNameExists(pack Pack, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	manifest, err := loadPackManifestForPack(pack)
	if err != nil {
		return false, err
	}
	_, exists := manifest.Profiles[name]
	return exists, nil
}

func permissionModeArgs(agentType string, permission packProfilePermission) ([]string, error) {
	mode := strings.TrimSpace(permission.PermissionMode)
	if mode == "" {
		return nil, nil
	}
	switch mode {
	case "default", "plan", "bypass":
	default:
		return nil, fmt.Errorf("[permission].permission_mode must be one of default, plan, bypass (got %q)", mode)
	}
	switch profileConfigProtocol(agentType) {
	case "codex":
		value := map[string]map[string]string{
			"default": {"approval_policy": "never", "sandbox_mode": "workspace-write"},
			"plan":    {"approval_policy": "on-request", "sandbox_mode": "read-only"},
			"bypass":  {"approval_policy": "never", "sandbox_mode": "danger-full-access"},
		}[mode]
		return []string{
			"-c", fmt.Sprintf("approval_policy=%q", value["approval_policy"]),
			"-c", fmt.Sprintf("sandbox_mode=%q", value["sandbox_mode"]),
		}, nil
	case "claude":
		value := map[string]string{
			"default": "default",
			"plan":    "plan",
			"bypass":  "bypassPermissions",
		}[mode]
		return []string{"--permission-mode", value}, nil
	default:
		return distributionPermissionModeArgs(agentType, mode)
	}
}

func splitProfileRef(ref string) (namespace, name, revision string, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", "", false
	}
	base, rev, _ := strings.Cut(ref, "@")
	parts := strings.Split(base, "/")
	switch {
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return parts[0], parts[1], rev, true
	default:
		return "", "", "", false
	}
}

func loadPackContent(root, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	path, err := packContentPath(root, ref)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read profile content %s: %w", ref, err)
	}
	return string(data), nil
}

func packContentPath(root, ref string) (string, error) {
	if filepath.IsAbs(ref) {
		return "", fmt.Errorf("profile content ref %q must be relative to the pack root", ref)
	}
	root = filepath.Clean(root)
	path := filepath.Clean(filepath.Join(root, ref))
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("profile content ref %q escapes the pack root", ref)
	}
	return path, nil
}

func cleanStrings(values []string) []string {
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
