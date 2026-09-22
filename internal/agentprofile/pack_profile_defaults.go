package agentprofile

import (
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/distribution"
)

// validateProvider owns common invariants and native combinations. Extensions only
// admit additional combinations after those invariants have passed.
func validateProvider(agentType string, provider *Provider) error {
	agentType = strings.TrimSpace(agentType)
	if _, err := adapter.Get(agentType); err != nil {
		return err
	}
	if provider == nil {
		return nil
	}
	if strings.TrimSpace(provider.UpstreamModel) == "" {
		return fmt.Errorf("[provider].upstream_model is required")
	}
	if !providerEnvNamePattern.MatchString(provider.APIKeyEnv) {
		return fmt.Errorf("[provider].api_key_env must be a bare environment variable name")
	}
	platform := strings.TrimSpace(provider.Platform)
	wireAPI := strings.TrimSpace(provider.WireAPI)
	if platform == "openai-compat" && strings.TrimSpace(provider.BaseURL) == "" {
		return fmt.Errorf("[provider].base_url is required")
	}
	if wireAPI != "" {
		if platform != "openai-compat" {
			return fmt.Errorf("[provider].wire_api is only supported with openai-compat")
		}
		switch wireAPI {
		case "responses", "chat-completions":
		case "responses-via-chat-completions":
			if agentType != "codex" {
				return fmt.Errorf("[provider].wire_api %q is only supported for codex profiles", wireAPI)
			}
			if err := distribution.RequireResponsesChat(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("[provider].wire_api %q is unsupported", wireAPI)
		}
	}
	if platform == "openai-compat" && (agentType == "codex" || agentType == "pi") {
		expected := "responses"
		if agentType == "pi" {
			expected = "chat-completions"
		}
		if wireAPI != "" && wireAPI != expected && wireAPI != "responses-via-chat-completions" {
			return fmt.Errorf("[provider].wire_api %q is unsupported for %s; use %s", wireAPI, agentType, expected)
		}
		return nil
	}
	if customization.ValidateAdditionalProvider != nil {
		return customization.ValidateAdditionalProvider(agentType, provider)
	}
	return fmt.Errorf("[provider].platform %q is unsupported for %s profiles", provider.Platform, agentType)
}

func distributionPermissionModeArgs(agentType, mode string) ([]string, error) {
	if customization.PermissionModeArgs != nil {
		return customization.PermissionModeArgs(agentType, mode)
	}
	return nil, nil
}
