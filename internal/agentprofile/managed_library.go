package agentprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/pelletier/go-toml/v2"
)

const (
	ManagedLocalPackNamespace = "local"
	ManagedPackOwnerAmux      = "amux"
	managedLocalPackBranch    = "main"
	managedLocalPackName      = "Local experts"
)

var managedProfileNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var managedRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// ManagedContractRunner is the seam between amux-managed authoring and the
// platform-neutral Agent Pack Contract implementation.
type ManagedContractRunner interface {
	Check(ctx context.Context, repoRoot string) error
}

// ExecManagedContractRunner invokes an agent-pack-contract executable already
// prepared by the amux installer. Installation remains outside this authoring
// adapter so user mutations never install dependencies on demand.
type ExecManagedContractRunner struct {
	Command string
}

// initializeManagedPack writes the v2 source contract directly. Authoring never
// depends on an external tool's default init template.
func initializeManagedPack(target string) error {
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	data := `schema_version = "v2"
name = "local"
namespace = "local"
description = "local"
owners = []
`
	return os.WriteFile(filepath.Join(target, "agent-pack.toml"), []byte(data), 0600)
}

func (r ExecManagedContractRunner) Check(ctx context.Context, repoRoot string) error {
	return r.run(ctx, "check", "--repo-root", repoRoot)
}

func (r ExecManagedContractRunner) run(ctx context.Context, args ...string) error {
	command := strings.TrimSpace(r.Command)
	if command == "" {
		command = strings.TrimSpace(os.Getenv("AMUX_AGENT_PACK_CONTRACT_BIN"))
	}
	if command == "" {
		command = "agent-pack-contract"
	}
	cmd := exec.CommandContext(ctx, command, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var execErr *exec.Error
	if (errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound)) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("managed agent-pack-contract runtime is missing; run `amux update` or set AMUX_AGENT_PACK_CONTRACT_BIN: %w", err)
	}
	message := strings.TrimSpace(string(out))
	if message == "" {
		return fmt.Errorf("agent-pack-contract %s: %w", strings.Join(args, " "), err)
	}
	return fmt.Errorf("agent-pack-contract %s: %w\n%s", strings.Join(args, " "), err, message)
}

type ManagedProfileSpec struct {
	Description  string
	Name         string
	Instructions string
}

type ManagedLibraryMutation struct {
	ProfileRef string
	Revision   string
}

type ManagedLibrarySnapshot struct {
	Initialized      bool
	Namespace        string
	Revision         string
	OriginPath       string
	MaterializedPath string
	Profiles         []string
}

type ManagedExpert struct {
	Name         string
	Revision     string
	Instructions string
}

type ManagedLibraryVersion struct {
	Revision string
	Time     time.Time
	Subject  string
}

// ManagedLibrary hides Git, worktree, Contract, publication, and registry
// synchronization behind a small authoring interface.
type ManagedLibrary struct {
	root                  string
	registryPath          string
	contract              ManagedContractRunner
	requireAuthoringReady func() error
}

func DefaultManagedLibraryRoot() string {
	return filepath.Join(amuxhome.Dir(), "managed-agent-packs", ManagedLocalPackNamespace)
}

func NewDefaultManagedLibrary() *ManagedLibrary {
	library := NewManagedLibrary(
		DefaultManagedLibraryRoot(),
		DefaultPackRegistryPath(),
		ExecManagedContractRunner{Command: managedContractExecutablePath()},
	)
	library.requireAuthoringReady = RequireDefaultManagedAuthoringReady
	return library
}

// PrepareDefaultManagedLibrary is the installer-owned repair path. It bypasses
// the persisted authoring gate so a later update can recover a degraded state.
func PrepareDefaultManagedLibrary(ctx context.Context) error {
	return NewManagedLibrary(
		DefaultManagedLibraryRoot(),
		DefaultPackRegistryPath(),
		ExecManagedContractRunner{Command: managedContractExecutablePath()},
	).Ensure(ctx)
}

func managedContractExecutablePath() string {
	if command := strings.TrimSpace(os.Getenv("AMUX_AGENT_PACK_CONTRACT_BIN")); command != "" {
		return command
	}
	return DefaultManagedContractExecutablePath()
}

func NewManagedLibrary(root, registryPath string, contract ManagedContractRunner) *ManagedLibrary {
	return &ManagedLibrary{
		root:         cleanManagedPath(root),
		registryPath: cleanManagedPath(registryPath),
		contract:     contract,
	}
}

