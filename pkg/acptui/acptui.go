package acptui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// Config configures the TUI behavior.
type Config struct {
	// SessionName is displayed in the status bar.
	SessionName string
	// AgentType is displayed in the status bar.
	AgentType string
	// TargetName is displayed in the attach panel header.
	TargetName string

	// Stdin overrides os.Stdin. If nil, os.Stdin is used.
	Stdin *os.File
	// Stdout overrides os.Stdout. If nil, os.Stdout is used.
	Stdout *os.File

	// OutputFilter is called before displaying output. Return nil to discard.
	// If nil, all output is displayed.
	OutputFilter func(data []byte) []byte

	// ShouldTriggerStatus controls whether a given output event should
	// trigger the "running" status indicator. If nil, all output triggers it.
	ShouldTriggerStatus func(OutputEvent) bool

	// ShouldLockTurn is called on each output event. If it returns true,
	// the TUI locks input (another source triggered the agent). The turn
	// unlocks when a TurnDone event arrives. If nil, output never locks.
	ShouldLockTurn func(OutputEvent) bool

	// Debug prints raw stdin bytes to stderr.
	Debug bool
}

func (c *Config) stdin() *os.File {
	if c.Stdin != nil {
		return c.Stdin
	}
	return os.Stdin
}

func (c *Config) stdout() *os.File {
	if c.Stdout != nil {
		return c.Stdout
	}
	return os.Stdout
}

