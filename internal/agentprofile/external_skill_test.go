package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// writeExternalSkillPack builds a minimal materialized Git pack whose skill dir
// lives at skills/<sub>/<name> (sub is upstream|custom) and returns the Pack.
func writeExternalSkillPack(t *testing.T, ns, sub, skill, skillBody string) Pack {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "skills", sub, skill)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillBody), 0600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return Pack{
		Namespace:        ns,
		SourceKind:       PackSourceGit,
		MaterializedPath: root,
		CurrentRevision:  "abc123",
	}
}

// TestAttachExternalSkillOverridesBuiltinAttachesSameNamedPackSkill verifies a
// builtin profile (e.g. builtin/reviewer) gets an external override for a skill
// when an installed Git pack ships a same-named dir.
func TestAttachExternalSkillOverridesBuiltin(t *testing.T) {
	pack := writeExternalSkillPack(t, "team", "custom", "external-tool",
		"---\nname: external-tool\ndescription: EXTERNAL external-tool\n---\n# ext\n")
	registry := PackRegistry{Packs: map[string]Pack{"team": pack}}

	profile := externalSkillBuiltinFixture(t)
	attached := AttachExternalSkillOverrides(profile, registry)
	if got := attached.ExternalSkillDirs["external-tool"]; got == "" {
		t.Fatalf("ExternalSkillDirs[external-tool] empty, want external dir: %+v", attached.ExternalSkillDirs)
	}

	// ResolveSkills description must come from the external pack.
	skills := attached.ResolveSkills()
	for _, s := range skills {
		if s.Name == "external-tool" && s.Description != "EXTERNAL external-tool" {
			t.Fatalf("external-tool description = %q, want external EXTERNAL external-tool", s.Description)
		}
	}
}

// TestAttachExternalSkillOverridesNoPackKeepsEmbedded verifies that without an
// installed Git pack, the embedded builtin skill stays the source (no override,
// no behavioral change).
func TestAttachExternalSkillOverridesNoPack(t *testing.T) {
	profile := externalSkillBuiltinFixture(t)
	attached := AttachExternalSkillOverrides(profile, PackRegistry{Packs: map[string]Pack{}})
	if len(attached.ExternalSkillDirs) != 0 {
		t.Fatalf("ExternalSkillDirs = %+v, want empty without packs", attached.ExternalSkillDirs)
	}
	skills := attached.ResolveSkills()
	for _, s := range skills {
		if s.Name == "external-tool" && s.Description == "EXTERNAL external-tool" {
			t.Fatalf("external-tool description = external even without external pack: %q", s.Description)
		}
	}
}

func TestBuiltinSkillOverridesExcludeUnpublishedAndLocalPacks(t *testing.T) {
	for _, candidate := range []Pack{
		{Namespace: "a-preview", DevelopmentOf: "team"},
		{Namespace: "a-scratch", Temporary: true},
		{Namespace: "a-evaluation", Evaluation: true},
		{Namespace: "a-legacy-dev"},
		{Namespace: ManagedLocalPackNamespace, ManagedBy: ManagedPackOwnerAmux},
	} {
		t.Run(candidate.Namespace, func(t *testing.T) {
			unpublished := writeExternalSkillPack(t, candidate.Namespace, "custom", "external-tool", "UNPUBLISHED")
			candidate.SourceKind, candidate.MaterializedPath = unpublished.SourceKind, unpublished.MaterializedPath
			formal := writeExternalSkillPack(t, "z-team", "custom", "external-tool", "PUBLISHED")
			registry := PackRegistry{Packs: map[string]Pack{candidate.Namespace: candidate, formal.Namespace: formal}}
			profile := AttachExternalSkillOverrides(externalSkillBuiltinFixture(t), registry)
			root := t.TempDir()
			if _, err := RenderProfileWorkspace(profile, root); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(root, ".agents", "skills", "external-tool", "SKILL.md"))
			if err != nil || string(body) != "PUBLISHED" {
				t.Fatalf("rendered skill = %q, err=%v", body, err)
			}
		})
	}
}

