package agentprofile

import (
	"strings"
	"testing"
)

func TestManifestV11SupportedModelsDistinguishesNativeAndProviderModels(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "type": "codex",
      "model": "gpt-5.4",
      "supported_models": ["gpt-5.4", "gpt-5.5", "external/gpt-5.5"],
      "providers": ["external/gpt-5.5"]
    }
  },
  "providers": {
    "external/gpt-5.5": {
      "platform": "openai-compat",
      "base_url": "https://models.example/v1",
      "api_key_env": "PROVIDER_API_KEY",
      "upstream_model": "gpt-5.5-2026-04-24",
      "context_window": 200000
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root, CurrentRevision: "abc123"},
	}}
	profile, ok, err := ResolvePackProfile(registry, "team/coder")
	if err != nil || !ok {
		t.Fatalf("ResolvePackProfile = (%+v, %v, %v)", profile, ok, err)
	}

	if got, want := profile.SupportedModels, []string{"gpt-5.4", "gpt-5.5", "external/gpt-5.5"}; !equalStrings(got, want) {
		t.Fatalf("SupportedModels = %v, want %v", got, want)
	}
	if profile.Model != "gpt-5.4" || profile.Provider != nil {
		t.Fatalf("default selection = model %q provider %+v, want native default", profile.Model, profile.Provider)
	}

	native, err := ResolveModelSelection(profile, "gpt-5.5")
	if err != nil {
		t.Fatalf("ResolveModelSelection native: %v", err)
	}
	if native.Kind != ModelKindNative || native.AgentModel != "gpt-5.5" || native.Provider != nil {
		t.Fatalf("native selection = %+v", native)
	}

	external, err := ResolveModelSelection(profile, "external/gpt-5.5")
	if err != nil {
		t.Fatalf("ResolveModelSelection provider: %v", err)
	}
	if external.Kind != ModelKindProvider || external.AgentModel != "gpt-5.5-2026-04-24" ||
		external.ProviderRef != "external" || external.Provider == nil ||
		external.Provider.CredentialProfile != "openai-compat" {
		t.Fatalf("provider selection = %+v", external)
	}
	launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{Model: "external/gpt-5.5"})
	if err != nil {
		t.Fatalf("ProjectProfileLaunchChecked provider: %v", err)
	}
	if launch.Model != "external/gpt-5.5" || launch.ProfileSpec.ModelRef != "external/gpt-5.5" ||
		launch.ProfileSpec.ModelKind != string(ModelKindProvider) ||
		launch.ProfileSpec.ProviderRef != "external" ||
		launch.ProfileSpec.Provider == nil ||
		launch.ProfileSpec.Provider.CredentialProfile != "openai-compat" ||
		launch.ProfileSpec.CredentialEnvSuggestions["PROVIDER_API_KEY"] != "openai-compat" {
		t.Fatalf("provider launch snapshot = %+v", launch)
	}

	if _, err := ResolveModelSelection(profile, "gpt-4.1"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported selection error = %v", err)
	}
}

func TestSuggestedCredentialProfileForEnvUsesEffectiveProviderCredentialProfile(t *testing.T) {
	profile := Profile{
		Provider: &Provider{
			CredentialProfile: "shared-example",
			Platform:          "openai-compat",
			APIKeyEnv:         "EXAMPLE_API_KEY",
		},
	}
	if got, want := SuggestedCredentialProfileForEnv(profile, "EXAMPLE_API_KEY"), "shared-example"; got != want {
		t.Fatalf("SuggestedCredentialProfileForEnv() = %q, want explicit credential profile %q", got, want)
	}
}

