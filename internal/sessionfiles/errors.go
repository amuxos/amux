package sessionfiles

import (
	"errors"
	"strings"
)

// ErrHelperUpgradeRequired is a stable diagnostic marker. Tunnel errors retain
// the Runtime's gRPC error text, so callers must also recognize its wire form.
var ErrHelperUpgradeRequired = errors.New("session_file_helper_upgrade_required")

func NeedsHelperUpgrade(err error) bool {
	if errors.Is(err, ErrHelperUpgradeRequired) {
		return true
	}
	if err == nil {
		return false
	}
	message := strings.TrimPrefix(err.Error(), "rpc error: code = Unimplemented desc = ")
	marker := ErrHelperUpgradeRequired.Error()
	return message == marker || strings.HasPrefix(message, marker+": ")
}
