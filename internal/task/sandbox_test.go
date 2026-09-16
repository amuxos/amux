package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareSandboxPrivateHome(t *testing.T) {
	dir := t.TempDir()
	sb, err := PrepareSandbox(dir, "task-1", "", IsolationPrivateHome)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{sb.Home, sb.Tmp, sb.Workspace, sb.AgentConfig, sb.Secret} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("expected dir %s: %v", d, err)
		}
	}
	env := sb.BaseEnv()
	if !hasEnv(env, "HOME="+sb.Home) || !hasEnv(env, "TMPDIR="+sb.Tmp) {
		t.Fatalf("env missing HOME/TMPDIR: %v", env)
	}
}

func TestPrepareSandboxProcessSharesHome(t *testing.T) {
	dir := t.TempDir()
	sb, err := PrepareSandbox(dir, "task-2", "", IsolationProcess)
	if err != nil {
		t.Fatal(err)
	}
	if sb.Home != "" {
		t.Fatalf("process isolation should not own a HOME, got %q", sb.Home)
	}
	for _, e := range sb.BaseEnv() {
		if len(e) >= 5 && e[:5] == "HOME=" {
			t.Fatalf("process isolation must not override HOME: %v", e)
		}
	}
}

// TestPrepareSandboxInstanceKeyReusesTree guards Task 3.2: a persistent agent
// instance keys its sandbox by instance-<id>, so two tasks on the same instance
// reuse the same sandbox tree (durable HOME/workspace), while ephemeral tasks
// each get a fresh task-keyed tree.
func TestPrepareSandboxInstanceKeyReusesTree(t *testing.T) {
	dir := t.TempDir()
	sb1, err := PrepareSandbox(dir, "task-A", "inst-1", IsolationPrivateHome)
	if err != nil {
		t.Fatal(err)
	}
	sb2, err := PrepareSandbox(dir, "task-B", "inst-1", IsolationPrivateHome)
	if err != nil {
		t.Fatal(err)
	}
	if sb1.Root != sb2.Root {
		t.Fatalf("same instance should reuse sandbox root: %q vs %q", sb1.Root, sb2.Root)
	}
	if sb1.Home != sb2.Home || sb1.Workspace != sb2.Workspace {
		t.Fatalf("same instance should reuse HOME/workspace: %q/%q vs %q/%q", sb1.Home, sb1.Workspace, sb2.Home, sb2.Workspace)
	}
	// The root lives under instance-inst-1, not the task ids.
	if got := filepath.Base(sb1.Root); got != "instance-inst-1" {
		t.Fatalf("sandbox root base = %q, want instance-inst-1", got)
	}

	// Ephemeral tasks key by task id and are independent.
	se1, err := PrepareSandbox(dir, "task-X", "", IsolationPrivateHome)
	if err != nil {
		t.Fatal(err)
	}
	se2, err := PrepareSandbox(dir, "task-Y", "", IsolationPrivateHome)
	if err != nil {
		t.Fatal(err)
	}
	if se1.Root == se2.Root {
		t.Fatalf("ephemeral tasks should have distinct roots: %q", se1.Root)
	}
	if got := filepath.Base(se1.Root); got != "task-X" {
		t.Fatalf("ephemeral root base = %q, want task-X", got)
	}
}

func TestMaterializeAndDestroySecrets(t *testing.T) {
	dir := t.TempDir()
	sb, err := PrepareSandbox(dir, "task-3", "", IsolationPrivateHome)
	if err != nil {
		t.Fatal(err)
	}
	env, err := sb.Materialize(CredentialMaterial{
		Env:     []string{"ANTHROPIC_API_KEY=sk-ant-secretvalue123456"},
		Files:   map[string]string{"auth.json": `{"token":"sk-ant-secretvalue123456"}`},
		Secrets: []string{"sk-ant-secretvalue123456"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEnv(env, "ANTHROPIC_API_KEY=sk-ant-secretvalue123456") {
		t.Fatalf("env = %v", env)
	}
	authPath := filepath.Join(sb.Secret, "auth.json")
	if _, err := os.Stat(authPath); err != nil {
		t.Fatalf("secret file missing: %v", err)
	}
	// Every injected secret value (env VALUE, file content, explicit Secrets) is
	// registered as a redaction literal. Here the same value appears via all three.
	got := sb.Secrets()
	if len(got) != 3 {
		t.Fatalf("secrets = %v", got)
	}
	for _, s := range got {
		if !strings.Contains(s, "sk-ant-secretvalue123456") {
			t.Fatalf("unexpected secret literal %q", s)
		}
	}
	if err := sb.DestroySecrets(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sb.Secret); !os.IsNotExist(err) {
		t.Fatalf("secret dir should be gone: %v", err)
	}
}

func TestCleanupRemovesAllWhenNotRetained(t *testing.T) {
	dir := t.TempDir()
	sb, _ := PrepareSandbox(dir, "task-4", "", IsolationPrivateHome)
	if err := sb.Cleanup(false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sb.Root); !os.IsNotExist(err) {
		t.Fatalf("root should be gone: %v", err)
	}
}

func TestCleanupRetainsWorkspaceButDestroysSecret(t *testing.T) {
	dir := t.TempDir()
	sb, _ := PrepareSandbox(dir, "task-5", "", IsolationPrivateHome)
	_, _ = sb.Materialize(CredentialMaterial{Files: map[string]string{"k": "v"}})
	if err := sb.Cleanup(true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sb.Workspace); err != nil {
		t.Fatalf("workspace should be retained: %v", err)
	}
	if _, err := os.Stat(sb.Secret); !os.IsNotExist(err) {
		t.Fatalf("secret must be destroyed even when workspace retained: %v", err)
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
