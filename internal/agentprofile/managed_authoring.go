package agentprofile

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

type ManagedAuthoringOperation string

const (
	ManagedAuthoringCreate ManagedAuthoringOperation = "create"
	ManagedAuthoringFork   ManagedAuthoringOperation = "fork"
	ManagedAuthoringUpdate ManagedAuthoringOperation = "update"

	managedAuthoringPatchMaxBytes = 512 << 10
	managedAuthoringFileMaxBytes  = 256 << 10
	managedAuthoringReadMaxBytes  = 2 << 20
	managedEvaluationTTL          = 30 * time.Minute
	managedEvaluationPrefix       = "local-eval-"
	managedEvaluationSuffix       = "-dev"
)

type ManagedAuthoringRequest struct {
	Operation    ManagedAuthoringOperation
	TargetRef    string
	SourceRef    string
	BaseRevision string
	Patch        string
}

type ManagedAuthoringFile struct {
	Path    string
	Content string
	Binary  bool
}

type ManagedAuthoringBase struct {
	Operation    ManagedAuthoringOperation
	TargetRef    string
	SourceRef    string
	BaseRevision string
	Files        []ManagedAuthoringFile
}

type ManagedAuthoringResult struct {
	BaseRevision string
	Tree         string
	PatchDigest  string
	Diff         string
	Mutation     ManagedLibraryMutation
}

type ManagedAuthoringEvaluation struct {
	Namespace  string
	ProfileRef string
	Tree       string
	ExpiresAt  time.Time
}

type managedAuthoringApplyMode uint8

const (
	managedAuthoringPreview managedAuthoringApplyMode = iota
	managedAuthoringPublish
	managedAuthoringEvaluation
)

type managedAuthoringApplyOptions struct {
	mode                managedAuthoringApplyMode
	expectedTree        string
	evaluationNamespace string
}

// ReadAuthoringBase reconstructs the exact source tree an Agent must patch.
// The temporary worktree is deleted before return and never becomes a formal
// local revision.
func (m *ManagedLibrary) ReadAuthoringBase(ctx context.Context, request ManagedAuthoringRequest) (ManagedAuthoringBase, error) {
	request, targetName, err := normalizeManagedAuthoringRequest(request, false)
	if err != nil {
		return ManagedAuthoringBase{}, err
	}
	if err := m.Ensure(ctx); err != nil {
		return ManagedAuthoringBase{}, err
	}
	var result ManagedAuthoringBase
	err = m.withAuthoringWorktree(ctx, request, targetName, func(worktree, current string) error {
		if err := validateManagedAuthoringTree(worktree); err != nil {
			return err
		}
		files, err := readManagedAuthoringFiles(worktree, targetName)
		if err != nil {
			return err
		}
		result = ManagedAuthoringBase{
			Operation: request.Operation, TargetRef: request.TargetRef, SourceRef: request.SourceRef,
			BaseRevision: current, Files: files,
		}
		return nil
	})
	return result, err
}

func (m *ManagedLibrary) PreviewAuthoring(ctx context.Context, request ManagedAuthoringRequest) (ManagedAuthoringResult, error) {
	return m.applyAuthoring(ctx, request, managedAuthoringApplyOptions{mode: managedAuthoringPreview})
}

func (m *ManagedLibrary) PublishAuthoring(ctx context.Context, request ManagedAuthoringRequest, expectedTree string) (ManagedAuthoringResult, error) {
	expectedTree = strings.TrimSpace(expectedTree)
	if !managedRevisionPattern.MatchString(expectedTree) {
		return ManagedAuthoringResult{}, fmt.Errorf("confirmed preview tree is required")
	}
	return m.applyAuthoring(ctx, request, managedAuthoringApplyOptions{mode: managedAuthoringPublish, expectedTree: expectedTree})
}

