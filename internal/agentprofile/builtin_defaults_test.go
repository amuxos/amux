package agentprofile

import (
	"io/fs"
	"slices"
	"testing"
)

func TestPublicBuiltinProfilesUseNativeProviders(t *testing.T) {
	profiles := Builtins()
	if len(profiles) != 3 {
		t.Fatalf("public builtin count = %d, want 3", len(profiles))
	}
	for _, profile := range profiles {
		if !slices.Contains([]string{"claude", "codex", "pi"}, profile.Type) || profile.Name != profile.Type {
			t.Fatalf("unexpected public builtin %s (%s)", profile.Ref(), profile.Type)
		}
		if profile.Provider != nil || len(profile.DefaultEnv) != 0 || len(profile.EnvRefs) != 0 || len(profile.Envs) != 0 || len(profile.Skills) != 0 {
			t.Fatalf("public builtin %s has unreviewed provider, credential or skill bindings", profile.Ref())
		}
	}
	if profile, ok := ResolveBuiltin(DefaultProfileRef); !ok || profile.Type != "codex" {
		t.Fatalf("public default profile %q does not resolve to Codex", DefaultProfileRef)
	}
	if got := CatalogPacks(); len(got) != 0 {
		t.Fatalf("public pack marketplace contains %d unreviewed entries", len(got))
	}
	catalog, err := RuntimeCatalog()
	if err != nil || len(catalog) != 3 {
		t.Fatalf("RuntimeCatalog = %v, %v", catalog, err)
	}
	for _, runtime := range catalog {
		if !slices.Contains([]string{"claude", "codex", "pi"}, runtime.AgentType) {
			t.Fatalf("unexpected runtime %q", runtime.Ref)
		}
		for _, model := range runtime.Models {
			if model.Provider != nil {
				t.Fatalf("runtime %s contains an unreviewed provider binding", runtime.Ref)
			}
		}
	}
}

func TestPublicEmbeddedPackContainsOnlyReviewedManifest(t *testing.T) {
	var files []string
	err := fs.WalkDir(builtinProfileFS, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil || !slices.Equal(files, []string{packManifestRelPath}) {
		t.Fatalf("public embedded files = %v, %v", files, err)
	}
}
