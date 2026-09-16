package isolation

import (
	"fmt"
	"io/fs"
)

// ExecAccessDenial names the first path component that prevents a principal
// from executing a launch binary. execve reports only EACCES, which reaches the
// operator as a bare "permission denied" with no indication of which directory
// on the chain is closed; this carries the component, its mode and its owner.
type ExecAccessDenial struct {
	// Path is the launch binary as requested.
	Path string
	// Component is the first path component that denies access. It may sit on
	// the symlink target chain rather than on Path itself.
	Component string
	Mode      fs.FileMode
	OwnerUID  int
	OwnerGID  int
	// Directory is true when Component denies search rather than execution.
	Directory bool
	// UID/GID are the credentials the check ran for, or -1 when the check asked
	// the kernel about the calling process instead of deriving DAC bits.
	UID int
	GID int
}

func (d *ExecAccessDenial) Error() string {
	if d == nil {
		return ""
	}
	who := "the launching process"
	if d.UID >= 0 {
		who = fmt.Sprintf("uid %d gid %d", d.UID, d.GID)
	}
	return fmt.Sprintf("launch binary %q is not executable by %s: %s", d.Path, who, d.Detail())
}

// Detail renders only the blocking-component clause, for callers whose message
// already names the binary they tried to run.
func (d *ExecAccessDenial) Detail() string {
	if d == nil {
		return ""
	}
	subject := fmt.Sprintf("file %q is not executable", d.Component)
	if d.Directory {
		subject = fmt.Sprintf("directory %q denies search", d.Component)
	}
	return fmt.Sprintf("%s (mode %04o, owner %d:%d)", subject, d.Mode.Perm(), d.OwnerUID, d.OwnerGID)
}

// DescribeExecFailure explains an execve failure for a launch binary. The
// kernel reports only an errno, so a denied exec reaches the operator as a bare
// "permission denied" with no indication of which directory closed the chain.
func DescribeExecFailure(path string, err error) string {
	if denial := CheckExecAccessForCaller(path); denial != nil {
		return fmt.Sprintf("exec %s: %v: %s", path, err, denial.Detail())
	}
	return fmt.Sprintf("exec %s: %v", path, err)
}
