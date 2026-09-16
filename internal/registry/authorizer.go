package registry

import (
	"errors"
	"sync"
)

// Registration auth errors.
var (
	// ErrBadToken is returned when the enrollment/shared token is missing or wrong.
	ErrBadToken = errors.New("registry: invalid runtime token")
	// ErrTargetNotAllowed is returned when a target_id is not on the allowlist.
	ErrTargetNotAllowed = errors.New("registry: target not allowed")
	// ErrLabelNotAllowed is returned when a reported label is outside the
	// server-side allowlist for the target.
	ErrLabelNotAllowed = errors.New("registry: label not allowed")
)

// Authorizer enforces the Phase 2 starter registration auth from the design's
// "Runtime 身份与注册鉴权": a hub shared token plus a server-side target
// allowlist, with optional per-target allowed labels. A runtime cannot register
// a target_id it is not authorized for, nor report labels outside the allowlist.
//
// This is the trusted-mode starter; mTLS / short-lived runtime tokens with
// target binding are the distributed-production form (later phase).
//
// The zero Authorizer (no token, no allowlist) authorizes everything, preserving
// today's open local/hub behavior until an operator opts in by configuring it.
type Authorizer struct {
	mu          sync.RWMutex
	sharedToken string
	// allowedTargets, when non-nil, restricts which target_ids may register.
	allowedTargets map[string]struct{}
	// allowedLabels[target][key] = value restricts reported label values per
	// target. A target absent here has no label restriction.
	allowedLabels map[string]map[string]string
}

// AuthorizerConfig configures an Authorizer.
type AuthorizerConfig struct {
	SharedToken    string
	AllowedTargets []string
	AllowedLabels  map[string]map[string]string
}

// NewAuthorizer builds an Authorizer. An empty config authorizes everything.
func NewAuthorizer(cfg AuthorizerConfig) *Authorizer {
	a := &Authorizer{sharedToken: cfg.SharedToken}
	if len(cfg.AllowedTargets) > 0 {
		a.allowedTargets = make(map[string]struct{}, len(cfg.AllowedTargets))
		for _, t := range cfg.AllowedTargets {
			a.allowedTargets[t] = struct{}{}
		}
	}
	if len(cfg.AllowedLabels) > 0 {
		a.allowedLabels = cfg.AllowedLabels
	}
	return a
}

// Authorize validates a registration attempt. token is the value the runtime
// presented; caps are its reported capabilities (labels are validated against
// the allowlist). It returns nil when the runtime may register.
func (a *Authorizer) Authorize(targetID, token string, caps Capabilities) error {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.sharedToken != "" && token != a.sharedToken {
		return ErrBadToken
	}
	if a.allowedTargets != nil {
		if _, ok := a.allowedTargets[targetID]; !ok {
			return ErrTargetNotAllowed
		}
	}
	if allowed, ok := a.allowedLabels[targetID]; ok {
		for k, v := range caps.Labels {
			want, present := allowed[k]
			if !present || want != v {
				return ErrLabelNotAllowed
			}
		}
	}
	return nil
}

// Enabled reports whether any auth restriction is configured. When false,
// Authorize always succeeds (open mode).
func (a *Authorizer) Enabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.sharedToken != "" || a.allowedTargets != nil || a.allowedLabels != nil
}
