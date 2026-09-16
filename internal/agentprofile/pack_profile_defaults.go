package agentprofile

import (
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/distribution"
)

func validateProvider(agentType string, provider *Provider) error {
	agentType = strings.TrimSpace(agentType)
	if _, err := adapter.Get(agentType); err != nil {
		return err
	}
	if customization.ValidateProvider != nil {
		return customization.ValidateProvider(agentType, provider)
	}
	if provider == nil {
		return nil
	}
	if agentType != "codex" && agentType != "pi" {
		return fmt.Errorf("[provider] is only supported for codex and pi profiles")
	}
	platform := strings.TrimSpace(provider.Platform)
	if platform != "openai-compat" {
		return fmt.Errorf("[provider].platform %q is unsupported for %s profiles", provider.Platform, agentType)
	}
	if strings.TrimSpace(provider.UpstreamModel) == "" {
		return fmt.Errorf("[provider].upstream_model is required")
	}
	if !providerEnvNamePattern.MatchString(provider.APIKeyEnv) {
		return fmt.Errorf("[provider].api_key_env must be a bare environment variable name")
	}
	if platform == "openai-compat" && strings.TrimSpace(provider.BaseURL) == "" {
		return fmt.Errorf("[provider].base_url is required")
	}
	wireAPI := strings.TrimSpace(provider.WireAPI)
	if wireAPI == "responses-via-chat-completions" {
		return distribution.RequireResponsesChat()
	}
	if wireAPI != "" {
		if platform != "openai-compat" {
			return fmt.Errorf("[provider].wire_api is only supported with openai-compat")
		}
		expected := "responses"
		if agentType == "pi" {
			expected = "chat-completions"
		}
		if wireAPI != expected {
			return fmt.Errorf("[provider].wire_api %q is unsupported for %s; use %s", wireAPI, agentType, expected)
		}
	}
	return nil
}

func distributionPermissionModeArgs(agentType, mode string) ([]string, error) {
	if customization.PermissionModeArgs != nil {
		return customization.PermissionModeArgs(agentType, mode)
	}
	return nil, nil
}
