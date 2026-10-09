package agentprofile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestPackCatalogManagerInstallSnapshotAndRemove(t *testing.T) {
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	manager, err := NewPackCatalogManager(registryPath, []CatalogPack{{
		ID:          "team",
		Namespace:   "team",
		DisplayName: "Team Agents",
		Description: "Team profiles",
		GitURL:      upstream,
		Ref:         "stable",
	}})
	if err != nil {
		t.Fatalf("NewPackCatalogManager: %v", err)
	}

	before, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot before install: %v", err)
	}
	if len(before.Market) != 1 || len(before.Loaded) != 0 || len(before.Builtins) == 0 {
		t.Fatalf("snapshot before install = %+v, want one available pack", before)
	}

	installed, err := manager.Install(context.Background(), "team")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if installed.Namespace != "team" || installed.CurrentRevision == "" {
		t.Fatalf("installed pack = %+v, want synced team revision", installed)
	}
	if !installed.AutoSync || installed.SyncInterval != "30s" {
		t.Fatalf("Feishu catalog install autosync = %v interval = %q, want enabled at 30s", installed.AutoSync, installed.SyncInterval)
	}

	after, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after install: %v", err)
	}
	if len(after.Loaded) != 1 || after.Loaded[0].ProfileCount != 1 || !after.Loaded[0].AutoSync || len(after.Market) != 0 {
		t.Fatalf("snapshot after install = %+v, want one installed profile", after)
	}
	materializedPath := installed.MaterializedPath

	removed, err := manager.Remove("team")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if removed.FilesRemoved {
		t.Fatalf("Remove = %+v, want Feishu keep-files semantics", removed)
	}
	if _, err := os.Stat(materializedPath); err != nil {
		t.Fatalf("materialized path after remove: %v, want retained files", err)
	}
	final, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after remove: %v", err)
	}
	if len(final.Loaded) != 0 || len(final.Market) != 1 {
		t.Fatalf("snapshot after remove = %+v, want catalog entry available again", final)
	}
	if _, err := manager.Remove("team"); err == nil {
		t.Fatal("second Remove succeeded for a library that is no longer loaded")
	}
}

func TestPackCatalogManagerSnapshotStopsWaitingForRegistryLockWhenContextExpires(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	manager, err := NewPackCatalogManager(registryPath, nil)
	if err != nil {
		t.Fatalf("NewPackCatalogManager: %v", err)
	}
	if err := SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{}}); err != nil {
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
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = manager.SnapshotContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SnapshotContext error = %v, want context deadline exceeded", err)
	}
}

