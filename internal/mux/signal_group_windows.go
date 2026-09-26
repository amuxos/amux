//go:build windows

package mux

import (
	"os"

	"golang.org/x/sys/windows"
)

// signalProcessGroup terminates the process on Windows. Process groups from
// CREATE_NEW_PROCESS_GROUP do not map to POSIX kill(-pid); TerminateProcess
// on the broker pid is the reliable stop used by broker_process_windows.go.
func signalProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if proc, findErr := os.FindProcess(pid); findErr == nil {
			_ = proc.Kill()
		}
		return
	}
	defer windows.CloseHandle(handle)
	_ = windows.TerminateProcess(handle, 1)
}
