//go:build windows

package cli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureManagedServiceProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

func lockServiceFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0, &overlapped,
	)
}

func platformProcessRunning(pid int) (bool, error) {
	const stillActive = 259
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false, err
	}
	return exitCode == stillActive, nil
}

func signalServiceProcess(_ int, proc *os.Process, _ bool) error {
	// os.Process does not expose a portable graceful console signal on
	// Windows. Managed services therefore use the same terminal kill for both
	// escalation stages.
	return proc.Kill()
}

func addressInUseError(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE)
}
