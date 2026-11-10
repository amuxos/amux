package mux

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildFakeStat renders a /proc/<pid>/stat line with the given ppid (field 4)
// and starttime (field 22). comm is deliberately given a space and a paren to
// exercise the "count fields from after the final ')'" parsing.
func buildFakeStat(pid, ppid int, starttime uint64) string {
	fields := []string{fmt.Sprint(pid), "(we ird)comm)", "S", fmt.Sprint(ppid)} // fields 1-4
	for i := 5; i <= 21; i++ {
		fields = append(fields, "1") // fields 5-21
	}
	fields = append(fields, fmt.Sprint(starttime)) // field 22
	return strings.Join(fields, " ") + "\n"
}

// writeFakeProc installs a fake procfs entry. exeTarget, when non-empty, is
// used verbatim as the exe symlink target (so a " (deleted)" suffix exercises
// the deleted-binary detection); the target need not exist.
func writeFakeProc(t *testing.T, root string, pid int, cmdline []string, exeTarget string, ppid int, starttime uint64) {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	cl := strings.Join(cmdline, "\x00") + "\x00"
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cl), 0o644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(buildFakeStat(pid, ppid, starttime)), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	if exeTarget != "" {
		if err := os.Symlink(exeTarget, filepath.Join(dir, "exe")); err != nil {
			t.Fatalf("symlink exe: %v", err)
		}
	}
}

