package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePackProfileFromManifest(t *testing.T) {
	root := t.TempDir()
	writePackContent(t, root, "instructions/coder/AGENTS.md", "# Instructions\n\nUse the team coding playbook.\n")
	writeManifest(t, root, `{
  "schema_version": "v2",
  "pack_namespace": "example-team",
  "profiles": {
    "coder": {
      "namespace": "common",
      "instructions": [
        "instructions/coder/AGENTS.md"
      ],
      "skills": [
        "amux-dev",
        "code-review"
      ],
      "subagents": [],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "coder"
      ]
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {
			Namespace:        "team",
			SourceKind:       PackSourceGit,
			Ref:              "stable",
			MaterializedPath: root,
			CurrentRevision:  "abc123",
		},
	}}

	profile, ok, err := ResolvePackProfile(registry, "team/coder")
	if err != nil {
		t.Fatalf("ResolvePackProfile: %v", err)
	}
	if !ok {
		t.Fatal("ResolvePackProfile ok=false, want true")
	}
	if profile.Ref() != "team/coder" || profile.PinnedRef() != "team/coder@abc123" {
		t.Fatalf("refs = %q / %q, want team/coder / team/coder@abc123", profile.Ref(), profile.PinnedRef())
	}
	if profile.Source != SourcePack || profile.Type != "" || profile.Model != "" {
		t.Fatalf("profile = %+v, want content-only pack profile", profile)
	}
	if profile.Agents != "instructions/coder/AGENTS.md" {
		t.Fatalf("agents = %q", profile.Agents)
	}
	if profile.AgentsContent != "# Instructions\n\nUse the team coding playbook.\n" {
		t.Fatalf("agents content = %q", profile.AgentsContent)
	}
	if got, want := profile.Skills, []string{"amux-dev", "code-review"}; !equalStrings(got, want) {
		t.Fatalf("skills = %v, want %v", got, want)
	}
	if got, want := profile.DefaultArgs, []string(nil); !equalStrings(got, want) {
		t.Fatalf("default args = %v, want %v", got, want)
	}
}

func TestResolvePackProfileHonorsPinnedRevision(t *testing.T) {
	root := t.TempDir()
	writePackContent(t, root, "instructions/coder/AGENTS.md", "# Coder\n")
	writeManifest(t, root, `{
  "schema_version": "v2",
  "pack_namespace": "example-team",
  "profiles": {
    "coder": {
      "namespace": "common",
      "instructions": [
        "instructions/coder/AGENTS.md"
      ],
      "skills": [],
      "subagents": [],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "coder"
      ]
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", SourceKind: PackSourceGit, MaterializedPath: root, CurrentRevision: "abc123"},
	}}

	profile, ok, err := ResolvePackProfile(registry, "team/coder@abc123")
	if err != nil {
		t.Fatalf("ResolvePackProfile current pinned revision: %v", err)
	}
	if !ok || profile.PinnedRef() != "team/coder@abc123" {
		t.Fatalf("profile ok=%v pinned=%q, want team/coder@abc123", ok, profile.PinnedRef())
	}
	_, _, err = ResolvePackProfile(registry, "team/coder@oldrev")
	if err == nil || !strings.Contains(err.Error(), "not materialized") {
		t.Fatalf("ResolvePackProfile old revision error = %v, want not materialized", err)
	}
}

func TestListPackProfilesSortsByRef(t *testing.T) {
	root := t.TempDir()
	writePackContent(t, root, "instructions/coder/AGENTS.md", "# Coder\n")
	writePackContent(t, root, "instructions/reviewer/AGENTS.md", "# Reviewer\n")
	writeManifest(t, root, `{
  "schema_version": "v2",
  "pack_namespace": "example-team",
  "profiles": {
    "reviewer": {
      "namespace": "common",
      "instructions": [
        "instructions/reviewer/AGENTS.md"
      ],
      "skills": [],
      "subagents": [],
      "extends": null
    },
    "coder": {
      "namespace": "common",
      "instructions": [
        "instructions/coder/AGENTS.md"
      ],
      "skills": [],
      "subagents": [],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "reviewer",
        "coder"
      ]
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", SourceKind: PackSourceGit, MaterializedPath: root, CurrentRevision: "def456"},
	}}

	profiles, err := ListPackProfiles(registry)
	if err != nil {
		t.Fatalf("ListPackProfiles: %v", err)
	}
	if len(profiles) != 2 || profiles[0].Ref() != "team/coder" || profiles[1].Ref() != "team/reviewer" {
		refs := make([]string, 0, len(profiles))
		for _, profile := range profiles {
			refs = append(refs, profile.Ref())
		}
		t.Fatalf("profiles = %v, want sorted team/coder, team/reviewer", refs)
	}
	if profiles[0].PinnedRef() != "team/coder@def456" {
		t.Fatalf("pinned ref = %q, want team/coder@def456", profiles[0].PinnedRef())
	}
}

func TestListPackProfilesByPackIsolatesUnavailablePack(t *testing.T) {
	healthyRoot := t.TempDir()
	writeManifest(t, healthyRoot, `{
  "schema_version": "v2",
  "pack_namespace": "example-team",
  "profiles": {
    "coder": {
      "namespace": "common",
      "instructions": [],
      "skills": [],
      "subagents": [],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "coder"
      ]
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"broken": {
			Namespace:        "broken",
			SourceKind:       PackSourceGit,
			MaterializedPath: t.TempDir(),
			CurrentRevision:  "bad123",
		},
		"team": {
			Namespace:        "team",
			SourceKind:       PackSourceGit,
			MaterializedPath: healthyRoot,
			CurrentRevision:  "good123",
		},
	}}

	results := ListPackProfilesByPack(registry)
	if len(results) != 2 {
		t.Fatalf("results = %#v, want one result per Pack", results)
	}
	if results[0].Namespace != "broken" || results[0].Err == nil {
		t.Fatalf("results[0] = %#v, want isolated broken Pack error", results[0])
	}
	if results[1].Namespace != "team" || results[1].Err != nil ||
		len(results[1].Profiles) != 1 || results[1].Profiles[0].Ref() != "team/coder" {
		t.Fatalf("results[1] = %#v, want healthy team/coder Profile", results[1])
	}
}

func TestResolvePackProfileRejectsBuiltinNamespaceAndUnknownProfile(t *testing.T) {
	root := t.TempDir()
	writePackContent(t, root, "instructions/coder/AGENTS.md", "# Coder\n")
	writeManifest(t, root, `{
  "schema_version": "v2",
  "pack_namespace": "example-team",
  "profiles": {
    "coder": {
      "namespace": "common",
      "instructions": [
        "instructions/coder/AGENTS.md"
      ],
      "skills": [],
      "subagents": [],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "coder"
      ]
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", SourceKind: PackSourceGit, MaterializedPath: root, CurrentRevision: "abc123"},
	}}

	if profile, ok, err := ResolvePackProfile(registry, "team/missing"); err != nil || ok {
		t.Fatalf("missing profile = %+v ok=%v err=%v, want ok=false nil err", profile, ok, err)
	}
	if _, ok, err := ResolvePackProfile(registry, "builtin/claude"); err == nil || ok || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("builtin resolve ok=%v err=%v, want reserved namespace error", ok, err)
	}
}

func writePackContent(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir content dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write content: %v", err)
	}
}
