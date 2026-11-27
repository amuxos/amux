package agentprofile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDevelopmentLeaseReconcileWaitsForLiveOwnerThenCleans(t *testing.T) {
	t.Parallel()
	registryPath, repo, worktree, namespace, branch := setupDevelopmentLeaseTest(t)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	lease, err := RegisterDevelopmentLease(registryPath, RegisterDevelopmentLeaseOptions{
		Namespace:      namespace,
		DevelopmentOf:  "team",
		RepositoryPath: repo,
		WorktreePath:   worktree,
		Branch:         branch,
		OwnerSessionID: "session-live",
		TTL:            time.Hour,
		Now:            now,
	})
	if err != nil {
		t.Fatalf("RegisterDevelopmentLease: %v", err)
	}
	if lease.ExpiresAt != now.Add(time.Hour) {
		t.Fatalf("ExpiresAt = %v, want %v", lease.ExpiresAt, now.Add(time.Hour))
	}

	results, err := ReconcileDevelopmentLeases(
		registryPath,
		now.Add(2*time.Hour),
		map[string]struct{}{"session-live": {}},
	)
	if err != nil {
		t.Fatalf("ReconcileDevelopmentLeases live owner: %v", err)
	}
	if len(results) != 1 || results[0].Status != DevelopmentLeaseKept {
		t.Fatalf("live-owner results = %#v, want one kept result", results)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("live owner worktree was removed: %v", err)
	}

	results, err = ReconcileDevelopmentLeases(registryPath, now.Add(2*time.Hour), map[string]struct{}{})
	if err != nil {
		t.Fatalf("ReconcileDevelopmentLeases archived owner: %v", err)
	}
	if len(results) != 1 || results[0].Status != DevelopmentLeaseCleaned {
		t.Fatalf("archived-owner results = %#v, want one cleaned result", results)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after owner disappeared: %v", err)
	}
	if got := strings.TrimSpace(runDevelopmentLeaseGit(t, repo, "show-ref", "--verify", "refs/heads/"+branch)); got == "" {
		t.Fatal("cleanup removed the task branch")
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if _, ok := registry.Packs[namespace]; ok {
		t.Fatalf("temporary pack %q still registered", namespace)
	}
	leases, err := LoadDevelopmentLeaseRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadDevelopmentLeaseRegistry: %v", err)
	}
	if len(leases.Leases) != 0 {
		t.Fatalf("leases after cleanup = %#v, want empty", leases.Leases)
	}
}

func TestDevelopmentLeaseReconcileUsesTTLOnlyWithoutOwner(t *testing.T) {
	t.Parallel()
	registryPath, repo, worktree, namespace, branch := setupDevelopmentLeaseTest(t)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	if _, err := RegisterDevelopmentLease(registryPath, RegisterDevelopmentLeaseOptions{
		Namespace:      namespace,
		DevelopmentOf:  "team",
		RepositoryPath: repo,
		WorktreePath:   worktree,
		Branch:         branch,
		TTL:            time.Hour,
		Now:            now,
	}); err != nil {
		t.Fatalf("RegisterDevelopmentLease: %v", err)
	}

	results, err := ReconcileDevelopmentLeases(registryPath, now.Add(30*time.Minute), nil)
	if err != nil {
		t.Fatalf("ReconcileDevelopmentLeases before TTL: %v", err)
	}
	if len(results) != 1 || results[0].Status != DevelopmentLeaseKept {
		t.Fatalf("before-TTL results = %#v, want one kept result", results)
	}
	results, err = ReconcileDevelopmentLeases(registryPath, now.Add(2*time.Hour), nil)
	if err != nil {
		t.Fatalf("ReconcileDevelopmentLeases after TTL: %v", err)
	}
	if len(results) != 1 || results[0].Status != DevelopmentLeaseCleaned {
		t.Fatalf("after-TTL results = %#v, want one cleaned result", results)
	}
}

