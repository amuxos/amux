package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/provider"
	providerjsonrpc "github.com/amuxos/amux/internal/provider/jsonrpc"
	"github.com/amuxos/amux/internal/session"
)

type Proc struct {
	handle isolation.ProcessHandle
	client *providerjsonrpc.Client
	stdin  io.WriteCloser

	inputMu sync.Mutex
	input   bytes.Buffer

	eventSinks session.EventSinks

	pidMu sync.Mutex
	pid   int
}

type StartOptions struct {
	SessionID        string
	ResumeSessionID  string
	Cwd              string
	Env              []string
	ExtraArgs        []string
	SidecarPath      string
	ClaudeExecutable string
	Backend          isolation.LaunchBackend
	LaunchContext    isolation.LaunchContext
}

type SessionStartParams struct {
	AmuxSessionID    string   `json:"amuxSessionId"`
	Cwd              string   `json:"cwd,omitempty"`
	Env              []string `json:"env,omitempty"`
	ExtraArgs        []string `json:"extraArgs,omitempty"`
	ClaudeExecutable string   `json:"claudeExecutable,omitempty"`
	Resume           string   `json:"resume,omitempty"`
}

type EventNotification struct {
	Event provider.Event `json:"event"`
}

type PermissionNotification struct {
	Permission provider.Permission `json:"permission"`
}

func resolveSidecarPath(override string) string {
	candidates := []string{}
	if override != "" {
		candidates = append(candidates, override)
	}
	if env := os.Getenv("AMUX_CLAUDE_SIDECAR"); env != "" {
		candidates = append(candidates, env)
	}
	if exe, err := os.Executable(); err == nil {
		// Packaged desktop layout: <App>/Contents/Resources/bin/amux(let)
		// plus <App>/Contents/Resources/sidecars/claude-sdk/dist/index.js.
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "..", "sidecars", "claude-sdk", "dist", "index.js"),
		)
		// Development layout: repo/bin/amux(let) plus repo/sidecars/...
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "..", "sidecars", "claude-sdk", "dist", "index.js"),
		)
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return "amux-claude-sdk-sidecar"
}

func Start(ctx context.Context, opts StartOptions) (*Proc, <-chan []byte, error) {
	sidecar := resolveSidecarPath(opts.SidecarPath)
	backend := opts.Backend
	if backend == nil {
		backend = isolation.NewPassthroughBackend()
	}
	handle, err := backend.Start(ctx, isolation.LaunchSpec{
		Path:            sidecar,
		Env:             append([]string(nil), opts.Env...),
		Dir:             opts.Cwd,
		SetProcessGroup: true,
		Context:         opts.LaunchContext,
	})
	if err != nil {
		return nil, nil, err
	}
	stdout, stdin := handle.Stdout(), handle.Stdin()
	go func() { _, _ = io.Copy(os.Stderr, handle.Stderr()) }()

	p := &Proc{handle: handle, stdin: stdin, pid: handle.PID()}
	p.client = providerjsonrpc.NewClient(stdout, stdin)
	out := make(chan []byte, 256)
	p.client.OnNotification("event", func(msg providerjsonrpc.Message) {
		var n EventNotification
		if err := json.Unmarshal(msg.Params, &n); err == nil {
			p.handleProviderEvent(n.Event)
		}
	})
	p.client.OnNotification("permission.request", func(msg providerjsonrpc.Message) {
		var n PermissionNotification
		if err := json.Unmarshal(msg.Params, &n); err == nil {
			p.handlePermission(n.Permission)
		}
	})
	p.client.OnNotification("runtime.pid", func(msg providerjsonrpc.Message) {
		var n struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(msg.Params, &n); err == nil && n.PID > 0 {
			p.pidMu.Lock()
			p.pid = n.PID
			p.pidMu.Unlock()
		}
	})
	p.client.OnNotification("session.id", func(msg providerjsonrpc.Message) {
		var n struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(msg.Params, &n); err == nil && n.SessionID != "" {
			p.emitEvent(agentstream.ProducerEmission{
				Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: "claude", NativeType: "session.id"},
				Session: agentstream.SessionMetadataChanged{AgentSessionID: n.SessionID},
			})
		}
	})
	go func() {
		_ = p.client.Run()
		close(out)
	}()

	if _, err := p.client.Call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"client":          map[string]string{"name": "amux", "version": "dev"},
	}); err != nil {
		_ = p.Close()
		return nil, nil, fmt.Errorf("claude sidecar initialize: %w", err)
	}
	if _, err := p.client.Call(ctx, "session.start", SessionStartParams{
		AmuxSessionID: opts.SessionID,
		Cwd:           opts.Cwd,
		// The SDK inherits the sidecar's effective process environment, which
		// the launch backend may have rewritten for principal isolation. Do
		// not replay the original Runtime env over those isolated HOME paths.
		ExtraArgs:        opts.ExtraArgs,
		ClaudeExecutable: opts.ClaudeExecutable,
		Resume:           opts.ResumeSessionID,
	}); err != nil {
		_ = p.Close()
		return nil, nil, fmt.Errorf("claude sidecar session.start: %w", err)
	}
	return p, out, nil
}

