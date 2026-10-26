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
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

func testExecutionConfig(model, effort string) (map[string]string, error) {
	values := make(map[string]string)
	if model != "" {
		values["model"] = model
	}
	if effort != "" {
		values["effort"] = effort
	}
	return values, nil
}

func startConfigProc(t *testing.T, opts StartOptions, modes ...string) (*Proc, string) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "capture.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=^TestACPConfigAgent$")
	cmd.Env = append(os.Environ(), "AMUX_TEST_ACP_CONFIG=1", "AMUX_TEST_ACP_CAPTURE="+capture, "AMUX_TEST_ACP_MODES="+strings.Join(modes, ","))
	opts.Cwd = t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, _, err := StartWithOptions(ctx, cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, capture
}

func configRequests(t *testing.T, capture string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var requests []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var req map[string]any
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, req)
	}
	return requests
}

func TestExecutionConfigFrozenAcrossNewResumeAndClear(t *testing.T) {
	for _, resume := range []string{"", "saved-session"} {
		t.Run("resume="+resume, func(t *testing.T) {
			p, capture := startConfigProc(t, StartOptions{Model: "model-b", ReasoningEffort: "high", ExecutionConfig: testExecutionConfig, ResumeSessionID: resume})
			oldID := p.SessionID()
			if resume != "" && oldID != resume {
				t.Fatalf("resume ID = %q", oldID)
			}
			var terminals int
			var metadata []agentstream.SessionMetadataChanged
			p.SetEventSink(func(e agentstream.ProducerEmission) bool {
				if m, ok := e.Session.(agentstream.SessionMetadataChanged); ok {
					metadata = append(metadata, m)
				}
				return false
			})
			if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{Control: "clear"}, func(e agentstream.ProducerEmission) bool {
				if e.Terminal != nil {
					terminals++
				}
				if m, ok := e.Session.(agentstream.SessionMetadataChanged); ok {
					metadata = append(metadata, m)
				}
				return false
			}); err != nil {
				t.Fatal(err)
			}
			if p.SessionID() == oldID || terminals != 1 {
				t.Fatalf("ID = %q old %q; terminals=%d", p.SessionID(), oldID, terminals)
			}
			if p.EffectiveModel() != "model-b" {
				t.Fatalf("effective model=%q", p.EffectiveModel())
			}
			if len(metadata) != 2 || metadata[1].AgentSessionID != p.SessionID() || metadata[1].EffectiveModel != "model-b" {
				t.Fatalf("metadata=%+v", metadata)
			}
			counts := map[string]int{}
			for _, req := range configRequests(t, capture) {
				if req["method"] == "session/set_config_option" {
					params := req["params"].(map[string]any)
					counts[params["configId"].(string)+":"+params["value"].(string)]++
				}
			}
			if counts["model:model-b"] != 2 || counts["effort:high"] != 2 {
				t.Fatalf("applied settings=%v", counts)
			}
		})
	}
}

func TestExecutionReconfigureReadbackAndRollback(t *testing.T) {
	for _, mode := range []string{"", "bad-readback", "bad-duplicate"} {
		t.Run(mode, func(t *testing.T) {
			p, capture := startConfigProc(t, StartOptions{Model: "model-a", ReasoningEffort: "low", ExecutionConfig: testExecutionConfig}, mode)
			id := p.SessionID()
			var metadata []agentstream.SessionMetadataChanged
			p.SetEventSink(func(e agentstream.ProducerEmission) bool {
				if m, ok := e.Session.(agentstream.SessionMetadataChanged); ok {
					metadata = append(metadata, m)
				}
				return false
			})
			err := p.ReconfigureExecution("model-b", "high")
			if mode == "" {
				if err != nil || p.EffectiveModel() != "model-b" || len(metadata) != 2 {
					t.Fatalf("err=%v model=%q metadata=%+v", err, p.EffectiveModel(), metadata)
				}
			} else {
				if err == nil || p.EffectiveModel() != "model-a" || len(metadata) != 1 {
					t.Fatalf("err=%v model=%q metadata=%+v", err, p.EffectiveModel(), metadata)
				}
				requests := configRequests(t, capture)
				n := len(requests)
				if requests[n-2]["params"].(map[string]any)["value"] != "model-a" || requests[n-1]["params"].(map[string]any)["value"] != "low" {
					t.Fatalf("missing rollback: %+v", requests)
				}
			}
			if p.SessionID() != id {
				t.Fatal("hot config changed native identity")
			}
		})
	}
}

