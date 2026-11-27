package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	serviceStopGracePeriod  = 5 * time.Second
	serviceStopPollInterval = 100 * time.Millisecond
	// serviceAddrFreeTimeout bounds how long Stop waits for the listen socket to
	// be released after a kill before treating the port as held by an orphan.
	serviceAddrFreeTimeout = 2 * time.Second
)

type managedService struct {
	Name   string
	Binary string
	// ExpectedExecutable optionally overrides the executable identity used by
	// orphan-listener reclamation. Production services leave it empty and use
	// the sibling Binary path; focused tests may point it at their owned child.
	ExpectedExecutable string
	Args               []string
	PIDPath            string
	LogPath            string
	StartedAs          string
	// ListenAddr, when set, is the TCP address the service binds. Stop confirms
	// it is actually released (reclaiming an orphan that the pid file no longer
	// tracks) and Start refuses to spawn over an addr already held by an
	// unmanaged process. Empty for unix-socket services (e.g. the runtime).
	ListenAddr string
	// Optional timeout overrides for focused tests; zero values use production defaults.
	stopGracePeriod time.Duration
	addrFreeTimeout time.Duration
}

func (s managedService) gracePeriod() time.Duration {
	if s.stopGracePeriod > 0 {
		return s.stopGracePeriod
	}
	return serviceStopGracePeriod
}

func (s managedService) addrTimeout() time.Duration {
	if s.addrFreeTimeout > 0 {
		return s.addrFreeTimeout
	}
	return serviceAddrFreeTimeout
}

func (s managedService) Start(foreground bool) error {
	if foreground {
		return runSiblingBinary(s.Binary, s.Args...)
	}
	if err := os.MkdirAll(filepath.Dir(s.PIDPath), 0755); err != nil {
		return err
	}
	// Serialize concurrent Start attempts (e.g. parallel auto-starts from
	// `amux run`) so the running check and the spawn+pid-write below are atomic
	// with respect to other amux processes. Without this, two callers could both
	// observe "not running" and each spawn a daemon. The amuxlet socket flock is
	// the authoritative backstop; this just avoids the wasted spawn and noise.
	unlock, err := lockServiceStart(s.PIDPath)
	if err != nil {
		return err
	}
	defer unlock()
	if pid, ok := readRunningPID(s.PIDPath); ok {
		return &alreadyRunningError{name: s.Name, pid: pid}
	}
	// Guard against the doomed-spawn loop: if the listen addr is already held by
	// a process we don't track (e.g. an orphaned daemon the pid file lost), do
	// not fork a new daemon that will fail to bind and overwrite the pid file
	// with its short-lived pid. Refuse with an actionable message instead.
	if s.ListenAddr != "" && addrInUse(s.ListenAddr) {
		msg := fmt.Sprintf("%s addr %s already in use by an unmanaged process", s.Name, s.ListenAddr)
		if pids := pidsListeningOn(s.ListenAddr); len(pids) > 0 {
			msg += fmt.Sprintf(" (pid %v)", pids)
		}
		return fmt.Errorf("%s; run 'amux %s stop' or kill it first", msg, s.Name)
	}
	logFile, err := os.OpenFile(s.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer logFile.Close()

	path, err := siblingBinaryPath(s.Binary)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, s.Args...)
	cmd.Env = stripAgentContextEnv(os.Environ())
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureManagedServiceProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(s.PIDPath, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0644); err != nil {
		_ = signalServiceProcess(cmd.Process.Pid, cmd.Process, false)
		return err
	}
	return cmd.Process.Release()
}

func (s managedService) Stop() error {
	if err := s.stopRecordedPID(); err != nil {
		return err
	}
	if s.ListenAddr == "" {
		return nil
	}
	// The recorded pid may have been wrong (stale pid file pointing away from the
	// process that actually holds the port), or the OS may need a moment to
	// release the listen socket after the kill. Confirm the addr is truly free;
	// reclaim any orphan still holding it, and fail honestly if we cannot.
	if waitForAddrFree(s.ListenAddr, s.addrTimeout()) {
		return nil
	}
	if err := s.reclaimAddr(); err != nil {
		return err
	}
	if !waitForAddrFree(s.ListenAddr, s.addrTimeout()) {
		return fmt.Errorf("%s addr %s still in use after stop; kill the process holding it manually", s.Name, s.ListenAddr)
	}
	return nil
}