// Run starts the interactive line-mode TUI. It blocks until the user
// detaches (/detach or Ctrl-]), the context is cancelled, or the stream ends.
func Run(ctx context.Context, stream AgentStream, sender AgentSender, cfg Config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stdin := cfg.stdin()
	stdout := cfg.stdout()
	fd := int(stdin.Fd())

	if !term.IsTerminal(fd) {
		return fmt.Errorf("acptui: stdin is not a terminal")
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("acptui: terminal input mode: %w", err)
	}
	defer term.Restore(fd, oldState)

	sendCh := make(chan []byte, 16)
	done := make(chan struct{})
	var stdoutMu sync.Mutex

	var panel *Panel
	notice := func(text string) {
		if strings.TrimSpace(text) == "" || panel == nil {
			return
		}
		panel.AddTimelineEvent(TimelineEvent{
			Time:   time.Now().Format("15:04"),
			Source: "system",
			Text:   text,
		})
	}
	editor := NewLineEditor(func(data []byte) {
		blocked := panel != nil && panel.Blocked()
		handleSubmittedLine(ctx, data, blocked, sender, cancel, notice, func(data []byte) {
			select {
			case sendCh <- data:
			case <-ctx.Done():
			}
		})
	})

	panel = NewPanel(PanelConfig{
		SessionName: cfg.SessionName,
		AgentType:   cfg.AgentType,
		TargetName:  cfg.TargetName,
	}, editor)
	defer panel.Close()

	if cols, rows, err := term.GetSize(fd); err == nil {
		panel.SetSize(cols, rows)
	}

	renderPanel := func() {
		if cols, rows, err := term.GetSize(fd); err == nil {
			panel.SetSize(cols, rows)
		}
		stdoutMu.Lock()
		stdout.Write(panel.RenderPanel())
		stdoutMu.Unlock()
	}

	// Disable Kitty keyboard protocol.
	stdoutMu.Lock()
	stdout.WriteString(KittyReset)
	stdoutMu.Unlock()

	// Handle terminal resize.
	winchCh := make(chan os.Signal, 1)
	stopResizeNotifications := notifyTerminalResize(winchCh)
	defer stopResizeNotifications()
	go func() {
		for {
			select {
			case <-winchCh:
				if cols, rows, err := term.GetSize(fd); err == nil {
					panel.SetSize(cols, rows)
				}
				renderPanel()
			case <-ctx.Done():
				return
			}
		}
	}()

	renderPanel()

	// Goroutine 1: stream output → panel → stdout.
	go func() {
		defer close(done)
		for {
			ev, err := stream.Recv()
			if err != nil {
				return
			}
			switch ev.Kind {
			case OutputKindPermissionRequest:
				if ev.PermissionRequest != nil {
					panel.SetPermissionRequest(ev.PermissionRequest)
					panel.LockTurn()
					panel.SetStatus("permission")
					stdoutMu.Lock()
					stdout.Write(panel.RenderPanel())
					stdoutMu.Unlock()
				}
			case OutputKindUsage:
				if ev.Usage == nil {
					continue
				}
				panel.SetUsage(ev.Usage)
				stdoutMu.Lock()
				stdout.Write(panel.RenderPanel())
				stdoutMu.Unlock()
			case OutputKindTurnDone:
				panel.UnlockTurn()
				panel.SetStatus("idle")
				if u := panel.CurrentUsage(); u != nil {
					panel.AddTimelineEvent(TimelineEvent{
						Time:   time.Now().Format("15:04"),
						Source: "system",
						Text:   usageTimelineText(u),
					})
				}
				if ev.StopReason == "cancelled" {
					panel.AddTimelineEvent(TimelineEvent{
						Time:   time.Now().Format("15:04"),
						Source: "system",
						Text:   "interrupt cancelled",
					})
				}
				stdoutMu.Lock()
				stdout.Write(panel.RenderPanel())
				stdoutMu.Unlock()
			case OutputKindToolEvent:
				if ev.ToolEvent == nil {
					continue
				}
				panel.AddTimelineEvent(TimelineEvent{
					Time:   time.Now().Format("15:04"),
					Source: "tool",
					Text:   formatStructuredToolEvent(ev.ToolEvent),
				})
				stdoutMu.Lock()
				stdout.Write(panel.RenderPanel())
				stdoutMu.Unlock()
			case OutputKindThinkingDelta:
				if ev.ThinkingDelta == "" {
					continue
				}
				panel.SetStatus("thinking")
				panel.AddTimelineEvent(TimelineEvent{
					Time:   time.Now().Format("15:04"),
					Source: "thinking",
					Text:   strings.TrimSpace(ev.ThinkingDelta),
				})
				stdoutMu.Lock()
				stdout.Write(panel.RenderPanel())
				stdoutMu.Unlock()
			case OutputKindData:
				if len(ev.Data) == 0 {
					continue
				}
				data := ev.Data
				if cfg.OutputFilter != nil {
					data = cfg.OutputFilter(data)
					if data == nil {
						continue
					}
				}
				// Lock turn if another source triggered the agent.
				if cfg.ShouldLockTurn != nil && cfg.ShouldLockTurn(ev) {
					panel.LockTurn()
					panel.SetStatus("waiting")
				}
				shouldTrigger := true
				if cfg.ShouldTriggerStatus != nil {
					shouldTrigger = cfg.ShouldTriggerStatus(ev)
				}
				normalized := NormalizeLineModeOutput(data)
				if cols, rows, err := term.GetSize(fd); err == nil {
					panel.SetSize(cols, rows)
					_ = rows
				}
				stdoutMu.Lock()
				stdout.Write(panel.RenderEvent(ev, normalized, shouldTrigger))
				stdoutMu.Unlock()
			}
		}
	}()

	// Goroutine 2: read stdin, handle detach and input.
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := stdin.Read(buf)
			if err != nil {
				cancel()
				return
			}
			if n == 0 {
				continue
			}
			if cfg.Debug {
				fmt.Fprintf(os.Stderr, "\r\n[stdin %d bytes]: %x\r\n", n, buf[:n])
			}
			if IsDetach(buf[:n]) {
				cancel()
				return
			}
			data := FilterTerminalResponses(buf[:n])
			if len(data) == 0 {
				continue
			}
			if handled := handleInputBytes(data, editor); !handled {
				continue
			}
			renderPanel()
		}
	}()

	// Goroutine 3: send queued input to agent.
	go func() {
		for {
			select {
			case data := <-sendCh:
				panel.SetStatus("waiting")
				panel.LockTurn()
				renderPanel()
				if err := sender.Send(ctx, data); err != nil {
					cancel()
					return
				}
				// Sender.Send may block until the agent finishes (synchronous
				// prompt). Unlock here as a fallback; TurnDone from stream
				// also unlocks (idempotent).
				panel.UnlockTurn()
				panel.SetStatus("idle")
				renderPanel()
			case <-ctx.Done():
				return
			}
		}
	}()

	// Wait for termination.
	sigCh := make(chan os.Signal, 1)
	stopTerminationNotifications := notifyTermination(sigCh)
	defer stopTerminationNotifications()

	select {
	case <-done:
	case <-sigCh:
	case <-ctx.Done():
	}

	// Clean up terminal.
	stdoutMu.Lock()
	stdout.WriteString("\033[?25h\033[0m\r\n")
	stdoutMu.Unlock()

	// Brief wait for output goroutine to finish flushing.
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
	}

	return nil
}

