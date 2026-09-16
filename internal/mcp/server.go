// Package mcp adapts amux's tool registry to the official Model Context
// Protocol Go SDK. The public types stay intentionally small so callers own
// tool semantics while the SDK owns protocol negotiation and stdio JSON-RPC.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var errInputClosed = errors.New("MCP input closed")

// ProtocolVersion is the latest MCP version supported by the pinned SDK.
const ProtocolVersion = "2025-11-25"

// Tool is an MCP tool advertised to the host.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Content is one item of a tool result (text only for Phase 1).
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolResult is the result of a tools/call.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Text builds a single-text-content result.
func Text(s string) ToolResult { return ToolResult{Content: []Content{{Type: "text", Text: s}}} }

// ErrorText builds an error result carrying a message.
func ErrorText(s string) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: s}}, IsError: true}
}

// Handler invokes a tool by name with raw JSON arguments.
type Handler func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)

// Server is a thin adapter around the official MCP SDK server.
type Server struct {
	sdk *sdkmcp.Server

	serveMu      sync.Mutex
	callMu       sync.RWMutex
	callLifetime context.Context
}

// NewServer builds a server advertising the given tools, dispatching tools/call
// to handler.
func NewServer(name, version string, tools []Tool, handler Handler) *Server {
	server := &Server{}
	sdk := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: name, Version: version},
		&sdkmcp.ServerOptions{Capabilities: &sdkmcp.ServerCapabilities{}},
	)

	invoke := func(ctx context.Context, name string, args json.RawMessage) (*sdkmcp.CallToolResult, error) {
		if handler == nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "no tool handler"}
		}
		callCtx, done := server.callContext(ctx)
		defer done()
		result, err := handler(callCtx, name, args)
		if err != nil {
			// Tool errors stay in-band so the model can read and recover from them.
			result = ErrorText(fmt.Sprintf("%s: %v", name, err))
		}
		return toSDKResult(result), nil
	}

	for _, tool := range tools {
		t := tool
		if len(t.InputSchema) == 0 {
			t.InputSchema = json.RawMessage(`{"type":"object"}`)
		}
		sdk.AddTool(&sdkmcp.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return invoke(ctx, req.Params.Name, req.Params.Arguments)
		})
	}

	server.sdk = sdk
	return server
}

func (s *Server) callContext(ctx context.Context) (context.Context, func()) {
	s.callMu.RLock()
	lifetime := s.callLifetime
	s.callMu.RUnlock()
	if lifetime == nil {
		return ctx, func() {}
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, cancel)
	return callCtx, func() {
		stop()
		cancel()
	}
}

func toSDKResult(result ToolResult) *sdkmcp.CallToolResult {
	content := make([]sdkmcp.Content, 0, len(result.Content))
	for _, item := range result.Content {
		content = append(content, &sdkmcp.TextContent{Text: item.Text})
	}
	return &sdkmcp.CallToolResult{Content: content, IsError: result.IsError}
}

// Serve runs the SDK's JSON-RPC loop until the input ends or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	// The SDK documents Run as a single-session convenience. Serialize calls so
	// the per-Serve tool-call lifetime below cannot be replaced by another Run.
	s.serveMu.Lock()
	defer s.serveMu.Unlock()

	serveCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	callLifetime, cancelCalls := context.WithCancel(context.Background())
	s.callMu.Lock()
	s.callLifetime = callLifetime
	s.callMu.Unlock()
	defer func() {
		cancelCalls()
		s.callMu.Lock()
		s.callLifetime = nil
		s.callMu.Unlock()
	}()
	stopCalls := context.AfterFunc(serveCtx, cancelCalls)
	defer stopCalls()
	cancelInput := func() {
		cancelCalls()
		cancel(errInputClosed)
	}
	reader := &eofCancelReadCloser{
		ReadCloser: readCloser(in),
		cancel:     cancelInput,
	}
	err := s.sdk.Run(serveCtx, &sdkmcp.IOTransport{
		Reader: reader,
		Writer: nopWriteCloser{Writer: out},
	})
	if errors.Is(context.Cause(serveCtx), errInputClosed) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// eofCancelReadCloser turns input EOF into cancellation of the server context.
// The SDK waits for in-flight handlers after its read side closes, so without
// this bridge a host that closes stdin during a blocking tool call can leave
// the MCP child process running until that call's own deadline.
type eofCancelReadCloser struct {
	io.ReadCloser
	cancel     func()
	pendingEOF bool
}

func (r *eofCancelReadCloser) Read(p []byte) (int, error) {
	if r.pendingEOF {
		r.pendingEOF = false
		r.cancel()
		return 0, io.EOF
	}
	n, err := r.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		if n > 0 {
			// Deliver the final bytes before surfacing EOF and cancelling the
			// request contexts derived from serveCtx.
			r.pendingEOF = true
			return n, nil
		}
		r.cancel()
	}
	return n, err
}

func readCloser(r io.Reader) io.ReadCloser {
	if rc, ok := r.(io.ReadCloser); ok {
		return rc
	}
	return io.NopCloser(r)
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
