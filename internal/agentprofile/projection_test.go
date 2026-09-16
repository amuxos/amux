package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderProfileWorkspaceWritesAgentsContent(t *testing.T) {
	profile := Profile{
		Namespace:     "team",
		Name:          "coder",
		Type:          "codex",
		Agents:        "AGENTS.md",
		AgentsContent: "# Instructions\n\nUse the shared agent playbook.\n",
	}
	root := t.TempDir()

	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}

	if !containsString(result.Files, "AGENTS.md") {
		t.Fatalf("files = %#v, want AGENTS.md", result.Files)
	}
	assertFileContains(t, filepath.Join(root, "AGENTS.md"), "Use the shared agent playbook.")
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if strings.Contains(string(data), "amux runtime identity") {
		t.Fatalf("AGENTS.md guessed runtime identity for empty model:\n%s", data)
	}
}

func TestRenderProfileWorkspaceUsesClaudeImportAndSkillsDirectory(t *testing.T) {
	packRoot := t.TempDir()
	skillRoot := filepath.Join(packRoot, "skills", "code-review")
	if err := os.MkdirAll(skillRoot, 0700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: code-review\ndescription: Review code\n---\n"), 0600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	profile := Profile{
		Namespace:     "team",
		Name:          "claude-reviewer",
		Type:          "claude",
		Agents:        "AGENTS.md",
		AgentsContent: "# Instructions\n\nReview code carefully.\n",
		Skills:        []string{"code-review"},
		SkillDirs:     []string{"skills"},
		Source:        SourcePack,
		PackPath:      packRoot,
	}
	root := t.TempDir()

	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}

	for _, want := range []string{"AGENTS.md", "CLAUDE.md", ".claude/skills/code-review/SKILL.md"} {
		if !containsString(result.Files, want) {
			t.Fatalf("files = %#v, want %s", result.Files, want)
		}
	}
	assertFileContains(t, filepath.Join(root, "CLAUDE.md"), "[AGENTS.md](AGENTS.md)")
	assertFileContains(t, filepath.Join(root, "AGENTS.md"), "Review code carefully.")
	assertFileContains(t, filepath.Join(root, ".claude", "skills", "code-review", "SKILL.md"), "code-review")
}

func TestRenderProfileWorkspaceSkipsCodexProjectionFileWithoutContent(t *testing.T) {
	profile, ok := ResolveBuiltin("codex")
	if !ok {
		t.Fatal("missing builtin/codex")
	}
	profile.Model = "gpt-5-codex"
	root := t.TempDir()

	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}

	if containsString(result.Files, "AGENTS.md") {
		t.Fatalf("files = %#v, do not want AGENTS.md without agent-facing content", result.Files)
	}
	assertFileNotExists(t, filepath.Join(root, "AGENTS.md"))
	assertFileNotExists(t, filepath.Join(root, "amux-profile.json"))
}

func TestRenderProfileWorkspaceCopiesCodexSkillsToAgentsDirectory(t *testing.T) {
	packRoot := t.TempDir()
	skillRoot := filepath.Join(packRoot, "skills", "code-review")
	if err := os.MkdirAll(skillRoot, 0700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: code-review\ndescription: Review code\n---\n"), 0600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	profile := Profile{
		Namespace: "team",
		Name:      "reviewer",
		Type:      "codex",
		Skills:    []string{"code-review"},
		SkillDirs: []string{"skills"},
		Source:    SourcePack,
		PackPath:  packRoot,
	}
	root := t.TempDir()

	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}

	for _, want := range []string{".agents/skills/code-review/SKILL.md"} {
		if !containsString(result.Files, want) {
			t.Fatalf("files = %#v, want %s", result.Files, want)
		}
	}
	if containsString(result.Files, "AGENTS.md") {
		t.Fatalf("files = %#v, do not want AGENTS.md for skill-only profile", result.Files)
	}
	assertFileNotExists(t, filepath.Join(root, "AGENTS.md"))
	assertFileContains(t, filepath.Join(root, ".agents", "skills", "code-review", "SKILL.md"), "code-review")
}