func (p *Proc) PID() int {
	p.pidMu.Lock()
	defer p.pidMu.Unlock()
	return p.pid
}

func (p *Proc) SubmitInput(input session.UserInput) error {
	return p.submitInput(context.Background(), input)
}

func (p *Proc) submitInput(ctx context.Context, input session.UserInput) error {
	if input.ImageCount() > 0 {
		return fmt.Errorf("claude sidecar does not accept image input")
	}
	text := input.Text()
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.submitText(ctx, text)
}

func (p *Proc) submitText(ctx context.Context, text string) error {
	_, err := p.client.Call(ctx, "turn.start", map[string]string{"text": text})
	return err
}

func (p *Proc) Write(data []byte) (int, error) {
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	written := len(data)
	for _, b := range data {
		if b == '\r' || b == '\n' {
			text := strings.TrimSpace(p.input.String())
			p.input.Reset()
			if text == "" {
				continue
			}
			if err := p.submitText(context.Background(), text); err != nil {
				return 0, err
			}
			continue
		}
		_ = p.input.WriteByte(b)
	}
	return written, nil
}

func (p *Proc) DispatchTurn(ctx context.Context, req session.TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(req.TurnToken) == "" {
		return fmt.Errorf("turn token is required")
	}
	if req.Control != "" {
		return fmt.Errorf("claude session control %q is unsupported", req.Control)
	}
	if req.Input.ImageCount() > 0 {
		return fmt.Errorf("claude sidecar does not support image input")
	}
	if err := p.eventSinks.BeginTurn(sink); err != nil {
		return err
	}
	if err := p.submitInput(ctx, req.Input); err != nil {
		p.eventSinks.AbortTurn()
		return err
	}
	return nil
}

func (p *Proc) Interrupt() error {
	_, err := p.client.Call(context.Background(), "interrupt", map[string]bool{"ok": true})
	return err
}

func (p *Proc) RespondPermission(permissionID, optionID string) error {
	_, err := p.client.Call(context.Background(), "permission.respond", provider.PermissionResponse{PermissionId: permissionID, OptionId: optionID})
	return err
}

// RespondPermissionMulti is not supported on the Claude sidecar protocol; the
// multi-select user-input path only applies to Codex-compatible app-server.
func (p *Proc) RespondPermissionMulti(permissionID string, optionIDs []string) error {
	return fmt.Errorf("multi-select permission response is not supported for claude provider")
}

func (p *Proc) SetEventSink(fn func(agentstream.ProducerEmission) bool) {
	p.eventSinks.SetGlobal(fn)
}

func (p *Proc) Close() error {
	_, _ = p.client.Call(context.Background(), "shutdown", nil)
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.handle != nil {
		_ = p.handle.Signal(context.Background(), "TERM")
		_ = p.handle.Close()
	}
	return nil
}

func (p *Proc) handleProviderEvent(ev provider.Event) {
	emission, ignore, err := emissionFromSidecarEvent(ev)
	if err == nil && !ignore {
		p.emitEvent(emission)
	}
}

func (p *Proc) handlePermission(perm provider.Permission) {
	p.emitEvent(agentstream.ProducerEmission{
		Origin: agentstream.Origin{
			Actor: agentstream.ActorAgent, Channel: "agent", Provider: perm.Provider,
			NativeType: "permission_request", NativeID: perm.ID,
		},
		Turn: canonicalClaudePermission(perm),
	})
}

func (p *Proc) emitEvent(ev agentstream.ProducerEmission) {
	p.eventSinks.Emit(ev)
}
