package codex

import (
	"encoding/json"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	providerjsonrpc "github.com/amuxos/amux/internal/provider/jsonrpc"
)

func TestProcCompletedMessageWaitsForTurnTerminal(t *testing.T) {
	for _, providerName := range testAppServerProviders() {
		for _, terminalMethod := range []string{"turn/completed", "thread/status/changed"} {
			t.Run(providerName+"/"+terminalMethod, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					reader, writer := io.Pipe()
					defer reader.Close()
					defer writer.Close()
					proc := &Proc{
						providerName:        providerName,
						threadID:            "thread-1",
						client:              providerjsonrpc.NewClient(reader, io.Discard),
						idleCompletionDelay: 5 * time.Second,
					}
					defer proc.Close()
					var events []agentstream.ProducerEmission
					proc.SetEventSink(func(ev agentstream.ProducerEmission) bool {
						events = append(events, ev)
						return true
					})
					proc.threadMu.Lock()
					proc.freshArmLocked("turn-1")
					proc.threadMu.Unlock()
					proc.registerHandlers(make(chan []byte, 16))
					go func() { _ = proc.client.Run() }()
					encoder := json.NewEncoder(writer)
					notify := func(method string, params map[string]any) {
						t.Helper()
						if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params}); err != nil {
							t.Fatal(err)
						}
						synctest.Wait()
					}
					item := func(method string, value threadItem) {
						notify(method, map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": value})
					}
					assertActive := func() {
						t.Helper()
						if got := proc.snapshotTurnID(); got != "turn-1" {
							t.Fatalf("completed message ended a still-running turn: active turn = %q", got)
						}
						for _, ev := range events {
							if ev.Terminal != nil {
								t.Fatalf("terminal before provider completion: %+v", ev.Terminal)
							}
						}
					}

					item("item/completed", threadItem{ID: "progress", Type: "agentMessage", Text: "Checking the evidence."})
					// Providers may think silently between a progress message and a tool.
					time.Sleep(35 * time.Second)
					synctest.Wait()
					assertActive()
					tool := threadItem{ID: "tool-1", Type: "commandExecution", Status: "inProgress", Command: "inspect"}
					item("item/started", tool)
					tool.Status = "completed"
					item("item/completed", tool)
					notify("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "answer", "delta": "Final"})
					item("item/completed", threadItem{ID: "answer", Type: "agentMessage", Text: "Final answer."})
					time.Sleep(35 * time.Second)
					synctest.Wait()
					assertActive()
					if terminalMethod == "turn/completed" {
						notify(terminalMethod, map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1"}})
					} else {
						notify(terminalMethod, map[string]any{"threadId": "thread-1", "status": map[string]any{"type": "idle"}})
						time.Sleep(6 * time.Second)
						synctest.Wait()
					}
					// Duplicate completion must not emit a second result or terminal.
					notify("turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1"}})
					var result string
					var tools, terminals int
					for _, ev := range events {
						switch payload := ev.Turn.(type) {
						case agentstream.ToolChanged:
							tools++
						case agentstream.ResultObserved:
							if result != "" {
								t.Fatal("duplicate result")
							}
							result = payload.Text
						}
						if ev.Terminal != nil {
							terminals++
							if ev.Terminal.Outcome != agentstream.OutcomeSuccess || result != "Final answer." {
								t.Fatalf("terminal = %+v, preceding result = %q", ev.Terminal, result)
							}
						}
					}
					if tools != 2 || terminals != 1 || proc.snapshotTurnID() != "" {
						t.Fatalf("tools = %d, terminals = %d, active turn = %q", tools, terminals, proc.snapshotTurnID())
					}
				})
			})
		}
	}
}