func TestExecutionConfigRejectsUnadvertisedBeforeMutation(t *testing.T) {
	p, capture := startConfigProc(t, StartOptions{ExecutionConfig: testExecutionConfig})
	if err := p.ReconfigureExecution("unknown", "high"); err == nil {
		t.Fatal("unadvertised model accepted")
	}
	for _, req := range configRequests(t, capture) {
		if req["method"] == "session/set_config_option" {
			t.Fatal("invalid config reached native write")
		}
	}
	p.inputMu.Lock()
	err := p.ReconfigureExecution("model-b", "high")
	p.inputMu.Unlock()
	if err != session.ErrTurnSinkActive {
		t.Fatalf("busy error=%v", err)
	}
}

func TestClearFailedConfigurationKeepsPublishedIdentity(t *testing.T) {
	p, _ := startConfigProc(t, StartOptions{Model: "model-b", ReasoningEffort: "high", ExecutionConfig: testExecutionConfig}, "clear-no-config")
	id := p.SessionID()
	var terminals []agentstream.Terminal
	if err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{Control: "clear"}, func(e agentstream.ProducerEmission) bool {
		if e.Terminal != nil {
			terminals = append(terminals, *e.Terminal)
		}
		return false
	}); err != nil {
		t.Fatal(err)
	}
	if p.SessionID() != id || p.EffectiveModel() != "model-b" || len(terminals) != 1 || terminals[0].Outcome != agentstream.OutcomeError {
		t.Fatalf("ID=%q terminals=%+v", p.SessionID(), terminals)
	}
}

func TestCompactRequiresNativeAdvertisement(t *testing.T) {
	for _, mode := range []string{"", "compact"} {
		t.Run(mode, func(t *testing.T) {
			p, capture := startConfigProc(t, StartOptions{}, mode)
			var terminals int
			err := p.DispatchTurn(context.Background(), session.TurnDispatchRequest{Control: "compact"}, func(e agentstream.ProducerEmission) bool {
				if e.Terminal != nil {
					terminals++
				}
				return false
			})
			if mode == "" {
				if err == nil || terminals != 0 {
					t.Fatalf("err=%v terminals=%d", err, terminals)
				}
			} else if err != nil || terminals != 1 {
				t.Fatalf("err=%v terminals=%d", err, terminals)
			}
			prompts := 0
			for _, req := range configRequests(t, capture) {
				if req["method"] == "session/prompt" {
					prompts++
					prompt := req["params"].(map[string]any)["prompt"].([]any)
					if prompt[0].(map[string]any)["text"] != "/compact" {
						t.Fatalf("prompt=%v", prompt)
					}
				}
			}
			if (prompts == 1) != (mode == "compact") {
				t.Fatalf("prompts=%d", prompts)
			}
		})
	}
}

func TestExecutionConfigRequiresMapperForExplicitStartup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestACPConfigAgent$")
	cmd.Env = append(os.Environ(), "AMUX_TEST_ACP_CONFIG=1")
	p, _, err := StartWithOptions(context.Background(), cmd, StartOptions{Model: "model-b"})
	if p != nil {
		p.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("err=%v", err)
	}
}

