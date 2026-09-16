//go:build !windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// helperSocketOwner reports the credentials the running helper granted control
// access to: it chowns its socket to the service user at startup.
func helperSocketOwner(socket string) (int, int, error) {
	info, err := os.Stat(socket)
	if err != nil {
		return 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("cannot read helper socket ownership")
	}
	return int(stat.Uid), int(stat.Gid), nil
}

func setProcessCredential(cmd *exec.Cmd, uid, gid int) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), NoSetGroups: true},
	}
}
