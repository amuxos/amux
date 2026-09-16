//go:build !windows

package cli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureManagedServiceProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func lockServiceFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func platformProcessRunning(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

func signalServiceProcess(pid int, proc *os.Process, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(-pid, signal); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return proc.Signal(signal)
}

func addressInUseError(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
