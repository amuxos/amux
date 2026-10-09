package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicPackExampleUsesCallerRuntimeSelection(t *testing.T) {
	pack := Pack{Namespace: "team", MaterializedPath: filepath.Join("..", "..", "examples", "public-pack"), CurrentRevision: "example"}
	if err := ValidatePackManifest(pack); err != nil {
		t.Fatal(err)
	}
	profile, ok, err := ResolvePackProfile(PackRegistry{Packs: map[string]Pack{"team": pack}}, "team/reviewer")
	if err != nil || !ok {
		t.Fatalf("example profile: found=%v error=%v", ok, err)
	}
	for _, engine := range []string{"codex", "claude", "pi"} {
		launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: engine, Model: "review-model"})
		if err != nil || launch.Model != "review-model" || launch.ProfileSpec.Provider != nil {
			t.Fatalf("%s launch: %+v error=%v", engine, launch, err)
		}
		resolved, err := FromPBProfileSpec(launch.ProfileSpec)
		if err != nil {
			t.Fatal(err)
		}
		resolved.PackPath = pack.MaterializedPath
		if _, err := RenderProfileWorkspace(resolved, t.TempDir()); err != nil {
			t.Fatal(err)
		}
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
			if profile.Type != "" || !strings.Contains(profile.AgentsContent, example.instruction) {
				t.Fatalf("resolved profile lost instructions: %+v", profile)
			}
			launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: example.agent})
			if err != nil {
				t.Fatal(err)
			}
			profile, err = FromPBProfileSpec(launch.ProfileSpec)
			if err != nil {
				t.Fatal(err)
			}
			profile.PackPath = pack.MaterializedPath
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
