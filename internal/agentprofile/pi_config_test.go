package agentprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializePiConfigWritesNativeModelSettings(t *testing.T) {
	profile := Profile{
		Namespace: "team",
		Name:      "pi-main",
		Type:      "pi",
		Model:     "deepseek-v4-flash",
	}
	root := t.TempDir()
	files, err := materializePiConfig(profile, root)
	if err != nil {
		t.Fatalf("materializePiConfig: %v", err)
	}
	if !containsString(files, ".pi/settings.json") {
		t.Fatalf("files = %#v, want .pi/settings.json", files)
	}
	settings := map[string]string{}
	data, err := os.ReadFile(filepath.Join(root, ".pi", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	if got, want := settings["defaultModel"], "deepseek-v4-flash"; got != want {
		t.Fatalf("defaultModel = %q, want %q", got, want)
	}
	if _, ok := settings["defaultProvider"]; ok {
		t.Fatalf("defaultProvider unexpectedly set: %v", settings)
	}
	// No provider-backed extension for a native model.
	assertFileNotExists(t, filepath.Join(root, ".pi", "extensions", "agent-pack-provider.ts"))
}

func TestMaterializePiConfigWritesProviderBackedSettingsAndExtension(t *testing.T) {
	profile := Profile{
		Namespace: "team",
		Name:      "pi-main",
		Type:      "pi",
		Model:     "glm-test/glm5.2",
		Provider: &Provider{
			Ref:           "glm-test/glm5.2",
			Platform:      "openai-compat",
			APIKeyEnv:     "GLM_API_KEY",
			BaseURL:       "https://provider.example/v1",
			UpstreamModel: "glm-5.2",
			ContextWindow: 262144,
			MaxTokens:     16384,
		},
	}
	root := t.TempDir()
	files, err := materializePiConfig(profile, root)
	if err != nil {
		t.Fatalf("materializePiConfig: %v", err)
	}
	if !containsString(files, ".pi/settings.json") || !containsString(files, ".pi/extensions/agent-pack-provider.ts") {
		t.Fatalf("files = %#v, want settings.json + provider extension", files)
	}
	settings := map[string]string{}
	data, _ := os.ReadFile(filepath.Join(root, ".pi", "settings.json"))
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	if got, want := settings["defaultProvider"], "glm-test"; got != want {
		t.Fatalf("defaultProvider = %q, want %q", got, want)
	}
	if got, want := settings["defaultModel"], "glm-5.2"; got != want {
		t.Fatalf("defaultModel = %q, want %q", got, want)
	}

	ext, err := os.ReadFile(filepath.Join(root, ".pi", "extensions", "agent-pack-provider.ts"))
	if err != nil {
		t.Fatalf("read extension: %v", err)
	}
	extText := string(ext)
	for _, want := range []string{
		`pi.registerProvider("glm-test"`,
		`"apiKey": "$GLM_API_KEY"`,
		`"baseUrl": "https://provider.example/v1"`,
		`"api": "openai-completions"`,
		`"id": "glm-5.2"`,
		`"contextWindow": 262144`,
		`"maxTokens": 16384`,
	} {
		if !strings.Contains(extText, want) {
			t.Errorf("extension missing %q:\n%s", want, extText)
		}
	}
	// Secrets must never be inlined; only the $ENV reference appears.
	if strings.Contains(extText, "GLM_API_KEY_RAW") || strings.Count(extText, "GLM_API_KEY") != 1 {
		t.Fatalf("extension leaks or duplicates the secret env reference:\n%s", extText)
	}
}

func TestPublicProviderRequiresExplicitPlatformAndEndpoint(t *testing.T) {
	for _, agent := range []string{"codex", "pi"} {
		for _, tc := range []struct {
			name, platform, baseURL, wantError string
		}{
			{"unregistered platform", "custom-provider", "https://provider.example/v1", "unsupported"},
			{"missing endpoint", "openai-compat", "", "base_url is required"},
		} {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				profile := Profile{
					Namespace: "team", Name: "provider", Type: agent, Model: "service/example-model",
					Provider: &Provider{
						Platform: tc.platform, BaseURL: tc.baseURL,
						APIKeyEnv: "PROVIDER_API_KEY", UpstreamModel: "example-model",
					},
				}
				root := filepath.Join(t.TempDir(), "projection")
				if _, err := RenderProfileWorkspace(profile, root); err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("RenderProfileWorkspace error = %v, want %q", err, tc.wantError)
				}
				assertFileNotExists(t, root)
			})
		}
	}
}

func TestPiProviderLaunchArgs(t *testing.T) {
	profile := Profile{
		Type:  "pi",
		Model: "glm-test/glm5.2",
		Provider: &Provider{
			Ref:           "glm-test/glm5.2",
			Platform:      "openai-compat",
			APIKeyEnv:     "GLM_API_KEY",
			UpstreamModel: "glm-5.2",
		},
	}
	got := piProviderLaunchArgs(profile, "glm-test/glm5.2")
	want := []string{"--model", "glm-test/glm-5.2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("piProviderLaunchArgs = %v, want %v", got, want)
	}

	// Non-provider-backed pi profiles keep native model semantics (no args).
	native := Profile{Type: "pi", Model: "deepseek-v4-flash"}
	if got := piProviderLaunchArgs(native, "deepseek-v4-flash"); got != nil {
		t.Fatalf("native pi launch args = %v, want nil", got)
	}
}

func TestPiProviderID(t *testing.T) {
	if got, want := piProviderID("glm-test/glm5.2"), "glm-test"; got != want {
		t.Fatalf("piProviderID(qualified) = %q, want %q", got, want)
	}
	if got, want := piProviderID("legacy-model"), "legacy-model"; got != want {
		t.Fatalf("piProviderID(unqualified) = %q, want %q", got, want)
	}
}

func TestRenderProfileWorkspaceProjectsPiLayout(t *testing.T) {
	profile := Profile{
		Namespace:     "team",
		Name:          "pi-coder",
		Type:          "pi",
		Agents:        "AGENTS.md",
		AgentsContent: "# Pi agent\n",
		Model:         "glm-test/glm5.2",
		Skills:        []string{},
		Provider: &Provider{
			Ref:           "glm-test/glm5.2",
			Platform:      "openai-compat",
			APIKeyEnv:     "GLM_API_KEY",
			BaseURL:       "https://provider.example/v1",
			UpstreamModel: "glm-5.2",
		},
	}
	root := t.TempDir()
	result, err := RenderProfileWorkspace(profile, root)
	if err != nil {
		t.Fatalf("RenderProfileWorkspace: %v", err)
	}
	for _, want := range []string{"AGENTS.md", ".pi/settings.json", ".pi/extensions/agent-pack-provider.ts"} {
		if !containsString(result.Files, want) {
			t.Fatalf("files = %#v, want %q", result.Files, want)
		}
	}
	assertFileContains(t, filepath.Join(root, "AGENTS.md"), "Pi agent")
	assertFileNotExists(t, filepath.Join(root, "amux-profile.json"))
}
