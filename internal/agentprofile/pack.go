package agentprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const PackSourceGit = "git"

var packNamespacePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var packErrorURLPattern = regexp.MustCompile(`https?://[^\s]+`)
var packErrorSecretParamPattern = regexp.MustCompile(`(?i)\b(access[_-]?token|api[_-]?key|token|password|secret)=([^\s;&]+)`)

var ErrPackRegistryStateChanged = errors.New("Agent Pack registry state changed")

type PackRegistry struct {
	Packs map[string]Pack `json:"packs"`
}

type Pack struct {
	Namespace        string        `json:"namespace"`
	SourceKind       string        `json:"source_kind"`
	ManagedBy        string        `json:"managed_by,omitempty"`
	DisplayName      string        `json:"display_name,omitempty"`
	GitURL           string        `json:"git_url,omitempty"`
	Ref              string        `json:"ref,omitempty"`
	DevelopmentOf    string        `json:"development_of,omitempty"`
	Temporary        bool          `json:"temporary,omitempty"`
	Evaluation       bool          `json:"evaluation,omitempty"`
	MaterializedPath string        `json:"materialized_path"`
	CurrentRevision  string        `json:"current_revision,omitempty"`
	AutoSync         bool          `json:"auto_sync"`
	SyncInterval     string        `json:"sync_interval,omitempty"`
	LastSyncedAt     string        `json:"last_synced_at,omitempty"`
	LastError        string        `json:"last_error,omitempty"`
	Versions         []PackVersion `json:"versions,omitempty"`
}

type PackVersion struct {
	Revision string `json:"revision"`
	Ref      string `json:"ref,omitempty"`
	SyncedAt string `json:"synced_at"`
}

type AddGitPackOptions struct {
	Ref           string
	DevelopmentOf string
	Temporary     bool
	Evaluation    bool
	AutoSync      bool
	SyncInterval  string
	ManagedBy     string
	DisplayName   string
}

type RemovePackOptions struct {
	KeepFiles bool
}

type RemovePackResult struct {
	Pack         Pack
	RemovedPath  string
	FilesRemoved bool
}

func LoadPackRegistry(path string) (PackRegistry, error) {
	var registry PackRegistry
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PackRegistry{Packs: map[string]Pack{}}, nil
		}
		return PackRegistry{}, err
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return PackRegistry{}, err
	}
	autoSyncPresent := packFieldPresence(data, "auto_sync")
	if registry.Packs == nil {
		registry.Packs = map[string]Pack{}
	}
	for namespace, pack := range registry.Packs {
		pack = normalizePackSyncDefaults(pack, autoSyncPresent[namespace])
		next, err := normalizePackMaterializedPath(path, pack)
		if err != nil {
			return PackRegistry{}, err
		}
		registry.Packs[namespace] = next
	}
	return registry, nil
}

func SavePackRegistry(path string, registry PackRegistry) error {
	return WithPackRegistryLock(path, func() error {
		return savePackRegistryUnlocked(path, registry)
	})
}

