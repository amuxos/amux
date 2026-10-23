package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/amuxos/amux/internal/provider"
	"github.com/amuxos/amux/internal/session"
)

type Proc struct {
	handle              isolation.ProcessHandle
	stdin               io.WriteCloser
	out                 chan []byte
	input               *promptBuffer
	sessionID           string
	stateMu             sync.Mutex
	inputMu             sync.Mutex
	cwd                 string
	model               string
	reasoningEffort     string
	executionConfig     func(string, string) (map[string]string, error)
	configOptions       []sessionConfigOption
	configTimeout       time.Duration
	commands            []provider.AvailableCommand
	settingUp           atomic.Bool
	stagedCommands      map[string][]provider.AvailableCommand
	done                chan struct{}
	readDone            chan struct{}
	closeOnce           sync.Once
	watchdogMu          sync.Mutex
	watchdog            *turnWatchdog
	watchdogGeneration  uint64
	watchdogPolicy      watchdogPolicy
	watchdogWake        chan struct{}
	watchdogCancelGrace time.Duration
	discardTurnFrames   atomic.Bool
	stderr              *tailBuffer

	imageInput            bool
	inputCapabilitySource string

	waitMu  sync.Mutex
	waitErr error

	writeMu               sync.Mutex
	promptMu              sync.Mutex
	activePromptID        int64
	activePromptCancelled bool
	nextID                atomic.Int64
	// restoring is true while session/load or session/resume hydrates an
	// existing ACP session. Frames emitted before the restore response are
	// historical UI state, not live output for amux attach/bridge consumers.
	restoring atomic.Bool

	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage

	toolMu     sync.Mutex
	toolTitles map[string]string

	permMu              sync.Mutex
	permissions         map[string]int64
	permissionCancelled bool

	eventSinks session.EventSinks

	// turnMu guards the per-turn accumulation of the agent's final message,
	// used to emit canonical ResultObserved when the turn completes.
	// turnFinal is the authoritative full final message (message /
	// agent_end event); turnDeltas accumulates agent_message_chunk text when no
	// authoritative final message is sent.
	turnMu     sync.Mutex
	turnDeltas strings.Builder
	turnFinal  string
	turnUsage  json.RawMessage

	promptTailDrain time.Duration
	turnActivity    atomic.Uint64
	turnActivityCh  chan struct{}
}

type rpcMessage struct {
	CancellationRequested bool            `json:"-"`
	JSONRPC               string          `json:"jsonrpc,omitempty"`
	ID                    int64           `json:"id,omitempty"`
	Method                string          `json:"method,omitempty"`
	Params                json.RawMessage `json:"params,omitempty"`
	Result                json.RawMessage `json:"result,omitempty"`
	Error                 json.RawMessage `json:"error,omitempty"`
}

type StartOptions struct {
	Cwd             string
	Model           string
	ReasoningEffort string
	ExecutionConfig func(model, reasoningEffort string) (map[string]string, error)
	ResumeSessionID string
	PromptTailDrain time.Duration
	Watchdog        *config.StructuredProviderConfig
	// Test overrides; production uses seconds and a two-second cancel grace.
	WatchdogTimeoutUnit time.Duration
	WatchdogCancelGrace time.Duration
	Backend             isolation.LaunchBackend
	LaunchContext       isolation.LaunchContext
}

type tailBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
		b.truncated = true
	}
	return n, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := strings.TrimSpace(string(b.data))
	if b.truncated {
		return "[stderr truncated]\n" + text
	}
	return text
}

