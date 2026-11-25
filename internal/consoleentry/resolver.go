package consoleentry

import (
	"context"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/extensionstate"
)

// PublicOriginResolver resolves a browser-facing console origin after a
// listener binds a local port (for example, managed reverse-proxy or
// port-forwarding environments where the external URL appears only after the
// port begins listening).
type PublicOriginResolver interface {
	Active() bool
	Resolve(ctx context.Context, port int) (string, error)
}

var registeredPublicOriginResolver PublicOriginResolver

// RegisterPublicOriginResolver installs an immutable post-listen origin
// resolver before startup freezes extensions.
func RegisterPublicOriginResolver(resolver PublicOriginResolver) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if resolver == nil {
		return fmt.Errorf("console public origin resolver is required")
	}
	if registeredPublicOriginResolver != nil {
		return fmt.Errorf("console public origin resolver already registered")
	}
	registeredPublicOriginResolver = resolver
	return nil
}

// HasActivePublicOriginResolver reports whether a registered resolver is active
// in the current runtime environment.
func HasActivePublicOriginResolver() bool {
	return registeredPublicOriginResolver != nil && registeredPublicOriginResolver.Active()
}

// ResolvePublicOrigin asks the active resolver for a validated, non-loopback
// browser-facing origin for the given bound TCP port.
func ResolvePublicOrigin(ctx context.Context, port int) (string, error) {
	if port < 1 || port > 65535 || registeredPublicOriginResolver == nil || !registeredPublicOriginResolver.Active() {
		return "", nil
	}
	raw, err := registeredPublicOriginResolver.Resolve(ctx, port)
	if err != nil || strings.TrimSpace(raw) == "" {
		return "", err
	}
	validated, err := ValidateURL(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if LocalURL(validated) {
		return "", ErrInvalidURL
	}
	return validated, nil
}
