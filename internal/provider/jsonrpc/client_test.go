package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type rw struct {
	io.Reader
	io.Writer
}

func TestClientCallResponse(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	c := NewClient(clientR, clientW)
	go func() { _ = c.Run() }()
	defer clientR.Close()
	defer clientW.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		line, err := readLine(serverR)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var msg Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Errorf("unmarshal request: %v", err)
			return
		}
		if msg.Method != "ping" || msg.ID == 0 {
			t.Errorf("request = %+v, want ping with id", msg)
		}
		_, _ = serverW.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := c.Call(ctx, "ping", map[string]bool{"ok": true})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(string(resp.Result), "ok") {
		t.Fatalf("result = %s, want ok", resp.Result)
	}
	<-serverDone
}

func TestClientNotification(t *testing.T) {
	r, w := io.Pipe()
	c := NewClient(r, io.Discard)
	got := make(chan Message, 1)
	c.OnNotification("event", func(msg Message) { got <- msg })
	go func() { _ = c.Run() }()
	defer r.Close()

	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","method":"event","params":{"type":"turn_done"}}` + "\n"))
	select {
	case msg := <-got:
		if msg.Method != "event" {
			t.Fatalf("method = %q, want event", msg.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("notification timed out")
	}
}

func TestClientZeroIDRequestAndNotification(t *testing.T) {
	var output bytes.Buffer
	c := NewClient(strings.NewReader("{\"id\":0,\"method\":\"approve\",\"params\":{}}\n{\"method\":\"approve\",\"params\":{}}\n"), &output)
	requests, notifications := 0, 0
	c.OnRequest("approve", func(msg Message) {
		requests++
		if err := c.Respond(msg.ID, map[string]string{"decision": "decline"}); err != nil {
			t.Fatal(err)
		}
	})
	c.OnNotification("approve", func(Message) { notifications++ })
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || notifications != 1 {
		t.Fatalf("requests=%d notifications=%d, want one of each", requests, notifications)
	}
	var response struct {
		ID     *int64 `json:"id"`
		Result struct {
			Decision string `json:"decision"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID == nil || *response.ID != 0 || response.Result.Decision != "decline" {
		t.Fatalf("zero-id response lost its identity: %s", output.Bytes())
	}
}

func TestClientUnknownZeroIDRequestRepliesError(t *testing.T) {
	var output bytes.Buffer
	c := NewClient(strings.NewReader("{\"id\":0,\"method\":\"unknown\",\"params\":{}}\n"), &output)
	hooked := false
	c.OnUnknownRequest(func(method string, id int64, _ json.RawMessage) {
		hooked = method == "unknown" && id == 0
	})
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID    *int64 `json:"id"`
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	if !hooked || response.ID == nil || *response.ID != 0 || response.Error == nil || response.Error.Code != -32601 {
		t.Fatalf("unknown zero-id request was not rejected: hooked=%v response=%s", hooked, output.Bytes())
	}
}

func TestClientCallContextCancel(t *testing.T) {
	r := strings.NewReader("")
	c := NewClient(r, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Call(ctx, "slow", nil)
	if err == nil {
		t.Fatal("Call error = nil, want context cancellation")
	}
}

// captureStderr swaps os.Stderr to capture drop-log output. Returns a restore
// func and the buffer holding captured bytes.
func captureStderr(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(&buf, r); close(done) }()
	return &buf, func() {
		os.Stderr = orig
		w.Close()
		<-done
		r.Close()
	}
}

func TestDebugDropEnvGated(t *testing.T) {
	// Disabled: no output.
	t.Setenv("AMUX_DEBUG_JSONRPC", "")
	buf, restore := captureStderr(t)
	debugDrop("notification", "thread/status/changed", 0, json.RawMessage(`{"status":{"type":"idle"}}`))
	restore()
	if buf.Len() != 0 {
		t.Fatalf("expected no output when disabled, got %q", buf.String())
	}

	// Enabled: output with method + truncated params.
	t.Setenv("AMUX_DEBUG_JSONRPC", "1")
	buf2, restore2 := captureStderr(t)
	long := json.RawMessage(`"` + strings.Repeat("x", 300) + `"`)
	debugDrop("notification", "thread/status/changed", 0, long)
	restore2()
	out := buf2.String()
	if !strings.Contains(out, "thread/status/changed") {
		t.Fatalf("expected method in output, got %q", out)
	}
	if !strings.Contains(out, "...(truncated)") {
		t.Fatalf("expected truncation marker, got %q", out)
	}
}

func TestDispatchDropsUnhandledNotificationLogs(t *testing.T) {
	t.Setenv("AMUX_DEBUG_JSONRPC", "1")
	r, w := io.Pipe()
	c := NewClient(r, io.Discard)
	go func() { _ = c.Run() }()
	defer r.Close()

	buf, restore := captureStderr(t)
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","method":"thread/status/changed","params":{"status":{"type":"idle"}}}` + "\n"))
	// Give the reader goroutine time to dispatch.
	time.Sleep(100 * time.Millisecond)
	restore()

	if !strings.Contains(buf.String(), "thread/status/changed") {
		t.Fatalf("expected drop log for unhandled notification, got %q", buf.String())
	}
}

// TestDispatchUnhandledServerRequestRepliesError verifies that a
// server->client request with no registered handler gets a -32601 JSON-RPC
// error response (so the provider does not block waiting) and fires the
// OnUnknownRequest hook.
func TestDispatchUnhandledServerRequestRepliesError(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	c := NewClient(clientR, clientW)
	go func() { _ = c.Run() }()
	defer clientR.Close()
	defer clientW.Close()

	hooked := make(chan string, 1)
	c.OnUnknownRequest(func(method string, id int64, _ json.RawMessage) {
		hooked <- method
	})

	// Provider sends an unhandled server->client request (e.g. list_models).
	_, _ = serverW.Write([]byte(`{"jsonrpc":"2.0","id":42,"method":"list_models","params":{}}` + "\n"))

	select {
	case m := <-hooked:
		if m != "list_models" {
			t.Fatalf("hook method = %q, want list_models", m)
		}
	case <-time.After(time.Second):
		t.Fatal("OnUnknownRequest hook did not fire")
	}

	// The client should have written a -32601 error response.
	line, err := readLine(serverR)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp Message
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.ID != 42 || resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("response = %+v, want id=42 error code=-32601", resp)
	}
	if !strings.Contains(resp.Error.Message, "list_models") {
		t.Fatalf("error message = %q, want it to mention list_models", resp.Error.Message)
	}
}

func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
}
