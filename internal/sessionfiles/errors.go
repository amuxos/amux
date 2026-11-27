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

// ErrArtifactTooLarge is propagated through the helper and Runtime tunnel.
var ErrArtifactTooLarge = errors.New("session_artifact_too_large")

// ArtifactSizeExitCode conveys the size failure from the target-UID worker.
const ArtifactSizeExitCode = 124

func ArtifactTooLarge(err error) bool {
	if errors.Is(err, ErrArtifactTooLarge) {
		return true
	}
	if err == nil {
		return false
	}
	message := strings.TrimPrefix(err.Error(), "rpc error: code = ResourceExhausted desc = ")
	return message == ErrArtifactTooLarge.Error()
}