// PrepareAuthoringEvaluation validates the same preview tree and exposes it as
// a short-lived hidden Profile. It never advances the managed local branch.
func (m *ManagedLibrary) PrepareAuthoringEvaluation(ctx context.Context, request ManagedAuthoringRequest) (ManagedAuthoringResult, ManagedAuthoringEvaluation, error) {
	if err := m.ReconcileAuthoringEvaluations(time.Now()); err != nil {
		return ManagedAuthoringResult{}, ManagedAuthoringEvaluation{}, err
	}
	namespace, err := newManagedEvaluationNamespace()
	if err != nil {
		return ManagedAuthoringResult{}, ManagedAuthoringEvaluation{}, err
	}
	result, err := m.applyAuthoring(ctx, request, managedAuthoringApplyOptions{mode: managedAuthoringEvaluation, evaluationNamespace: namespace})
	if err != nil {
		return ManagedAuthoringResult{}, ManagedAuthoringEvaluation{}, err
	}
	branchRef := "refs/heads/" + namespace
	defer func() {
		_, _ = gitCommand(context.Background(), "", "--git-dir", m.originPath(), "update-ref", "-d", branchRef)
	}()
	pack, err := AddGitPackWithOptions(ctx, m.registryPath, namespace, m.originPath(), AddGitPackOptions{
		Ref: namespace, DevelopmentOf: ManagedLocalPackNamespace, Temporary: true, Evaluation: true,
		ManagedBy: ManagedPackOwnerAmux, DisplayName: "Local evaluation", AutoSync: false,
	})
	if err != nil {
		return ManagedAuthoringResult{}, ManagedAuthoringEvaluation{}, err
	}
	_, targetName, _ := strings.Cut(request.TargetRef, "/")
	return result, ManagedAuthoringEvaluation{
		Namespace: namespace, ProfileRef: namespace + "/" + targetName, Tree: result.Tree,
		ExpiresAt: parseManagedEvaluationExpiry(pack),
	}, nil
}

func (m *ManagedLibrary) CleanupAuthoringEvaluation(profileRef string) error {
	namespace, _, ok := strings.Cut(strings.TrimSpace(profileRef), "/")
	if !ok || !isManagedEvaluationNamespace(namespace) {
		return fmt.Errorf("invalid local expert evaluation profile ref %q", profileRef)
	}
	registry, err := LoadPackRegistry(m.registryPath)
	if err != nil {
		return err
	}
	pack, ok := registry.Packs[namespace]
	if !ok {
		return nil
	}
	if !isManagedEvaluationPack(pack) {
		return fmt.Errorf("agent pack namespace %q is not a managed local evaluation", namespace)
	}
	_, err = RemovePack(m.registryPath, namespace, RemovePackOptions{})
	return err
}

// ReconcileAuthoringEvaluations removes hidden candidates whose fixed TTL has
// elapsed. Running tasks already copied their frozen Profile into a workspace.
func (m *ManagedLibrary) ReconcileAuthoringEvaluations(now time.Time) error {
	registry, err := LoadPackRegistry(m.registryPath)
	if err != nil {
		return err
	}
	var expired []string
	for namespace, pack := range registry.Packs {
		if isManagedEvaluationPack(pack) && !now.Before(parseManagedEvaluationExpiry(pack)) {
			expired = append(expired, namespace)
		}
	}
	sort.Strings(expired)
	for _, namespace := range expired {
		if _, err := RemovePack(m.registryPath, namespace, RemovePackOptions{}); err != nil {
			return err
		}
	}
	refs, err := gitCommand(context.Background(), "", "--git-dir", m.originPath(), "for-each-ref", "--format=%(refname)", "refs/heads/"+managedEvaluationPrefix)
	if err == nil {
		for _, ref := range strings.Fields(refs) {
			if isManagedEvaluationNamespace(strings.TrimPrefix(ref, "refs/heads/")) {
				_, _ = gitCommand(context.Background(), "", "--git-dir", m.originPath(), "update-ref", "-d", ref)
			}
		}
	}
	return nil
}

