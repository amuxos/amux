package agentprofile

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
)

type ProjectionResult struct {
	Root       string
	ProfileRef string
	AgentType  string
	Files      []string
}

func RenderProfileWorkspace(profile Profile, root string) (ProjectionResult, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return ProjectionResult{}, fmt.Errorf("render root is required")
	}
	if profile.Ref() == "" || strings.TrimSpace(profile.Type) == "" {
		return ProjectionResult{}, fmt.Errorf("profile ref and type are required")
	}
	if err := validateDistributionProfile(profile); err != nil {
		return ProjectionResult{}, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return ProjectionResult{}, err
	}
	if err := cleanupLegacyProjectionFiles(root); err != nil {
		return ProjectionResult{}, err
	}
	result := ProjectionResult{
		Root:       root,
		ProfileRef: profile.Ref(),
		AgentType:  profile.Type,
	}
	providerFiles := providerProjectionFiles(profile)
	for _, file := range providerFiles {
		if err := writeProjectionFile(root, file.name, file.body(profile)); err != nil {
			return ProjectionResult{}, err
		}
		result.Files = append(result.Files, file.name)
	}
	for _, file := range providerConfigProjectionFiles(profile) {
		if err := writeProjectionFile(root, file.name, file.body(profile)); err != nil {
			return ProjectionResult{}, err
		}
		result.Files = append(result.Files, file.name)
	}
	distributionConfigFile, err := materializeDistributionConfig(profile, root)
	if err != nil {
		return ProjectionResult{}, err
	}
	if distributionConfigFile != "" {
		result.Files = append(result.Files, distributionConfigFile)
	}
	piConfigFiles, err := materializePiConfig(profile, root)
	if err != nil {
		return ProjectionResult{}, err
	}
	result.Files = append(result.Files, piConfigFiles...)
	skillFiles, err := projectSkillDirectories(profile, root)
	if err != nil {
		return ProjectionResult{}, err
	}
	result.Files = append(result.Files, skillFiles...)
	return result, nil
}

// AttachLocalPackPath enriches a resolved profile snapshot with target-local
// pack location metadata. Most profile specs only identify skills by name;
// local and evaluation Task snapshots are the exception and carry their files
// directly so another runtime can reproduce the exact source tree.
func AttachLocalPackPath(profile Profile, registryPath string) (Profile, error) {
	if strings.TrimSpace(profile.PackPath) != "" || len(profile.Skills) == 0 {
		return profile, nil
	}
	registryPath = strings.TrimSpace(registryPath)
	if registryPath == "" {
		return profile, nil
	}
	if profile.Namespace == "" || profile.Namespace == BuiltinNamespace {
		// Builtin profiles keep their own namespace pack (none); but their
		// embedded skills may still be overridden by any installed Git-backed
		// pack that ships a same-named skill (external > embedded). Attach those
		// so projection uses the periodically-synced version when present.
		return attachExternalSkillOverrides(profile, registryPath)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		return Profile{}, err
	}
	pack, ok := registry.Packs[profile.Namespace]
	if !ok {
		return profile, nil
	}
	if profile.Revision != "" && strings.TrimSpace(pack.CurrentRevision) != profile.Revision {
		if profile.Source == SourceLocal {
			historicalPath := managedRevisionMaterializedPath(registryPath, profile.Revision)
			if info, err := os.Stat(historicalPath); err == nil && info.IsDir() {
				profile.PackPath = historicalPath
			}
		}
		return profile, nil
	}
	profile.PackPath = pack.MaterializedPath
	return profile, nil
}

type providerProjection struct {
	name string
	body func(Profile) string
}

func providerProjectionFiles(profile Profile) []providerProjection {
	body := agentFacingProjectionBody(profile)
	if strings.TrimSpace(body) == "" {
		return nil
	}
	switch strings.TrimSpace(profile.Type) {
	case "claude":
		return []providerProjection{
			{name: "AGENTS.md", body: func(Profile) string { return body }},
			{name: "CLAUDE.md", body: claudeProjectionBody},
		}
	default:
		return []providerProjection{{name: providerInstructionsFile(profile.Type), body: func(Profile) string { return body }}}
	}
}

