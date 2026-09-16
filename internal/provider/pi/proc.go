// Package pi drives a `pi --mode rpc` subprocess (the pi coding agent) over
// its JSONL RPC protocol. The protocol is strict JSONL over stdio: commands
// are JSON objects written to stdin (one per line, LF-only), responses and
// agent events are JSON objects streamed on stdout.
//
// The proc normalizes pi events into amux session events:
//
//   - agent_start           -> EventTurnStarted (turn boundary)
//   - message_update        -> EventAssistantTextDelta / EventThinkingDelta
//   - tool_execution_*      -> EventToolStarted / EventToolUpdated / EventToolCompleted / EventToolFailed
//   - agent_settled         -> EventResult + turn_done (the amux turn terminal)
//   - prompt rejection      -> turn_done(failed)
//   - process exit mid-run  -> turn_done(failed)
//
// pi has no permission-gating protocol in RPC mode: it executes its tools
// autonomously, so RespondPermission is a no-op error and amux's permission
// policy is not exercised by this provider.
package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/provider"
	"github.com/amuxos/amux/internal/session"
)

// maxWriteDuration bounds a single stdin write. A wedged pi process that stops
// reading stdin fills the OS pipe buffer, and an unbounded write would block
// holding writeMu — deadlocking every later command (prompts, interrupts)
// behind it. 10s matches the jsonrpc client's bound used by the other
// structured providers.
const maxWriteDuration = 10 * time.Second

type StartOptions struct {
	Cwd             string
	Env             []string
	ExtraArgs       []string
	ResumeSessionID string
	// Executable overrides the pi binary path. Empty means "pi" in $PATH.
	// Used by tests to point at a fake pi RPC server.
	Executable string
	// ProviderName is the provider id used in amux events and errors. Empty
	// defaults to "pi".
	ProviderName  string
	Backend       isolation.LaunchBackend
	LaunchContext isolation.LaunchContext
}

// Proc drives a `pi --mode rpc` subprocess.
type Proc struct {
	handle isolation.ProcessHandle
	args   []string
	stdin  io.WriteCloser
	out    chan []byte

	providerName string

	writeMu sync.Mutex

	eventSinks session.EventSinks

	// runMu guards the per-run state below.
	runMu              sync.Mutex
	runActive          bool
	interruptRequested bool
	// closing marks an intentional shutdown. Close is only invoked by callers
	// that deliberately tear down the agent process (session Kill for
	// stop/restart/reconfigure, structured broker shutdown); a process
	// crashing on its own never reaches Close. When set, readLoop must not
	// report the resulting exit as a mid-run failure — the session side ends
	// the turn itself (Kill -> abortTurn).
	closing atomic.Bool
	// finalText accumulates assistant text deltas and is replaced by the
	// authoritative assistant message text on message_end/turn_end, so the
	// native result reflects the agent's final assistant message.
	finalText string
	// agentSessionID / effectiveModel come from the get_state response and are
	// published as session metadata for restart/resume and effective_model.
	agentSessionID string
	effectiveModel string
}

// rpcCommand is a pi RPC command (the subset amux sends).
type rpcCommand struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