// Ensure prepares the installation-owned Git repository without publishing an
// empty runtime Pack. The first successful create or fork performs admission.
func (m *ManagedLibrary) Ensure(ctx context.Context) error {
	if err := m.authoringReady(); err != nil {
		return err
	}
	return m.withLock(func() error {
		if err := m.validateIdentity(); err != nil {
			return err
		}
		if m.originExists() {
			return nil
		}
		if m.contract == nil {
			return fmt.Errorf("managed Agent Pack contract runner is unavailable")
		}
		if entries, err := os.ReadDir(m.root); err == nil && len(entries) != 0 {
			return fmt.Errorf("managed local expert root %s is not empty but has no usable origin; move it aside or repair it before retrying", m.root)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(m.root)
		if err := os.MkdirAll(parent, 0700); err != nil {
			return err
		}
		operationRoot, err := os.MkdirTemp(parent, ".local-prepare-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(operationRoot)
		repoRoot := filepath.Join(operationRoot, ManagedLocalPackNamespace)
		if err := initializeManagedPack(repoRoot); err != nil {
			return err
		}
		if _, err := gitCommand(ctx, repoRoot, "init"); err != nil {
			return err
		}
		if err := configureManagedGitIdentity(ctx, repoRoot); err != nil {
			return err
		}
		if _, err := gitCommand(ctx, repoRoot, "add", "--all"); err != nil {
			return err
		}
		if _, err := gitCommand(ctx, repoRoot, "commit", "--allow-empty", "-m", "amux: initialize local expert library"); err != nil {
			return err
		}
		if _, err := gitCommand(ctx, repoRoot, "branch", "-M", managedLocalPackBranch); err != nil {
			return err
		}
		if err := os.MkdirAll(m.root, 0700); err != nil {
			return err
		}
		if _, err := gitCommand(ctx, "", "clone", "--bare", repoRoot, m.originPath()); err != nil {
			_ = os.RemoveAll(m.originPath())
			return err
		}
		return nil
	})
}

func (m *ManagedLibrary) Create(ctx context.Context, spec ManagedProfileSpec) (ManagedLibraryMutation, error) {
	if err := validateManagedProfileSpec(&spec); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if err := m.authoringReady(); err != nil {
		return ManagedLibraryMutation{}, err
	}
	var result ManagedLibraryMutation
	err := m.withLock(func() error {
		if err := m.validateIdentity(); err != nil {
			return err
		}
		if !m.originExists() {
			created, err := m.initialize(ctx, spec)
			result = created
			return err
		}
		created, err := m.mutateExisting(ctx, "", spec.Name, func(worktree string) error {
			profilePath := managedProfilePath(worktree, spec.Name)
			if _, statErr := os.Stat(profilePath); statErr == nil {
				return fmt.Errorf("local expert %q already exists", spec.Name)
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
			return writeManagedProfile(worktree, spec)
		}, "amux: create local expert "+spec.Name)
		result = created
		return err
	})
	return result, err
}

// ForkBuiltin copies one embedded profile and its required authoring assets
// into the independently versioned local library.
func (m *ManagedLibrary) ForkBuiltin(ctx context.Context, sourceRef, targetName string) (ManagedLibraryMutation, error) {
	profile, ok := ResolveBuiltin(strings.TrimSpace(sourceRef))
	if !ok || !strings.HasPrefix(strings.TrimSpace(sourceRef), BuiltinNamespace+"/") {
		return ManagedLibraryMutation{}, fmt.Errorf("fork source must be an existing builtin profile")
	}
	targetName = strings.TrimSpace(targetName)
	if err := validateManagedProfileName(targetName); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if err := m.Ensure(ctx); err != nil {
		return ManagedLibraryMutation{}, err
	}
	var result ManagedLibraryMutation
	err := m.withLock(func() error {
		if err := m.validateIdentity(); err != nil {
			return err
		}
		created, err := m.mutateExisting(ctx, "", targetName, func(worktree string) error {
			if _, statErr := os.Stat(managedProfilePath(worktree, targetName)); statErr == nil {
				return fmt.Errorf("local expert %q already exists", targetName)
			} else if !os.IsNotExist(statErr) {
				return statErr
			}
			return writeBuiltinFork(worktree, profile, targetName)
		}, "amux: fork "+profile.Ref()+" to local/"+targetName)
		result = created
		return err
	})
	return result, err
}

func (m *ManagedLibrary) History(ctx context.Context, limit int) ([]ManagedLibraryVersion, error) {
	if limit <= 0 {
		limit = 20
	}
	var versions []ManagedLibraryVersion
	err := m.withLock(func() error {
		if !m.originExists() {
			return nil
		}
		output, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "log", "-n", fmt.Sprint(limit), "--format=%H%x1f%ct%x1f%s", "refs/heads/"+managedLocalPackBranch)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			parts := strings.SplitN(line, "\x1f", 3)
			if len(parts) != 3 {
				return fmt.Errorf("parse local expert history entry %q", line)
			}
			seconds, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return fmt.Errorf("parse local expert history time: %w", err)
			}
			versions = append(versions, ManagedLibraryVersion{Revision: parts[0], Time: time.Unix(seconds, 0).UTC(), Subject: parts[2]})
		}
		return nil
	})
	return versions, err
}

