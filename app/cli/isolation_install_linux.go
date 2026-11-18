//go:build linux

package cli

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The helper re-execs itself as the isolated UID. Explicitly set permissions
// on new directories and the dedicated install directory, bypassing umask.
// Check each parent before descending so no unprivileged user can substitute
// a directory in the path later used to install or run the binaries.
func prepareIsolationHelperDir(root string, create bool) error {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	const mode = 0o755
	fd, err := unix.Open(root, flags, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: root, Err: err}
	}
	defer func() { _ = unix.Close(fd) }()
	path := root
	owner := uint32(os.Geteuid())
	parts := strings.Split(strings.TrimPrefix(isolationHelperDir, "/"), "/")
	for index, part := range parts {
		path = filepath.Join(path, part)
		created := false
		if create {
			err := unix.Mkdirat(fd, part, mode)
			if err != nil && err != unix.EEXIST {
				return &os.PathError{Op: "mkdir", Path: path, Err: err}
			}
			created = err == nil
		}
		child, err := unix.Openat(fd, part, flags, 0)
		if err != nil {
			return &os.PathError{Op: "open", Path: path, Err: err}
		}
		_ = unix.Close(fd)
		fd = child

		var info unix.Stat_t
		if err := unix.Fstat(fd, &info); err != nil {
			return &os.PathError{Op: "stat", Path: path, Err: err}
		}
		if info.Uid != owner || info.Mode&0o022 != 0 {
			return &os.PathError{Op: "validate", Path: path, Err: helperUpgradeProblem("invalid_target")}
		}
		if created || index == len(parts)-1 {
			if info.Mode&0o7777 != mode {
				if err := unix.Fchmod(fd, mode); err != nil {
					return &os.PathError{Op: "chmod", Path: path, Err: err}
				}
			}
		} else if info.Mode&0o111 != 0o111 {
			return &os.PathError{Op: "access", Path: path, Err: unix.EACCES}
		}
	}
	return nil
}
