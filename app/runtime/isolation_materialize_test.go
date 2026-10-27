package runtime

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/adapter"
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

// The credential derived from the principal's own login replaces the service
// account's copy for the same HOME path; other projected files keep their
// service copies.
func TestStageIsolatedHomeFilesPrefersDerivedOverride(t *testing.T) {
	sourceHome := t.TempDir()
	for _, rel := range []string{".trae/cli/auth.json", ".codex/auth.json"} {
		if err := os.MkdirAll(filepath.Join(sourceHome, filepath.FromSlash(filepath.Dir(rel))), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceHome, filepath.FromSlash(rel)), []byte("service-"+rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	staged, err := stageIsolatedHomeFiles(isolatedHomeProjection{
		SourceHome: sourceHome,
		Files:      []string{".trae/cli/auth.json", ".codex/auth.json"},
		Override:   map[string][]byte{".trae/cli/auth.json": []byte("derived-credential")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(staged)
	data, err := os.ReadFile(filepath.Join(staged, ".trae", "cli", "auth.json"))
	if err != nil || string(data) != "derived-credential" {
		t.Fatalf("derived credential = %q, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(staged, ".codex", "auth.json"))
	if err != nil || string(data) != "service-.codex/auth.json" {
		t.Fatalf("codex credential = %q, %v", data, err)
	}
}

// A principal-signed-in credential is staged even when the service account has
// no copy of that file at all.
func TestStageIsolatedHomeFilesStagesOverrideWithoutServiceCopy(t *testing.T) {
	staged, err := stageIsolatedHomeFiles(isolatedHomeProjection{
		SourceHome: t.TempDir(),
		Files:      []string{".trae/cli/auth.json"},
		Override:   map[string][]byte{".trae/cli/auth.json": []byte("derived-credential")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if staged == "" {
		t.Fatal("expected a staging directory")
	}
	defer os.RemoveAll(staged)
	data, err := os.ReadFile(filepath.Join(staged, ".trae", "cli", "auth.json"))
	if err != nil || string(data) != "derived-credential" {
		t.Fatalf("credential = %q, %v", data, err)
	}
}

// Override paths come from adapter code but still reach the privileged
// materializer; an escape must never be staged.
func TestStageIsolatedHomeFilesRejectsEscapingOverride(t *testing.T) {
	if _, err := stageIsolatedHomeFiles(isolatedHomeProjection{
		SourceHome: t.TempDir(),
		Override:   map[string][]byte{"../outside": []byte("credential")},
	}); err == nil {
		t.Fatal("expected an escaping override path to be rejected")
	}
}

type stubCredentialAdapter struct{}

func (stubCredentialAdapter) Cmd([]string) *exec.Cmd { return exec.Command("true") }

func (stubCredentialAdapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }

func (stubCredentialAdapter) PrincipalHomeSources() []string { return []string{"sources/one"} }

func (stubCredentialAdapter) DerivePrincipalHomeFile(map[string][]byte, time.Time) (string, []byte, bool) {
	return "", nil, false
}

// Without a principal identity or a usable isolation runtime there is nothing
// to derive: the projection keeps the service copy.
func TestPrincipalHomeCredentialOverrideRequiresIsolation(t *testing.T) {
	stub := stubCredentialAdapter{}
	if got := principalHomeCredentialOverride(context.Background(), nil, principal.ExecutionIdentity{}, stub, time.Now()); got != nil {
		t.Fatalf("override = %v, want nil without an isolation runtime", got)
	}
	identified := principal.ExecutionIdentity{
		IdentityMode:         principal.IdentityModeIdentified,
		EffectivePrincipalID: "7aac69c6-8fbf-4c1a-9f00-123456789abc",
		IsolationBackend:     principal.BackendLinuxDAC,
	}
	if got := principalHomeCredentialOverride(context.Background(), &isolation.Runtime{}, identified, stub, time.Now()); got != nil {
		t.Fatalf("override = %v, want nil when the isolation runtime cannot read", got)
	}
}
