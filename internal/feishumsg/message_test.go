package feishumsg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/i18n"
)

func TestMessageValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message Message
		valid   bool
	}{
		{"group", Message{ReceiveID: "oc-a", ReceiveIDType: "chat_id", Text: "hello"}, true},
		{"open_id disabled", Message{ReceiveID: "recipient", ReceiveIDType: "open_id", Text: "hello"}, false},
		{"user_id disabled", Message{ReceiveID: "recipient", ReceiveIDType: "user_id", Text: "hello"}, false},
		{"union_id disabled", Message{ReceiveID: "recipient", ReceiveIDType: "union_id", Text: "hello"}, false},
		{"email disabled", Message{ReceiveID: "recipient", ReceiveIDType: "email", Text: "hello"}, false},
		{"reply", Message{ReplyToMessageID: "om-a", ReplyInThread: true, Text: "hello"}, true},
		{"explicit text", Message{ReplyToMessageID: "om-a", Text: "**hello**", Format: "text"}, true},
		{"markdown", Message{ReplyToMessageID: "om-a", Text: "**hello**", Format: "markdown"}, true},
		{"bad format", Message{ReplyToMessageID: "om-a", Text: "hello", Format: "html"}, false},
		{"blank markdown", Message{ReplyToMessageID: "om-a", Text: " \n", Format: "markdown"}, false},
		{"no destination", Message{Text: "hello"}, false},
		{"missing type", Message{ReceiveID: "oc-a", Text: "hello"}, false},
		{"bad type", Message{ReceiveID: "oc-a", ReceiveIDType: "bot_id", Text: "hello"}, false},
		{"mixed", Message{ReceiveID: "oc-a", ReceiveIDType: "chat_id", ReplyToMessageID: "om-a", Text: "hello"}, false},
		{"thread without reply", Message{ReceiveID: "oc-a", ReceiveIDType: "chat_id", ReplyInThread: true, Text: "hello"}, false},
		{"blank", Message{ReplyToMessageID: "om-a", Text: " \n"}, false},
		{"large encoded", Message{ReplyToMessageID: "om-a", Text: strings.Repeat("\x00", 4000)}, false},
		{"long key", Message{ReplyToMessageID: "om-a", Text: "hello", UUID: strings.Repeat("x", 51)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.message.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate = %v, valid=%v", err, tc.valid)
			}
		})
	}
	text := "  中文 <hello> \"quoted\"\n"
	var decoded map[string]string
	if err := json.Unmarshal([]byte((Message{Text: text}).Content()), &decoded); err != nil || decoded["text"] != text {
		t.Fatalf("content changed: %v, %v", decoded, err)
	}
}

func TestMarkdownContentAndEncodedLimit(t *testing.T) {
	const markdown = "# 标题\n\n**bold** and *italic* [link](https://example.com/?a=1&b=2)\n\n- item\n\n```go\nfmt.Println(\"<hello>\")\n```\n\n| A | B |\n|---|---|\n| 1 | 2 |\n"
	msg := Message{ReplyToMessageID: "om-a", Text: markdown, Format: "markdown"}
	if err := msg.Validate(); err != nil {
		t.Fatal(err)
	}
	var post map[string]struct {
		Content [][]map[string]string `json:"content"`
	}
	if err := json.Unmarshal([]byte(msg.Content()), &post); err != nil {
		t.Fatal(err)
	}
	blocks := post["zh_cn"].Content
	if msg.MessageType() != "post" || len(post) != 1 || len(blocks) != 1 || len(blocks[0]) != 1 || blocks[0][0]["tag"] != "md" || blocks[0][0]["text"] != markdown {
		t.Fatalf("invalid Markdown post: %s", msg.Content())
	}
	for _, format := range []string{"", "text", "markdown"} {
		t.Run("size/"+format, func(t *testing.T) {
			msg := Message{ReplyToMessageID: "om-a", Format: format}
			overhead := len(msg.Content())
			msg.Text = strings.Repeat("a", (20<<10)-overhead)
			if err := msg.Validate(); err != nil {
				t.Fatalf("exact limit rejected: %v", err)
			}
			msg.Text += "a"
			if err := msg.Validate(); err == nil {
				t.Fatal("accepted oversized encoded message")
			}
		})
	}
	for _, format := range []string{"", "text"} {
		msg.Format = format
		var body map[string]string
		if err := json.Unmarshal([]byte(msg.Content()), &body); err != nil || body["text"] != markdown || len(body) != 1 || msg.MessageType() != "text" {
			t.Fatalf("plain text compatibility: %s, %v", msg.Content(), err)
		}
	}
}

func TestCopyLanguagesAndFallback(t *testing.T) {
	var entries map[string]i18n.Entry
	if err := json.Unmarshal(english, &entries); err != nil {
		t.Fatal(err)
	}
	for key, entry := range entries {
		args := []any{}
		for i := 0; i < strings.Count(entry.Other, "%s"); i++ {
			args = append(args, "原文 raw")
		}
		en := catalog.Format(i18n.English, key, args...)
		zh := catalog.Format(i18n.Chinese, key, args...)
		if en == key || zh == key || en == zh || strings.Contains(en+zh, "%!") {
			t.Fatalf("bad translations %s: %q %q", key, en, zh)
		}
		if catalog.Format("unknown", key, args...) != en {
			t.Fatalf("fallback differs: %s", key)
		}
		if !strings.Contains(Copy(key, args...), en) || !strings.Contains(Copy(key, args...), zh) {
			t.Fatalf("missing language: %s", key)
		}
	}
}
