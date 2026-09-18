package claude

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/amuxos/amux/internal/isolation"
)

type environmentBackend struct {
	env []string
}

func (b environmentBackend) Start(ctx context.Context, spec isolation.LaunchSpec) (isolation.ProcessHandle, error) {
	// Model the isolation backend replacing service-user native directories.
	spec.Env = b.env
	return isolation.NewPassthroughBackend().Start(ctx, spec)
}

func TestClaudeSDKUsesBackendEnvironmentWithoutReplayingRuntimeEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("python fake sidecar is unix-only")
	}
	home := t.TempDir()
	sidecar := filepath.Join(t.TempDir(), "environment-sidecar")
	const script = `#!/usr/bin/env python3
import json, os, sys
for line in sys.stdin:
    msg=json.loads(line)
    method=msg.get('method')
    if method == 'session.start':
        assert not msg['params'].get('env'), 'Runtime env overwrote the isolation backend'
        assert os.environ['CLAUDE_CONFIG_DIR'] == ''
        assert os.environ['PI_CODING_AGENT_DIR'] == os.environ['HOME'] + '/.pi/agent'
        assert os.environ['CLAUDE_CODE_DISABLE_AUTO_MEMORY'] == '1'
        assert msg['params']['resume'] == 'existing-native-thread'
    print(json.dumps({'jsonrpc':'2.0','id':msg.get('id'),'result':{}}), flush=True)
    if method == 'shutdown':
        break
`
	if err := os.WriteFile(sidecar, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	proc, _, err := Start(context.Background(), StartOptions{
		Cwd: t.TempDir(), SidecarPath: sidecar, ResumeSessionID: "existing-native-thread",
		Env: []string{"HOME=/service", "CLAUDE_CONFIG_DIR=/service/shared", "CLAUDE_CODE_DISABLE_AUTO_MEMORY=0"},
		Backend: environmentBackend{env: []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CLAUDE_CONFIG_DIR=",
			"PI_CODING_AGENT_DIR=" + filepath.Join(home, ".pi", "agent"),
			"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Close() })
}
