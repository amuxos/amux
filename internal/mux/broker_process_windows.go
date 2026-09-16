//go:build windows

package mux

import (
	"os/exec"
	"syscall"
)

func configureBrokerProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func killBrokerProcess(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
