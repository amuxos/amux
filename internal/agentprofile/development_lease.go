package agentprofile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DefaultDevelopmentLeaseTTL = 24 * time.Hour

type DevelopmentLeaseRegistry struct {
	Leases map[string]DevelopmentLease `json:"leases"`
}

type DevelopmentLease struct {
	Namespace            string    `json:"namespace"`
	DevelopmentOf        string    `json:"development_of"`
	GitURL               string    `json:"git_url"`
	RepositoryPath       string    `json:"repository_path"`
	WorktreePath         string    `json:"worktree_path"`
	Branch               string    `json:"branch"`
	OwnerSessionID       string    `json:"owner_session_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	ExpiresAt            time.Time `json:"expires_at"`
	LastCleanupAttemptAt time.Time `json:"last_cleanup_attempt_at,omitempty"`
	LastError            string    `json:"last_error,omitempty"`
}

type RegisterDevelopmentLeaseOptions struct {
	Namespace      string
	DevelopmentOf  string
	RepositoryPath string
	WorktreePath   string
	Branch         string
	OwnerSessionID string
	TTL            time.Duration
	Now            time.Time
}

type DevelopmentLeaseStatus string

const (
	DevelopmentLeaseKept     DevelopmentLeaseStatus = "kept"
	DevelopmentLeaseCleaned  DevelopmentLeaseStatus = "cleaned"
	DevelopmentLeaseRetained DevelopmentLeaseStatus = "retained"
)

type DevelopmentLeaseCleanupResult struct {
	Lease  DevelopmentLease
	Status DevelopmentLeaseStatus
	Reason string
}

type PackDevelopmentContext struct {
	FormalNamespace string
	GitURL          string
	Ref             string
}

func ResolvePackDevelopmentContext(packRegistryPath, profileRef string) (PackDevelopmentContext, error) {
	profileRef = strings.TrimSpace(profileRef)
	separator := strings.IndexByte(profileRef, '/')
	if separator <= 0 || separator == len(profileRef)-1 {
		return PackDevelopmentContext{}, fmt.Errorf("invalid profile ref %q; expected <namespace/profile>", profileRef)
	}
	selectedNamespace := profileRef[:separator]
	if selectedNamespace == BuiltinNamespace {
		return PackDevelopmentContext{}, fmt.Errorf("builtin profile %q has no external development repository", profileRef)
	}
	registry, err := LoadPackRegistry(packRegistryPath)
	if err != nil {
		return PackDevelopmentContext{}, err
	}
	selected, ok := registry.Packs[selectedNamespace]
	if !ok {
		return PackDevelopmentContext{}, fmt.Errorf("agent pack namespace %q is not registered", selectedNamespace)
	}
	if selected.SourceKind != PackSourceGit {
		return PackDevelopmentContext{}, fmt.Errorf("profile %q does not belong to a Git-backed agent pack", profileRef)
	}

	formalNamespace := strings.TrimSpace(selected.DevelopmentOf)
	if formalNamespace == "" {
		formalNamespace = selectedNamespace
		if strings.HasSuffix(selectedNamespace, "-dev") {
			legacyFormalNamespace := strings.TrimSuffix(selectedNamespace, "-dev")
			if legacyFormal, found := registry.Packs[legacyFormalNamespace]; found && strings.TrimSpace(legacyFormal.GitURL) != "" {
				formalNamespace = legacyFormalNamespace
			}
		}
	}
	formal, ok := registry.Packs[formalNamespace]
	if !ok || formal.SourceKind != PackSourceGit || formal.DevelopmentOf != "" {
		return PackDevelopmentContext{}, fmt.Errorf("formal agent pack namespace %q is not a registered formal Git pack", formalNamespace)
	}
	gitURL := strings.TrimSpace(formal.GitURL)
	if gitURL == "" {
		return PackDevelopmentContext{}, fmt.Errorf("formal agent pack namespace %q has no Git URL", formalNamespace)
	}
	return PackDevelopmentContext{
		FormalNamespace: formalNamespace,
		GitURL:          gitURL,
		Ref:             strings.TrimSpace(formal.Ref),
	}, nil
}

func DevelopmentLeaseRegistryPath(packRegistryPath string) string {
	return filepath.Join(filepath.Dir(packRegistryPath), "development-leases.json")
}

func LoadDevelopmentLeaseRegistry(packRegistryPath string) (DevelopmentLeaseRegistry, error) {
	path := DevelopmentLeaseRegistryPath(packRegistryPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DevelopmentLeaseRegistry{Leases: map[string]DevelopmentLease{}}, nil
		}
		return DevelopmentLeaseRegistry{}, err
	}
	var registry DevelopmentLeaseRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return DevelopmentLeaseRegistry{}, err
	}
	if registry.Leases == nil {
		registry.Leases = map[string]DevelopmentLease{}
	}
	return registry, nil
}

func RegisterDevelopmentLease(packRegistryPath string, opts RegisterDevelopmentLeaseOptions) (DevelopmentLease, error) {
	var lease DevelopmentLease
	err := WithPackRegistryLock(packRegistryPath, func() error {
		namespace := strings.TrimSpace(opts.Namespace)
		developmentOf := strings.TrimSpace(opts.DevelopmentOf)
		branch := strings.TrimSpace(opts.Branch)
		if err := validatePackNamespace(namespace); err != nil {
			return err
		}
		if !strings.HasSuffix(namespace, "-dev") {
			return fmt.Errorf("development lease namespace %q must end with -dev", namespace)
		}
		if developmentOf == "" {
			return fmt.Errorf("development lease parent namespace is required")
		}
		if err := validatePackNamespace(developmentOf); err != nil {
			return fmt.Errorf("invalid development lease parent: %w", err)
		}
		if branch == "" {
			return fmt.Errorf("development lease branch is required")
		}
		repositoryPath, err := canonicalExistingPath(opts.RepositoryPath)
		if err != nil {
			return fmt.Errorf("resolve development lease repository: %w", err)
		}
		worktreePath, err := canonicalExistingPath(opts.WorktreePath)
		if err != nil {
			return fmt.Errorf("resolve development lease worktree: %w", err)
		}
		packs, err := LoadPackRegistry(packRegistryPath)
		if err != nil {
			return err
		}
		parent, ok := packs.Packs[developmentOf]
		if !ok || parent.SourceKind != PackSourceGit || parent.DevelopmentOf != "" {
			return fmt.Errorf("development lease parent %q is not a registered formal Git pack", developmentOf)
		}
		if err := validateDevelopmentRepositoryOrigin(repositoryPath, parent.GitURL); err != nil {
			return err
		}
		if err := validateDevelopmentWorktree(repositoryPath, worktreePath, branch, false); err != nil {
			return err
		}
		if pack, ok := packs.Packs[namespace]; ok {
			if err := validateDevelopmentLeasePack(pack, DevelopmentLease{
				Namespace: namespace, DevelopmentOf: developmentOf, GitURL: parent.GitURL, Branch: branch,
			}); err != nil {
				return err
			}
		}
		now := opts.Now
		if now.IsZero() {
			now = time.Now()
		}
		ttl := opts.TTL
		if ttl <= 0 {
			ttl = DefaultDevelopmentLeaseTTL
		}
		leases, err := LoadDevelopmentLeaseRegistry(packRegistryPath)
		if err != nil {
			return err
		}
		lease = DevelopmentLease{
			Namespace:      namespace,
			DevelopmentOf:  developmentOf,
			GitURL:         NormalizePackGitURL(parent.GitURL),
			RepositoryPath: repositoryPath,
			WorktreePath:   worktreePath,
			Branch:         branch,
			OwnerSessionID: strings.TrimSpace(opts.OwnerSessionID),
			CreatedAt:      now,
			UpdatedAt:      now,
			ExpiresAt:      now.Add(ttl),
		}
		if existing, ok := leases.Leases[namespace]; ok {
			if !sameDevelopmentLeaseIdentity(existing, lease) {
				return fmt.Errorf("development lease %q is already registered with a different identity", namespace)
			}
			if existing.OwnerSessionID != "" && lease.OwnerSessionID != "" &&
				existing.OwnerSessionID != lease.OwnerSessionID {
				return fmt.Errorf("development lease %q is owned by another session", namespace)
			}
			lease.CreatedAt = existing.CreatedAt
			if lease.OwnerSessionID == "" {
				lease.OwnerSessionID = existing.OwnerSessionID
			}
		}
		leases.Leases[namespace] = lease
		return saveDevelopmentLeaseRegistryUnlocked(packRegistryPath, leases)
	})
	return lease, err
}

func CleanupDevelopmentLease(packRegistryPath, namespace string) (DevelopmentLeaseCleanupResult, error) {
	var result DevelopmentLeaseCleanupResult
	err := WithPackRegistryLock(packRegistryPath, func() error {
		leases, err := LoadDevelopmentLeaseRegistry(packRegistryPath)
		if err != nil {
			return err
		}
		lease, ok := leases.Leases[strings.TrimSpace(namespace)]
		if !ok {
			return fmt.Errorf("unknown development lease namespace %q", namespace)
		}
		result, err = cleanupDevelopmentLeaseUnlocked(packRegistryPath, leases, lease, time.Now())
		return err
	})
	return result, err
}

func ReconcileDevelopmentLeases(
	packRegistryPath string,
	now time.Time,
	liveOwnerSessionIDs map[string]struct{},
) ([]DevelopmentLeaseCleanupResult, error) {
	var results []DevelopmentLeaseCleanupResult
	err := WithPackRegistryLock(packRegistryPath, func() error {
		leases, err := LoadDevelopmentLeaseRegistry(packRegistryPath)
		if err != nil {
			return err
		}
		namespaces := make([]string, 0, len(leases.Leases))
		for namespace := range leases.Leases {
			namespaces = append(namespaces, namespace)
		}
		sort.Strings(namespaces)
		var cleanupErrors []error
		for _, namespace := range namespaces {
			lease, ok := leases.Leases[namespace]
			if !ok {
				continue
			}
			if lease.OwnerSessionID != "" {
				if _, live := liveOwnerSessionIDs[lease.OwnerSessionID]; live {
					results = append(results, DevelopmentLeaseCleanupResult{
						Lease: lease, Status: DevelopmentLeaseKept, Reason: "owner session is live",
					})
					continue
				}
			} else if now.Before(lease.ExpiresAt) {
				results = append(results, DevelopmentLeaseCleanupResult{
					Lease: lease, Status: DevelopmentLeaseKept, Reason: "ownerless lease has not expired",
				})
				continue
			}
			result, cleanupErr := cleanupDevelopmentLeaseUnlocked(packRegistryPath, leases, lease, now)
			results = append(results, result)
			if cleanupErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("%s: %w", namespace, cleanupErr))
			}
		}
		return errors.Join(cleanupErrors...)
	})
	return results, err
}

func cleanupDevelopmentLeaseUnlocked(
	packRegistryPath string,
	leases DevelopmentLeaseRegistry,
	lease DevelopmentLease,
	now time.Time,
) (DevelopmentLeaseCleanupResult, error) {
	retain := func(reason string) (DevelopmentLeaseCleanupResult, error) {
		lease.LastCleanupAttemptAt = now
		lease.LastError = reason
		lease.UpdatedAt = now
		leases.Leases[lease.Namespace] = lease
		if err := saveDevelopmentLeaseRegistryUnlocked(packRegistryPath, leases); err != nil {
			return DevelopmentLeaseCleanupResult{}, err
		}
		return DevelopmentLeaseCleanupResult{
			Lease: lease, Status: DevelopmentLeaseRetained, Reason: reason,
		}, nil
	}

	packs, err := LoadPackRegistry(packRegistryPath)
	if err != nil {
		return DevelopmentLeaseCleanupResult{}, err
	}
	if pack, ok := packs.Packs[lease.Namespace]; ok {
		if err := validateDevelopmentLeasePack(pack, lease); err != nil {
			return retain(err.Error())
		}
	}

	if _, err := os.Stat(lease.WorktreePath); err == nil {
		if err := validateDevelopmentRepositoryOrigin(lease.RepositoryPath, lease.GitURL); err != nil {
			return retain(err.Error())
		}
		if err := validateDevelopmentWorktree(
			lease.RepositoryPath,
			lease.WorktreePath,
			lease.Branch,
			true,
		); err != nil {
			return retain(err.Error())
		}
		if out, err := runDevelopmentGit(lease.RepositoryPath, "worktree", "remove", lease.WorktreePath); err != nil {
			return retain(fmt.Sprintf("remove development worktree: %v: %s", err, out))
		}
		if out, err := runDevelopmentGit(lease.RepositoryPath, "worktree", "prune"); err != nil {
			return retain(fmt.Sprintf("prune development worktrees: %v: %s", err, out))
		}
	} else if !os.IsNotExist(err) {
		return retain(fmt.Sprintf("inspect development worktree: %v", err))
	}

	if _, ok := packs.Packs[lease.Namespace]; ok {
		if _, err := removePackUnlocked(packRegistryPath, lease.Namespace, RemovePackOptions{}); err != nil {
			return retain(fmt.Sprintf("remove temporary development pack: %v", err))
		}
	}
	delete(leases.Leases, lease.Namespace)
	if err := saveDevelopmentLeaseRegistryUnlocked(packRegistryPath, leases); err != nil {
		return DevelopmentLeaseCleanupResult{}, err
	}
	lease.LastCleanupAttemptAt = now
	lease.LastError = ""
	return DevelopmentLeaseCleanupResult{Lease: lease, Status: DevelopmentLeaseCleaned}, nil
}

func validateDevelopmentLeasePack(pack Pack, lease DevelopmentLease) error {
	if pack.Namespace != lease.Namespace ||
		!pack.Temporary ||
		pack.DevelopmentOf != lease.DevelopmentOf ||
		pack.Ref != lease.Branch ||
		pack.SourceKind != PackSourceGit ||
		!SamePackGitURL(pack.GitURL, lease.GitURL) {
		return fmt.Errorf("temporary agent pack %q identity does not match its development lease", lease.Namespace)
	}
	return nil
}

func validateDevelopmentRepositoryOrigin(repositoryPath, expectedGitURL string) error {
	origin, err := runDevelopmentGit(repositoryPath, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("inspect development repository origin: %w: %s", err, origin)
	}
	if !SamePackGitURL(origin, expectedGitURL) {
		return fmt.Errorf(
			"development repository origin %q does not match parent agent pack source %q",
			origin,
			NormalizePackGitURL(expectedGitURL),
		)
	}
	return nil
}

func validateDevelopmentWorktree(repositoryPath, worktreePath, branch string, requireClean bool) error {
	repositoryPath, err := canonicalExistingPath(repositoryPath)
	if err != nil {
		return fmt.Errorf("resolve development repository: %w", err)
	}
	worktreePath, err = canonicalExistingPath(worktreePath)
	if err != nil {
		return fmt.Errorf("resolve development worktree: %w", err)
	}
	currentBranch, err := runDevelopmentGit(worktreePath, "branch", "--show-current")
	if err != nil {
		return fmt.Errorf("inspect development worktree branch: %w: %s", err, currentBranch)
	}
	if strings.TrimSpace(currentBranch) != branch {
		return fmt.Errorf(
			"development worktree branch %q does not match lease branch %q",
			strings.TrimSpace(currentBranch),
			branch,
		)
	}
	found, err := developmentWorktreeRegistered(repositoryPath, worktreePath, branch)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("development worktree %q is not registered with repository %q", worktreePath, repositoryPath)
	}
	if requireClean {
		status, err := runDevelopmentGit(worktreePath, "status", "--porcelain")
		if err != nil {
			return fmt.Errorf("inspect development worktree status: %w: %s", err, status)
		}
		if strings.TrimSpace(status) != "" {
			return fmt.Errorf("development worktree has uncommitted changes")
		}
	}
	return nil
}

func developmentWorktreeRegistered(repositoryPath, worktreePath, branch string) (bool, error) {
	out, err := runDevelopmentGit(repositoryPath, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("list development worktrees: %w: %s", err, out)
	}
	var pathValue, branchValue string
	check := func() bool {
		if pathValue == "" {
			return false
		}
		canonicalPath, err := canonicalExistingPath(pathValue)
		return err == nil && canonicalPath == worktreePath && branchValue == "refs/heads/"+branch
	}
	for _, line := range append(strings.Split(out, "\n"), "") {
		if line == "" {
			if check() {
				return true, nil
			}
			pathValue, branchValue = "", ""
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			pathValue = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			branchValue = strings.TrimPrefix(line, "branch ")
		}
	}
	return false, nil
}

func sameDevelopmentLeaseIdentity(left, right DevelopmentLease) bool {
	return left.Namespace == right.Namespace &&
		left.DevelopmentOf == right.DevelopmentOf &&
		SamePackGitURL(left.GitURL, right.GitURL) &&
		left.RepositoryPath == right.RepositoryPath &&
		left.WorktreePath == right.WorktreePath &&
		left.Branch == right.Branch
}

func canonicalExistingPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func runDevelopmentGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func saveDevelopmentLeaseRegistryUnlocked(
	packRegistryPath string,
	registry DevelopmentLeaseRegistry,
) error {
	if registry.Leases == nil {
		registry.Leases = map[string]DevelopmentLease{}
	}
	path := DevelopmentLeaseRegistryPath(packRegistryPath)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".development-leases-*.tmp")
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
