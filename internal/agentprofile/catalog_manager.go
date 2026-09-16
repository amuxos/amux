package agentprofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const packCatalogSnapshotLockTimeout = 10 * time.Second

// BuiltinExpertState is the read-only user-facing projection of one embedded
// Profile in the expert-library management surface.
type BuiltinExpertState struct {
	Ref         string
	Name        string
	Description string
}

// LocalExpertState is the writable profile projection of the installation's
// managed local Git library. It is intentionally not a LoadedPackState because
// remote update/removal actions do not apply.
type LocalExpertState struct {
	Ref         string
	Name        string
	Description string
	AgentType   string
	Revision    string
	Writable    bool
}

// LoadedPackState is the current state of one formally registered expert
// library. Namespace remains the runtime identity; GitURL is the library
// identity used to match an installed Pack with the market.
type LoadedPackState struct {
	Namespace           string
	DisplayName         string
	GitURL              string
	Ref                 string
	CurrentRevision     string
	LastSyncedAt        string
	LastError           string
	ProfileCount        int
	AutoSync            bool
	InMarket            bool
	DuplicateNamespaces []string
	Generation          string
}

// CatalogPackState is one not-yet-loaded market entry.
type CatalogPackState struct {
	CatalogPack
	NamespaceConflict bool
	Generation        string
}

var ErrCatalogStateChanged = errors.New("Agent Pack catalog state changed")

// PackCatalogSnapshot is the point-in-time projection consumed by management
// surfaces. It intentionally separates embedded experts, registered libraries,
// and market entries so callers do not need to reconstruct product semantics.
type PackCatalogSnapshot struct {
	Builtins     []BuiltinExpertState
	LocalExperts []LocalExpertState
	Loaded       []LoadedPackState
	Market       []CatalogPackState
}

// PackCatalogManager serializes registry mutations shared by CLI-independent
// management surfaces such as Feishu.
type PackCatalogManager struct {
	mu              contextMutex
	registryPath    string
	catalog         []CatalogPack
	catalogByID     map[string]CatalogPack
	catalogByGitURL map[string]CatalogPack
}

// contextMutex keeps the existing mutual-exclusion semantics while allowing
// request-scoped management operations to stop waiting when their deadline
// expires. Pack mutations may involve external processes, so an ordinary
// sync.Mutex would make an otherwise bounded Feishu transition wait forever.
type contextMutex struct {
	token chan struct{}
}

func newContextMutex() contextMutex {
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return contextMutex{token: token}
}

func (m *contextMutex) Lock() {
	<-m.token
}

func (m *contextMutex) LockContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
		return nil
	}
}

func (m *contextMutex) Unlock() {
	m.token <- struct{}{}
}

func NewPackCatalogManager(registryPath string, catalog []CatalogPack) (*PackCatalogManager, error) {
	registryPath = strings.TrimSpace(registryPath)
	if registryPath == "" {
		return nil, fmt.Errorf("agent pack registry path is required")
	}
	normalized, err := normalizeCatalogPacks(catalog)
	if err != nil {
		return nil, err
	}
	manager := &PackCatalogManager{
		mu:              newContextMutex(),
		registryPath:    registryPath,
		catalog:         normalized,
		catalogByID:     make(map[string]CatalogPack, len(normalized)),
		catalogByGitURL: make(map[string]CatalogPack, len(normalized)),
	}
	for _, pack := range normalized {
		manager.catalogByID[pack.ID] = pack
		manager.catalogByGitURL[NormalizePackGitURL(pack.GitURL)] = pack
	}
	return manager, nil
}

func NewDefaultPackCatalogManager() *PackCatalogManager {
	manager, err := NewPackCatalogManager(DefaultPackRegistryPath(), CatalogPacks())
	if err != nil {
		panic(fmt.Errorf("create default Agent Pack catalog manager: %w", err))
	}
	return manager
}

func (m *PackCatalogManager) Snapshot() (PackCatalogSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), packCatalogSnapshotLockTimeout)
	defer cancel()
	return m.SnapshotContext(ctx)
}

