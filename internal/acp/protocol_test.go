package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestContentChunkFromUpdateDistinguishesMessageAndThought(t *testing.T) {
	message := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}}}`)
	thought := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"pondering"}}}}`)
	other := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"c1"}}}`)
	history := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"old"},"history":true}}}`)

	if text, kind, ok := contentChunkFromUpdate(message); !ok || kind != chunkKindMessage || text != "answer" {
		t.Fatalf("message chunk = (%q, %q, %v), want (answer, message, true)", text, kind, ok)
	}
	if text, kind, ok := contentChunkFromUpdate(thought); !ok || kind != chunkKindThought || text != "pondering" {
		t.Fatalf("thought chunk = (%q, %q, %v), want (pondering, thought, true)", text, kind, ok)
	}
	if _, _, ok := contentChunkFromUpdate(other); ok {
		t.Fatal("tool_call update parsed as content chunk, want ignored")
	}
	if _, _, ok := contentChunkFromUpdate(history); ok {
		t.Fatal("historical thought chunk parsed, want ignored")
	}
}

func TestAssistantTextFromEventExtractsMessage(t *testing.T) {
	line := []byte(`{"id":"e1","session_id":"s1","message":{"message":{"role":"assistant","content":"smoke-ok","response_meta":{"finish_reason":"stop"}}}}`)

	got, kind, ok := assistantTextFromEvent(line)
	if !ok {
		t.Fatal("assistantTextFromEvent() ok = false, want true")
	}
	if got != "smoke-ok" {
		t.Fatalf("assistantTextFromEvent() = %q, want %q", got, "smoke-ok")
	}
	if kind != assistantEventKindMessage {
		t.Fatalf("assistantTextFromEvent() kind = %q, want %q", kind, assistantEventKindMessage)
	}
}

func TestAssistantTextFromEventExtractsAgentEnd(t *testing.T) {
	line := []byte(`{"id":"e2","session_id":"s1","agent_end":{"output":{"role":"assistant","content":"done","response_meta":{"finish_reason":"stop"}}}}`)

	got, kind, ok := assistantTextFromEvent(line)
	if !ok {
		t.Fatal("assistantTextFromEvent() ok = false, want true")
	}
	if got != "done" {
		t.Fatalf("assistantTextFromEvent() = %q, want %q", got, "done")
	}
	if kind != assistantEventKindAgentEnd {
		t.Fatalf("assistantTextFromEvent() kind = %q, want %q", kind, assistantEventKindAgentEnd)
	}
}

func TestAssistantTextFromEventPrefersMessageOverAgentEnd(t *testing.T) {
	line := []byte(`{"message":{"message":{"role":"assistant","content":"message text"}},"agent_end":{"output":{"role":"assistant","content":"end text"}}}`)

	got, kind, ok := assistantTextFromEvent(line)
	if !ok {
		t.Fatal("assistantTextFromEvent() ok = false, want true")
	}
	if got != "message text" {
		t.Fatalf("assistantTextFromEvent() = %q, want message text", got)
	}
	if kind != assistantEventKindMessage {
		t.Fatalf("assistantTextFromEvent() kind = %q, want %q", kind, assistantEventKindMessage)
	}
}

func TestAssistantTextFromEventIgnoresNonAssistantMessage(t *testing.T) {
	line := []byte(`{"message":{"message":{"role":"user","content":"hello"}}}`)

	if got, _, ok := assistantTextFromEvent(line); ok {
		t.Fatalf("assistantTextFromEvent() = %q, true; want ignored user message", got)
	}
}