func StartWithOptions(ctx context.Context, cmd *exec.Cmd, opts StartOptions) (*Proc, <-chan []byte, error) {
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	backend := opts.Backend
	if backend == nil {
		backend = isolation.NewPassthroughBackend()
	}
	handle, err := backend.Start(ctx, isolation.LaunchSpec{
		Path:            cmd.Path,
		Args:            append([]string(nil), cmd.Args[1:]...),
		Env:             append([]string(nil), cmd.Env...),
		Dir:             cmd.Dir,
		SetProcessGroup: true,
		Context:         opts.LaunchContext,
	})
	if err != nil {
		return nil, nil, err
	}
	stdout, stdin, stderr := handle.Stdout(), handle.Stdin(), handle.Stderr()

	stderrCapture := &tailBuffer{limit: 64 * 1024}
	p := &Proc{
		handle:              handle,
		stdin:               stdin,
		out:                 make(chan []byte, 256),
		done:                make(chan struct{}),
		readDone:            make(chan struct{}),
		watchdogPolicy:      newWatchdogPolicy(opts.Watchdog, opts.WatchdogTimeoutUnit),
		watchdogWake:        make(chan struct{}, 1),
		watchdogCancelGrace: opts.WatchdogCancelGrace,
		stderr:              stderrCapture,
		pending:             make(map[int64]chan rpcMessage),
		toolTitles:          make(map[string]string),
		permissions:         make(map[string]int64),
		promptTailDrain:     opts.PromptTailDrain,
		turnActivityCh:      make(chan struct{}, 1),
		cwd:                 opts.Cwd,
		executionConfig:     opts.ExecutionConfig,
	}
	p.input = newPromptBuffer(p.prompt)

	go p.readLoop(stdout)
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderrCapture, stderr)
		close(stderrDone)
	}()
	go func() {
		_, err := handle.Wait(context.Background())
		<-stderrDone
		p.waitMu.Lock()
		p.waitErr = err
		p.waitMu.Unlock()
		close(p.done)
	}()

	if err := p.initialize(ctx, opts); err != nil {
		_ = p.Close()
		return nil, nil, err
	}
	return p, p.out, nil
}

func (p *Proc) initialize(ctx context.Context, opts StartOptions) error {
	initResp, err := p.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]bool{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
		"clientInfo": map[string]string{
			"name":    "amux",
			"version": "0",
		},
	})
	if err != nil {
		return err
	}

	var initResult initializeResult
	if err := json.Unmarshal(initResp.Result, &initResult); err != nil {
		if opts.ResumeSessionID != "" {
			return fmt.Errorf("decode initialize result: %w", err)
		}
		// Invalid optional metadata must not enable capabilities decoded before
		// the malformed field. Preserve legacy text-only new-session startup.
		initResult = initializeResult{}
		p.inputCapabilitySource = "acp:default"
	} else {
		p.inputCapabilitySource = "acp:initialize"
	}
	p.imageInput = initResult.AgentCapabilities.PromptCapabilities.Image

	if opts.ResumeSessionID != "" {
		if initResult.supportsResume() {
			if err := p.restoreSession(ctx, "session/resume", opts); err != nil {
				return err
			}
			return nil
		}
		if initResult.AgentCapabilities.LoadSession {
			if err := p.restoreSession(ctx, "session/load", opts); err != nil {
				return err
			}
			return nil
		}
	}

	return p.createSession(ctx, opts.Model, opts.ReasoningEffort)
}