func TestListBrokerProcesses(t *testing.T) {
	procRoot := t.TempDir()
	sessDir := t.TempDir()

	sidStructured := "11111111-1111-1111-1111-111111111111"
	sidACP := "22222222-2222-2222-2222-222222222222"
	structSock := filepath.Join(sessDir, sidStructured+".structured.sock")
	acpSock := filepath.Join(sessDir, sidACP+".acp.sock")

	// Agent pid files sitting alongside the sockets (as the brokers write them).
	if err := os.WriteFile(filepath.Join(sessDir, sidStructured+".pid"), []byte("9001"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, sidACP+".pid"), []byte("9002"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A structured broker (has --session-id), current binary.
	writeFakeProc(t, procRoot, 5001, []string{
		"/home/u/.amux/bin/amuxlet", "_structured_broker",
		"--provider", "codex-app-server", "--session-id", sidStructured,
		"--socket", structSock, "--pid-file", filepath.Join(sessDir, sidStructured+".pid"),
		"--cwd", "/tmp", "--", "--some-agent-flag",
	}, "/home/u/.amux/bin/amuxlet", 1, 123456)

	// An ACP broker (no --session-id; sid must come from the socket), deleted exe.
	writeFakeProc(t, procRoot, 5002, []string{
		"/home/u/.amux/bin/amuxlet", "_acp_broker",
		"--socket", acpSock, "--pid-file", filepath.Join(sessDir, sidACP+".pid"),
		"--cwd", "/tmp", "--", "/usr/bin/demo-agent", "acp", "serve",
	}, "/home/u/.amux/bin/.amux-backup-123/amuxlet (deleted)", 1, 223456)

	// A non-broker process (the amuxlet main itself) — must be skipped.
	writeFakeProc(t, procRoot, 5003, []string{"/home/u/.amux/bin/amuxlet"}, "/home/u/.amux/bin/amuxlet", 1, 300)
	// A process whose ExtraArgs contain --socket after "--" — must not be a broker,
	// and even a broker must not read flags past the separator.
	writeFakeProc(t, procRoot, 5004, []string{"codex", "app-server", "--", "--socket", "/tmp/fake.structured.sock"}, "/usr/bin/codex", 1, 400)
	// Non-numeric and unreadable entries — must be skipped.
	if err := os.MkdirAll(filepath.Join(procRoot, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(procRoot, "6000"), 0o755); err != nil {
		t.Fatal(err)
	} // no cmdline -> unreadable

	got, err := listBrokerProcesses(procRoot)
	if err != nil {
		t.Fatalf("listBrokerProcesses: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d brokers, want 2: %+v", len(got), got)
	}
	byPID := map[int]BrokerProcess{}
	for _, bp := range got {
		byPID[bp.PID] = bp
	}

	sb, ok := byPID[5001]
	if !ok {
		t.Fatalf("structured broker 5001 missing: %+v", got)
	}
	if sb.ACP {
		t.Errorf("structured broker marked ACP")
	}
	if sb.SessionID != sidStructured {
		t.Errorf("structured sid = %q, want %q", sb.SessionID, sidStructured)
	}
	if sb.Socket != structSock {
		t.Errorf("structured socket = %q, want %q", sb.Socket, structSock)
	}
	if sb.DeletedExe {
		t.Errorf("structured broker wrongly marked deleted")
	}
	if sb.StartTime != 123456 {
		t.Errorf("structured starttime = %d, want 123456", sb.StartTime)
	}
	if sb.AgentPID != 9001 {
		t.Errorf("structured agent pid = %d, want 9001", sb.AgentPID)
	}

	ab, ok := byPID[5002]
	if !ok {
		t.Fatalf("acp broker 5002 missing: %+v", got)
	}
	if !ab.ACP {
		t.Errorf("acp broker not marked ACP")
	}
	if ab.SessionID != sidACP {
		t.Errorf("acp sid = %q, want %q (derived from socket)", ab.SessionID, sidACP)
	}
	if !ab.DeletedExe {
		t.Errorf("acp broker should be marked deleted-exe")
	}
	if ab.AgentPID != 9002 {
		t.Errorf("acp agent pid = %d, want 9002", ab.AgentPID)
	}
}

func TestParseBrokerCmdlineStopsAtSeparator(t *testing.T) {
	// The real --socket precedes "--"; a decoy --socket in ExtraArgs must be ignored.
	cmdline := strings.Join([]string{
		"/amuxlet", "_structured_broker", "--socket", "/sessions/abc.structured.sock",
		"--", "--socket", "/evil/xyz.structured.sock",
	}, "\x00") + "\x00"
	bp, ok := parseBrokerCmdline(4242, []byte(cmdline))
	if !ok {
		t.Fatal("expected broker to parse")
	}
	if bp.Socket != "/sessions/abc.structured.sock" {
		t.Errorf("socket = %q, want the pre-separator value", bp.Socket)
	}
	if bp.SessionID != "abc" {
		t.Errorf("sid = %q, want abc", bp.SessionID)
	}
}

func TestSessionIDFromSocket(t *testing.T) {
	cases := map[string]string{
		"/h/.amux/sessions/uuid-1.structured.sock": "uuid-1",
		"/h/.amux/sessions/uuid-2.acp.sock":        "uuid-2",
	}
	for sock, want := range cases {
		if got, ok := sessionIDFromSocket(sock); !ok || got != want {
			t.Errorf("sessionIDFromSocket(%q) = %q,%v want %q,true", sock, got, ok, want)
		}
	}
	for _, bad := range []string{"/h/.amux/sessions/uuid.pid", "/h/.amux/sessions/uuid.log", ""} {
		if _, ok := sessionIDFromSocket(bad); ok {
			t.Errorf("sessionIDFromSocket(%q) unexpectedly ok", bad)
		}
	}
}

func TestPIDFileForSocket(t *testing.T) {
	got := pidFileForSocket("/h/.amux/sessions/uuid-1.structured.sock")
	want := "/h/.amux/sessions/uuid-1.pid"
	if got != want {
		t.Errorf("pidFileForSocket = %q, want %q", got, want)
	}
	if got := pidFileForSocket("/h/.amux/sessions/uuid-1.acp.sock"); got != want {
		t.Errorf("pidFileForSocket acp = %q, want %q", got, want)
	}
}

func TestReadProcStartTimeAndPPID(t *testing.T) {
	dir := t.TempDir()
	statPath := filepath.Join(dir, "stat")
	if err := os.WriteFile(statPath, []byte(buildFakeStat(777, 4242, 987654)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readProcStartTime(statPath); got != 987654 {
		t.Errorf("readProcStartTime = %d, want 987654", got)
	}
	if got := procStatPPID(statPath); got != 4242 {
		t.Errorf("procStatPPID = %d, want 4242", got)
	}
	if got := readProcStartTime(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("readProcStartTime(missing) = %d, want 0", got)
	}
}
