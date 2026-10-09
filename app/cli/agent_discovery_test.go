package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
)

func seedProfileDiscoveryRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("AMUX_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "agent-pack.manifest.json"), []byte(`{"schema_version":"v2","pack_namespace":"team","profiles":{"reviewer":{"description":"Review changes"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	registry := agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{
		"team":    {Namespace: "team", SourceKind: agentprofile.PackSourceGit, MaterializedPath: root},
		"preview": {Namespace: "preview", SourceKind: agentprofile.PackSourceGit, MaterializedPath: root, DevelopmentOf: "team"},
		"broken":  {Namespace: "broken", SourceKind: agentprofile.PackSourceGit, MaterializedPath: t.TempDir()},
	}}
	if err := agentprofile.SavePackRegistry(defaultAgentPackRegistryPath(), registry); err != nil {
		t.Fatal(err)
	}
}

func TestAgentDiscoveryPartialResultsAndLocalizedWarning(t *testing.T) {
	seedProfileDiscoveryRegistry(t)
	var previousJSON string
	for _, tc := range []struct{ locale, warning, description string }{
		{"en", "Some expert libraries are unavailable", "brief description of the expert purpose"},
		{"zh-CN", "部分专家库不可用", "专家用途的简短说明"},
		{"fr-FR", "Some expert libraries are unavailable", "brief description of the expert purpose"},
	} {
		t.Setenv("AMUX_LANGUAGE", tc.locale)
		cmd := newAgentCmd()
		var output, warnings bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&warnings)
		cmd.SetArgs([]string{"list"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "team/reviewer") || strings.Contains(output.String(), "preview/reviewer") {
			t.Fatalf("output=%s", output.String())
		}
		if !strings.Contains(warnings.String(), tc.warning) || !strings.Contains(warnings.String(), "broken") {
			t.Fatalf("warning=%s", warnings.String())
		}
		if got := newAgentLocalCreateCmd().Flags().Lookup("description").Usage; got != tc.description {
			t.Fatalf("description help=%q", got)
		}
		cmd = newRootCmd()
		output.Reset()
		warnings.Reset()
		cmd.SetOut(&output)
		cmd.SetErr(&warnings)
		cmd.SetArgs([]string{"agent", "list", "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "profiles_error") || warnings.Len() != 0 {
			t.Fatalf("JSON=%s stderr=%s", output.String(), warnings.String())
		}
		if previousJSON != "" && output.String() != previousJSON {
			t.Fatal("locale changed structured profile discovery")
		}
		previousJSON = output.String()
	}
	for _, flag := range []string{"--all", "--dev"} {
		cmd := newRootCmd()
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetArgs([]string{"agent", "list", flag, "--json"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Profiles []struct {
				Ref string `json:"ref"`
			} `json:"profiles"`
		}
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		var refs []string
		for _, p := range result.Profiles {
			refs = append(refs, p.Ref)
		}
		if !strings.Contains(strings.Join(refs, ","), "preview/reviewer") {
			t.Fatalf("%s refs=%v", flag, refs)
		}
		if flag == "--dev" && len(refs) != 1 {
			t.Fatalf("development refs=%v", refs)
		}
	}
	refs, _ := completeAgentProfileRefs(nil, nil, "team/")
	if len(refs) != 1 || !strings.HasPrefix(refs[0], "team/reviewer") {
		t.Fatalf("completion=%v", refs)
	}
}

func TestMCPDiscoveryUsesHubCatalogAndKeepsPartialResults(t *testing.T) {
	seedProfileDiscoveryRegistry(t)
	useLocalAgentProfileClient(t)
	result, err := (&mcpHandler{}).listAgentProfilesPage(context.Background(), nil)
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data := result.Content[0].Text
	if !strings.Contains(data, "team/reviewer") || strings.Contains(data, "preview/reviewer") || !strings.Contains(data, "profiles_error") {
		t.Fatalf("discovery=%s", data)
	}
	detail, err := (&mcpHandler{}).getAgentProfile(context.Background(), json.RawMessage(`{"ref":"preview/reviewer"}`))
	if err != nil || detail.IsError || !strings.Contains(detail.Content[0].Text, "preview/reviewer") {
		t.Fatalf("explicit development profile=%+v err=%v", detail, err)
	}
}
