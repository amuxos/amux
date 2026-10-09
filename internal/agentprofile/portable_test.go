package agentprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/task"
)

func portableTestProfile(t *testing.T) Profile {
	t.Helper()
	root := t.TempDir()
	writePackContent(t, root, "instructions/common/reviewer/AGENTS.md", "Review changed code.\n")
	writePackContent(t, root, "skills/custom/review/SKILL.md", "---\nname: review\ndescription: Review checklist\n---\nVerify behavior.\n")
	writeManifest(t, root, `{"schema_version":"v2","pack_namespace":"example","profiles":{"reviewer":{"namespace":"common","description":"Reviewer","instructions":["instructions/common/reviewer/AGENTS.md"],"skills":["review"],"subagents":[],"envs":["REVIEW_TOKEN"],"extends":null}},"namespaces":{"common":{"profiles":["reviewer"]}}}`)
	profiles, err := listPackProfiles(Pack{Namespace: "team", MaterializedPath: root})
	if err != nil {
		t.Fatal(err)
	}
	return profiles[0]
}

func TestPortableProfileCatalogAndExecutionBoundaries(t *testing.T) {
	profile := portableTestProfile(t)
	if profile.Type != "" || profile.Model != "" || profile.Provider != nil || len(profile.DefaultArgs) != 0 || len(profile.Placement.Requires.AgentTypes) != 0 {
		t.Fatalf("execution leaked into content: %+v", profile)
	}
	spec := ToPBProfileSpec(profile)
	if _, err := FromPBProfileDefinition(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := FromPBProfileSpec(spec); err == nil {
		t.Fatal("unresolved content accepted as execution snapshot")
	}
	if _, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{}); err == nil || !strings.Contains(err.Error(), "--runtime") || !strings.Contains(err.Error(), "执行引擎") {
		t.Fatalf("missing-runtime error = %v", err)
	}
	for _, engine := range []string{"codex", "claude"} {
		t.Run(engine, func(t *testing.T) {
			args, err := permissionModeArgs(engine, "plan")
			if err != nil {
				t.Fatal(err)
			}
			launch, err := ProjectProfileLaunchChecked(profile, args, nil, LaunchOptions{Runtime: engine, Model: "native-review-model"})
			if err != nil {
				t.Fatal(err)
			}
			if launch.AgentType != engine || launch.ProfileSpec.RuntimeRef != engine || launch.ProfileSpec.Model != "native-review-model" {
				t.Fatalf("launch = %+v", launch)
			}
			if !strings.Contains(strings.Join(launch.ExtraArgs, " "), strings.Join(args, " ")) {
				t.Fatalf("lost execution permission: %v", launch.ExtraArgs)
			}
			resolved, err := FromPBProfileSpec(launch.ProfileSpec)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(resolved.Envs, profile.Envs) || resolved.AgentsContent != profile.AgentsContent {
				t.Fatal("content or environment requirements lost")
			}
			resolved.PackPath = profile.PackPath
			out := t.TempDir()
			if _, err := RenderProfileWorkspace(resolved, out); err != nil {
				t.Fatal(err)
			}
			skill := ".agents/skills/review/SKILL.md"
			if engine == "claude" {
				skill = ".claude/skills/review/SKILL.md"
			}
			if _, err := os.Stat(filepath.Join(out, skill)); err != nil {
				t.Fatal(err)
			}
			other := profile
			other.Name = "another"
			second, err := ProjectProfileLaunchChecked(other, args, nil, LaunchOptions{Runtime: engine, Model: "native-review-model"})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(launch.ExtraArgs, second.ExtraArgs) {
				t.Fatal("changing expert changed execution arguments")
			}
		})
	}
}

