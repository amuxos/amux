package agentprofile

import (
	"reflect"
	"strings"
	"testing"
)

func TestDiscoveryKeepsHealthyPacksAndExplicitDevelopmentRefs(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{"schema_version":"v2","pack_namespace":"team","profiles":{"reviewer":{"description":"Review changes"}}}`)
	registry := PackRegistry{Packs: map[string]Pack{}}
	for _, pack := range []Pack{
		{Namespace: "team"},
		{Namespace: "preview", DevelopmentOf: "team"},
		{Namespace: "scratch", Temporary: true},
		{Namespace: "evaluation", Evaluation: true},
		{Namespace: "legacy-dev"},
		{Namespace: ManagedLocalPackNamespace, ManagedBy: ManagedPackOwnerAmux},
	} {
		pack.SourceKind, pack.MaterializedPath = PackSourceGit, root
		registry.Packs[pack.Namespace] = pack
	}
	registry.Packs["broken"] = Pack{Namespace: "broken", SourceKind: PackSourceGit, MaterializedPath: t.TempDir()}
	for _, tc := range []struct {
		all  bool
		want []string
	}{
		{false, []string{"local/reviewer", "team/reviewer"}},
		{true, []string{"evaluation/reviewer", "legacy-dev/reviewer", "local/reviewer", "preview/reviewer", "scratch/reviewer", "team/reviewer"}},
	} {
		profiles, err := DiscoverProfiles(registry, tc.all)
		if err == nil || !strings.Contains(err.Error(), "broken") {
			t.Fatalf("diagnostic = %v, want broken pack", err)
		}
		var refs []string
		for _, p := range profiles {
			if p.Source != SourceBuiltin {
				refs = append(refs, p.Ref())
			}
		}
		if !reflect.DeepEqual(refs, tc.want) {
			t.Fatalf("all=%v refs=%v, want %v", tc.all, refs, tc.want)
		}
	}
	profile, ok, err := ResolvePackProfile(registry, "evaluation/reviewer")
	if err != nil || !ok || profile.Description != "Review changes" {
		t.Fatalf("explicit evaluation: %+v %v %v", profile, ok, err)
	}

	manager, err := NewPackCatalogManager("unused", nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := manager.snapshot(registry)
	if len(snapshot.LocalExperts) != 1 || len(snapshot.Loaded) != 2 {
		t.Fatalf("management catalog includes hidden packs: %+v", snapshot)
	}
	for _, loaded := range snapshot.Loaded {
		if loaded.Namespace == "broken" && loaded.LastError == "" {
			t.Fatal("broken pack missing management diagnostic")
		}
		if loaded.Namespace != "team" && loaded.Namespace != "broken" {
			t.Fatalf("unexpected loaded pack: %+v", loaded)
		}
	}
}

func TestDevelopmentPackFailureDoesNotWarnInDefaultDiscovery(t *testing.T) {
	registry := PackRegistry{Packs: map[string]Pack{"preview": {Namespace: "preview", DevelopmentOf: "team", MaterializedPath: t.TempDir()}}}
	profiles, err := DiscoverProfiles(registry, false)
	if err != nil || len(profiles) != len(Builtins()) {
		t.Fatalf("profiles=%v err=%v", profiles, err)
	}
}
