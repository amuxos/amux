package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/mcp"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (h *mcpHandler) sessionList(ctx context.Context, _ json.RawMessage) (mcp.ToolResult, error) {
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := clientop.SessionList(ctx, client)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(sessionListMCPView(resp))), nil
}
func (h *mcpHandler) sessionRun(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var req clientop.SessionRunRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	if strings.TrimSpace(req.Name) == "" {
		return mcp.ErrorText("name is required"), nil
	}
	if strings.TrimSpace(req.Profile) == "" {
		return mcp.ErrorText("profile is required"), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	launch, err := projectRunLaunchWithRuntime(req.Profile, req.AgentArgs, nil, req.Runtime, req.Model, func(ref string) (agentprofile.Profile, error) {
		return resolveHubRunProfile(ctx, client, ref)
	})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	req.AgentType = launch.AgentType
	req.AgentProfile = launch.AgentProfile
	req.ProfileSpec = launch.ProfileSpec
	req.ExtraArgs = launch.ExtraArgs
	req.Env = launch.Env
	req.Target, err = clientop.ResolveSessionRunTarget(ctx, client, req.Target)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	resp, err := clientop.SessionRun(ctx, client, req)
	if err != nil {
		var fe *clientop.FieldError
		if errors.As(err, &fe) {
			return mcp.ErrorText(fe.Render(mcpSessionRunFieldNames)), nil
		}
		return mcp.ErrorText(err.Error()), nil
	}
	view := sessionRunMCPView(resp)
	view["target"] = req.Target
	return mcp.Text(toJSON(view)), nil
}
func resolveHubRunProfile(ctx context.Context, client pb.AmuxdClient, ref string) (agentprofile.Profile, error) {
	resp, err := client.GetAgentProfile(ctx, &pb.GetAgentProfileRequest{Ref: ref})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return agentprofile.Profile{}, fmt.Errorf("%s: %w", status.Convert(err).Message(), agentprofile.ErrProfileNotFound)
		}
		return agentprofile.Profile{}, fmt.Errorf("get agent profile %q: %w", ref, err)
	}
	if resp == nil || resp.Profile == nil || resp.Profile.Profile == nil {
		return agentprofile.Profile{}, fmt.Errorf("get agent profile %q: hub returned an empty profile", ref)
	}
	profile, err := agentprofile.FromPBProfileDefinition(resp.Profile.Profile)
	if err != nil {
		return agentprofile.Profile{}, fmt.Errorf("get agent profile %q: invalid hub profile: %w", ref, err)
	}
	return profile, nil
}
func (h *mcpHandler) sessionSend(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a mcpSessionSendArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	ref := mcpSessionRefArgs{Session: a.Session, Target: a.Target}.ref()
	if ref.Name == "" {
		return mcp.ErrorText("session is required"), nil
	}
	if strings.TrimSpace(a.Text) == "" {
		return mcp.ErrorText("text is required"), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := clientop.SessionSend(ctx, client, ref, a.Text)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[string]any{"session": ref.Name, "ok": resp.GetOk()})), nil
}
func (h *mcpHandler) sessionInterrupt(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	return h.sessionLifecycle(ctx, args, func(ctx context.Context, hub pb.AmuxdClient, ref clientop.SessionRef) (bool, error) {
		resp, err := clientop.SessionInterrupt(ctx, hub, ref)
		if err != nil {
			return false, err
		}
		return resp.GetOk(), nil
	})
}
func (h *mcpHandler) sessionSleep(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	return h.sessionLifecycle(ctx, args, func(ctx context.Context, hub pb.AmuxdClient, ref clientop.SessionRef) (bool, error) {
		resp, err := clientop.SessionSleep(ctx, hub, ref)
		if err != nil {
			return false, err
		}
		return resp.GetOk(), nil
	})
}
func (h *mcpHandler) sessionStop(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	return h.sessionLifecycle(ctx, args, func(ctx context.Context, hub pb.AmuxdClient, ref clientop.SessionRef) (bool, error) {
		resp, err := clientop.SessionStop(ctx, hub, ref)
		if err != nil {
			return false, err
		}
		return resp.GetOk(), nil
	})
}
func (h *mcpHandler) sessionResume(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	ref, err := sessionRefForMCP(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := clientop.SessionResume(ctx, client, ref)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(sessionRunMCPView(resp))), nil
}
func (h *mcpHandler) sessionRestart(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	ref, err := sessionRefForMCP(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := clientop.SessionRestart(ctx, client, ref)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(sessionRunMCPView(resp))), nil
}

