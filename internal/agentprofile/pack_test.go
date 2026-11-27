package agentprofile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSyncPackStopsWaitingForRegistryLockWhenContextExpires(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	if err := SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", SourceKind: PackSourceGit},
	}}); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	lockFile, err := os.OpenFile(registryPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open registry lock: %v", err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("hold registry lock: %v", err)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, syncErr := SyncPack(ctx, registryPath, "team")
		done <- syncErr
	}()

	select {
	case syncErr := <-done:
		if !errors.Is(syncErr, context.DeadlineExceeded) {
			t.Fatalf("SyncPack error = %v, want context deadline exceeded", syncErr)
		}
	case <-time.After(250 * time.Millisecond):
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		lockHeld = false
		<-done
		t.Fatal("SyncPack kept waiting for registry lock after context deadline")
	}
}

func TestSyncPackDoesNotHoldRegistryLockDuringGitFetch(t *testing.T) {
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{Ref: "stable", AutoSync: true, SyncInterval: defaultPackSyncInterval}); err != nil {
		t.Fatalf("AddGitPack: %v", err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up git: %v", err)
	}
	binDir := t.TempDir()
	fetchStarted := filepath.Join(root, "fetch-started")
	gitPath := filepath.Join(binDir, "git")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = fetch ]; then\n" +
		"  : > \"$AMUX_TEST_FETCH_STARTED\"\n" +
		"  exec sleep 30\n" +
		"fi\n" +
		"exec \"$AMUX_TEST_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(gitPath, []byte(script), 0700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("AMUX_TEST_FETCH_STARTED", fetchStarted)
	t.Setenv("AMUX_TEST_REAL_GIT", realGit)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	syncCtx, cancelSync := context.WithCancel(context.Background())
	defer cancelSync()
	syncDone := make(chan error, 1)
	go func() {
		_, syncErr := SyncPack(syncCtx, registryPath, "team")
		syncDone <- syncErr
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(fetchStarted); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat fetch marker: %v", err)
		}
		if time.Now().After(deadline) {
			cancelSync()
			<-syncDone
			t.Fatal("git fetch did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	lockCtx, cancelLock := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelLock()
	if err := WithPackRegistryLockContext(lockCtx, registryPath, func() error {
		_, loadErr := LoadPackRegistry(registryPath)
		return loadErr
	}); err != nil {
		cancelSync()
		<-syncDone
		t.Fatalf("registry read blocked behind git fetch: %v", err)
	}

	cancelSync()
	if err := <-syncDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("SyncPack error = %v, want context canceled", err)
	}
}

func TestSyncPackMigratesLegacySourcePathToRepoPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	oldPath := filepath.Join(root, ".amux", "agent-packs", "team", "source")
	newPath := filepath.Join(root, ".amux", "agent-packs", "team", "repo")
	runPackGit(t, "", "clone", upstream, oldPath)

	registry := PackRegistry{Packs: map[string]Pack{
		"team": {
			Namespace:        "team",
			SourceKind:       PackSourceGit,
			GitURL:           upstream,
			Ref:              "stable",
			MaterializedPath: oldPath,
		},
	}}
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	pack, err := SyncPack(context.Background(), registryPath, "team")
	if err != nil {
		t.Fatalf("SyncPack: %v", err)
	}
	if pack.MaterializedPath != newPath {
		t.Fatalf("MaterializedPath = %q, want %q", pack.MaterializedPath, newPath)
	}
	if _, err := os.Stat(filepath.Join(newPath, ".git")); err != nil {
		t.Fatalf("expected migrated repo git dir: %v", err)
	}
	if _, err := os.Stat(oldPath); err == nil {
		t.Fatal("legacy source path still exists, want moved")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat legacy source path: %v", err)
	}
	saved, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if saved.Packs["team"].MaterializedPath != newPath {
		t.Fatalf("saved MaterializedPath = %q, want %q", saved.Packs["team"].MaterializedPath, newPath)
	}
}