// Restore publishes the selected historical tree as a new commit, preserving
// a linear audit trail and using the same Contract/CAS/LKG transaction.
func (m *ManagedLibrary) Restore(ctx context.Context, revision, expectedRevision string) (ManagedLibraryMutation, error) {
	revision = strings.TrimSpace(revision)
	expectedRevision = strings.TrimSpace(expectedRevision)
	if revision == "" || expectedRevision == "" {
		return ManagedLibraryMutation{}, fmt.Errorf("revision and expected revision are required")
	}
	if err := m.authoringReady(); err != nil {
		return ManagedLibraryMutation{}, err
	}
	var result ManagedLibraryMutation
	err := m.withLock(func() error {
		if err := m.validateIdentity(); err != nil {
			return err
		}
		if !m.originExists() {
			return fmt.Errorf("local expert library is not initialized")
		}
		if _, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "merge-base", "--is-ancestor", revision, "refs/heads/"+managedLocalPackBranch); err != nil {
			return fmt.Errorf("revision %q is not in local expert history", revision)
		}
		restored, err := m.mutateExisting(ctx, expectedRevision, "", func(worktree string) error {
			_, err := gitCommand(ctx, worktree, "read-tree", "--reset", "-u", revision)
			return err
		}, "amux: restore local expert library to "+shortPackRevision(revision))
		result = restored
		return err
	})
	return result, err
}

func (m *ManagedLibrary) UpdateInstructions(ctx context.Context, name, instructions, expectedRevision string) (ManagedLibraryMutation, error) {
	name = strings.TrimSpace(name)
	if !managedProfileNamePattern.MatchString(name) {
		return ManagedLibraryMutation{}, fmt.Errorf("local expert name %q must be lowercase kebab-case", name)
	}
	if strings.TrimSpace(instructions) == "" {
		return ManagedLibraryMutation{}, fmt.Errorf("instructions must not be empty")
	}
	expectedRevision = strings.TrimSpace(expectedRevision)
	if expectedRevision == "" {
		return ManagedLibraryMutation{}, fmt.Errorf("expected revision is required; run `amux agent local status`")
	}
	if err := m.authoringReady(); err != nil {
		return ManagedLibraryMutation{}, err
	}
	var result ManagedLibraryMutation
	err := m.withLock(func() error {
		if err := m.validateIdentity(); err != nil {
			return err
		}
		if !m.originExists() {
			return fmt.Errorf("local expert library is not initialized; run `amux agent local create <name> ...`")
		}
		updated, err := m.mutateExisting(ctx, expectedRevision, name, func(worktree string) error {
			if _, statErr := os.Stat(managedProfilePath(worktree, name)); statErr != nil {
				if os.IsNotExist(statErr) {
					return fmt.Errorf("unknown local expert %q", name)
				}
				return statErr
			}
			return writeManagedInstructions(worktree, name, instructions)
		}, "amux: update local expert "+name)
		result = updated
		return err
	})
	return result, err
}

func (m *ManagedLibrary) authoringReady() error {
	if m.requireAuthoringReady == nil {
		return nil
	}
	return m.requireAuthoringReady()
}

func (m *ManagedLibrary) Snapshot(ctx context.Context) (ManagedLibrarySnapshot, error) {
	var snapshot ManagedLibrarySnapshot
	err := m.withLock(func() error {
		if err := m.validateIdentity(); err != nil {
			return err
		}
		snapshot = ManagedLibrarySnapshot{
			Initialized: m.originExists(),
			Namespace:   ManagedLocalPackNamespace,
			OriginPath:  m.originPath(),
		}
		if !snapshot.Initialized {
			return nil
		}
		revision, err := m.currentRevision(ctx)
		if err != nil {
			return err
		}
		snapshot.Revision = revision
		manifestText, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "show", managedLocalPackBranch+":"+packManifestRelPath)
		if err != nil {
			registry, loadErr := LoadPackRegistry(m.registryPath)
			if loadErr != nil {
				return loadErr
			}
			if _, registered := registry.Packs[ManagedLocalPackNamespace]; !registered {
				return nil
			}
			return err
		}
		var manifest packManifest
		if err := json.Unmarshal([]byte(manifestText), &manifest); err != nil {
			return fmt.Errorf("parse managed local expert manifest: %w", err)
		}
		for name := range manifest.Profiles {
			snapshot.Profiles = append(snapshot.Profiles, name)
		}
		sort.Strings(snapshot.Profiles)
		registry, err := LoadPackRegistry(m.registryPath)
		if err != nil {
			return err
		}
		if pack, ok := registry.Packs[ManagedLocalPackNamespace]; ok && strings.TrimSpace(pack.MaterializedPath) != "" {
			snapshot.MaterializedPath = pack.MaterializedPath
		}
		return nil
	})
	return snapshot, err
}