func TestExternalProfileKeepsOwnSkillSource(t *testing.T) {
	pack := writeExternalSkillPack(t, "team", "custom", "external-tool", "EXTERNAL")
	profile := Profile{Namespace: "another", Name: "reviewer", Source: SourcePack, Skills: []string{"external-tool"}}
	attached := AttachExternalSkillOverrides(profile, PackRegistry{Packs: map[string]Pack{"team": pack}})
	if len(attached.ExternalSkillDirs) != 0 {
		t.Fatalf("non-builtin override = %v", attached.ExternalSkillDirs)
	}
}

// TestProjectBuiltinSkillWithoutExternalUsesPlaceholder verifies the builtin
// placeholder model: without an external pack the projection carries only the
// placeholder SKILL.md (+ source.toml excluded), i.e. not the full skill tree.
func TestProjectBuiltinSkillWithoutExternalUsesPlaceholder(t *testing.T) {
	profile := externalSkillBuiltinFixture(t)
	// nil ExternalSkillDirs => pure builtin placeholder projection.
	root := t.TempDir()
	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}
	var sourceFiles, toolFiles []string
	for _, f := range result.Files {
		switch {
		case strings.HasPrefix(f, ".agents/skills/source-tool/"):
			sourceFiles = append(sourceFiles, f)
		case strings.HasPrefix(f, ".agents/skills/external-tool/"):
			toolFiles = append(toolFiles, f)
		}
	}
	if len(sourceFiles) > 2 {
		t.Fatalf("source-tool projected %d files, want only placeholder (~1-2): %v", len(sourceFiles), sourceFiles)
	}
	if len(toolFiles) > 2 {
		t.Fatalf("external-tool projected %d files, want only placeholder (~1-2): %v", len(toolFiles), toolFiles)
	}
	if len(sourceFiles) == 0 {
		t.Fatalf("source-tool placeholder should project at least one file")
	}
	// source.toml stays in the source pack.
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "source-tool", "source.toml")); !os.IsNotExist(err) {
		t.Fatalf("source.toml should not project, stat err = %v", err)
	}
}

// TestAttachExternalSkillOverridesMultiplePacksFirstNamespaceWins verifies
// deterministic conflict resolution: the lowest-namespace pack wins.
func TestAttachExternalSkillOverridesMultiplePacks(t *testing.T) {
	packB := writeExternalSkillPack(t, "b-team", "custom", "forge-cli",
		"---\nname: forge-cli\ndescription: B TEAM forge\n---\n")
	packA := writeExternalSkillPack(t, "a-team", "custom", "forge-cli",
		"---\nname: forge-cli\ndescription: A TEAM forge\n---\n")
	registry := PackRegistry{Packs: map[string]Pack{
		"b-team": packB, "a-team": packA,
	}}

	profile := externalSkillBuiltinFixture(t)
	attached := AttachExternalSkillOverrides(profile, registry)
	dir := attached.ExternalSkillDirs["forge-cli"]
	if dir == "" {
		t.Fatalf("ExternalSkillDirs[forge-cli] empty: %+v", attached.ExternalSkillDirs)
	}
	// a-team sorts before b-team, so the a-team dir must have won.
	if !isUnder(packA.MaterializedPath, dir) {
		t.Fatalf("forge-cli dir %q should be under a-team pack %q", dir, packA.MaterializedPath)
	}
	skills := attached.ResolveSkills()
	for _, s := range skills {
		if s.Name == "forge-cli" && s.Description != "A TEAM forge" {
			t.Fatalf("forge-cli description = %q, want A TEAM forge (lowest namespace wins)", s.Description)
		}
	}
}

