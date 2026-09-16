package agentprofile

import (
	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeProfileIntoSandbox(t *testing.T) {
	sb, err := task.PrepareSandbox(t.TempDir(), "t1", "", task.IsolationPrivateHome)
	if err != nil {
		t.Fatal(err)
	}
	spec := &pb.AgentProfileSpec{
		Ref:           "team/coder",
		Namespace:     "team",
		Name:          "coder",
		Type:          "claude",
		Source:        "pack",
		Agents:        "AGENTS.md",
		AgentsContent: "be terse\n",
	}
	amuxDir := t.TempDir()
	args, err := MaterializeIntoSandbox(spec, sb, adapter.Layout, amuxDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("profile materialization args = %v, want none", args)
	}
	assertSandboxFileContains(t, filepath.Join(sb.Workspace, "AGENTS.md"), "be terse")
	assertSandboxFileContains(t, filepath.Join(sb.Workspace, "CLAUDE.md"), "[AGENTS.md](AGENTS.md)")
	if _, err := os.Stat(filepath.Join(sb.AgentConfig, "claude-mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("stat claude mcp config = %v, want not exist", err)
	}
}

func TestMaterializeProfileIntoSandboxProjectsMultipleLocalPackSkills(t *testing.T) {
	sb, err := task.PrepareSandbox(t.TempDir(), "multi-skills", "", task.IsolationProcess)
	if err != nil {
		t.Fatal(err)
	}
	amuxDir := t.TempDir()
	packRoot := filepath.Join(amuxDir, "agent-packs", "team", "repo")
	writeSandboxPackSkill(t, packRoot, "alpha", "Alpha skill")
	writeSandboxPackSkill(t, packRoot, "beta", "Beta skill")
	if err := SavePackRegistry(filepath.Join(amuxDir, "agent-packs", "registry.json"), PackRegistry{Packs: map[string]Pack{
		"team": {
			Namespace:        "team",
			SourceKind:       PackSourceGit,
			MaterializedPath: packRoot,
			CurrentRevision:  "abc123",
		},
	}}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	spec := &pb.AgentProfileSpec{
		Ref:       "team/coder",
		PinnedRef: "team/coder@abc123",
		Namespace: "team",
		Name:      "coder",
		Revision:  "abc123",
		Type:      "codex",
		Source:    "pack",
		Skills:    []string{"alpha", "beta"},
		SkillDirs: []string{"skills"},
	}

	if _, err := MaterializeIntoSandbox(spec, sb, adapter.Layout, amuxDir); err != nil {
		t.Fatal(err)
	}

	assertSandboxFileContains(t, filepath.Join(sb.Workspace, ".agents", "skills", "alpha", "SKILL.md"), "Alpha skill")
	assertSandboxFileContains(t, filepath.Join(sb.Workspace, ".agents", "skills", "beta", "SKILL.md"), "Beta skill")
}

func argsContainPrefix(args []string, want string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, want) {
			return true
		}
	}
	return false
}

func assertSandboxFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s missing %q:\n%s", path, want, string(data))
	}
}

func writeSandboxPackSkill(t *testing.T, packRoot, name, description string) {
	t.Helper()
	path := filepath.Join(packRoot, "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nUse this skill.\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}
