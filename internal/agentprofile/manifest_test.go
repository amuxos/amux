package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListPackProfilesFromManifestConsumesAllProfilesNotNamespaceIndexOnly(t *testing.T) {
	root := t.TempDir()
	writePackContent(t, root, "instructions/a.md", "A\n")
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "example-team",
  "profiles": {
    "listed": {"namespace":"common","type":"codex","model":"MiniMax-M2.7","providers":[],"instructions":["instructions/a.md"],"skills":[],"subagents":[],"extends":null},
    "not-in-namespace-index": {"namespace":"common","type":"codex","model":"MiniMax-M2.7","providers":[],"instructions":["instructions/a.md"],"skills":[],"subagents":[],"extends":null}
  },
  "providers": {},
  "namespaces": {"common": {"profiles": ["listed"]}}
}`)

	profiles, err := listPackProfiles(Pack{Namespace: "team", MaterializedPath: root})
	if err != nil {
		t.Fatalf("listPackProfiles: %v", err)
	}
	refs := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		refs = append(refs, profile.Ref())
	}
	if !containsString(refs, "team/listed") || !containsString(refs, "team/not-in-namespace-index") {
		t.Fatalf("refs = %v, want all manifest.profiles entries regardless of namespaces index", refs)
	}
}

func TestManifestInstructionsLoadInArrayOrder(t *testing.T) {
	root := t.TempDir()
	writePackContent(t, root, "instructions/one.md", "one\n")
	writePackContent(t, root, "instructions/two.md", "two\n")
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "example-team",
  "profiles": {
    "multi": {"namespace":"common","type":"codex","model":"gpt-5.4","providers":[],"instructions":["instructions/one.md","instructions/two.md"],"skills":[],"subagents":[],"extends":null}
  },
  "providers": {},
  "namespaces": {"common": {"profiles": ["multi"]}}
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root},
	}}

	profile, ok, err := ResolvePackProfile(registry, "team/multi")
	if err != nil {
		t.Fatalf("ResolvePackProfile: %v", err)
	}
	if !ok {
		t.Fatal("ResolvePackProfile ok=false")
	}
	if profile.Agents != "instructions/one.md, instructions/two.md" {
		t.Fatalf("agents = %q, want ordered instruction list display", profile.Agents)
	}
	if profile.AgentsContent != "one\ntwo\n" {
		t.Fatalf("agents content = %q, want concatenated instructions in order", profile.AgentsContent)
	}
}

func TestManifestLoaderRejectsInvalidContract(t *testing.T) {
	t.Run("unsupported schema version", func(t *testing.T) {
		root := t.TempDir()
		writeManifest(t, root, `{"schema_version":"v99","pack_namespace":"example-team","profiles":{},"providers":{},"namespaces":{}}`)
		_, err := LoadPackManifest(filepath.Join(root, "dist", "agent-pack.manifest.json"))
		if err == nil || !strings.Contains(err.Error(), `unsupported agent pack manifest schema_version "v99"`) {
			t.Fatalf("error = %v, want unsupported schema_version", err)
		}
	})

	t.Run("missing referenced provider", func(t *testing.T) {
		root := t.TempDir()
		writePackContent(t, root, "instructions/a.md", "A\n")
		writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "example-team",
  "profiles": {
    "bad": {"namespace":"common","type":"codex","model":"MiniMax-M2.7","providers":["missing/model"],"instructions":["instructions/a.md"],"skills":[],"subagents":[],"extends":null}
  },
  "providers": {},
  "namespaces": {"common": {"profiles": ["bad"]}}
}`)
		_, _, err := loadPackProfile(Pack{Namespace: "team", MaterializedPath: root}, "bad")
		if err == nil || !strings.Contains(err.Error(), `profile bad references unknown provider "missing/model"`) {
			t.Fatalf("error = %v, want missing provider", err)
		}
	})

	t.Run("empty instructions allowed", func(t *testing.T) {
		root := t.TempDir()
		writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "example-team",
  "profiles": {
    "coder": {"namespace":"common","type":"codex","model":"MiniMax-M2.7","providers":[],"instructions":[],"skills":[],"subagents":[],"extends":null}
  },
  "providers": {},
  "namespaces": {"common": {"profiles": ["coder"]}}
}`)
		profile, ok, err := loadPackProfile(Pack{Namespace: "team", MaterializedPath: root}, "coder")
		if err != nil {
			t.Fatalf("loadPackProfile: %v", err)
		}
		if !ok {
			t.Fatalf("profile coder not found")
		}
		if profile.Agents != "" {
			t.Fatalf("Agents = %q, want empty", profile.Agents)
		}
		if profile.AgentsContent != "" {
			t.Fatalf("AgentsContent = %q, want empty", profile.AgentsContent)
		}
	})

	t.Run("empty model allowed without providers", func(t *testing.T) {
		root := t.TempDir()
		writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "example-team",
  "profiles": {
    "codex": {"namespace":"common","type":"codex","model":"","providers":[],"instructions":[],"skills":[],"subagents":[],"extends":null}
  },
  "providers": {},
  "namespaces": {"common": {"profiles": ["codex"]}}
}`)
		profile, ok, err := loadPackProfile(Pack{Namespace: "team", MaterializedPath: root}, "codex")
		if err != nil {
			t.Fatalf("loadPackProfile: %v", err)
		}
		if !ok {
			t.Fatalf("profile codex not found")
		}
		if profile.Model != "" {
			t.Fatalf("Model = %q, want empty", profile.Model)
		}
		if profile.Provider != nil {
			t.Fatalf("Provider = %+v, want nil", profile.Provider)
		}
	})

	t.Run("empty model rejected when providers declared", func(t *testing.T) {
		root := t.TempDir()
		writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "example-team",
  "profiles": {
    "bad": {"namespace":"common","type":"codex","model":"","providers":["glm-prod/glm"],"instructions":[],"skills":[],"subagents":[],"extends":null}
  },
  "providers": {
    "glm-prod/glm": {"platform":"openai-compat","base_url":"https://x/v1","upstream_model":"glm-4","api_key_env":"GLM_API_KEY"}
  },
  "namespaces": {"common": {"profiles": ["bad"]}}
}`)
		_, _, err := loadPackProfile(Pack{Namespace: "team", MaterializedPath: root}, "bad")
		if err == nil || !strings.Contains(err.Error(), "profile bad model is required when providers are declared") {
			t.Fatalf("error = %v, want model-required-with-providers", err)
		}
	})
}

func TestResolveProfileSuggestsHyphenatedManifestProfileName(t *testing.T) {
	root := t.TempDir()
	writePackContent(t, root, "instructions/push-sre/AGENTS.md", "# Push SRE\n")
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "example-team",
  "profiles": {
    "push-sre": {"namespace":"push","type":"codex","model":"MiniMax-M2.7","providers":[],"instructions":["instructions/push-sre/AGENTS.md"],"skills":[],"subagents":[],"extends":null}
  },
  "providers": {},
  "namespaces": {"push": {"profiles": ["push-sre"]}}
}`)
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	if err := SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{
		"expert-agents": {Namespace: "expert-agents", MaterializedPath: root},
	}}); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	_, err := ResolveProfile(registryPath, "expert-agents/push_sre")
	if err == nil {
		t.Fatal("ResolveProfile succeeded, want suggestion error")
	}
	if !IsProfileNotFound(err) {
		t.Fatalf("IsProfileNotFound(%v) = false, want true", err)
	}
	if !strings.Contains(err.Error(), `did you mean "expert-agents/push-sre"`) {
		t.Fatalf("error = %v, want hyphenated suggestion", err)
	}
}

func writeManifest(t *testing.T, root, body string) {
	t.Helper()
	path := filepath.Join(root, "dist", "agent-pack.manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}
