package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This fixture explicitly opts in and verifies the native memory contract,
// including config/read and persistent thread memory-mode requests.
func TestCodexMemoryLaunchAndResumeE2E(t *testing.T) {
	requireLongTest(t)
	t.Parallel()
	e := newCLIE2E(t)
	capture := filepath.Join(t.TempDir(), "codex-memory.jsonl")
	e.env = append(e.env, "AMUX_FAKE_MEMORY_CAPTURE="+capture)
	e.writeFakeCodexMemory(t)
	e.run(t, "setup", "--no-start")
	e.config(t, "runtime.id", "memory-e2e")
	e.config(t, "runtime.name", "memory-e2e")
	e.config(t, "memory.enabled", "true")
	e.startSystemOnFreePort(t)
	e.waitForCommandOutput(t, "memory runtime", "memory-e2e", "system", "target", "list")
	e.run(t, "session", "run", "codex", "-n", "memory", "--on", "memory-e2e")
	e.run(t, "session", "send", "memory-e2e/memory", "hello memory")
	e.run(t, "session", "sleep", "memory-e2e/memory")
	e.run(t, "session", "resume", "memory-e2e/memory")
	e.run(t, "session", "send", "memory-e2e/memory", "hello again")
	e.trackManagedPIDs(t)
	e.run(t, "session", "stop", "memory-e2e/memory")
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"method": "thread/memoryMode/set"`) < 2 || !strings.Contains(string(data), `"method": "thread/resume"`) {
		t.Fatalf("memory contract missing from native launches:\n%s", data)
	}
}

func (e *cliE2E) writeFakeCodexMemory(t *testing.T) {
	t.Helper()
	body := `#!/usr/bin/env python3
import json, os, sys
for required in ['features.memories=false','memories.use_memories=false','memories.generate_memories=false']:
    assert required in sys.argv, sys.argv
thread_id='memory-e2e-thread'
turn=0
disabled=False
for line in sys.stdin:
    msg=json.loads(line)
    with open(os.environ['AMUX_FAKE_MEMORY_CAPTURE'],'a') as capture:
        capture.write(json.dumps(msg)+'\n')
    mid=msg.get('id'); method=msg.get('method'); params=msg.get('params') or {}
    if mid is None: continue
    result={}
    if method=='config/read':
        result={'config':{'developer_instructions':'Keep original project rules.', 'features':{'memories':False}, 'memories':{'use_memories':False,'generate_memories':False}}}
    elif method in ['thread/start','thread/resume']:
        assert params['developerInstructions'].startswith('Keep original project rules.\n\n')
        assert 'amux.memory_get' in params['developerInstructions']
        assert 'baseInstructions' not in params
        assert params['config']['memories.generate_memories'] is False
        result={'thread':{'id':thread_id},'model':'fake'}
    elif method=='thread/memoryMode/set':
        assert params['mode']=='disabled'
        disabled=True
    elif method=='model/list': result={'data':[]}
    elif method=='skills/list': result={'data':[]}
    elif method=='turn/start':
        assert disabled
        turn+=1; tid='turn-'+str(turn)
        print(json.dumps({'method':'turn/started','params':{'threadId':thread_id,'turn':{'id':tid}}}),flush=True)
        print(json.dumps({'method':'turn/completed','params':{'threadId':thread_id,'turn':{'id':tid}}}),flush=True)
        result={'turn':{'id':tid}}
    print(json.dumps({'id':mid,'result':result}),flush=True)
`
	if err := os.WriteFile(filepath.Join(e.fakeBin, "codex"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
