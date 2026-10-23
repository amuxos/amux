package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

func startCancellationAgent(t *testing.T, tail time.Duration) *Proc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestACPCancellationAgent$")
	cmd.Env = append(os.Environ(), "AMUX_TEST_ACP_CANCEL=1")
	p, _, err := StartWithOptions(context.Background(), cmd, StartOptions{Cwd: t.TempDir(), PromptTailDrain: tail})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestActiveInterruptMapsNativeEndTurnToCancelled(t *testing.T) {
	p := startCancellationAgent(t, 0)
	active := make(chan struct{})
	var once sync.Once
	var terminal *agentstream.Terminal
	completed := make(chan error, 1)
	go func() {
		completed <- p.DispatchTurn(context.Background(), session.TurnDispatchRequest{Input: session.TextUserInput("wait for cancel")}, func(e agentstream.ProducerEmission) bool {
			if _, ok := e.Turn.(agentstream.AssistantDelta); ok {
				once.Do(func() { close(active) })
			}
			if e.Terminal != nil {
				terminal = e.Terminal
			}
			return false
		})
	}()
	select {
	case <-active:
	case <-time.After(time.Second):
		t.Fatal("native prompt did not start")
	}
	if err := p.Interrupt(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled native prompt did not complete")
	}
	if terminal == nil || terminal.Outcome != agentstream.OutcomeCancelled || terminal.Code != "cancelled" {
		t.Fatalf("terminal=%+v", terminal)
	}
	var nextTerminal *agentstream.Terminal
	if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{Input: session.TextUserInput("normal")}, func(e agentstream.ProducerEmission) bool {
		if e.Terminal != nil {
			nextTerminal = e.Terminal
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if nextTerminal == nil || nextTerminal.Outcome != agentstream.OutcomeSuccess {
		t.Fatalf("cancel leaked into next prompt: %+v", nextTerminal)
	}
}

func TestIdleAndTailInterruptDoNotRewriteCompletedNativeResponse(t *testing.T) {
	p := startCancellationAgent(t, 100*time.Millisecond)
	if err := p.Interrupt(); err != nil {
		t.Fatal(err)
	}
	var terminal *agentstream.Terminal
	completed := make(chan error, 1)
	go func() {
		completed <- p.DispatchTurn(context.Background(), session.TurnDispatchRequest{Input: session.TextUserInput("normal")}, func(e agentstream.ProducerEmission) bool {
			if e.Terminal != nil {
				terminal = e.Terminal
			}
			return false
		})
	}()
	deadline := time.Now().Add(time.Second)
	responseRead := false
	for time.Now().Before(deadline) {
		p.turnMu.Lock()
		seen := p.turnDeltas.Len() > 0
		p.turnMu.Unlock()
		p.promptMu.Lock()
		active := p.activePromptID
		p.promptMu.Unlock()
		if seen && active == 0 {
			responseRead = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !responseRead {
		t.Fatal("native response was not read before tail drain")
	}
	if err := p.Interrupt(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tail drain did not complete")
	}
	if terminal == nil || terminal.Outcome != agentstream.OutcomeSuccess {
		t.Fatalf("idle/tail interrupt rewrote native response: %+v", terminal)
	}
}

func TestACPCancellationAgent(t *testing.T) {
	if os.Getenv("AMUX_TEST_ACP_CANCEL") != "1" {
		return
	}
	respond := func(id int64, result any) {
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		fmt.Println(string(data))
	}
	promptID := int64(0)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			respond(req.ID, map[string]any{})
		case "session/new":
			respond(req.ID, map[string]string{"sessionId": "cancel-session"})
		case "session/prompt":
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"cancel-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"started"}}}}`)
			if len(req.Params.Prompt) > 0 && req.Params.Prompt[0].Text == "wait for cancel" {
				promptID = req.ID
			} else {
				respond(req.ID, map[string]string{"stopReason": "end_turn"})
			}
		case "session/cancel":
			if promptID != 0 {
				respond(promptID, map[string]string{"stopReason": "end_turn"})
				promptID = 0
			}
		}
	}
	os.Exit(0)
}
