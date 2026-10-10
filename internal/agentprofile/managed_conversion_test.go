package agentprofile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func legacyManagedLibrary(t *testing.T, empty bool, runner ManagedContractRunner) (*ManagedLibrary, string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "old-source")
	writePackContent(t, repo, "agent-pack.toml", "schema_version = \"v1.1\"\nname = \"local\"\nnamespace = \"local\"\ndescription = \"local\"\nowners = []\n")
	if !empty {
		writePackContent(t, repo, "profiles/common/reviewer.toml", `description = "Review changes"
type = "codex"
model = "old/model"
providers = ["old/model"]
supported_models = []
instructions = ["instructions/common/reviewer/AGENTS.md"]
skills = []
subagents = []
envs = ["REVIEW_TOKEN"]
[permission]
permission_mode = "plan"
`)
		writePackContent(t, repo, "instructions/common/reviewer/AGENTS.md", "Review changes carefully.\n")
		writePackContent(t, repo, "providers/old.toml", "[models.model]\nplatform = \"openai-compat\"\n")
		writeManifest(t, repo, `{"schema_version":"v1.1","pack_namespace":"local","profiles":{"reviewer":{"type":"codex","model":"old/model","instructions":["instructions/common/reviewer/AGENTS.md"]}},"providers":{}}`)
	}
	runPackGit(t, repo, "init", "-b", "main")
	runPackGit(t, repo, "config", "user.name", "amux test")
	runPackGit(t, repo, "config", "user.email", "amux@example.invalid")
	runPackGit(t, repo, "add", "--all")
	runPackGit(t, repo, "commit", "-m", "old local source")
	revision := packGitRev(t, repo)
	library := NewManagedLibrary(filepath.Join(root, "managed-agent-packs", "local"), filepath.Join(root, "agent-packs", "registry.json"), runner)
	if err := os.MkdirAll(library.root, 0700); err != nil {
		t.Fatal(err)
	}
	runPackGit(t, "", "clone", "--bare", repo, library.originPath())
	materialized := filepath.Join(root, "agent-packs", "local", "repo")
	if !empty {
		runPackGit(t, "", "clone", repo, materialized)
		pack := library.packForMaterializedRevision(materialized, revision)
		if err := SavePackRegistry(library.registryPath, PackRegistry{Packs: map[string]Pack{"local": pack}}); err != nil {
			t.Fatal(err)
		}
	}
	return library, revision, materialized
}

