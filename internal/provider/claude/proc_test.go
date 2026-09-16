package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

func TestProcDispatchTurnStopsWaitingWhenContextIsCanceled(t *testing.T) {
	sidecar := writeBlockingTurnSidecar(t)
	proc, _, err := Start(context.Background(), StartOptions{SessionID: "s1", Cwd: t.TempDir(), SidecarPath: sidecar})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- proc.DispatchTurn(ctx, session.TurnDispatchRequest{
			TurnToken: "turn-1",
			Input:     session.TextUserInput("hello"),
		}, func(testProducerEmission) bool { return false })
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("DispatchTurn error = %v, want context deadline exceeded", err)
		}
	case <-time.After(time.Second):
		_ = proc.Close()
		t.Fatal("DispatchTurn did not stop after context cancellation")
	}
}

func TestProcDispatchTurnDoesNotStartWithCanceledContext(t *testing.T) {
	sidecar := writeFakeSidecar(t)
	proc, _, err := Start(context.Background(), StartOptions{SessionID: "s1", Cwd: t.TempDir(), SidecarPath: sidecar})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 1)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = proc.DispatchTurn(ctx, session.TurnDispatchRequest{
		TurnToken: "turn-1",
		Input:     session.TextUserInput("must not reach sidecar"),
	}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DispatchTurn error = %v, want context canceled", err)
	}
	select {
	case ev := <-events:
		t.Fatalf("canceled dispatch reached sidecar: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProcDispatchTurnDoesNotStartWhenCanceledWaitingForInputLock(t *testing.T) {
	sidecar := writeFakeSidecar(t)
	proc, _, err := Start(context.Background(), StartOptions{SessionID: "s1", Cwd: t.TempDir(), SidecarPath: sidecar})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 2)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	proc.inputMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- proc.DispatchTurn(ctx, session.TurnDispatchRequest{
			TurnToken: "turn-1",
			Input:     session.TextUserInput("must not reach sidecar"),
		}, func(emission testProducerEmission) bool {
			ev := captureEmission(emission)
			events <- ev
			return false
		})
	}()

	// Give DispatchTurn time to pass its initial context check and block on
	// inputMu, then cancel while the request is queued behind the lock.
	time.Sleep(20 * time.Millisecond)
	cancel()
	proc.inputMu.Unlock()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DispatchTurn error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("DispatchTurn did not stop after releasing input lock")
	}
	select {
	case ev := <-events:
		t.Fatalf("dispatch canceled while waiting for input lock reached sidecar: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProcDispatchTurnRejectsWhitespaceTurnToken(t *testing.T) {
	sidecar := writeFakeSidecar(t)
	proc, _, err := Start(context.Background(), StartOptions{SessionID: "s1", Cwd: t.TempDir(), SidecarPath: sidecar})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	err = proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: " \t\n",
		Input:     session.TextUserInput("must not reach sidecar"),
	}, func(testProducerEmission) bool { return false })
	if err == nil || err.Error() != "turn token is required" {
		t.Fatalf("DispatchTurn error = %v, want turn token is required", err)
	}
}