// rpcResponse is a pi RPC response envelope.
type rpcResponse struct {
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

// rpcEventBase is the shared {type} discriminator of streamed events.
type rpcEventBase struct {
	Type string `json:"type"`
}

// agentContentBlock is a content block inside an agent message. pi text
// content uses {type:"text", text:...}; thinking uses {type:"thinking",
// thinking:...}.
type agentContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// agentMessage is the subset of pi's AgentMessage needed for result text.
type agentMessage struct {
	ID         string              `json:"id"`
	Role       string              `json:"role"`
	Content    []agentContentBlock `json:"content"`
	StopReason string              `json:"stopReason"`
}

func extractText(m agentMessage) string {
	var b strings.Builder
	for _, block := range m.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// assistantMessageEvent is the payload of message_update.
type assistantMessageEvent struct {
	Type         string          `json:"type"`
	ContentIndex int             `json:"contentIndex"`
	Delta        string          `json:"delta"`
	Content      string          `json:"content"`
	ToolCall     json.RawMessage `json:"toolCall"`
}

// toolExecutionFrame is the shared shape of tool_execution_start/update/end.
type toolExecutionFrame struct {
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	Args          json.RawMessage `json:"args"`
	PartialResult *toolResultBody `json:"partialResult"`
	Result        *toolResultBody `json:"result"`
	IsError       bool            `json:"isError"`
}

type toolResultBody struct {
	Content []agentContentBlock `json:"content"`
}

func toolResultText(b *toolResultBody) string {
	if b == nil {
		return ""
	}
	var out strings.Builder
	for _, block := range b.Content {
		if block.Type == "text" {
			out.WriteString(block.Text)
		}
	}
	return strings.TrimSpace(out.String())
}

// Start launches `pi --mode rpc` and returns the proc. Session id/model
// metadata is captured asynchronously via get_state so startup never blocks on
// pi's model/extensions initialization. Callers must Close the proc.
func Start(ctx context.Context, opts StartOptions) (*Proc, <-chan []byte, error) {
	providerName := strings.TrimSpace(opts.ProviderName)
	if providerName == "" {
		providerName = "pi"
	}
	bin := strings.TrimSpace(opts.Executable)
	if bin == "" {
		bin = providerName
	}
	bin, err := provider.ResolveExecutableFromEnvPATH(bin, opts.Env)
	if err != nil {
		return nil, nil, err
	}
	args := []string{"--mode", "rpc"}
	if resumeID := strings.TrimSpace(opts.ResumeSessionID); resumeID != "" {
		args = append(args, "--session-id", resumeID)
	}
	args = append(args, opts.ExtraArgs...)
	backend := opts.Backend
	if backend == nil {
		backend = isolation.NewPassthroughBackend()
	}
	handle, err := backend.Start(ctx, isolation.LaunchSpec{
		Path:            bin,
		Args:            append([]string(nil), args...),
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

	p := &Proc{
		handle:       handle,
		args:         append([]string{bin}, args...),
		stdin:        stdin,
		providerName: providerName,
	}
	out := make(chan []byte, 256)
	go func() {
		p.readLoop(stdout)
		close(out)
	}()
	// Fire-and-forget: capture the pi session id and confirmed model. The
	// response handler publishes them as session metadata asynchronously.
	_ = p.send(rpcCommand{Type: "get_state"})
	return p, out, nil
}

// readLoop consumes pi's stdout JSONL until EOF and surfaces a mid-run process
// exit as a visible terminal failure (the amuxlet side also fails the active
// turn on broker exit, so this is belt-and-braces).
func (p *Proc) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var base rpcEventBase
		if err := json.Unmarshal(line, &base); err != nil || base.Type == "" {
			continue
		}
		p.handleFrame(base.Type, line)
	}
	// Skip the failure when Close marked this exit as intentional (session
	// stop/restart/reconfigure, broker shutdown): the session side ends the
	// turn itself via abortTurn, so reporting here would surface a spurious
	// failure for a successful teardown. A process crashing on its own never
	// reaches Close, so genuine mid-run exits still report. Race note: a
	// genuine crash racing a concurrent Close may be swallowed; accepted.
	if p.snapshotRunActive() && !p.closing.Load() {
		p.emitTurnFailure("pi process exited")
	}
}

func (p *Proc) handleFrame(typ string, raw []byte) {
	switch typ {
	case "response":
		p.handleResponse(raw)
	case "agent_start":
		p.noteRunStart()
		// Timeline emitted TurnStarted before dispatch; agent_start only confirms
		// the provider-side run is active.
	case "message_start":
		p.handleMessageStart(raw)
	case "message_update":
		p.handleMessageUpdate(raw)
	case "message_end":
		p.handleMessageEnd(raw)
	case "turn_end":
		p.handleTurnEnd(raw)
	case "tool_execution_start":
		p.handleToolFrame(raw, agentstream.ToolPhaseStarted)
	case "tool_execution_update":
		p.handleToolFrame(raw, agentstream.ToolPhaseUpdated)
	case "tool_execution_end":
		// Completed/Failed is decided by isError in the frame.
		p.handleToolEnd(raw)
	case "agent_settled":
		p.completeRun()
	default:
		// agent_end / turn_start / queue_update / compaction_* / auto_retry_* /
		// extension_error: run progress that is not part of the amux turn
		// lifecycle (agent_settled is the terminal).
	}
}

func (p *Proc) handleResponse(raw []byte) {
	var r rpcResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return
	}
	switch r.Command {
	case "prompt":
		if !r.Success {
			// Preflight rejection: pi never started a run, so no agent_settled
			// will arrive. Release the amux input queue with a visible terminal
			// failure instead of wedging it.
			reason := strings.TrimSpace(r.Error)
			if reason == "" {
				reason = "prompt rejected"
			}
			p.emitTurnFailure(reason)
		}
	case "get_state":
		if !r.Success {
			return
		}
		var st struct {
			SessionID string `json:"sessionId"`
			Model     *struct {
				ID string `json:"id"`
			} `json:"model"`
		}
		if err := json.Unmarshal(r.Data, &st); err != nil {
			return
		}
		sid := strings.TrimSpace(st.SessionID)
		mid := ""
		if st.Model != nil {
			mid = strings.TrimSpace(st.Model.ID)
		}
		if sid == "" && mid == "" {
			return
		}
		p.runMu.Lock()
		if sid != "" {
			p.agentSessionID = sid
		}
		if mid != "" {
			p.effectiveModel = mid
		}
		sid = p.agentSessionID
		mid = p.effectiveModel
		p.runMu.Unlock()
		p.emitSessionMetadata(sid, mid)
	case "abort":
		// Ack for an abort. If a run was active the terminal arrives via
		// agent_settled; if pi was idle there is nothing to finish.
	}
}

func (p *Proc) noteRunStart() {
	p.runMu.Lock()
	p.runActive = true
	p.interruptRequested = false
	p.finalText = ""
	p.runMu.Unlock()
}

func (p *Proc) snapshotRunActive() bool {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	return p.runActive
}

func (p *Proc) handleMessageStart(raw []byte) {
	var n struct {
		Message agentMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &n); err != nil || n.Message.Role != "assistant" {
		return
	}
	// A new assistant message is a text boundary (a tool-use message contributes
	// no text and a new text message starts a fresh segment).
	p.runMu.Lock()
	p.finalText = ""
	p.runMu.Unlock()
	p.emitTurn(agentstream.AssistantMessage{}, agentstream.ActorAgent, "agent", "message_start", n.Message.ID)
}

func (p *Proc) handleMessageUpdate(raw []byte) {
	var n struct {
		AssistantMessageEvent *assistantMessageEvent `json:"assistantMessageEvent"`
	}
	if err := json.Unmarshal(raw, &n); err != nil || n.AssistantMessageEvent == nil {
		return
	}
	ev := n.AssistantMessageEvent
	switch ev.Type {
	case "text_delta":
		delta := ev.Delta
		if delta == "" {
			return
		}
		p.runMu.Lock()
		p.finalText += delta
		p.runMu.Unlock()
		p.emitTurn(agentstream.AssistantDelta{Text: delta}, agentstream.ActorAgent, "agent", "text_delta", "")
	case "thinking_delta":
		delta := ev.Delta
		if delta == "" {
			return
		}
		p.emitTurn(agentstream.ThinkingDelta{Text: delta}, agentstream.ActorAgent, "agent", "thinking_delta", "")
	}
}

// handleMessageEnd makes the completed assistant message the authoritative
// full text for the current segment (streamed deltas are already emitted).
func (p *Proc) handleMessageEnd(raw []byte) {
	var n struct {
		Message agentMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &n); err != nil || n.Message.Role != "assistant" {
		return
	}
	if text := extractText(n.Message); text != "" {
		p.runMu.Lock()
		p.finalText = text
		p.runMu.Unlock()
	}
}

func (p *Proc) handleTurnEnd(raw []byte) {
	var n struct {
		Message agentMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &n); err != nil {
		return
	}
	if n.Message.Role == "assistant" {
		if text := extractText(n.Message); text != "" {
			p.runMu.Lock()
			p.finalText = text
			p.runMu.Unlock()
		}
	}
}

func (p *Proc) handleToolFrame(raw []byte, phase agentstream.ToolPhase) {
	var n toolExecutionFrame
	if err := json.Unmarshal(raw, &n); err != nil || n.ToolCallID == "" {
		return
	}
	tool := agentstream.Tool{
		ID:    n.ToolCallID,
		Name:  n.ToolName,
		Input: n.Args,
	}
	if phase == agentstream.ToolPhaseUpdated {
		tool.OutputPreview = truncateOutput(toolResultText(n.PartialResult))
	}
	p.emitTurn(agentstream.ToolChanged{Phase: phase, Tool: tool}, agentstream.ActorTool, "tool", "tool_execution", n.ToolCallID)
}

func (p *Proc) handleToolEnd(raw []byte) {
	var n toolExecutionFrame
	if err := json.Unmarshal(raw, &n); err != nil || n.ToolCallID == "" {
		return
	}
	phase := agentstream.ToolPhaseCompleted
	if n.IsError {
		phase = agentstream.ToolPhaseFailed
	}
	tool := agentstream.Tool{
		ID:            n.ToolCallID,
		Name:          n.ToolName,
		Input:         n.Args,
		OutputPreview: truncateOutput(toolResultText(n.Result)),
	}
	if n.IsError {
		tool.Error = truncateOutput(toolResultText(n.Result))
	}
	p.emitTurn(agentstream.ToolChanged{Phase: phase, Tool: tool}, agentstream.ActorTool, "tool", "tool_execution_end", n.ToolCallID)
}

// completeRun emits the amux turn terminal for an agent run. Called on
// agent_settled. A cancelled run (user interrupt) emits turn_done(cancelled);
// a normal run emits the native result followed by turn_done.
func (p *Proc) completeRun() {
	p.runMu.Lock()
	if !p.runActive {
		p.runMu.Unlock()
		return
	}
	p.runActive = false
	interrupted := p.interruptRequested
	p.interruptRequested = false
	text := p.finalText
	p.finalText = ""
	p.runMu.Unlock()

	if interrupted {
		p.emitTerminal(agentstream.Terminal{Outcome: agentstream.OutcomeCancelled, Code: "cancelled", Message: "cancelled"}, "agent_settled")
		return
	}
	if strings.TrimSpace(text) != "" {
		p.emitTurn(agentstream.ResultObserved{Text: text}, agentstream.ActorAgent, "result", "result", "")
	}
	p.emitTerminal(agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}, "agent_settled")
}