func TestPackCatalogManagerEnablesAutoSyncForInstalledCatalogPacks(t *testing.T) {
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	customUpstream := initPackGitRepo(t, filepath.Join(root, "custom-upstream"), "v1")
	registryPath := filepath.Join(root, ".amux", "agent-packs", "registry.json")
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "team", upstream, AddGitPackOptions{
		Ref: "stable", AutoSync: false,
	}); err != nil {
		t.Fatalf("AddGitPackWithOptions: %v", err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, "custom", customUpstream, AddGitPackOptions{
		Ref: "stable", AutoSync: false,
	}); err != nil {
		t.Fatalf("AddGitPackWithOptions(custom): %v", err)
	}
	manager, err := NewPackCatalogManager(registryPath, []CatalogPack{localCatalogPack("team", upstream)})
	if err != nil {
		t.Fatalf("NewPackCatalogManager: %v", err)
	}

	if err := manager.EnableAutoSyncForInstalledCatalogPacks(); err != nil {
		t.Fatalf("EnableAutoSyncForInstalledCatalogPacks: %v", err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if pack := registry.Packs["team"]; !pack.AutoSync || pack.SyncInterval != "30s" {
		t.Fatalf("migrated pack autosync = %v interval = %q, want enabled at 30s", pack.AutoSync, pack.SyncInterval)
	}
	if pack := registry.Packs["custom"]; pack.AutoSync {
		t.Fatal("custom non-Catalog pack autosync = true, want unchanged")
	}
}

func localCatalogPack(namespace, gitURL string) CatalogPack {
	return CatalogPack{
		ID: namespace, Namespace: namespace, DisplayName: namespace, GitURL: gitURL, Ref: "stable",
	}
}

func TestPackCatalogManagerRemoveRechecksReferencesAfterProfileSelection(t *testing.T) {
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	manager, err := NewPackCatalogManager(filepath.Join(root, "registry.json"), []CatalogPack{{
		ID: "team", Namespace: "team", DisplayName: "Team", GitURL: upstream, Ref: "stable",
	}})
	if err != nil {
		t.Fatalf("NewPackCatalogManager: %v", err)
	}
	if _, err := manager.Install(context.Background(), "team"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	secondManager, err := NewPackCatalogManager(filepath.Join(root, "registry.json"), []CatalogPack{{
		ID: "team", Namespace: "team", DisplayName: "Team", GitURL: upstream, Ref: "stable",
	}})
	if err != nil {
		t.Fatalf("NewPackCatalogManager(second): %v", err)
	}

	selectionStarted := make(chan struct{})
	releaseSelection := make(chan struct{})
	selectionDone := make(chan error, 1)
	var referenced atomic.Bool
	go func() {
		selectionDone <- manager.WithRegistryStable(func() error {
			close(selectionStarted)
			<-releaseSelection
			referenced.Store(true)
			return nil
		})
	}()
	<-selectionStarted
	removeDone := make(chan []string, 1)
	go func() {
		_, refs, removeErr := secondManager.RemoveIfUnreferenced("team", func() ([]string, error) {
			if referenced.Load() {
				return []string{"feishu:bot"}, nil
			}
			return nil, nil
		})
		if removeErr != nil {
			removeDone <- []string{"error:" + removeErr.Error()}
			return
		}
		removeDone <- refs
	}()
	select {
	case <-removeDone:
		t.Fatal("RemoveIfUnreferenced passed the in-flight Profile selection")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseSelection)
	if err := <-selectionDone; err != nil {
		t.Fatalf("WithRegistryStable: %v", err)
	}
	refs := <-removeDone
	if len(refs) != 1 || refs[0] != "feishu:bot" {
		t.Fatalf("blocking references = %#v, want live Bot reference", refs)
	}
	snapshot, err := manager.Snapshot()
	if err != nil || len(snapshot.Loaded) != 1 {
		t.Fatalf("snapshot after blocked remove = %+v, %v", snapshot, err)
	}
}

func TestPackCatalogManagerRegistryStableContextStopsWaitingForManagerLock(t *testing.T) {
	manager, err := NewPackCatalogManager(filepath.Join(t.TempDir(), "registry.json"), nil)
	if err != nil {
		t.Fatalf("NewPackCatalogManager: %v", err)
	}

	lockStarted := make(chan struct{})
	releaseLock := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- manager.WithRegistryStable(func() error {
			close(lockStarted)
			<-releaseLock
			return nil
		})
	}()
	<-lockStarted

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = manager.WithRegistryStableContext(ctx, func() error {
		t.Fatal("registry-stable callback ran without acquiring the manager lock")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WithRegistryStableContext error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("WithRegistryStableContext elapsed = %s, want bounded lock wait", elapsed)
	}

	close(releaseLock)
	if err := <-lockDone; err != nil {
		t.Fatalf("release registry-stable holder: %v", err)
	}
}

func TestPackCatalogManagerRejectsStaleCardGeneration(t *testing.T) {
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	manager, err := NewPackCatalogManager(filepath.Join(root, "registry.json"), []CatalogPack{{
		ID: "team", Namespace: "team", DisplayName: "Team", GitURL: upstream, Ref: "stable",
	}})
	if err != nil {
		t.Fatalf("NewPackCatalogManager: %v", err)
	}
	available, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot available: %v", err)
	}
	absentGeneration := available.Market[0].Generation
	if absentGeneration == "" {
		t.Fatal("available Catalog Pack has empty generation")
	}
	autoSyncStarts := 0
	ensureAutoSync := func(context.Context) error {
		autoSyncStarts++
		return nil
	}
	if _, err := manager.InstallIfCurrentWithAutoSync(context.Background(), "team", "stale", ensureAutoSync); !errors.Is(err, ErrCatalogStateChanged) {
		t.Fatalf("InstallIfCurrentWithAutoSync with stale generation = %v, want ErrCatalogStateChanged", err)
	}
	if autoSyncStarts != 0 {
		t.Fatalf("auto sync starts after stale install = %d, want 0", autoSyncStarts)
	}
	if _, err := manager.InstallIfCurrent(context.Background(), "team", absentGeneration); err != nil {
		t.Fatalf("InstallIfCurrent: %v", err)
	}
	if _, err := manager.SyncIfCurrentWithAutoSync(context.Background(), "team", absentGeneration, ensureAutoSync); !errors.Is(err, ErrCatalogStateChanged) {
		t.Fatalf("SyncIfCurrentWithAutoSync with stale absent generation = %v, want ErrCatalogStateChanged", err)
	}
	if autoSyncStarts != 0 {
		t.Fatalf("auto sync starts after stale sync = %d, want 0", autoSyncStarts)
	}
	installed, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot installed: %v", err)
	}
	oldCardGeneration := installed.Loaded[0].Generation
	writePackContent(t, upstream, "instructions/coder/AGENTS.md", "v2\n")
	runPackGit(t, upstream, "add", ".")
	runPackGit(t, upstream, "commit", "-m", "v2")
	if _, err := manager.Sync(context.Background(), "team"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, _, err := manager.RemoveIfCurrentAndUnreferenced("team", oldCardGeneration, func() ([]string, error) {
		return nil, nil
	}); !errors.Is(err, ErrCatalogStateChanged) {
		t.Fatalf("RemoveIfCurrentAndUnreferenced from stale card = %v, want ErrCatalogStateChanged", err)
	}
}

func TestPackCatalogManagerRemoveAcceptsCardAfterAutoSyncMetadataRefresh(t *testing.T) {
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	manager, err := NewPackCatalogManager(registryPath, []CatalogPack{{
		ID: "team", Namespace: "team", DisplayName: "Team", GitURL: upstream, Ref: "stable",
	}})
	if err != nil {
		t.Fatalf("NewPackCatalogManager: %v", err)
	}
	if _, err := manager.Install(context.Background(), "team"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	before, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot before auto sync: %v", err)
	}
	cardGeneration := before.Loaded[0].Generation

	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	pack := registry.Packs["team"]
	pack.LastSyncedAt = "2099-01-02T03:04:05Z"
	pack.LastError = "temporary background sync failure"
	registry.Packs["team"] = pack
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatalf("SavePackRegistry after metadata refresh: %v", err)
	}
	refreshed, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after metadata refresh: %v", err)
	}
	if refreshed.Loaded[0].Generation != cardGeneration {
		t.Fatalf(
			"generation after auto-sync metadata refresh = %q, want stable %q",
			refreshed.Loaded[0].Generation,
			cardGeneration,
		)
	}

	if _, _, err := manager.RemoveIfCurrentAndUnreferenced("team", cardGeneration, func() ([]string, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("RemoveIfCurrentAndUnreferenced after auto-sync metadata refresh = %v, want success", err)
	}
	after, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after remove: %v", err)
	}
	if len(after.Loaded) != 0 {
		t.Fatalf("snapshot after remove = %+v, want no loaded Pack", after)
	}
}