func isDetachCommand(data []byte) bool {
	return strings.TrimSpace(string(data)) == "/detach"
}

func isInterruptCommand(data []byte) bool {
	return strings.TrimSpace(string(data)) == "/interrupt"
}

func isQueueCommand(data []byte) bool {
	parts := strings.Fields(strings.TrimSpace(string(data)))
	return len(parts) > 0 && parts[0] == "/queue"
}

func isCwdCommand(data []byte) bool {
	text := strings.TrimSpace(string(data))
	return text == "/cwd" || strings.HasPrefix(text, "/cwd ")
}

func handleEditorCommand(ctx context.Context, data []byte, sender AgentSender, cancel context.CancelFunc, notice func(string)) bool {
	if handlePermissionCommand(ctx, data, sender, notice) {
		return true
	}
	if isDetachCommand(data) {
		cancel()
		return true
	}
	if isInterruptCommand(data) {
		if interrupter, ok := sender.(Interrupter); ok {
			if err := interrupter.Interrupt(ctx); err != nil {
				if notice != nil {
					notice("interrupt failed: " + err.Error())
				}
				return true
			}
			if notice != nil {
				notice("interrupt requested")
			}
		} else if notice != nil {
			notice("interrupt unsupported")
		}
		return true
	}
	if isQueueCommand(data) {
		handleQueueCommand(ctx, data, sender, notice)
		return true
	}
	if isCwdCommand(data) {
		handleCwdCommand(ctx, data, sender, notice)
		return true
	}
	return false
}

func handleCwdCommand(ctx context.Context, data []byte, sender AgentSender, notice func(string)) {
	manager, ok := sender.(WorkingDirectoryManager)
	if !ok {
		if notice != nil {
			notice("cwd switching unsupported")
		}
		return
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "/cwd"))
	if rest == "" {
		cwd, err := manager.WorkingDirectory(ctx)
		if notice != nil {
			if err != nil {
				notice("cwd query failed: " + err.Error())
			} else {
				notice("cwd: " + cwd)
			}
		}
		return
	}
	opts := WorkingDirectoryOptions{Wait: true}
	for {
		switch {
		case rest == "--interrupt":
			if notice != nil {
				notice("usage: /cwd [--interrupt|--no-wait] <path>")
			}
			return
		case strings.HasPrefix(rest, "--interrupt "):
			opts.Interrupt = true
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "--interrupt"))
		case rest == "--no-wait":
			if notice != nil {
				notice("usage: /cwd [--interrupt|--no-wait] <path>")
			}
			return
		case strings.HasPrefix(rest, "--no-wait "):
			opts.Wait = false
			rest = strings.TrimSpace(strings.TrimPrefix(rest, "--no-wait"))
		default:
			goto parsed
		}
	}