// emitTurnFailure is the terminal failure exit, the counterpart of
// completeRun. It emits a visible `[system] pi: <reason>` frame and a
// turn_done(failed) state change carrying a typed ProviderError.
func (p *Proc) emitTurnFailure(reason string) {
	p.runMu.Lock()
	p.runActive = false
	p.interruptRequested = false
	p.finalText = ""
	p.runMu.Unlock()

	p.emit(agentstream.ProducerEmission{
		Origin: agentstream.Origin{Actor: agentstream.ActorSystem, Channel: "system", Provider: p.providerName, NativeType: "provider_failure"},
		Turn:   agentstream.Diagnostic{Severity: agentstream.SeverityError, Code: "provider_error", Message: p.providerName + ": " + reason},
	})
	p.emitTerminal(agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: "provider_error", Message: reason}, "provider_failure")
}

// ─── session.Proc implementation ───────────────────────────────────────────

func (p *Proc) PID() int {
	if p.handle != nil {
		return p.handle.PID()
	}
	return 0
}

func (p *Proc) SubmitInput(input session.UserInput) error {
	if input.ImageCount() > 0 {
		return fmt.Errorf("pi RPC does not accept image input")
	}
	text := input.Text()
	return p.sendPrompt(text)
}

func (p *Proc) sendPrompt(text string) error {
	return p.send(rpcCommand{Type: "prompt", Message: text})
}