// SnapshotContext builds the management projection without waiting forever on
// a registry transaction owned by another process. The catalog metadata is
// immutable after manager construction, so the cross-process registry lock is
// sufficient to keep this read consistent with Pack mutations.
func (m *PackCatalogManager) SnapshotContext(ctx context.Context) (PackCatalogSnapshot, error) {
	if m == nil {
		return PackCatalogSnapshot{}, fmt.Errorf("agent pack catalog manager is unavailable")
	}
	var snapshot PackCatalogSnapshot
	err := WithPackRegistryLockContext(ctx, m.registryPath, func() error {
		registry, err := LoadPackRegistry(m.registryPath)
		if err != nil {
			return err
		}
		snapshot = m.snapshot(registry)
		return nil
	})
	return snapshot, err
}

func (m *PackCatalogManager) snapshot(registry PackRegistry) PackCatalogSnapshot {
	snapshot := PackCatalogSnapshot{
		Builtins:     builtinExpertStates(),
		LocalExperts: []LocalExpertState{},
		Loaded:       make([]LoadedPackState, 0, len(registry.Packs)),
		Market:       make([]CatalogPackState, 0, len(m.catalog)),
	}

	formalByGitURL := make(map[string][]Pack)
	displaySources := make([]PackDisplaySource, 0, len(registry.Packs)+len(m.catalog))
	for _, pack := range ListPacks(registry) {
		if isDevelopmentPackNamespace(pack.Namespace) || isManagedLocalPack(pack) {
			continue
		}
		gitURL := NormalizePackGitURL(pack.GitURL)
		formalByGitURL[gitURL] = append(formalByGitURL[gitURL], pack)
		displaySources = append(displaySources, PackDisplaySource{
			Key: "loaded:" + pack.Namespace, Namespace: pack.Namespace, GitURL: gitURL,
		})
	}
	for _, entry := range m.catalog {
		if len(formalByGitURL[NormalizePackGitURL(entry.GitURL)]) == 0 {
			displaySources = append(displaySources, PackDisplaySource{
				Key:         "market:" + entry.ID,
				Namespace:   entry.Namespace,
				GitURL:      entry.GitURL,
				DisplayName: entry.DisplayName,
			})
		}
	}
	displayNames := DisambiguatePackDisplayNames(displaySources)

	for _, pack := range ListPacks(registry) {
		if isDevelopmentPackNamespace(pack.Namespace) {
			continue
		}
		if isManagedLocalPack(pack) {
			if profiles, err := listPackProfiles(pack); err == nil {
				for _, profile := range profiles {
					snapshot.LocalExperts = append(snapshot.LocalExperts, LocalExpertState{
						Ref: profile.Ref(), Name: profile.Name, Description: strings.TrimSpace(profile.Description),
						AgentType: profile.Type, Revision: strings.TrimSpace(pack.CurrentRevision), Writable: true,
					})
				}
			}
			continue
		}
		gitURL := NormalizePackGitURL(pack.GitURL)
		duplicates := formalByGitURL[gitURL]
		duplicateNamespaces := make([]string, 0, len(duplicates))
		for _, duplicate := range duplicates {
			if duplicate.Namespace != pack.Namespace {
				duplicateNamespaces = append(duplicateNamespaces, duplicate.Namespace)
			}
		}
		sort.Strings(duplicateNamespaces)
		profileCount := 0
		if profiles, err := listPackProfiles(pack); err == nil {
			profileCount = len(profiles)
		}
		_, inMarket := m.catalogByGitURL[gitURL]
		snapshot.Loaded = append(snapshot.Loaded, LoadedPackState{
			Namespace:           pack.Namespace,
			DisplayName:         displayNames["loaded:"+pack.Namespace],
			GitURL:              gitURL,
			Ref:                 strings.TrimSpace(pack.Ref),
			CurrentRevision:     strings.TrimSpace(pack.CurrentRevision),
			LastSyncedAt:        strings.TrimSpace(pack.LastSyncedAt),
			LastError:           strings.TrimSpace(pack.LastError),
			ProfileCount:        profileCount,
			AutoSync:            pack.AutoSync,
			InMarket:            inMarket,
			DuplicateNamespaces: duplicateNamespaces,
			Generation:          loadedPackGeneration(pack),
		})
	}

	for _, entry := range m.catalog {
		gitURL := NormalizePackGitURL(entry.GitURL)
		if len(formalByGitURL[gitURL]) > 0 {
			continue
		}
		entry.DisplayName = displayNames["market:"+entry.ID]
		occupant, occupied := registry.Packs[entry.Namespace]
		snapshot.Market = append(snapshot.Market, CatalogPackState{
			CatalogPack:       entry,
			NamespaceConflict: occupied && !SamePackGitURL(occupant.GitURL, entry.GitURL),
			Generation:        catalogPackGeneration(entry, registry),
		})
	}
	return snapshot
}