func (h *mcpHandler) sessionSetModel(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a mcpSessionSetModelArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	ref := mcpSessionRefArgs{Session: a.Session, Target: a.Target}.ref()
	if ref.Name == "" {
		return mcp.ErrorText("session is required"), nil
	}
	if strings.TrimSpace(a.Model) == "" {
		return mcp.ErrorText("model is required"), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := clientop.SessionSetModel(ctx, client, ref, a.Model)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(sessionRunMCPView(resp))), nil
}

func (h *mcpHandler) sessionLifecycle(ctx context.Context, args json.RawMessage, call func(context.Context, pb.AmuxdClient, clientop.SessionRef) (bool, error)) (mcp.ToolResult, error) {
	ref, err := sessionRefForMCP(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	ok, err := call(ctx, client, ref)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[ // sessionLifecycle is the shared parse-dial-call-render path for the lifecycle
	// tools whose hub RPC answers a bare ok (interrupt/sleep/stop).
	string]any{"session": ref.Name, "ok": ok})), nil
}

func (h *mcpHandler) sessionQueueList(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	ref, err := sessionRefForMCP(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := clientop.SessionQueueList(ctx, client, ref)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	items := make([]map[ // sessionQueueList dispatches amux.session_queue_list: the session's pending
	// queued inputs, oldest first as the hub returns them. The hub applies the
	// session ownership boundary only while fleet isolation is active.
	string]any, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		if item == nil {
			continue
		}
		items = append(items, map[string]any{"id": item.GetId(), "source": item.GetSource(), "text": item.GetText(), "created_at": item.GetCreatedAt()})
	}
	return mcp.Text(toJSON(map[string]any{"session": ref.Name, "items": items})), nil
}

func (h *mcpHandler) sessionQueueClear(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	ref, err := sessionRefForMCP(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := clientop.SessionQueueClear(ctx, client, ref)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[ // sessionQueueClear dispatches amux.session_queue_clear: drop every queued
	// input pending on the session.
	string]any{"session": ref.Name, "cleared": resp.GetCleared()})), nil
}

func (h *mcpHandler) sessionQueueCancel(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a mcpSessionQueueCancelArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return mcp.ErrorText("invalid arguments: " + err.Error()), nil
	}
	ref := mcpSessionRefArgs{Session: a.Session, Target: a.Target}.ref()
	if ref.Name == "" {
		return mcp.ErrorText("session is required"), nil
	}
	queueID := strings.TrimSpace(a.QueueID)
	if queueID == "" {
		return mcp.ErrorText("queue_id is required"), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := clientop.SessionQueueCancel(ctx, client, ref, queueID)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[ // sessionQueueCancel dispatches amux.session_queue_cancel: drop one queued
	// input by the id amux.session_queue_list reported for it.
	string]any{"session": ref.Name, "queue_id": queueID, "ok": resp.GetOk()})), nil
}

func (h *mcpHandler) sessionPrune(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var a mcpSessionPruneArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return mcp.ErrorText("invalid arguments: " + err.Error()), nil
		}
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := clientop.SessionPrune(ctx, client, strings.TrimSpace(a.Target))
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	names := resp.GetNames()
	if names == nil {
		names = []string{ // sessionPrune dispatches amux.session_prune: delete archived sessions from a
			// target. This is destructive and distinct from amux.task_prune, which deletes
			// terminal task history.
		}
	}
	return mcp.Text(toJSON(map[string]any{"count": resp.GetCount(), "names": names})), nil
}