// TestProjectBuiltinSkillUsesExternalOverride verifies projection prefers the
// external pack copy over the embedded one.
func TestProjectBuiltinSkillUsesExternalOverride(t *testing.T) {
	pack := writeExternalSkillPack(t, "team", "custom", "amux-cli",
		"---\nname: amux-cli\ndescription: EXTERNAL amux-cli\n---\n# external amux-cli body\n")
	registry := PackRegistry{Packs: map[string]Pack{"team": pack}}

	profile := externalSkillBuiltinFixture(t)
	profile = AttachExternalSkillOverrides(profile, registry)
	root := t.TempDir()
	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}
	// builtin/reviewer is a Codex profile, so skills project to .agents/skills/.
	if !containsString(result.Files, ".agents/skills/amux-cli/SKILL.md") {
		t.Fatalf("files = %#v, want .agents/skills/amux-cli/SKILL.md", result.Files)
	}
	data, err := os.ReadFile(filepath.Join(root, ".agents", "skills", "amux-cli", "SKILL.md"))
	if err != nil {
		t.Fatalf("read projected skill: %v", err)
	}
	if got := string(data); got != "---\nname: amux-cli\ndescription: EXTERNAL amux-cli\n---\n# external amux-cli body\n" {
		t.Fatalf("projected skill content = %q, want external body", got)
	}
	// source.toml must stay in the source pack for external skills too.
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "amux-cli", "source.toml")); !os.IsNotExist(err) {
		t.Fatalf("source.toml should not project, stat err = %v", err)
	}
}

// TestAttachExternalSkillOverridesPBOnlyReattachesAtProjection verifies the
// override is not lost when the profile crosses the pb boundary: FromPB loses
// it, but AttachLocalPackPath re-injects it before projection (as amuxlet does).
func TestAttachLocalPackPathReinjectsExternalOnBuiltin(t *testing.T) {
	pack := writeExternalSkillPack(t, "team", "custom", "external-tool",
		"---\nname: external-tool\ndescription: EXTERNAL external-tool\n---\n")
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	registry := PackRegistry{Packs: map[string]Pack{"team": pack}}
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	profile := externalSkillBuiltinFixture(t)
	// Simulate cross-pb: no ExternalSkillDirs in the spec.
	roundTripped, err := FromPBProfileSpec(ToPBProfileSpec(profile))
	if err != nil {
		t.Fatalf("FromPBProfileSpec: %v", err)
	}
	if len(roundTripped.ExternalSkillDirs) != 0 {
		t.Fatalf("pb round trip should drop ExternalSkillDirs, got %+v", roundTripped.ExternalSkillDirs)
	}
	reattached, err := AttachLocalPackPath(roundTripped, registryPath)
	if err != nil {
		t.Fatalf("AttachLocalPackPath: %v", err)
	}
	if got := reattached.ExternalSkillDirs["external-tool"]; got == "" {
		t.Fatalf("AttachLocalPackPath did not re-inject external external-tool: %+v", reattached.ExternalSkillDirs)
	}
}

func isUnder(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Public builtins deliberately ship no skills. Use a self-contained embedded
// fixture to preserve override coverage without depending on a distribution.
func externalSkillBuiltinFixture(t *testing.T) Profile {
	t.Helper()
	previous := builtinProfileFS
	files := fstest.MapFS{}
	skills := []string{"external-tool", "source-tool", "forge-cli", "amux-cli"}
	for _, skill := range skills {
		base := "skills/custom/" + skill + "/"
		files[base+"SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: " + skill + "\ndescription: Embedded " + skill + "\n---\n# placeholder\n")}
		files[base+"source.toml"] = &fstest.MapFile{Data: []byte("source = \"fixture\"\n")}
	}
	builtinProfileFS = files
	t.Cleanup(func() { builtinProfileFS = previous })
	return Profile{Namespace: BuiltinNamespace, Name: "reviewer", Type: "codex", Source: SourceBuiltin, Skills: skills}
}