func TestProviderLiteralKeyYieldsNoCredentialSetupSuggestion(t *testing.T) {
	// A provider with api_key_literal is self-contained: there is no credential
	// store to set up, so no setup suggestion may be derived for its env.
	profile := Profile{
		Provider: &Provider{
			CredentialProfile: "openai-compat",
			Platform:          "openai-compat",
			APIKeyEnv:         "GLM_API_KEY",
			APIKeyLiteral:     "amux",
		},
	}
	if got := SuggestedCredentialProfileForEnv(profile, "GLM_API_KEY"); got != "" {
		t.Fatalf("SuggestedCredentialProfileForEnv() = %q, want none for literal key", got)
	}
}

func TestProviderPBRoundTripPreservesAPIKeyLiteral(t *testing.T) {
	profile := Profile{
		Namespace: "builtin",
		Name:      "reviewer",
		Type:      "codex",
		Model:     "service/example-model",
		Provider: &Provider{
			Platform:      "openai-compat",
			APIKeyEnv:     "GLM_API_KEY",
			APIKeyLiteral: "test-api-key",
			BaseURL:       "http://x/v1",
			UpstreamModel: "DeepSeek-V4-Flash",
		},
	}
	roundTripped, err := FromPBProfileSpec(ToPBProfileSpec(profile))
	if err != nil {
		t.Fatalf("FromPBProfileSpec: %v", err)
	}
	if roundTripped.Provider == nil {
		t.Fatal("round-tripped provider is nil")
	}
	if got, want := roundTripped.Provider.APIKeyLiteral, "test-api-key"; got != want {
		t.Fatalf("round-tripped api_key_literal = %q, want %q", got, want)
	}
}

func TestSuggestedCredentialProfileForEnvOmitsAmbiguousProviderOwnership(t *testing.T) {
	profile := Profile{ModelOptions: map[string]ModelOption{
		"example-a/model": {Provider: &Provider{CredentialProfile: "example-a", APIKeyEnv: "EXAMPLE_API_KEY"}},
		"example-b/model": {Provider: &Provider{CredentialProfile: "example-b", APIKeyEnv: "EXAMPLE_API_KEY"}},
	}}
	if got := SuggestedCredentialProfileForEnv(profile, "EXAMPLE_API_KEY"); got != "" {
		t.Fatalf("SuggestedCredentialProfileForEnv() = %q, want no ambiguous suggestion", got)
	}
}

func TestManifestV11SupportedModelsEmptyDefaultUsesAgentDefault(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "type": "codex",
      "model": "",
      "supported_models": ["gpt-5.4"]
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root, CurrentRevision: "abc123"},
	}}
	profile, ok, err := ResolvePackProfile(registry, "team/coder")
	if err != nil || !ok {
		t.Fatalf("ResolvePackProfile = (%+v, %v, %v)", profile, ok, err)
	}
	selection, err := ResolveModelSelection(profile, "")
	if err != nil {
		t.Fatalf("ResolveModelSelection: %v", err)
	}
	if selection.Ref != "" || selection.AgentModel != "" || selection.Kind != ModelKindDefault {
		t.Fatalf("selection = %+v, want agent default", selection)
	}
	launch, err := ProjectProfileLaunchChecked(profile, nil, nil, LaunchOptions{})
	if err != nil {
		t.Fatalf("ProjectProfileLaunchChecked: %v", err)
	}
	if launch.Model != "" || AgentArgsContainModel("codex", launch.ExtraArgs) {
		t.Fatalf("launch = %+v, want no model argument", launch)
	}
}

func TestManifestV11SupportedModelsRejectsDefaultOutsideAllowlist(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "type": "codex",
      "model": "gpt-5.4",
      "supported_models": ["gpt-5.5"]
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root, CurrentRevision: "abc123"},
	}}
	_, _, err := ResolvePackProfile(registry, "team/coder")
	if err == nil || !strings.Contains(err.Error(), "default model") {
		t.Fatalf("error = %v, want invalid default model", err)
	}
}