func savePackRegistryUnlocked(path string, registry PackRegistry) error {
	if registry.Packs == nil {
		registry.Packs = map[string]Pack{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".registry-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// WithPackRegistryLock serializes registry transactions across amux processes.
func WithPackRegistryLock(registryPath string, operation func() error) error {
	return WithPackRegistryLockContext(context.Background(), registryPath, operation)
}

// WithPackRegistryLockContext serializes registry transactions across amux
// processes while allowing callers to stop waiting for a lock held by a slow
// or stale process.
func WithPackRegistryLockContext(ctx context.Context, registryPath string, operation func() error) error {
	registryPath = strings.TrimSpace(registryPath)
	if registryPath == "" {
		return fmt.Errorf("agent pack registry path is required")
	}
	if operation == nil {
		return fmt.Errorf("agent pack registry operation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(filepath.Dir(registryPath), 0700); err != nil {
		return err
	}
	lockFile, err := os.OpenFile(registryPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if err := lockPackFile(ctx, lockFile); err != nil {
		return err
	}
	defer unlockPackFile(lockFile) //nolint:errcheck
	return operation()
}

func ListPacks(registry PackRegistry) []Pack {
	packs := make([]Pack, 0, len(registry.Packs))
	for _, pack := range registry.Packs {
		packs = append(packs, pack)
	}
	sort.Slice(packs, func(i, j int) bool {
		return packs[i].Namespace < packs[j].Namespace
	})
	return packs
}

// NormalizePackGitURL returns the stable repository identity used by amux.
// It is intentionally conservative: different transports remain different
// sources, while inconsequential surrounding whitespace and trailing slashes
// do not create duplicate registrations.
func NormalizePackGitURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// PackRepositoryName derives the default user-facing name and namespace from a
// Git source without exposing its host or organization path.
func PackRepositoryName(gitURL string) string {
	value := NormalizePackGitURL(gitURL)
	if value == "" {
		return ""
	}
	nameSource := value
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		nameSource = parsed.Path
	}
	name := path.Base(strings.ReplaceAll(nameSource, `\`, "/"))
	if colon := strings.LastIndex(name, ":"); colon >= 0 {
		name = name[colon+1:]
	}
	return strings.TrimSuffix(strings.TrimSpace(name), ".git")
}

// SamePackGitURL reports whether two source strings identify the same expert
// library. SSH and HTTPS forms deliberately remain distinct.
func SamePackGitURL(left, right string) bool {
	left = NormalizePackGitURL(left)
	return left != "" && left == NormalizePackGitURL(right)
}

// PackDisplaySource is one visible expert-library source participating in
// user-facing name disambiguation.
type PackDisplaySource struct {
	Key         string
	Namespace   string
	GitURL      string
	DisplayName string
}

// DisambiguatePackDisplayNames derives stable user-facing names for the
// provided visible sources. Different repositories with the same basename are
// disambiguated by namespace.
func DisambiguatePackDisplayNames(sources []PackDisplaySource) map[string]string {
	names := make(map[string]string, len(sources))
	urlsByName := make(map[string]map[string]struct{}, len(sources))
	for _, source := range sources {
		name := strings.TrimSpace(source.DisplayName)
		if name == "" {
			name = PackRepositoryName(source.GitURL)
		}
		if name == "" {
			name = strings.TrimSpace(source.Namespace)
		}
		names[source.Key] = name
		urls := urlsByName[name]
		if urls == nil {
			urls = make(map[string]struct{})
			urlsByName[name] = urls
		}
		urls[NormalizePackGitURL(source.GitURL)] = struct{}{}
	}
	for _, source := range sources {
		name := names[source.Key]
		if len(urlsByName[name]) > 1 {
			names[source.Key] = name + "（" + strings.TrimSpace(source.Namespace) + "）"
		}
	}
	return names
}

func AddGitPackWithOptions(ctx context.Context, registryPath, namespace, gitURL string, opts AddGitPackOptions) (Pack, error) {
	var pack Pack
	err := WithPackRegistryLock(registryPath, func() error {
		var err error
		pack, err = addGitPackWithOptionsUnlocked(ctx, registryPath, namespace, gitURL, opts)
		return err
	})
	return pack, err
}

func addGitPackWithOptionsUnlocked(ctx context.Context, registryPath, namespace, gitURL string, opts AddGitPackOptions) (Pack, error) {
	namespace = strings.TrimSpace(namespace)
	gitURL = NormalizePackGitURL(gitURL)
	ref := strings.TrimSpace(opts.Ref)
	syncInterval := strings.TrimSpace(opts.SyncInterval)
	if syncInterval == "" {
		syncInterval = defaultPackSyncInterval
	}
	if err := validatePackNamespace(namespace); err != nil {
		return Pack{}, err
	}
	if namespace == ManagedLocalPackNamespace && strings.TrimSpace(opts.ManagedBy) != ManagedPackOwnerAmux {
		return Pack{}, fmt.Errorf("agent pack namespace %q is managed by amux; use `amux agent fork` or `amux agent local`", namespace)
	}
	if gitURL == "" {
		return Pack{}, fmt.Errorf("git URL is required")
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		return Pack{}, err
	}
	developmentOf := strings.TrimSpace(opts.DevelopmentOf)
	if isManagedEvaluationNamespace(namespace) && !opts.Evaluation {
		return Pack{}, fmt.Errorf("agent pack namespace %q is reserved for managed local evaluation", namespace)
	}
	if opts.Evaluation && (!opts.Temporary || developmentOf != ManagedLocalPackNamespace ||
		strings.TrimSpace(opts.ManagedBy) != ManagedPackOwnerAmux || !isManagedEvaluationNamespace(namespace)) {
		return Pack{}, fmt.Errorf("managed local evaluation pack identity is invalid")
	}
	if opts.Temporary && developmentOf == "" {
		return Pack{}, fmt.Errorf("temporary agent pack %q must declare a development parent", namespace)
	}
	if developmentOf != "" {
		if !strings.HasSuffix(namespace, "-dev") {
			return Pack{}, fmt.Errorf("development agent pack namespace %q must end with -dev", namespace)
		}
		if opts.Temporary && ref == "" {
			return Pack{}, fmt.Errorf("temporary agent pack %q must bind a Git ref", namespace)
		}
		parent, ok := registry.Packs[developmentOf]
		managedEvaluationWithoutPublishedParent := !ok && opts.Evaluation
		if !ok && !managedEvaluationWithoutPublishedParent {
			return Pack{}, fmt.Errorf("development parent agent pack namespace %q is not registered", developmentOf)
		}
		if ok && parent.DevelopmentOf != "" {
			return Pack{}, fmt.Errorf("development parent agent pack namespace %q is not a formal pack", developmentOf)
		}
		if ok && (parent.SourceKind != PackSourceGit || !SamePackGitURL(parent.GitURL, gitURL)) {
			return Pack{}, fmt.Errorf("development agent pack %q must use the same Git repository as %q", namespace, developmentOf)
		}
	}
	if existing, ok := registry.Packs[namespace]; ok && existing.DevelopmentOf != "" {
		if existing.DevelopmentOf != developmentOf || existing.Temporary != opts.Temporary || existing.Evaluation != opts.Evaluation {
			return Pack{}, fmt.Errorf(
				"agent pack namespace %q cannot change its development identity",
				namespace,
			)
		}
	}
	if existing, ok := registry.Packs[namespace]; ok && !SamePackGitURL(existing.GitURL, gitURL) {
		return Pack{}, fmt.Errorf(
			"agent pack namespace %q is already registered from %s",
			namespace,
			existing.GitURL,
		)
	}
	_, targetAlreadyRegistered := registry.Packs[namespace]
	existingNamespaces := make([]string, 0, len(registry.Packs))
	for existingNamespace := range registry.Packs {
		existingNamespaces = append(existingNamespaces, existingNamespace)
	}
	sort.Strings(existingNamespaces)
	for _, existingNamespace := range existingNamespaces {
		existing := registry.Packs[existingNamespace]
		if existingNamespace == namespace || !SamePackGitURL(existing.GitURL, gitURL) {
			continue
		}
		// Re-registering an existing identity stays idempotent even when an older
		// registry also contains legacy same-repository entries. This does not
		// admit a new suffix-only development identity.
		if targetAlreadyRegistered {
			continue
		}
		if developmentOf != "" &&
			(existingNamespace == developmentOf ||
				existingNamespace == developmentOf+"-dev" ||
				existing.DevelopmentOf == developmentOf) {
			continue
		}
		return Pack{}, fmt.Errorf(
			"agent pack repository %q is already registered as namespace %q",
			gitURL,
			existingNamespace,
		)
	}
	pack := Pack{
		Namespace:        namespace,
		SourceKind:       PackSourceGit,
		ManagedBy:        strings.TrimSpace(opts.ManagedBy),
		DisplayName:      strings.TrimSpace(opts.DisplayName),
		GitURL:           gitURL,
		Ref:              ref,
		DevelopmentOf:    developmentOf,
		Temporary:        opts.Temporary,
		Evaluation:       opts.Evaluation,
		MaterializedPath: packMaterializedPath(registryPath, namespace),
		AutoSync:         opts.AutoSync && !opts.Temporary,
		SyncInterval:     syncInterval,
	}
	if existing, ok := registry.Packs[namespace]; ok {
		pack.Versions = existing.Versions
		if pack.ManagedBy == "" {
			pack.ManagedBy = existing.ManagedBy
		}
		if pack.DisplayName == "" {
			pack.DisplayName = existing.DisplayName
		}
	}
	pack, err = syncGitPack(ctx, pack)
	if err != nil {
		return Pack{}, err
	}
	registry.Packs[namespace] = pack
	if err := savePackRegistryUnlocked(registryPath, registry); err != nil {
		return Pack{}, err
	}
	return pack, nil
}

func SyncPack(ctx context.Context, registryPath, namespace string) (Pack, error) {
	return syncPackWithHooks(ctx, registryPath, namespace, func(_ PackRegistry, pack Pack) error {
		if isManagedLocalPack(pack) {
			return fmt.Errorf("agent pack namespace %q is managed by amux and cannot be synced as a remote pack", namespace)
		}
		return nil
	}, nil)
}

type packSyncCheck func(PackRegistry, Pack) error
type packSyncBeforeFetch func(Pack) error

// syncPackWithHooks keeps remote Git I/O outside the registry transaction.
// Fetch only updates Git object/ref storage; the materialized checkout and
// registry metadata are changed together under the short final transaction.
func syncPackWithHooks(
	ctx context.Context,
	registryPath, namespace string,
	check packSyncCheck,
	beforeFetch packSyncBeforeFetch,
) (Pack, error) {
	namespace = strings.TrimSpace(namespace)
	if err := validatePackNamespace(namespace); err != nil {
		return Pack{}, err
	}
	var result Pack
	err := withPackSyncLockContext(ctx, registryPath, namespace, func() error {
		var base Pack
		if err := WithPackRegistryLockContext(ctx, registryPath, func() error {
			registry, err := LoadPackRegistry(registryPath)
			if err != nil {
				return err
			}
			var ok bool
			base, ok = registry.Packs[namespace]
			if !ok {
				return fmt.Errorf("unknown agent pack namespace %q", namespace)
			}
			if base.SourceKind != PackSourceGit {
				return fmt.Errorf("unsupported agent pack source %q", base.SourceKind)
			}
			if check != nil {
				return check(registry, base)
			}
			return nil
		}); err != nil {
			return err
		}
		if beforeFetch != nil {
			if err := beforeFetch(base); err != nil {
				return err
			}
		}
		if err := prepareGitPackSync(ctx, base); err != nil {
			recordPackSyncFailure(registryPath, base, err)
			return err
		}
		return WithPackRegistryLockContext(ctx, registryPath, func() error {
			registry, err := LoadPackRegistry(registryPath)
			if err != nil {
				return err
			}
			current, ok := registry.Packs[namespace]
			if !ok || !samePackSyncBase(base, current) {
				return ErrPackRegistryStateChanged
			}
			if check != nil {
				if err := check(registry, current); err != nil {
					return err
				}
			}
			next, err := finishGitPackSync(ctx, current)
			if err != nil {
				current.LastError = sanitizePackLastError(err)
				registry.Packs[namespace] = current
				_ = savePackRegistryUnlocked(registryPath, registry)
				return err
			}
			registry.Packs[namespace] = next
			if err := savePackRegistryUnlocked(registryPath, registry); err != nil {
				return err
			}
			result = next
			return nil
		})
	})
	return result, err
}

func withPackSyncLockContext(ctx context.Context, registryPath, namespace string, operation func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(filepath.Dir(registryPath), 0700); err != nil {
		return err
	}
	lockPath := registryPath + "." + namespace + ".sync.lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	if err := lockPackFile(ctx, lockFile); err != nil {
		return err
	}
	defer unlockPackFile(lockFile) //nolint:errcheck
	return operation()
}

func samePackSyncBase(base, current Pack) bool {
	return base.Namespace == current.Namespace &&
		base.SourceKind == current.SourceKind &&
		SamePackGitURL(base.GitURL, current.GitURL) &&
		base.Ref == current.Ref &&
		base.DevelopmentOf == current.DevelopmentOf &&
		base.Temporary == current.Temporary &&
		base.Evaluation == current.Evaluation &&
		filepath.Clean(base.MaterializedPath) == filepath.Clean(current.MaterializedPath) &&
		base.CurrentRevision == current.CurrentRevision
}

func recordPackSyncFailure(registryPath string, base Pack, syncErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = WithPackRegistryLockContext(ctx, registryPath, func() error {
		registry, err := LoadPackRegistry(registryPath)
		if err != nil {
			return err
		}
		current, ok := registry.Packs[base.Namespace]
		if !ok || !samePackSyncBase(base, current) {
			return nil
		}
		current.LastError = sanitizePackLastError(syncErr)
		registry.Packs[base.Namespace] = current
		return savePackRegistryUnlocked(registryPath, registry)
	})
}

func RollbackPack(ctx context.Context, registryPath, namespace, revision string) (Pack, error) {
	var pack Pack
	err := WithPackRegistryLock(registryPath, func() error {
		var err error
		pack, err = rollbackPackUnlocked(ctx, registryPath, namespace, revision)
		return err
	})
	return pack, err
}

func rollbackPackUnlocked(ctx context.Context, registryPath, namespace, revision string) (Pack, error) {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return Pack{}, fmt.Errorf("revision is required")
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		return Pack{}, err
	}
	pack, ok := registry.Packs[strings.TrimSpace(namespace)]
	if !ok {
		return Pack{}, fmt.Errorf("unknown agent pack namespace %q", namespace)
	}
	if isManagedLocalPack(pack) {
		return Pack{}, fmt.Errorf("agent pack namespace %q is managed by amux; use `amux agent local restore`", namespace)
	}
	if !packHasRevision(pack, revision) {
		return Pack{}, fmt.Errorf("revision %q is not recorded for pack %q", revision, namespace)
	}
	if _, err := gitCommand(ctx, pack.MaterializedPath, "checkout", "--detach", revision); err != nil {
		return Pack{}, err
	}
	pack.CurrentRevision = revision
	pack.Versions = appendPackVersion(pack.Versions, revision, pack.Ref)
	registry.Packs[pack.Namespace] = pack
	if err := savePackRegistryUnlocked(registryPath, registry); err != nil {
		return Pack{}, err
	}
	return pack, nil
}

func RemovePack(registryPath, namespace string, opts RemovePackOptions) (RemovePackResult, error) {
	var result RemovePackResult
	err := WithPackRegistryLock(registryPath, func() error {
		var err error
		result, err = removePackUnlocked(registryPath, namespace, opts)
		return err
	})
	return result, err
}

// RemovePackIf waits for the registry lock until ctx ends and asks guard to
// approve the current Pack while holding it. References in other stores can
// still change independently after the guard returns.
func RemovePackIf(ctx context.Context, registryPath, namespace string, opts RemovePackOptions, guard func(Pack) error) (RemovePackResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var result RemovePackResult
	err := WithPackRegistryLockContext(ctx, registryPath, func() error {
		if guard != nil {
			registry, err := LoadPackRegistry(registryPath)
			if err != nil {
				return err
			}
			// An unknown namespace is reported by the removal itself.
			if pack, ok := registry.Packs[strings.TrimSpace(namespace)]; ok {
				if err := guard(pack); err != nil {
					return err
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		result, err = removePackUnlocked(registryPath, namespace, opts)
		return err
	})
	return result, err
}

func removePackUnlocked(registryPath, namespace string, opts RemovePackOptions) (RemovePackResult, error) {
	namespace = strings.TrimSpace(namespace)
	if err := validatePackNamespace(namespace); err != nil {
		return RemovePackResult{}, err
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		return RemovePackResult{}, err
	}
	pack, ok := registry.Packs[namespace]
	if !ok {
		return RemovePackResult{}, fmt.Errorf("unknown agent pack namespace %q", namespace)
	}
	if isManagedLocalPack(pack) {
		return RemovePackResult{}, fmt.Errorf("agent pack namespace %q is managed by amux and cannot be removed as a remote pack", namespace)
	}
	delete(registry.Packs, namespace)
	if err := savePackRegistryUnlocked(registryPath, registry); err != nil {
		return RemovePackResult{}, err
	}
	result := RemovePackResult{Pack: pack}
	if opts.KeepFiles {
		return result, nil
	}
	result.RemovedPath = packRootPath(registryPath, namespace)
	if err := os.RemoveAll(result.RemovedPath); err != nil {
		return result, fmt.Errorf("remove materialized agent pack files %s after unregistering %q: %w", result.RemovedPath, namespace, err)
	}
	result.FilesRemoved = true
	return result, nil
}

func validatePackNamespace(namespace string) error {
	if namespace == "" {
		return fmt.Errorf("agent pack namespace is required")
	}
	if namespace == BuiltinNamespace {
		return fmt.Errorf("agent pack namespace %q is reserved", namespace)
	}
	if !packNamespacePattern.MatchString(namespace) {
		return fmt.Errorf("invalid agent pack namespace %q", namespace)
	}
	return nil
}

const (
	defaultPackSyncInterval = "30s"
	packGitFetchTimeout     = 3 * time.Minute
)

func normalizePackSyncDefaults(pack Pack, autoSyncPresent bool) Pack {
	if !autoSyncPresent {
		pack.AutoSync = true
	}
	if strings.TrimSpace(pack.SyncInterval) == "" {
		pack.SyncInterval = defaultPackSyncInterval
	}
	return pack
}

func packFieldPresence(data []byte, field string) map[string]bool {
	present := map[string]bool{}
	var raw struct {
		Packs map[string]map[string]json.RawMessage `json:"packs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return present
	}
	for namespace, fields := range raw.Packs {
		if _, ok := fields[field]; ok {
			present[namespace] = true
		}
	}
	return present
}

func packMaterializedPath(registryPath, namespace string) string {
	return filepath.Join(packRootPath(registryPath, namespace), "repo")
}

func packRootPath(registryPath, namespace string) string {
	return filepath.Join(filepath.Dir(registryPath), namespace)
}

func legacyPackMaterializedPath(registryPath, namespace string) string {
	return filepath.Join(filepath.Dir(registryPath), namespace, "source")
}

func normalizePackMaterializedPath(registryPath string, pack Pack) (Pack, error) {
	expected := packMaterializedPath(registryPath, pack.Namespace)
	legacy := legacyPackMaterializedPath(registryPath, pack.Namespace)
	current := strings.TrimSpace(pack.MaterializedPath)
	if current == "" {
		pack.MaterializedPath = expected
		return pack, nil
	}
	if filepath.Clean(current) != filepath.Clean(legacy) {
		return pack, nil
	}
	if _, err := os.Stat(expected); err == nil {
		pack.MaterializedPath = expected
		return pack, nil
	} else if err != nil && !os.IsNotExist(err) {
		return Pack{}, err
	}
	if _, err := os.Stat(legacy); err == nil {
		if err := os.Rename(legacy, expected); err != nil {
			return Pack{}, fmt.Errorf("migrate agent pack path %s to %s: %w", legacy, expected, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return Pack{}, err
	}
	pack.MaterializedPath = expected
	return pack, nil
}

func syncGitPack(ctx context.Context, pack Pack) (Pack, error) {
	if err := prepareGitPackSync(ctx, pack); err != nil {
		return Pack{}, err
	}
	return finishGitPackSync(ctx, pack)
}

func prepareGitPackSync(ctx context.Context, pack Pack) error {
	pack = normalizePackSyncDefaults(pack, true)
	if strings.TrimSpace(pack.MaterializedPath) == "" {
		return fmt.Errorf("materialized path is required")
	}
	if err := ensureGitClone(ctx, pack); err != nil {
		return err
	}
	if _, err := gitCommand(ctx, pack.MaterializedPath, "remote", "set-url", "origin", pack.GitURL); err != nil {
		return err
	}
	if _, err := gitFetchWithTimeout(ctx, pack.MaterializedPath, packGitFetchTimeout); err != nil {
		return err
	}
	return nil
}

func finishGitPackSync(ctx context.Context, pack Pack) (Pack, error) {
	pack = normalizePackSyncDefaults(pack, true)
	previousRevision := strings.TrimSpace(pack.CurrentRevision)
	if err := checkoutPackRef(ctx, pack); err != nil {
		return Pack{}, err
	}
	rev, err := gitCommand(ctx, pack.MaterializedPath, "rev-parse", "HEAD")
	if err != nil {
		return Pack{}, err
	}
	pack.CurrentRevision = strings.TrimSpace(rev)
	if err := ValidatePackManifest(pack); err != nil {
		if rollbackErr := rollbackPackCheckout(ctx, pack.MaterializedPath, previousRevision); rollbackErr != nil {
			return Pack{}, fmt.Errorf("%w; rollback to previous revision %s failed: %v", err, shortPackRevision(previousRevision), rollbackErr)
		}
		return Pack{}, err
	}
	pack.LastSyncedAt = time.Now().UTC().Format(time.RFC3339)
	pack.LastError = ""
	pack.Versions = appendPackVersion(pack.Versions, pack.CurrentRevision, pack.Ref)
	return pack, nil
}

func rollbackPackCheckout(ctx context.Context, materializedPath, revision string) error {
	revision = strings.TrimSpace(revision)
	if revision == "" {
		return nil
	}
	_, err := gitCommand(ctx, materializedPath, "checkout", "--detach", revision)
	return err
}

func sanitizePackLastError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	text = strings.ReplaceAll(text, "\r\n", " ; ")
	text = strings.ReplaceAll(text, "\n", " ; ")
	text = strings.ReplaceAll(text, "\r", " ; ")
	text = packErrorURLPattern.ReplaceAllStringFunc(text, sanitizePackErrorURL)
	text = packErrorSecretParamPattern.ReplaceAllString(text, "$1=[REDACTED]")
	text = strings.Join(strings.Fields(text), " ")
	const maxPackLastErrorLength = 512
	if len(text) > maxPackLastErrorLength {
		text = text[:maxPackLastErrorLength-3] + "..."
	}
	return text
}

func sanitizePackErrorURL(raw string) string {
	trimmed := strings.TrimRight(raw, ".,;:)")
	suffix := strings.TrimPrefix(raw, trimmed)
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String() + suffix
}

func shortPackRevision(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) <= 12 {
		return revision
	}
	return revision[:12]
}

func ensureGitClone(ctx context.Context, pack Pack) error {
	if _, err := os.Stat(filepath.Join(pack.MaterializedPath, ".git")); err == nil {
		return nil
	}
	if info, err := os.Stat(pack.MaterializedPath); err == nil && info.IsDir() {
		entries, readErr := os.ReadDir(pack.MaterializedPath)
		if readErr != nil {
			return readErr
		}
		if len(entries) > 0 {
			return fmt.Errorf("materialized path exists and is not a git repository: %s", pack.MaterializedPath)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pack.MaterializedPath), 0700); err != nil {
		return err
	}
	_, err := gitCommand(ctx, "", "clone", pack.GitURL, pack.MaterializedPath)
	return err
}

func checkoutPackRef(ctx context.Context, pack Pack) error {
	ref := strings.TrimSpace(pack.Ref)
	if ref == "" {
		_, _ = gitCommand(ctx, pack.MaterializedPath, "remote", "set-head", "origin", "--auto")
		_, err := gitCommand(ctx, pack.MaterializedPath, "checkout", "--detach", "origin/HEAD")
		return err
	}
	if _, err := gitCommand(ctx, pack.MaterializedPath, "checkout", "--detach", "origin/"+ref); err == nil {
		return nil
	}
	_, err := gitCommand(ctx, pack.MaterializedPath, "checkout", "--detach", ref)
	return err
}

func appendPackVersion(versions []PackVersion, revision, ref string) []PackVersion {
	if revision == "" {
		return versions
	}
	if len(versions) > 0 && versions[len(versions)-1].Revision == revision {
		return versions
	}
	return append(versions, PackVersion{
		Revision: revision,
		Ref:      ref,
		SyncedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

func packHasRevision(pack Pack, revision string) bool {
	for _, version := range pack.Versions {
		if version.Revision == revision {
			return true
		}
	}
	return false
}

func gitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func gitFetchWithTimeout(ctx context.Context, dir string, timeout time.Duration) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := gitCommand(fetchCtx, dir, "fetch", "--tags", "origin")
	if err != nil && fetchCtx.Err() != nil {
		return "", fmt.Errorf("git fetch --tags origin: %w", fetchCtx.Err())
	}
	return out, err
}