// Workspace projections use the adapter's relative layout paths. A home-relative
// skill layout can also be rendered into a task workspace (for example Claude).
func providerLayoutPath(agentType string, surface adapter.Surface) string {
	layout, err := adapter.Layout(strings.TrimSpace(agentType))
	if err != nil {
		return ""
	}
	placement := layout[surface]
	if !placement.Supported || (placement.Kind != "cwd-rel" && placement.Kind != "home-rel") {
		return ""
	}
	return filepath.FromSlash(placement.Path)
}

func providerInstructionsFile(agentType string) string {
	if path := providerLayoutPath(agentType, adapter.SurfaceRules); path != "" {
		return path
	}
	return "amux-context.md"
}

func providerSkillProjectionDir(agentType string) string {
	return providerLayoutPath(agentType, adapter.SurfaceSkills)
}

func projectSkillDirectories(profile Profile, root string) ([]string, error) {
	dstBase := providerSkillProjectionDir(profile.Type)
	if dstBase == "" || len(profile.Skills) == 0 {
		return nil, nil
	}
	if len(profile.SnapshotSkillFiles) > 0 {
		return projectSnapshotSkillFiles(profile, root, dstBase)
	}
	// Built-in profiles vendor their skills inside the amux binary rather than a
	// materialized pack on disk: PackPath is the "builtin" sentinel and never a
	// real directory. Project those skill trees straight from the embedded FS.
	if profile.Source == SourceBuiltin {
		return projectBuiltinSkillDirectories(profile, root, dstBase)
	}
	if strings.TrimSpace(profile.PackPath) == "" {
		return nil, nil
	}
	dirs := profile.SkillDirs
	if len(dirs) == 0 {
		dirs = defaultSkillDirs()
	}
	var files []string
	for _, skill := range profile.Skills {
		// An installed Git-backed pack's same-named skill overrides this pack's
		// own copy (external > embedded), mirroring the builtin path below.
		if src := externalSkillOverride(profile, skill); src != "" {
			copied, err := copySkillDir(src, skill, dstBase, root)
			if err != nil {
				return nil, err
			}
			files = append(files, copied...)
			continue
		}
		src, ok, err := findProfileSkillDir(profile.PackPath, dirs, skill)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		dstRel := filepath.Join(dstBase, skill)
		dst := filepath.Join(root, dstRel)
		if err := copyDir(src, dst); err != nil {
			return nil, fmt.Errorf("project skill %s: %w", skill, err)
		}
		copied, err := listFilesRelative(root, dst)
		if err != nil {
			return nil, err
		}
		files = append(files, copied...)
	}
	return files, nil
}

// externalSkillOverride returns the disk source directory for a skill when an
// installed Git-backed pack overrides it (external > embedded), else "".
func externalSkillOverride(profile Profile, skill string) string {
	skill = strings.TrimSpace(skill)
	if skill == "" {
		return ""
	}
	src := strings.TrimSpace(profile.ExternalSkillDirs[skill])
	if src == "" {
		return ""
	}
	if info, err := os.Stat(src); err == nil && info.IsDir() {
		return src
	}
	return ""
}

// copySkillDir projects a disk skill directory into the provider skill root and
// returns the relative file list. Shared by builtin and pack projection paths.
func copySkillDir(src, skill, dstBase, root string) ([]string, error) {
	dstRel := filepath.Join(dstBase, skill)
	dst := filepath.Join(root, dstRel)
	if err := copyDir(src, dst); err != nil {
		return nil, fmt.Errorf("project skill %s: %w", skill, err)
	}
	return listFilesRelative(root, dst)
}

