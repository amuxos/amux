package bridge

import "context"

// AutomateOpenPlatformSetup invokes an explicitly installed console fallback.
// The default distribution leaves it unavailable.
func AutomateOpenPlatformSetup(ctx context.Context, appID string, options ConsoleOptions) ConsoleResult {
	if hook := bridgeExtensions.snapshot().ConsoleSetup; hook != nil {
		return hook(ctx, appID, options)
	}
	return ConsoleResult{Reason: "unavailable", Message: "Use the official app registration flow or configure the app manually."}
}