type initializeResult struct {
	AgentCapabilities struct {
		LoadSession        bool `json:"loadSession"`
		PromptCapabilities struct {
			Image bool `json:"image"`
		} `json:"promptCapabilities"`
		SessionCapabilities struct {
			Resume *json.RawMessage `json:"resume"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
}

func (r initializeResult) supportsResume() bool {
	return r.AgentCapabilities.SessionCapabilities.Resume != nil
}

func sessionSetupParams(cwd, sessionID string) map[string]any {
	params := map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	}
	if sessionID != "" {
		params["sessionId"] = sessionID
	}
	return params
}

func (p *Proc) restoreSession(ctx context.Context, method string, opts StartOptions) error {
	p.restoring.Store(true)
	defer p.restoring.Store(false)
	p.beginSessionSetup()
	defer p.endSessionSetup()
	resp, err := p.call(ctx, method, sessionSetupParams(opts.Cwd, opts.ResumeSessionID))
	if err != nil {
		return err
	}
	result, err := decodeSessionSetup(resp.Result, opts.ResumeSessionID)
	if err != nil {
		return err
	}
	options, err := p.configureSession(ctx, result.SessionID, result.ConfigOptions, opts.Model, opts.ReasoningEffort)
	if err != nil {
		return err
	}
	p.commitSession(result.SessionID, opts.Model, opts.ReasoningEffort, options)
	return nil
}

func (p *Proc) emitAgentSessionID() {
	p.stateMu.Lock()
	id, model := p.sessionID, p.model
	p.stateMu.Unlock()
	p.emitEmission(agentstream.ProducerEmission{
		Origin:  p.origin(agentstream.ActorAgent, "agent", "session_metadata"),
		Session: agentstream.SessionMetadataChanged{AgentSessionID: id, EffectiveModel: model},
	})
}

// emitThinking maps an ACP agent_thought_chunk to canonical reasoning. Thought
// text is not written to the output stream: it reaches attach (line-mode
// thinking) and the Feishu bridge through the structured event, not the agent
// transcript, and is not accumulated into the native turn result.
func (p *Proc) emitThinking(text string) {
	p.emitTurn(agentstream.ThinkingDelta{Text: text}, agentstream.ActorAgent, "agent", "agent_thought_chunk")
}

// resetTurnResult clears the per-turn final-message accumulation at the start of
// a prompt turn.
func (p *Proc) resetTurnResult() {
	p.turnMu.Lock()
	p.turnDeltas.Reset()
	p.turnFinal = ""
	p.turnUsage = nil
	p.turnMu.Unlock()
}

func (p *Proc) appendTurnDelta(text string) {
	p.turnMu.Lock()
	p.turnDeltas.WriteString(text)
	p.turnMu.Unlock()
	p.noteTurnActivity()
}

func (p *Proc) hasTurnDeltas() bool {
	p.turnMu.Lock()
	defer p.turnMu.Unlock()
	return p.turnDeltas.Len() > 0
}

func (p *Proc) setTurnFinal(text string) {
	p.turnMu.Lock()
	p.turnFinal = text
	p.turnMu.Unlock()
	p.noteTurnActivity()
}

func (p *Proc) noteTurnActivity() {
	p.turnActivity.Add(1)
	if p.turnActivityCh == nil {
		return
	}
	select {
	case p.turnActivityCh <- struct{}{}:
	default:
	}
}

func (p *Proc) waitPromptTail() {
	quietWindow := p.promptTailDrain
	if quietWindow <= 0 {
		return
	}
	maxWait := quietWindow * 5
	if maxWait < quietWindow {
		maxWait = quietWindow
	}

	quietTimer := time.NewTimer(quietWindow)
	defer quietTimer.Stop()
	maxTimer := time.NewTimer(maxWait)
	defer maxTimer.Stop()
	observed := p.turnActivity.Load()

	for {
		select {
		case <-p.turnActivityCh:
			current := p.turnActivity.Load()
			if current == observed {
				continue
			}
			observed = current
			resetTimer(quietTimer, quietWindow)
		case <-quietTimer.C:
			current := p.turnActivity.Load()
			if current == observed {
				return
			}
			observed = current
			quietTimer.Reset(quietWindow)
		case <-maxTimer.C:
			return
		case <-p.done:
			return
		}
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

// turnResultText returns the agent's final message for this turn, preferring an
// authoritative full message (message / agent_end) over accumulated
// agent_message_chunk deltas.
func (p *Proc) turnResultText() string {
	p.turnMu.Lock()
	defer p.turnMu.Unlock()
	if strings.TrimSpace(p.turnFinal) != "" {
		return p.turnFinal
	}
	return p.turnDeltas.String()
}

func (p *Proc) readLoop(r io.Reader) {
	defer close(p.out)
	defer func() {
		if p.readDone != nil {
			close(p.readDone)
		}
	}()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lastAssistantText string
	var lastAssistantKind assistantEventKind
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)

		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err == nil {
			if msg.ID != 0 && msg.Method == "" {
				msg.CancellationRequested = p.finishPromptRPC(msg.ID)
				p.pendingMu.Lock()
				ch := p.pending[msg.ID]
				delete(p.pending, msg.ID)
				p.pendingMu.Unlock()
				if ch != nil {
					ch <- msg
					close(ch)
					continue
				}
			}
			if msg.ID != 0 && msg.Method != "" {
				if !p.acceptsSessionFrame(line) {
					_ = p.writeErrorResponse(msg.ID, -32602, "request belongs to a different Acp session")
					continue
				}
				if p.handleRequest(msg) {
					continue
				}
				_ = p.writeErrorResponse(msg.ID, -32601, fmt.Sprintf("unsupported Acp request %q", msg.Method))
				continue
			}
		}
		if p.discardTurnFrames.Load() {
			continue
		}
		if p.settingUp.Load() {
			if upd, ok := semanticUpdateFromLine(line); ok && upd.HasCommands {
				p.stageCommands(line, upd.AvailableCommands)
			}
			continue
		}
		if !p.acceptsSessionFrame(line) {
			continue
		}
		if p.captureConfigUpdate(line) {
			continue
		}
		if p.restoring.Load() {
			// ACP agents may hydrate a newly attached client by replaying
			// session/update notifications before the load/resume response.
			// Keep the current available-command snapshot because it is session
			// state, not replayed turn output. Drop every other hydrated frame.
			if upd, ok := semanticUpdateFromLine(line); ok && upd.HasCommands {
				p.emitCommands(upd.AvailableCommands)
			}
			continue
		}

		if text, kind, ok := contentChunkFromUpdate(line); ok {
			if kind == chunkKindThought {
				p.emitThinking(text)
				continue
			}
			debugACPAgentChunk(text)
			p.appendTurnDelta(text)
			p.emitTurn(agentstream.AssistantDelta{Text: text}, agentstream.ActorAgent, "agent", "agent_message_chunk")
			select {
			case p.out <- []byte(text):
			default:
			}
			continue
		}
		if text, kind, ok := assistantTextFromEvent(line); ok {
			// Capture the authoritative final message for the native result
			// even when the duplicate agent_end output is suppressed below.
			p.setTurnFinal(text)
			if p.hasTurnDeltas() {
				lastAssistantText = text
				lastAssistantKind = kind
				continue
			}
			if kind == assistantEventKindAgentEnd && lastAssistantKind == assistantEventKindMessage && lastAssistantText == text {
				lastAssistantKind = kind
				continue
			}
			debugACPAgentChunk(text)
			p.emitTurn(agentstream.AssistantMessage{Text: text}, agentstream.ActorAgent, "agent", string(kind))
			select {
			case p.out <- []byte(text):
			default:
			}
			lastAssistantText = text
			lastAssistantKind = kind
			continue
		}

		if upd, ok := toolUpdateFromLine(line); ok {
			p.emitToolUpdate(upd)
			continue
		}
		if upd, ok := semanticUpdateFromLine(line); ok {
			switch {
			case upd.HasCommands:
				p.emitCommands(upd.AvailableCommands)
			case upd.Plan != nil:
				entries := make([]agentstream.PlanEntry, 0, len(upd.Plan.Entries))
				for _, entry := range upd.Plan.Entries {
					entries = append(entries, agentstream.PlanEntry{Content: entry.Content, Priority: entry.Priority, Status: entry.Status})
				}
				p.emitTurn(agentstream.PlanReplaced{Entries: entries}, agentstream.ActorAgent, "agent", "plan")
			case len(upd.Usage) > 0:
				p.setTurnUsage(upd.Usage)
				p.emitTurn(agentstream.UsageObserved{Usage: upd.Usage}, agentstream.ActorAgent, "agent", "usage")
			}
			continue
		}
		// Unknown ACP updates are intentionally ignored. Opaque provider data is
		// never allowed to become lifecycle authority by accident.
	}
}

// acceptsSessionFrame keeps one ACP Proc scoped to the session it created or
// resumed. Missing session IDs remain accepted for agents that emit legacy or
// additive frames without an explicit scope; only an explicit mismatch is
// rejected.
func (p *Proc) acceptsSessionFrame(line []byte) bool {
	var envelope struct {
		SessionID string `json:"session_id"`
		Params    struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return true
	}
	sessionID := envelope.Params.SessionID
	if sessionID == "" {
		sessionID = envelope.SessionID
	}
	currentID := p.SessionID()
	return sessionID == "" || currentID == "" || sessionID == currentID
}

func debugACPAgentChunk(text string) {
	if os.Getenv("AMUX_DEBUG_ACP_CHUNKS") != "1" {
		return
	}
	fmt.Fprintf(os.Stderr, "amux acp agent chunk: %q\n", text)
}

func (p *Proc) handleRequest(msg rpcMessage) bool {
	if msg.Method != "session/request_permission" {
		return false
	}
	permID := fmt.Sprintf("%s:%d", p.SessionID(), msg.ID)
	p.permMu.Lock()
	if p.permissionCancelled || p.discardTurnFrames.Load() {
		p.permMu.Unlock()
		_ = p.writeResponse(msg.ID, map[string]any{"outcome": map[string]string{"outcome": "cancelled"}})
		return true
	}
	p.permissions[permID] = msg.ID
	p.observeWatchdogPermission(permID, true)
	p.permMu.Unlock()
	p.emitTurn(acpPermissionRequested(permID, msg.Params), agentstream.ActorAgent, "agent", "session/request_permission")
	return true
}

func (p *Proc) RespondPermission(permissionID, optionID string) error {
	if permissionID == "" {
		return fmt.Errorf("permission id is required")
	}
	if optionID == "" {
		return fmt.Errorf("permission option is required")
	}
	p.permMu.Lock()
	id, ok := p.permissions[permissionID]
	if ok {
		delete(p.permissions, permissionID)
		p.observeWatchdogPermission(permissionID, false)
	}
	p.permMu.Unlock()
	if !ok {
		return fmt.Errorf("permission request %q not found", permissionID)
	}
	return p.writeResponse(id, map[string]any{
		"outcome": map[string]string{
			"outcome":  "selected",
			"optionId": optionID,
		},
	})
}

// RespondPermissionMulti is not supported on the ACP protocol; the multi-select
// user-input path only applies to Codex-compatible app-server (item/tool/requestUserInput).
func (p *Proc) RespondPermissionMulti(permissionID string, optionIDs []string) error {
	return fmt.Errorf("multi-select permission response is not supported for ACP provider")
}

// emitToolUpdate maps an ACP tool_call / tool_call_update directly to canonical
// ToolChanged. ACP previously text-ized these as
// "🔧 工具开始:" lines into the output stream; the structured event lets the
// bridge (Feishu activity card) and CLI attach (line-mode tool timeline)
// render tool detail uniformly with the Codex/Claude structured path.
// The legacy text path is retired; acptui's text-prefix matching remains only
// as a fallback for replaying old sessions whose buffer still holds the text.
func (p *Proc) emitToolUpdate(upd toolUpdate) {
	p.observeWatchdogTool(upd)
	title := enrichToolTitle(upd.Title, upd.RawInput)
	p.toolMu.Lock()
	if title != "" {
		p.toolTitles[upd.ID] = title
	} else {
		title = p.toolTitles[upd.ID]
	}
	p.toolMu.Unlock()

	if title == "" {
		title = upd.Kind
	}
	if title == "" {
		title = upd.ID
	}

	name := upd.Kind
	if name == "" {
		name = "tool"
	}

	tool := agentstream.Tool{
		ID:            upd.ID,
		Name:          name,
		Title:         title,
		Input:         upd.RawInput,
		Content:       upd.Content,
		Locations:     upd.Locations,
		RawOutput:     upd.RawOutput,
		OutputPreview: upd.OutputPreview,
	}

	switch upd.Update {
	case "tool_call":
		p.emitTurn(agentstream.ToolChanged{Phase: agentstream.ToolPhaseStarted, Tool: tool}, agentstream.ActorTool, "tool", "tool_call")
	case "tool_call_update":
		switch upd.Status {
		case "completed", "success", "succeeded", "ok":
			p.emitTurn(agentstream.ToolChanged{Phase: agentstream.ToolPhaseCompleted, Tool: tool}, agentstream.ActorTool, "tool", "tool_call_update")
		case "failed", "error":
			p.emitTurn(agentstream.ToolChanged{Phase: agentstream.ToolPhaseFailed, Tool: tool}, agentstream.ActorTool, "tool", "tool_call_update")
		default:
			p.emitTurn(agentstream.ToolChanged{Phase: agentstream.ToolPhaseUpdated, Tool: tool}, agentstream.ActorTool, "tool", "tool_call_update")
		}
	}
}

func (p *Proc) call(ctx context.Context, method string, params any) (rpcMessage, error) {
	id := p.nextID.Add(1)
	ch := make(chan rpcMessage, 1)

	p.pendingMu.Lock()
	p.pending[id] = ch
	p.pendingMu.Unlock()

	if err := p.writeRequest(id, method, params); err != nil {
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
		return rpcMessage{}, err
	}

	select {
	case msg := <-ch:
		return callResult(method, msg)
	case <-p.done:
		// Prefer a response that raced with a clean process exit.
		select {
		case msg := <-ch:
			return callResult(method, msg)
		default:
		}
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
		return rpcMessage{}, p.processExitError(method)
	case <-ctx.Done():
		p.pendingMu.Lock()
		delete(p.pending, id)
		p.pendingMu.Unlock()
		return rpcMessage{}, ctx.Err()
	}
}

func callResult(method string, msg rpcMessage) (rpcMessage, error) {
	if len(msg.Error) > 0 {
		return msg, fmt.Errorf("acp %s: %s", method, string(msg.Error))
	}
	return msg, nil
}

func (p *Proc) processExitError(method string) error {
	p.waitMu.Lock()
	waitErr := p.waitErr
	p.waitMu.Unlock()
	stderr := ""
	if p.stderr != nil {
		stderr = p.stderr.String()
	}
	if stderr != "" && waitErr != nil {
		return fmt.Errorf("acp process exited during %s: %v; stderr: %s", method, waitErr, stderr)
	}
	if stderr != "" {
		return fmt.Errorf("acp process exited during %s; stderr: %s", method, stderr)
	}
	if waitErr != nil {
		return fmt.Errorf("acp process exited during %s: %v", method, waitErr)
	}
	return fmt.Errorf("acp process exited during %s", method)
}

func (p *Proc) writeRequest(id int64, method string, params any) error {
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if method == "session/prompt" {
		p.startPromptRPC(id)
	}
	_, err = p.stdin.Write(data)
	if err != nil && method == "session/prompt" {
		p.finishPromptRPC(id)
	}
	return err
}

func (p *Proc) writeNotification(method string, params any) error {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	return p.writeJSON(req)
}

func (p *Proc) writeResponse(id int64, result any) error {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	return p.writeJSON(resp)
}

func (p *Proc) writeErrorResponse(id int64, code int, message string) error {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	return p.writeJSON(resp)
}

func (p *Proc) writeJSON(v any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.writeJSONLocked(v)
}

func (p *Proc) writeJSONLocked(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = p.stdin.Write(data)
	return err
}

func (p *Proc) startPromptRPC(id int64) {
	p.promptMu.Lock()
	p.activePromptID = id
	p.activePromptCancelled = false
	p.promptMu.Unlock()
}

func (p *Proc) finishPromptRPC(id int64) bool {
	p.promptMu.Lock()
	defer p.promptMu.Unlock()
	if p.activePromptID != id {
		return false
	}
	cancelled := p.activePromptCancelled
	p.activePromptID = 0
	p.activePromptCancelled = false
	return cancelled
}

type promptResult struct {
	StopReason string          `json:"stopReason"`
	Usage      json.RawMessage `json:"usage"`
}

func (p *Proc) prompt(text string) error {
	if !p.inputMu.TryLock() {
		return session.ErrTurnSinkActive
	}
	defer p.inputMu.Unlock()
	content, err := promptContent(session.TextUserInput(text), p.imageInput)
	if err != nil {
		return err
	}
	return p.promptContent(content)
}

func (p *Proc) promptContent(content []map[string]string) error {
	p.resetTurnResult()
	defer p.resetTurnResult()
	p.permMu.Lock()
	p.permissionCancelled = false
	p.permMu.Unlock()
	resp, err := p.callPrompt(content)
	if err == nil {
		p.waitPromptTail()
	}
	stopReason := ""
	var promptUsage json.RawMessage
	if err == nil && len(resp.Result) > 0 {
		var result promptResult
		if json.Unmarshal(resp.Result, &result) == nil {
			stopReason = result.StopReason
			promptUsage = normalizedPromptUsage(result.Usage)
		}
	}
	if err != nil {
		code := "acp_prompt_failed"
		var watchdogErr *watchdogFailure
		if errors.As(err, &watchdogErr) {
			code = watchdogErr.code
		}
		p.emitTerminal(agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: code, Message: err.Error()}, "session/prompt")
		return err
	}
	usage := p.turnResultUsage(promptUsage)
	if len(promptUsage) > 0 {
		p.emitTurn(agentstream.UsageObserved{Usage: usage}, agentstream.ActorAgent, "agent", "prompt_usage")
	}
	// Emit the provider's native turn result (the agent's own final message)
	// before the terminal turn_done, mirroring the codex/claude structured
	// path so the task control plane records a summary without injecting a
	// fallback envelope. Skipped when the turn produced no final text.
	if result := p.turnResultText(); strings.TrimSpace(result) != "" {
		p.emitTurn(agentstream.ResultObserved{Text: result, Usage: usage}, agentstream.ActorAgent, "result", "prompt_result")
	}
	terminal := agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed", Message: stopReason}
	if stopReason == "cancelled" || resp.CancellationRequested {
		terminal = agentstream.Terminal{Outcome: agentstream.OutcomeCancelled, Code: "cancelled", Message: "cancelled"}
	}
	p.emitTerminal(terminal, "prompt_result")
	return nil
}

func (p *Proc) PID() int {
	if p.handle == nil {
		return 0
	}
	return p.handle.PID()
}

func (p *Proc) SessionID() string {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.sessionID
}

func (p *Proc) EffectiveModel() string {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return p.model
}

func (p *Proc) Write(data []byte) (int, error) {
	return p.input.Write(data)
}

func (p *Proc) DispatchTurn(ctx context.Context, req session.TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.inputMu.TryLock() {
		return session.ErrTurnSinkActive
	}
	defer p.inputMu.Unlock()
	var content []map[string]string
	var err error
	switch req.Control {
	case "":
		content, err = promptContent(req.Input, p.imageInput)
	case "clear":
	case "compact":
		if !p.hasCommand("compact") {
			return acpError("control.compact_unavailable")
		}
		content = []map[string]string{{"type": "text", "text": "/compact"}}
	default:
		return acpError("control.unsupported", req.Control)
	}
	if err != nil {
		return err
	}
	if err := p.eventSinks.BeginTurn(sink); err != nil {
		return err
	}
	if req.Control == "clear" {
		p.stateMu.Lock()
		model, effort := p.model, p.reasoningEffort
		p.stateMu.Unlock()
		if err := p.createSession(ctx, model, effort); err != nil {
			p.emitTerminal(agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: "acp_clear_failed", Message: err.Error()}, "session/new")
		} else {
			p.emitTerminal(agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}, "session/new")
		}
		return nil
	}
	// Native prompt failures already emit their one correlated terminal.
	_ = p.promptContent(content)
	return nil
}

func (p *Proc) Interrupt() error {
	id := p.SessionID()
	if id == "" {
		return fmt.Errorf("acp session is not initialized")
	}
	// Serialize the cancellation writes with native prompt submission. A new
	// prompt cannot overtake an interrupt intended for the preceding request.
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.promptMu.Lock()
	defer p.promptMu.Unlock()
	p.permMu.Lock()
	p.permissionCancelled = true
	pending := p.permissions
	p.permissions = make(map[string]int64)
	p.permMu.Unlock()
	var firstErr error
	for permissionID, requestID := range pending {
		p.observeWatchdogPermission(permissionID, false)
		if err := p.writeJSONLocked(map[string]any{"jsonrpc": "2.0", "id": requestID, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	cancelErr := p.writeJSONLocked(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": id}})
	if cancelErr == nil && p.activePromptID != 0 {
		p.activePromptCancelled = true
	}
	if cancelErr != nil && firstErr == nil {
		firstErr = cancelErr
	}
	return firstErr
}

func (p *Proc) SetEventSink(fn func(agentstream.ProducerEmission) bool) {
	p.eventSinks.SetGlobal(fn)
}

func (p *Proc) emitEmission(emission agentstream.ProducerEmission) {
	p.eventSinks.Emit(emission)
}

func (p *Proc) emitTurn(payload agentstream.TurnPayload, actor agentstream.ActorClass, channel, nativeType string) {
	p.observeWatchdogActivity()
	p.emitEmission(agentstream.ProducerEmission{Origin: p.origin(actor, channel, nativeType), Turn: payload})
}

func (p *Proc) emitTerminal(terminal agentstream.Terminal, nativeType string) {
	p.emitEmission(agentstream.ProducerEmission{Origin: p.origin(agentstream.ActorAgent, "agent", nativeType), Terminal: &terminal})
}

func (p *Proc) emitCommands(commands []provider.AvailableCommand) {
	p.stateMu.Lock()
	p.commands = append([]provider.AvailableCommand(nil), commands...)
	p.stateMu.Unlock()
	canonical := make([]agentstream.Command, 0, len(commands))
	for _, command := range commands {
		canonical = append(canonical, agentstream.Command{Name: command.Name, Description: command.Description, InputHint: command.InputHint, Kind: command.Kind})
	}
	p.emitEmission(agentstream.ProducerEmission{
		Origin:  p.origin(agentstream.ActorAgent, "agent", "available_commands"),
		Session: agentstream.CommandsReplaced{Commands: canonical},
	})
}

func (p *Proc) origin(actor agentstream.ActorClass, channel, nativeType string) agentstream.Origin {
	return agentstream.Origin{Actor: actor, Channel: channel, Provider: "acp", NativeType: nativeType}
}

func acpPermissionRequested(id string, raw json.RawMessage) agentstream.PermissionRequested {
	request := agentstream.PermissionRequest{ID: id, Provider: "acp", Details: append(json.RawMessage(nil), raw...)}
	var payload struct {
		Options []struct {
			ID       string `json:"id"`
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(raw, &payload) == nil {
		for _, option := range payload.Options {
			optionID := option.OptionID
			if optionID == "" {
				optionID = option.ID
			}
			request.Options = append(request.Options, agentstream.PermissionOption{ID: optionID, Name: option.Name, Kind: option.Kind})
		}
	}
	return agentstream.PermissionRequested{Request: request}
}

func (p *Proc) Close() error {
	p.closeOnce.Do(func() {
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
		if p.handle != nil {
			_ = p.handle.Kill(context.Background())
			// Descendants can retain stdout/stderr after the agent exits. Close
			// our descriptors before waiting for their readers to finish.
			_ = p.handle.Close()
		}
		if p.done != nil {
			<-p.done
		}
		if p.readDone != nil {
			<-p.readDone
		}
	})
	return nil
}
