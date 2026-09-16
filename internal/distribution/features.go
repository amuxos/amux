package distribution

import "errors"

// RequireResponsesChat checks availability before creating resources or
// materializing credentials for an optional compatibility gateway.
func RequireResponsesChat() error {
	if !SupportsResponsesChat {
		return errors.New("responses-via-chat-completions is unavailable in this distribution; configure a native Responses upstream")
	}
	return nil
}