func (h *mcpHandler) sessionStream(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	ref, err := sessionRefForMCP(args)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, err := newSessionClientForMCP()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	ref, err = clientop.ResolveSessionRef(ctx, client, ref)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}

	drainCtx, cancel := context.WithTimeout(ctx, sessionStreamMaxWait)
	defer cancel()
	stream, err := client.Attach(drainCtx, &pb.AmuxdAttachRequest{TargetId: ref.Target, Name: ref.Name, TimelineMode: pb.AttachRequest_FOLLOW})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	transcript, state, truncated, err := drainSessionTranscript(drainCtx, stream, sessionStreamTranscriptCap, sessionStreamIdleGrace)
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	return mcp.Text(toJSON(map[ // sessionStream dispatches amux.session_stream: a one-shot Attach with replay
	// whose events drain into a transcript until the session goes quiet or a short
	// deadline passes. It is not a live tail; the caller re-invokes for newer
	// output.
	string]any{"session": ref.Name, "state": state, "truncated": truncated, "transcript": transcript})), nil
}

func drainSessionTranscript(ctx context.Context, stream attachEventStream, maxBytes int, idleGrace time.Duration) (transcript string, state string, truncated bool, err error) {
	type recvResult struct {
		ev  *pb.AttachEvent
		err error
	}
	results := make(chan recvResult, 1)
	go func() {
		for {
			ev, rerr := stream.Recv()
			results <- recvResult{ev: ev, err: rerr}
			if rerr != nil {
				return
			}
		}
	}()
	var buf strings.Builder
	lastState := ""
	idle := time.NewTimer(idleGrace)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return buf.String(), lastState, truncated, nil
		case <-idle.C:
			return buf.String(), lastState, truncated, nil
		case r := <-results:
			if r.err != nil {
				if r.err == io.EOF || ctx.Err() != nil {
					return buf.String(), lastState, truncated, nil
				}
				return "", lastState, truncated, r.err
			}
			ev := r.ev
			if ev.GetKind() == pb.AttachEvent_HEARTBEAT {
				continue
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(idleGrace)
			if ev.GetKind() == pb.AttachEvent_STATE_CHANGE {
				lastState = ev.GetState()
			}
			data := ev.GetData()
			if len(data) == 0 || truncated {
				continue
			}
			remaining := maxBytes - buf.Len()
			switch {
			case remaining <= 0:
				truncated = true
			case len(data) > remaining:
				buf.Write(data[: // drainSessionTranscript reads an Attach stream until the session goes quiet
				// (idleGrace without a non-heartbeat event — replay delivered, nothing live
				// following), the stream ends, or ctx's deadline passes; every path but a
				// stream error returns the transcript aggregated so far. Heartbeats are
				// keepalive noise: they neither extend the idle window nor reach the
				// transcript. Output payloads aggregate up to maxBytes; past the cap the drain
				// keeps watching for quiet but stops appending and reports truncated.
				remaining])
				truncated = true
			default:
				buf.Write(data)
			}
		}
	}
}

func sessionMCPView(s *pb.Session) map[ // sessionMCPView is the compact session object the MCP tools return.
string]any {
	if s == nil {
		return nil
	}
	view := map[string]any{"id": s.GetId(), "name": s.GetName(), "state": s.GetState()}
	if profile := s.GetAgentProfile(); profile != "" {
		view["agent_profile"] = profile
	}
	if agentType := s.GetAgentType(); agentType != "" {
		view["agent_type"] = agentType
	}
	if cwd := s.GetCwd(); cwd != "" {
		view["cwd"] = cwd
	}
	return view
}

func sessionRunMCPView(resp *pb.RunResponse) map[ // sessionRunMCPView renders a RunResponse (session_run / session_resume /
// session_restart); launch warnings pass through when present.
string]any {
	view := map[string]any{"session": sessionMCPView(resp.GetSession())}
	if warnings := resp.GetWarnings(); len(warnings) > 0 {
		view["warnings"] = warnings
	}
	return view
}
func sessionListMCPView(resp *pb.ListTargetsResponse) map[string]any {
	targets := make([]map[string]any, 0, len(resp.GetTargets()))
	for _, target := range resp.GetTargets() {
		if target == nil {
			continue
		}
		sessions := make([]map[string]any, 0, len(target.GetSessions()))
		for _, s := range target.GetSessions() {
			sessions = append(sessions, sessionMCPView(s))
		}
		targets = append(targets, map[string]any{"id": target.GetId(), "name": target.GetName(), "status": target.GetStatus(), "sessions": sessions})
	}
	return map[string]any{"targets": targets}
}
