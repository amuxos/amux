package agentprofile

import (
	"path/filepath"
	"strings"
)

// attachExternalSkillOverrides scans formal installed Git-backed packs for skill
// directories with the same (logical) name as skills declared by the profile,
// and records the first match as that skill's external source. When an override
// exists it wins over the embedded source (priority: external pack > embedded).
//
// Conflicts are resolved deterministically: packs are iterated in ListPacks
// order (namespace lexicographic), and within a pack the default skill
// directories are searched in order, so the first name hit is stable.
//
// The function never fails structurally: a missing/unreadable registry or a
// missing override simply leaves the profile unchanged, falling back to the
// embedded skill exactly as before. This keeps profile resolution non-blocking
// when no external packs are installed.
func attachExternalSkillOverrides(profile Profile, registryPath string) (Profile, error) {
	if profile.Source != SourceBuiltin || len(profile.Skills) == 0 || strings.TrimSpace(registryPath) == "" {
		return profile, nil
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		return profile, nil
	}
	return attachExternalSkillOverridesFromRegistry(profile, registry), nil
}

// AttachExternalSkillOverrides is the exported wrapper for callers that already
// hold a loaded registry (CLI discovery + MCP), avoiding a second registry load.
// Builtin profiles get same-named Git-pack skills attached; non-builtin profiles
// keep their own PackPath resolution and are returned unchanged.
func AttachExternalSkillOverrides(profile Profile, registry PackRegistry) Profile {
	return attachExternalSkillOverridesFromRegistry(profile, registry)
}

func attachExternalSkillOverridesFromRegistry(profile Profile, registry PackRegistry) Profile {
	if profile.Source != SourceBuiltin || len(profile.Skills) == 0 {
		return profile
	}
	profile.ExternalSkillDirs = cloneStringMap(profile.ExternalSkillDirs)
	dirs := defaultSkillDirs()
	for _, pack := range ListPacks(registry) {
		if pack.SourceKind != PackSourceGit || pack.IsDevelopment() || strings.TrimSpace(pack.Namespace) == ManagedLocalPackNamespace {
			continue
		}
		packPath := strings.TrimSpace(pack.MaterializedPath)
		if packPath == "" {
			continue
		}
		for _, skill := range profile.Skills {
			skill = strings.TrimSpace(skill)
			if skill == "" {
				continue
			}
			if _, already := profile.ExternalSkillDirs[skill]; already {
				continue // first (lowest-namespace) pack wins
			}
			dir, ok, err := findProfileSkillDir(packPath, dirs, skill)
			if err != nil || !ok {
				continue
			}
			if profile.ExternalSkillDirs == nil {
				profile.ExternalSkillDirs = map[string]string{}
			}
			profile.ExternalSkillDirs[skill] = filepath.Clean(dir)
		}
	}
	return profile
}
