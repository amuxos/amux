package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

type usageServerTurn struct {
	Text    string            `json:"text"`
	Updates []json.RawMessage `json:"updates"`
	Result  json.RawMessage   `json:"result"`
}

func TestPromptResponseUsageReachesCanonicalEvents(t *testing.T) {
	for _, tt := range []struct {
		name        string
		turn        usageServerTurn
		wantUsage   string
		wantOutcome agentstream.Outcome
	}{
		{
			name:        "final text with reported token categories",
			turn:        usageServerTurn{Text: "done", Result: json.RawMessage(`{"stopReason":"end_turn","usage":{"inputTokens":101,"outputTokens":13,"totalTokens":114,"thoughtTokens":4,"cachedReadTokens":60,"cachedWriteTokens":8}}`)},
			wantUsage:   `{"input_tokens":101,"output_tokens":13,"total_tokens":114,"reasoning_output_tokens":4,"cached_input_tokens":60,"cache_creation_input_tokens":8}`,
			wantOutcome: agentstream.OutcomeSuccess,
		},
		{
			name:        "usage without final text",
			turn:        usageServerTurn{Result: json.RawMessage(`{"stopReason":"end_turn","usage":{"inputTokens":0,"outputTokens":7,"totalTokens":7}}`)},
			wantUsage:   `{"input_tokens":0,"output_tokens":7,"total_tokens":7}`,
			wantOutcome: agentstream.OutcomeSuccess,
		},
		{
			name: "latest context snapshot remains separate from prompt tokens",
			turn: usageServerTurn{
				Text:    "done",
				Updates: []json.RawMessage{json.RawMessage(`{"used":90,"size":1000,"cost":{"amount":1,"currency":"USD"}}`), json.RawMessage(`{"used":70,"size":1000,"cost":{"amount":1.2,"currency":"USD"}}`)},
				Result:  json.RawMessage(`{"stopReason":"end_turn","usage":{"inputTokens":20,"outputTokens":5,"totalTokens":25}}`),
			},
			wantUsage:   `{"used":70,"size":1000,"cost":{"amount":1.2,"currency":"USD"},"input_tokens":20,"output_tokens":5,"total_tokens":25}`,
			wantOutcome: agentstream.OutcomeSuccess,
		},
		{
			name:        "malformed usage cannot hide cancelled completion",
			turn:        usageServerTurn{Text: "partial", Result: json.RawMessage(`{"stopReason":"cancelled","usage":"invalid"}`)},
			wantOutcome: agentstream.OutcomeCancelled,
		},
		{
			name:        "invalid counters do not hide known counters or completion",
			turn:        usageServerTurn{Text: "done", Result: json.RawMessage(`{"stopReason":"end_turn","usage":{"inputTokens":null,"outputTokens":2,"totalTokens":"invalid","thoughtTokens":-1}}`)},
			wantUsage:   `{"output_tokens":2}`,
			wantOutcome: agentstream.OutcomeSuccess,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			proc := startUsageServer(t, []usageServerTurn{tt.turn})
			events := dispatchUsageTurn(t, proc)
			var usageEvents, resultEvents, terminals int
			for i, ev := range events {
				if ev.Terminal != nil {
					terminals++
					if i != len(events)-1 || ev.Terminal.Outcome != tt.wantOutcome {
						t.Fatalf("terminal at %d/%d = %+v, want final %s", i, len(events), ev.Terminal, tt.wantOutcome)
					}
				}
				if ev.Body == nil {
					continue
				}
				switch ev.Body.Type {
				case string(agentstream.KindUsageObserved):
					usageEvents++
					if ev.Origin.NativeType == "prompt_usage" {
						assertUsageJSON(t, ev.Body.Usage, tt.wantUsage)
					}
				case string(agentstream.KindResultObserved):
					resultEvents++
					if ev.Body.Result == nil || ev.Body.Result.Text != tt.turn.Text || ev.Body.Result.CostUSD != 0 {
						t.Fatalf("result = %+v", ev.Body.Result)
					}
					assertUsageJSON(t, ev.Body.Result.Usage, tt.wantUsage)
				}
			}
			wantUsageEvents := len(tt.turn.Updates)
			if tt.wantUsage != "" {
				wantUsageEvents++
			}
			wantResults := 0
			if tt.turn.Text != "" {
				wantResults = 1
			}
			if usageEvents != wantUsageEvents || resultEvents != wantResults || terminals != 1 {
				t.Fatalf("event counts usage=%d result=%d terminal=%d; want %d/%d/1", usageEvents, resultEvents, terminals, wantUsageEvents, wantResults)
			}
		})
	}
}

