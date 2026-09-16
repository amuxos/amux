//go:build !windows

package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func setProcessGroup(cmd *exec.Cmd, enabled bool) {
	if enabled {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
}

func parseSignal(name string) (os.Signal, error) {
	name = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(name)), "SIG")
	switch name {
	case "TERM":
		return syscall.SIGTERM, nil
	case "INT", "INTERRUPT":
		return syscall.SIGINT, nil
	case "HUP":
		return syscall.SIGHUP, nil
	case "KILL":
		return syscall.SIGKILL, nil
	}
	n, err := strconv.Atoi(name)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("unsupported signal %q", name)
	}
	return syscall.Signal(n), nil
}

func signalProcess(p *os.Process, signal os.Signal, pid int) error {
	return p.Signal(signal)
}

func killProcess(p *os.Process, pid int) error {
	return p.Kill()
}

func exitStatus(state *os.ProcessState, started, exited time.Time) ExitStatus {
	out := ExitStatus{Code: -1, StartedAt: started, ExitedAt: exited}
	if state == nil {
		return out
	}
	out.Code = state.ExitCode()
	if ws, ok := state.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			out.Signal = strings.TrimPrefix(strings.ToUpper(ws.Signal().String()), "SIG")
			out.Code = 128 + int(ws.Signal())
		}
		out.CoreDumped = ws.CoreDump()
	}
	return out
}
