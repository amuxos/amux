// Package feishumsg defines the Agent-facing Feishu send contract. Bot credentials
// and platform delivery remain owned by Bridge.
package feishumsg

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"

	"github.com/amuxos/amux/internal/i18n"
)

//go:embed en.json
var english []byte

//go:embed zh-CN.json
var chinese []byte

var catalog = func() *i18n.Catalog {
	c, err := i18n.New(english, chinese)
	if err != nil {
		panic(err)
	}
	return c
}()

// Copy preserves both languages on the language-neutral MCP/RPC surface.
func Copy(key string, args ...any) string {
	return catalog.Format(i18n.English, key, args...) + " " + catalog.Format(i18n.Chinese, key, args...)
}

type Message struct {
	ReceiveID        string `json:"receive_id,omitempty"`
	ReceiveIDType    string `json:"receive_id_type,omitempty"`
	ReplyToMessageID string `json:"reply_to_message_id,omitempty"`
	ReplyInThread    bool   `json:"reply_in_thread,omitempty"`
	Text             string `json:"text"`
	Format           string `json:"format,omitempty"`
	UUID             string `json:"uuid,omitempty"`
}

func (m Message) Validate() error {
	switch m.Format {
	case "", "text", "markdown":
	default:
		return errors.New(Copy("invalid_format"))
	}
	if strings.TrimSpace(m.Text) == "" {
		return errors.New(Copy("empty_text"))
	}
	if len(m.Content()) > 20<<10 {
		return errors.New(Copy("large_text"))
	}
	if len(m.UUID) > 50 {
		return errors.New(Copy("uuid_length"))
	}
	if strings.TrimSpace(m.ReplyToMessageID) != "" {
		if m.ReceiveID != "" || m.ReceiveIDType != "" {
			return errors.New(Copy("destination"))
		}
		return nil
	}
	if strings.TrimSpace(m.ReceiveID) == "" || m.ReceiveIDType == "" || m.ReplyInThread {
		return errors.New(Copy("destination"))
	}
	switch m.ReceiveIDType {
	case "chat_id":
		return nil
	default:
		return errors.New(Copy("id_type"))
	}
}

func (m Message) Content() string {
	var content any = map[string]string{"text": m.Text}
	if m.Format == "markdown" {
		// Feishu renders Markdown in a native rich-text post. Keep the source
		// intact rather than applying the Agent reply renderer's transformations.
		content = map[string]any{"zh_cn": map[string]any{
			"content": [][]map[string]string{{{"tag": "md", "text": m.Text}}},
		}}
	}
	data, _ := json.Marshal(content)
	return string(data)
}

func (m Message) MessageType() string {
	if m.Format == "markdown" {
		return "post"
	}
	return "text"
}

type Result struct {
	MessageID string
	ChatID    string
	ThreadID  string
}
