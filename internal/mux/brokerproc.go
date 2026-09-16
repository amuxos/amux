package mux

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// BrokerProcess describes a live session-broker subprocess (_structured_broker
// or _acp_broker) discovered by scanning the process table.
//
// amuxlet tracks sessions in runtime.db, but brokers are spawned with Setsid
// and are never Wait()ed, so they outlive the amuxlet that spawned them and
// are reparented to init. When amuxlet is SIGKILLed, crashes, or its binary is
// overwritten by `amux update`, the graceful shutdown path never runs and the
// broker leaks. The process table is the only ground truth for which brokers
// are actually still alive, so the reaper enumerates them here and reconciles
// them against the session DB.
type BrokerProcess struct {
	PID        int    // the broker process id
	AgentPID   int    // inner agent pid from <sid>.pid (0 if unreadable)
	SessionID  string // session the broker serves, derived from the socket basename
	Socket     string // --socket path
	ACP        bool   // true for _acp_broker, false for _structured_broker
	DeletedExe bool   // exe link points to a deleted/overwritten binary
	StartTime  uint64 // process start time (proc stat field 22); orders duplicate brokers
}

// brokerSubcommands are the hidden re-exec subcommands that mark a process as
// a session broker. The bool value is the broker's ACP flag.
var brokerSubcommands = map[string]bool{
	"_structured_broker": false,
	"_acp_broker":        true,
}

// brokerSocketSuffixes are the per-session socket file suffixes, used both to
// recognise a broker socket and to recover the session id from its basename.
var brokerSocketSuffixes = []string{".structured.sock", ".acp.sock"}

// ListBrokerProcesses enumerates live session-broker subprocesses on this
// host. On non-Linux platforms there is no procfs, so it returns nil: the
// process reaper is a no-op there (the session-file reaper still runs).
func ListBrokerProcesses() ([]BrokerProcess, error) {
	return listBrokerProcesses(defaultProcRoot)
}

// listBrokerProcesses scans procRoot (a procfs mount, "/proc" in production)
// for broker processes. It is split from ListBrokerProcesses so tests can
// point it at a fixture directory.
func listBrokerProcesses(procRoot string) ([]BrokerProcess, error) {
	if procRoot == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var out []BrokerProcess
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if bp, ok := readBrokerProcess(procRoot, pid); ok {
			out = append(out, bp)
		}
	}
	return out, nil
}

// readBrokerProcess reads procRoot/<pid> and returns a BrokerProcess if the
// process is a session broker, or ok=false otherwise.
func readBrokerProcess(procRoot string, pid int) (BrokerProcess, bool) {
	dir := filepath.Join(procRoot, strconv.Itoa(pid))
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return BrokerProcess{}, false
	}
	bp, ok := parseBrokerCmdline(pid, cmdline)
	if !ok {
		return BrokerProcess{}, false
	}
	// The exe link carries a " (deleted)" suffix when the binary was
	// overwritten (e.g. by `amux update`) after the process was exec'd.
	if exe, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		bp.DeletedExe = strings.HasSuffix(exe, " (deleted)")
	}
	bp.StartTime = readProcStartTime(filepath.Join(dir, "stat"))
	bp.AgentPID = readAgentPID(pidFileForSocket(bp.Socket))
	return bp, true
}

// StillBroker re-reads the broker's cmdline and reports whether the process at
// bp.PID is still a broker serving the same session. The reaper calls this
// immediately before signalling so a PID that died and was reused by an
// unrelated process is never signalled.
func (bp BrokerProcess) StillBroker() bool {
	cur, ok := readBrokerProcess(defaultProcRoot, bp.PID)
	return ok && cur.SessionID == bp.SessionID
}