func (m *ManagedLibrary) Expert(ctx context.Context, name string) (ManagedExpert, error) {
	name = strings.TrimSpace(name)
	if !managedProfileNamePattern.MatchString(name) {
		return ManagedExpert{}, fmt.Errorf("local expert name %q must be lowercase kebab-case", name)
	}
	var expert ManagedExpert
	err := m.withLock(func() error {
		if err := m.validateIdentity(); err != nil {
			return err
		}
		if !m.originExists() {
			return fmt.Errorf("local expert library is not initialized")
		}
		revision, err := m.currentRevision(ctx)
		if err != nil {
			return err
		}
		instructions, err := gitCommand(
			ctx,
			"",
			"--git-dir", m.originPath(),
			"show", revision+":"+filepath.ToSlash(filepath.Join("instructions", "common", name, "AGENTS.md")),
		)
		if err != nil {
			return fmt.Errorf("read local expert %q: %w", name, err)
		}
		expert = ManagedExpert{
			Name:         name,
			Revision:     revision,
			Instructions: strings.TrimSuffix(instructions, "\n"),
		}
		return nil
	})
	return expert, err
}

func (m *ManagedLibrary) initialize(ctx context.Context, spec ManagedProfileSpec) (ManagedLibraryMutation, error) {
	if m.contract == nil {
		return ManagedLibraryMutation{}, fmt.Errorf("managed Agent Pack contract runner is unavailable")
	}
	if entries, err := os.ReadDir(m.root); err == nil {
		if len(entries) != 0 {
			return ManagedLibraryMutation{}, fmt.Errorf("managed local expert root %s is not empty but has no usable origin; move it aside or repair it before retrying", m.root)
		}
	} else if !os.IsNotExist(err) {
		return ManagedLibraryMutation{}, err
	}
	parent := filepath.Dir(m.root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return ManagedLibraryMutation{}, err
	}
	operationRoot, err := os.MkdirTemp(parent, ".local-init-")
	if err != nil {
		return ManagedLibraryMutation{}, err
	}
	defer os.RemoveAll(operationRoot)
	repoRoot := filepath.Join(operationRoot, ManagedLocalPackNamespace)
	if err := initializeManagedPack(repoRoot); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if err := writeManagedProfile(repoRoot, spec); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if err := m.check(ctx, repoRoot); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if _, err := gitCommand(ctx, repoRoot, "init"); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if err := configureManagedGitIdentity(ctx, repoRoot); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if _, err := gitCommand(ctx, repoRoot, "add", "--all"); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if _, err := gitCommand(ctx, repoRoot, "commit", "-m", "amux: create local expert "+spec.Name); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if _, err := gitCommand(ctx, repoRoot, "branch", "-M", managedLocalPackBranch); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if _, err := gitCommand(ctx, "", "clone", "--bare", repoRoot, m.originPath()); err != nil {
		_ = os.RemoveAll(m.originPath())
		return ManagedLibraryMutation{}, err
	}
	materializedRoot := filepath.Dir(packMaterializedPath(m.registryPath, ManagedLocalPackNamespace))
	_, materializedStatErr := os.Stat(materializedRoot)
	if materializedStatErr != nil && !os.IsNotExist(materializedStatErr) {
		_ = os.RemoveAll(m.originPath())
		return ManagedLibraryMutation{}, materializedStatErr
	}
	materializedWasAbsent := os.IsNotExist(materializedStatErr)
	pack, err := m.syncPublished(ctx)
	if err != nil {
		_ = os.RemoveAll(m.originPath())
		if materializedWasAbsent {
			_ = os.RemoveAll(materializedRoot)
		}
		return ManagedLibraryMutation{}, err
	}
	return ManagedLibraryMutation{ProfileRef: ManagedLocalPackNamespace + "/" + spec.Name, Revision: pack.CurrentRevision}, nil
}

func (m *ManagedLibrary) mutateExisting(ctx context.Context, expectedRevision, profileName string, mutate func(string) error, message string) (ManagedLibraryMutation, error) {
	return m.mutateExistingSource(ctx, expectedRevision, profileName, mutate, message, false)
}

