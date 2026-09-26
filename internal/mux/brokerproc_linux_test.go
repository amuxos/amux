//go:build linux

package mux

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// TestListBrokerProcessesHostProc runs against the host's real /proc to
// validate the procfs parsing end to end (real cmdline/stat/exe formats). It
// is tolerant: it only requires that enumeration succeeds and that any broker
// found is well-formed. On a host with live brokers it should find them.
func TestListBrokerProcessesHostProc(t *testing.T) {
	brokers, err := ListBrokerProcesses()
	if err != nil {
		t.Fatalf("ListBrokerProcesses: %v", err)
	}
	for _, bp := range brokers {
		if bp.PID <= 0 {
			t.Errorf("bad pid: %+v", bp)
		}
		if bp.SessionID == "" {
			t.Errorf("broker missing session id: %+v", bp)
		}
		if bp.Socket == "" {
			t.Errorf("broker missing socket: %+v", bp)
		}
		if bp.StartTime == 0 {
			t.Errorf("broker missing start time (proc stat parse failed): %+v", bp)
		}
	}
	t.Logf("enumerated %d live broker(s) on host", len(brokers))
}

// TestKillGroupKillsSetsidProcess verifies the destructive core of the
// reaper: signalling a Setsid group leader by its (negative) pid kills it.
func TestKillGroupKillsSetsidProcess(t *testing.T) {
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("sleep %d not running: %v", pid, err)
	}
	BrokerProcess{PID: pid}.KillGroup()
	_ = cmd.Wait() // reap so the pid is fully released
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("process %d still alive after KillGroup()", pid)
	}
}

// TestIsChildOf verifies the agent-pid ownership guard against real /proc.
func TestIsChildOf(t *testing.T) {
	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() { _ = child.Process.Kill(); _, _ = child.Process.Wait() }()

	me := BrokerProcess{PID: os.Getpid()}
	if !me.IsChildOf(child.Process.Pid) {
		t.Errorf("IsChildOf(own child %d) = false, want true", child.Process.Pid)
	}
	if me.IsChildOf(1) {
		t.Errorf("IsChildOf(init) = true, want false")
	}
	if me.IsChildOf(0) || me.IsChildOf(-7) {
		t.Errorf("IsChildOf(non-positive) = true, want false")
	}
}