func TestManagedConvertV2PublishesAndPreservesHistory(t *testing.T) {
	var runner ManagedContractRunner = &fakeManagedContractRunner{}
	if binary := os.Getenv("AMUX_TEST_CONTRACT_BIN"); binary != "" {
		runner = ExecManagedContractRunner{Command: binary}
	}
	library, before, legacyPath := legacyManagedLibrary(t, false, runner)
	legacySource, err := os.ReadFile(filepath.Join(legacyPath, "profiles/common/reviewer.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Maintenance repair must remain available when ordinary authoring is gated.
	library.requireAuthoringReady = func() error { t.Fatal("conversion consulted the authoring gate"); return nil }
	result, err := library.ConvertV2(context.Background(), before)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision == before || result.ProfileRef != "local" {
		t.Fatalf("conversion = %+v", result)
	}
	registry, err := LoadPackRegistry(library.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	pack := registry.Packs["local"]
	if pack.CurrentRevision != result.Revision || pack.MaterializedPath == legacyPath {
		t.Fatalf("published pack = %+v", pack)
	}
	var old, current map[string]any
	if err := toml.Unmarshal(legacySource, &old); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(pack.MaterializedPath, "profiles/common/reviewer.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(data, &current); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "model", "providers", "supported_models", "permission"} {
		delete(old, key)
	}
	if !reflect.DeepEqual(old, current) {
		t.Fatalf("content changed: old=%#v new=%#v", old, current)
	}
	if _, err := os.Stat(filepath.Join(pack.MaterializedPath, "providers")); !os.IsNotExist(err) {
		t.Fatalf("provider source remains: %v", err)
	}
	for _, path := range []string{legacyPath, managedRevisionMaterializedPath(library.registryPath, before)} {
		data, err := os.ReadFile(filepath.Join(path, "profiles/common/reviewer.toml"))
		if err != nil || string(data) != string(legacySource) || packGitRev(t, path) != before {
			t.Fatalf("historical source changed at %s: %v", path, err)
		}
	}
	parent, err := gitCommand(context.Background(), "", "--git-dir", library.originPath(), "rev-parse", "main^")
	if err != nil || strings.TrimSpace(parent) != before {
		t.Fatalf("parent = %s, %v", parent, err)
	}
	profile, ok, err := ResolvePackProfile(registry, "local/reviewer")
	if err != nil || !ok || profile.Type != "" || profile.Model != "" || profile.AgentsContent != "Review changes carefully.\n" {
		t.Fatalf("converted profile = %+v, %v", profile, err)
	}
	for _, runtime := range []string{"codex", "claude"} {
		if _, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: runtime}); err != nil {
			t.Fatalf("projection %s: %v", runtime, err)
		}
	}
	again, err := library.ConvertV2(context.Background(), result.Revision)
	if err != nil || again != result {
		t.Fatalf("repeat conversion = %+v, %v", again, err)
	}
}

func TestManagedConvertV2DoesNotRepairRegistryBeforePublishing(t *testing.T) {
	for _, failure := range []string{"revision", "contract", "publish", "v2"} {
		t.Run(failure, func(t *testing.T) {
			runner := &fakeManagedContractRunner{}
			library, before, _ := legacyManagedLibrary(t, false, runner)
			if failure == "v2" {
				result, err := library.ConvertV2(context.Background(), before)
				if err != nil {
					t.Fatal(err)
				}
				before = result.Revision
			}
			registry, err := LoadPackRegistry(library.registryPath)
			if err != nil {
				t.Fatal(err)
			}
			pack := registry.Packs[ManagedLocalPackNamespace]
			pack.ManagedBy = ""
			pack.DisplayName = ""
			registry.Packs[ManagedLocalPackNamespace] = pack
			if err := SavePackRegistry(library.registryPath, registry); err != nil {
				t.Fatal(err)
			}
			registryBefore, err := os.ReadFile(library.registryPath)
			if err != nil {
				t.Fatal(err)
			}
			expected := before
			switch failure {
			case "revision":
				expected = strings.Repeat("0", 40)
			case "contract":
				runner.failCheck = true
			case "publish":
				if err := os.WriteFile(filepath.Join(filepath.Dir(library.registryPath), "local", "revisions"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = library.ConvertV2(context.Background(), expected)
			if (err == nil) != (failure == "v2") {
				t.Fatalf("conversion error = %v", err)
			}
			after, err := library.currentRevision(context.Background())
			if err != nil || after != before {
				t.Fatalf("formal revision = %s, %v; want %s", after, err, before)
			}
			registryAfter, err := os.ReadFile(library.registryPath)
			if err != nil || string(registryBefore) != string(registryAfter) {
				t.Fatalf("conversion modified registry before publishing: %v", err)
			}
		})
	}
}

func TestManagedConvertV2RejectsDanglingProfilesSymlink(t *testing.T) {
	library, before, _ := legacyManagedLibrary(t, true, &fakeManagedContractRunner{})
	work := filepath.Join(t.TempDir(), "repo")
	runPackGit(t, "", "clone", library.originPath(), work)
	runPackGit(t, work, "config", "user.name", "amux test")
	runPackGit(t, work, "config", "user.email", "amux@example.invalid")
	if err := os.Symlink("missing", filepath.Join(work, "profiles")); err != nil {
		t.Fatal(err)
	}
	runPackGit(t, work, "add", "--all")
	runPackGit(t, work, "commit", "-m", "dangling profiles symlink")
	runPackGit(t, work, "push", "origin", "main")
	before = packGitRev(t, work)
	if _, err := library.ConvertV2(context.Background(), before); err == nil || !strings.Contains(err.Error(), "regular files and directories") {
		t.Fatalf("dangling profiles symlink conversion error = %v", err)
	}
	after, err := library.currentRevision(context.Background())
	if err != nil || after != before {
		t.Fatalf("formal revision = %s, %v; want %s", after, err, before)
	}
}

func TestManagedConvertV2EmptyPublicationRechecksRegistry(t *testing.T) {
	library, before, _ := legacyManagedLibrary(t, true, &fakeManagedContractRunner{})
	var registryBefore []byte
	_, err := library.mutateExistingSource(context.Background(), before, "", func(root string) error {
		if err := convertManagedV2Source(root); err != nil {
			return err
		}
		// Generic Pack registration owns a different lock and can occur while
		// conversion is preparing its candidate source.
		if err := SavePackRegistry(library.registryPath, PackRegistry{Packs: map[string]Pack{
			ManagedLocalPackNamespace: {
				Namespace: ManagedLocalPackNamespace, SourceKind: PackSourceGit,
				GitURL: filepath.Join(t.TempDir(), "other.git"),
			},
		}}); err != nil {
			return err
		}
		var err error
		registryBefore, err = os.ReadFile(library.registryPath)
		return err
	}, "convert empty source", true)
	if err == nil {
		t.Fatal("conversion published after another source registered the local namespace")
	}
	after, err := library.currentRevision(context.Background())
	if err != nil || after != before {
		t.Fatalf("formal revision = %s, %v; want %s", after, err, before)
	}
	registryAfter, err := os.ReadFile(library.registryPath)
	if err != nil || string(registryBefore) != string(registryAfter) {
		t.Fatalf("concurrent registration was modified: %v", err)
	}
}

func TestManagedConvertV2FailuresKeepFormalState(t *testing.T) {
	for _, failure := range []string{"revision", "contract", "manifest", "publish", "schema", "malformed", "symlink"} {
		t.Run(failure, func(t *testing.T) {
			runner := &fakeManagedContractRunner{}
			library, before, _ := legacyManagedLibrary(t, false, runner)
			expected := before
			switch failure {
			case "revision":
				expected = strings.Repeat("0", 40)
			case "contract":
				runner.failCheck = true
			case "manifest":
				runner.invalidManifest = true
			case "publish":
				// Fail after the formal branch CAS; publication must roll it back.
				if err := os.WriteFile(filepath.Join(filepath.Dir(library.registryPath), "local", "revisions"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				work := filepath.Join(t.TempDir(), "repo")
				runPackGit(t, "", "clone", library.originPath(), work)
				runPackGit(t, work, "config", "user.name", "amux test")
				runPackGit(t, work, "config", "user.email", "amux@example.invalid")
				if failure == "schema" {
					writePackContent(t, work, "agent-pack.toml", "schema_version = \"v99\"\n")
				} else if failure == "malformed" {
					writePackContent(t, work, "profiles/common/reviewer.toml", "description = [")
				} else {
					path := filepath.Join(work, "profiles/common/reviewer.toml")
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("../../agent-pack.toml", path); err != nil {
						t.Fatal(err)
					}
				}
				runPackGit(t, work, "add", "--all")
				runPackGit(t, work, "commit", "-m", "invalid source")
				runPackGit(t, work, "push", "origin", "main")
				before = packGitRev(t, work)
				expected = before
			}
			registryBefore, err := os.ReadFile(library.registryPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := library.ConvertV2(context.Background(), expected); err == nil {
				t.Fatal("conversion succeeded")
			}
			after, err := library.currentRevision(context.Background())
			if err != nil || after != before {
				t.Fatalf("formal revision = %s, %v; want %s", after, err, before)
			}
			registryAfter, err := os.ReadFile(library.registryPath)
			if err != nil || string(registryBefore) != string(registryAfter) {
				t.Fatalf("registry changed: %v", err)
			}
		})
	}
}

func TestManagedConvertV2EmptyLibraryStaysUnregistered(t *testing.T) {
	library, before, _ := legacyManagedLibrary(t, true, &fakeManagedContractRunner{failCheck: true})
	result, err := library.ConvertV2(context.Background(), before)
	if err != nil || result.Revision == before {
		t.Fatalf("empty conversion = %+v, %v", result, err)
	}
	registry, err := LoadPackRegistry(library.registryPath)
	if err != nil || len(registry.Packs) != 0 {
		t.Fatalf("empty library was registered: %+v, %v", registry, err)
	}
	if _, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Instructions: "Review changes"}); err == nil {
		t.Fatal("creation bypassed the failing compiler")
	}
	library.contract = &fakeManagedContractRunner{}
	if _, err := library.Create(context.Background(), ManagedProfileSpec{Name: "reviewer", Instructions: "Review changes"}); err != nil {
		t.Fatalf("create after conversion: %v", err)
	}
}

func TestManagedConvertV2RequiresRevisionAndExistingLibrary(t *testing.T) {
	library := NewManagedLibrary(filepath.Join(t.TempDir(), "local"), filepath.Join(t.TempDir(), "registry.json"), &fakeManagedContractRunner{})
	for _, revision := range []string{"", "short", strings.Repeat("0", 40)} {
		if _, err := library.ConvertV2(context.Background(), revision); err == nil {
			t.Fatalf("accepted revision %q without a library", revision)
		}
	}
}

func TestManagedV2SourceConversionPreservesContentSemantics(t *testing.T) {
	root := t.TempDir()
	writePackContent(t, root, "agent-pack.toml", "schema_version = \"v1.1\"\nnamespace = \"local\"\nproviders = {}\n")
	for _, namespace := range []string{"common", "team"} {
		for _, instructions := range []string{"", "instructions = []\n"} {
			name := "inherited"
			if instructions != "" {
				name = "empty"
			}
			path := filepath.Join(root, "profiles", namespace, name+".toml")
			writePackContent(t, root, filepath.ToSlash(filepath.Join("profiles", namespace, name+".toml")), "description = \"审阅 / Review\"\nextends = \"base\"\nskills = []\n"+instructions+"\"type\" = \"\"\nmodel = \"\"\nproviders = []\nsupported_models = []\npermission = {}\n")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := toml.Unmarshal(before, &want); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"type", "model", "providers", "supported_models", "permission"} {
				delete(want, key)
			}
			t.Cleanup(func() {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]any
				if err := toml.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: content changed: %#v, want %#v", path, got, want)
				}
			})
		}
	}
	writePackContent(t, root, "skills/custom/review/SKILL.md", "Review content unchanged.\n")
	if err := convertManagedV2Source(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "skills/custom/review/SKILL.md"))
	if err != nil || string(data) != "Review content unchanged.\n" {
		t.Fatalf("skill changed: %q, %v", data, err)
	}
}

func TestManagedConvertV2RealContractPreservesInheritance(t *testing.T) {
	binary := os.Getenv("AMUX_TEST_CONTRACT_BIN")
	if binary == "" {
		t.Skip("set AMUX_TEST_CONTRACT_BIN to exercise the released compiler")
	}
	library, _, _ := legacyManagedLibrary(t, false, ExecManagedContractRunner{Command: binary})
	work := filepath.Join(t.TempDir(), "repo")
	runPackGit(t, "", "clone", library.originPath(), work)
	runPackGit(t, work, "config", "user.name", "amux test")
	runPackGit(t, work, "config", "user.email", "amux@example.invalid")
	writePackContent(t, work, "profiles/common/base.toml", `description = "Base content"
type = "codex"
model = "old/model"
instructions = ["instructions/common/base/AGENTS.md"]
skills = ["review"]
subagents = []
envs = ["REVIEW_TOKEN"]
[permission]
permission_mode = "plan"
`)
	writePackContent(t, work, "instructions/common/base/AGENTS.md", "Review inherited changes carefully.\n")
	writePackContent(t, work, "profiles/common/reviewer.toml", "description = \"审阅 / Review\"\nextends = \"base\"\n")
	writePackContent(t, work, "profiles/common/empty.toml", "description = \"Empty content\"\nextends = \"base\"\ninstructions = []\nskills = []\nsubagents = []\nenvs = []\n")
	const skill = "---\nname: review\ndescription: Review changes\n---\n\nReview the complete change.\n"
	writePackContent(t, work, "skills/custom/review/SKILL.md", skill)
	writePackContent(t, work, "skills/custom/review/source.toml", "source_kind = \"custom\"\nasset_type = \"skill\"\n[origin]\nkind = \"local\"\n")
	runPackGit(t, work, "add", "--all")
	runPackGit(t, work, "commit", "-m", "legacy inherited content")
	runPackGit(t, work, "push", "origin", "main")
	before := packGitRev(t, work)
	registry, err := LoadPackRegistry(library.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	pack := registry.Packs[ManagedLocalPackNamespace]
	pack.CurrentRevision = before
	pack.ManagedBy = ""
	pack.DisplayName = ""
	registry.Packs[ManagedLocalPackNamespace] = pack
	if err := SavePackRegistry(library.registryPath, registry); err != nil {
		t.Fatal(err)
	}
	if _, err := library.ConvertV2(context.Background(), before); err != nil {
		t.Fatal(err)
	}
	registry, err = LoadPackRegistry(library.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	pack = registry.Packs[ManagedLocalPackNamespace]
	if pack.ManagedBy != ManagedPackOwnerAmux || pack.DisplayName != managedLocalPackName {
		t.Fatalf("publication did not repair legacy metadata: %+v", pack)
	}
	manifest, err := LoadPackManifest(packManifestPath(pack))
	if err != nil {
		t.Fatal(err)
	}
	inherited := manifest.Profiles["reviewer"]
	if inherited.Description != "审阅 / Review" || !reflect.DeepEqual(inherited.Instructions, []string{"instructions/common/base/AGENTS.md"}) || !reflect.DeepEqual(inherited.Skills, []string{"review"}) || !reflect.DeepEqual(inherited.Envs, []string{"REVIEW_TOKEN"}) {
		t.Fatalf("inherited content changed: %+v", inherited)
	}
	empty, ok := manifest.Profiles["empty"]
	if !ok || len(empty.Instructions) != 0 || len(empty.Skills) != 0 || len(empty.Envs) != 0 {
		t.Fatalf("explicit empty lists inherited content: %+v", empty)
	}
	data, err := os.ReadFile(filepath.Join(pack.MaterializedPath, "skills/custom/review/SKILL.md"))
	if err != nil || string(data) != skill {
		t.Fatalf("skill changed: %q, %v", data, err)
	}
}
