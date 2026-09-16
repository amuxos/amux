package cli

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestManagedServiceStopKillsProcessThatIgnoresTerm(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap '' TERM; while :; do sleep 1; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stubborn process: %v", err)
	}
	pid := cmd.Process.Pid
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	pidPath := filepath.Join(t.TempDir(), "service.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)+"\n"), 0644); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	svc := managedService{Name: "stubborn", PIDPath: pidPath}
	start := time.Now()
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if time.Since(start) > 7*time.Second {
		t.Fatalf("Stop took too long: %s", time.Since(start))
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid file should be removed, err=%v", err)
	}
	_, _ = cmd.Process.Wait()
	waited = true
	if processRunning(pid) {
		t.Fatalf("process %d should be stopped", pid)
	}
}

func TestManagedServiceStopRemovesStalePIDFile(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "service.pid")
	if err := os.WriteFile(pidPath, []byte("999999999\n"), 0644); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	svc := managedService{Name: "stale", PIDPath: pidPath}
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop stale pid: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid file should be removed, err=%v", err)
	}
}

func TestProcessRunningFalseAfterWaitedProcessExit(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait process: %v", err)
	}
	if processRunning(pid) {
		t.Fatalf("waited process %d should not be running", pid)
	}
}

func TestProcessRunningTrueForCurrentProcess(t *testing.T) {
	if !processRunning(syscall.Getpid()) {
		t.Fatal("current process should be running")
	}
}

func TestAddrInUseReflectsListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if !addrInUse(addr) {
		t.Fatalf("addr %s should report in use while listener is open", addr)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !waitForAddrFree(addr, 2*time.Second) {
		t.Fatalf("addr %s should become free after listener closes", addr)
	}
}

// freeLoopbackAddr returns a 127.0.0.1 addr that was just bound and released, so
// it is very likely free for an immediate rebind by a child process.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestPidsListeningOnFindsListener(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	pids := pidsListeningOn(addr)
	self := os.Getpid()
	found := false
	for _, pid := range pids {
		if pid == self {
			found = true
		}
	}
	if !found {
		t.Fatalf("pidsListeningOn(%s) = %v, expected to include our pid %d", addr, pids, self)
	}
}

func TestManagedServiceStopReclaimsOrphanOnListenAddr(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available; orphan reclamation relies on it")
	}
	addr := freeLoopbackAddr(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not available to spawn an orphan listener")
	}
	// An orphan listener whose pid is NOT in the pid file. It exits on SIGTERM
	// (the SIGKILL escalation is covered separately) so the test stays fast; its
	// job is to prove addr-based discovery + termination frees the port. Try the
	// common nc invocations.
	var cmd *exec.Cmd
	for _, inv := range []string{
		"exec nc -l 127.0.0.1 " + port,
		"exec nc -l " + port,
	} {
		c := exec.Command("sh", "-c", inv)
		if err := c.Start(); err != nil {
			continue
		}
		if waitForAddrInUse(addr, 2*time.Second) {
			cmd = c
			break
		}
		_ = c.Process.Kill()
		_, _ = c.Process.Wait()
	}
	if cmd == nil {
		t.Skip("could not spawn an nc orphan listener on this platform")
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Pid file points at a dead pid; the live holder is the orphan on ListenAddr.
	pidPath := filepath.Join(t.TempDir(), "hub.pid")
	if err := os.WriteFile(pidPath, []byte("999999999\n"), 0644); err != nil {
		t.Fatalf("write pid: %v", err)
	}
	executable, err := processExecutablePath(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("resolve orphan executable: %v", err)
	}
	svc := managedService{Name: "hub", PIDPath: pidPath, ListenAddr: addr, ExpectedExecutable: executable}
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop should reclaim orphan and succeed: %v", err)
	}
	if addrInUse(addr) {
		t.Fatalf("addr %s should be free after Stop reclaimed the orphan", addr)
	}
}

func TestManagedServiceStopDoesNotReclaimUnrelatedExecutable(t *testing.T) {
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not available to spawn an unrelated listener")
	}
	addr := freeLoopbackAddr(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	cmd := exec.Command("sh", "-c", "exec nc -l 127.0.0.1 "+port)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start unrelated listener: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if !waitForAddrInUse(addr, 2*time.Second) {
		t.Skip("nc did not acquire the listener")
	}

	svc := managedService{
		Name:               "hub",
		Binary:             "amuxd",
		PIDPath:            filepath.Join(t.TempDir(), "hub.pid"),
		ListenAddr:         addr,
		ExpectedExecutable: filepath.Join(t.TempDir(), "amuxd"),
	}
	if err := svc.Stop(); err == nil {
		t.Fatal("Stop should fail while an unrelated executable owns the address")
	}
	if !processRunning(cmd.Process.Pid) || !addrInUse(addr) {
		t.Fatal("Stop terminated an unrelated listener")
	}
}

func TestManagedServiceStopFailsHonestlyWhenAddrStaysHeld(t *testing.T) {
	// A live listener holds the addr; with no pid file and lsof unable to find a
	// killable owner (we pretend none), Stop must return an error rather than
	// falsely reporting success. We simulate "cannot reclaim" by holding the
	// listener in-process: pidsListeningOn would find our own pid, which Stop
	// skips, so the addr stays in use -> honest error.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	pidPath := filepath.Join(t.TempDir(), "hub.pid") // no file on disk
	svc := managedService{Name: "hub", PIDPath: pidPath, ListenAddr: addr}
	err = svc.Stop()
	if err == nil {
		t.Fatal("Stop should fail honestly while the addr is still held")
	}
}

func TestManagedServiceStartRefusesOccupiedAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	svc := managedService{
		Name:       "hub",
		Binary:     "amuxd",
		PIDPath:    filepath.Join(t.TempDir(), "hub.pid"),
		LogPath:    filepath.Join(t.TempDir(), "hub.log"),
		ListenAddr: addr,
	}
	err = svc.Start(false)
	if err == nil {
		t.Fatal("Start should refuse to spawn over an occupied addr")
	}
	if isAlreadyRunning(err) {
		t.Fatalf("expected occupied-addr refusal, got already-running: %v", err)
	}
}

func TestResolveSiblingBinaryPathUsesParentInstallDirFromUpdateTempDir(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "bin")
	executable := filepath.Join(binDir, ".amux-backup-123456", "amux")

	got := resolveSiblingBinaryPath("amuxlet", executable)
	want := filepath.Join(binDir, "amuxlet")
	if got != want {
		t.Fatalf("resolveSiblingBinaryPath() = %q, want %q", got, want)
	}
}

func waitForAddrInUse(addr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if addrInUse(addr) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return addrInUse(addr)
}