func TestPromptUsageDoesNotLeakAcrossTurns(t *testing.T) {
	proc := startUsageServer(t, []usageServerTurn{
		{Text: "one", Updates: []json.RawMessage{json.RawMessage(`{"used":70,"size":1000,"cost":{"amount":1.2,"currency":"USD"}}`)}, Result: json.RawMessage(`{"usage":{"inputTokens":20,"outputTokens":5}}`)},
		{Text: "two", Result: json.RawMessage(`{"usage":{"inputTokens":8,"outputTokens":2}}`)},
		{Text: "three", Result: json.RawMessage(`{"stopReason":"end_turn"}`)},
	})
	want := []string{`{"used":70,"size":1000,"cost":{"amount":1.2,"currency":"USD"},"input_tokens":20,"output_tokens":5}`, `{"input_tokens":8,"output_tokens":2}`, ""}
	for _, expected := range want {
		events := dispatchUsageTurn(t, proc)
		for _, ev := range events {
			if ev.Body != nil && ev.Body.Type == string(agentstream.KindResultObserved) {
				assertUsageJSON(t, ev.Body.Result.Usage, expected)
			}
		}
	}
}

func startUsageServer(t *testing.T, turns []usageServerTurn) *Proc {
	t.Helper()
	raw, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFakeACPUsageServer$")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP_USAGE_TURNS="+string(raw))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Close() })
	return proc
}

func dispatchUsageTurn(t *testing.T, proc *Proc) []testEmission {
	t.Helper()
	var mu sync.Mutex
	var events []testEmission
	err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{Input: session.TextUserInput("continue")}, func(emission testProducerEmission) bool {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, captureEmission(emission))
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]testEmission(nil), events...)
}

func assertUsageJSON(t *testing.T, actual json.RawMessage, expected string) {
	t.Helper()
	if expected == "" {
		if len(actual) != 0 {
			t.Fatalf("usage = %s, want absent", actual)
		}
		return
	}
	var got, want map[string]json.RawMessage
	if json.Unmarshal(actual, &got) != nil || json.Unmarshal([]byte(expected), &want) != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("usage = %s, want %s", actual, expected)
	}
}

func TestFakeACPUsageServer(t *testing.T) {
	raw := os.Getenv("AMUX_FAKE_ACP_USAGE_TURNS")
	if raw == "" {
		return
	}
	var turns []usageServerTurn
	if err := json.Unmarshal([]byte(raw), &turns); err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req rpcMessage
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID)
		case "session/new":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"sessionId":"usage-session"}}`+"\n", req.ID)
		case "session/prompt":
			if len(turns) == 0 {
				os.Exit(3)
			}
			turn := turns[0]
			turns = turns[1:]
			for _, usage := range turn.Updates {
				var update map[string]json.RawMessage
				_ = json.Unmarshal(usage, &update)
				update["sessionUpdate"] = json.RawMessage(`"usage_update"`)
				data, _ := json.Marshal(update)
				fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"usage-session","update":%s}}`+"\n", data)
			}
			if turn.Text != "" {
				fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"usage-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`+"\n", turn.Text)
			}
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", req.ID, turn.Result)
		}
	}
	os.Exit(0)
}