func TestRemovePackDeletesKeyNamespaceRootNotStoredPackNamespace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	teamRoot := filepath.Join(root, ".amux", "agent-packs", "team")
	otherRoot := filepath.Join(root, ".amux", "agent-packs", "other")
	if err := os.MkdirAll(filepath.Join(teamRoot, "repo"), 0700); err != nil {
		t.Fatalf("mkdir team root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(otherRoot, "repo"), 0700); err != nil {
		t.Fatalf("mkdir other root: %v", err)
	}
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {
			Namespace:        "other",
			SourceKind:       PackSourceGit,
			MaterializedPath: filepath.Join(teamRoot, "repo"),
		},
	}}
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	result, err := RemovePack(registryPath, "team", RemovePackOptions{})
	if err != nil {
		t.Fatalf("RemovePack: %v", err)
	}
	if result.RemovedPath != teamRoot || !result.FilesRemoved {
		t.Fatalf("RemovePack result = %+v, want removed team root", result)
	}
	if _, err := os.Stat(teamRoot); err == nil {
		t.Fatal("team root still exists, want removed")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat team root: %v", err)
	}
	if _, err := os.Stat(otherRoot); err != nil {
		t.Fatalf("other root should not be removed: %v", err)
	}
	saved, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if _, ok := saved.Packs["team"]; ok {
		t.Fatal("team pack still registered, want removed")
	}
}

func TestSyncPackWithEmptyRefTracksLatestRemoteHead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")

	pack, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{Ref: "", AutoSync: true, SyncInterval: defaultPackSyncInterval})
	if err != nil {
		t.Fatalf("AddGitPack: %v", err)
	}
	initialRevision := pack.CurrentRevision

	if err := os.WriteFile(filepath.Join(upstream, "CHANGELOG.md"), []byte("new revision\n"), 0600); err != nil {
		t.Fatalf("write new revision file: %v", err)
	}
	runPackGit(t, upstream, "add", ".")
	runPackGit(t, upstream, "commit", "-m", "add new profile")

	pack, err = SyncPack(context.Background(), registryPath, "team")
	if err != nil {
		t.Fatalf("SyncPack: %v", err)
	}
	if pack.CurrentRevision == initialRevision {
		t.Fatalf("CurrentRevision stayed at %s, want latest remote HEAD", pack.CurrentRevision)
	}
	if _, err := os.Stat(filepath.Join(pack.MaterializedPath, "CHANGELOG.md")); err != nil {
		t.Fatalf("expected synced file from latest remote HEAD: %v", err)
	}
}

func TestAddGitPackValidatesManifestAfterCheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	if err := os.RemoveAll(filepath.Join(upstream, "profiles")); err != nil {
		t.Fatalf("remove legacy profiles: %v", err)
	}
	writeManifest(t, upstream, `{
  "schema_version": "v2",
  "pack_namespace": "example-team",
  "profiles": {
    "broken": {
      "namespace": "common",
      "instructions": [
        "instructions/missing.md"
      ],
      "skills": [],
      "subagents": [],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "broken"
      ]
    }
  }
}`)
	runPackGit(t, upstream, "add", ".")
	runPackGit(t, upstream, "commit", "-m", "add broken manifest")

	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	_, err := AddGitPackWithOptions(context.Background(), registryPath, "expert-agents", upstream, AddGitPackOptions{Ref: "", AutoSync: true, SyncInterval: defaultPackSyncInterval})
	if err == nil {
		t.Fatal("AddGitPack succeeded, want manifest validation error")
	}
	if !strings.Contains(err.Error(), "read profile content instructions/missing.md") {
		t.Fatalf("AddGitPack error = %v, want missing manifest instruction file", err)
	}
	registry, loadErr := LoadPackRegistry(registryPath)
	if loadErr != nil {
		t.Fatalf("LoadPackRegistry: %v", loadErr)
	}
	if _, ok := registry.Packs["expert-agents"]; ok {
		t.Fatal("invalid manifest pack was saved to registry")
	}
}

func TestAddGitPackRequiresManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := filepath.Join(root, "legacy-upstream")
	if err := os.MkdirAll(filepath.Join(upstream, "profiles"), 0700); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(upstream, "profiles", "coder.toml"), []byte("type = \"codex\"\n"), 0600); err != nil {
		t.Fatalf("write legacy profile: %v", err)
	}
	runPackGit(t, upstream, "init")
	runPackGit(t, upstream, "config", "user.name", "amux test")
	runPackGit(t, upstream, "config", "user.email", "amux@example.com")
	runPackGit(t, upstream, "add", ".")
	runPackGit(t, upstream, "commit", "-m", "legacy pack")

	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	_, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{Ref: "", AutoSync: true, SyncInterval: defaultPackSyncInterval})
	if err == nil {
		t.Fatal("AddGitPack succeeded without manifest, want manifest required error")
	}
	if !strings.Contains(err.Error(), "agent pack manifest") || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("AddGitPack error = %v, want manifest required error", err)
	}
}

