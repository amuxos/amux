//go:build !windows

package isolation

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// maxExecAccessLinks bounds symlink resolution, matching the kernel's own limit
// closely enough that a loop cannot hang the walk.
const maxExecAccessLinks = 16

// CheckExecAccessForUID reports the first component of path that DAC-denies
// execution for (uid, gid). It is the privileged pre-flight: the helper runs as
// root and cannot ask the kernel on the principal's behalf, so it derives the
// answer from mode bits. A nil result means "permitted, or not decidable from
// mode bits alone" — the check must never turn a launch that would have worked
// into a refusal.
func CheckExecAccessForUID(path string, uid, gid int) *ExecAccessDenial {
	return checkExecAccess(path, uid, gid)
}

// CheckExecAccessForCaller reports the first component of path the calling
// process cannot traverse or execute. The re-exec wrapper already runs as the
// principal, so the kernel's own answer is authoritative: it accounts for ACLs,
// mount options and user-namespace id mapping that mode bits alone miss.
func CheckExecAccessForCaller(path string) *ExecAccessDenial {
	return checkExecAccess(path, -1, -1)
}

func checkExecAccess(path string, uid, gid int) *ExecAccessDenial {
	if !filepath.IsAbs(path) {
		return nil
	}
	return walkExecAccess(path, path, uid, gid, 0)
}

// walkExecAccess checks every directory that must be searched plus the binary
// itself. Symlinks are read rather than followed through Stat: the caller may
// well be unable to stat the target — that is precisely the failure being
// diagnosed — while reading the link only needs the parent directory it already
// reached.
func walkExecAccess(requested, path string, uid, gid, depth int) *ExecAccessDenial {
	if depth > maxExecAccessLinks {
		return nil
	}
	for _, component := range pathChain(path) {
		info, err := os.Lstat(component)
		if err != nil {
			// Either the path does not exist (execve will say ENOENT) or an
			// ancestor already denied us, which this walk reported earlier.
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(component)
			if err != nil {
				return nil
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(component), target)
			}
			if denial := walkExecAccess(requested, target, uid, gid, depth+1); denial != nil {
				return denial
			}
			continue
		}
		if execPermitted(component, info, uid, gid) {
			continue
		}
		if hasPosixACL(component) {
			// An ACL can grant what the mode bits deny; refuse to guess.
			return nil
		}
		ownerUID, ownerGID := fileOwner(info)
		return &ExecAccessDenial{
			Path:      requested,
			Component: component,
			Mode:      info.Mode(),
			OwnerUID:  ownerUID,
			OwnerGID:  ownerGID,
			Directory: info.IsDir(),
			UID:       uid,
			GID:       gid,
		}
	}
	return nil
}

func execPermitted(component string, info fs.FileInfo, uid, gid int) bool {
	if uid < 0 {
		return unix.Access(component, unix.X_OK) == nil
	}
	return execBitPermitted(info, uid, gid)
}

func pathChain(path string) []string {
	path = filepath.Clean(path)
	var chain []string
	for {
		chain = append(chain, path)
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func execBitPermitted(info fs.FileInfo, uid, gid int) bool {
	perm := info.Mode().Perm()
	ownerUID, ownerGID := fileOwner(info)
	if ownerUID < 0 {
		return true
	}
	switch {
	case uid == 0:
		// Root bypasses the owner/group/other split but still needs some
		// execute bit, and DAC_OVERRIDE does not cross a user namespace.
		return perm&0o111 != 0
	case ownerUID == uid:
		return perm&0o100 != 0
	case ownerGID == gid:
		return perm&0o010 != 0
	default:
		return perm&0o001 != 0
	}
}

func fileOwner(info fs.FileInfo) (int, int) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1
	}
	return int(st.Uid), int(st.Gid)
}