func copyDir(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill source contains symlink: %s", path)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// projectBuiltinSkillDirectories projects skill trees from the embedded standard
// Agent Pack into the rendered workspace. skill_dirs are interpreted relative to
// the Pack root, mirroring materialized PackPath behavior.
func projectBuiltinSkillDirectories(profile Profile, root, dstBase string) ([]string, error) {
	// Profile.SkillDirs retains the legacy public snapshot/display value for
	// builtin profiles. The embedded source is now a standard Agent Pack, so
	// runtime lookup always follows the Contract's canonical asset directories.
	dirs := defaultSkillDirs()
	var files []string
	for _, skill := range profile.Skills {
		// An installed Git-backed pack's same-named skill overrides the embedded
		// copy (external > embedded), so profiles pick up autosync'd skill
		// updates without a binary release.
		if src := externalSkillOverride(profile, skill); src != "" {
			copied, err := copySkillDir(src, skill, dstBase, root)
			if err != nil {
				return nil, err
			}
			files = append(files, copied...)
			continue
		}
		src, ok := findBuiltinSkillDir(dirs, skill)
		if !ok {
			continue
		}
		dstRel := filepath.Join(dstBase, skill)
		dst := filepath.Join(root, dstRel)
		if err := copyEmbedDir(builtinProfileFS, src, dst); err != nil {
			return nil, fmt.Errorf("project skill %s: %w", skill, err)
		}
		copied, err := listFilesRelative(root, dst)
		if err != nil {
			return nil, err
		}
		files = append(files, copied...)
	}
	return files, nil
}

func findBuiltinSkillDir(dirs []string, skill string) (string, bool) {
	skill = strings.TrimSpace(skill)
	if skill == "" {
		return "", false
	}
	for _, dir := range dirs {
		candidate := path.Join(path.Clean(filepath.ToSlash(dir)), skill)
		info, err := fs.Stat(builtinProfileFS, candidate)
		if err == nil && info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

// copyEmbedDir recursively copies the src subtree of fsys onto disk at dst.
// It mirrors copyDir but reads from an fs.FS (the embedded builtin FS).
func copyEmbedDir(fsys fs.FS, src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return fs.WalkDir(fsys, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, filepath.FromSlash(p))
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		// source.toml governs the source pack; it is not an agent-facing skill
		// asset and was not present in the legacy builtin projection.
		if path.Base(p) == "source.toml" {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
}

func defaultSkillDirs() []string {
	return []string{"skills", "skills/upstream", "skills/custom"}
}

func findProfileSkillDir(packRoot string, dirs []string, skill string) (string, bool, error) {
	skill = strings.TrimSpace(skill)
	if skill == "" {
		return "", false, nil
	}
	for _, dir := range dirs {
		path := filepath.Join(packRoot, filepath.FromSlash(dir), skill)
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return path, true, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
}

func listFilesRelative(root, dir string) ([]string, error) {
	var files []string
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return nil, err
	}
	return files, nil
}

func agentFacingProjectionBody(profile Profile) string {
	if strings.TrimSpace(profile.AgentsContent) == "" {
		return ""
	}
	body := ensureTrailingNewline(profile.AgentsContent)
	model := strings.TrimSpace(profile.Model)
	if model == "" {
		return body
	}
	body += "\n"
	body += "## amux runtime identity\n\n"
	body += "This section is runtime-provided session metadata.\n\n"
	body += "The model selected for this session launch is " + strconv.Quote(model) + ".\n"
	body += "When asked about your current model, use this value. Do not infer model identity from memory or prior conversations.\n"
	return body
}

func claudeProjectionBody(Profile) string {
	return "[AGENTS.md](AGENTS.md)\n"
}

func writeProjectionFile(root, name, body string) error {
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0600)
}

func cleanupLegacyProjectionFiles(root string) error {
	for _, name := range []string{"amux-context.md", "amux-profile.json", "projection-report.md"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err := removeIfLegacyProjection(filepath.Join(root, name)); err != nil {
			return err
		}
	}
	return nil
}

func removeIfLegacyProjection(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	text := strings.TrimSpace(string(data))
	if strings.Contains(text, "This workspace was rendered from amux agent profile") || text == "[AGENTS.md](AGENTS.md)" {
		return os.Remove(path)
	}
	return nil
}

func ensureTrailingNewline(value string) string {
	if strings.HasSuffix(value, "\n") {
		return value
	}
	return value + "\n"
}
