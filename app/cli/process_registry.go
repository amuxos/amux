package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	trackedProcessRoleMCP          = "mcp"
	trackedProcessRoleGUI          = "gui"
	trackedProcessRolePackAutoSync = "agent-pack-autosync"
)

type trackedProcess struct {
	Role      string   `json:"role"`
	PID       int      `json:"pid"`
	PPID      int      `json:"ppid,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	Version   string   `json:"version,omitempty"`
	StartedAt int64    `json:"started_at,omitempty"`
	Args      []string `json:"args,omitempty"`
}

func registerTrackedProcess(role string) func() {
	role = strings.TrimSpace(role)
	if role == "" {
		return func() {}
	}
	proc := trackedProcess{
		Role:      role,
		PID:       os.Getpid(),
		PPID:      os.Getppid(),
		SessionID: strings.TrimSpace(os.Getenv("AMUX_SESSION_ID")),
		Version:   version,
		StartedAt: time.Now().Unix(),
		Args:      append([]string(nil), os.Args...),
	}
	if err := os.MkdirAll(trackedProcessDir(), 0700); err != nil {
		return func() {}
	}
	path := trackedProcessPath(role, proc.PID)
	data, err := json.MarshalIndent(proc, "", "  ")
	if err != nil {
		return func() {}
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); err != nil {
		return func() {}
	}
	return func() { _ = os.Remove(path) }
}

func trackedProcessDir() string {
	return filepath.Join(defaultAmuxDir(), "processes")
}

func trackedProcessPath(role string, pid int) string {
	return filepath.Join(trackedProcessDir(), fmt.Sprintf("%s-%d.json", role, pid))
}

func listTrackedProcesses(roles ...string) ([]trackedProcess, error) {
	entries, err := os.ReadDir(trackedProcessDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []trackedProcess
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(trackedProcessDir(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var proc trackedProcess
		if err := json.Unmarshal(data, &proc); err != nil || proc.PID <= 0 || proc.Role == "" {
			_ = os.Remove(path)
			continue
		}
		if len(roles) > 0 && !slices.Contains(roles, proc.Role) {
			continue
		}
		if !processRunning(proc.PID) {
			_ = os.Remove(path)
			continue
		}
		if !trackedProcessMatchesRunningInvocation(proc) {
			_ = os.Remove(path)
			continue
		}
		out = append(out, proc)
	}
	return out, nil
}

func trackedProcessMatchesRunningInvocation(proc trackedProcess) bool {
	expected := expectedTrackedProcessInvocation(proc)
	if len(expected) == 0 {
		return true
	}
	commandLine, ok := processCommandLine(proc.PID)
	if !ok {
		return true
	}
	for _, token := range expected {
		if !strings.Contains(commandLine, token) {
			return false
		}
	}
	return true
}

func expectedTrackedProcessInvocation(proc trackedProcess) []string {
	if !trackedProcessArgsDeclareRole(proc) {
		return nil
	}
	var expected []string
	if len(proc.Args) > 0 {
		if base := filepath.Base(proc.Args[0]); base != "." && base != string(filepath.Separator) {
			expected = append(expected, base)
		}
	}
	switch proc.Role {
	case trackedProcessRoleMCP:
		expected = append(expected, "mcp")
	case trackedProcessRoleGUI:
		expected = append(expected, "gui", "serve")
	default:
		return nil
	}
	return expected
}

func trackedProcessArgsDeclareRole(proc trackedProcess) bool {
	switch proc.Role {
	case trackedProcessRoleMCP:
		return len(proc.Args) >= 2 && proc.Args[1] == "mcp"
	case trackedProcessRoleGUI:
		return len(proc.Args) >= 3 && proc.Args[1] == "gui" && proc.Args[2] == "serve"
	default:
		return false
	}
}

func restartableTrackedProcesses(procs []trackedProcess) []trackedProcess {
	out := make([]trackedProcess, 0, len(procs))
	for _, proc := range procs {
		if proc.SessionID != "" {
			continue
		}
		out = append(out, proc)
	}
	return out
}

func restartTrackedLocalServiceProcesses(timeout time.Duration) ([]trackedProcess, error) {
	procs, err := listTrackedProcesses(trackedProcessRoleMCP, trackedProcessRoleGUI)
	if err != nil {
		return nil, err
	}
	procs = restartableTrackedProcesses(procs)
	for _, proc := range procs {
		p, err := os.FindProcess(proc.PID)
		if err != nil {
			_ = os.Remove(trackedProcessPath(proc.Role, proc.PID))
			continue
		}
		if err := p.Signal(syscall.SIGTERM); err != nil && processRunning(proc.PID) {
			return procs, fmt.Errorf("signal %s pid %d: %w", proc.Role, proc.PID, err)
		}
	}
	deadline := time.Now().Add(timeout)
	for _, proc := range procs {
		for processRunning(proc.PID) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if processRunning(proc.PID) {
			return procs, fmt.Errorf("%s pid %d did not exit within %s", proc.Role, proc.PID, timeout)
		}
		_ = os.Remove(trackedProcessPath(proc.Role, proc.PID))
	}
	return procs, nil
}