func TestToolUpdateFromLineExtractsToolCall(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"List files","status":"pending","kind":"read"}}}`)

	got, ok := toolUpdateFromLine(line)
	if !ok {
		t.Fatal("toolUpdateFromLine() ok = false, want true")
	}
	if got.ID != "call-1" || got.Title != "List files" || got.Status != "pending" || got.Kind != "read" || got.Update != "tool_call" {
		t.Fatalf("toolUpdateFromLine() = %#v", got)
	}
}

func TestToolUpdateFromLineCarriesRawInput(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"bash","status":"in_progress","rawInput":{"Command":"echo hi","Description":"Echo hi"}}}}`)

	got, ok := toolUpdateFromLine(line)
	if !ok {
		t.Fatal("toolUpdateFromLine() ok = false, want true")
	}
	if detail := toolInputDetail(got.RawInput); detail != "echo hi" {
		t.Fatalf("toolInputDetail() = %q, want %q", detail, "echo hi")
	}
}

func TestEnrichToolTitle(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		rawInput string
		want     string
	}{
		{"bash command", "bash", `{"Command":"echo hello-from-bash","Description":"Echo"}`, "bash echo hello-from-bash"},
		{"lowercase command", "bash", `{"command":"ls -la"}`, "bash ls -la"},
		{"argv command", "exec", `{"command":["go","test","./..."]}`, "exec go test ./..."},
		{"file title untouched", "Write /tmp/demo.txt", `{"content":"alpha","path":"/tmp/demo.txt"}`, "Write /tmp/demo.txt"},
		{"bare title falls back to path", "read", `{"path":"/tmp/demo.txt"}`, "read /tmp/demo.txt"},
		{"no rawInput keeps bare title", "bash", ``, "bash"},
		{"multiline command collapsed", "bash", "{\"Command\":\"echo a\\n  echo b\"}", "bash echo a echo b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := enrichToolTitle(tt.title, json.RawMessage(tt.rawInput))
			if got != tt.want {
				t.Fatalf("enrichToolTitle(%q, %s) = %q, want %q", tt.title, tt.rawInput, got, tt.want)
			}
		})
	}
}

func TestSummarizeDetailTruncates(t *testing.T) {
	long := strings.Repeat("x", maxToolDetailRunes+50)
	got := summarizeDetail(long)
	if r := []rune(got); len(r) != maxToolDetailRunes+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("summarizeDetail length = %d, suffix ok = %v", len([]rune(got)), strings.HasSuffix(got, "…"))
	}
}

func TestToolUpdateFromLineExtractsToolCallUpdate(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-1","status":"completed"}}}`)

	got, ok := toolUpdateFromLine(line)
	if !ok {
		t.Fatal("toolUpdateFromLine() ok = false, want true")
	}
	if got.ID != "call-1" || got.Status != "completed" || got.Update != "tool_call_update" {
		t.Fatalf("toolUpdateFromLine() = %#v", got)
	}
}

func TestSemanticUpdateFromLineMapsPlanUsageAndAvailableCommands(t *testing.T) {
	t.Run("available commands", func(t *testing.T) {
		line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"review","description":"Review changes","input":{"hint":"<path>"}},{"name":"amux-skill","description":"Run a skill"}]}}}`)
		got, ok := semanticUpdateFromLine(line)
		if !ok || !got.HasCommands || len(got.AvailableCommands) != 2 {
			t.Fatalf("semanticUpdateFromLine() = %+v, %v, want commands", got, ok)
		}
		if got.AvailableCommands[0].Name != "review" || got.AvailableCommands[0].Description != "Review changes" || got.AvailableCommands[0].InputHint != "<path>" {
			t.Fatalf("first command = %+v", got.AvailableCommands[0])
		}
	})
	t.Run("plan", func(t *testing.T) {
		line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"content":"Inspect","priority":"high","status":"completed"},{"content":"Fix","priority":"high","status":"in_progress"}]}}}`)
		got, ok := semanticUpdateFromLine(line)
		if !ok || got.Plan == nil {
			t.Fatalf("semanticUpdateFromLine() = %+v, %v, want plan", got, ok)
		}
		if len(got.Plan.Entries) != 2 || got.Plan.Entries[1].Content != "Fix" || got.Plan.Entries[1].Status != "in_progress" {
			t.Fatalf("plan = %+v, want complete entries", got.Plan)
		}
	})

	t.Run("usage", func(t *testing.T) {
		line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":48151,"size":1050000,"cost":{"amount":0.240995,"currency":"USD"}}}}`)
		got, ok := semanticUpdateFromLine(line)
		if !ok || len(got.Usage) == 0 {
			t.Fatalf("semanticUpdateFromLine() = %+v, %v, want usage", got, ok)
		}
		var usage struct {
			Used int64 `json:"used"`
			Size int64 `json:"size"`
		}
		if err := json.Unmarshal(got.Usage, &usage); err != nil {
			t.Fatalf("unmarshal usage: %v", err)
		}
		if usage.Used != 48151 || usage.Size != 1050000 {
			t.Fatalf("usage = %+v, want observed ACP values", usage)
		}
	})
}

