//go:build !linux && !windows

package isolation

// hasPosixACL is a Linux concern; the helper only runs there. Elsewhere the
// pre-flight is exercised by tests against plain mode bits.
func hasPosixACL(string) bool { return false }