func TestPackCatalogManagerKeepsNamespaceCollisionSeparateFromMarket(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	manager, err := NewPackCatalogManager(registryPath, []CatalogPack{{
		ID:          "team",
		Namespace:   "team",
		DisplayName: "Team Agents",
		GitURL:      "https://example.invalid/team.git",
		Ref:         "stable",
	}})
	if err != nil {
		t.Fatalf("NewPackCatalogManager: %v", err)
	}
	if _, err := manager.Install(context.Background(), "missing"); err == nil {
		t.Fatal("Install(missing) succeeded")
	}
	if _, err := manager.Sync(context.Background(), "missing"); err == nil {
		t.Fatal("Sync(missing) succeeded")
	}
	if _, err := manager.Remove("missing"); err == nil {
		t.Fatal("Remove(missing) succeeded")
	}
	if err := SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{
		"team": {
			Namespace:  "team",
			SourceKind: PackSourceGit,
			GitURL:     "https://example.invalid/other.git",
			Ref:        "stable",
		},
	}}); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}
	snapshot, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Loaded) != 1 || len(snapshot.Market) != 1 {
		t.Fatalf("conflicting snapshot = %+v, want loaded custom source plus available market entry", snapshot)
	}
	if !snapshot.Market[0].NamespaceConflict {
		t.Fatalf("conflicting market state = %+v, want namespace conflict", snapshot.Market[0])
	}
	if _, err := manager.Install(context.Background(), "team"); err == nil {
		t.Fatal("Install(team) accepted a namespace owned by another Git URL")
	}
	if _, err := manager.Remove("team"); err != nil {
		t.Fatalf("Remove(team) rejected a loaded custom source: %v", err)
	}
}

