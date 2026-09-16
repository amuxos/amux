package agentprofile

import (
	"path/filepath"
	"testing"
)

func TestProjectProfileLaunchAppliesModelOverrideWithoutMutatingProfile(t *testing.T) {
	profile := Profile{
		Namespace:   "team",
		Name:        "coder",
		Revision:    "abc123",
		Type:        "codex",
		Model:       "gpt-5-codex",
		DefaultArgs: []string{"--sandbox", "workspace-write"},
		DefaultEnv:  []string{"TEAM=amux"},
		Source:      SourcePack,
	}

	launch := ProjectProfileLaunch(profile, []string{"--approval-policy", "never"}, []string{"EXTRA=1"}, LaunchOptions{Model: "custom-model"})

	if launch.AgentType != "codex" || launch.AgentProfile != "team/coder@abc123" {
		t.Fatalf("launch refs = %q/%q, want codex team/coder@abc123", launch.AgentType, launch.AgentProfile)
	}
	if got, want := launch.ExtraArgs, []string{"--model", "custom-model", "--sandbox", "workspace-write", "--approval-policy", "never"}; !equalStrings(got, want) {
		t.Fatalf("extra args = %#v, want %#v", got, want)
	}
	if got, want := launch.Env, []string{"TEAM=amux", "EXTRA=1"}; !equalStrings(got, want) {
		t.Fatalf("env = %#v, want %#v", got, want)
	}
	if launch.ProfileSpec == nil || launch.ProfileSpec.Model != "custom-model" {
		t.Fatalf("profile spec model = %+v, want effective override model", launch.ProfileSpec)
	}
	if profile.Model != "gpt-5-codex" {
		t.Fatalf("profile mutated model = %q, want original gpt-5-codex", profile.Model)
	}
}

func TestDefaultPackRegistryPathUsesAMUXHome(t *testing.T) {
	amuxHome := filepath.Join(t.TempDir(), ".amux-real")
	t.Setenv("AMUX_HOME", amuxHome)
	if got, want := DefaultPackRegistryPath(), filepath.Join(amuxHome, "agent-packs", "registry.json"); got != want {
		t.Fatalf("DefaultPackRegistryPath = %q, want %q", got, want)
	}
}

func TestProjectProfileLaunchExplicitModelArgWinsOverProfileDefaultModelArg(t *testing.T) {
	profile := Profile{
		Namespace:   "team",
		Name:        "coder",
		Revision:    "abc123",
		Type:        "codex",
		Model:       "profile-model",
		DefaultArgs: []string{"--model", "default-arg-model", "--sandbox", "workspace-write"},
		Source:      SourcePack,
	}

	launch := ProjectProfileLaunch(profile, []string{"--model", "explicit-model"}, nil, LaunchOptions{})

	if launch.ProfileSpec == nil || launch.ProfileSpec.Model != "explicit-model" {
		t.Fatalf("profile spec model = %+v, want explicit provider arg model", launch.ProfileSpec)
	}
	if got, want := launch.ExtraArgs, []string{"--model", "default-arg-model", "--sandbox", "workspace-write", "--model", "explicit-model"}; !equalStrings(got, want) {
		t.Fatalf("extra args = %#v, want args preserved %#v", got, want)
	}
}

func TestProjectProfileLaunchCodexOpenAICompatInjectsResponsesProviderArgs(t *testing.T) {
	profile := Profile{
		Namespace: "builtin",
		Name:      "reviewer",
		Type:      "codex",
		Model:     "service/example-model",
		Provider: &Provider{
			Ref:           "service",
			Platform:      "openai-compat",
			WireAPI:       "responses",
			BaseURL:       "https://provider.example/v1",
			UpstreamModel: "default_model",
			APIKeyEnv:     "GLM_API_KEY",
			ContextWindow: 900000,
			MaxTokens:     80000,
			Thinking:      map[string]string{"type": "enabled", "reasoning_effort": "high"},
		},
		DefaultArgs: []string{"-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`},
	}

	launch := ProjectProfileLaunch(profile, nil, nil, LaunchOptions{})
	if got, ok := AgentArgsModel("codex", launch.ExtraArgs); !ok || got != "default_model" {
		t.Fatalf("Codex process model = %q, %t; want upstream default_model", got, ok)
	}
	for _, want := range []string{
		`model_provider="amux"`,
		`model="default_model"`,
		"disable_response_storage=true",
		`model_providers.amux.name="service"`,
		`model_providers.amux.base_url="https://provider.example/v1"`,
		`model_providers.amux.env_key="GLM_API_KEY"`,
		`model_providers.amux.wire_api="responses"`,
		`model_providers.amux.env_http_headers={"X-Session-Id"="AMUX_SESSION_ID"}`,
		"model_context_window=900000",
		"model_max_output_tokens=80000",
		`model_reasoning_effort="high"`,
	} {
		if !containsString(launch.ExtraArgs, want) {
			t.Fatalf("extra args = %#v\nmissing %q", launch.ExtraArgs, want)
		}
	}
	if launch.Model != "service/example-model" ||
		launch.ProfileSpec.Model != "service/example-model" {
		t.Fatalf("launch/profile model = %q/%q, want user-facing provider ref", launch.Model, launch.ProfileSpec.Model)
	}
	if launch.ProfileSpec.Provider.WireApi != "responses" {
		t.Fatalf("provider wire_api = %q, want responses", launch.ProfileSpec.Provider.WireApi)
	}
	if containsString(launch.ExtraArgs, `web_search="disabled"`) {
		t.Fatalf("direct Responses provider unexpectedly disables web search: %#v", launch.ExtraArgs)
	}
}

func TestProjectProfileLaunchPreservesProviderLiteralForCredentialPlan(t *testing.T) {
	// The provider snapshot emitted into ProfileSpec must carry api_key_literal
	// so the runtime credential plan can render the key without a store lookup.
	profile := Profile{
		Namespace: "builtin",
		Name:      "reviewer",
		Type:      "codex",
		Model:     "service/example-model",
		Provider: &Provider{
			Platform:      "openai-compat",
			BaseURL:       "https://provider.example/v1",
			UpstreamModel: "DeepSeek-V4-Flash",
			APIKeyEnv:     "GLM_API_KEY",
			APIKeyLiteral: "test-api-key",
		},
	}
	launch := ProjectProfileLaunch(profile, nil, nil, LaunchOptions{})
	if launch.ProfileSpec == nil || launch.ProfileSpec.Provider == nil {
		t.Fatalf("profile spec provider missing: %+v", launch.ProfileSpec)
	}
	if got, want := launch.ProfileSpec.Provider.ApiKeyLiteral, "test-api-key"; got != want {
		t.Fatalf("projected api_key_literal = %q, want %q", got, want)
	}
	// The launch args must still reference the env var (codex reads it from the
	// process env which the credential plan populates with the literal).
	if !containsString(launch.ExtraArgs, `model_providers.amux.env_key="GLM_API_KEY"`) {
		t.Fatalf("extra args = %#v\nexpected env_key reference preserved", launch.ExtraArgs)
	}
}