func (m *ManagedLibrary) applyAuthoring(ctx context.Context, request ManagedAuthoringRequest, opts managedAuthoringApplyOptions) (ManagedAuthoringResult, error) {
	request, targetName, err := normalizeManagedAuthoringRequest(request, true)
	if err != nil {
		return ManagedAuthoringResult{}, err
	}
	if err := validateManagedAuthoringPatch(request.Patch); err != nil {
		return ManagedAuthoringResult{}, err
	}
	if err := m.Ensure(ctx); err != nil {
		return ManagedAuthoringResult{}, err
	}
	digest := sha256.Sum256([]byte(request.Patch))
	result := ManagedAuthoringResult{BaseRevision: request.BaseRevision, PatchDigest: hex.EncodeToString(digest[:])}
	err = m.withAuthoringWorktree(ctx, request, targetName, func(worktree, current string) error {
		if current != request.BaseRevision {
			return fmt.Errorf("local expert library revision conflict: expected %s, current %s", request.BaseRevision, current)
		}
		if strings.TrimSpace(request.Patch) != "" {
			if err := gitApplyManagedPatch(ctx, worktree, request.Patch); err != nil {
				return err
			}
		}
		if err := validateManagedAuthoringTree(worktree); err != nil {
			return err
		}
		if err := validateManagedAuthoringTarget(worktree, targetName); err != nil {
			return err
		}
		if err := m.contract.Check(ctx, worktree); err != nil {
			return err
		}
		if err := validateManagedAuthoringTree(worktree); err != nil {
			return err
		}
		if _, err := gitCommand(ctx, worktree, "add", "--all"); err != nil {
			return err
		}
		tree, err := gitCommand(ctx, worktree, "write-tree")
		if err != nil {
			return err
		}
		result.Tree = strings.TrimSpace(tree)
		diff, err := gitCommand(ctx, worktree, "diff", "--cached", "--binary", "--no-color", current)
		if err != nil {
			return err
		}
		result.Diff = diff
		if opts.expectedTree != "" && result.Tree != opts.expectedTree {
			return fmt.Errorf("confirmed preview tree mismatch: expected %s, reconstructed %s", opts.expectedTree, result.Tree)
		}
		switch opts.mode {
		case managedAuthoringEvaluation:
			if _, err := gitCommand(ctx, worktree, "commit", "--allow-empty", "-m", "amux: temporary local expert evaluation"); err != nil {
				return err
			}
			revision, err := gitCommand(ctx, worktree, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			if _, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "update-ref", "refs/heads/"+opts.evaluationNamespace, strings.TrimSpace(revision)); err != nil {
				return err
			}
			return nil
		case managedAuthoringPreview:
			return nil
		case managedAuthoringPublish:
		default:
			return fmt.Errorf("invalid managed authoring apply mode")
		}
		statusText, err := gitCommand(ctx, worktree, "status", "--porcelain")
		if err != nil {
			return err
		}
		if strings.TrimSpace(statusText) == "" {
			result.Mutation = managedMutation(targetName, current)
			return nil
		}
		if _, err := gitCommand(ctx, worktree, "commit", "-m", managedAuthoringCommitMessage(request, targetName)); err != nil {
			return err
		}
		next, err := gitCommand(ctx, worktree, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		pack, err := m.publishCandidate(ctx, current, strings.TrimSpace(next))
		if err != nil {
			return err
		}
		result.Mutation = managedMutation(targetName, pack.CurrentRevision)
		return nil
	})
	return result, err
}

func newManagedEvaluationNamespace() (string, error) {
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate local expert evaluation namespace: %w", err)
	}
	return managedEvaluationPrefix + hex.EncodeToString(token[:]) + managedEvaluationSuffix, nil
}

func isManagedEvaluationNamespace(namespace string) bool {
	return strings.HasPrefix(namespace, managedEvaluationPrefix) && strings.HasSuffix(namespace, managedEvaluationSuffix)
}

func isManagedEvaluationPack(pack Pack) bool {
	return isManagedEvaluationNamespace(pack.Namespace) && pack.Temporary && pack.Evaluation &&
		pack.DevelopmentOf == ManagedLocalPackNamespace && pack.ManagedBy == ManagedPackOwnerAmux
}

// IsManagedEvaluationProfileRef reports whether ref belongs to the reserved,
// evaluation-only local candidate namespace.
func IsManagedEvaluationProfileRef(ref string) bool {
	namespace, _, ok := strings.Cut(strings.TrimSpace(ref), "/")
	return ok && isManagedEvaluationNamespace(namespace)
}

func parseManagedEvaluationExpiry(pack Pack) time.Time {
	syncedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(pack.LastSyncedAt))
	if err != nil {
		return time.Time{}
	}
	return syncedAt.Add(managedEvaluationTTL)
}

func validateManagedAuthoringTarget(worktree, targetName string) error {
	info, err := os.Lstat(managedProfilePath(worktree, targetName))
	if os.IsNotExist(err) {
		return fmt.Errorf("authoring patch must preserve target expert %q", targetName)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("authoring target profile for %q must be a regular file", targetName)
	}
	return nil
}

func normalizeManagedAuthoringRequest(request ManagedAuthoringRequest, requireBase bool) (ManagedAuthoringRequest, string, error) {
	request.Operation = ManagedAuthoringOperation(strings.TrimSpace(string(request.Operation)))
	request.TargetRef = strings.TrimSpace(request.TargetRef)
	request.SourceRef = strings.TrimSpace(request.SourceRef)
	request.BaseRevision = strings.TrimSpace(request.BaseRevision)
	namespace, targetName, ok := strings.Cut(request.TargetRef, "/")
	if !ok || namespace != ManagedLocalPackNamespace {
		return request, "", fmt.Errorf("target_ref must be local/<name>")
	}
	if err := validateManagedProfileName(targetName); err != nil {
		return request, "", err
	}
	switch request.Operation {
	case ManagedAuthoringCreate, ManagedAuthoringUpdate:
		if request.SourceRef != "" {
			return request, "", fmt.Errorf("source_ref is only valid for fork")
		}
	case ManagedAuthoringFork:
		if !strings.HasPrefix(request.SourceRef, BuiltinNamespace+"/") {
			return request, "", fmt.Errorf("fork source_ref must be builtin/<name>")
		}
	default:
		return request, "", fmt.Errorf("operation must be create, fork, or update")
	}
	if requireBase && !managedRevisionPattern.MatchString(request.BaseRevision) {
		return request, "", fmt.Errorf("base_revision is required")
	}
	return request, targetName, nil
}

