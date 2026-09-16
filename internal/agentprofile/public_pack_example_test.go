package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicPackExampleUsesManifestAndExplicitProviderSelection(t *testing.T) {
	pack := Pack{Namespace: "team", MaterializedPath: filepath.Join("..", "..", "examples", "public-pack"), CurrentRevision: "example"}
	if err := ValidatePackManifest(pack); err != nil {
		t.Fatal(err)
	}
	profile, ok, err := ResolvePackProfile(PackRegistry{Packs: map[string]Pack{"team": pack}}, "team/reviewer")
	if err != nil || !ok {
		t.Fatalf("example profile: found=%v error=%v", ok, err)
	}
	native, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{})
	if err != nil || native.Model != "gpt-5.4" || native.ProfileSpec.Provider != nil {
		t.Fatalf("native default: %+v error=%v", native, err)
	}
	external, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Model: "service/example-model"})
	if err != nil {
		t.Fatal(err)
	}
	if external.ProfileSpec.Provider == nil || external.ProfileSpec.Provider.BaseUrl != "https://provider.example/v1" || external.ProfileSpec.Provider.ApiKeyEnv != "EXAMPLE_API_KEY" {
		t.Fatalf("external provider = %+v", external.ProfileSpec.Provider)
	}
	if model, ok := AgentArgsModel("codex", external.ExtraArgs); !ok || model != "example-model" {
		t.Fatalf("external launch model = %q present=%v", model, ok)
	}
	if _, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Model: "unlisted-model"}); err == nil {
		t.Fatal("profile allowlist accepted an unlisted model")
	}
	if _, err := RenderProfileWorkspace(profile, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestAgentAuthoringExamplesResolveAndProject(t *testing.T) {
	for _, example := range []struct{ name, agent, instruction, skill string }{
		{"code-reviewer", "claude", "聚焦正确性", ".claude/skills/diff-review/SKILL.md"},
		{"codex-rules", "codex", "遵循仓库既有风格", ""},
	} {
		t.Run(example.name, func(t *testing.T) {
			pack := Pack{Namespace: "example", MaterializedPath: filepath.Join("..", "..", "examples", "agents", example.name), CurrentRevision: "example"}
			if err := ValidatePackManifest(pack); err != nil {
				t.Fatal(err)
			}
			profile, ok, err := ResolvePackProfile(PackRegistry{Packs: map[string]Pack{"example": pack}}, "example/"+example.name)
			if err != nil || !ok {
				t.Fatalf("resolve: found=%v error=%v", ok, err)
			}
			if profile.Type != example.agent || !strings.Contains(profile.AgentsContent, example.instruction) {
				t.Fatalf("resolved profile lost instructions: %+v", profile)
			}
			workspace := t.TempDir()
			if _, err := RenderProfileWorkspace(profile, workspace); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
			if err != nil || !strings.Contains(string(data), example.instruction) {
				t.Fatalf("projected instructions: %q error=%v", data, err)
			}
			if example.skill != "" {
				data, err := os.ReadFile(filepath.Join(workspace, example.skill))
				if err != nil || !strings.Contains(string(data), "name: diff-review") {
					t.Fatalf("projected skill: %q error=%v", data, err)
				}
			}
		})
	}
}
