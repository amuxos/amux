package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/sourceframe"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/pkg/acptui"
	"github.com/spf13/cobra"
)

func sessionEntryForAttach(sessions []*sessionEntry, targetID, name string) (*sessionEntry, bool) {
	if target, sessionName, ok := splitTargetSession(name); ok {
		for _, sess := range sessions {
			if sess == nil || (sess.Name != sessionName && sess.Id != sessionName) {
				continue
			}
			if sess.TargetName != target && sess.TargetID != target {
				continue
			}
			return sess, true
		}
		return nil, false
	}
	for _, sess := range sessions {
		if sess == nil || (sess.Name != name && sess.Id != name) {
			continue
		}
		if targetID != "" && sess.TargetID != targetID {
			continue
		}
		return sess, true
	}
	return nil, false
}

func isAttachSourceFrame(data []byte) bool {
	return sourceframe.Classify(data).Input
}

// ── amux adapters for acptui ─────────────────────────────────────────────────

// amuxAgentStream adapts a gRPC AttachClient stream to acptui.AgentStream.
type attachPermissionState struct {
	mu      sync.Mutex
	id      string
	options []string
}

type amuxAgentStream struct {
	stream pb.Amuxlet_AttachClient
	perm   *attachPermissionState
}

func (s *amuxAgentStream) Recv() (acptui.OutputEvent, error) {
	for {
		ev, err := s.stream.Recv()
		if err != nil {
			return acptui.OutputEvent{}, err
		}
		if ev.Kind == pb.AttachEvent_STATE_CHANGE && ev.State == "turn_done" {
			return acptui.OutputEvent{Kind: acptui.OutputKindTurnDone, StopReason: ev.StopReason}, nil
		}
		if ev.Kind == pb.AttachEvent_PERMISSION_REQUEST {
			req := acptuiPermissionRequest(ev.PermissionId, ev.PermissionPayload)
			if s.perm != nil {
				s.perm.mu.Lock()
				s.perm.id = ev.PermissionId
				s.perm.options = s.perm.options[:0]
				for _, opt := range req.Options {
					s.perm.options = append(s.perm.options, opt.OptionId)
				}
				s.perm.mu.Unlock()
			}
			return acptui.OutputEvent{Kind: acptui.OutputKindPermissionRequest, PermissionRequest: req}, nil
		}
		// Structured provider events (tool started/completed/failed, thinking
		// delta) arrive as OUTPUT frames with a non-empty EventType and empty
		// Data. Before v0.1.32 the `len(ev.Data) == 0` guard below discarded
		// them, so app-server tool/thinking detail was invisible in CLI attach.
		// Dispatch them to the structured acptui kinds before the data guard.
		if ev.Kind == pb.AttachEvent_OUTPUT && ev.EventType != "" {
			if te := attachToolEvent(ev); te != nil {
				return acptui.OutputEvent{Kind: acptui.OutputKindToolEvent, ToolEvent: te}, nil
			}
			if ev.EventType == "thinking_delta" {
				return acptui.OutputEvent{Kind: acptui.OutputKindThinkingDelta, ThinkingDelta: ev.Text}, nil
			}
			if ev.EventType == "usage" {
				if u := attachUsageStats(ev); u != nil {
					return acptui.OutputEvent{Kind: acptui.OutputKindUsage, Usage: u}, nil
				}
				continue
			}
			// Other structured event types (result, turn_started) have no
			// line-mode rendering; fall through to the data guard which
			// drops them when Data is empty.
		}
		// A top-level provider error (event_type="error" or AttachEvent.ERROR)
		// carries the message in ErrorMessage with empty Data. Render it as a
		// line-mode data frame so the user sees the error instead of a silent
		// hang (v0.1.38).
		if (ev.Kind == pb.AttachEvent_ERROR || (ev.Kind == pb.AttachEvent_OUTPUT && ev.EventType == "error")) && ev.ErrorMessage != "" {
			line := "\r\n[error] " + ev.ErrorMessage + "\r\n"
			return acptui.OutputEvent{Kind: acptui.OutputKindData, Data: []byte(line)}, nil
		}
		if ev.Kind != pb.AttachEvent_OUTPUT || len(ev.Data) == 0 {
			continue
		}
		data, source, input := attachOutputEventFields(ev)
		return acptui.OutputEvent{Kind: acptui.OutputKindData, Data: data, Source: source, Input: input}, nil
	}
}

