package agentprofile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedSourceRejectsOldVersionWithoutRewriting(t *testing.T) {
	root := t.TempDir()
	data := []byte("schema_version = \"v1.1\"\nnamespace = \"local\"\n")
	path := filepath.Join(root, "agent-pack.toml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateManagedPackSource(root); err == nil || !strings.Contains(err.Error(), "supported: v2") {
		t.Fatalf("old source: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatalf("source changed: %q, %v", after, err)
	}
}

// Run against a released compiler without downloading or installing user tools.
func TestManagedV2RealContract(t *testing.T) {
	binary := os.Getenv("AMUX_TEST_CONTRACT_BIN")
	if binary == "" {
		t.Skip("set AMUX_TEST_CONTRACT_BIN to exercise the released compiler")
	}
	root := t.TempDir()
	registryPath := filepath.Join(root, "registry.json")
	library := NewManagedLibrary(filepath.Join(root, "local"), registryPath, ExecManagedContractRunner{Command: binary})
	ctx := context.Background()
	created, err := library.Create(ctx, ManagedProfileSpec{Name: "reviewer", Instructions: "Review changes"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := library.UpdateInstructions(ctx, "reviewer", "Review updated changes", created.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := library.ForkBuiltin(ctx, "builtin/codex", "forked"); err != nil {
		t.Fatal(err)
	}
	base, err := library.ReadAuthoringBase(ctx, ManagedAuthoringRequest{Operation: ManagedAuthoringCreate, TargetRef: "local/authored"})
	if err != nil {
		t.Fatal(err)
	}
	request := ManagedAuthoringRequest{Operation: ManagedAuthoringCreate, TargetRef: "local/authored", BaseRevision: base.BaseRevision}
	preview, err := library.PreviewAuthoring(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	_, candidate, err := library.PrepareAuthoringEvaluation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := library.CleanupAuthoringEvaluation(candidate.ProfileRef); err != nil {
		t.Fatal(err)
	}
	if _, err := library.PublishAuthoring(ctx, request, preview.Tree); err != nil {
		t.Fatal(err)
	}
	registry, err := LoadPackRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	pack := registry.Packs[ManagedLocalPackNamespace]
	manifest, err := LoadPackManifest(packManifestPath(pack))
	if err != nil || manifest.SchemaVersion != "v2" || len(manifest.Profiles) != 3 {
		t.Fatalf("manifest: %+v, %v", manifest, err)
	}
	for _, name := range []string{"reviewer", "forked", "authored"} {
		profile, ok, err := ResolvePackProfile(registry, "local/"+name)
		if err != nil || !ok {
			t.Fatalf("%s: %v", name, err)
		}
		if profile.Type != "" || profile.Model != "" || profile.Provider != nil || len(profile.DefaultArgs) != 0 {
			t.Fatalf("execution leaked: %+v", profile)
		}
		for _, engine := range []string{"codex", "claude"} {
			if _, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Runtime: engine}); err != nil {
				t.Fatalf("%s/%s: %v", name, engine, err)
			}
		}
	}
}
