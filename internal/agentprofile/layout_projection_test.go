package agentprofile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
)

type layoutProjectionAdapter struct{ adapter.CodexAdapter }

func (*layoutProjectionAdapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{ConfigLayout: map[adapter.Surface]adapter.Placement{
		adapter.SurfaceRules:  {Supported: true, Kind: "cwd-rel", Path: "rules/INSTRUCTIONS.md"},
		adapter.SurfaceSkills: {Supported: true, Kind: "cwd-rel", Path: "extensions/skills"},
	}}
}

func TestWorkspaceProjectionUsesRegisteredAdapterLayout(t *testing.T) {
	const childEnv = "AMUX_TEST_LAYOUT_PROJECTION"
	if os.Getenv(childEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWorkspaceProjectionUsesRegisteredAdapterLayout$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("projection subprocess: %v\n%s", err, output)
		}
		return
	}
	if err := adapter.Register("layout-example", func() adapter.Adapter { return &layoutProjectionAdapter{} }); err != nil {
		t.Fatal(err)
	}
	pack := t.TempDir()
	skill := filepath.Join(pack, "skills", "example")
	if err := os.MkdirAll(skill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("example skill"), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	profile := Profile{Namespace: "test", Name: "example", Type: "layout-example", AgentsContent: "example instructions", Source: SourcePack, PackPath: pack, SkillDirs: []string{"skills"}, Skills: []string{"example"}}
	if _, err := RenderProfileWorkspace(profile, root); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(root, "rules", "INSTRUCTIONS.md"), "example instructions")
	assertFileContains(t, filepath.Join(root, "extensions", "skills", "example", "SKILL.md"), "example skill")
	assertFileNotExists(t, filepath.Join(root, "AGENTS.md"))
}
