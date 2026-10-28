package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestAsyncUserInputCompletedMessages(t *testing.T) {
	const script = `#!/usr/bin/env python3
import json, sys
turn = 0
for line in sys.stdin:
    msg = json.loads(line)
    method, mid = msg.get("method"), msg.get("id")
    if method == "initialize":
        result = {"userAgent":"fake"}
    elif method == "thread/start":
        result = {"thread":{"id":"native-1"}}
    elif method == "turn/start":
        turn += 1
        tid = "turn-" + str(turn)
        def notify(method, params):
            print(json.dumps({"method":method,"params":params}),flush=True)
        notify("turn/started", {"threadId":"native-1","turn":{"id":tid}})
        notify("thread/tokenUsage/updated", {"threadId":"native-1","turnId":tid,"tokenUsage":{"last":{"inputTokens":4,"outputTokens":3,"totalTokens":7}}})
        item = {"type":"agentMessage","id":"question-"+str(turn),"text":"Choose scope","phase":"final_answer","delivery":"async","questions":[{"title":"Choose scope","options":["API","Worker"]},{"title":"Additional context?","options":None}]}
        for scope in ["stale-turn",tid,tid]:
            notify("item/completed", {"threadId":"native-1","turnId":scope,"item":item})
        notify("turn/completed", {"threadId":"native-1","turn":{"id":tid}})
        result = {"turn":{"id":tid}}
    else:
        result = {}
    if mid is not None:
        assert method is not None, "Async questions must not receive RPC responses"
        print(json.dumps({"id":mid,"result":result}),flush=True)
`
	proc, _, err := Start(context.Background(), compatibleStartOptions(t, writeFakeCodex(t, script)))
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	events := make(chan testEmission, 64)
	proc.SetEventSink(captureEventSink(events))
	for turn := 1; turn <= 2; turn++ {
		if _, err := proc.Write([]byte("continue\r")); err != nil {
			t.Fatal(err)
		}
		questions := []agentstream.PermissionRequest{}
		deadline := time.After(3 * time.Second)
	loop:
		for {
			select {
			case ev := <-events:
				if ev.Permission != nil {
					questions = append(questions, *ev.Permission)
				}
				if ev.Terminal != nil {
					if ev.Terminal.Outcome != agentstream.OutcomeSuccess {
						t.Fatalf("terminal=%+v", ev.Terminal)
					}
					break loop
				}
				if (ev.Kind == agentstream.KindAssistantMessage || ev.Kind == agentstream.KindResultObserved) && ev.Text != "" {
					t.Fatalf("async question polluted assistant result: %q", ev.Text)
				}
			case <-deadline:
				t.Fatal("async questions blocked turn completion")
			}
		}
		if len(questions) != 2 {
			t.Fatalf("questions=%+v", questions)
		}
		for i, q := range questions {
			if q.ID != fmt.Sprintf("async:question-%d:%d", turn, i) || !q.IsAsyncUserInput() || q.ToolCall.AgentSessionID != "native-1" || !q.ToolCall.AllowOther || q.ToolCall.Blocking == nil || *q.ToolCall.Blocking {
				t.Fatalf("question=%+v tool=%+v", q, q.ToolCall)
			}
		}
		if questions[0].ToolCall.Question != "Choose scope" || len(questions[0].Options) != 2 || questions[0].Options[1].ID != "Worker" || len(questions[1].Options) != 0 {
			t.Fatalf("question content=%+v", questions)
		}
		proc.threadMu.Lock()
		pending := len(proc.userInputs) + len(proc.approvals)
		proc.threadMu.Unlock()
		if pending != 0 {
			t.Fatalf("async questions registered %d provider waits", pending)
		}
	}
}

func TestAsyncUserInputMalformedBatchFailsAtomically(t *testing.T) {
	for _, questions := range []string{`[{"title":""}]`, `[{"title":"valid"},{"title":"invalid","options":["A","A"]}]`, `[{"title":"invalid","options":[""]}]`} {
		t.Run(questions, func(t *testing.T) {
			p := &Proc{threadID: "native", turnID: "turn"}
			events := make(chan testEmission, 16)
			p.SetEventSink(captureEventSink(events))
			var qs []asyncInputQuestion
			if err := json.Unmarshal([]byte(questions), &qs); err != nil {
				t.Fatal(err)
			}
			p.emitAsyncUserInput(itemLifecycleNotification{ThreadID: "native", TurnID: "turn", Item: threadItem{ID: "item", Questions: qs}})
			deadline := time.After(time.Second)
			for {
				select {
				case ev := <-events:
					if ev.Permission != nil {
						t.Fatal("partial malformed batch emitted")
					}
					if ev.Terminal != nil {
						if ev.Terminal.Outcome != agentstream.OutcomeError {
							t.Fatal("malformed question accepted")
						}
						return
					}
				case <-deadline:
					t.Fatal("malformed question did not fail")
				}
			}
		})
	}
}
