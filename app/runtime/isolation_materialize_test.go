package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
)

func TestMaterializeLinuxDACTaskLaunchCopiesAndRewritesRunArtifacts(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "sandbox", "workspace")
	for _, dir := range []string{workspace, filepath.Join(root, "sandbox", "agent-config"), filepath.Join(root, "sandbox", "secret"), filepath.Join(root, "sandbox", "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sandbox", "secret", "token"), []byte("canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := isolation.PrincipalPaths{
		Workspace: filepath.Join(root, "principal", "workspace"),
		Run:       filepath.Join(root, "principal", "run", "launch-1"),
		Home:      filepath.Join(root, "principal", "home"),
	}
	for _, dir := range []string{paths.Workspace, paths.Run, paths.Home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env, record, args, err := materializeLinuxDACTaskLaunch(
		func(sources []isolation.BundleSource) error {
			var bundle bytes.Buffer
			if err := isolation.WriteMaterializeBundle(&bundle, sources); err != nil {
				return err
			}
			return isolation.ApplyMaterializeBundle(bytes.NewReader(bundle.Bytes()), paths.Workspace, paths.Run, paths.Home)
		},
		workspace,
		paths,
		isolatedHomeProjection{},
		[]string{"TMPDIR=" + filepath.Join(root, "sandbox", "tmp")},
		[]string{"CONFIG=" + filepath.Join(root, "sandbox", "agent-config", "config.json")},
		[]string{"--credential=" + filepath.Join(root, "sandbox", "secret", "token")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if env[0] != "TMPDIR="+filepath.Join(paths.Run, "tmp") {
		t.Fatalf("env = %q", env[0])
	}
	if record[0] != "CONFIG="+filepath.Join(paths.Run, "agent-config", "config.json") {
		t.Fatalf("record = %q", record[0])
	}
	if args[0] != "--credential="+filepath.Join(paths.Run, "secret", "token") {
		t.Fatalf("args = %q", args[0])
	}
	if got, err := os.ReadFile(filepath.Join(paths.Run, "secret", "token")); err != nil || string(got) != "canary" {
		t.Fatalf("copied secret = %q, %v", got, err)
	}
}

func TestStageIsolatedHomeFilesCopiesExistingOnly(t *testing.T) {
	sourceHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceHome, ".trae", "cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceHome, ".trae", "cli", "auth.json"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}

	staged, err := stageIsolatedHomeFiles(isolatedHomeProjection{
		SourceHome: sourceHome, Files: []string{".trae/cli/auth.json", ".codex/auth.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if staged == "" {
		t.Fatal("expected a staging directory")
	}
	defer os.RemoveAll(staged)

	data, err := os.ReadFile(filepath.Join(staged, ".trae", "cli", "auth.json"))
	if err != nil || string(data) != "token" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(staged, ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("absent credential was staged: %v", err)
	}
}

func TestStageIsolatedHomeFilesSkipsWhenNothingExists(t *testing.T) {
	staged, err := stageIsolatedHomeFiles(isolatedHomeProjection{
		SourceHome: t.TempDir(), Files: []string{".trae/cli/auth.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if staged != "" {
		os.RemoveAll(staged)
		t.Fatal("expected no staging directory when no credential exists")
	}
}

// A relative path is the adapter's declaration, but it still reaches the
// privileged materializer; an escape must never be staged.
func TestStageIsolatedHomeFilesRejectsEscape(t *testing.T) {
	if _, err := stageIsolatedHomeFiles(isolatedHomeProjection{
		SourceHome: t.TempDir(), Files: []string{"../outside"},
	}); err == nil {
		t.Fatal("expected an escaping HOME-relative path to be rejected")
	}
}

// The provider's account credential has to reach the principal HOME in the same
// launch bundle as the workspace: the agent authenticates before the first turn.
func TestMaterializeLinuxDACSessionLaunchProjectsHomeCredential(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "sandbox", "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceHome := filepath.Join(root, "service-home")
	if err := os.MkdirAll(filepath.Join(sourceHome, ".trae", "cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceHome, ".trae", "cli", "auth.json"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := isolation.PrincipalPaths{
		Workspace: filepath.Join(root, "principal", "workspace"),
		Run:       filepath.Join(root, "principal", "run", "launch-1"),
		Home:      filepath.Join(root, "principal", "home"),
	}
	for _, dir := range []string{paths.Workspace, paths.Run, paths.Home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	_, _, _, err := materializeLinuxDACSessionLaunch(
		func(sources []isolation.BundleSource) error {
			var bundle bytes.Buffer
			if err := isolation.WriteMaterializeBundle(&bundle, sources); err != nil {
				return err
			}
			return isolation.ApplyMaterializeBundle(bytes.NewReader(bundle.Bytes()), paths.Workspace, paths.Run, paths.Home)
		},
		workspace, "", "", paths,
		isolatedHomeProjection{SourceHome: sourceHome, Files: []string{".trae/cli/auth.json"}}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(paths.Home, ".trae", "cli", "auth.json"))
	if err != nil || string(data) != "token" {
		t.Fatalf("principal home credential: data=%q err=%v", data, err)
	}
}

// Subagent tasks launch the same providers, so the task bundle carries the same
// HOME credential; otherwise every isolated task turn comes back empty.
func TestMaterializeLinuxDACTaskLaunchProjectsHomeCredential(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "sandbox", "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceHome := filepath.Join(root, "service-home")
	if err := os.MkdirAll(filepath.Join(sourceHome, ".trae", "cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceHome, ".trae", "cli", "auth.json"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := isolation.PrincipalPaths{
		Workspace: filepath.Join(root, "principal", "workspace"),
		Run:       filepath.Join(root, "principal", "run", "launch-1"),
		Home:      filepath.Join(root, "principal", "home"),
	}
	for _, dir := range []string{paths.Workspace, paths.Run, paths.Home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	_, _, _, err := materializeLinuxDACTaskLaunch(
		func(sources []isolation.BundleSource) error {
			var bundle bytes.Buffer
			if err := isolation.WriteMaterializeBundle(&bundle, sources); err != nil {
				return err
			}
			return isolation.ApplyMaterializeBundle(bytes.NewReader(bundle.Bytes()), paths.Workspace, paths.Run, paths.Home)
		},
		workspace, paths,
		isolatedHomeProjection{SourceHome: sourceHome, Files: []string{".trae/cli/auth.json"}}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(paths.Home, ".trae", "cli", "auth.json"))
	if err != nil || string(data) != "token" {
		t.Fatalf("principal home credential: data=%q err=%v", data, err)
	}
}

// Persistent agent instances deliberately share one durable principal workspace
// across executions. Reclaiming it when the first session is torn down would
// delete a live agent's state.
func TestPrincipalWorkspaceInUseDetectsSharedWorkspace(t *testing.T) {
	shared := principal.ExecutionIdentity{
		IdentityMode:         "identified",
		EffectivePrincipalID: "p1",
		IsolationBackend:     principal.BackendLinuxDAC,
		ExecutionStateRef:    "instance-7",
	}
	other := session.Snapshot{ID: "s2", ExecutionIdentity: shared}
	self := session.Snapshot{ID: "s1", ExecutionIdentity: shared}

	if !principalWorkspaceInUse(self, []session.Snapshot{self, other}) {
		t.Fatal("a workspace shared with another session was reported free")
	}
	if principalWorkspaceInUse(self, []session.Snapshot{self}) {
		t.Fatal("a workspace referenced only by this session was reported in use")
	}
}

func TestPrincipalWorkspaceInUseIgnoresOtherPrincipals(t *testing.T) {
	self := session.Snapshot{ID: "s1", ExecutionIdentity: principal.ExecutionIdentity{
		IdentityMode: "identified", EffectivePrincipalID: "p1",
		IsolationBackend: principal.BackendLinuxDAC, ExecutionStateRef: "session-1",
	}}
	other := session.Snapshot{ID: "s2", ExecutionIdentity: principal.ExecutionIdentity{
		IdentityMode: "identified", EffectivePrincipalID: "p2",
		IsolationBackend: principal.BackendLinuxDAC, ExecutionStateRef: "session-2",
	}}

	if principalWorkspaceInUse(self, []session.Snapshot{self, other}) {
		t.Fatal("an unrelated principal's session blocked reclamation")
	}
}
