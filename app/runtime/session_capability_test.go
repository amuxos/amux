package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessionprotocol"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestSessionCapabilityRawProfileAndRelaunch(t *testing.T) {
	for _, agent := range []string{"codex", "claude", "pi"} {
		for _, profile := range []string{"", "external-pack-profile"} {
			t.Run(agent+"/"+profile, func(t *testing.T) {
				s, args, envs := memoryTestServer(t)
				cwd := t.TempDir()
				userRules := filepath.Join(cwd, "AGENTS.md")
				if err := os.WriteFile(userRules, []byte("USER RULES"), 0600); err != nil {
					t.Fatal(err)
				}
				userArgs := []string{"--append-system-prompt", "user prompt"}
				if agent == "codex" {
					userArgs = []string{"-c", `developer_instructions="user prompt"`}
				}
				res, err := s.Run(context.Background(), &pb.RunRequest{Name: "capability", AgentType: agent, AgentProfile: profile, Cwd: cwd, ExtraArgs: userArgs})
				if err != nil {
					t.Fatal(err)
				}
				sess, _ := s.mgr.Get(res.Session.Id)
				if _, err := s.relaunchSession(sess, "restarting"); err != nil {
					t.Fatal(err)
				}
				expected := "MCP"
				if agent == "pi" {
					expected = "CLI"
				}
				for _, env := range *envs {
					instruction := sessionprotocol.FromEnv(env)
					if !strings.Contains(instruction, "Transport: "+expected) || !strings.Contains(instruction, sessionprotocol.SkillName) {
						t.Fatalf("context=%s", instruction)
					}
				}
				for _, a := range *args {
					if !strings.Contains(strings.Join(a, " "), "user prompt") {
						t.Fatal("user prompt lost")
					}
					if agent != "codex" && (strings.Count(strings.Join(a, " "), "<amux-runtime>") != 1 || strings.Count(strings.Join(a, " "), "--append-system-prompt") != 1) {
						t.Fatal("native system prompt absent or duplicated")
					}
				}
				skill := filepath.Join(profileSessionDir(s.amuxDir, sess.ID), "agent-config", "runtime-skills", sessionprotocol.SkillName, "SKILL.md")
				if _, err := os.ReadFile(skill); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(userRules)
				if err != nil || string(data) != "USER RULES" {
					t.Fatal("workspace rules changed")
				}
			})
		}
	}
}

