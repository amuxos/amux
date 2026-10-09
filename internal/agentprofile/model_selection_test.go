package agentprofile

import (
	"testing"
)

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
