//go:build !windows

package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A managed launcher holds an update lock before starting either subcommand.
// Force the probe to acquire it first: a hard kill leaves the lock behind and
// prevents the real app server from answering initialize. Run twice to ensure
// cleanup also permits the next Session, not only the current one.
func TestUserInputProbeReleasesLauncherLock(t *testing.T) {
	opts := compatibleStartOptions(t, writeFakeCodex(t, lockingUserInputLauncher))
	opts.ProviderName = "codex"
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		proc, _, err := Start(ctx, opts)
		if err != nil {
			cancel()
			t.Fatalf("session %d blocked by probe-owned launcher lock: %v", i+1, err)
		}
		if proc.defaultModeUserInput {
			t.Error("timed-out probe enabled the feature")
		}
		if _, err := proc.client.Call(ctx, "config/read", map[string]any{}); err != nil {
			t.Errorf("probe cleanup stopped agent: %v", err)
		}
		_ = proc.Close()
		cancel()
		if _, err := os.Stat(filepath.Join(opts.Cwd, "update-lock")); !os.IsNotExist(err) {
			t.Fatalf("launcher lock survived probe cleanup: %v", err)
		}
	}
}

func TestUserInputProbeCancellationReleasesLauncherLock(t *testing.T) {
	opts := compatibleStartOptions(t, writeFakeCodex(t, lockingUserInputLauncher))
	opts.ProviderName = "codex"
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	ready := make(chan bool, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(filepath.Join(opts.Cwd, "probe-owns-lock")); err == nil {
				ready <- true
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				ready <- false
				return
			case <-ticker.C:
			}
		}
	}()
	proc, _, err := Start(ctx, opts)
	if proc != nil {
		_ = proc.Close()
	}
	if err == nil || !<-ready {
		t.Fatalf("did not cancel startup while the probe owned its lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.Cwd, "update-lock")); !os.IsNotExist(err) {
		t.Fatalf("cancelled startup left the probe's lock: %v", err)
	}
}

const lockingUserInputLauncher = `#!/usr/bin/env python3
import json, pathlib, signal, sys, time
lock = pathlib.Path('update-lock')
ready = pathlib.Path('probe-owns-lock')
if sys.argv[-2:] == ['features', 'list']:
    lock.mkdir()
    def terminate(signum, frame):
        lock.rmdir()
        sys.exit(0)
    signal.signal(signal.SIGTERM, terminate)
    ready.touch()
    time.sleep(60)
    sys.exit(1)
# Main launch starts first but must wait for the probe's shared update lock.
deadline = time.monotonic() + 5
while not ready.exists() and time.monotonic() < deadline: time.sleep(.005)
while lock.exists() and time.monotonic() < deadline: time.sleep(.005)
blocked = lock.exists() or not ready.exists()
if ready.exists(): ready.unlink()
for line in sys.stdin:
    msg = json.loads(line)
    if 'id' not in msg: continue
    method = msg.get('method')
    if method == 'initialize' and blocked:
        print(json.dumps({'id':msg['id'], 'error':{'code':-1,'message':'launcher update lock timeout'}}), flush=True)
        continue
    result = {}
    if method == 'initialize': result = {'userAgent':'host'}
    elif method in ['thread/start','thread/resume']: result = {'thread':{'id':'t1'}}
    print(json.dumps({'id':msg['id'],'result':result}), flush=True)
`