func (m *ManagedLibrary) mutateExistingSource(ctx context.Context, expectedRevision, profileName string, mutate func(string) error, message string, unpublished bool) (ManagedLibraryMutation, error) {
	if m.contract == nil {
		return ManagedLibraryMutation{}, fmt.Errorf("managed Agent Pack contract runner is unavailable")
	}
	current, err := m.currentRevision(ctx)
	if err != nil {
		return ManagedLibraryMutation{}, err
	}
	if expectedRevision != "" && expectedRevision != current {
		return ManagedLibraryMutation{}, fmt.Errorf("local expert library revision conflict: expected %s, current %s; refresh with `amux agent local status`", expectedRevision, current)
	}
	operationRoot, err := os.MkdirTemp(m.root, ".operation-")
	if err != nil {
		return ManagedLibraryMutation{}, err
	}
	defer os.RemoveAll(operationRoot)
	worktree := filepath.Join(operationRoot, "repo")
	if _, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "worktree", "add", "--detach", worktree, current); err != nil {
		return ManagedLibraryMutation{}, err
	}
	defer func() {
		_, _ = gitCommand(context.Background(), "", "--git-dir", m.originPath(), "worktree", "remove", "--force", worktree)
	}()
	if err := configureManagedGitIdentity(ctx, worktree); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if err := mutate(worktree); err != nil {
		return ManagedLibraryMutation{}, err
	}
	if unpublished {
		if err := validateManagedPackSource(worktree); err != nil {
			return ManagedLibraryMutation{}, err
		}
	} else {
		if err := m.check(ctx, worktree); err != nil {
			return ManagedLibraryMutation{}, err
		}
	}
	if _, err := gitCommand(ctx, worktree, "add", "--all"); err != nil {
		return ManagedLibraryMutation{}, err
	}
	status, err := gitCommand(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return ManagedLibraryMutation{}, err
	}
	if strings.TrimSpace(status) == "" {
		return managedMutation(profileName, current), nil
	}
	if _, err := gitCommand(ctx, worktree, "commit", "-m", message); err != nil {
		return ManagedLibraryMutation{}, err
	}
	next, err := gitCommand(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return ManagedLibraryMutation{}, err
	}
	next = strings.TrimSpace(next)
	if unpublished {
		// Generic registration does not take the managed-library lock. Keep
		// the empty-library precondition true through the branch CAS too.
		err := WithPackRegistryLockContext(ctx, m.registryPath, func() error {
			registry, err := LoadPackRegistry(m.registryPath)
			if err != nil {
				return err
			}
			if _, registered := registry.Packs[ManagedLocalPackNamespace]; registered {
				return profileError("conversion_empty_registered")
			}
			_, err = gitCommand(ctx, "", "--git-dir", m.originPath(), "update-ref", "refs/heads/"+managedLocalPackBranch, next, current)
			return err
		})
		if err != nil {
			return ManagedLibraryMutation{}, err
		}
		return managedMutation(profileName, next), nil
	}
	pack, err := m.publishCandidate(ctx, current, next)
	if err != nil {
		return ManagedLibraryMutation{}, err
	}
	return managedMutation(profileName, pack.CurrentRevision), nil
}

func managedMutation(profileName, revision string) ManagedLibraryMutation {
	ref := ManagedLocalPackNamespace
	if strings.TrimSpace(profileName) != "" {
		ref += "/" + strings.TrimSpace(profileName)
	}
	return ManagedLibraryMutation{ProfileRef: ref, Revision: revision}
}

func (m *ManagedLibrary) syncPublished(ctx context.Context) (Pack, error) {
	revision, err := m.currentRevision(ctx)
	if err != nil {
		return Pack{}, err
	}
	return m.publishMaterializedRevision(ctx, "", revision)
}

func (m *ManagedLibrary) publishCandidate(ctx context.Context, current, next string) (Pack, error) {
	if _, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "update-ref", "refs/heads/"+managedLocalPackBranch, next, current); err != nil {
		return Pack{}, fmt.Errorf("local expert library revision conflict while publishing; refresh with `amux agent local status`: %w", err)
	}
	result, publishErr := m.publishMaterializedRevision(ctx, current, next)
	if publishErr == nil {
		return result, nil
	}
	if _, rollbackErr := gitCommand(context.Background(), "", "--git-dir", m.originPath(), "update-ref", "refs/heads/"+managedLocalPackBranch, current, next); rollbackErr != nil {
		return Pack{}, errors.Join(publishErr, fmt.Errorf("rollback formal branch: %w", rollbackErr))
	}
	return Pack{}, publishErr
}