// Write retains the byte-oriented Proc contract for direct callers. Session
// submissions use SubmitInput so text framing cannot define turn boundaries.
func (p *Proc) Write(data []byte) (int, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return len(data), nil
	}
	if err := p.sendPrompt(text); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (p *Proc) DispatchTurn(ctx context.Context, req session.TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.Control != "" {
		return fmt.Errorf("pi session control %q is unsupported", req.Control)
	}
	if err := p.eventSinks.BeginTurn(sink); err != nil {
		return err
	}
	if err := p.SubmitInput(req.Input); err != nil {
		p.eventSinks.AbortTurn()
		return err
	}
	return nil
}

// Interrupt cancels the running agent via pi's abort command. The terminal
// (turn_done cancelled) arrives through agent_settled.
func (p *Proc) Interrupt() error {
	p.runMu.Lock()
	active := p.runActive
	if active {
		p.interruptRequested = true
	}
	p.runMu.Unlock()
	return p.send(rpcCommand{Type: "abort"})
}

// RespondPermission is not supported: pi's RPC protocol has no permission
// gating, so it is never called by the runtime.
func (p *Proc) RespondPermission(permissionID, optionID string) error {
	return fmt.Errorf("pi: permission responses are not supported (pi has no permission protocol)")
}

func (p *Proc) RespondPermissionMulti(permissionID string, optionIDs []string) error {
	return fmt.Errorf("pi: permission responses are not supported (pi has no permission protocol)")
}

