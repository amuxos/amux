package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServerHandshakeAndToolCall(t *testing.T) {
	tools := []Tool{
		{Name: "amux.test", Description: "test tool", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	handler := func(_ context.Context, name string, args json.RawMessage) (ToolResult, error) {
		if name != "amux.test" {
			return ErrorText("unknown"), nil
		}
		return Text("got:" + string(args)), nil
	}
	srv := NewServer("amux", "test", tools, handler)

	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"amux.test","arguments":{"x":1}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
	}, "\n") + "\n"

	inR, inW := io.Pipe()
	out := &syncBuffer{changed: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), inR, out) }()
	if _, err := io.WriteString(inW, in); err != nil {
		t.Fatalf("write input: %v", err)
	}
	deadline := time.After(time.Second)
	for len(nonEmptyLines(out.String())) < 4 {
		select {
		case <-out.changed:
		case <-deadline:
			t.Fatalf("timed out waiting for responses: %q", out.String())
		}
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}

	lines := nonEmptyLines(out.String())
	// initialize, tools/list, tools/call, ping => 4 responses (the notification has no response).
	if len(lines) != 4 {
		t.Fatalf("expected 4 responses, got %d: %q", len(lines), out.String())
	}
	byID := responseLinesByID(t, lines)

	// initialize
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Tools json.RawMessage `json:"tools"`
			} `json:"capabilities"`
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	mustUnmarshal(t, byID["1"], &init)
	if init.Result.ProtocolVersion != "2024-11-05" || init.Result.ServerInfo.Name != "amux" {
		t.Fatalf("bad initialize result: %s", byID["1"])
	}
	if len(init.Result.Capabilities.Tools) == 0 {
		t.Fatalf("initialize result did not advertise tools capability: %s", byID["1"])
	}

	// tools/list
	var list struct {
		Result struct {
			Tools []Tool `json:"tools"`
		} `json:"result"`
	}
	mustUnmarshal(t, byID["2"], &list)
	if len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "amux.test" {
		t.Fatalf("bad tools/list: %s", byID["2"])
	}

	// tools/call
	var call struct {
		Result ToolResult `json:"result"`
	}
	mustUnmarshal(t, byID["3"], &call)
	if len(call.Result.Content) != 1 || !strings.Contains(call.Result.Content[0].Text, `got:{"x":1}`) {
		t.Fatalf("bad tools/call: %s", byID["3"])
	}
}

func responseLinesByID(t *testing.T, lines []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(lines))
	for _, line := range lines {
		var response struct {
			ID json.RawMessage `json:"id"`
		}
		mustUnmarshal(t, line, &response)
		out[string(response.ID)] = line
	}
	return out
}

type syncBuffer struct {
	mu      sync.Mutex
	buf     strings.Builder
	changed chan struct{}
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	select {
	case b.changed <- struct{}{}:
	default:
	}
	return n, err
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServerUnknownMethod(t *testing.T) {
	srv := NewServer("amux", "test", nil, nil)
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":9,"method":"does/not/exist"}`,
	}, "\n") + "\n"
	out := serveUntilResponses(t, srv, in, 2)
	if !strings.Contains(out, `"id":9`) || !strings.Contains(out, `"error"`) || !strings.Contains(out, "unsupported") {
		t.Fatalf("expected unsupported-method JSON-RPC error, got %q", out)
	}
}

func TestServerNegotiatesUnsupportedProtocolVersion(t *testing.T) {
	srv := NewServer("amux", "test", nil, nil)
	in := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n"
	out := serveUntilResponses(t, srv, in, 1)

	var response struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	mustUnmarshal(t, strings.TrimSpace(out), &response)
	if response.Result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("negotiated protocol version = %q, want %q: %s", response.Result.ProtocolVersion, ProtocolVersion, out)
	}
}