func (m *ManagedLibrary) withAuthoringWorktree(ctx context.Context, request ManagedAuthoringRequest, targetName string, fn func(worktree, current string) error) error {
	if m.contract == nil {
		return fmt.Errorf("managed Agent Pack contract runner is unavailable")
	}
	return m.withLock(func() error {
		if err := m.validateIdentity(); err != nil {
			return err
		}
		current, err := m.currentRevision(ctx)
		if err != nil {
			return err
		}
		operationRoot, err := os.MkdirTemp(m.root, ".authoring-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(operationRoot)
		worktree := filepath.Join(operationRoot, "repo")
		if _, err := gitCommand(ctx, "", "--git-dir", m.originPath(), "worktree", "add", "--detach", worktree, current); err != nil {
			return err
		}
		defer func() {
			_, _ = gitCommand(context.Background(), "", "--git-dir", m.originPath(), "worktree", "remove", "--force", worktree)
		}()
		if err := configureManagedGitIdentity(ctx, worktree); err != nil {
			return err
		}
		if err := prepareManagedAuthoringBase(worktree, request, targetName); err != nil {
			return err
		}
		if _, err := gitCommand(ctx, worktree, "add", "--all"); err != nil {
			return err
		}
		return fn(worktree, current)
	})
}

func prepareManagedAuthoringBase(worktree string, request ManagedAuthoringRequest, targetName string) error {
	profilePath := managedProfilePath(worktree, targetName)
	_, statErr := os.Stat(profilePath)
	switch request.Operation {
	case ManagedAuthoringUpdate:
		if os.IsNotExist(statErr) {
			return fmt.Errorf("unknown local expert %q", targetName)
		}
		return statErr
	case ManagedAuthoringCreate, ManagedAuthoringFork:
		if statErr == nil {
			return fmt.Errorf("local expert %q already exists", targetName)
		}
		if !os.IsNotExist(statErr) {
			return statErr
		}
	}
	if request.Operation == ManagedAuthoringCreate {
		return writeManagedProfile(worktree, ManagedProfileSpec{
			Name: targetName, Type: ManagedLocalDefaultAgent, PermissionMode: "default",
			Instructions: "# " + targetName + "\n\nYou are the local expert " + targetName + ".\n",
		})
	}
	profile, ok := ResolveBuiltin(request.SourceRef)
	if !ok {
		return fmt.Errorf("unknown builtin expert %q", request.SourceRef)
	}
	return writeBuiltinFork(worktree, profile, targetName)
}

func readManagedAuthoringFiles(worktree, targetName string) ([]ManagedAuthoringFile, error) {
	profileRel := filepath.ToSlash(filepath.Join("profiles", "common", targetName+".toml"))
	profileData, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(profileRel)))
	if err != nil {
		return nil, err
	}
	source := map[string]any{}
	if err := toml.Unmarshal(profileData, &source); err != nil {
		return nil, fmt.Errorf("parse local expert source: %w", err)
	}
	paths := map[string]struct{}{profileRel: {}}
	for _, key := range []string{"instructions", "agents"} {
		for _, ref := range managedAuthoringRefs(source[key]) {
			if err := addManagedAuthoringPath(worktree, ref, paths); err != nil {
				return nil, err
			}
		}
	}
	for _, skillName := range managedAuthoringRefs(source["skills"]) {
		for _, category := range []string{"custom", "upstream"} {
			ref := filepath.ToSlash(filepath.Join("skills", category, skillName))
			if _, err := os.Stat(filepath.Join(worktree, filepath.FromSlash(ref))); err == nil {
				if err := addManagedAuthoringPath(worktree, ref, paths); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	providerRefs := append(managedAuthoringRefs(source["providers"]), managedAuthoringRefs(source["supported_models"])...)
	if model, ok := source["model"].(string); ok {
		providerRefs = append(providerRefs, model)
	}
	for _, ref := range providerRefs {
		binding, model, ok := strings.Cut(ref, "/")
		if !ok {
			continue
		}
		providerPath := filepath.ToSlash(filepath.Join("providers", binding, model+".toml"))
		if _, err := os.Stat(filepath.Join(worktree, filepath.FromSlash(providerPath))); err == nil {
			paths[providerPath] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(paths))
	for name := range paths {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	files := make([]ManagedAuthoringFile, 0, len(ordered))
	total := 0
	for _, name := range ordered {
		data, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		if len(data) > managedAuthoringFileMaxBytes {
			return nil, fmt.Errorf("authoring file %s exceeds %d bytes", name, managedAuthoringFileMaxBytes)
		}
		total += len(data)
		if total > managedAuthoringReadMaxBytes {
			return nil, fmt.Errorf("authoring source exceeds %d bytes", managedAuthoringReadMaxBytes)
		}
		file := ManagedAuthoringFile{Path: name, Binary: !utf8.Valid(data)}
		if !file.Binary {
			file.Content = string(data)
		}
		files = append(files, file)
	}
	return files, nil
}

func managedAuthoringRefs(value any) []string {
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return []string{strings.TrimSpace(text)}
	}
	return managedStringSlice(value)
}

func addManagedAuthoringPath(root, rel string, paths map[string]struct{}) error {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if err := validateManagedAuthoringPath(rel); err != nil {
		return err
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		paths[rel] = struct{}{}
		return nil
	}
	return filepath.WalkDir(full, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		child, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		paths[filepath.ToSlash(child)] = struct{}{}
		return nil
	})
}

func validateManagedAuthoringPatch(patchText string) error {
	if len(patchText) > managedAuthoringPatchMaxBytes {
		return fmt.Errorf("authoring patch exceeds %d bytes", managedAuthoringPatchMaxBytes)
	}
	if strings.ContainsRune(patchText, '\x00') || strings.Contains(patchText, "GIT binary patch") || strings.Contains(patchText, "Binary files ") {
		return fmt.Errorf("binary patches are not supported")
	}
	scanner := bufio.NewScanner(strings.NewReader(patchText))
	for scanner.Scan() {
		line := scanner.Text()
		var candidates []string
		switch {
		case strings.HasPrefix(line, "diff --git "):
			candidates = strings.Fields(strings.TrimPrefix(line, "diff --git "))
			if len(candidates) != 2 {
				return fmt.Errorf("unsafe or quoted patch path")
			}
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			candidates = []string{strings.TrimSpace(line[4:])}
		case strings.HasPrefix(line, "rename from "):
			candidates = []string{strings.TrimPrefix(line, "rename from ")}
		case strings.HasPrefix(line, "rename to "):
			candidates = []string{strings.TrimPrefix(line, "rename to ")}
		case strings.HasPrefix(line, "copy from "):
			candidates = []string{strings.TrimPrefix(line, "copy from ")}
		case strings.HasPrefix(line, "copy to "):
			candidates = []string{strings.TrimPrefix(line, "copy to ")}
		}
		for _, candidate := range candidates {
			candidate = strings.TrimPrefix(strings.TrimPrefix(candidate, "a/"), "b/")
			if candidate == "/dev/null" {
				continue
			}
			if err := validateManagedAuthoringPath(candidate); err != nil {
				return fmt.Errorf("unsafe patch path %q: %w", candidate, err)
			}
		}
	}
	return scanner.Err()
}

func validateManagedAuthoringPath(rel string) error {
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" || strings.ContainsAny(rel, "\x00\r\n") || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".git" || strings.HasPrefix(rel, ".git/") || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("path must be a clean Pack-relative path")
	}
	return nil
}

func gitApplyManagedPatch(ctx context.Context, worktree, patchText string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "apply", "--index", "--recount", "--whitespace=nowarn", "-")
	cmd.Stdin = strings.NewReader(patchText)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(out))
	if message == "" {
		return fmt.Errorf("apply authoring patch: %w", err)
	}
	return fmt.Errorf("apply authoring patch: %w: %s", err, message)
}

func validateManagedAuthoringTree(root string) error {
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == filepath.Join(root, ".git") {
			return nil
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("authoring tree contains unsupported symlink %s", name)
		}
		return nil
	})
}

func managedAuthoringCommitMessage(request ManagedAuthoringRequest, targetName string) string {
	switch request.Operation {
	case ManagedAuthoringCreate:
		return "amux: create local expert " + targetName
	case ManagedAuthoringFork:
		return "amux: fork " + request.SourceRef + " to local/" + targetName
	default:
		return "amux: update local expert " + targetName
	}
}
