package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/provider"
)

type sessionUpdateMessage struct {
	Method string `json:"method"`
	Params struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			History       bool   `json:"history"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	} `json:"params"`
}

type agentContent struct {
	Type string
	Text string
}

type assistantEventMessage struct {
	Message struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	} `json:"message"`
	AgentEnd struct {
		Output struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"output"`
	} `json:"agent_end"`
}

type assistantEventKind string

const (
	assistantEventKindNone     assistantEventKind = ""
	assistantEventKindMessage  assistantEventKind = "message"
	assistantEventKindAgentEnd assistantEventKind = "agent_end"
)

type toolUpdate struct {
	Update        string
	ID            string
	Title         string
	Status        string
	Kind          string
	RawInput      json.RawMessage
	Content       json.RawMessage
	Locations     json.RawMessage
	RawOutput     json.RawMessage
	OutputPreview string
}

type semanticUpdate struct {
	Plan              *provider.Plan
	Usage             json.RawMessage
	AvailableCommands []provider.AvailableCommand
	HasCommands       bool
}

func semanticUpdateFromLine(line []byte) (semanticUpdate, bool) {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Update json.RawMessage `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Method != "session/update" || len(msg.Params.Update) == 0 {
		return semanticUpdate{}, false
	}
	var header struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(msg.Params.Update, &header); err != nil {
		return semanticUpdate{}, false
	}
	switch header.SessionUpdate {
	case "available_commands_update":
		var update struct {
			AvailableCommands []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Input       *struct {
					Hint string `json:"hint"`
				} `json:"input,omitempty"`
			} `json:"availableCommands"`
		}
		if err := json.Unmarshal(msg.Params.Update, &update); err != nil {
			return semanticUpdate{}, false
		}
		commands := make([]provider.AvailableCommand, 0, len(update.AvailableCommands))
		for _, command := range update.AvailableCommands {
			inputHint := ""
			if command.Input != nil {
				inputHint = command.Input.Hint
			}
			commands = append(commands, provider.AvailableCommand{
				Name: command.Name, Description: command.Description, InputHint: inputHint,
			})
		}
		return semanticUpdate{AvailableCommands: commands, HasCommands: true}, true
	case "plan":
		var plan provider.Plan
		if err := json.Unmarshal(msg.Params.Update, &plan); err != nil {
			return semanticUpdate{}, false
		}
		return semanticUpdate{Plan: &plan}, true
	case "usage_update":
		data := normalizedUsageUpdate(msg.Params.Update)
		return semanticUpdate{Usage: data}, len(data) > 0
	default:
		return semanticUpdate{}, false
	}
}

func unknownSessionUpdateFromLine(line []byte) (string, json.RawMessage, bool) {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &msg) != nil || msg.Method != "session/update" || msg.Params.Update.SessionUpdate == "" {
		return "", nil, false
	}
	if msg.Params.Update.SessionUpdate == "user_message_chunk" ||
		msg.Params.Update.SessionUpdate == "available_commands_update" {
		return "", nil, false
	}
	return msg.Params.Update.SessionUpdate, append(json.RawMessage(nil), line...), true
}

// chunkKind distinguishes the two ContentChunk-shaped session/update variants
// that carry streamed assistant content: a normal message chunk versus the
// agent's internal reasoning (agent_thought_chunk).
type chunkKind string

const (
	chunkKindMessage chunkKind = "message"
	chunkKindThought chunkKind = "thought"
)

// contentChunkFromUpdate parses an agent_message_chunk or agent_thought_chunk
// session/update. Both share the ACP ContentChunk shape (content.text); the
// thought variant is the agent's streamed internal reasoning and is mapped to a
// structured thinking event rather than the output stream. Historical replay
// chunks (history=true) are ignored, matching the message-chunk path.
func contentChunkFromUpdate(line []byte) (string, chunkKind, bool) {
	var msg sessionUpdateMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return "", "", false
	}
	if msg.Method != "session/update" || msg.Params.Update.History {
		return "", "", false
	}
	var kind chunkKind
	switch msg.Params.Update.SessionUpdate {
	case "agent_message_chunk":
		kind = chunkKindMessage
	case "agent_thought_chunk":
		kind = chunkKindThought
	default:
		return "", "", false
	}
	if msg.Params.Update.Content.Text == "" {
		return "", "", false
	}
	return msg.Params.Update.Content.Text, kind, true
}

func assistantTextFromEvent(line []byte) (string, assistantEventKind, bool) {
	var msg assistantEventMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return "", assistantEventKindNone, false
	}
	if msg.Message.Message.Role == "assistant" && msg.Message.Message.Content != "" {
		return msg.Message.Message.Content, assistantEventKindMessage, true
	}
	if msg.AgentEnd.Output.Role == "assistant" && msg.AgentEnd.Output.Content != "" {
		return msg.AgentEnd.Output.Content, assistantEventKindAgentEnd, true
	}
	return "", assistantEventKindNone, false
}

func toolUpdateFromLine(line []byte) (toolUpdate, bool) {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Update struct {
				SessionUpdate string          `json:"sessionUpdate"`
				ToolCallID    string          `json:"toolCallId"`
				Title         string          `json:"title"`
				Status        string          `json:"status"`
				Kind          string          `json:"kind"`
				RawInput      json.RawMessage `json:"rawInput"`
				Content       json.RawMessage `json:"content"`
				Locations     json.RawMessage `json:"locations"`
				RawOutput     json.RawMessage `json:"rawOutput"`
			} `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		return toolUpdate{}, false
	}
	if msg.Method != "session/update" {
		return toolUpdate{}, false
	}
	switch msg.Params.Update.SessionUpdate {
	case "tool_call", "tool_call_update":
	default:
		return toolUpdate{}, false
	}
	if msg.Params.Update.ToolCallID == "" {
		return toolUpdate{}, false
	}
	return toolUpdate{
		Update:        msg.Params.Update.SessionUpdate,
		ID:            msg.Params.Update.ToolCallID,
		Title:         msg.Params.Update.Title,
		Status:        msg.Params.Update.Status,
		Kind:          msg.Params.Update.Kind,
		RawInput:      msg.Params.Update.RawInput,
		Content:       msg.Params.Update.Content,
		Locations:     msg.Params.Update.Locations,
		RawOutput:     msg.Params.Update.RawOutput,
		OutputPreview: toolOutputPreview(msg.Params.Update.Content, msg.Params.Update.RawOutput),
	}, true
}

