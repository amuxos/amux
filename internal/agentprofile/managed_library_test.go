package agentprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

type fakeManagedContractRunner struct {
	failCheck       bool
	invalidManifest bool
	checkHook       func() error
}

func (r *fakeManagedContractRunner) Init(_ context.Context, target string) error {
	if err := os.MkdirAll(filepath.Join(target, "dist"), 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(target, "agent-pack.toml"), []byte(strings.Join([]string{
		`schema_version = "v1.1"`,
		`name = "local"`,
		`namespace = "local"`,
		`description = "Local experts managed by amux."`,
		`owners = []`,
		"",
	}, "\n")), 0600)
}

func (r *fakeManagedContractRunner) Check(_ context.Context, repoRoot string) error {
	if r.failCheck {
		return errors.New("contract rejected source")
	}
	profilePaths, err := filepath.Glob(filepath.Join(repoRoot, "profiles", "common", "*.toml"))
	if err != nil {
		return err
	}
	profiles := map[string]any{}
	names := make([]string, 0, len(profilePaths))
	for _, profilePath := range profilePaths {
		name := strings.TrimSuffix(filepath.Base(profilePath), ".toml")
		source, readErr := os.ReadFile(profilePath)
		if readErr != nil {
			return readErr
		}
		instructionPaths := []string{"instructions/common/" + name + "/AGENTS.md"}
		if r.invalidManifest {
			instructionPaths = []string{"instructions/missing/AGENTS.md"}
		}
		entry := map[string]any{
			"namespace":    "common",
			"description":  managedTestTOMLString(source, "description"),
			"type":         managedTestTOMLString(source, "type"),
			"providers":    []string{},
			"instructions": instructionPaths,
			"skills":       []string{},
			"subagents":    []string{},
			"extends":      nil,
			"permission": map[string]any{
				"permission_mode": managedTestTOMLString(source, "permission_mode"),
				"allowed_tools":   []string{},
				"ask_tools":       []string{},
			},
		}
		if model := managedTestTOMLString(source, "model"); model != "" {
			entry["model"] = model
		}
		profiles[name] = entry
		names = append(names, name)
	}
	sort.Strings(names)
	manifest := map[string]any{
		"schema_version": "v1.1",
		"pack_namespace": "local",
		"profiles":       profiles,
		"providers":      map[string]any{},
		"namespaces": map[string]any{
			"common": map[string]any{"profiles": names},
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(repoRoot, "dist", "agent-pack.manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		return err
	}
	if r.checkHook != nil {
		return r.checkHook()
	}
	return nil
}

func managedTestTOMLString(source []byte, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*=\s*["']([^"']*)["']\s*$`)
	match := re.FindSubmatch(source)
	if len(match) != 2 {
		return ""
	}
	return string(match[1])
}

func TestNewDefaultManagedLibraryUsesManagedContractExecutable(t *testing.T) {
	amuxHome := filepath.Join(t.TempDir(), "amux-home")
	t.Setenv("AMUX_HOME", amuxHome)
	t.Setenv("AMUX_AGENT_PACK_CONTRACT_BIN", "")

	library := NewDefaultManagedLibrary()
	runner, ok := library.contract.(ExecManagedContractRunner)
	if !ok {
		t.Fatalf("default contract runner = %T", library.contract)
	}
	want := filepath.Join(amuxHome, "libexec", "agent-pack-contract", "agent-pack-contract")
	if runner.Command != want {
		t.Fatalf("default Contract command = %q, want %q", runner.Command, want)
	}
}

func TestNewDefaultManagedLibraryHonorsContractExecutableOverride(t *testing.T) {
	t.Setenv("AMUX_AGENT_PACK_CONTRACT_BIN", "/tmp/custom-contract")

	library := NewDefaultManagedLibrary()
	runner, ok := library.contract.(ExecManagedContractRunner)
	if !ok {
		t.Fatalf("default contract runner = %T", library.contract)
	}
	if runner.Command != "/tmp/custom-contract" {
		t.Fatalf("overridden Contract command = %q", runner.Command)
	}
}

func TestDefaultManagedLibraryDegradedAuthoringStateBlocksWritesButAllowsSnapshot(t *testing.T) {
	amuxHome := filepath.Join(t.TempDir(), "amux-home")
	t.Setenv("AMUX_HOME", amuxHome)
	state := ManagedAuthoringState{
		AgentPackContractStatus:    ManagedAuthoringStatusDegraded,
		LocalExpertAuthoringStatus: ManagedAuthoringStatusDegraded,
	}
	if err := SaveDefaultManagedAuthoringState(state); err != nil {
		t.Fatalf("save degraded authoring state: %v", err)
	}

	library := NewDefaultManagedLibrary()
	_, err := library.Create(context.Background(), ManagedProfileSpec{
		Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "review",
	})
	if err == nil || !strings.Contains(err.Error(), "local expert authoring is degraded") || !strings.Contains(err.Error(), "amux update") {
		t.Fatalf("Create error = %v, want degraded authoring repair guidance", err)
	}
	if _, err := library.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot should remain available while authoring is degraded: %v", err)
	}
}

func TestManagedLibraryCreateRegistersStandardGitPack(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "managed-agent-packs", "local")
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(libraryRoot, registryPath, &fakeManagedContractRunner{})

	result, err := library.Create(context.Background(), ManagedProfileSpec{
		Name:           "reviewer",
		Description:    "Review concrete changes",
		Type:           "codex",
		Model:          "gpt-5.4",
		PermissionMode: "default",
		Instructions:   "# Reviewer\n\nReview concrete changes.\n",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.ProfileRef != "local/reviewer" || result.Revision == "" {
		t.Fatalf("Create result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(libraryRoot, "origin.git", "HEAD")); err != nil {
		t.Fatalf("managed bare origin: %v", err)
	}

	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	pack, ok := registry.Packs[ManagedLocalPackNamespace]
	if !ok {
		t.Fatal("managed local pack was not registered")
	}
	if pack.SourceKind != PackSourceGit || pack.ManagedBy != ManagedPackOwnerAmux || pack.AutoSync {
		t.Fatalf("registered pack = %+v", pack)
	}
	if pack.MaterializedPath != managedRevisionMaterializedPath(registryPath, result.Revision) {
		t.Fatalf("materialized path = %q, want immutable revision path", pack.MaterializedPath)
	}
	profile, ok, err := ResolvePackProfile(registry, result.ProfileRef)
	if err != nil || !ok {
		t.Fatalf("ResolvePackProfile: ok=%v err=%v", ok, err)
	}
	if profile.Description != "Review concrete changes" || profile.Type != "codex" || profile.Model != "gpt-5.4" || !strings.Contains(profile.AgentsContent, "Review concrete changes") {
		t.Fatalf("resolved profile = %+v", profile)
	}
}

func TestManagedLibraryRepairsLegacySameOriginRegistrationWithoutMetadata(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "managed-agent-packs", "local")
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(libraryRoot, registryPath, &fakeManagedContractRunner{})

	created, err := library.Create(context.Background(), ManagedProfileSpec{
		Name:           "reviewer",
		Type:           "codex",
		PermissionMode: "default",
		Instructions:   "Review concrete changes.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := registry.Packs[ManagedLocalPackNamespace]
	legacy.ManagedBy = ""
	legacy.DisplayName = ""
	registry.Packs[ManagedLocalPackNamespace] = legacy
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}

	snapshot, err := library.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot legacy same-origin registration: %v", err)
	}
	if snapshot.Revision != created.Revision || !reflect.DeepEqual(snapshot.Profiles, []string{"reviewer"}) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	repaired, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	pack := repaired.Packs[ManagedLocalPackNamespace]
	if pack.ManagedBy != ManagedPackOwnerAmux || pack.DisplayName != managedLocalPackName {
		t.Fatalf("repaired pack metadata = managed_by %q, display_name %q", pack.ManagedBy, pack.DisplayName)
	}
}

func TestManagedLibraryDoesNotTakeOverSameOriginOwnedByAnotherManager(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "managed-agent-packs", "local")
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(libraryRoot, registryPath, &fakeManagedContractRunner{})
	if _, err := library.Create(context.Background(), ManagedProfileSpec{
		Name:           "reviewer",
		Type:           "codex",
		PermissionMode: "default",
		Instructions:   "Review concrete changes.",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	foreign := registry.Packs[ManagedLocalPackNamespace]
	foreign.ManagedBy = "another-manager"
	registry.Packs[ManagedLocalPackNamespace] = foreign
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}

	if _, err := library.Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "already used by another source") {
		t.Fatalf("Snapshot error = %v, want ownership conflict", err)
	}
	unchanged, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := unchanged.Packs[ManagedLocalPackNamespace].ManagedBy; got != "another-manager" {
		t.Fatalf("managed_by = %q, want foreign owner preserved", got)
	}
}

func TestManagedLibraryCreateAddsAnotherProfileAtLatestRevision(t *testing.T) {
	root := t.TempDir()
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		&fakeManagedContractRunner{},
	)
	first, err := library.Create(context.Background(), ManagedProfileSpec{Name: "one", Type: "codex", PermissionMode: "default", Instructions: "one"})
	if err != nil {
		t.Fatalf("create one: %v", err)
	}
	second, err := library.Create(context.Background(), ManagedProfileSpec{Name: "two", Type: "claude", PermissionMode: "plan", Instructions: "two"})
	if err != nil {
		t.Fatalf("create two: %v", err)
	}
	if second.Revision == first.Revision {
		t.Fatal("second create did not publish a new revision")
	}
	snapshot, err := library.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := strings.Join(snapshot.Profiles, ","); got != "one,two" {
		t.Fatalf("profiles = %q, want one,two", got)
	}
}

func TestManagedLibraryUpdateInstructionsRequiresCurrentRevision(t *testing.T) {
	root := t.TempDir()
	runner := &fakeManagedContractRunner{}
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		runner,
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{
		Name: "reviewer", Type: "codex", Model: "gpt-5.4", PermissionMode: "plan", Instructions: "old",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := library.UpdateInstructions(context.Background(), "reviewer", "stale", "deadbeef"); err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatalf("stale update error = %v", err)
	}
	updated, err := library.UpdateInstructions(context.Background(), "reviewer", "new instructions", created.Revision)
	if err != nil {
		t.Fatalf("UpdateInstructions: %v", err)
	}
	if updated.Revision == created.Revision {
		t.Fatal("update did not publish a new revision")
	}
	registry, _ := LoadPackRegistry(filepath.Join(root, "agent-packs", "registry.json"))
	profile, ok, err := ResolvePackProfile(registry, "local/reviewer")
	if err != nil || !ok || !strings.Contains(profile.AgentsContent, "new instructions") {
		t.Fatalf("updated profile: ok=%v err=%v profile=%+v", ok, err, profile)
	}
	if profile.Type != "codex" || profile.Model != "gpt-5.4" {
		t.Fatalf("updated profile type/model = %s/%s, want existing codex/gpt-5.4", profile.Type, profile.Model)
	}
	if got, want := profile.DefaultArgs, []string{"-c", `approval_policy="on-request"`, "-c", `sandbox_mode="read-only"`}; !equalStrings(got, want) {
		t.Fatalf("updated profile permission args = %#v, want %#v", got, want)
	}
}

func TestManagedLibraryAuthoringPreviewDoesNotPublishAndPublishReplaysPatch(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), registryPath, &fakeManagedContractRunner{})
	created, err := library.Create(context.Background(), ManagedProfileSpec{
		Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "old instructions",
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := library.ReadAuthoringBase(context.Background(), ManagedAuthoringRequest{
		Operation: ManagedAuthoringUpdate, TargetRef: "local/reviewer",
	})
	if err != nil {
		t.Fatalf("ReadAuthoringBase: %v", err)
	}
	if base.BaseRevision != created.Revision || !authoringFileContains(base.Files, "instructions/common/reviewer/AGENTS.md", "old instructions") {
		t.Fatalf("base = %+v", base)
	}
	patch := strings.Join([]string{
		"diff --git a/instructions/common/reviewer/AGENTS.md b/instructions/common/reviewer/AGENTS.md",
		"index 02e2d47..63d8dbd 100644",
		"--- a/instructions/common/reviewer/AGENTS.md",
		"+++ b/instructions/common/reviewer/AGENTS.md",
		"@@ -1 +1 @@",
		"-old instructions",
		"+new instructions",
		"",
	}, "\n")
	request := ManagedAuthoringRequest{
		Operation: ManagedAuthoringUpdate, TargetRef: "local/reviewer", BaseRevision: base.BaseRevision, Patch: patch,
	}
	preview, err := library.PreviewAuthoring(context.Background(), request)
	if err != nil {
		t.Fatalf("PreviewAuthoring: %v", err)
	}
	if preview.Tree == "" || preview.PatchDigest == "" || !strings.Contains(preview.Diff, "+new instructions") {
		t.Fatalf("preview = %+v", preview)
	}
	afterPreview, err := library.Snapshot(context.Background())
	if err != nil || afterPreview.Revision != created.Revision {
		t.Fatalf("preview published state: snapshot=%+v err=%v", afterPreview, err)
	}
	published, err := library.PublishAuthoring(context.Background(), request, preview.Tree)
	if err != nil {
		t.Fatalf("PublishAuthoring: %v", err)
	}
	if published.Mutation.Revision == created.Revision || published.Tree != preview.Tree || published.PatchDigest != preview.PatchDigest {
		t.Fatalf("published = %+v", published)
	}
	expert, err := library.Expert(context.Background(), "reviewer")
	if err != nil || expert.Instructions != "new instructions" {
		t.Fatalf("expert = %+v err=%v", expert, err)
	}
}

func TestManagedLibraryAuthoringEvaluationUsesExactPreviewAndCleansUp(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		registryPath,
		&fakeManagedContractRunner{},
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Instructions: "old"})
	if err != nil {
		t.Fatal(err)
	}
	patch := "diff --git a/instructions/common/reviewer/AGENTS.md b/instructions/common/reviewer/AGENTS.md\n" +
		"--- a/instructions/common/reviewer/AGENTS.md\n+++ b/instructions/common/reviewer/AGENTS.md\n" +
		"@@ -1 +1 @@\n-old\n+candidate\n"
	preview, candidate, err := library.PrepareAuthoringEvaluation(context.Background(), ManagedAuthoringRequest{
		Operation: ManagedAuthoringUpdate, TargetRef: "local/reviewer", BaseRevision: created.Revision, Patch: patch,
	})
	if err != nil {
		t.Fatalf("PrepareAuthoringEvaluation: %v", err)
	}
	if candidate.Tree != preview.Tree || candidate.ProfileRef == "" || !strings.HasPrefix(candidate.ProfileRef, "local-eval-") {
		t.Fatalf("candidate = %+v, preview = %+v", candidate, preview)
	}
	if !candidate.ExpiresAt.After(time.Now()) {
		t.Fatalf("candidate expiry = %v, want future", candidate.ExpiresAt)
	}
	formal, err := ResolveProfile(registryPath, "local/reviewer")
	if err != nil || !strings.Contains(formal.AgentsContent, "old") {
		t.Fatalf("formal profile = %+v, err=%v", formal, err)
	}
	evaluated, err := ResolveProfile(registryPath, candidate.ProfileRef)
	if err != nil || !strings.Contains(evaluated.AgentsContent, "candidate") {
		t.Fatalf("evaluation profile = %+v, err=%v", evaluated, err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	pack := registry.Packs[candidate.Namespace]
	if !pack.Temporary || !pack.Evaluation || pack.DevelopmentOf != ManagedLocalPackNamespace || pack.ManagedBy != ManagedPackOwnerAmux {
		t.Fatalf("evaluation pack = %+v", pack)
	}
	refs, err := gitCommand(context.Background(), "", "--git-dir", library.originPath(), "for-each-ref", "--format=%(refname)", "refs/heads/local-eval-")
	if err != nil || strings.TrimSpace(refs) != "" {
		t.Fatalf("temporary evaluation branch leaked: refs=%q err=%v", refs, err)
	}
	materializedPath := pack.MaterializedPath
	if err := library.CleanupAuthoringEvaluation(candidate.ProfileRef); err != nil {
		t.Fatalf("CleanupAuthoringEvaluation: %v", err)
	}
	if _, err := ResolveProfile(registryPath, candidate.ProfileRef); !IsProfileNotFound(err) {
		t.Fatalf("resolve cleaned candidate error = %v, want profile not found", err)
	}
	if _, err := os.Stat(materializedPath); !os.IsNotExist(err) {
		t.Fatalf("candidate materialized path still exists: %v", err)
	}
	formal, err = ResolveProfile(registryPath, "local/reviewer")
	if err != nil || !strings.Contains(formal.AgentsContent, "old") {
		t.Fatalf("formal profile after cleanup = %+v, err=%v", formal, err)
	}
}

func TestManagedLibraryReconcilesExpiredAuthoringEvaluation(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), registryPath, &fakeManagedContractRunner{})
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Instructions: "old"})
	if err != nil {
		t.Fatal(err)
	}
	_, candidate, err := library.PrepareAuthoringEvaluation(context.Background(), ManagedAuthoringRequest{
		Operation: ManagedAuthoringUpdate, TargetRef: "local/reviewer", BaseRevision: created.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := library.ReconcileAuthoringEvaluations(candidate.ExpiresAt.Add(time.Second)); err != nil {
		t.Fatalf("ReconcileAuthoringEvaluations: %v", err)
	}
	if _, err := ResolveProfile(registryPath, candidate.ProfileRef); !IsProfileNotFound(err) {
		t.Fatalf("resolve expired candidate error = %v, want profile not found", err)
	}
}

func TestManagedLibraryPreparesCreateEvaluationBeforeLocalPackIsPublished(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), registryPath, &fakeManagedContractRunner{})
	base, err := library.ReadAuthoringBase(context.Background(), ManagedAuthoringRequest{
		Operation: ManagedAuthoringCreate, TargetRef: "local/new-expert",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, candidate, err := library.PrepareAuthoringEvaluation(context.Background(), ManagedAuthoringRequest{
		Operation: ManagedAuthoringCreate, TargetRef: "local/new-expert", BaseRevision: base.BaseRevision,
	})
	if err != nil {
		t.Fatalf("PrepareAuthoringEvaluation(create): %v", err)
	}
	if _, err := ResolveProfile(registryPath, candidate.ProfileRef); err != nil {
		t.Fatalf("resolve create candidate: %v", err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, published := registry.Packs[ManagedLocalPackNamespace]; published {
		t.Fatal("create evaluation published an empty/formal local pack")
	}
}

func TestManagedLibraryAuthoringRejectsWrongTreeAndUnsafePatch(t *testing.T) {
	root := t.TempDir()
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		&fakeManagedContractRunner{},
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Instructions: "old"})
	if err != nil {
		t.Fatal(err)
	}
	request := ManagedAuthoringRequest{
		Operation:    ManagedAuthoringUpdate,
		TargetRef:    "local/reviewer",
		BaseRevision: created.Revision,
		Patch:        "diff --git a/../escape b/../escape\nnew file mode 100644\n--- /dev/null\n+++ b/../escape\n@@ -0,0 +1 @@\n+bad\n",
	}
	if _, err := library.PreviewAuthoring(context.Background(), request); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe patch error = %v", err)
	}
	request.Patch = strings.Join([]string{
		"diff --git a/instructions/common/reviewer/AGENTS.md b/instructions/common/reviewer/AGENTS.md",
		"--- a/instructions/common/reviewer/AGENTS.md",
		"+++ b/instructions/common/reviewer/AGENTS.md",
		"@@ -1 +1 @@", "-old", "+new", "",
	}, "\n")
	if _, err := library.PublishAuthoring(context.Background(), request, strings.Repeat("0", 40)); err == nil || !strings.Contains(err.Error(), "tree") {
		t.Fatalf("wrong tree error = %v", err)
	}
}

func TestManagedLibraryAuthoringRejectsBareParentPatchPath(t *testing.T) {
	root := t.TempDir()
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		&fakeManagedContractRunner{},
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Instructions: "old"})
	if err != nil {
		t.Fatal(err)
	}
	request := ManagedAuthoringRequest{
		Operation:    ManagedAuthoringUpdate,
		TargetRef:    "local/reviewer",
		BaseRevision: created.Revision,
		Patch:        "diff --git a/.. b/..\nnew file mode 100644\n--- /dev/null\n+++ b/..\n@@ -0,0 +1 @@\n+bad\n",
	}
	if _, err := library.PreviewAuthoring(context.Background(), request); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("bare parent patch error = %v", err)
	}
}

func TestManagedAuthoringTreeRejectsSymlinkWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := validateManagedAuthoringTree(root); err == nil || !strings.Contains(err.Error(), "unsupported symlink") {
		t.Fatalf("symlink validation error = %v", err)
	}
}

func TestManagedLibraryAuthoringRejectsNULInTextPatch(t *testing.T) {
	root := t.TempDir()
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		&fakeManagedContractRunner{},
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Instructions: "old"})
	if err != nil {
		t.Fatal(err)
	}
	request := ManagedAuthoringRequest{
		Operation:    ManagedAuthoringUpdate,
		TargetRef:    "local/reviewer",
		BaseRevision: created.Revision,
		Patch: "diff --git a/instructions/common/reviewer/AGENTS.md b/instructions/common/reviewer/AGENTS.md\n" +
			"--- a/instructions/common/reviewer/AGENTS.md\n+++ b/instructions/common/reviewer/AGENTS.md\n" +
			"@@ -1 +1 @@\n-old\n+new\x00content\n",
	}
	if _, err := library.PreviewAuthoring(context.Background(), request); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("NUL patch error = %v", err)
	}
}

func TestManagedLibraryAuthoringRejectsDeletingTargetExpert(t *testing.T) {
	root := t.TempDir()
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		&fakeManagedContractRunner{},
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Instructions: "old"})
	if err != nil {
		t.Fatal(err)
	}
	base, err := library.ReadAuthoringBase(context.Background(), ManagedAuthoringRequest{
		Operation: ManagedAuthoringUpdate, TargetRef: "local/reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	var profileContent string
	for _, file := range base.Files {
		if file.Path == "profiles/common/reviewer.toml" {
			profileContent = file.Content
			break
		}
	}
	if profileContent == "" {
		t.Fatal("prepared authoring base omitted target profile")
	}
	lines := strings.Split(strings.TrimSuffix(profileContent, "\n"), "\n")
	for i := range lines {
		lines[i] = "-" + lines[i]
	}
	patch := strings.Join([]string{
		"diff --git a/profiles/common/reviewer.toml b/profiles/common/reviewer.toml",
		"deleted file mode 100644",
		"--- a/profiles/common/reviewer.toml",
		"+++ /dev/null",
		fmt.Sprintf("@@ -1,%d +0,0 @@", len(lines)),
		strings.Join(lines, "\n"),
		"",
	}, "\n")
	_, err = library.PreviewAuthoring(context.Background(), ManagedAuthoringRequest{
		Operation: ManagedAuthoringUpdate, TargetRef: "local/reviewer", BaseRevision: created.Revision, Patch: patch,
	})
	if err == nil || !strings.Contains(err.Error(), "preserve target expert") {
		t.Fatalf("delete target error = %v", err)
	}
}

func authoringFileContains(files []ManagedAuthoringFile, path, fragment string) bool {
	for _, file := range files {
		if file.Path == path && strings.Contains(file.Content, fragment) {
			return true
		}
	}
	return false
}

func authoringHasPathPrefix(files []ManagedAuthoringFile, prefix string) bool {
	for _, file := range files {
		if strings.HasPrefix(file.Path, prefix) {
			return true
		}
	}
	return false
}

func TestManagedLibraryExpertReadsPublishedInstructions(t *testing.T) {
	root := t.TempDir()
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		&fakeManagedContractRunner{},
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "old"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := library.UpdateInstructions(context.Background(), "reviewer", "new instructions", created.Revision)
	if err != nil {
		t.Fatalf("UpdateInstructions: %v", err)
	}
	registry, err := LoadPackRegistry(filepath.Join(root, "agent-packs", "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	materializedInstructions := filepath.Join(registry.Packs[ManagedLocalPackNamespace].MaterializedPath, "instructions", "common", "reviewer", "AGENTS.md")
	if err := os.WriteFile(materializedInstructions, []byte("stale materialized copy\n"), 0600); err != nil {
		t.Fatalf("write stale materialized instructions: %v", err)
	}
	expert, err := library.Expert(context.Background(), "reviewer")
	if err != nil {
		t.Fatalf("Expert: %v", err)
	}
	if expert.Name != "reviewer" || expert.Revision != updated.Revision || expert.Instructions != "new instructions" {
		t.Fatalf("Expert = %+v", expert)
	}
}

func TestManagedLibraryNoopUpdateKeepsProfileRef(t *testing.T) {
	root := t.TempDir()
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		&fakeManagedContractRunner{},
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "same\n"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := library.UpdateInstructions(context.Background(), "reviewer", "same\n", created.Revision)
	if err != nil {
		t.Fatalf("UpdateInstructions: %v", err)
	}
	if updated.ProfileRef != "local/reviewer" || updated.Revision != created.Revision {
		t.Fatalf("no-op update = %+v", updated)
	}
}

func TestManagedLibraryReregistersAfterPackRemoval(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		registryPath,
		&fakeManagedContractRunner{},
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "one", Type: "codex", PermissionMode: "default", Instructions: "one"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	delete(registry.Packs, ManagedLocalPackNamespace)
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	updated, err := library.UpdateInstructions(context.Background(), "one", "updated", created.Revision)
	if err != nil {
		t.Fatalf("UpdateInstructions: %v", err)
	}
	registry, err = LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if registry.Packs[ManagedLocalPackNamespace].ManagedBy != ManagedPackOwnerAmux || updated.Revision == created.Revision {
		t.Fatalf("re-registered pack = %+v, update = %+v", registry.Packs[ManagedLocalPackNamespace], updated)
	}
}

func TestManagedLocalPackRejectsGenericAddRemoveAndRollback(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), registryPath, &fakeManagedContractRunner{})
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddGitPackWithOptions(context.Background(), registryPath, ManagedLocalPackNamespace, filepath.Join(root, "other.git"), AddGitPackOptions{Ref: "main", AutoSync: true, SyncInterval: defaultPackSyncInterval}); err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("generic add error = %v", err)
	}
	if _, err := RemovePack(registryPath, ManagedLocalPackNamespace, RemovePackOptions{}); err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("generic remove error = %v", err)
	}
	if _, err := RollbackPack(context.Background(), registryPath, ManagedLocalPackNamespace, created.Revision); err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("generic rollback error = %v", err)
	}
	if _, err := SyncPack(context.Background(), registryPath, ManagedLocalPackNamespace); err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("generic sync error = %v", err)
	}
}

func TestManagedLibraryContractFailureKeepsPublishedRevision(t *testing.T) {
	root := t.TempDir()
	runner := &fakeManagedContractRunner{}
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		filepath.Join(root, "agent-packs", "registry.json"),
		runner,
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "old"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runner.failCheck = true
	if _, err := library.UpdateInstructions(context.Background(), "reviewer", "invalid", created.Revision); err == nil || !strings.Contains(err.Error(), "contract rejected source") {
		t.Fatalf("failed update error = %v", err)
	}
	snapshot, err := library.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != created.Revision {
		t.Fatalf("revision = %s, want %s", snapshot.Revision, created.Revision)
	}
}

func TestManagedLibraryPublishFailureRollsBackAllPublishedState(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	runner := &fakeManagedContractRunner{}
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		registryPath,
		runner,
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "old"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runner.invalidManifest = true
	if _, err := library.UpdateInstructions(context.Background(), "reviewer", "candidate", created.Revision); err == nil {
		t.Fatal("UpdateInstructions succeeded with an invalid published manifest")
	}
	snapshot, err := library.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != created.Revision {
		t.Fatalf("formal revision = %s, want %s", snapshot.Revision, created.Revision)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	pack := registry.Packs[ManagedLocalPackNamespace]
	if pack.CurrentRevision != created.Revision {
		t.Fatalf("registry revision = %s, want %s", pack.CurrentRevision, created.Revision)
	}
	profile, ok, err := ResolvePackProfile(registry, "local/reviewer")
	if err != nil || !ok || !strings.Contains(profile.AgentsContent, "old") || strings.Contains(profile.AgentsContent, "candidate") {
		t.Fatalf("materialized profile after rollback: ok=%v err=%v profile=%+v", ok, err, profile)
	}
}

func TestManagedLibraryDoesNotDeleteUnknownIncompleteRoot(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "managed-agent-packs", "local")
	if err := os.MkdirAll(libraryRoot, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(libraryRoot, "keep-me")
	if err := os.WriteFile(sentinel, []byte("unknown state"), 0600); err != nil {
		t.Fatal(err)
	}
	library := NewManagedLibrary(libraryRoot, filepath.Join(root, "agent-packs", "registry.json"), &fakeManagedContractRunner{})
	if _, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "review"}); err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("Create error = %v", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "unknown state" {
		t.Fatalf("sentinel changed: data=%q err=%v", data, err)
	}
}

func TestManagedLibraryPublishRevalidatesRegistryIdentity(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	runner := &fakeManagedContractRunner{}
	library := NewManagedLibrary(
		filepath.Join(root, "managed-agent-packs", "local"),
		registryPath,
		runner,
	)
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "old"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	runner.checkHook = func() error {
		return SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{
			ManagedLocalPackNamespace: {
				Namespace:        ManagedLocalPackNamespace,
				SourceKind:       PackSourceGit,
				GitURL:           filepath.Join(root, "other.git"),
				MaterializedPath: filepath.Join(root, "other", "repo"),
			},
		}})
	}
	if _, err := library.UpdateInstructions(context.Background(), "reviewer", "candidate", created.Revision); err == nil || !strings.Contains(err.Error(), "already used by another source") {
		t.Fatalf("UpdateInstructions error = %v", err)
	}
	revision, err := library.currentRevision(context.Background())
	if err != nil {
		t.Fatalf("currentRevision: %v", err)
	}
	if revision != created.Revision {
		t.Fatalf("formal revision = %s, want %s", revision, created.Revision)
	}
}

func TestManagedLibraryEnsurePreparesEmptyGitWithoutRegisteringPack(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), registryPath, &fakeManagedContractRunner{})
	if err := library.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	snapshot, err := library.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !snapshot.Initialized || snapshot.Revision == "" || len(snapshot.Profiles) != 0 {
		t.Fatalf("empty snapshot = %+v", snapshot)
	}
	firstRevision := snapshot.Revision
	if err := library.Ensure(context.Background()); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	snapshot, err = library.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot after second Ensure: %v", err)
	}
	if snapshot.Revision != firstRevision {
		t.Fatalf("second Ensure changed local history from %s to %s", firstRevision, snapshot.Revision)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if _, ok := registry.Packs[ManagedLocalPackNamespace]; ok {
		t.Fatalf("empty local library registered before first profile: %+v", registry.Packs[ManagedLocalPackNamespace])
	}
}

func TestManagedLibraryHistoryAndRestoreCreateAuditableRevision(t *testing.T) {
	root := t.TempDir()
	library := NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), filepath.Join(root, "agent-packs", "registry.json"), &fakeManagedContractRunner{})
	created, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "old"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := library.UpdateInstructions(context.Background(), "reviewer", "new", created.Revision)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := library.Restore(context.Background(), created.Revision, updated.Revision)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.Revision == created.Revision || restored.Revision == updated.Revision {
		t.Fatalf("restore revision = %q, want a new audit commit", restored.Revision)
	}
	expert, err := library.Expert(context.Background(), "reviewer")
	if err != nil || expert.Instructions != "old" {
		t.Fatalf("restored expert = %+v err=%v", expert, err)
	}
	history, err := library.History(context.Background(), 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) < 3 || history[0].Revision != restored.Revision || !strings.Contains(history[0].Subject, "restore") {
		t.Fatalf("history = %+v", history)
	}
}

func TestManagedLibraryUpdatePreservesLegacyMaterializedRevision(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "agent-packs", "registry.json")
	library := NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), registryPath, &fakeManagedContractRunner{})
	created, err := library.Create(context.Background(), ManagedProfileSpec{
		Name: "reviewer", Type: "codex", PermissionMode: "default", Instructions: "old",
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	pack := registry.Packs[ManagedLocalPackNamespace]
	legacyPath := packMaterializedPath(registryPath, ManagedLocalPackNamespace)
	immutableRoot := filepath.Dir(pack.MaterializedPath)
	if err := os.Rename(pack.MaterializedPath, legacyPath); err != nil {
		t.Fatalf("simulate legacy materialized checkout: %v", err)
	}
	if err := os.Remove(immutableRoot); err != nil {
		t.Fatalf("remove empty immutable revision root: %v", err)
	}
	pack.MaterializedPath = legacyPath
	registry.Packs[ManagedLocalPackNamespace] = pack
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}

	updated, err := library.UpdateInstructions(context.Background(), "reviewer", "new", created.Revision)
	if err != nil {
		t.Fatalf("UpdateInstructions: %v", err)
	}
	previousPath := managedRevisionMaterializedPath(registryPath, created.Revision)
	if _, err := os.Stat(filepath.Join(previousPath, "dist", "agent-pack.manifest.json")); err != nil {
		t.Fatalf("previous immutable revision: %v", err)
	}
	registry, err = LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	wantCurrentPath := managedRevisionMaterializedPath(registryPath, updated.Revision)
	if registry.Packs[ManagedLocalPackNamespace].MaterializedPath != wantCurrentPath {
		t.Fatalf("current materialized path = %q, want %q", registry.Packs[ManagedLocalPackNamespace].MaterializedPath, wantCurrentPath)
	}
	oldProfile := Profile{
		Namespace: ManagedLocalPackNamespace, Name: "reviewer", Revision: created.Revision,
		Type: "codex", Source: SourceLocal, Skills: []string{"example"},
	}
	oldProfile, err = AttachLocalPackPath(oldProfile, registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if oldProfile.PackPath != previousPath {
		t.Fatalf("legacy snapshot PackPath = %q, want %q", oldProfile.PackPath, previousPath)
	}
}