func TestDevelopmentLeaseCleanupRetainsDirtyWorktree(t *testing.T) {
	t.Parallel()
	registryPath, repo, worktree, namespace, branch := setupDevelopmentLeaseTest(t)
	if _, err := RegisterDevelopmentLease(registryPath, RegisterDevelopmentLeaseOptions{
		Namespace:      namespace,
		DevelopmentOf:  "team",
		RepositoryPath: repo,
		WorktreePath:   worktree,
		Branch:         branch,
		OwnerSessionID: "session-done",
		TTL:            time.Hour,
	}); err != nil {
		t.Fatalf("RegisterDevelopmentLease: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "dirty.txt"), []byte("do not delete\n"), 0600); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	result, err := CleanupDevelopmentLease(registryPath, namespace)
	if err != nil {
		t.Fatalf("CleanupDevelopmentLease: %v", err)
	}
	if result.Status != DevelopmentLeaseRetained || !strings.Contains(result.Reason, "uncommitted") {
		t.Fatalf("dirty cleanup result = %#v, want retained uncommitted reason", result)
	}
	if _, err := os.Stat(filepath.Join(worktree, "dirty.txt")); err != nil {
		t.Fatalf("dirty worktree content was removed: %v", err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if _, ok := registry.Packs[namespace]; !ok {
		t.Fatalf("temporary pack %q was removed for dirty worktree", namespace)
	}
	leases, err := LoadDevelopmentLeaseRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadDevelopmentLeaseRegistry: %v", err)
	}
	if got := leases.Leases[namespace].LastError; !strings.Contains(got, "uncommitted") {
		t.Fatalf("persisted LastError = %q, want uncommitted reason", got)
	}
}

func TestDevelopmentLeaseRegisterRejectsUnrelatedRepositoryOrigin(t *testing.T) {
	t.Parallel()
	registryPath, repo, worktree, namespace, branch := setupDevelopmentLeaseTest(t)
	runDevelopmentLeaseGit(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unrelated"))
	_, err := RegisterDevelopmentLease(registryPath, RegisterDevelopmentLeaseOptions{
		Namespace:      namespace,
		DevelopmentOf:  "team",
		RepositoryPath: repo,
		WorktreePath:   worktree,
		Branch:         branch,
		TTL:            time.Hour,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match parent agent pack source") {
		t.Fatalf("RegisterDevelopmentLease unrelated origin error = %v", err)
	}
}

func TestDevelopmentLeaseCleanupRetainsWorktreeWhenOriginChanges(t *testing.T) {
	t.Parallel()
	registryPath, repo, worktree, namespace, branch := setupDevelopmentLeaseTest(t)
	if _, err := RegisterDevelopmentLease(registryPath, RegisterDevelopmentLeaseOptions{
		Namespace:      namespace,
		DevelopmentOf:  "team",
		RepositoryPath: repo,
		WorktreePath:   worktree,
		Branch:         branch,
		TTL:            time.Hour,
	}); err != nil {
		t.Fatalf("RegisterDevelopmentLease: %v", err)
	}
	runDevelopmentLeaseGit(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unrelated"))
	result, err := CleanupDevelopmentLease(registryPath, namespace)
	if err != nil {
		t.Fatalf("CleanupDevelopmentLease: %v", err)
	}
	if result.Status != DevelopmentLeaseRetained ||
		!strings.Contains(result.Reason, "does not match parent agent pack source") {
		t.Fatalf("changed-origin cleanup result = %#v, want retained source mismatch", result)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("source-mismatched worktree was removed: %v", err)
	}
}

func setupDevelopmentLeaseTest(t *testing.T) (registryPath, repo, worktree, namespace, branch string) {
	t.Helper()
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	worktree = filepath.Join(root, "worktree")
	namespace = "team-task-dev"
	branch = "amux-skill/task"
	runDevelopmentLeaseGit(t, "", "init", "-b", "main", repo)
	runDevelopmentLeaseGit(t, repo, "config", "user.name", "amux test")
	runDevelopmentLeaseGit(t, repo, "config", "user.email", "amux@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runDevelopmentLeaseGit(t, repo, "add", "README.md")
	runDevelopmentLeaseGit(t, repo, "commit", "-m", "base")
	runDevelopmentLeaseGit(t, repo, "remote", "add", "origin", repo)
	runDevelopmentLeaseGit(t, repo, "worktree", "add", "-b", branch, worktree, "main")

	registryPath = filepath.Join(root, "agent-packs", "registry.json")
	materializedPath := filepath.Join(root, "agent-packs", namespace, "repo")
	if err := os.MkdirAll(materializedPath, 0700); err != nil {
		t.Fatalf("mkdir materialized path: %v", err)
	}
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {
			Namespace:        "team",
			SourceKind:       PackSourceGit,
			GitURL:           repo,
			MaterializedPath: filepath.Join(root, "agent-packs", "team", "repo"),
		},
		namespace: {
			Namespace:        namespace,
			SourceKind:       PackSourceGit,
			GitURL:           repo,
			Ref:              branch,
			DevelopmentOf:    "team",
			Temporary:        true,
			MaterializedPath: materializedPath,
		},
	}}
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}
	return registryPath, repo, worktree, namespace, branch
}

func runDevelopmentLeaseGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestDevelopmentLeaseOperatorCleanupAndRelease(t *testing.T) {
	t.Parallel()
	registryPath, repo, worktree, namespace, branch := setupDevelopmentLeaseTest(t)
	register := func() {
		t.Helper()
		if _, err := RegisterDevelopmentLease(registryPath, RegisterDevelopmentLeaseOptions{
			Namespace: namespace, DevelopmentOf: "team", RepositoryPath: repo, WorktreePath: worktree, Branch: branch, OwnerSessionID: "session-live", TTL: time.Hour,
		}); err != nil {
			t.Fatalf("RegisterDevelopmentLease: %v", err)
		}
	}
	register()
	// Uncommitted work keeps the lease: cleanup never discards a dirty worktree.
	if err := os.WriteFile(filepath.Join(worktree, "draft.md"), []byte("wip\n"), 0600); err != nil {
		t.Fatal(err)
	}
	guardErr := errors.New("blocked")
	if _, err := CleanupDevelopmentLeaseIf(context.Background(), registryPath, namespace, func(Pack) error { return guardErr }); !errors.Is(err, guardErr) {
		t.Fatalf("guard not applied: %v", err)
	}
	result, err := CleanupDevelopmentLeaseIf(context.Background(), registryPath, namespace, nil)
	if err != nil || result.Status != DevelopmentLeaseRetained || !strings.Contains(result.Reason, "uncommitted") {
		t.Fatalf("dirty cleanup = %#v err=%v", result, err)
	}
	// Release unregisters the Pack and forgets the lease but keeps the worktree.
	removed, err := ReleaseDevelopmentLeaseIf(context.Background(), registryPath, namespace, nil)
	if err != nil || removed.Pack.Namespace != namespace {
		t.Fatalf("release = %#v err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "draft.md")); err != nil {
		t.Fatalf("release touched the worktree: %v", err)
	}
	registry, _ := LoadPackRegistry(registryPath)
	leases, _ := LoadDevelopmentLeaseRegistry(registryPath)
	if _, ok := registry.Packs[namespace]; ok || len(leases.Leases) != 0 {
		t.Fatalf("pack=%v leases=%v", registry.Packs, leases.Leases)
	}
	if _, err := ReleaseDevelopmentLeaseIf(context.Background(), registryPath, namespace, nil); err == nil {
		t.Fatal("released an unknown lease")
	}
}