parsed:
	if strings.HasPrefix(rest, "--") {
		if notice != nil {
			notice("usage: /cwd [--interrupt|--no-wait] <path>")
		}
		return
	}
	if opts.Interrupt {
		opts.Wait = true
	}
	if notice != nil {
		notice("switching workspace to " + rest + "...")
	}
	cwd, err := manager.ChangeWorkingDirectory(ctx, rest, opts)
	if notice != nil {
		if err != nil {
			notice("cwd switch failed: " + err.Error())
		} else {
			notice("workspace ready: " + cwd + " (conversation preserved)")
		}
	}
}

func handlePermissionCommand(ctx context.Context, data []byte, sender AgentSender, notice func(string)) bool {
	parts := strings.Fields(strings.TrimSpace(string(data)))
	if len(parts) == 0 {
		return false
	}
	if parts[0] != "/allow" && parts[0] != "/reject" && parts[0] != "/permit" {
		return false
	}
	responder, ok := sender.(PermissionResponder)
	if !ok {
		if notice != nil {
			notice("permission response unsupported")
		}
		return true
	}
	requestID := ""
	optionID := ""
	switch parts[0] {
	case "/allow":
		optionID = "allow"
	case "/reject":
		optionID = "reject"
	case "/permit":
		if len(parts) < 2 {
			if notice != nil {
				notice("usage: /permit <option-id>")
			}
			return true
		}
		optionID = parts[1]
	}
	if len(parts) >= 3 {
		requestID = parts[2]
	}
	if err := responder.RespondToPermission(ctx, requestID, optionID); err != nil {
		if notice != nil {
			notice("permission response failed: " + err.Error())
		}
		return true
	}
	if notice != nil {
		notice("permission response sent: " + optionID)
	}
	return true
}

func handleQueueCommand(ctx context.Context, data []byte, sender AgentSender, notice func(string)) {
	if notice == nil {
		return
	}
	manager, ok := sender.(QueueManager)
	if !ok {
		notice("queue unsupported")
		return
	}
	parts := strings.Fields(strings.TrimSpace(string(data)))
	if len(parts) == 1 {
		items, err := manager.QueueList(ctx)
		if err != nil {
			notice("queue failed: " + err.Error())
			return
		}
		if len(items) == 0 {
			notice("queue empty")
			return
		}
		lines := make([]string, 0, len(items)+1)
		lines = append(lines, fmt.Sprintf("queue: %d pending", len(items)))
		for _, item := range items {
			source := item.Source
			if source == "" {
				source = "-"
			}
			lines = append(lines, fmt.Sprintf("%s [%s] %s", item.ID, source, compactQueueText(item.Text, 80)))
		}
		notice(strings.Join(lines, "\n"))
		return
	}
	if len(parts) == 2 && parts[1] == "clear" {
		cleared, err := manager.QueueClear(ctx)
		if err != nil {
			notice("queue clear failed: " + err.Error())
			return
		}
		notice(fmt.Sprintf("queue cleared: %d", cleared))
		return
	}
	if len(parts) == 3 && parts[1] == "cancel" {
		if err := manager.QueueCancel(ctx, parts[2]); err != nil {
			notice("queue cancel failed: " + err.Error())
			return
		}
		notice("queue cancelled: " + parts[2])
		return
	}
	notice("usage: /queue [clear|cancel <id>]")
}

func compactQueueText(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if max > 0 && len(runes) > max {
		return string(runes[:max-3]) + "..."
	}
	return text
}

func handleSubmittedLine(ctx context.Context, data []byte, blocked bool, sender AgentSender, cancel context.CancelFunc, notice func(string), send func([]byte)) {
	if handleEditorCommand(ctx, data, sender, cancel, notice) {
		return
	}
	if blocked {
		return
	}
	send(data)
}

func handleInputBytes(data []byte, editor *LineEditor) bool {
	echo := editor.Write(data)
	return len(echo) > 0
}
