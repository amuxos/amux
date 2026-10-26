package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
)

func startWatchdogAgent(t *testing.T, mode string, settings config.StructuredProviderConfig) (*Proc, string) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "requests.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=^TestACPWatchdogAgent$")
	cmd.Env = append(os.Environ(), "AMUX_TEST_WATCHDOG="+mode, "AMUX_TEST_WATCHDOG_CAPTURE="+capture)
	p, _, err := StartWithOptions(context.Background(), cmd, StartOptions{Cwd: t.TempDir(), Watchdog: &settings, WatchdogTimeoutUnit: 20 * time.Millisecond, WatchdogCancelGrace: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, capture
}

func TestWatchdogExpiredTurnDrainsBeforeOneTerminal(t *testing.T) {
	tests := []struct {
		name, mode, code string
		settings         config.StructuredProviderConfig
	}{
		{"idle", "silent", "turn_idle_timeout", config.StructuredProviderConfig{TurnIdleTimeoutSeconds: 3}},
		{"active hard cap", "active", "turn_hard_cap", config.StructuredProviderConfig{TurnIdleTimeoutSeconds: 5, TurnHardCapSeconds: 10}},
		{"repeated tool", "repeat", "repeat_tool_loop", config.StructuredProviderConfig{RepeatToolCallLimit: 3}},
		{"permission and late frames", "permission", "turn_hard_cap", config.StructuredProviderConfig{TurnIdleTimeoutSeconds: 5, TurnHardCapSeconds: 10}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, capture := startWatchdogAgent(t, tc.mode, tc.settings)
			var mu sync.Mutex
			var terminals []agentstream.Terminal
			var lateText, latePermission bool
			var drained bool
			sink := func(e agentstream.ProducerEmission) bool {
				mu.Lock()
				defer mu.Unlock()
				if e.Terminal != nil {
					terminals = append(terminals, *e.Terminal)
					select {
					case <-p.readDone:
						drained = true
					default:
					}
					select {
					case <-p.done:
					default:
						drained = false
					}
				}
				if delta, ok := e.Turn.(agentstream.AssistantDelta); ok && strings.Contains(delta.Text, "LATE") {
					lateText = true
				}
				if perm, ok := e.Turn.(agentstream.PermissionRequested); ok && strings.HasSuffix(perm.Request.ID, ":501") {
					latePermission = true
				}
				return false
			}
			p.SetEventSink(sink)
			if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{Input: session.TextUserInput("work")}, sink); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			if len(terminals) != 1 || terminals[0].Code != tc.code || terminals[0].Outcome != agentstream.OutcomeError || !drained || lateText || latePermission {
				t.Fatalf("terminals=%+v drained=%v lateText=%v latePermission=%v", terminals, drained, lateText, latePermission)
			}
			mu.Unlock()
			requests := configRequests(t, capture)
			cancelled := false
			permissions := map[float64]string{}
			for _, req := range requests {
				if req["method"] == "session/cancel" {
					cancelled = true
				}
				if result, ok := req["result"].(map[string]any); ok {
					if outcome, ok := result["outcome"].(map[string]any); ok {
						permissions[req["id"].(float64)], _ = outcome["outcome"].(string)
					}
				}
			}
			if !cancelled {
				t.Fatal("native session/cancel missing")
			}
			if tc.mode == "permission" && (permissions[500] != "cancelled" || permissions[501] != "cancelled") {
				t.Fatalf("pending/late permission responses=%v", permissions)
			}
		})
	}
}

func TestWatchdogDoesNotInferIdleFromUnverifiedToolProgress(t *testing.T) {
	p, _ := startWatchdogAgent(t, "tool", config.StructuredProviderConfig{TurnIdleTimeoutSeconds: 5, TurnHardCapSeconds: 100, ToolIdleTimeoutSeconds: 1})
	var terminal *agentstream.Terminal
	if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{Input: session.TextUserInput("work")}, func(e agentstream.ProducerEmission) bool {
		if e.Terminal != nil {
			terminal = e.Terminal
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if terminal == nil || terminal.Outcome != agentstream.OutcomeSuccess {
		t.Fatalf("terminal=%+v", terminal)
	}
	select {
	case <-p.done:
		t.Fatal("healthy long tool retired process")
	default:
	}
}

func TestWatchdogGenerationCannotStopNewTurn(t *testing.T) {
	p := &Proc{watchdogPolicy: watchdogPolicy{idle: time.Millisecond}}
	old := p.startWatchdog()
	current := p.startWatchdog()
	p.stopWatchdog(old)
	p.watchdogMu.Lock()
	p.watchdog.activity = time.Now().Add(-time.Second)
	p.watchdogMu.Unlock()
	if reason, _ := p.watchdogCheck(old); reason != "" {
		t.Fatalf("old generation reason=%q", reason)
	}
	if reason, _ := p.watchdogCheck(current); reason != "turn_idle_timeout" {
		t.Fatalf("current generation reason=%q", reason)
	}
}

func TestACPWatchdogAgent(t *testing.T) {
	mode := os.Getenv("AMUX_TEST_WATCHDOG")
	if mode == "" {
		return
	}
	var writeMu sync.Mutex
	output := func(value any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		data, _ := json.Marshal(value)
		fmt.Println(string(data))
	}
	respond := func(id int64, result any) { output(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) }
	update := func(value any) {
		output(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "watchdog-session", "update": value}})
	}
	permission := func(id int64) {
		output(map[string]any{"jsonrpc": "2.0", "id": id, "method": "session/request_permission", "params": map[string]any{"sessionId": "watchdog-session", "options": []map[string]string{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}}}})
	}
	var promptID int64
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if path := os.Getenv("AMUX_TEST_WATCHDOG_CAPTURE"); path != "" {
			f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if f != nil {
				_, _ = f.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
				_ = f.Close()
			}
		}
		var req rpcMessage
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			respond(req.ID, map[string]any{})
		case "session/new":
			respond(req.ID, map[string]string{"sessionId": "watchdog-session"})
		case "session/prompt":
			promptID = req.ID
			switch mode {
			case "active":
				go func() {
					for {
						time.Sleep(3 * time.Millisecond)
						update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "working"}})
					}
				}()
			case "repeat":
				for i := 0; i < 3; i++ {
					update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": fmt.Sprint(i), "kind": "execute", "title": "Run", "status": "completed", "rawInput": map[string]string{"command": "same"}})
				}
			case "permission":
				permission(500)
			case "tool":
				update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "long-tool", "kind": "execute", "title": "Long job", "status": "in_progress"})
				id := req.ID
				go func() {
					time.Sleep(120 * time.Millisecond)
					update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "long-tool", "status": "completed"})
					respond(id, map[string]string{"stopReason": "end_turn"})
				}()
			}
		case "session/cancel":
			if mode == "permission" {
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "LATE output"}})
				permission(501)
			}
		case "":
			if mode == "permission" && req.ID == 501 {
				respond(promptID, map[string]string{"stopReason": "cancelled"})
			}
		}
	}
	os.Exit(0)
}