// stopRecordedPID terminates the process named in the pid file (SIGTERM, then
// SIGKILL after the grace period) and removes the pid file.
func (s managedService) stopRecordedPID() error {
	pid, ok := readPIDFile(s.PIDPath)
	if !ok {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil || !processRunning(pid) {
		_ = os.Remove(s.PIDPath)
		return nil
	}
	if err := s.stopProcess(pid, proc); err != nil {
		return err
	}
	_ = os.Remove(s.PIDPath)
	return nil
}

// stopProcess escalates SIGTERM -> wait -> SIGKILL on a single process (group).
func (s managedService) stopProcess(pid int, proc *os.Process) error {
	if err := signalServiceProcess(pid, proc, false); err != nil && !isProcessFinished(err) {
		return err
	}
	grace := s.gracePeriod()
	if waitForProcessExit(pid, grace) {
		return nil
	}
	if err := signalServiceProcess(pid, proc, true); err != nil && !isProcessFinished(err) {
		return fmt.Errorf("%s did not stop within %s and kill failed (pid %d): %w", s.Name, grace, pid, err)
	}
	return nil
}

// reclaimAddr terminates any process (other than ourselves) still listening on
// the service's addr — the orphan that the pid file lost track of.
func (s managedService) reclaimAddr() error {
	self := os.Getpid()
	for _, pid := range pidsListeningOn(s.ListenAddr) {
		if pid <= 0 || pid == self {
			continue
		}
		owned, err := s.ownsExecutable(pid)
		if err != nil || !owned {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if err := s.stopProcess(pid, proc); err != nil {
			return fmt.Errorf("reclaim %s addr %s: %w", s.Name, s.ListenAddr, err)
		}
	}
	return nil
}

func (s managedService) ownsExecutable(pid int) (bool, error) {
	expected := strings.TrimSpace(s.ExpectedExecutable)
	if expected == "" {
		if strings.TrimSpace(s.Binary) == "" {
			return false, nil
		}
		var err error
		expected, err = siblingBinaryPath(s.Binary)
		if err != nil {
			return false, err
		}
	}
	actual, err := processExecutablePath(pid)
	if err != nil {
		return false, err
	}
	return sameExecutablePath(actual, expected), nil
}

func processExecutablePath(pid int) (string, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("process %d executable path is empty", pid)
	}
	return path, nil
}

func sameExecutablePath(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	if !filepath.IsAbs(left) || !filepath.IsAbs(right) {
		return false
	}
	resolvedLeft, leftErr := filepath.EvalSymlinks(left)
	resolvedRight, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(resolvedLeft) == filepath.Clean(resolvedRight)
}

func isProcessFinished(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "process already finished")
}

// addrInUse reports whether a TCP addr is currently held by an active listener.
// It probes by attempting to bind: success means free, EADDRINUSE means held.
// Other bind errors are treated as "not detectably in use" so we don't block on
// unrelated failures (the daemon will surface a real bind error itself).
func addrInUse(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		_ = ln.Close()
		return false
	}
	return addressInUseError(err) || strings.Contains(strings.ToLower(err.Error()), "address already in use")
}

// waitForAddrFree polls until the addr is bindable or the deadline passes.
func waitForAddrFree(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !addrInUse(addr) {
			return true
		}
		if time.Now().After(deadline) {
			return !addrInUse(addr)
		}
		time.Sleep(serviceStopPollInterval)
	}
}

// pidsListeningOn returns the pids of processes listening on addr, best-effort,
// via lsof. It returns nil when lsof is absent or finds nothing — callers must
// degrade gracefully (honest error) rather than depend on it.
func pidsListeningOn(addr string) []int {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return nil
	}
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return nil
	}
	netspec := "TCP:" + port
	if host != "" && host != "0.0.0.0" && host != "::" && host != "*" {
		netspec = "TCP@" + host + ":" + port
	}
	out, err := exec.Command(lsof, "-nP", "-sTCP:LISTEN", "-t", "-i"+netspec).Output()
	if err != nil {
		return nil
	}
	seen := map[int]bool{}
	var pids []int
	for _, line := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && pid > 0 && !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	return pids
}

func (s managedService) Restart(foreground bool) error {
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start(foreground)
}

// alreadyRunningError reports that a managed service was found already running
// when a Start was attempted. Auto-start callers treat it as success (the
// daemon they wanted is up); explicit `runtime start` surfaces it to the user.
type alreadyRunningError struct {
	name string
	pid  int
}

func (e *alreadyRunningError) Error() string {
	return fmt.Sprintf("%s already running (pid %d)", e.name, e.pid)
}

func isAlreadyRunning(err error) bool {
	var e *alreadyRunningError
	return errors.As(err, &e)
}

// lockServiceStart takes a blocking exclusive advisory lock keyed on the PID
// file path, so concurrent Start attempts run their check-spawn-write sequence
// one at a time. The returned func releases the lock by closing the fd.
func lockServiceStart(pidPath string) (func(), error) {
	f, err := os.OpenFile(pidPath+".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open start lock: %w", err)
	}
	if err := lockServiceFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquire start lock: %w", err)
	}
	return func() { _ = f.Close() }, nil
}

func siblingBinaryPath(name string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return resolveSiblingBinaryPath(name, exe), nil
}

func resolveSiblingBinaryPath(name, executablePath string) string {
	dir := filepath.Dir(executablePath)
	base := filepath.Base(dir)
	if strings.HasPrefix(base, ".amux-backup-") || strings.HasPrefix(base, ".amux-stage-") {
		dir = filepath.Dir(dir)
	}
	return filepath.Join(dir, exeName(name))
}

// exeName returns the on-disk binary name for the current OS.
func exeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}

func readRunningPID(path string) (int, bool) {
	pid, ok := readPIDFile(path)
	if !ok || !processRunning(pid) {
		return 0, false
	}
	return pid, true
}

func readPIDFile(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid, err == nil && pid > 0
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	running, err := platformProcessRunning(pid)
	if err != nil || !running {
		return false
	}
	if zombie, ok := processZombie(pid); ok {
		return !zombie
	}
	return true
}

func processZombie(pid int) (bool, bool) {
	if zombie, ok := processZombieFromProc(pid); ok {
		return zombie, true
	}
	return processZombieFromPS(pid)
}

func processZombieFromProc(pid int) (bool, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return false, false
	}
	return fields[2] == "Z", true
}

func waitForProcessExit(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !processRunning(pid) {
			return true
		}
		time.Sleep(serviceStopPollInterval)
	}
	return !processRunning(pid)
}

func processZombieFromPS(pid int) (bool, bool) {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false, false
	}
	stat := strings.TrimSpace(string(out))
	if stat == "" {
		return false, true
	}
	return strings.HasPrefix(stat, "Z"), true
}

func processCommandLine(pid int) (string, bool) {
	if pid <= 0 {
		return "", false
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", false
	}
	commandLine := strings.TrimSpace(string(out))
	return commandLine, commandLine != ""
}