func (p *Proc) SetEventSink(fn func(agentstream.ProducerEmission) bool) {
	p.eventSinks.SetGlobal(fn)
	if fn != nil {
		p.runMu.Lock()
		sid, mid := p.agentSessionID, p.effectiveModel
		p.runMu.Unlock()
		if sid != "" || mid != "" {
			fn(agentstream.ProducerEmission{
				Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.providerName, NativeType: "session_metadata"},
				Session: agentstream.SessionMetadataChanged{AgentSessionID: sid, EffectiveModel: mid},
			})
		}
	}
}

func (p *Proc) Close() error {
	// Mark the shutdown as intentional before touching the process: every
	// Close caller (session Kill for stop/restart/reconfigure, broker
	// shutdown) is a deliberate teardown, so readLoop must not report the
	// resulting exit as a "pi process exited" turn failure. A process
	// crashing on its own never reaches Close, so genuine mid-run failures
	// are still reported.
	p.closing.Store(true)
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.handle != nil {
		_ = p.handle.Signal(context.Background(), "TERM")
		_ = p.handle.Close()
	}
	return nil
}

// send writes one JSONL command to pi's stdin with a bounded write.
func (p *Proc) send(cmd any) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if f, ok := p.stdin.(*os.File); ok {
		_ = f.SetWriteDeadline(time.Now().Add(maxWriteDuration))
		defer func() { _ = f.SetWriteDeadline(time.Time{}) }()
	}
	_, err = p.stdin.Write(data)
	return err
}

func (p *Proc) emit(ev agentstream.ProducerEmission) {
	p.eventSinks.Emit(ev)
}

func (p *Proc) emitTurn(payload agentstream.TurnPayload, actor agentstream.ActorClass, channel, nativeType, nativeID string) {
	p.emit(agentstream.ProducerEmission{
		Origin: agentstream.Origin{Actor: actor, Channel: channel, Provider: p.providerName, NativeType: nativeType, NativeID: nativeID},
		Turn:   payload,
	})
}

func (p *Proc) emitTerminal(terminal agentstream.Terminal, nativeType string) {
	p.emit(agentstream.ProducerEmission{
		Origin:   agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.providerName, NativeType: nativeType},
		Terminal: &terminal,
	})
}

func (p *Proc) emitSessionMetadata(agentSessionID, effectiveModel string) {
	p.emit(agentstream.ProducerEmission{
		Origin:  agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent", Provider: p.providerName, NativeType: "session_metadata"},
		Session: agentstream.SessionMetadataChanged{AgentSessionID: agentSessionID, EffectiveModel: effectiveModel},
	})
}

func truncateOutput(s string) string {
	const maxPreview = 4000
	if len(s) <= maxPreview {
		return s
	}
	return s[:maxPreview] + "...(truncated)"
}