func TestServerRejectsUnadvertisedToolName(t *testing.T) {
	srv := NewServer("amux", "test", []Tool{{
		Name:        "amux.primary",
		Description: "primary tool",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}, func(_ context.Context, name string, _ json.RawMessage) (ToolResult, error) {
		return Text("called:" + name), nil
	})
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"primary","arguments":{}}}`,
	}, "\n") + "\n"
	out := serveUntilResponses(t, srv, in, 3)
	byID := responseLinesByID(t, nonEmptyLines(out))

	var list struct {
		Result struct {
			Tools []Tool `json:"tools"`
		} `json:"result"`
	}
	mustUnmarshal(t, byID["2"], &list)
	if len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "amux.primary" {
		t.Fatalf("tools/list advertised an unexpected name: %s", byID["2"])
	}

	var call struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	mustUnmarshal(t, byID["3"], &call)
	if call.Error.Code != -32602 || !strings.Contains(call.Error.Message, `unknown tool "primary"`) {
		t.Fatalf("unadvertised tool name was not rejected: %s", byID["3"])
	}
}

func TestServerInputEOFCancelsInFlightToolCall(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	srv := NewServer("amux", "test", []Tool{{Name: "amux.block"}}, func(ctx context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ToolResult{}, ctx.Err()
	})

	inR, inW := io.Pipe()
	out := &syncBuffer{changed: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), inR, out) }()
	_, _ = io.WriteString(inW, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"amux.block","arguments":{}}}`,
	}, "\n")+"\n")

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool handler did not start")
	}
	_ = inW.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("input EOF did not cancel the in-flight tool handler")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve after input EOF: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after input EOF")
	}
}

func TestServerRejectsNonJSONInput(t *testing.T) {
	srv := NewServer("amux", "test", nil, nil)
	var out strings.Builder
	err := srv.Serve(context.Background(), strings.NewReader("not-json\n"), &out)
	if err == nil {
		t.Fatal("serve accepted non-JSON input")
	}
}

func TestServerHandlerErrorIsReturnedInBand(t *testing.T) {
	srv := NewServer("amux", "test", []Tool{{Name: "amux.fail"}}, func(context.Context, string, json.RawMessage) (ToolResult, error) {
		return ToolResult{}, errors.New("boom")
	})
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"amux.fail","arguments":{}}}`,
	}, "\n") + "\n"
	out := serveUntilResponses(t, srv, in, 2)
	byID := responseLinesByID(t, nonEmptyLines(out))
	var call struct {
		Result ToolResult `json:"result"`
	}
	mustUnmarshal(t, byID["2"], &call)
	if !call.Result.IsError || len(call.Result.Content) != 1 || call.Result.Content[0].Text != "amux.fail: boom" {
		t.Fatalf("handler error was not returned in-band: %s", byID["2"])
	}
}

func TestServerCancellationDoesNotBlockOtherRequests(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	srv := NewServer("amux", "test", []Tool{{Name: "amux.block"}}, func(ctx context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return Text("late result"), ctx.Err()
	})

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	serveCtx, stopServe := context.WithCancel(context.Background())
	t.Cleanup(func() {
		stopServe()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
	})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(serveCtx, inR, outW) }()
	reader := bufio.NewReader(outR)

	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`+"\n")
	if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, `"id":0`) {
		t.Fatalf("initialize response = %q, %v", line, err)
	}
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"amux.block","arguments":{}}}`+"\n")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool handler did not start")
	}

	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	type readResult struct {
		line string
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		readCh <- readResult{line: line, err: err}
	}()
	var line string
	select {
	case result := <-readCh:
		if result.err != nil {
			t.Fatalf("ping response while tool is running: %v", result.err)
		}
		line = result.line
	case <-time.After(time.Second):
		t.Fatal("ping was blocked by the running tool")
	}
	if !strings.Contains(line, `"id":2`) {
		t.Fatalf("first response = %s, want ping id=2", line)
	}

	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"user interrupted"}}`+"\n")
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("tool handler context was not cancelled")
	}

	restCh := make(chan []byte, 1)
	go func() {
		rest, _ := io.ReadAll(reader)
		restCh <- rest
	}()
	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not stop after input closed")
	}
	_ = outW.Close()
	rest := <-restCh
	if strings.Contains(string(rest), "late result") {
		t.Fatalf("cancelled request produced a late response: %s", rest)
	}
}

func serveUntilResponses(t *testing.T, srv *Server, input string, count int) string {
	t.Helper()
	inR, inW := io.Pipe()
	out := &syncBuffer{changed: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), inR, out) }()
	if _, err := io.WriteString(inW, input); err != nil {
		t.Fatalf("write input: %v", err)
	}
	deadline := time.After(time.Second)
	for len(nonEmptyLines(out.String())) < count {
		select {
		case <-out.changed:
		case <-deadline:
			t.Fatalf("timed out waiting for responses: %q", out.String())
		}
	}
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
	return out.String()
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func mustUnmarshal(t *testing.T, s string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(s), v); err != nil {
		t.Fatalf("unmarshal %q: %v", s, err)
	}
}
