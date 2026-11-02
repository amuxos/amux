package codex

import (
	"context"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/principal"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The app server refuses to finish initialization until the native probe has
// started. This catches serial startup without a wall-clock performance budget.
func TestUserInputDiscoveryOverlapsCodexInitialize(t *testing.T) {
	opts := compatibleStartOptions(t, writeFakeCodex(t, overlappingUserInputServer))
	opts.ProviderName = "codex"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	proc, _, err := Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	t.Logf("startup with overlapping probe: %s", time.Since(started))
	if !proc.defaultModeUserInput {
		t.Fatal("probe result was not applied before thread/start")
	}
	if _, err := os.Stat(filepath.Join(opts.Cwd, "probe-started")); err != nil {
		t.Fatal(err)
	}
}

func TestUserInputDiscoveryExplicitOverrideSkipsEarlyProbe(t *testing.T) {
	script := strings.Replace(overlappingUserInputServer, "if method == 'initialize':", "if method == 'initialize':\n        result = {'userAgent': 'host'}\n    elif method == 'never':", 1)
	script = strings.Replace(script, "assert msg['params']['config']['features.default_mode_request_user_input']", "assert not msg['params'].get('config', {}).get('features.default_mode_request_user_input')", 1)
	opts := compatibleStartOptions(t, writeFakeCodex(t, script))
	opts.ProviderName = "codex"
	opts.ExtraArgs = []string{"-c", "features.default_mode_request_user_input=false"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	if _, err := os.Stat(filepath.Join(opts.Cwd, "probe-started")); !os.IsNotExist(err) {
		t.Fatalf("explicit override launched a probe: %v", err)
	}
}

const overlappingUserInputServer = `#!/usr/bin/env python3
import json, pathlib, sys, time
marker = pathlib.Path('probe-started')
if sys.argv[-2:] == ['features', 'list']:
    marker.touch()
    print('default_mode_request_user_input under development false')
    sys.exit(0)
for line in sys.stdin:
    msg = json.loads(line)
    if 'id' not in msg: continue
    method = msg.get('method')
    result = {}
    if method == 'initialize':
        deadline = time.monotonic() + 1
        while not marker.exists() and time.monotonic() < deadline: time.sleep(.005)
        if not marker.exists():
            print(json.dumps({'id': msg['id'], 'error': {'code': -1, 'message': 'feature probe waited for initialize'}}), flush=True)
            continue
        result = {'userAgent': 'host'}
    elif method in ['thread/start', 'thread/resume']:
        assert msg['params']['config']['features.default_mode_request_user_input']
        result = {'thread': {'id': 't1'}}
    print(json.dumps({'id': msg['id'], 'result': result}), flush=True)
`

func TestUserInputDiscoveryStartupLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "failure", "timeout", "initialize-failure", "missing-host", "cancel", "ignore-term"} {
		t.Run(mode, func(t *testing.T) {
			opts := compatibleStartOptions(t, writeFakeCodex(t, lifecycleUserInputServer))
			opts.ProviderName = "codex"
			opts.Env = append(os.Environ(), "AMUX_TEST_PROBE_MODE="+mode)
			opts.LaunchContext = isolation.LaunchContext{LaunchID: "main-launch", Backend: "passthrough", Identity: &principal.ExecutionIdentity{EffectivePrincipalID: "test-actor"}}
			backend := &startupProbeBackend{LaunchBackend: isolation.NewPassthroughBackend(), started: make(chan struct{})}
			opts.Backend = backend
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if mode == "cancel" {
				go func() {
					select {
					case <-backend.started:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			proc, _, err := Start(ctx, opts)
			wantError := mode == "initialize-failure" || mode == "cancel"
			if (err != nil) != wantError {
				t.Fatalf("Start error=%v, want error=%v", err, wantError)
			}
			if proc != nil {
				defer proc.Close()
				if proc.defaultModeUserInput != (mode == "success") {
					t.Fatalf("capability=%v", proc.defaultModeUserInput)
				}
				if _, err := proc.client.Call(ctx, "config/read", map[string]any{}); err != nil {
					t.Fatalf("probe cleanup stopped the agent: %v", err)
				}
			}
			backend.mu.Lock()
			probe, spec := backend.probe, backend.spec
			backend.mu.Unlock()
			if probe == nil {
				t.Fatal("probe was not started")
			}
			if spec.Context.LaunchID == opts.LaunchContext.LaunchID || spec.Context.Identity != opts.LaunchContext.Identity || spec.Context.Backend != opts.LaunchContext.Backend || spec.Dir != opts.Cwd || !reflect.DeepEqual(spec.Env, opts.Env) {
				t.Fatal("probe changed launch authority or reused the main launch")
			}
			cleanup, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			t.Cleanup(func() { _ = probe.Kill(context.Background()); _ = probe.Close() })
			// Nonzero exit is expected on failure/timeout. Only deadline expiry means
			// startup returned while its probe was still alive.
			_, _ = probe.Wait(cleanup)
			if cleanup.Err() != nil {
				t.Fatal("startup left its probe running")
			}
		})
	}
}

type startupProbeBackend struct {
	isolation.LaunchBackend
	mu      sync.Mutex
	probe   isolation.ProcessHandle
	spec    isolation.LaunchSpec
	started chan struct{}
}

func (b *startupProbeBackend) Start(ctx context.Context, spec isolation.LaunchSpec) (isolation.ProcessHandle, error) {
	handle, err := b.LaunchBackend.Start(ctx, spec)
	if err == nil && len(spec.Args) >= 2 && spec.Args[len(spec.Args)-2] == "features" {
		b.mu.Lock()
		b.probe, b.spec = handle, spec
		b.mu.Unlock()
		close(b.started)
	}
	return handle, err
}

const lifecycleUserInputServer = `#!/usr/bin/env python3
import json, os, pathlib, signal, sys, time
mode = os.environ['AMUX_TEST_PROBE_MODE']
marker = pathlib.Path('probe-started')
if sys.argv[-2:] == ['features', 'list']:
    if mode == 'ignore-term': signal.signal(signal.SIGTERM, signal.SIG_IGN)
    marker.touch()
    if mode in ['ignore-term', 'timeout', 'initialize-failure', 'missing-host', 'cancel']: time.sleep(30)
    if mode == 'failure': sys.exit(7)
    print('default_mode_request_user_input under development false')
    sys.exit(0)
for line in sys.stdin:
    msg = json.loads(line)
    if 'id' not in msg: continue
    method = msg.get('method')
    result = {}
    if method == 'initialize':
        if mode == 'cancel': time.sleep(30)
        deadline = time.monotonic() + 1
        while not marker.exists() and time.monotonic() < deadline: time.sleep(.005)
        if mode == 'initialize-failure':
            print(json.dumps({'id': msg['id'], 'error': {'code': -1, 'message': 'test initialize failure'}}), flush=True)
            continue
        if mode != 'missing-host': result = {'userAgent': 'host'}
    elif method == 'thread/start':
        assert msg['params'].get('config', {}).get('features.default_mode_request_user_input', False) == (mode == 'success')
        result = {'thread': {'id': 't1'}}
    print(json.dumps({'id': msg['id'], 'result': result}), flush=True)
`

// A launcher startup cost is paid by both app-server and features list. Keep
// this benchmark reproducible without invoking an installed agent or network.
func BenchmarkCodexStartupProbe(b *testing.B) {
	for _, name := range []string{"compatible", "codex"} {
		b.Run(name, func(b *testing.B) {
			dir := b.TempDir()
			executable := filepath.Join(dir, "slow-agent")
			if err := os.WriteFile(executable, []byte(slowStartupServer), 0700); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				proc, _, err := Start(ctx, StartOptions{Executable: executable, Cwd: dir, ProviderName: name})
				if err != nil {
					cancel()
					b.Fatal(err)
				}
				_ = proc.Close()
				cancel()
			}
		})
	}
}

const slowStartupServer = `#!/usr/bin/env python3
import json, sys, time
time.sleep(.3)
if sys.argv[-2:] == ['features', 'list']:
    print('default_mode_request_user_input under development false')
    sys.exit(0)
for line in sys.stdin:
    msg = json.loads(line)
    if 'id' not in msg: continue
    result = {}
    if msg.get('method') == 'initialize': result = {'userAgent': 'host'}
    elif msg.get('method') in ['thread/start', 'thread/resume']: result = {'thread': {'id': 't1'}}
    print(json.dumps({'id': msg['id'], 'result': result}), flush=True)
`