func TestManifestV11ExplicitEmptySupportedModelsRejectsOverride(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "type": "codex",
      "model": "",
      "supported_models": []
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root, CurrentRevision: "abc123"},
	}}
	profile, ok, err := ResolvePackProfile(registry, "team/coder")
	if err != nil || !ok {
		t.Fatalf("ResolvePackProfile = (%+v, %v, %v)", profile, ok, err)
	}
	if _, err := ResolveModelSelection(profile, "gpt-5.4"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("explicit model error = %v, want supported-model rejection", err)
	}
}

func TestManifestV11SupportedModelsRejectsUnknownQualifiedProviderModel(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "type": "codex",
      "model": "",
      "supported_models": ["missing-provider/gpt-5.5"]
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root, CurrentRevision: "abc123"},
	}}
	if _, _, err := ResolvePackProfile(registry, "team/coder"); err == nil ||
		!strings.Contains(err.Error(), `provider-backed model "missing-provider/gpt-5.5"`) {
		t.Fatalf("ResolvePackProfile error = %v, want unknown provider rejection", err)
	}
}

func TestManifestV11SupportedModelsRejectsMalformedQualifiedProviderModel(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "type": "codex",
      "model": "",
      "supported_models": ["/gpt-5.5"]
    }
  },
  "providers": {
    "/gpt-5.5": {
      "platform": "openai-compat",
      "upstream_model": "gpt-5.5"
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root, CurrentRevision: "abc123"},
	}}
	if _, _, err := ResolvePackProfile(registry, "team/coder"); err == nil ||
		!strings.Contains(err.Error(), "must use <provider-binding>/<model>") {
		t.Fatalf("ResolvePackProfile error = %v, want malformed provider ref rejection", err)
	}
}

func TestManifestV11SupportedModelsRequiresProviderDefaultInProviders(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "type": "codex",
      "model": "external/gpt-5.5",
      "supported_models": ["external/gpt-5.5"]
    }
  },
  "providers": {
    "external/gpt-5.5": {
      "platform": "openai-compat",
      "upstream_model": "gpt-5.5"
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root, CurrentRevision: "abc123"},
	}}
	if _, _, err := ResolvePackProfile(registry, "team/coder"); err == nil ||
		!strings.Contains(err.Error(), "must also be listed in providers") {
		t.Fatalf("ResolvePackProfile error = %v, want legacy provider projection rejection", err)
	}
}

func TestManifestV11RejectsUnqualifiedProviderModel(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "type": "codex",
      "model": "external-gpt-5.5",
      "supported_models": ["external-gpt-5.5"],
      "providers": ["external-gpt-5.5"]
    }
  },
  "providers": {
    "external-gpt-5.5": {
      "platform": "openai-compat",
      "base_url": "https://models.example/v1",
      "api_key_env": "PROVIDER_API_KEY",
      "upstream_model": "gpt-5.5"
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root, CurrentRevision: "abc123"},
	}}
	if _, _, err := ResolvePackProfile(registry, "team/coder"); err == nil ||
		!strings.Contains(err.Error(), "must use <provider-binding>/<model>") {
		t.Fatalf("ResolvePackProfile error = %v, want unqualified provider ref rejection", err)
	}
}

func TestManifestV11SupportedModelsDoesNotInferNativeModelFromGlobalProvider(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "type": "codex",
      "model": "shared-name",
      "supported_models": ["shared-name"],
      "providers": []
    }
  },
  "providers": {
    "shared-name": {
      "platform": "openai-compat",
      "upstream_model": "external-shared-name"
    }
  }
}`)
	registry := PackRegistry{Packs: map[string]Pack{
		"team": {Namespace: "team", MaterializedPath: root, CurrentRevision: "abc123"},
	}}
	profile, ok, err := ResolvePackProfile(registry, "team/coder")
	if err != nil || !ok {
		t.Fatalf("ResolvePackProfile = (%+v, %v, %v)", profile, ok, err)
	}
	selection, err := ResolveModelSelection(profile, "shared-name")
	if err != nil || selection.Kind != ModelKindNative || selection.Provider != nil {
		t.Fatalf("ResolveModelSelection = (%+v, %v), want native model", selection, err)
	}
}
