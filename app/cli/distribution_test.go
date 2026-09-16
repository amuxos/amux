package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const cliE2EBuildTags = ""

func TestPublicCLIAndMCPCatalogsContainOnlyNativeProfiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--json", "agent", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Profiles []struct {
			Ref       string `json:"ref"`
			AgentType string `json:"agent_type"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"builtin/codex": "codex", "builtin/claude": "claude", "builtin/pi": "pi"}
	if len(result.Profiles) != len(want) {
		t.Fatalf("public profiles = %+v", result.Profiles)
	}
	for _, profile := range result.Profiles {
		if expected, ok := want[profile.Ref]; !ok || profile.AgentType != expected {
			t.Fatalf("unexpected public profile: %+v", profile)
		}
		delete(want, profile.Ref)
	}
	useLocalAgentProfileClient(t)
	res, err := (&mcpHandler{}).listAgentProfilesPage(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	view := decodeMCPListAgentProfiles(t, res.Content[0].Text)
	if len(view.Profiles) != 3 || len(view.CapabilitySets) != 3 {
		t.Fatalf("MCP public catalog = %+v", view)
	}
	for _, agent := range []string{"codex", "claude", "pi"} {
		profile := findMCPProfile(t, view.Profiles, "builtin/"+agent)
		if profile.AgentType != agent || view.CapabilitySets[agent]["base_agent"] != agent {
			t.Fatalf("MCP public profile %s = %+v", agent, profile)
		}
	}
}

func TestPublicCLIDefaultsAndRuntimeModelSelection(t *testing.T) {
	if spec := runSubmit(t, "review changes"); spec.Agent != "builtin/codex" {
		t.Fatalf("default task profile = %q", spec.Agent)
	}
	fake := &fakeManagedLocalActions{}
	useFakeManagedLocalActions(t, fake)
	file := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(file, []byte("Review changes.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newAgentCmd()
	cmd.SetArgs([]string{"local", "create", "reviewer", "--instructions-file", file})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if fake.createdSpec.Type != "codex" || fake.createdSpec.Instructions != "Review changes.\n" {
		t.Fatalf("default local expert = %+v", fake.createdSpec)
	}
	req, err := buildRunRequestWithRuntime([]string{"builtin/claude"}, -1, "work", nil, "", "", "", "codex", "custom-native-model")
	if err != nil {
		t.Fatal(err)
	}
	if req.AgentProfile != "builtin/claude" || req.AgentType != "codex" || req.ProfileSpec.Model != "custom-native-model" || req.ProfileSpec.Provider != nil {
		t.Fatalf("runtime override = %+v", req)
	}
}

func TestPublicCapabilityConfigUsesNativeProfileModelPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, capability := range []string{"group_watch", "document_comments"} {
		prefix := "bridge.feishu.profiles.coder." + capability + "."
		for _, args := range [][]string{
			{"set", prefix + "agent_profile_ref", "builtin/codex"},
			{"set", prefix + "model_ref", "custom-native-model"},
		} {
			cmd := newConfigCmd()
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("native profile model config %v: %v", args, err)
			}
		}
	}
}

func TestPublicRuntimeCompletionsUsePublicSuggestions(t *testing.T) {
	if got := agentTypeCompletions(""); !slices.Equal(got, []string{"claude", "codex", "pi"}) {
		t.Fatalf("agent completions = %v", got)
	}
	if got := runtimeRefCompletions("co"); !slices.Equal(got, []string{"codex"}) {
		t.Fatalf("runtime completions = %v", got)
	}
	if got := runtimeModelCompletions("codex", ""); len(got) != 0 {
		t.Fatalf("unexpected Codex suggestions = %v", got)
	}
	if got := runtimeModelCompletions("claude", "so"); !slices.Equal(got, []string{"sonnet"}) {
		t.Fatalf("Claude suggestions = %v", got)
	}
}
