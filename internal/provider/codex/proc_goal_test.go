package codex

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

func TestNativeGoalContinuesThroughSessionAdmission(t *testing.T) {
	executable := writeFakeCodex(t, fakeNativeGoalServer)
	proc, _, err := startTestProc(t, StartOptions{Executable: executable, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	if !proc.GoalSupported() {
		t.Fatal("native goal RPC was not discovered")
	}
	sess := &session.Session{ID: "amux-goal-session", Name: "goal-test"}
	events := make(chan agentstream.Event, 64)
	sess.SetEventTap(func(event agentstream.Event) bool { events <- event; return false })
	sess.SetProc(proc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	budget := int64(40000)
	if _, err := sess.Goal(ctx, session.GoalRequest{Action: "create", Objective: "finish both turns", TokenBudget: &budget}); err != nil {
		t.Fatal(err)
	}
	started, finished := 0, 0
	tokens := map[string]bool{}
	for finished < 2 {
		select {
		case event := <-events:
			switch body := event.Body.(type) {
			case agentstream.TurnStarted:
				started++
				if event.Origin.Channel != "goal" || body.InputSummary != "finish both turns" {
					t.Fatalf("wrong continuation origin: %+v", event)
				}
				if event.Turn == nil || tokens[event.Turn.ID] {
					t.Fatal("continuation reused turn identity")
				}
				tokens[event.Turn.ID] = true
			case agentstream.TurnFinished:
				finished++
				if body.Terminal.Outcome != agentstream.OutcomeSuccess {
					t.Fatalf("goal continuation interrupted: %+v", body)
				}
			}
		case <-ctx.Done():
			t.Fatalf("native continuation stalled: starts=%d finishes=%d", started, finished)
		}
	}
	if started != 2 {
		t.Fatalf("starts=%d", started)
	}
	goal, err := sess.Goal(ctx, session.GoalRequest{Action: "get"})
	if err != nil || goal == nil || goal.Status != "complete" || goal.TokensUsed != 12 {
		t.Fatalf("goal=%+v error=%v", goal, err)
	}
}

func TestGoalDiscoveryFailsClosed(t *testing.T) {
	for _, raw := range []string{`{}`, `{"goal":{"objective":"x","status":"active"}}`, `{"goal":"wrong"}`} {
		if _, err := decodeGoalResponse([]byte(raw)); err == nil {
			t.Errorf("accepted unsupported/malformed response %s", raw)
		}
	}
	if goal, err := decodeGoalResponse([]byte(`{"goal":null}`)); err != nil || goal != nil {
		t.Fatalf("valid empty goal=%+v,%v", goal, err)
	}
}

func TestGoalDiscoveryUsesRuntimeCapabilityForOtherProviders(t *testing.T) {
	for _, supported := range []bool{true, false} {
		name := "native"
		script := fakeNativeGoalServer
		if !supported {
			name = "unsupported"
			script = strings.Replace(script, `elif method=="thread/goal/get": result={"goal":goal}`, `elif method=="thread/goal/get": result={}`, 1)
		}
		t.Run(name, func(t *testing.T) {
			proc, _, err := startTestProc(t, StartOptions{ProviderName: "compatible", Executable: writeFakeCodex(t, script), Cwd: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer proc.Close()
			if proc.GoalSupported() != supported {
				t.Fatalf("provider capability=%v, want %v", proc.GoalSupported(), supported)
			}
		})
	}
}

func TestGoalPausedBeforeOrdinaryTurnSubmission(t *testing.T) {
	script := strings.Replace(fakeNativeGoalServer, "goal=None", `goal={"threadId":thread,"objective":"existing task","status":"active","tokensUsed":19,"timeUsedSeconds":0,"createdAt":1,"updatedAt":1}`, 1)
	script = strings.Replace(script, `elif method=="turn/interrupt":`, `elif method=="turn/start":
        if goal["status"]!="paused": raise RuntimeError("goal scheduler still active during user submission")
        result={"turn":{"id":"ordinary-turn"}}
    elif method=="turn/interrupt":`, 1)
	proc, _, err := startTestProc(t, StartOptions{Executable: writeFakeCodex(t, script), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	proc.enableGoalTurns()
	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "ordinary", Input: session.TextUserInput("follow-up")}, func(agentstream.ProducerEmission) bool { return false }); err != nil {
		t.Fatal(err)
	}
	goal, err := proc.Goal(context.Background(), session.GoalRequest{Action: "get"})
	if err != nil || goal == nil || goal.Status != "paused" || goal.TokensUsed != 19 {
		t.Fatalf("native goal state=%+v error=%v", goal, err)
	}
}

func TestRestoredGoalWaitsForRuntimeOwner(t *testing.T) {
	script := strings.Replace(fakeNativeGoalServer, "goal=None", `goal={"threadId":thread,"objective":"restored task","status":"active","tokensUsed":19,"timeUsedSeconds":0,"createdAt":1,"updatedAt":1}`, 1)
	script = strings.Replace(script, `elif method=="thread/start": result={"thread":{"id":thread}}`, `elif method=="thread/start":
        emit("turn/started",{"threadId":thread,"turn":{"id":"restored-turn"}})
        emit("item/agentMessage/delta",{"threadId":thread,"turnId":"restored-turn","itemId":"answer","delta":"restored progress"})
        emit("turn/completed",{"threadId":thread,"turn":{"id":"restored-turn","status":"completed"}})
        result={"thread":{"id":thread}}`, 1)
	proc, _, err := startTestProc(t, StartOptions{Executable: writeFakeCodex(t, script), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	if !proc.GoalSupported() {
		t.Fatal("restore probe failed")
	}
	events := make(chan agentstream.ProducerEmission, 8)
	proc.SetGoalTurnAdmitter(func(request session.GoalTurnRequest) (session.GoalTurnBinding, error) {
		if request.Goal.Objective != "restored task" {
			t.Errorf("wrong restored goal %+v", request)
		}
		return session.GoalTurnBinding{TurnToken: "restored-canonical", Sink: func(e agentstream.ProducerEmission) bool { events <- e; return false }}, nil
	})
	proc.EnableGoalTurns()
	timeout := time.After(3 * time.Second)
	foundText := false
	for {
		select {
		case e := <-events:
			if delta, ok := e.Turn.(agentstream.AssistantDelta); ok && delta.Text == "restored progress" {
				foundText = true
			}
			if e.Terminal != nil {
				if !foundText || e.Terminal.Outcome != agentstream.OutcomeSuccess {
					t.Fatalf("bad restored output %+v", e)
				}
				return
			}
		case <-timeout:
			t.Fatal("restored native turn lost before owner installed")
		}
	}
}

const fakeNativeGoalServer = `#!/usr/bin/env python3
import json,sys
thread="goal-native"
goal=None
def emit(method,params):
    print(json.dumps({"jsonrpc":"2.0","method":method,"params":params}),flush=True)
for line in sys.stdin:
    request=json.loads(line);method=request.get("method");rid=request.get("id");p=request.get("params",{})
    result={}
    if method=="initialize": result={"userAgent":"fake-goals"}
    elif method=="thread/start": result={"thread":{"id":thread}}
    elif method=="thread/goal/get": result={"goal":goal}
    elif method=="thread/goal/set":
        if goal is None or (p.get("objective") and p["objective"]!=goal["objective"]):
            goal={"threadId":thread,"objective":p.get("objective",""),"status":"active","tokensUsed":0,"timeUsedSeconds":0,"createdAt":1,"updatedAt":1}
        if "status" in p: goal["status"]=p["status"]
        if "tokenBudget" in p: goal["tokenBudget"]=p["tokenBudget"]
        result={"goal":goal}
    elif method=="thread/goal/clear": goal=None
    elif method=="turn/interrupt":
        raise RuntimeError("authorized goal was unexpectedly interrupted")
    if rid is not None: print(json.dumps({"jsonrpc":"2.0","id":rid,"result":result}),flush=True)
    if method=="thread/goal/set":
        emit("thread/goal/updated",{"threadId":thread,"goal":goal})
        if goal["status"]=="active":
            for n in (1,2):
                turn="native-goal-%d"%n
                emit("turn/started",{"threadId":thread,"turn":{"id":turn}})
                emit("item/agentMessage/delta",{"threadId":thread,"turnId":turn,"itemId":"answer-%d"%n,"delta":"progress %d"%n})
                emit("turn/completed",{"threadId":thread,"turn":{"id":turn,"status":"completed"}})
            goal["status"]="complete";goal["tokensUsed"]=12;goal["updatedAt"]=2
            emit("thread/goal/updated",{"threadId":thread,"goal":goal})
`
