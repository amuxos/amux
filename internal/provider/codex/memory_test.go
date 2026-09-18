package codex

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/memoryprotocol"
)

// The fake rejects instruction replacement, native memory enablement and
// missing persistent disablement. No turn/model/network request is needed.
const fakeMemoryCodex = `#!/usr/bin/env python3
import json, sys
for key in ['features.memories=false', 'memories.use_memories=false', 'memories.generate_memories=false']:
    assert key in sys.argv, sys.argv
thread_count = 0
disabled = False
for line in sys.stdin:
    msg=json.loads(line); method=msg.get('method'); mid=msg.get('id'); params=msg.get('params', {})
    if mid is None: continue
    result={}
    if method == 'config/read':
        result={'config':{'developer_instructions':'KEEP EXISTING RULES', 'features':{'memories':False}, 'memories':{'use_memories':False,'generate_memories':False}}}
    elif method in ['thread/start','thread/resume']:
        assert params['developerInstructions'].startswith('KEEP EXISTING RULES\n\n'), params
        assert 'amux.memory_get' in params['developerInstructions'], params
        assert 'baseInstructions' not in params, params
        assert params['config']['memories.use_memories'] is False, params
        assert params['config']['memories.generate_memories'] is False, params
        if thread_count: assert disabled
        thread_count += 1; disabled = False
        result={'thread':{'id':'memory-thread-'+str(thread_count)},'model':'test'}
    elif method == 'thread/memoryMode/set':
        assert params['mode']=='disabled'
        disabled=True
    elif method == 'model/list':
        assert disabled
        result={'data':[]}
    print(json.dumps({'id':mid,'result':result}), flush=True)
`

func TestManagedMemoryStartResumeAndClear(t *testing.T) {
	for _, resume := range []string{"", "managed-existing-thread"} {
		t.Run("resume="+resume, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, _, err := Start(ctx, StartOptions{Cwd: t.TempDir(), Env: append(os.Environ(), memoryprotocol.BindingEnv+"=global"), CodexExecutable: writeFakeCodex(t, fakeMemoryCodex), ResumeSessionID: resume})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if err := p.clearThread(); err != nil {
				t.Fatalf("clear: %v", err)
			}
			if got := p.snapshotThreadID(); got != "memory-thread-2" {
				t.Fatalf("cleared thread = %q", got)
			}
		})
	}
}

func TestManagedMemoryRejectsUnsupportedNativeContracts(t *testing.T) {
	for name, script := range map[string]string{
		"missing config":              strings.Replace(fakeMemoryCodex, "'memories':{'use_memories':False,'generate_memories':False}", "'memories':{}", 1),
		"reading enabled":             strings.Replace(fakeMemoryCodex, "'use_memories':False", "'use_memories':True", 1),
		"persistent mode unavailable": strings.Replace(fakeMemoryCodex, "disabled=True", "print(json.dumps({'id':mid,'error':{'code':-32601,'message':'unsupported'}}),flush=True); continue", 1),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, _, err := Start(ctx, StartOptions{Cwd: t.TempDir(), Env: append(os.Environ(), memoryprotocol.BindingEnv+"=global"), CodexExecutable: writeFakeCodex(t, script)})
			if p != nil {
				_ = p.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "amux memory requires") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestUnmanagedCodexKeepsNativeMemoryConfig(t *testing.T) {
	args := []string{"-c", "memories.use_memories=true"}
	if got := strings.Join(memoryLaunchArgs(StartOptions{ExtraArgs: args}), " "); got != strings.Join(args, " ") {
		t.Fatalf("unmanaged args changed: %s", got)
	}
}