func TestUnknownSessionUpdateFromLinePreservesRawFrame(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"current_mode_update","modeId":"code"}}}`)
	name, raw, ok := unknownSessionUpdateFromLine(line)
	if !ok || name != "current_mode_update" || !bytes.Equal(raw, line) {
		t.Fatalf("unknown update = %q %s %v", name, raw, ok)
	}
	commands := []byte(`{"method":"session/update","params":{"update":{"sessionUpdate":"available_commands_update","availableCommands":[]}}}`)
	if _, _, ok := unknownSessionUpdateFromLine(commands); ok {
		t.Fatal("available_commands_update must be mapped as a semantic event")
	}
	user := []byte(`{"method":"session/update","params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"secret"}}}}`)
	if _, _, ok := unknownSessionUpdateFromLine(user); ok {
		t.Fatal("user_message_chunk must not be stored as unknown")
	}
}

func TestToolUpdateFromLinePreservesRichContent(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-1","status":"completed","title":"Edit file","content":[{"type":"content","content":{"type":"text","text":"command output"}},{"type":"diff","path":"main.go","oldText":"old","newText":"new"},{"type":"terminal","terminalId":"term-1"}],"locations":[{"path":"/repo/main.go","line":12}],"rawInput":{"path":"main.go"},"rawOutput":{"changed":true}}}}`)
	got, ok := toolUpdateFromLine(line)
	if !ok {
		t.Fatal("toolUpdateFromLine() = false, want true")
	}
	if len(got.Content) == 0 || len(got.Locations) == 0 || len(got.RawOutput) == 0 {
		t.Fatalf("tool detail = %+v, want content/locations/rawOutput", got)
	}
	if got.OutputPreview != "command output" {
		t.Fatalf("OutputPreview = %q, want command output", got.OutputPreview)
	}
}

func TestPromptBufferFlushesOnCarriageReturn(t *testing.T) {
	var got []string
	buf := newPromptBuffer(func(text string) error {
		got = append(got, text)
		return nil
	})

	if _, err := buf.Write([]byte("hello")); err != nil {
		t.Fatalf("Write text: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got prompts after partial input = %v, want none", got)
	}
	if _, err := buf.Write([]byte("\r")); err != nil {
		t.Fatalf("Write CR: %v", err)
	}
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("got prompts = %v, want [hello]", got)
	}
}

func TestPromptBufferPreservesNewlinesUntilCarriageReturn(t *testing.T) {
	var got []string
	buf := newPromptBuffer(func(text string) error {
		got = append(got, text)
		return nil
	})

	if _, err := buf.Write([]byte("line 1\nline 2\nline 3")); err != nil {
		t.Fatalf("Write multiline text: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got prompts after multiline text = %v, want none before CR", got)
	}
	if _, err := buf.Write([]byte("\r")); err != nil {
		t.Fatalf("Write CR: %v", err)
	}

	want := "line 1\nline 2\nline 3"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got prompts = %q, want [%q]", got, want)
	}
}