func TestPortableManifestRejectsExecutionFieldPresence(t *testing.T) {
	for _, key := range []string{"type", "model", "providers", "supported_models", "permission", "Type", "Permission"} {
		for _, value := range []any{nil, "", []string{}, map[string]any{}} {
			entry := map[string]any{"namespace": "common", key: value}
			data, err := json.Marshal(map[string]any{"schema_version": "v2", "pack_namespace": "example", "profiles": map[string]any{"reviewer": entry}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parsePackManifest("manifest.json", data); err == nil {
				t.Fatalf("accepted %s=%v", key, value)
			}
		}
	}
}

func TestProfileMessageCatalogLanguagesAndFallback(t *testing.T) {
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese, "unknown"} {
		message := profileCatalog.Format(locale, "runtime_required", "team/reviewer")
		if !strings.Contains(message, "team/reviewer") || strings.Contains(message, "%!") || message == "runtime_required" {
			t.Fatalf("locale %s: %s", locale, message)
		}
	}
}

func TestPortableTaskResolvesBeforeSchedulingAndRetryKeepsSnapshot(t *testing.T) {
	profile := portableTestProfile(t)
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	registry := PackRegistry{Packs: map[string]Pack{"team": {Namespace: "team", MaterializedPath: profile.PackPath}}}
	if err := SavePackRegistry(registryPath, registry); err != nil {
		t.Fatal(err)
	}
	spec := task.Spec{Agent: profile.Ref(), Runtime: "claude", Model: "review-model", ExtraArgs: []string{"--permission-mode", "plan"}}
	if err := ResolveTaskSpec(&spec, registryPath); err != nil {
		t.Fatal(err)
	}
	if spec.Agent != "claude" || spec.ResolvedProfile == nil || spec.ResolvedProfile.RuntimeRef != "claude" || !reflect.DeepEqual(spec.ResolvedProfile.Envs, profile.Envs) {
		t.Fatalf("unresolved task: %+v", spec)
	}
	frozen := spec.ResolvedProfile
	// A retry already owns its resolved snapshot and needs no pack registry.
	if err := ResolveTaskSpec(&spec, "/missing/registry.json"); err != nil {
		t.Fatal(err)
	}
	if spec.ResolvedProfile != frozen {
		t.Fatal("retry replaced frozen execution")
	}
	missingRuntime := task.Spec{Agent: profile.Ref()}
	if err := ResolveTaskSpec(&missingRuntime, registryPath); err != nil {
		t.Fatal(err)
	}
	if missingRuntime.Agent != "codex" || missingRuntime.ResolvedProfile.RuntimeRef != "codex" || missingRuntime.ResolvedProfile.Name != profile.Name {
		t.Fatalf("task did not use the default engine with the selected content: %+v", missingRuntime)
	}
}

func TestManifestAcceptsOnlyV2AndNoPackProviders(t *testing.T) {
	for _, version := range []string{"v1.1", "v1", "", "v3"} {
		data, _ := json.Marshal(map[string]any{"schema_version": version, "pack_namespace": "team", "profiles": map[string]any{"reviewer": map[string]any{}}})
		if _, err := parsePackManifest("manifest.json", data); err == nil || !strings.Contains(err.Error(), "supported: v2") {
			t.Fatalf("version %q: %v", version, err)
		}
	}
	for _, key := range []string{"providers", "Providers"} {
		for _, value := range []any{nil, map[string]any{}, map[string]any{"service/model": map[string]any{"base_url": "https://example.test"}}} {
			data, _ := json.Marshal(map[string]any{"schema_version": "v2", "pack_namespace": "team", "profiles": map[string]any{"reviewer": map[string]any{}}, key: value})
			if _, err := parsePackManifest("manifest.json", data); err == nil {
				t.Fatalf("accepted %s=%v", key, value)
			}
		}
	}
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese, "unknown"} {
		for key, args := range map[string][]any{"schema_unsupported": {"old", "manifest.json"}, "pack_providers_forbidden": nil, "source_schema_unsupported": {"old"}, "runtime_permission_invalid": {"invalid"}} {
			message := profileCatalog.Format(locale, key, args...)
			if message == key || strings.Contains(message, "%!") {
				t.Fatalf("%s/%s: %s", locale, key, message)
			}
		}
	}
}
