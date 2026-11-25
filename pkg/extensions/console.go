package extensions

import "github.com/amuxos/amux/internal/consoleentry"

type ConsolePublicOriginResolver = consoleentry.PublicOriginResolver

// RegisterConsolePublicOriginResolver installs an immutable resolver that
// discovers a browser-facing console origin after a listener binds a port.
func RegisterConsolePublicOriginResolver(resolver ConsolePublicOriginResolver) error {
	return consoleentry.RegisterPublicOriginResolver(resolver)
}