func TestSessionCapabilityUnavailableAndTaskRestricted(t *testing.T) {
	s, _, _ := memoryTestServer(t)
	s.disableMCPAutoInject = true
	res, err := s.Run(context.Background(), &pb.RunRequest{Name: "disabled", AgentType: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := s.mgr.Get(res.Session.Id)
	_, env, err := s.configureSessionCapability(context.Background(), sess, nil, []string{"KEEP=value", sessionprotocol.InstructionEnv + "=forged"})
	if err != nil || !strings.Contains(sessionprotocol.FromEnv(env), "Transport: unavailable") {
		t.Fatalf("env=%v err=%v", env, err)
	}
	child, err := s.mgr.CreateOwned("child", "pi", "task-parent", "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args, env, err := s.configureSessionCapability(context.Background(), child, []string{"--user-flag"}, []string{"PATH=/restricted"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sessionprotocol.FromEnv(env), "Task-owned child") || launchEnvValue(env, "PATH") != "/restricted" || len(args) != 3 || args[0] != "--user-flag" || args[1] != "--append-system-prompt" {
		t.Fatal("child gained tool configuration")
	}
}

func TestSessionSkillDACBundle(t *testing.T) {
	stage, run := t.TempDir(), t.TempDir()
	if err := writeSessionSkill(filepath.Join(stage, "runtime-skills")); err != nil {
		t.Fatal(err)
	}
	var bundle bytes.Buffer
	if err := isolation.WriteMaterializeBundle(&bundle, []isolation.BundleSource{{Source: stage, Prefix: "agent-config"}}); err != nil {
		t.Fatal(err)
	}
	if err := isolation.ApplyMaterializeBundle(&bundle, t.TempDir(), run, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(run, "agent-config", "runtime-skills", sessionprotocol.SkillName)
	for _, file := range []string{"SKILL.md", "references/schedules.md"} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil || len(data) == 0 {
			t.Fatalf("resource %s: %v", file, err)
		}
	}
}

type capabilityFileHost struct{ adapter.CodexAdapter }

func (*capabilityFileHost) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{Protocol: adapter.ProtocolACP, ConfigLayout: map[adapter.Surface]adapter.Placement{
		adapter.SurfaceMCP:    {Supported: true, Kind: "cwd-rel", Path: ".agent/config.json"},
		adapter.SurfaceSkills: {Supported: true, Kind: "cwd-rel", Path: ".agent/skills"},
	}}
}

func TestSessionCapabilityUsesExtensionLayout(t *testing.T) {
	const name = "session-capability-file-host"
	if err := adapter.Register(name, func() adapter.Adapter { return &capabilityFileHost{} }); err != nil {
		t.Fatal(err)
	}
	s, _, _ := memoryTestServer(t)
	sess, err := s.mgr.Create("extension", name, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		s.disableMCPAutoInject = disabled
		_, env, err := s.configureSessionCapability(context.Background(), sess, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sessionprotocol.FromEnv(env), "Transport: MCP") == disabled {
			t.Fatal("extension layout/auto-inject ignored")
		}
	}
	if !sessionCapabilityUsesCLI(session.Snapshot{AgentType: name, ExecutionIdentity: principal.ExecutionIdentity{IsolationBackend: principal.BackendLinuxDAC}}) {
		t.Fatal("isolated extension must use CLI without replacing shared host configuration")
	}
	data, err := os.ReadFile(filepath.Join(sess.Cwd, ".agent", "skills", sessionDiscoveryName, "SKILL.md"))
	if err != nil || !strings.Contains(string(data), sessionprotocol.InstructionEnv) {
		t.Fatalf("native Skill catalog discovery missing: %v", err)
	}
}

func TestSessionDiscoveryNativeCatalog(t *testing.T) {
	for _, kind := range []string{"cwd-rel", "home-rel"} {
		t.Run(kind, func(t *testing.T) {
			cwd, home := t.TempDir(), t.TempDir()
			root := cwd
			if kind == "home-rel" {
				root = home
			}
			rules := filepath.Join(root, "AGENTS.md")
			if err := os.WriteFile(rules, []byte("user rules"), 0600); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(root, ".agent", "skills", "user-skill", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(other), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(other, []byte("user skill"), 0600); err != nil {
				t.Fatal(err)
			}
			placement := adapter.Placement{Supported: true, Kind: kind, Path: ".agent/skills"}
			s := &server{}
			for range 2 { // Relaunch publishes the same catalog entry.
				if err := s.materializeSessionDiscovery(context.Background(), session.Snapshot{Cwd: cwd}, []string{"HOME=" + home}, placement); err != nil {
					t.Fatal(err)
				}
			}
			entry := filepath.Join(root, ".agent", "skills", sessionDiscoveryName, "SKILL.md")
			if data, err := os.ReadFile(entry); err != nil || string(data) != sessionDiscoverySkill {
				t.Fatalf("catalog entry: %v", err)
			}
			for path, want := range map[string]string{rules: "user rules", other: "user skill"} {
				if data, err := os.ReadFile(path); err != nil || string(data) != want {
					t.Fatalf("user content changed at %s: %v", path, err)
				}
			}
			// The same native directory survives target-UID materialization.
			prefix := "workspace"
			if kind == "home-rel" {
				prefix = "home"
			}
			var bundle bytes.Buffer
			if err := isolation.WriteMaterializeBundle(&bundle, []isolation.BundleSource{{Source: root, Prefix: prefix}}); err != nil {
				t.Fatal(err)
			}
			dacWorkspace, dacHome := t.TempDir(), t.TempDir()
			if err := isolation.ApplyMaterializeBundle(&bundle, dacWorkspace, t.TempDir(), dacHome); err != nil {
				t.Fatal(err)
			}
			dacRoot := dacWorkspace
			if kind == "home-rel" {
				dacRoot = dacHome
			}
			if _, err := os.ReadFile(filepath.Join(dacRoot, ".agent", "skills", sessionDiscoveryName, "SKILL.md")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