// attachToolEvent converts a structured tool AttachEvent (tool_started /
// tool_updated / tool_completed / tool_failed) into an acptui.ToolEvent for line-mode
// rendering. Returns nil for non-tool event types.
func attachToolEvent(ev *pb.AttachEvent) *acptui.ToolEvent {
	switch ev.EventType {
	case "tool_started", "tool_updated", "tool_completed", "tool_failed":
	default:
		return nil
	}
	return &acptui.ToolEvent{
		Name:          ev.ToolName,
		Title:         ev.ToolTitle,
		Status:        toolEventStatus(ev.EventType, ev.ToolStatus),
		Input:         ev.ToolInputJson,
		OutputPreview: ev.ToolOutputPreview,
	}
}

// attachUsageStats parses a structured usage AttachEvent (event_type="usage",
// UsageJson produced by the app-server tokenUsage mapping) into the
// provider-neutral acptui.UsageStats. Returns nil when the payload is absent
// or unparseable.
func attachUsageStats(ev *pb.AttachEvent) *acptui.UsageStats {
	if len(ev.UsageJson) == 0 {
		return nil
	}
	var raw struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		TotalTokens              int64 `json:"total_tokens"`
		CachedInputTokens        int64 `json:"cached_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		ReasoningOutputTokens    int64 `json:"reasoning_output_tokens"`
		ContextWindow            int64 `json:"context_window"`
		TTFTMs                   int64 `json:"ttft_ms"`
		TokensPerSec             int64 `json:"tokens_per_sec"`
	}
	if err := json.Unmarshal(ev.UsageJson, &raw); err != nil {
		return nil
	}
	return &acptui.UsageStats{
		InputTokens:              raw.InputTokens,
		OutputTokens:             raw.OutputTokens,
		TotalTokens:              raw.TotalTokens,
		CachedInputTokens:        raw.CachedInputTokens,
		CacheCreationInputTokens: raw.CacheCreationInputTokens,
		ReasoningOutputTokens:    raw.ReasoningOutputTokens,
		ContextWindow:            raw.ContextWindow,
		TTFTMs:                   raw.TTFTMs,
		TokensPerSec:             raw.TokensPerSec,
	}
}

// toolEventStatus normalizes the tool status for rendering: prefer the
// provider-reported ToolStatus, fall back to the event type.
func toolEventStatus(eventType, toolStatus string) string {
	if toolStatus != "" {
		return toolStatus
	}
	switch eventType {
	case "tool_started", "tool_updated":
		return "running"
	case "tool_completed":
		return "completed"
	case "tool_failed":
		return "failed"
	}
	return ""
}

func acptuiPermissionRequest(permissionID string, payload []byte) *acptui.PermissionRequest {
	var raw struct {
		Options []struct {
			Kind     string `json:"kind"`
			Name     string `json:"name"`
			OptionId string `json:"optionId"`
		} `json:"options"`
		Tool struct {
			Title string `json:"title"`
		} `json:"toolCall"`
	}
	_ = json.Unmarshal(payload, &raw)
	title := raw.Tool.Title
	if title == "" {
		title = "Agent permission request"
	}
	req := &acptui.PermissionRequest{RequestID: permissionID, Title: title}
	for _, opt := range raw.Options {
		id := opt.OptionId
		if id == "" {
			continue
		}
		label := opt.Name
		if label == "" {
			label = id
		}
		req.Options = append(req.Options, acptui.PermissionOption{OptionId: id, Label: label, Kind: opt.Kind})
	}
	if len(req.Options) == 0 {
		req.Options = []acptui.PermissionOption{{OptionId: "allow", Label: "Allow"}, {OptionId: "reject", Label: "Reject"}}
	}
	return req
}

func attachOutputEventFields(ev *pb.AttachEvent) ([]byte, string, bool) {
	if ev == nil {
		return nil, "", false
	}
	data := ev.Data
	source := ev.Source
	input := ev.Input
	class := sourceframe.Classify(data)
	if class.Input {
		source = class.Source
		input = true
		data = cleanAttachSourceFrameText(data)
	}
	return data, source, input
}

func cleanAttachSourceFrameText(data []byte) []byte {
	text := string(data)
	for _, marker := range []string{"📱 来自飞书: ", "🖥️ 来自桌面: ", "💻 来自终端: "} {
		if _, after, ok := strings.Cut(text, marker); ok {
			return []byte(strings.TrimSpace(after))
		}
	}
	return data
}

// amuxAgentSender adapts sessionClient.Send to acptui.AgentSender.
type amuxAgentSender struct {
	sc       sessionClient
	targetID string
	name     string
	perm     *attachPermissionState
}

func (s *amuxAgentSender) Send(ctx context.Context, data []byte) error {
	text := strings.TrimRight(string(data), "\r\n")
	return s.sc.Submit(ctx, s.targetID, s.name, text)
}

func (s *amuxAgentSender) Interrupt(ctx context.Context) error {
	return s.sc.Interrupt(ctx, s.targetID, s.name)
}

func (s *amuxAgentSender) WorkingDirectory(ctx context.Context) (string, error) {
	sessions, err := s.sc.List(ctx)
	if err != nil {
		return "", err
	}
	entry, ok := sessionEntryForAttach(sessions, s.targetID, s.name)
	if !ok {
		return "", fmt.Errorf("session %q not found", s.name)
	}
	return entry.Cwd, nil
}

func (s *amuxAgentSender) ChangeWorkingDirectory(ctx context.Context, path string, opts acptui.WorkingDirectoryOptions) (string, error) {
	current, err := s.WorkingDirectory(ctx)
	if err != nil {
		return "", err
	}
	resp, err := s.sc.ChangeCwd(ctx, s.targetID, &pb.ChangeCwdRequest{
		Name:        s.name,
		Cwd:         path,
		Wait:        opts.Wait || opts.Interrupt,
		Interrupt:   opts.Interrupt,
		ExpectedCwd: current,
	})
	if err != nil {
		return "", err
	}
	return resp.EffectiveCwd, nil
}

func (s *amuxAgentSender) QueueList(ctx context.Context) ([]acptui.QueueItem, error) {
	items, err := s.sc.QueueList(ctx, s.targetID, s.name)
	if err != nil {
		return nil, err
	}
	out := make([]acptui.QueueItem, 0, len(items))
	for _, item := range items {
		out = append(out, acptui.QueueItem{
			ID:     item.Id,
			Source: item.Source,
			Text:   item.Text,
		})
	}
	return out, nil
}

func (s *amuxAgentSender) QueueClear(ctx context.Context) (int, error) {
	cleared, err := s.sc.QueueClear(ctx, s.targetID, s.name)
	return int(cleared), err
}

func (s *amuxAgentSender) QueueCancel(ctx context.Context, id string) error {
	return s.sc.QueueCancel(ctx, s.targetID, s.name, id)
}

func parsePermissionOptionIndex(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func (s *amuxAgentSender) RespondToPermission(ctx context.Context, requestID, optionID string) error {
	if s.perm != nil {
		s.perm.mu.Lock()
		if requestID == "" {
			requestID = s.perm.id
		}
		if n, err := parsePermissionOptionIndex(optionID); err == nil && n > 0 && n <= len(s.perm.options) {
			optionID = s.perm.options[n-1]
		}
		s.perm.mu.Unlock()
	}
	return s.sc.RespondPermission(ctx, s.targetID, s.name, requestID, optionID)
}

// ── attach command ───────────────────────────────────────────────────────────

func newAttachCmd() *cobra.Command {
	var debug bool
	c := &cobra.Command{
		Use:               "attach <name>",
		Short:             "Attach to a session (/detach or Ctrl-] to detach)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSingleSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]

			r, err := newSessionResolver()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			defer r.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			resolved, err := r.ResolveSession(ctx, ref)
			if err != nil {
				return err
			}
			sc := resolved.Target.Backend
			targetID := resolved.Target.ID
			name := resolved.Session.Name
			agentType := resolved.Session.AgentType
			targetName := targetDisplayName(resolved.Target)
			stream, err := sc.Attach(ctx, targetID, name)
			if err != nil {
				return fmt.Errorf("attach: %w", err)
			}

			permState := &attachPermissionState{}
			agentStream := &amuxAgentStream{stream: stream, perm: permState}
			agentSender := &amuxAgentSender{sc: sc, targetID: targetID, name: name, perm: permState}
			cfg := acptui.Config{
				SessionName: name,
				AgentType:   agentType,
				TargetName:  targetName,
				Debug:       debug,
				ShouldTriggerStatus: func(ev acptui.OutputEvent) bool {
					return !ev.Input && !isAttachSourceFrame(ev.Data)
				},
				ShouldLockTurn: func(ev acptui.OutputEvent) bool {
					class := sourceframe.Classify(ev.Data)
					return (ev.Input && ev.Source == sourceframe.SourceLark) ||
						(class.Input && class.Source == sourceframe.SourceLark)
				},
			}
			return acptui.Run(ctx, agentStream, agentSender, cfg)
		},
	}
	c.Flags().BoolVar(&debug, "debug", false, "print raw stdin bytes to stderr")
	return c
}
