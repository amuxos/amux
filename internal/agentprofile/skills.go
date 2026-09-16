package agentprofile

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// SkillInfo describes a single skill declared by an Agent Profile: its name and
// the human-readable capability summary parsed from the skill's SKILL.md
// frontmatter description. Description is empty when the skill directory or its
// SKILL.md cannot be located (e.g. a pack declares a skill but ships no skill
// tree on this host).
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ResolveSkills maps the profile's declared skill names to SkillInfo entries,
// resolving each skill's SKILL.md description from the source the profile was
// loaded from: the embedded builtin FS for builtin profiles, or the
// materialized PackPath on disk for Git-backed pack profiles. One entry is always
// emitted per declared skill (order preserved); Description degrades to "" when
// the SKILL.md is absent or unreadable. ResolveSkills never errors — it is a
// best-effort enrichment used by discovery surfaces.
func (p Profile) ResolveSkills() []SkillInfo {
	if len(p.Skills) == 0 {
		return nil
	}
	dirs := p.SkillDirs
	if p.Source == SourceBuiltin || len(dirs) == 0 {
		dirs = defaultSkillDirs()
	}
	infos := make([]SkillInfo, 0, len(p.Skills))
	for _, skill := range p.Skills {
		name := strings.TrimSpace(skill)
		if name == "" {
			continue
		}
		infos = append(infos, SkillInfo{Name: name, Description: p.resolveSkillDescription(name, dirs)})
	}
	return infos
}

func (p Profile) resolveSkillDescription(skill string, dirs []string) string {
	// An installed Git-backed pack's same-named skill overrides the embedded or
	// own-pack copy for description display too, keeping discovery surfaces
	// consistent with what actually gets projected.
	if src := externalSkillOverride(p, skill); src != "" {
		data, err := os.ReadFile(filepath.Join(src, "SKILL.md"))
		if err != nil {
			return ""
		}
		return parseSkillDescription(data)
	}
	if p.Source == SourceBuiltin {
		dir, ok := findBuiltinSkillDir(dirs, skill)
		if !ok {
			return ""
		}
		data, err := fs.ReadFile(builtinProfileFS, path.Join(dir, "SKILL.md"))
		if err != nil {
			return ""
		}
		return parseSkillDescription(data)
	}
	if strings.TrimSpace(p.PackPath) == "" {
		return ""
	}
	dir, ok, err := findProfileSkillDir(p.PackPath, dirs, skill)
	if err != nil || !ok {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return ""
	}
	return parseSkillDescription(data)
}

// parseSkillDescription extracts the `description` value from a SKILL.md YAML
// frontmatter block. It returns an empty string when no frontmatter description
// is present.
func parseSkillDescription(data []byte) string {
	lines := strings.Split(string(data), "\n")
	inFrontMatter := false
	started := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "---" {
			if !started {
				started = true
				inFrontMatter = true
				continue
			}
			break
		}
		started = true
		if inFrontMatter && line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			if !inFrontMatter {
				break
			}
			continue
		}
		if strings.TrimSpace(key) == "description" {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}
