package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegisterTrackedProcessListsAndCleansUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	oldVersion := version
	version = "v-test"
	t.Cleanup(func() { version = oldVersion })

	cleanup := registerTrackedProcess(trackedProcessRoleMCP)
	procs, err := listTrackedProcesses(trackedProcessRoleMCP)
	if err != nil {
		t.Fatalf("list tracked processes: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("tracked processes = %d, want 1: %+v", len(procs), procs)
	}
	if got := procs[0]; got.Role != trackedProcessRoleMCP || got.PID != os.Getpid() || got.Version != "v-test" || got.StartedAt == 0 {
		t.Fatalf("tracked process = %+v", got)
	}

	cleanup()
	procs, err = listTrackedProcesses(trackedProcessRoleMCP)
	if err != nil {
		t.Fatalf("list after cleanup: %v", err)
	}
	if len(procs) != 0 {
		t.Fatalf("tracked processes after cleanup = %+v, want none", procs)
	}
}

func TestListTrackedProcessesRemovesStaleEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := os.MkdirAll(trackedProcessDir(), 0700); err != nil {
		t.Fatal(err)
	}
	stalePath := trackedProcessPath(trackedProcessRoleGUI, 99999999)
	if err := os.WriteFile(stalePath, []byte(`{"role":"gui","pid":99999999}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	procs, err := listTrackedProcesses(trackedProcessRoleGUI)
	if err != nil {
		t.Fatalf("list tracked processes: %v", err)
	}
	if len(procs) != 0 {
		t.Fatalf("tracked processes = %+v, want none", procs)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale entry should be removed, err=%v", err)
	}
}

func TestListTrackedProcessesRemovesPIDReuseEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := os.MkdirAll(trackedProcessDir(), 0700); err != nil {
		t.Fatal(err)
	}
	reusedPath := trackedProcessPath(trackedProcessRoleMCP, os.Getpid())
	entry := `{"role":"mcp","pid":` + strconv.Itoa(os.Getpid()) + `,"args":["/tmp/amux","mcp"]}` + "\n"
	if err := os.WriteFile(reusedPath, []byte(entry), 0600); err != nil {
		t.Fatal(err)
	}

	procs, err := listTrackedProcesses(trackedProcessRoleMCP)
	if err != nil {
		t.Fatalf("list tracked processes: %v", err)
	}
	if len(procs) != 0 {
		t.Fatalf("tracked processes = %+v, want none", procs)
	}
	if _, err := os.Stat(reusedPath); !os.IsNotExist(err) {
		t.Fatalf("pid reuse entry should be removed, err=%v", err)
	}
}

func TestFormatTrackedProcesses(t *testing.T) {
	got := formatTrackedProcesses([]trackedProcess{
		{Role: trackedProcessRoleMCP, PID: 12, StartedAt: time.Now().Unix()},
		{Role: trackedProcessRoleGUI, PID: 34},
	})
	for _, want := range []string{"mcp(pid 12)", "gui(pid 34)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatTrackedProcesses() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, string(filepath.Separator)) {
		t.Fatalf("formatTrackedProcesses() should not expose paths: %q", got)
	}
}

func TestRestartableTrackedProcessesExcludeSessionOwned(t *testing.T) {
	got := restartableTrackedProcesses([]trackedProcess{
		{Role: trackedProcessRoleMCP, PID: 12, SessionID: "sess-demo-agent"},
		{Role: trackedProcessRoleGUI, PID: 34},
	})
	if len(got) != 1 || got[0].Role != trackedProcessRoleGUI || got[0].PID != 34 {
		t.Fatalf("restartableTrackedProcesses() = %+v, want only gui pid 34", got)
	}
}

func TestPrintRestartedClientProcessesNoneDoesNotPrintExitNote(t *testing.T) {
	var out bytes.Buffer
	printRestartedLocalServiceProcesses(&out, nil)
	text := out.String()
	for _, want := range []string{
		"Restarted local server(s): none",
		"No registered running `amux mcp` / `amux gui serve` processes found.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "asked their old") {
		t.Fatalf("none output should not claim processes were asked to exit:\n%s", text)
	}
}

func TestPrintRestartedClientProcessesPrintsExitNoteWhenNonEmpty(t *testing.T) {
	var out bytes.Buffer
	printRestartedLocalServiceProcesses(&out, []trackedProcess{{Role: trackedProcessRoleMCP, PID: 12}})
	text := out.String()
	for _, want := range []string{
		"Restarted local server(s): mcp(pid 12)",
		"asked their old `amux mcp` / `amux gui serve` processes to exit",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}