// parseBrokerCmdline parses a NUL-separated /proc cmdline. argv[1] must be a
// broker subcommand. Flag parsing stops at the "--" separator so agent
// ExtraArgs are never mistaken for broker flags. The session id is recovered
// from the --socket basename because the ACP broker has no --session-id flag.
func parseBrokerCmdline(pid int, cmdline []byte) (BrokerProcess, bool) {
	var argv []string
	for _, a := range strings.Split(string(cmdline), "\x00") {
		if a != "" {
			argv = append(argv, a)
		}
	}
	if len(argv) < 2 {
		return BrokerProcess{}, false
	}
	acp, isBroker := brokerSubcommands[argv[1]]
	if !isBroker {
		return BrokerProcess{}, false
	}
	bp := BrokerProcess{PID: pid, ACP: acp}
	for i := 2; i < len(argv); i++ {
		if argv[i] == "--" {
			break
		}
		if i+1 >= len(argv) {
			break
		}
		switch argv[i] {
		case "--socket":
			bp.Socket = argv[i+1]
			i++
		case "--session-id":
			bp.SessionID = argv[i+1]
			i++
		}
	}
	// The socket basename is the authoritative session-id source (and the only
	// one for ACP brokers).
	if sid, ok := sessionIDFromSocket(bp.Socket); ok {
		bp.SessionID = sid
	}
	if bp.SessionID == "" || bp.Socket == "" {
		return BrokerProcess{}, false
	}
	return bp, true
}

// sessionIDFromSocket maps ".../sessions/<sid>.structured.sock" or
// ".../sessions/<sid>.acp.sock" to <sid>.
func sessionIDFromSocket(sock string) (string, bool) {
	base := filepath.Base(sock)
	for _, suf := range brokerSocketSuffixes {
		if strings.HasSuffix(base, suf) {
			if sid := strings.TrimSuffix(base, suf); sid != "" {
				return sid, true
			}
		}
	}
	return "", false
}

// pidFileForSocket maps a broker socket path to the shared per-session pid
// file "<sid>.pid" sitting alongside it (both broker protocols write the inner
// agent pid to <sid>.pid, not the broker pid).
func pidFileForSocket(sock string) string {
	base := filepath.Base(sock)
	for _, suf := range brokerSocketSuffixes {
		if strings.HasSuffix(base, suf) {
			return filepath.Join(filepath.Dir(sock), strings.TrimSuffix(base, suf)+".pid")
		}
	}
	return ""
}

// readAgentPID reads the inner agent pid from the per-session pid file.
func readAgentPID(pidFile string) int {
	if pidFile == "" {
		return 0
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

// readProcStartTime extracts the process start time (field 22) from a proc
// stat file. The comm field (2) is parenthesised and may itself contain spaces
// or parens, so fields are counted from after the final ')'.
func readProcStartTime(statPath string) uint64 {
	fields := procStatFields(statPath)
	// fields[0] == state (field 3); starttime is field 22 -> index 19.
	if len(fields) <= 19 {
		return 0
	}
	st, _ := strconv.ParseUint(fields[19], 10, 64)
	return st
}

// procStatPPID extracts the parent pid (field 4) from a proc stat file.
func procStatPPID(statPath string) int {
	fields := procStatFields(statPath)
	// fields[0] == state (field 3); ppid is field 4 -> index 1.
	if len(fields) <= 1 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// procStatFields splits a proc stat file into the fields that follow the
// parenthesised comm field, so field N of the stat line is at index N-3.
func procStatFields(statPath string) []string {
	data, err := os.ReadFile(statPath)
	if err != nil {
		return nil
	}
	s := string(data)
	rparen := strings.LastIndex(s, ")")
	if rparen < 0 {
		return nil
	}
	return strings.Fields(s[rparen+1:])
}

// IsChildOf reports whether the process childPID is currently a child of this
// broker. The reaper uses it to confirm the agent pid read from <sid>.pid
// really belongs to this broker before signalling it, so a reused agent pid is
// never signalled.
func (bp BrokerProcess) IsChildOf(childPID int) bool {
	if childPID <= 0 || defaultProcRoot == "" {
		return false
	}
	statPath := filepath.Join(defaultProcRoot, strconv.Itoa(childPID), "stat")
	return procStatPPID(statPath) == bp.PID
}

// SignalGroup sends sig to the broker's process group and to its inner agent's
// process group. Brokers and agents are each Setsid group leaders, so each is
// signalled by its own (negative) pid; the two groups are independent and must
// both be signalled. Callers should still confirm the target with StillBroker
// / IsChildOf first.
func (bp BrokerProcess) SignalGroup(sig syscall.Signal) {
	signalProcessGroup(bp.PID, sig)
	if bp.IsChildOf(bp.AgentPID) {
		signalProcessGroup(bp.AgentPID, sig)
	}
}

// signalProcessGroup signals a process group led by pid, falling back to the
// single process when the group signal fails (e.g. the group is already gone).
func signalProcessGroup(pid int, sig syscall.Signal) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}