func TestRenderProfileWorkspaceFindsDefaultNestedSkillDirs(t *testing.T) {
	packRoot := t.TempDir()
	skillRoot := filepath.Join(packRoot, "skills", "upstream", "service-health")
	if err := os.MkdirAll(skillRoot, 0700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: service-health\ndescription: Service runbook\n---\n"), 0600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	profile := Profile{
		Namespace: "team",
		Name:      "service-health",
		Type:      "codex",
		Skills:    []string{"service-health"},
		Source:    SourcePack,
		PackPath:  packRoot,
	}
	root := t.TempDir()

	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}

	if !containsString(result.Files, ".agents/skills/service-health/SKILL.md") {
		t.Fatalf("files = %#v, want nested upstream skill", result.Files)
	}
	assertFileContains(t, filepath.Join(root, ".agents", "skills", "service-health", "SKILL.md"), "service-health")
}

func TestLocalProfileSourceDerivesWritableAcrossPBRoundTrip(t *testing.T) {
	profile := Profile{
		Namespace: "local",
		Name:      "reviewer",
		Revision:  "abc123",
		Type:      "codex",
		Source:    SourceLocal,
		Writable:  true,
	}

	roundTripped, err := FromPBProfileSpec(ToPBProfileSpec(profile))
	if err != nil {
		t.Fatalf("FromPBProfileSpec: %v", err)
	}
	if roundTripped.Source != SourceLocal || !roundTripped.Writable {
		t.Fatalf("round-tripped local profile = %+v", roundTripped)
	}
}

func TestRenderProfileWorkspaceRemovesLegacyGeneratedFiles(t *testing.T) {
	profile := Profile{
		Namespace: "builtin",
		Name:      "codex",
		Type:      "codex",
	}
	root := t.TempDir()
	for _, name := range []string{"amux-context.md", "amux-profile.json", "projection-report.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("legacy\n"), 0600); err != nil {
			t.Fatalf("write legacy %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# builtin/codex\n\nThis workspace was rendered from amux agent profile `builtin/codex`.\n"), 0600); err != nil {
		t.Fatalf("write legacy AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("[AGENTS.md](AGENTS.md)\n"), 0600); err != nil {
		t.Fatalf("write legacy CLAUDE.md: %v", err)
	}

	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}

	if containsString(result.Files, "AGENTS.md") {
		t.Fatalf("files = %#v, do not want AGENTS.md without agent-facing content", result.Files)
	}
	for _, name := range []string{"amux-context.md", "amux-profile.json", "projection-report.md", "AGENTS.md", "CLAUDE.md"} {
		assertFileNotExists(t, filepath.Join(root, name))
		if containsString(result.Files, name) {
			t.Fatalf("files = %#v, do not want %s", result.Files, name)
		}
	}
	assertFileNotExists(t, filepath.Join(root, "AGENTS.md"))
}

func TestRenderProfileWorkspaceKeepsUserAgentsFileWithoutContent(t *testing.T) {
	profile := Profile{
		Namespace: "builtin",
		Name:      "codex",
		Type:      "codex",
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Project Instructions\n\nKeep this file.\n"), 0600); err != nil {
		t.Fatalf("write user AGENTS.md: %v", err)
	}

	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}

	if containsString(result.Files, "AGENTS.md") {
		t.Fatalf("files = %#v, do not want AGENTS.md without agent-facing content", result.Files)
	}
	assertFileContains(t, filepath.Join(root, "AGENTS.md"), "Keep this file.")
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s missing %q:\n%s", path, want, string(data))
	}
}

func assertFileNotContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(data), want) {
		t.Fatalf("%s contains %q, want absent:\n%s", path, want, string(data))
	}
}

func assertFileNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s exists, want absent", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func boolPtr(v bool) *bool {
	return &v
}
