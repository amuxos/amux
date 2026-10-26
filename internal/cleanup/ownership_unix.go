//go:build !windows

package cleanup

import (
	"os"
	"syscall"
)

func preserveOwnership(f *os.File, st os.FileInfo) error {
	if raw, ok := st.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		return f.Chown(int(raw.Uid), int(raw.Gid))
	}
	return nil
}
