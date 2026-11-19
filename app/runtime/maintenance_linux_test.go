//go:build linux

package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/google/uuid"
)

func TestSessionCredentialCommandResolvesPATHInLinuxIsolation(t *testing.T) {
	binary, uidText := os.Getenv("AMUX_TEST_HELPER_BINARY"), os.Getenv("AMUX_TEST_MAINTENANCE_UID")
	if os.Geteuid() != 0 || binary == "" || uidText == "" {
		t.Skip("requires root, helper binary and dedicated test uid")
	}
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid < 65536 {
		t.Fatal("invalid test uid")
	}
	root, err := os.MkdirTemp("/tmp", "amux-credential-linux-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "credential-cli")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" \"$CHECK_VALUE\"\nprintf credential-stderr >&2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := isolation.HelperConfig{
		SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"), PrincipalRoot: filepath.Join(root, "principals"),
		ServiceUID: 0, ServiceGID: 0, UIDRangeStart: uid, UIDRangeEnd: uid, MaterializerPath: binary, ExecWrapperPath: binary,
		SkipAccountProvision: true, PIDNamespace: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- isolation.ServeHelper(ctx, cfg) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client := isolation.NewHelperClient(cfg.SocketPath)
	for {
		if _, err := client.Handshake(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	rt, err := isolation.NewRuntime(config.IsolationConfig{
		Enabled: true, Backend: principal.BackendLinuxDAC, UIDRangeStart: uid, UIDRangeEnd: uid,
		HelperSocket: cfg.SocketPath, MappingDB: filepath.Join(root, "mappings.db"), PrincipalRoot: cfg.PrincipalRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	principalID := uuid.NewString()
	identity := principal.ExecutionIdentity{
		IdentityMode: principal.IdentityModeIdentified, EffectivePrincipalID: principalID, OriginActorPrincipalID: principalID,
		Source: principal.SourceFeishu, IsolationBackend: principal.BackendLinuxDAC,
	}
	output, err := runSessionCredentialCommand(ctx, rt, identity, "credential-cli", []string{"credential-stdout"}, []string{"CHECK_VALUE=credential-env"}, nil)
	if err != nil || output.ExitCode != 0 || string(output.Stdout) != "credential-stdout\ncredential-env\n" || string(output.Stderr) != "credential-stderr" {
		t.Fatalf("output=%+v err=%v", output, err)
	}
}

type maintenanceIsolatedProc struct {
	serverTestProc
	handle isolation.ProcessHandle
}

func (p *maintenanceIsolatedProc) PID() int { return p.handle.PID() }
func (p *maintenanceIsolatedProc) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.handle.Kill(ctx); err != nil {
		return err
	}
	if _, err := p.handle.Wait(ctx); err != nil {
		return err
	}
	return p.handle.Close()
}

// Real Linux UID isolation and helper restart, with a deterministic turn source.
// Opt-in: uses a temporary helper/socket/state and never installed services.
func TestMaintenanceLinuxHelperIntegration(t *testing.T) {
	binary, uidText := os.Getenv("AMUX_TEST_HELPER_BINARY"), os.Getenv("AMUX_TEST_MAINTENANCE_UID")
	if os.Geteuid() != 0 || binary == "" || uidText == "" {
		t.Skip("requires root, helper binary and dedicated test uid")
	}
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid < 10000 {
		t.Fatal("invalid test uid")
	}
	root, err := os.MkdirTemp("/tmp", "amux-maintenance-linux-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0711); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	m, err := newRuntimeMaintenance(&server{amuxDir: root, mgr: mgr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := isolation.HelperConfig{SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"), PrincipalRoot: filepath.Join(root, "principals"), ServiceUID: 0, ServiceGID: 0, UIDRangeStart: uid, UIDRangeEnd: uid, MaterializerPath: "/bin/false", ExecWrapperPath: binary, SkipAccountProvision: true}
	client := isolation.NewHelperClient(cfg.SocketPath)
	startHelper := func() func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- isolation.ServeHelper(ctx, cfg) }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := client.Handshake(context.Background()); err == nil {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("helper not ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Error("helper shutdown timeout")
			}
		}
	}
	stop := startHelper()
	defer func() { stop() }()
	mapping := isolation.AccountMapping{PrincipalID: uuid.NewString(), Account: "amux_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12], UID: uid, GID: uid}
	start := func(s *session.Session) (*maintenanceIsolatedProc, error) {
		launch := uuid.NewString()
		paths, err := client.PrepareRun(context.Background(), mapping, "maintenance", launch)
		if err != nil {
			return nil, err
		}
		h, err := client.Start(context.Background(), mapping, isolation.LaunchSpec{Path: "/bin/sh", Args: []string{"-c", "id -u > observed-uid; exec sleep 300"}, Dir: paths.Workspace, Env: []string{"PATH=/usr/bin:/bin"}, Context: isolation.LaunchContext{LaunchID: launch}})
		if err != nil {
			return nil, err
		}
		p := &maintenanceIsolatedProc{handle: h}
		s.SetProc(p)
		if err := mgr.SetRunning(s); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, _ := os.ReadFile(filepath.Join(paths.Workspace, "observed-uid"))
			if strings.TrimSpace(string(data)) == uidText {
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("wrong isolated uid: %q", data)
			}
			time.Sleep(10 * time.Millisecond)
		}
		return p, nil
	}
	s, err := mgr.Create("isolated", "claude")
	if err != nil {
		t.Fatal(err)
	}
	p, err := start(s)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Kill() }()
	p.holdTurnOpen = true
	if _, err := s.SubmitInput(context.Background(), "cli", session.TextUserInput("active work")); err != nil {
		t.Fatal(err)
	}
	maintenanceCall(t, m, "begin")
	if out := maintenanceCall(t, m, "poll"); out.Phase != "draining" {
		t.Fatal(out)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if active, err := client.AcquireUpgradeGuard(context.Background(), r); err != nil || active != 1 {
		t.Fatalf("active launch lost: %d %v", active, err)
	}
	p.turnSink(testSuccessEmission())
	deadline := time.Now().Add(5 * time.Second)
	for {
		out := maintenanceCall(t, m, "poll")
		if out.Phase == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if active, err := client.AcquireUpgradeGuard(context.Background(), r); err != nil || active != 0 {
		t.Fatalf("helper not drained: %d %v", active, err)
	}
	stop()
	stop = func() {}
	_ = w.Close()
	stop = startHelper()
	m.resume = func(_ context.Context, s *session.Session) error { _, err := start(s); return err }
	maintenanceCall(t, m, "end")
	if s.StateSnapshot() != session.StateRunning {
		t.Fatal("isolated session not restored")
	}
	if n, err := client.DrainStatus(context.Background(), false); err != nil || n != 1 {
		t.Fatalf("restored launches=%d %v", n, err)
	}
}