func builtinExpertStates() []BuiltinExpertState {
	profiles := Builtins()
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Ref() < profiles[j].Ref()
	})
	out := make([]BuiltinExpertState, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, BuiltinExpertState{
			Ref:         profile.Ref(),
			Name:        profile.Name,
			Description: strings.TrimSpace(profile.Description),
		})
	}
	return out
}

func isDevelopmentPackNamespace(namespace string) bool {
	return strings.HasSuffix(strings.TrimSpace(namespace), "-dev")
}

func isManagedLocalPack(pack Pack) bool {
	return strings.TrimSpace(pack.Namespace) == ManagedLocalPackNamespace && strings.TrimSpace(pack.ManagedBy) == ManagedPackOwnerAmux
}

func (m *PackCatalogManager) Install(ctx context.Context, catalogID string) (Pack, error) {
	return m.install(ctx, catalogID, "", false, nil)
}

func (m *PackCatalogManager) InstallIfCurrent(ctx context.Context, catalogID, expectedGeneration string) (Pack, error) {
	return m.install(ctx, catalogID, expectedGeneration, true, nil)
}

// InstallIfCurrentWithAutoSync validates the card generation and market source
// before ensuring the local auto-sync worker, then installs under the same
// serialized registry transaction.
func (m *PackCatalogManager) InstallIfCurrentWithAutoSync(
	ctx context.Context,
	catalogID, expectedGeneration string,
	ensureAutoSync func(context.Context) error,
) (Pack, error) {
	return m.install(ctx, catalogID, expectedGeneration, true, ensureAutoSync)
}

