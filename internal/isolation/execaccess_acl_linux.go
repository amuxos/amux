//go:build linux

package isolation

import "golang.org/x/sys/unix"

// hasPosixACL reports whether a POSIX access ACL is attached to path. Such an
// ACL can grant execute permission that the mode bits deny, so the mode-bit
// pre-flight treats those components as undecidable instead of refusing them.
func hasPosixACL(path string) bool {
	size, err := unix.Getxattr(path, "system.posix_acl_access", nil)
	return err == nil && size > 0
}