func TestSyncPackFailureRecordsLastErrorAndKeepsLastRevision(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	pack, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{Ref: "stable", AutoSync: true, SyncInterval: defaultPackSyncInterval})
	if err != nil {
		t.Fatalf("AddGitPack: %v", err)
	}
	initialRevision := pack.CurrentRevision
	initialSyncedAt := pack.LastSyncedAt
	if initialSyncedAt == "" {
		t.Fatal("LastSyncedAt empty after successful add")
	}

	if err := os.Rename(upstream, filepath.Join(root, "upstream-gone")); err != nil {
		t.Fatalf("rename upstream: %v", err)
	}
	if _, err := SyncPack(context.Background(), registryPath, "team"); err == nil {
		t.Fatal("SyncPack succeeded after upstream was removed, want error")
	}

	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	saved := registry.Packs["team"]
	if saved.CurrentRevision != initialRevision {
		t.Fatalf("CurrentRevision = %q, want preserved %q", saved.CurrentRevision, initialRevision)
	}
	if saved.LastSyncedAt != initialSyncedAt {
		t.Fatalf("LastSyncedAt = %q, want preserved %q", saved.LastSyncedAt, initialSyncedAt)
	}
	if saved.LastError == "" {
		t.Fatal("LastError empty after failed sync")
	}
}

func TestGitFetchWithTimeoutStopsHungFetch(t *testing.T) {
	if packGitFetchTimeout != 3*time.Minute {
		t.Fatalf("packGitFetchTimeout = %s, want 3m", packGitFetchTimeout)
	}

	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	started := time.Now()
	_, err := gitFetchWithTimeout(context.Background(), t.TempDir(), 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gitFetchWithTimeout error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("gitFetchWithTimeout elapsed = %s, want bounded fetch", elapsed)
	}
}

func TestSyncPackFailureSanitizesLastError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {
			Namespace:        "team",
			SourceKind:       PackSourceGit,
			GitURL:           "https://user:secret-token@example.com/org/repo.git?access_token=abc123",
			Ref:              "stable",
			MaterializedPath: filepath.Join(root, ".amux", "agent-packs", "team", "repo"),
			CurrentRevision:  "abc123",
		},
	}}
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	if _, err := SyncPack(context.Background(), registryPath, "team"); err == nil {
		t.Fatal("SyncPack succeeded, want clone/fetch error")
	}

	saved, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	lastErr := saved.Packs["team"].LastError
	if strings.Contains(lastErr, "\n") || strings.Contains(lastErr, "\r") {
		t.Fatalf("LastError contains newline: %q", lastErr)
	}
	for _, secret := range []string{"secret-token", "access_token=abc123", "user:secret-token@"} {
		if strings.Contains(lastErr, secret) {
			t.Fatalf("LastError leaked %q: %q", secret, lastErr)
		}
	}
	if len(lastErr) > 512 {
		t.Fatalf("LastError length = %d, want <= 512: %q", len(lastErr), lastErr)
	}
}

func TestSyncPackManifestFailureRollsBackMaterializedCheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	pack, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{Ref: "stable", AutoSync: true, SyncInterval: defaultPackSyncInterval})
	if err != nil {
		t.Fatalf("AddGitPack: %v", err)
	}
	initialRevision := pack.CurrentRevision

	if err := os.RemoveAll(filepath.Join(upstream, "profiles")); err != nil {
		t.Fatalf("remove profiles: %v", err)
	}
	writeManifest(t, upstream, `{
  "schema_version": "v2",
  "pack_namespace": "example-team",
  "profiles": {
    "broken": {
      "namespace": "common",
      "instructions": [
        "instructions/missing.md"
      ],
      "skills": [],
      "subagents": [],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "broken"
      ]
    }
  }
}`)
	runPackGit(t, upstream, "add", ".")
	runPackGit(t, upstream, "commit", "-m", "add broken manifest")
	brokenRevision := packGitRev(t, upstream)
	if brokenRevision == initialRevision {
		t.Fatal("test repo revision did not change")
	}

	if _, err := SyncPack(context.Background(), registryPath, "team"); err == nil {
		t.Fatal("SyncPack succeeded, want manifest validation error")
	}

	materializedRevision := packGitRev(t, pack.MaterializedPath)
	if materializedRevision != initialRevision {
		t.Fatalf("materialized revision = %q, want rollback to %q", materializedRevision, initialRevision)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if got := registry.Packs["team"].CurrentRevision; got != initialRevision {
		t.Fatalf("registry revision = %q, want %q", got, initialRevision)
	}
}