func TestProcDeliversTextAndTurnDoneThroughEventSink(t *testing.T) {
	sidecar := writeFakeSidecar(t)
	proc, out, err := Start(context.Background(), StartOptions{SessionID: "s1", Cwd: t.TempDir(), SidecarPath: sidecar})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 4)
	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: "turn-1",
		Input:     session.TextUserInput("  hello\nfrom sidecar  \n"),
	}, func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	}); err != nil {
		t.Fatalf("DispatchTurn: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Body == nil || ev.Body.Type != string(agentstream.KindAssistantDelta) || ev.Body.Text != "  hello\nfrom sidecar  \n" {
			t.Fatalf("event = %+v, want provider assistant text", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for assistant text")
	}
	select {
	case ev := <-events:
		if ev.Terminal == nil || ev.Terminal.Outcome != agentstream.OutcomeSuccess {
			t.Fatalf("event = %+v, want provider turn_done", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn_done")
	}
	select {
	case data := <-out:
		t.Fatalf("lifecycle channel received semantic output %q", data)
	default:
	}
}

func TestProcSubmitInputPreservesWhitespaceOnlyText(t *testing.T) {
	sidecar := writeFakeSidecar(t)
	proc, _, err := Start(context.Background(), StartOptions{SessionID: "s1", Cwd: t.TempDir(), SidecarPath: sidecar})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 2)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})
	const input = " \t\n"
	if err := proc.SubmitInput(session.TextUserInput(input)); err != nil {
		t.Fatalf("SubmitInput: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Body == nil || ev.Body.Type != string(agentstream.KindAssistantDelta) || ev.Body.Text != input {
			t.Fatalf("event = %+v, want exact whitespace input", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for whitespace-only turn")
	}
}

func TestProcDeliversEventsEmittedBeforeSinkInstallation(t *testing.T) {
	sidecar := writeStartupEventSidecar(t)
	proc, _, err := Start(context.Background(), StartOptions{SessionID: "s1", Cwd: t.TempDir(), SidecarPath: sidecar})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	events := make(chan testEmission, 1)
	proc.SetEventSink(func(emission testProducerEmission) bool {
		ev := captureEmission(emission)
		events <- ev
		return false
	})

	select {
	case ev := <-events:
		if ev.Metadata == nil || ev.Metadata.AgentSessionID != "claude-session-1" || ev.Origin.Channel != "agent" {
			t.Fatalf("event = %+v, want startup session metadata", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for startup session metadata")
	}
}

func TestProcPreservesEventsEmittedWhilePendingEventsDrain(t *testing.T) {
	sidecar := writeStartupEventSidecar(t)
	proc, _, err := Start(context.Background(), StartOptions{SessionID: "s1", Cwd: t.TempDir(), SidecarPath: sidecar})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Close()

	delivered := make(chan testEmission, 3)
	firstDeliveryStarted := make(chan struct{})
	releaseFirstDelivery := make(chan struct{})
	sinkReady := make(chan struct{})
	go func() {
		proc.SetEventSink(func(emission testProducerEmission) bool {
			ev := captureEmission(emission)
			if ev.Metadata != nil && ev.Metadata.AgentSessionID != "" {
				close(firstDeliveryStarted)
				<-releaseFirstDelivery
			}
			delivered <- ev
			return false
		})
		close(sinkReady)
	}()

	select {
	case <-firstDeliveryStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pending event drain")
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := proc.Write([]byte("hello\r"))
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for concurrent provider event")
	}
	close(releaseFirstDelivery)
	select {
	case <-sinkReady:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event sink installation")
	}

	first := <-delivered
	if first.Metadata == nil || first.Metadata.AgentSessionID != "claude-session-1" {
		t.Fatalf("first event = %+v, want startup session metadata", first)
	}
	second := <-delivered
	if second.Body == nil || second.Body.Type != string(agentstream.KindAssistantDelta) || second.Body.Text != "during drain" {
		t.Fatalf("second event = %+v, want assistant text emitted during drain", second)
	}
	select {
	case ev := <-delivered:
		t.Fatalf("unexpected duplicate event: %+v", ev)
	default:
	}
}

func writeFakeSidecar(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake sidecar is unix-only")
	}
	path := filepath.Join(t.TempDir(), "fake-sidecar")
	script := `#!/usr/bin/env python3
import json, sys
for line in sys.stdin:
    msg=json.loads(line)
    mid=msg.get('id')
    method=msg.get('method')
    if method in ('initialize','session.start'):
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
    elif method == 'turn.start':
        print(json.dumps({'jsonrpc':'2.0','method':'event','params':{'event':{'provider':'claude','type':'assistant_text_delta','text':msg['params']['text']}}}), flush=True)
        print(json.dumps({'jsonrpc':'2.0','method':'event','params':{'event':{'provider':'claude','type':'turn_done','state':{'state':'turn_done','stop_reason':'end_turn'}}}}), flush=True)
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'state':'turn_done'}}), flush=True)
    elif method in ('interrupt','permission.respond','shutdown'):
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
        if method == 'shutdown':
            break
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sidecar: %v", err)
	}
	return path
}

func writeStartupEventSidecar(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake sidecar is unix-only")
	}
	path := filepath.Join(t.TempDir(), "fake-startup-event-sidecar")
	script := `#!/usr/bin/env python3
import json, sys
for line in sys.stdin:
    msg=json.loads(line)
    mid=msg.get('id')
    method=msg.get('method')
    if method == 'initialize':
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
    elif method == 'session.start':
        print(json.dumps({'jsonrpc':'2.0','method':'session.id','params':{'sessionId':'claude-session-1'}}), flush=True)
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
    elif method == 'turn.start':
        print(json.dumps({'jsonrpc':'2.0','method':'event','params':{'event':{'provider':'claude','type':'assistant_text_delta','text':'during drain'}}}), flush=True)
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
    elif method == 'shutdown':
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
        break
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake startup event sidecar: %v", err)
	}
	return path
}

func writeBlockingTurnSidecar(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake sidecar is unix-only")
	}
	path := filepath.Join(t.TempDir(), "fake-blocking-turn-sidecar")
	script := `#!/usr/bin/env python3
import json, sys
for line in sys.stdin:
    msg=json.loads(line)
    mid=msg.get('id')
    method=msg.get('method')
    if method in ('initialize','session.start'):
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
    elif method == 'turn.start':
        pass
    elif method == 'shutdown':
        print(json.dumps({'jsonrpc':'2.0','id':mid,'result':{'ok':True}}), flush=True)
        break
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake blocking turn sidecar: %v", err)
	}
	return path
}
