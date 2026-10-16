//go:build linux

package codex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/sessionprotocol"
)

// Use the real helper: Start with an existing launch ID reattaches to that
// process. A passthrough-only test cannot detect a probe killing the agent.
func TestUserInputDiscoveryIsolatedProcessLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			helperCtx, stopHelper := context.WithCancel(context.Background())
			done := make(chan error, 1)
			cfg := isolation.HelperConfig{
				SocketPath: filepath.Join(root, "helper.sock"), StateDir: filepath.Join(root, "state"),
				PrincipalRoot: filepath.Join(root, "principals"),
				ServiceUID:    os.Getuid(), ServiceGID: os.Getgid(),
				UIDRangeStart: max(os.Getuid(), 1), UIDRangeEnd: max(os.Getuid(), 1) + 10,
				AllowUnprivileged: true, SkipAccountProvision: true,
			}
			go func() { done <- isolation.ServeHelper(helperCtx, cfg) }()
			t.Cleanup(func() {
				stopHelper()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("helper: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("helper did not stop")
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			client := isolation.NewHelperClient(cfg.SocketPath)
			for {
				if _, err := client.Handshake(ctx); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("helper did not become ready")
				case <-time.After(10 * time.Millisecond):
				}
			}
			mapping := isolation.AccountMapping{
				PrincipalID: "d7eaf95f-4c18-43b2-81fd-f5f013ac901d", Account: "amux_0123456789ab",
				UID: max(os.Getuid(), 1), GID: max(os.Getgid(), 1),
			}
			paths, err := client.PrepareRun(ctx, mapping, "exec_probe", "launch-agent")
			if err != nil {
				t.Fatal(err)
			}
			backend := &userInputHelperBackend{client: client, mapping: mapping}
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				for _, handle := range backend.handles {
					_ = handle.Kill(cleanup)
					_, _ = handle.Wait(cleanup)
					_ = handle.Close()
				}
			})
			opts := compatibleStartOptions(t, writeFakeCodex(t, isolatedUserInputServer))
			opts.Cwd = paths.Workspace
			opts.Env = append(os.Environ(), "AMUX_TEST_PROBE_MODE="+mode, sessionprotocol.InstructionEnv+"=runtime instructions")
			opts.Backend = backend
			opts.LaunchContext = isolation.LaunchContext{
				LaunchID: "launch-agent", Backend: principal.BackendLinuxDAC,
				Identity: &principal.ExecutionIdentity{EffectivePrincipalID: mapping.PrincipalID},
			}
			proc, _, err := Start(ctx, opts)
			if err != nil {
				t.Fatalf("agent startup after %s probe: %v", mode, err)
			}
			defer proc.Close()
			// A subsequent discovery must also own a fresh launch; reusing a
			// completed probe's ID is rejected by the helper.
			proc.discoverUserInput(ctx, opts, opts.Executable, backend)
			if proc.defaultModeUserInput != (mode == "success") {
				t.Fatalf("discovered capability = %v", proc.defaultModeUserInput)
			}
			if _, err := proc.client.Call(ctx, "config/read", map[string]any{}); err != nil {
				t.Fatalf("agent stopped responding after probe cleanup: %v", err)
			}
			if len(backend.handles) != 3 {
				t.Fatalf("started %d processes, want agent and two probes", len(backend.handles))
			}
			seen := map[string]bool{opts.LaunchContext.LaunchID: true}
			for i, handle := range backend.handles[1:] {
				if seen[handle.LaunchID()] || handle.PID() == proc.PID() {
					t.Fatal("probe reused another process")
				}
				seen[handle.LaunchID()] = true
				spec := backend.specs[i+1]
				if spec.Context.Identity != opts.LaunchContext.Identity || spec.Context.Backend != opts.LaunchContext.Backend ||
					spec.Dir != opts.Cwd || !reflect.DeepEqual(spec.Env, opts.Env) {
					t.Fatal("probe changed the execution identity, isolation, directory or environment")
				}
				status, err := handle.Wait(ctx)
				if err != nil {
					t.Fatalf("probe was not reaped: %v", err)
				}
				if mode == "success" && status.Code != 0 || mode == "failure" && status.Code != 7 || mode == "timeout" && status.Signal == "" {
					t.Fatalf("unexpected %s probe exit: %+v", mode, status)
				}
			}
		})
	}
}

type userInputHelperBackend struct {
	client  *isolation.HelperClient
	mapping isolation.AccountMapping
	handles []isolation.ProcessHandle
	specs   []isolation.LaunchSpec
}

func (b *userInputHelperBackend) Start(ctx context.Context, spec isolation.LaunchSpec) (isolation.ProcessHandle, error) {
	handle, err := b.client.Start(ctx, b.mapping, spec)
	if err == nil {
		b.handles = append(b.handles, handle)
		b.specs = append(b.specs, spec)
	}
	return handle, err
}

const isolatedUserInputServer = `#!/usr/bin/env python3
import json, os, sys, time
if sys.argv[-2:] == ['features', 'list']:
    mode = os.environ['AMUX_TEST_PROBE_MODE']
    if mode == 'timeout': time.sleep(30)
    if mode == 'failure': sys.exit(7)
    print('default_mode_request_user_input under development false')
    sys.exit(0)
for line in sys.stdin:
    msg = json.loads(line)
    if 'id' not in msg: continue
    method = msg.get('method')
    result = {}
    if method == 'initialize': result = {'userAgent': 'test-host'}
    elif method == 'config/read': result = {'config': {}}
    elif method == 'thread/start': result = {'thread': {'id': 'test-thread'}}
    elif method == 'thread/goal/get':
        print(json.dumps({'id': msg['id'], 'error': {'code': -32601, 'message': 'unsupported'}}), flush=True)
        continue
    print(json.dumps({'id': msg['id'], 'result': result}), flush=True)
`