func TestLoadPackRegistryDefaultsOldPacksToAutoSync(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	if err := os.MkdirAll(filepath.Dir(registryPath), 0700); err != nil {
		t.Fatalf("mkdir registry dir: %v", err)
	}
	if err := os.WriteFile(registryPath, []byte(`{"packs":{"team":{"namespace":"team","source_kind":"git","materialized_path":"/tmp/team/repo"}}}`+"\n"), 0600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	pack := registry.Packs["team"]
	if !pack.AutoSync {
		t.Fatal("AutoSync = false for old registry entry, want default true")
	}
	if pack.SyncInterval != defaultPackSyncInterval {
		t.Fatalf("SyncInterval = %q, want %q", pack.SyncInterval, defaultPackSyncInterval)
	}
}

func TestAddGitPackRejectsDuplicateFormalRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "first", upstream, AddGitPackOptions{}); err != nil {
		t.Fatalf("AddGitPackWithOptions(first): %v", err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "second", upstream+"/", AddGitPackOptions{}); err == nil ||
		!strings.Contains(err.Error(), `already registered as namespace "first"`) {
		t.Fatalf("AddGitPackWithOptions(second) error = %v, want duplicate repository rejection", err)
	}
}

func TestAddGitPackRequiresExplicitDevelopmentParentForSameRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{}); err != nil {
		t.Fatalf("AddGitPackWithOptions(team): %v", err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team-dev", upstream, AddGitPackOptions{}); err == nil ||
		!strings.Contains(err.Error(), `already registered as namespace "team"`) {
		t.Fatalf("implicit team-dev error = %v, want duplicate repository rejection", err)
	}
	dev, err := AddGitPackWithOptions(context.Background(), registryPath, "team-dev", upstream, AddGitPackOptions{
		Ref:           "stable",
		DevelopmentOf: "team",
	})
	if err != nil {
		t.Fatalf("explicit AddGitPackWithOptions(team-dev): %v", err)
	}
	if dev.DevelopmentOf != "team" {
		t.Fatalf("team-dev DevelopmentOf = %q, want explicit parent team", dev.DevelopmentOf)
	}
}

func TestAddGitPackAllowsIndependentTemporaryDevelopmentPacks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{}); err != nil {
		t.Fatalf("AddGitPackWithOptions(team): %v", err)
	}
	first, err := AddGitPackWithOptions(context.Background(), registryPath, "team-task-a-dev", upstream, AddGitPackOptions{
		Ref:           "stable",
		DevelopmentOf: "team",
		Temporary:     true,
		AutoSync:      true,
		SyncInterval:  "30s",
	})
	if err != nil {
		t.Fatalf("AddGitPackWithOptions(team-task-a-dev): %v", err)
	}
	second, err := AddGitPackWithOptions(context.Background(), registryPath, "team-task-b-dev", upstream, AddGitPackOptions{
		Ref:           "stable",
		DevelopmentOf: "team",
		Temporary:     true,
		AutoSync:      true,
		SyncInterval:  "30s",
	})
	if err != nil {
		t.Fatalf("AddGitPackWithOptions(team-task-b-dev): %v", err)
	}
	if first.DevelopmentOf != "team" || second.DevelopmentOf != "team" || !first.Temporary || !second.Temporary {
		t.Fatalf("temporary development packs = %#v, %#v", first, second)
	}
	if first.MaterializedPath == second.MaterializedPath {
		t.Fatalf("temporary packs share materialized path %q", first.MaterializedPath)
	}
	if first.AutoSync || second.AutoSync {
		t.Fatalf("temporary packs auto sync = %v, %v, want both false", first.AutoSync, second.AutoSync)
	}
}

