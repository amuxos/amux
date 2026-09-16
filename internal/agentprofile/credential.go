package agentprofile

import "strings"

// EffectiveCredentialProfile returns the credential profile used by a
// provider. Platform is the compatibility fallback when no explicit profile
// was resolved into the provider snapshot.
func EffectiveCredentialProfile(provider *Provider) string {
	if provider == nil {
		return ""
	}
	if credentialProfile := strings.TrimSpace(provider.CredentialProfile); credentialProfile != "" {
		return credentialProfile
	}
	return strings.TrimSpace(provider.Platform)
}

// SuggestedCredentialProfileForEnv returns the unique provider credential
// profile associated with envName. It is only a setup destination hint;
// profile envs still resolve by globally unique env key at runtime.
func SuggestedCredentialProfileForEnv(profile Profile, envName string) string {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		return ""
	}
	return credentialEnvSuggestions(profile)[envName]
}

func credentialEnvSuggestions(profile Profile) map[string]string {
	candidatesByEnv := make(map[string]map[string]struct{})
	for envName, candidate := range profile.CredentialEnvSuggestions {
		addCredentialCandidate(candidatesByEnv, envName, candidate)
	}
	addProviderCredentialCandidate(candidatesByEnv, profile.Provider)
	for _, option := range profile.ModelOptions {
		addProviderCredentialCandidate(candidatesByEnv, option.Provider)
	}
	suggestions := make(map[string]string)
	for envName, candidates := range candidatesByEnv {
		if len(candidates) != 1 {
			continue
		}
		for candidate := range candidates {
			suggestions[envName] = candidate
		}
	}
	return suggestions
}

func addCredentialCandidate(candidatesByEnv map[string]map[string]struct{}, envName, candidate string) {
	envName = strings.TrimSpace(envName)
	candidate = strings.TrimSpace(candidate)
	if envName == "" || candidate == "" {
		return
	}
	if candidatesByEnv[envName] == nil {
		candidatesByEnv[envName] = make(map[string]struct{})
	}
	candidatesByEnv[envName][candidate] = struct{}{}
}

func addProviderCredentialCandidate(candidatesByEnv map[string]map[string]struct{}, provider *Provider) {
	if provider == nil {
		return
	}
	envName := strings.TrimSpace(provider.APIKeyEnv)
	if envName == "" || strings.TrimSpace(provider.APIKeyLiteral) != "" {
		// A literal key is self-contained; there is no credential store setup to
		// suggest for it.
		return
	}
	candidate := EffectiveCredentialProfile(provider)
	addCredentialCandidate(candidatesByEnv, envName, candidate)
}
