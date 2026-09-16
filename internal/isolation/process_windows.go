//go:build windows

package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func setProcessGroup(cmd *exec.Cmd, enabled bool) {}

func parseSignal(name string) (os.Signal, error) {
	if strings.EqualFold(strings.TrimSpace(name), "interrupt") ||
		strings.EqualFold(strings.TrimSpace(name), "int") {
		return os.Interrupt, nil
	}
	return nil, fmt.Errorf("signal %q is unsupported on windows", name)
}

func signalProcess(p *os.Process, signal os.Signal, pid int) error { return p.Signal(signal) }
func killProcess(p *os.Process, pid int) error                     { return p.Kill() }

func exitStatus(state *os.ProcessState, started, exited time.Time) ExitStatus {
	out := ExitStatus{Code: -1, StartedAt: started, ExitedAt: exited}
	if state != nil {
		out.Code = state.ExitCode()
	}
	return out
}