func TestAddGitPackAllowsTemporaryDevelopmentAlongsideExistingLegacyPair(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{}); err != nil {
		t.Fatalf("AddGitPackWithOptions(team): %v", err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	registry.Packs["team-dev"] = Pack{
		Namespace:        "team-dev",
		SourceKind:       PackSourceGit,
		GitURL:           upstream,
		Ref:              "amux-dev",
		MaterializedPath: packMaterializedPath(registryPath, "team-dev"),
		AutoSync:         true,
		SyncInterval:     "30s",
	}
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatalf("SavePackRegistry with legacy pair: %v", err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{}); err != nil {
		t.Fatalf("idempotent re-add of formal Pack alongside legacy pair: %v", err)
	}

	temporary, err := AddGitPackWithOptions(context.Background(), registryPath, "team-task-a-dev", upstream, AddGitPackOptions{
		Ref:           "stable",
		DevelopmentOf: "team",
		Temporary:     true,
	})
	if err != nil {
		t.Fatalf("AddGitPackWithOptions temporary alongside legacy pair: %v", err)
	}
	if temporary.DevelopmentOf != "team" || !temporary.Temporary {
		t.Fatalf("temporary development pack = %#v", temporary)
	}
}

func TestAddGitPackRejectsChangingDevelopmentIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{}); err != nil {
		t.Fatalf("AddGitPackWithOptions(team): %v", err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team-task-a-dev", upstream, AddGitPackOptions{
		Ref:           "stable",
		DevelopmentOf: "team",
		Temporary:     true,
	}); err != nil {
		t.Fatalf("AddGitPackWithOptions(team-task-a-dev): %v", err)
	}

	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team-task-a-dev", upstream, AddGitPackOptions{
		Ref:           "stable",
		DevelopmentOf: "team",
		Temporary:     false,
	}); err == nil || !strings.Contains(err.Error(), "development identity") {
		t.Fatalf("re-register with changed temporary status error = %v, want development identity rejection", err)
	}
}

func TestAddGitPackRejectsSuffixOnlyDevAfterTemporaryDevelopmentPack(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{}); err != nil {
		t.Fatalf("AddGitPackWithOptions(team): %v", err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team-task-a-dev", upstream, AddGitPackOptions{
		Ref:           "stable",
		DevelopmentOf: "team",
		Temporary:     true,
	}); err != nil {
		t.Fatalf("AddGitPackWithOptions(team-task-a-dev): %v", err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team-dev", upstream, AddGitPackOptions{}); err == nil ||
		!strings.Contains(err.Error(), `already registered as namespace "team"`) {
		t.Fatalf("suffix-only team-dev error = %v, want duplicate repository rejection", err)
	}
}

func TestAddGitPackRejectsUnpairedDevelopmentNamespace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{}); err != nil {
		t.Fatalf("AddGitPackWithOptions(team): %v", err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "other-dev", upstream, AddGitPackOptions{}); err == nil ||
		!strings.Contains(err.Error(), `already registered as namespace "team"`) {
		t.Fatalf("AddGitPackWithOptions(other-dev) error = %v, want unpaired development namespace rejection", err)
	}
}

