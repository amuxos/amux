package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessionprotocol"
)

// Native instructions live on the fake thread across turns and compaction.
// The wire log lets the test check configuration frequency and unmodified input.
const fakeRuntimeInstructionsServer = `#!/usr/bin/env python3
import json,os,sys
threads=0
instructions=None
log=open(os.environ['AMUX_NATIVE_TEST_LOG'],'w',buffering=1)
for line in sys.stdin:
    msg=json.loads(line); method=msg.get('method'); mid=msg.get('id'); p=msg.get('params',{})
    if mid is None: continue
    log.write(json.dumps({'method':method,'params':p})+'\n')
    result={}
    if method=='config/read':
        result={'config':{'developer_instructions':'KEEP USER AND PROFILE RULES\n\n<amux-runtime>stale launch</amux-runtime>'}}
    elif method in ['thread/start','thread/resume']:
        instructions=p['developerInstructions']
        assert instructions.startswith('KEEP USER AND PROFILE RULES\n\n'),p
        assert instructions.count('<amux-runtime>')==1,p
        assert 'stale launch' not in instructions,p
        assert os.environ['AMUX_SESSION_SCHEDULE_CONTEXT'] in instructions,p
        assert 'baseInstructions' not in p,p
        threads+=1
        result={'thread':{'id':'native-'+str(threads)},'model':'test'}
    elif method=='turn/start':
        assert instructions and os.environ['AMUX_SESSION_SCHEDULE_CONTEXT'] in instructions
        assert 'developerInstructions' not in p and 'baseInstructions' not in p,p
        assert all('<amux-runtime>' not in x.get('text','') for x in p['input']),p
        result={'turn':{'id':'turn-'+str(msg['id'])}}
    elif method=='model/list': result={'data':[]}
    elif method=='thread/goal/get':
        print(json.dumps({'id':mid,'error':{'code':-32601,'message':'unsupported'}}),flush=True); continue
    print(json.dumps({'id':mid,'result':result}),flush=True)
    if method=='turn/start':
        turn=result['turn']['id']
        print(json.dumps({'method':'turn/started','params':{'threadId':p['threadId'],'turn':{'id':turn}}}),flush=True)
        print(json.dumps({'method':'item/agentMessage/delta','params':{'threadId':p['threadId'],'turnId':turn,'itemId':'answer','delta':'ok'}}),flush=True)
        print(json.dumps({'method':'turn/completed','params':{'threadId':p['threadId'],'turn':{'id':turn,'status':'completed'}}}),flush=True)
`

func TestRuntimeInstructionsNativeLifecycleAndOriginalTurns(t *testing.T) {
	for _, provider := range []string{"codex", "compatible"} {
		for _, resume := range []string{"", "existing-thread"} {
			t.Run(provider+"/"+resume, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				wire := filepath.Join(t.TempDir(), "wire.jsonl")
				instruction := sessionprotocol.Instruction("/runtime/SKILL.md", "MCP", false)
				p, _, err := Start(ctx, StartOptions{
					Cwd: t.TempDir(), Executable: writeFakeCodex(t, fakeRuntimeInstructionsServer), ProviderName: provider,
					ResumeSessionID: resume, Env: append(os.Environ(), "AMUX_NATIVE_TEST_LOG="+wire, sessionprotocol.InstructionEnv+"="+instruction),
				})
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				send := func(text string) {
					t.Helper()
					done := make(chan struct{}, 1)
					if err := p.DispatchTurn(ctx, session.TurnDispatchRequest{TurnToken: text, Input: session.TextUserInput(text)}, func(ev testProducerEmission) bool {
						if ev.IsTerminal() {
							done <- struct{}{}
						}
						return false
					}); err != nil {
						t.Fatal(err)
					}
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				send("original first turn")
				if _, err := p.client.Call(ctx, "thread/compact/start", map[string]any{"threadId": p.snapshotThreadID()}); err != nil {
					t.Fatal(err)
				}
				send("original after compaction")
				if err := p.clearThread(); err != nil {
					t.Fatal(err)
				}
				send("original after clear")
				data, err := os.ReadFile(wire)
				if err != nil {
					t.Fatal(err)
				}
				var configs, threads int
				var texts []string
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					var call struct {
						Method string `json:"method"`
						Params struct {
							Input []struct {
								Text string `json:"text"`
							} `json:"input"`
						} `json:"params"`
					}
					if err := json.Unmarshal([]byte(line), &call); err != nil {
						t.Fatal(err)
					}
					switch call.Method {
					case "config/read":
						configs++
					case "thread/start", "thread/resume":
						threads++
					case "turn/start":
						if len(call.Params.Input) != 1 {
							t.Fatalf("input gained extra parts: %s", line)
						}
						texts = append(texts, call.Params.Input[0].Text)
					}
				}
				if configs != 2 || threads != 2 || strings.Join(texts, "|") != "original first turn|original after compaction|original after clear" {
					t.Fatalf("configs=%d threads=%d texts=%q", configs, threads, texts)
				}
			})
		}
	}
}

func TestRuntimeInstructionsPreserveManagedMemory(t *testing.T) {
	script := strings.Replace(fakeMemoryCodex, "thread_count = 0", "thread_count = 0\nconfig_count = 0", 1)
	script = strings.Replace(script, "if method == 'config/read':", "if method == 'config/read':\n        config_count += 1", 1)
	script = strings.Replace(script, "assert 'baseInstructions' not in params, params", "assert 'baseInstructions' not in params, params\n        assert config_count == thread_count+1\n        assert params['developerInstructions'].count('<amux-runtime>')==1", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, _, err := Start(ctx, StartOptions{Cwd: t.TempDir(), Executable: writeFakeCodex(t, script), Env: append(os.Environ(), memoryprotocol.BindingEnv+"=global", sessionprotocol.InstructionEnv+"="+sessionprotocol.Instruction("/runtime/SKILL.md", "MCP", false))})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.clearThread(); err != nil {
		t.Fatal(err)
	}
}
