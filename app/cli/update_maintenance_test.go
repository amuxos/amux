package cli

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type maintenanceFixture struct {
	pb.UnimplementedRuntimeMaintenanceServer
	mu      sync.Mutex
	phase   string
	busy    bool
	actions []string
	token   string
}

func (f *maintenanceFixture) Maintain(_ context.Context, req *pb.MaintenanceRequest) (*pb.MaintenanceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, req.Action)
	switch req.Action {
	case "begin":
		if f.token != "" && f.token != req.Token {
			return nil, status.Error(codes.AlreadyExists, "other updater")
		}
		f.token = req.Token
		f.phase = "draining"
	case "poll":
		if !f.busy {
			f.phase = "ready"
		}
	case "end":
		f.phase = "idle"
		f.token = ""
	}
	return &pb.MaintenanceResponse{Phase: f.phase, BusySessions: []string{"busy-session"}, ActiveTasks: 1}, nil
}
func setupMaintenanceFixture(t *testing.T, f *maintenanceFixture) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "amux-maint-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("AMUX_HOME", dir)
	lis, err := net.Listen("unix", filepath.Join(dir, "runtime.sock"))
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterRuntimeMaintenanceServer(srv, f)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	old := updateMaintenanceNeeded
	updateMaintenanceNeeded = func() (bool, error) { return true, nil }
	t.Cleanup(func() { updateMaintenanceNeeded = old })
}
func TestMaintenanceTimeoutDoesNotInstallOrCancelWork(t *testing.T) {
	f := &maintenanceFixture{busy: true}
	setupMaintenanceFixture(t, f)
	source := writeFakeInstallSource(t, "new")
	target := filepath.Join(defaultAmuxDir(), "bin")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "amux")
	if err := os.WriteFile(sentinel, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--source", source, "--drain-timeout", "50ms"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("timeout missing: %v", err)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil || string(got) != "old" {
		t.Fatalf("binaries changed on timeout: %q %v", got, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.phase != "idle" || f.actions[len(f.actions)-1] != "end" {
		t.Fatalf("not recovered: %v", f.actions)
	}
}
func TestMaintenanceHelperFailureRestoresBeforeReturning(t *testing.T) {
	f := &maintenanceFixture{}
	setupMaintenanceFixture(t, f)
	old := updateIsolationHelper
	t.Cleanup(func() { updateIsolationHelper = old })
	cause := errors.New("helper upgrade failed")
	updateIsolationHelper = func(_cmd *cobra.Command, _opts *installOptions) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.phase != "ready" {
			t.Fatal("helper before drain")
		}
		return cause
	}
	cmd := newUpdateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--post-install"})
	if err := cmd.Execute(); !errors.Is(err, cause) {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.phase != "idle" {
		t.Fatal("admission not restored")
	}
	if strings.Contains(out.String(), "Updated amux") {
		t.Fatal("false success")
	}
}
func TestMaintenanceHandoffPreservesLeaseAndTimeout(t *testing.T) {
	cmd := newUpdateCmd()
	old := updatePostInstallRunner
	t.Cleanup(func() { updatePostInstallRunner = old })
	updatePostInstallRunner = func(_cmd *cobra.Command, _ string, args []string) error {
		got := strings.Join(args, " ")
		if !strings.Contains(got, "--maintenance-token token-value") || !strings.Contains(got, "--drain-timeout 42s") {
			t.Fatal(got)
		}
		return nil
	}
	if err := runInstalledUpdatePostInstall(cmd, &installOptions{maintenanceToken: "token-value", drainTimeout: 42 * time.Second}); err != nil {
		t.Fatal(err)
	}
}
func TestMaintenanceCopyLanguages(t *testing.T) {
	cmd := newUpdateCmd()
	for _, key := range []string{"draining", "ready", "failed", "legacy", "timeout", "recovery_failed", "drain_timeout_help", "invalid_timeout", "expired", "restored"} {
		t.Setenv("AMUX_LANGUAGE", "en")
		en := maintenanceText(cmd, key)
		t.Setenv("AMUX_LANGUAGE", "zh-CN")
		zh := maintenanceText(cmd, key)
		t.Setenv("AMUX_LANGUAGE", "fr")
		t.Setenv("LC_ALL", "C")
		fallback := maintenanceText(cmd, key)
		if en == zh || fallback != en || strings.Contains(en, "update.maintenance.") {
			t.Fatalf("translations for %s: %s / %s", key, en, zh)
		}
	}
}

func TestMaintenanceRecoveryWaitTracksPendingSessions(t *testing.T) {
	// Inspect RPC deadlines directly so the test does not depend on scheduler timing.
	var endDeadlines []time.Time
	pending := []int32{12, 10, 10}
	n := 0
	call := func(ctx context.Context, action string) (*pb.MaintenanceResponse, error) {
		if action == "end" {
			deadline, _ := ctx.Deadline()
			endDeadlines = append(endDeadlines, deadline)
			if n == len(pending) {
				return &pb.MaintenanceResponse{Phase: "idle"}, nil
			}
			return nil, context.DeadlineExceeded
		}
		if action != "status" {
			t.Fatalf("unexpected action %q", action)
		}
		r := &pb.MaintenanceResponse{Phase: "recovering", SleepingSessions: pending[n]}
		n++
		return r, nil
	}
	if err := waitForMaintenanceRecovery(call, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !endDeadlines[1].Equal(endDeadlines[0]) || !endDeadlines[2].After(endDeadlines[1]) || !endDeadlines[3].Equal(endDeadlines[2]) {
		t.Fatalf("deadlines %v", endDeadlines)
	}
}