func (m *PackCatalogManager) install(
	ctx context.Context,
	catalogID, expectedGeneration string,
	verifyGeneration bool,
	ensureAutoSync func(context.Context) error,
) (Pack, error) {
	if m == nil {
		return Pack{}, fmt.Errorf("agent pack catalog manager is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.catalogByID[strings.TrimSpace(catalogID)]
	if !ok {
		return Pack{}, fmt.Errorf("unknown Agent Pack catalog id %q", catalogID)
	}
	var result Pack
	err := WithPackRegistryLock(m.registryPath, func() error {
		registry, err := LoadPackRegistry(m.registryPath)
		if err != nil {
			return err
		}
		if verifyGeneration {
			if err := verifyCatalogGeneration(entry, registry, expectedGeneration); err != nil {
				return err
			}
		}
		matches := formalPacksByGitURL(registry, entry.GitURL)
		if len(matches) > 1 {
			return fmt.Errorf(
				"agent pack repository %q is registered under multiple namespaces: %s",
				entry.GitURL,
				strings.Join(packNamespaces(matches), ", "),
			)
		}
		if occupant, occupied := registry.Packs[entry.Namespace]; occupied && !SamePackGitURL(occupant.GitURL, entry.GitURL) {
			return fmt.Errorf("agent pack namespace %q is already registered from another source", entry.Namespace)
		}
		if err := ensureCatalogAutoSync(ctx, ensureAutoSync); err != nil {
			return err
		}
		if len(matches) == 1 {
			existing := matches[0]
			if !existing.AutoSync || strings.TrimSpace(existing.SyncInterval) == "" {
				existing.AutoSync = true
				existing.SyncInterval = defaultPackSyncInterval
				registry.Packs[existing.Namespace] = existing
				if err := savePackRegistryUnlocked(m.registryPath, registry); err != nil {
					return err
				}
			}
			result = existing
			return nil
		}
		result, err = addGitPackWithOptionsUnlocked(ctx, m.registryPath, entry.Namespace, entry.GitURL, AddGitPackOptions{
			Ref: entry.Ref, AutoSync: true, SyncInterval: defaultPackSyncInterval,
		})
		return err
	})
	return result, err
}

// EnableAutoSyncForInstalledCatalogPacks migrates registered Packs whose Git
// repository is present in the maintained market. Custom sources are left
// unchanged.
func (m *PackCatalogManager) EnableAutoSyncForInstalledCatalogPacks() error {
	if m == nil {
		return fmt.Errorf("agent pack catalog manager is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return WithPackRegistryLock(m.registryPath, func() error {
		registry, err := LoadPackRegistry(m.registryPath)
		if err != nil {
			return err
		}
		changed := false
		for namespace, pack := range registry.Packs {
			if isDevelopmentPackNamespace(namespace) {
				continue
			}
			if _, present := m.catalogByGitURL[NormalizePackGitURL(pack.GitURL)]; !present {
				continue
			}
			if len(formalPacksByGitURL(registry, pack.GitURL)) != 1 {
				continue
			}
			if pack.AutoSync && strings.TrimSpace(pack.SyncInterval) != "" {
				continue
			}
			pack.AutoSync = true
			pack.SyncInterval = defaultPackSyncInterval
			registry.Packs[namespace] = pack
			changed = true
		}
		if !changed {
			return nil
		}
		return savePackRegistryUnlocked(m.registryPath, registry)
	})
}

func (m *PackCatalogManager) Sync(ctx context.Context, namespace string) (Pack, error) {
	return m.sync(ctx, namespace, "", false, nil)
}

func (m *PackCatalogManager) SyncIfCurrent(ctx context.Context, namespace, expectedGeneration string) (Pack, error) {
	return m.sync(ctx, namespace, expectedGeneration, true, nil)
}

func (m *PackCatalogManager) SyncIfCurrentWithAutoSync(
	ctx context.Context,
	namespace, expectedGeneration string,
	ensureAutoSync func(context.Context) error,
) (Pack, error) {
	return m.sync(ctx, namespace, expectedGeneration, true, ensureAutoSync)
}

func (m *PackCatalogManager) sync(
	ctx context.Context,
	namespace, expectedGeneration string,
	verifyGeneration bool,
	ensureAutoSync func(context.Context) error,
) (Pack, error) {
	if m == nil {
		return Pack{}, fmt.Errorf("agent pack catalog manager is unavailable")
	}
	namespace = strings.TrimSpace(namespace)
	check := func(registry PackRegistry, _ Pack) error {
		existing, installed := registry.Packs[namespace]
		if !installed || isDevelopmentPackNamespace(namespace) {
			return fmt.Errorf("agent pack namespace %q is not a manageable loaded pack", namespace)
		}
		if len(formalPacksByGitURL(registry, existing.GitURL)) != 1 {
			return ErrCatalogStateChanged
		}
		if verifyGeneration && loadedPackGeneration(existing) != strings.TrimSpace(expectedGeneration) {
			return ErrCatalogStateChanged
		}
		return nil
	}
	beforeFetch := func(existing Pack) error {
		if _, inMarket := m.catalogByGitURL[NormalizePackGitURL(existing.GitURL)]; inMarket {
			return ensureCatalogAutoSync(ctx, ensureAutoSync)
		}
		return nil
	}
	return syncPackWithHooks(ctx, m.registryPath, namespace, check, beforeFetch)
}

func ensureCatalogAutoSync(ctx context.Context, ensure func(context.Context) error) error {
	if ensure == nil {
		return nil
	}
	if err := ensure(ctx); err != nil {
		return fmt.Errorf("ensure Agent Pack auto sync: %w", err)
	}
	return nil
}

func (m *PackCatalogManager) Remove(namespace string) (RemovePackResult, error) {
	result, _, err := m.removeIfUnreferenced(namespace, "", false, nil)
	return result, err
}

// WithRegistryStable prevents Pack mutations while a caller validates and
// commits a Profile reference derived from the current registry.
func (m *PackCatalogManager) WithRegistryStable(commit func() error) error {
	return m.WithRegistryStableContext(context.Background(), commit)
}

// WithRegistryStableContext prevents Pack mutations while a caller validates
// and commits a Profile reference, but does not outlive the caller's deadline
// while waiting for either the in-process manager lock or cross-process file
// lock.
func (m *PackCatalogManager) WithRegistryStableContext(ctx context.Context, commit func() error) error {
	if m == nil {
		return fmt.Errorf("agent pack catalog manager is unavailable")
	}
	if commit == nil {
		return fmt.Errorf("registry-stable callback is required")
	}
	if err := m.mu.LockContext(ctx); err != nil {
		return err
	}
	defer m.mu.Unlock()
	return WithPackRegistryLockContext(ctx, m.registryPath, commit)
}

func (m *PackCatalogManager) RemoveIfUnreferenced(
	namespace string,
	references func() ([]string, error),
) (RemovePackResult, []string, error) {
	return m.removeIfUnreferenced(namespace, "", false, references)
}

func (m *PackCatalogManager) RemoveIfCurrentAndUnreferenced(
	namespace, expectedGeneration string,
	references func() ([]string, error),
) (RemovePackResult, []string, error) {
	return m.removeIfUnreferenced(namespace, expectedGeneration, true, references)
}

func (m *PackCatalogManager) removeIfUnreferenced(
	namespace, expectedGeneration string,
	verifyGeneration bool,
	references func() ([]string, error),
) (RemovePackResult, []string, error) {
	if m == nil {
		return RemovePackResult{}, nil, fmt.Errorf("agent pack catalog manager is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	namespace = strings.TrimSpace(namespace)
	var result RemovePackResult
	var usedBy []string
	err := WithPackRegistryLock(m.registryPath, func() error {
		registry, err := LoadPackRegistry(m.registryPath)
		if err != nil {
			return err
		}
		existing, installed := registry.Packs[namespace]
		if !installed || isDevelopmentPackNamespace(namespace) {
			return fmt.Errorf("agent pack namespace %q is not a manageable loaded pack", namespace)
		}
		if len(formalPacksByGitURL(registry, existing.GitURL)) != 1 {
			return ErrCatalogStateChanged
		}
		if verifyGeneration && loadedPackGeneration(existing) != strings.TrimSpace(expectedGeneration) {
			return ErrCatalogStateChanged
		}
		if references != nil {
			usedBy, err = references()
			if err != nil || len(usedBy) > 0 {
				return err
			}
		}
		result, err = removePackUnlocked(m.registryPath, namespace, RemovePackOptions{KeepFiles: true})
		return err
	})
	return result, usedBy, err
}

func verifyCatalogGeneration(entry CatalogPack, registry PackRegistry, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" || catalogPackGeneration(entry, registry) != expected {
		return ErrCatalogStateChanged
	}
	return nil
}

func catalogPackGeneration(entry CatalogPack, registry PackRegistry) string {
	matches := formalPacksByGitURL(registry, entry.GitURL)
	occupant, occupied := registry.Packs[entry.Namespace]
	payload, _ := json.Marshal(struct {
		Catalog  CatalogPack `json:"catalog"`
		Matches  []Pack      `json:"matches,omitempty"`
		Occupant *Pack       `json:"occupant,omitempty"`
	}{
		Catalog: entry,
		Matches: matches,
		Occupant: func() *Pack {
			if !occupied {
				return nil
			}
			return &occupant
		}(),
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func loadedPackGeneration(pack Pack) string {
	pack.LastSyncedAt = ""
	pack.LastError = ""
	payload, _ := json.Marshal(pack)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func formalPacksByGitURL(registry PackRegistry, gitURL string) []Pack {
	var matches []Pack
	for _, pack := range registry.Packs {
		if isDevelopmentPackNamespace(pack.Namespace) || !SamePackGitURL(pack.GitURL, gitURL) {
			continue
		}
		matches = append(matches, pack)
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Namespace < matches[j].Namespace
	})
	return matches
}

func packNamespaces(packs []Pack) []string {
	out := make([]string, 0, len(packs))
	for _, pack := range packs {
		out = append(out, pack.Namespace)
	}
	return out
}