func TestACPConfigAgent(t *testing.T) {
	if os.Getenv("AMUX_TEST_ACP_CONFIG") != "1" {
		return
	}
	modes := "," + os.Getenv("AMUX_TEST_ACP_MODES") + ","
	has := func(mode string) bool { return strings.Contains(modes, ","+mode+",") }
	sessions := map[string]map[string]string{}
	options := func(values map[string]string) []map[string]any {
		return []map[string]any{
			{"id": "model", "type": "select", "currentValue": values["model"], "options": []any{map[string]any{"group": "models", "options": []any{map[string]string{"value": "model-a"}, map[string]string{"value": "model-b"}}}}},
			{"id": "effort", "type": "select", "currentValue": values["effort"], "options": []any{map[string]string{"value": "low"}, map[string]string{"value": "high"}}},
		}
	}
	output := func(value any) { data, _ := json.Marshal(value); fmt.Println(string(data)) }
	count := 0
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if path := os.Getenv("AMUX_TEST_ACP_CAPTURE"); path != "" {
			f, _ := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if f != nil {
				_, _ = f.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
				_ = f.Close()
			}
		}
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				ConfigID  string `json:"configId"`
				Value     string `json:"value"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			continue
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"resume": map[string]any{}}}}
		case "session/new", "session/resume":
			count++
			id := req.Params.SessionID
			if id == "" {
				id = fmt.Sprintf("session-%d", count)
			}
			sessions[id] = map[string]string{"model": "model-a", "effort": "low"}
			result = map[string]any{"sessionId": id, "configOptions": options(sessions[id])}
			if count > 1 && has("clear-no-config") {
				result = map[string]any{"sessionId": id}
			}
			if has("compact") {
				output(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": id, "update": map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []map[string]string{{"name": "compact", "description": "Compact context"}}}}})
			}
		case "session/set_config_option":
			values := sessions[req.Params.SessionID]
			values[req.Params.ConfigID] = req.Params.Value
			if has("model-resets-effort") && req.Params.ConfigID == "model" && req.Params.Value == "model-b" {
				values["effort"] = "high"
			}
			reported := map[string]string{"model": values["model"], "effort": values["effort"]}
			if has("bad-readback") && req.Params.ConfigID == "effort" && req.Params.Value == "high" {
				reported["effort"] = "low"
			}
			readback := options(reported)
			if has("bad-duplicate") && req.Params.ConfigID == "model" && req.Params.Value == "model-b" {
				readback = append(readback, readback[0])
			}
			result = map[string]any{"configOptions": readback}
		case "session/prompt":
			result = map[string]string{"stopReason": "end_turn"}
		}
		output(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}

func TestExecutionConfigUsesLatestReadbackForDependentOptions(t *testing.T) {
	p, capture := startConfigProc(t, StartOptions{Model: "model-a", ReasoningEffort: "low", ExecutionConfig: testExecutionConfig}, "model-resets-effort")
	if err := p.ReconfigureExecution("model-b", "low"); err != nil {
		t.Fatal(err)
	}
	var values []string
	for _, req := range configRequests(t, capture) {
		if req["method"] == "session/set_config_option" {
			params := req["params"].(map[string]any)
			values = append(values, params["configId"].(string)+":"+params["value"].(string))
		}
	}
	if strings.Join(values, ",") != "model:model-b,effort:low" {
		t.Fatalf("dependent config writes=%v", values)
	}
}

func TestConfigurationUpdatesReplaceCachedAdvertisement(t *testing.T) {
	p := &Proc{}
	if !p.captureConfigUpdate([]byte(`{"method":"session/update","params":{"update":{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","type":"select","currentValue":"new-model","options":[{"value":"new-model"}]}]}}}`)) {
		t.Fatal("configuration update ignored")
	}
	if err := validateConfig(p.configOptions, map[string]string{"model": "new-model"}); err != nil {
		t.Fatal(err)
	}
	p.captureConfigUpdate([]byte(`{"method":"session/update","params":{"update":{"sessionUpdate":"config_option_update","configOptions":true}}}`))
	if err := validateConfig(p.configOptions, map[string]string{"model": "new-model"}); err == nil {
		t.Fatal("malformed update retained stale advertisement")
	}
}
