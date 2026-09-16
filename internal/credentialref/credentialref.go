package credentialref

import (
	"fmt"
	"regexp"
	"strings"
)

// EnvRef is a credential-backed environment variable reference declared by an
// Agent Profile. Raw keeps the original env_refs entry for diagnostics.
type EnvRef struct {
	Profile string
	Env     string
	Raw     string
}

var (
	profileNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	envNamePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseEnvRef parses credential:<profile>:<env> entries. Non-credential env
// refs are ignored so existing refs such as secret:<name> remain compatible.
func ParseEnvRef(value string) (EnvRef, bool, error) {
	raw := strings.TrimSpace(value)
	if raw != "credential" && !strings.HasPrefix(raw, "credential:") {
		return EnvRef{}, false, nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return EnvRef{}, true, fmt.Errorf("invalid credential env_ref %q: want credential:<profile>:<env>", value)
	}
	profile := strings.TrimSpace(parts[1])
	env := strings.TrimSpace(parts[2])
	if !profileNamePattern.MatchString(profile) {
		return EnvRef{}, true, fmt.Errorf("invalid credential env_ref %q: profile must match %s", value, profileNamePattern.String())
	}
	if !envNamePattern.MatchString(env) {
		return EnvRef{}, true, fmt.Errorf("invalid credential env_ref %q: env must match %s", value, envNamePattern.String())
	}
	return EnvRef{Profile: profile, Env: env, Raw: raw}, true, nil
}

// ParseEnvRefs returns only credential env refs, preserving their declaration
// order and failing fast on malformed credential refs.
func ParseEnvRefs(values []string) ([]EnvRef, error) {
	refs := make([]EnvRef, 0, len(values))
	for _, value := range values {
		ref, ok, err := ParseEnvRef(value)
		if err != nil {
			return nil, err
		}
		if ok {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}