func (m *ManagedLibrary) publishMaterializedRevision(ctx context.Context, expectedRevision, revision string) (Pack, error) {
	// Installations upgraded from the first managed-local prototype may still
	// point at a mutable agent-packs/local/repo checkout. Preserve its current
	// commit under the immutable layout before switching the registry, so old
	// session/task snapshots remain materializable after the first new write.
	if strings.TrimSpace(expectedRevision) != "" {
		registry, err := LoadPackRegistry(m.registryPath)
		if err != nil {
			return Pack{}, err
		}
		if pack, registered := registry.Packs[ManagedLocalPackNamespace]; registered && strings.TrimSpace(pack.CurrentRevision) == expectedRevision {
			// Historical snapshots can predate the current schema. Preserve
			// their bytes without revalidating or activating them as v2.
			if _, err := m.materializeRevisionChecked(ctx, expectedRevision, false); err != nil {
				return Pack{}, fmt.Errorf("preserve previous managed local revision %s: %w", shortPackRevision(expectedRevision), err)
			}
		}
	}
	materializedPath, err := m.materializeRevision(ctx, revision)
	if err != nil {
		return Pack{}, err
	}
	var result Pack
	err = WithPackRegistryLockContext(ctx, m.registryPath, func() error {
		registry, err := LoadPackRegistry(m.registryPath)
		if err != nil {
			return err
		}
		pack, registered := registry.Packs[ManagedLocalPackNamespace]
		if registered {
			pack, _, err = m.normalizeRegisteredPack(pack)
			if err != nil {
				return err
			}
			if expectedRevision != "" && strings.TrimSpace(pack.CurrentRevision) != expectedRevision {
				return fmt.Errorf("local expert library registry revision conflict: expected %s, current %s", expectedRevision, pack.CurrentRevision)
			}
		} else {
			pack = Pack{
				Namespace: ManagedLocalPackNamespace, SourceKind: PackSourceGit,
				GitURL: m.originPath(), Ref: managedLocalPackBranch, AutoSync: false,
				ManagedBy: ManagedPackOwnerAmux, DisplayName: managedLocalPackName,
				SyncInterval: defaultPackSyncInterval,
			}
		}
		pack.MaterializedPath = materializedPath
		pack.CurrentRevision = revision
		pack.LastSyncedAt = time.Now().UTC().Format(time.RFC3339)
		pack.LastError = ""
		pack.Versions = appendPackVersion(pack.Versions, revision, managedLocalPackBranch)
		registry.Packs[ManagedLocalPackNamespace] = pack
		if err := savePackRegistryUnlocked(m.registryPath, registry); err != nil {
			return err
		}
		result = pack
		return nil
	})
	if err != nil {
		return Pack{}, err
	}
	return result, nil
}

func (m *ManagedLibrary) materializeRevision(ctx context.Context, revision string) (string, error) {
	return m.materializeRevisionChecked(ctx, revision, true)
}