func toolOutputPreview(content, rawOutput json.RawMessage) string {
	var blocks []struct {
		Type    string `json:"type"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		for i := len(blocks) - 1; i >= 0; i-- {
			if text := summarizeDetail(blocks[i].Content.Text); text != "" {
				return text
			}
		}
	}
	var output struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(rawOutput, &output) == nil {
		return summarizeDetail(output.Output)
	}
	return ""
}

const maxToolDetailRunes = 120

// enrichToolTitle augments a bare tool title (e.g. "bash") with a concise
// summary of its input so the activity card and ACP output show *what* the tool
// is doing, not just its name. Some ACP agents embed the path in file-tool titles
// (e.g. "Write /a/b.txt") but leaves the bash title as the bare tool name with
// the command tucked away in rawInput. Titles that already carry a detail (any
// token past the first) are returned unchanged so we never double up.
func enrichToolTitle(title string, rawInput json.RawMessage) string {
	if title == "" || strings.ContainsAny(title, " \t\n") {
		return title
	}
	detail := toolInputDetail(rawInput)
	if detail == "" {
		return title
	}
	return title + " " + detail
}

// toolInputDetail pulls a one-line summary out of a tool's rawInput, preferring
// the executed command, then a touched path, then a query/url. Some agents use
// PascalCase keys (Command/Description) for bash; other providers and tools use
// lowercase, so both are tried.
func toolInputDetail(rawInput json.RawMessage) string {
	if len(rawInput) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rawInput, &m); err != nil {
		return ""
	}
	for _, k := range []string{"Command", "command", "Cmd", "cmd"} {
		if v, ok := m[k]; ok {
			if s := commandDetail(v); s != "" {
				return s
			}
		}
	}
	for _, k := range []string{"FilePath", "file_path", "Path", "path", "Pattern", "pattern", "Query", "query", "URL", "Url", "url"} {
		if v, ok := m[k]; ok {
			if s := stringDetail(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// commandDetail decodes a command field that may be either a string (Claude/ACP
// bash) or an argv array (Codex exec).
func commandDetail(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return summarizeDetail(s)
	}
	var arr []string
	if json.Unmarshal(v, &arr) == nil {
		return summarizeDetail(strings.Join(arr, " "))
	}
	return ""
}

func stringDetail(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return summarizeDetail(s)
	}
	return ""
}

// summarizeDetail collapses whitespace (commands often span multiple lines) and
// truncates to keep the status line a single readable row.
func summarizeDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxToolDetailRunes {
		return string(r[:maxToolDetailRunes]) + "…"
	}
	return s
}

type promptBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	onFlush func(string) error
}

func newPromptBuffer(onFlush func(string) error) *promptBuffer {
	return &promptBuffer{onFlush: onFlush}
}

func (b *promptBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, c := range data {
		switch c {
		case '\r':
			text := strings.TrimSpace(b.buf.String())
			b.buf.Reset()
			if text != "" {
				if err := b.onFlush(text); err != nil {
					return 0, err
				}
			}
		default:
			_ = b.buf.WriteByte(c)
		}
	}
	return len(data), nil
}