func TestPackCatalogManagerRejectsMutationAfterDuplicateRepositoryAppears(t *testing.T) {
	root := t.TempDir()
	upstream := initPackGitRepo(t, filepath.Join(root, "upstream"), "v1")
	registryPath := filepath.Join(root, "registry.json")
	manager, err := NewPackCatalogManager(registryPath, []CatalogPack{localCatalogPack("team", upstream)})
	if err != nil {
		t.Fatalf("NewPackCatalogManager: %v", err)
	}
	if _, err := manager.Install(context.Background(), "team"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	before, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	generation := before.Loaded[0].Generation
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	duplicate := registry.Packs["team"]
	duplicate.Namespace = "legacy-team"
	registry.Packs[duplicate.Namespace] = duplicate
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatalf("SavePackRegistry duplicate: %v", err)
	}

	if _, err := manager.SyncIfCurrent(context.Background(), "team", generation); !errors.Is(err, ErrCatalogStateChanged) {
		t.Fatalf("SyncIfCurrent after duplicate = %v, want ErrCatalogStateChanged", err)
	}
	if _, _, err := manager.RemoveIfCurrentAndUnreferenced("team", generation, nil); !errors.Is(err, ErrCatalogStateChanged) {
		t.Fatalf("RemoveIfCurrentAndUnreferenced after duplicate = %v, want ErrCatalogStateChanged", err)
	}
	after, err := manager.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after rejected mutation: %v", err)
	}
	if len(after.Loaded) != 2 {
		t.Fatalf("loaded Packs after rejected mutation = %+v, want both historical entries retained", after.Loaded)
	}
}

func TestPackCatalogSnapshotSeparatesManagedLocalExpertsFromLoadedPacks(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), registryPath, &fakeManagedContractRunner{})
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Instructions: "Review changes."})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	manager, err := NewPackCatalogManager(registryPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Loaded) != 0 {
		t.Fatalf("managed local pack leaked into Loaded: %+v", snapshot.Loaded)
	}
	if len(snapshot.LocalExperts) != 1 {
		t.Fatalf("LocalExperts = %+v", snapshot.LocalExperts)
	}
	local := snapshot.LocalExperts[0]
	if local.Ref != "local/reviewer" || local.AgentType != "" || local.Revision != created.Revision || !local.Writable {
		t.Fatalf("local expert state = %+v", local)
	}
}