func (m *ManagedLibrary) materializeRevisionChecked(ctx context.Context, revision string, validate bool) (string, error) {
	revision = strings.TrimSpace(revision)
	if !managedRevisionPattern.MatchString(revision) {
		return "", fmt.Errorf("invalid managed local expert revision %q", revision)
	}
	target := managedRevisionMaterializedPath(m.registryPath, revision)
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		head, err := gitCommand(ctx, target, "rev-parse", "HEAD")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(head) != revision {
			return "", fmt.Errorf("managed local revision path %s contains %s", target, strings.TrimSpace(head))
		}
		pack := m.packForMaterializedRevision(target, revision)
		if validate {
			if err := ValidatePackManifest(pack); err != nil {
				return "", fmt.Errorf("validate existing managed local revision %s: %w", shortPackRevision(revision), err)
			}
		}
		return target, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	localRoot := filepath.Dir(packMaterializedPath(m.registryPath, ManagedLocalPackNamespace))
	if err := os.MkdirAll(localRoot, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(localRoot, ".revision-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	stageRepo := filepath.Join(stage, "repo")
	if _, err := gitCommand(ctx, "", "clone", "--no-checkout", m.originPath(), stageRepo); err != nil {
		return "", err
	}
	if _, err := gitCommand(ctx, stageRepo, "checkout", "--detach", revision); err != nil {
		return "", err
	}
	if validate {
		if err := ValidatePackManifest(m.packForMaterializedRevision(stageRepo, revision)); err != nil {
			return "", err
		}
	}
	targetRoot := filepath.Dir(target)
	if err := os.MkdirAll(filepath.Dir(targetRoot), 0700); err != nil {
		return "", err
	}
	if err := os.Rename(stage, targetRoot); err != nil {
		return "", fmt.Errorf("publish immutable managed local revision %s: %w", shortPackRevision(revision), err)
	}
	return target, nil
}

func (m *ManagedLibrary) packForMaterializedRevision(path, revision string) Pack {
	return Pack{
		Namespace: ManagedLocalPackNamespace, SourceKind: PackSourceGit,
		ManagedBy: ManagedPackOwnerAmux, DisplayName: managedLocalPackName,
		GitURL: m.originPath(), Ref: managedLocalPackBranch,
		MaterializedPath: path, CurrentRevision: revision, AutoSync: false,
		SyncInterval: defaultPackSyncInterval,
	}
}

func (m *ManagedLibrary) normalizeRegisteredPack(pack Pack) (Pack, bool, error) {
	managedBy := strings.TrimSpace(pack.ManagedBy)
	if !SamePackGitURL(pack.GitURL, m.originPath()) || (managedBy != "" && managedBy != ManagedPackOwnerAmux) {
		return Pack{}, false, fmt.Errorf("agent pack namespace %q is already used by another source", ManagedLocalPackNamespace)
	}
	changed := false
	if managedBy == "" {
		pack.ManagedBy = ManagedPackOwnerAmux
		changed = true
	}
	if strings.TrimSpace(pack.DisplayName) == "" {
		pack.DisplayName = managedLocalPackName
		changed = true
	}
	return pack, changed, nil
}

func (m *ManagedLibrary) validateIdentity() error {
	return m.validateIdentityWithRepair(true)
}

func (m *ManagedLibrary) validateIdentityWithRepair(repair bool) error {
	return WithPackRegistryLock(m.registryPath, func() error {
		registry, err := LoadPackRegistry(m.registryPath)
		if err != nil {
			return err
		}
		pack, registered := registry.Packs[ManagedLocalPackNamespace]
		if !registered {
			return nil
		}
		pack, changed, normalizeErr := m.normalizeRegisteredPack(pack)
		if normalizeErr != nil {
			return fmt.Errorf("agent pack namespace %q is already used by another source; remove or rename it before creating local experts", ManagedLocalPackNamespace)
		}
		if !m.originExists() {
			return fmt.Errorf("managed local expert origin is missing at %s", m.originPath())
		}
		if !changed || !repair {
			return nil
		}
		registry.Packs[ManagedLocalPackNamespace] = pack
		return savePackRegistryUnlocked(m.registryPath, registry)
	})
}

func (m *ManagedLibrary) withLock(operation func() error) error {
	if m == nil || strings.TrimSpace(m.root) == "" || strings.TrimSpace(m.registryPath) == "" {
		return fmt.Errorf("managed local expert library paths are required")
	}
	if operation == nil {
		return fmt.Errorf("managed local expert library operation is required")
	}
	if err := os.MkdirAll(filepath.Dir(m.root), 0700); err != nil {
		return err
	}
	lockPath := m.root + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockExclusive(lock); err != nil {
		return err
	}
	defer unlockExclusive(lock) //nolint:errcheck
	return operation()
}

func (m *ManagedLibrary) originPath() string {
	return filepath.Join(m.root, "origin.git")
}

func (m *ManagedLibrary) originExists() bool {
	info, err := os.Stat(filepath.Join(m.originPath(), "HEAD"))
	return err == nil && !info.IsDir()
}

func (m *ManagedLibrary) currentRevision(ctx context.Context) (string, error) {
	revision, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "rev-parse", "refs/heads/"+managedLocalPackBranch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(revision), nil
}

func validateManagedProfileSpec(spec *ManagedProfileSpec) error {
	if spec == nil {
		return fmt.Errorf("local expert spec is required")
	}
	spec.Name = strings.TrimSpace(spec.Name)
	if err := validateManagedProfileName(spec.Name); err != nil {
		return err
	}
	spec.Description = strings.TrimSpace(spec.Description)
	if strings.TrimSpace(spec.Instructions) == "" {
		return fmt.Errorf("instructions must not be empty")
	}
	return nil
}

func validateManagedProfileName(name string) error {
	if !managedProfileNamePattern.MatchString(strings.TrimSpace(name)) {
		return fmt.Errorf("local expert name %q must be lowercase kebab-case", name)
	}
	return nil
}

func writeManagedProfile(repoRoot string, spec ManagedProfileSpec) error {
	description := strings.TrimSpace(spec.Description)
	if description == "" {
		description = spec.Name
	}
	profilePath := managedProfilePath(repoRoot, spec.Name)
	if err := os.MkdirAll(filepath.Dir(profilePath), 0700); err != nil {
		return err
	}
	lines := []string{
		"description = " + managedTOMLString(description),
	}
	lines = append(lines,
		"instructions = ["+managedTOMLString(filepath.ToSlash(filepath.Join("instructions", "common", spec.Name, "AGENTS.md")))+"]",
		"skills = []",
		"subagents = []",
		"",
	)
	if err := os.WriteFile(profilePath, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		return err
	}
	return writeManagedInstructions(repoRoot, spec.Name, spec.Instructions)
}

func writeManagedInstructions(repoRoot, name, instructions string) error {
	path := managedInstructionsPath(repoRoot, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if !strings.HasSuffix(instructions, "\n") {
		instructions += "\n"
	}
	return os.WriteFile(path, []byte(instructions), 0600)
}

func writeBuiltinFork(repoRoot string, profile Profile, targetName string) error {
	source := map[string]any{
		"description": profile.Description,
		"skills":      cloneStrings(profile.Skills),
		"subagents":   cloneStrings(profile.Subagents),
		"envs":        cloneStrings(profile.Envs),
		"env_refs":    cloneStrings(profile.EnvRefs),
	}
	if err := copyBuiltinForkAssets(builtinProfileFS, source, repoRoot); err != nil {
		return err
	}
	if strings.TrimSpace(profile.Description) == "" {
		source["description"] = targetName
	}
	if strings.TrimSpace(profile.AgentsContent) != "" {
		instructionRef := filepath.ToSlash(filepath.Join("instructions", "common", targetName, "AGENTS.md"))
		source["instructions"] = []string{instructionRef}
		if err := writeManagedInstructions(repoRoot, targetName, profile.AgentsContent); err != nil {
			return err
		}
	} else {
		delete(source, "instructions")
	}
	data, err := toml.Marshal(source)
	if err != nil {
		return fmt.Errorf("encode builtin profile %s for local fork: %w", profile.Ref(), err)
	}
	path := managedProfilePath(repoRoot, targetName)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func copyBuiltinForkAssets(sourceFS fs.FS, profileSource map[string]any, destinationRoot string) error {
	for _, name := range managedStringSlice(profileSource["skills"]) {
		copied := false
		for _, category := range []string{"custom", "upstream"} {
			sourcePath := filepath.ToSlash(filepath.Join("skills", category, name))
			if _, err := fs.Stat(sourceFS, sourcePath); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return err
			}
			if err := copyEmbeddedPathIfAbsent(sourceFS, sourcePath, destinationRoot); err != nil {
				return err
			}
			copied = true
			break
		}
		if !copied {
			return fmt.Errorf("builtin skill %q is missing from the embedded source pack", name)
		}
	}
	return nil
}

func managedStringSlice(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func copyEmbeddedPathIfAbsent(source fs.FS, sourcePath, destinationRoot string) error {
	destination := filepath.Join(destinationRoot, filepath.FromSlash(sourcePath))
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return fs.WalkDir(source, sourcePath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		target := filepath.Join(destinationRoot, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
}

func managedProfilePath(repoRoot, name string) string {
	return filepath.Join(repoRoot, "profiles", "common", name+".toml")
}

func managedRevisionMaterializedPath(registryPath, revision string) string {
	localRoot := filepath.Dir(packMaterializedPath(registryPath, ManagedLocalPackNamespace))
	return filepath.Join(localRoot, "revisions", strings.TrimSpace(revision), "repo")
}

func managedInstructionsPath(repoRoot, name string) string {
	return filepath.Join(repoRoot, "instructions", "common", name, "AGENTS.md")
}

func managedTOMLString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func cleanManagedPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func configureManagedGitIdentity(ctx context.Context, repoRoot string) error {
	if _, err := gitCommand(ctx, repoRoot, "config", "user.name", "amux"); err != nil {
		return err
	}
	_, err := gitCommand(ctx, repoRoot, "config", "user.email", "amux@local")
	return err
}

// check validates both sides of the compiler boundary before preview or publish.
func (m *ManagedLibrary) check(ctx context.Context, root string) error {
	if err := validateManagedPackSource(root); err != nil {
		return err
	}
	if err := m.contract.Check(ctx, root); err != nil {
		return err
	}
	return ValidatePackManifest(Pack{Namespace: ManagedLocalPackNamespace, MaterializedPath: root})
}

func validateManagedPackSource(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "agent-pack.toml"))
	if err != nil {
		return err
	}
	var source struct {
		SchemaVersion string `toml:"schema_version"`
	}
	if err := toml.Unmarshal(data, &source); err != nil {
		return err
	}
	if source.SchemaVersion != supportedPackManifestSchemaVersion {
		return profileError("source_schema_unsupported", source.SchemaVersion)
	}
	return nil
}