func TestAddGitPackRejectsNamespaceSourceReplacement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := initPackGitRepo(t, filepath.Join(root, "first"), "v1")
	second := initPackGitRepo(t, filepath.Join(root, "second"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", first, AddGitPackOptions{}); err != nil {
		t.Fatalf("AddGitPackWithOptions(first): %v", err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", second, AddGitPackOptions{}); err == nil ||
		!strings.Contains(err.Error(), `namespace "team" is already registered`) {
		t.Fatalf("AddGitPackWithOptions(second) error = %v, want namespace source rejection", err)
	}
}

func TestPackRepositoryNameSupportsCommonGitURLForms(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"git@example.com:org/tools.git":                   "tools",
		"https://example.com/org/tools.git/":              "tools",
		"https://user:secret@example.com/org/tools.git?q": "tools",
		"/tmp/local-tools":                                "local-tools",
	} {
		if got := PackRepositoryName(input); got != want {
			t.Fatalf("PackRepositoryName(%q) = %q, want %q", input, got, want)
		}
	}
}

func initPackGitRepo(t *testing.T, repo, value string) string {
	t.Helper()
	writePackContent(t, repo, "instructions/coder/AGENTS.md", value+"\n")
	writeManifest(t, repo, `{
  "schema_version": "v2",
  "pack_namespace": "example-team",
  "profiles": {
    "coder": {
      "namespace": "common",
      "instructions": [
        "instructions/coder/AGENTS.md"
      ],
      "skills": [],
      "subagents": [],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "coder"
      ]
    }
  }
}`)
	runPackGit(t, repo, "init")
	runPackGit(t, repo, "config", "user.name", "amux test")
	runPackGit(t, repo, "config", "user.email", "amux@example.com")
	runPackGit(t, repo, "add", ".")
	runPackGit(t, repo, "commit", "-m", "init")
	runPackGit(t, repo, "checkout", "-b", "stable")
	return repo
}

func runPackGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func packGitRev(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func TestRemovePackIfAsksTheGuardInsideTheLockAndStopsWaitingWithTheContext(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	teamRoot := filepath.Join(root, ".amux", "agent-packs", "team")
	if err := os.MkdirAll(filepath.Join(teamRoot, "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{"team": {Namespace: "team", SourceKind: PackSourceGit, CurrentRevision: "abc", MaterializedPath: filepath.Join(teamRoot, "repo")}}}); err != nil {
		t.Fatal(err)
	}
	registered := func() bool {
		registry, err := LoadPackRegistry(registryPath)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := registry.Packs["team"]
		return ok
	}

	// A refusing guard sees the registered Pack and leaves the registry and files alone.
	refused := errors.New("still referenced")
	var seen Pack
	if _, err := RemovePackIf(context.Background(), registryPath, "team", RemovePackOptions{}, func(pack Pack) error { seen = pack; return refused }); !errors.Is(err, refused) {
		t.Fatalf("guard error = %v", err)
	}
	if seen.CurrentRevision != "abc" || !registered() {
		t.Fatalf("guard saw %+v, registered=%v", seen, registered())
	}
	if _, err := os.Stat(teamRoot); err != nil {
		t.Fatalf("a refused removal touched the files: %v", err)
	}

	// The guard runs while the registry lock is held, so nobody can change the registry meanwhile.
	if _, err := RemovePackIf(context.Background(), registryPath, "team", RemovePackOptions{KeepFiles: true}, func(Pack) error {
		short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := WithPackRegistryLockContext(short, registryPath, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the lock was not held while the guard ran: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if registered() {
		t.Fatal("an approved removal left the Pack registered")
	}
	if _, err := os.Stat(teamRoot); err != nil {
		t.Fatalf("keep-files removal deleted the files: %v", err)
	}

	// Waiting for a lock held by someone else ends with the context; nothing is removed.
	if err := SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{"team": {Namespace: "team", SourceKind: PackSourceGit}}}); err != nil {
		t.Fatal(err)
	}
	holder := make(chan struct{})
	held := make(chan struct{})
	go func() {
		_ = WithPackRegistryLock(registryPath, func() error { close(held); <-holder; return nil })
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	called := false
	_, err := RemovePackIf(ctx, registryPath, "team", RemovePackOptions{}, func(Pack) error { called = true; return nil })
	close(holder)
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("waiting for a held lock: err=%v guardCalled=%v", err, called)
	}
	if !registered() {
		t.Fatal("a timed-out removal removed the Pack")
	}

	// Cancellation while the guard is running must not unregister the Pack.
	guardCtx, cancelGuard := context.WithCancel(context.Background())
	_, err = RemovePackIf(guardCtx, registryPath, "team", RemovePackOptions{}, func(Pack) error {
		cancelGuard()
		return nil
	})
	if !errors.Is(err, context.Canceled) || !registered() {
		t.Fatalf("cancelled guard: err=%v registered=%v", err, registered())
	}

	// An unknown namespace is still reported by the removal, without asking the guard.
	called = false
	if _, err := RemovePackIf(context.Background(), registryPath, "missing", RemovePackOptions{}, func(Pack) error { called = true; return nil }); err == nil || called {
		t.Fatalf("unknown namespace: err=%v guardCalled=%v", err, called)
	}
}
