//go:build linux

package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/isolation"
)

// Explicit opt-in. The fixture owns only temporary files and helper processes;
// it substitutes systemd process control and never touches an installed unit.
func TestHelperUpgradeRootIntegration(t *testing.T) {
	if os.Getenv("AMUX_HELPER_UPGRADE_E2E") != "1" {
		t.Skip("explicit helper upgrade opt-in required")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root required")
	}
	freshBinary, oldBinary := os.Getenv("AMUX_TEST_HELPER_BINARY"), os.Getenv("AMUX_TEST_OLD_HELPER_BINARY")
	if freshBinary == "" || oldBinary == "" {
		t.Fatal("old and new helper binaries required")
	}
	fresh, err := os.ReadFile(freshBinary)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(oldBinary)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(fresh, old) {
		t.Fatal("test requires different helper binaries")
	}
	root, err := os.MkdirTemp("/tmp", "amux-helper-upgrade-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	for _, dir := range []string{source, target} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for path, data := range map[string][]byte{
		filepath.Join(source, "amux-isolation-helper"): fresh, filepath.Join(source, "amuxlet"): fresh,
		filepath.Join(target, "amux-isolation-helper"): old, filepath.Join(target, "amuxlet-materializer"): old} {
		if err := os.WriteFile(path, data, 0755); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(root, "helper.sock")
	client := isolation.NewHelperClient(socket)
	var process *exec.Cmd
	var done chan error
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	hooks := helperUpgradeHooks{running: true,
		probe: func(ctx context.Context) (int, func(), error) { return guardHelperForUpgrade(ctx, socket) },
		stop: func(ctx context.Context) error {
			if process == nil {
				return nil
			}
			_ = process.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				process = nil
				return err
			case <-ctx.Done():
				_ = process.Process.Kill()
				<-done
				process = nil
				return ctx.Err()
			}
		},
		start: func(ctx context.Context) error {
			process = exec.Command(filepath.Join(target, "amux-isolation-helper"), "serve", "--socket", socket, "--state-dir", filepath.Join(root, "state"), "--principal-root", filepath.Join(root, "principals"), "--service-uid", "0", "--service-gid", "0", "--uid-range-start", "790991", "--uid-range-end", "790991", "--materializer-path", filepath.Join(target, "amuxlet-materializer"), "--pid-namespace=false", "--mount-namespace=false", "--seccomp=false")
			process.Stdout = os.Stdout
			process.Stderr = os.Stderr
			if err := process.Start(); err != nil {
				process = nil
				return err
			}
			done = make(chan error, 1)
			go func(p *exec.Cmd) { done <- p.Wait() }(process)
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				if _, err := client.Handshake(ctx); err == nil {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(25 * time.Millisecond):
				}
			}
			return fmt.Errorf("helper not ready")
		},
		runningMatches: func(want []byte) bool {
			if process == nil {
				return false
			}
			data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(process.Process.Pid), "exe"))
			return err == nil && bytes.Equal(data, want)
		},
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := hooks.stop(cleanup); err != nil {
			t.Error(err)
		}
	}()
	if err := hooks.start(ctx); err != nil {
		t.Fatal(err)
	}
	oldPID := process.Process.Pid
	result := upgradeHelperFiles(ctx, source, target, hooks, installFileAtomically)
	if result.Status != "failed" || result.Reason != "legacy_helper" || process.Process.Pid != oldPID || !hooks.runningMatches(old) {
		t.Fatalf("unsafe legacy upgrade: %+v", result)
	}
	// Bootstrap a legacy helper only after the operator has stopped it. A stopped
	// installation is updated automatically and remains stopped.
	if err := hooks.stop(ctx); err != nil {
		t.Fatal(err)
	}
	hooks.running = false
	result = upgradeHelperFiles(ctx, source, target, hooks, installFileAtomically)
	if result.Status != "updated" || process != nil {
		t.Fatalf("stopped migration: %+v", result)
	}
	if err := hooks.start(ctx); err != nil {
		t.Fatal(err)
	}
	hooks.running = true
	// A subsequent change exercises automatic guarded replacement and restart.
	upgradedMaterializer := append(append([]byte{}, fresh...), byte(10))
	if err := os.WriteFile(filepath.Join(source, "amuxlet"), upgradedMaterializer, 0755); err != nil {
		t.Fatal(err)
	}
	oldPID = process.Process.Pid
	result = upgradeHelperFiles(ctx, source, target, hooks, installFileAtomically)
	if result.Status != "updated" || process == nil || process.Process.Pid == oldPID || !hooks.runningMatches(fresh) {
		t.Fatalf("guarded upgrade: %+v", result)
	}
	if _, err := client.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	pid := process.Process.Pid
	if result := upgradeHelperFiles(ctx, source, target, hooks, installFileAtomically); result.Status != "unchanged" || process.Process.Pid != pid {
		t.Fatalf("idempotence: %+v", result)
	}
	// Force an executable-format failure after replacing both files. Recovery must
	// restart the previous real helper, not merely restore files under a stale PID.
	if err := os.WriteFile(filepath.Join(source, "amux-isolation-helper"), []byte("invalid executable"), 0755); err != nil {
		t.Fatal(err)
	}
	result = upgradeHelperFiles(ctx, source, target, hooks, installFileAtomically)
	if result.Status != "failed" || !hooks.runningMatches(fresh) {
		t.Fatalf("recovery: %+v", result)
	}
	if _, err := client.Handshake(ctx); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "amuxlet-materializer"))
	if err != nil || !bytes.Equal(data, upgradedMaterializer) {
		t.Fatalf("materializer rollback: %v", err)
	}
	t.Log("legacy refusal and stopped migration, guarded helper replacement, idempotence and failed-start rollback passed; no installed service changed")
}
