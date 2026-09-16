package sessionlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type routeCall struct {
	targetID string
	method   string
	payload  interface{}
}

type fakeRouter struct {
	calls []routeCall
	ctxs  []context.Context
	resp  *pb.TunnelMsg
	err   error
}

func (r *fakeRouter) Route(ctx context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error) {
	r.calls = append(r.calls, routeCall{targetID: targetID, method: method, payload: payload})
	r.ctxs = append(r.ctxs, ctx)
	return r.resp, r.err
}

func runResponse(t *testing.T, session *pb.Session) *pb.TunnelMsg {
	t.Helper()
	payload, err := json.Marshal(&pb.RunResponse{Session: session})
	if err != nil {
		t.Fatal(err)
	}
	return &pb.TunnelMsg{Payload: payload}
}

func validRequest() StartRequest {
	return StartRequest{
		TargetID: "target-1",
		Name:     "requested-name",
		Launch: agentprofile.LaunchProjection{
			AgentType:    "codex",
			AgentProfile: "team/coder@sha256:abc",
			ProfileSpec:  &pb.AgentProfileSpec{Model: "gpt-5.6"},
			ExtraArgs:    []string{"--flag"},
			Env:          []string{"KEY=value"},
			Model:        "gpt-5.6",
		},
		Workspace: WorkspaceRequest{
			Cwd: "/work", Repo: "ssh://repo", RepoRef: "main",
			Source: "feishu", Scope: "chat-1",
		},
	}
}

func TestStartMapsRequestAndBackfillsResponse(t *testing.T) {
	idle := int64(0)
	req := validRequest()
	req.IdleTimeoutSeconds = &idle
	router := &fakeRouter{resp: runResponse(t, &pb.Session{
		Name: "actual-name", AgentProfile: "resolved/profile", AgentType: "claude",
		EffectiveModel: "resolved-model", Cwd: "/resolved", State: "running",
	})}

	got, err := New(router).Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(router.calls) != 1 {
		t.Fatalf("Route calls = %d, want 1", len(router.calls))
	}
	call := router.calls[0]
	if call.targetID != req.TargetID || call.method != "Run" {
		t.Fatalf("Route = (%q, %q), want (%q, Run)", call.targetID, call.method, req.TargetID)
	}
	runReq, ok := call.payload.(*pb.RunRequest)
	if !ok {
		t.Fatalf("payload type = %T, want *pb.RunRequest", call.payload)
	}
	if runReq.Name != req.Name || runReq.AgentType != req.Launch.AgentType || runReq.AgentProfile != req.Launch.AgentProfile || runReq.ProfileSpec != req.Launch.ProfileSpec {
		t.Fatalf("launch mapping = %+v", runReq)
	}
	if !reflect.DeepEqual(runReq.ExtraArgs, req.Launch.ExtraArgs) || !reflect.DeepEqual(runReq.Env, req.Launch.Env) {
		t.Fatalf("args/env mapping = %+v", runReq)
	}
	if runReq.Cwd != req.Workspace.Cwd || runReq.Repo != req.Workspace.Repo || runReq.RepoRef != req.Workspace.RepoRef || runReq.WorkspaceSource != req.Workspace.Source || runReq.WorkspaceScope != req.Workspace.Scope {
		t.Fatalf("workspace mapping = %+v", runReq)
	}
	if !runReq.IdleTimeoutSecondsSet || runReq.IdleTimeoutSeconds != 0 {
		t.Fatalf("idle timeout = (%v, %d), want (true, 0)", runReq.IdleTimeoutSecondsSet, runReq.IdleTimeoutSeconds)
	}
	want := SessionRef{TargetID: "target-1", Name: "actual-name", AgentProfile: "resolved/profile", AgentType: "claude", EffectiveModel: "resolved-model", Cwd: "/resolved", State: "running"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SessionRef = %+v", got)
	}
}

func TestStartLeavesIdleTimeoutUnset(t *testing.T) {
	req := validRequest()
	router := &fakeRouter{resp: runResponse(t, &pb.Session{Name: req.Name})}
	if _, err := New(router).Start(context.Background(), req); err != nil {
		t.Fatalf("Start: %v", err)
	}
	runReq := router.calls[0].payload.(*pb.RunRequest)
	if runReq.IdleTimeoutSecondsSet || runReq.IdleTimeoutSeconds != 0 {
		t.Fatalf("idle timeout = (%v, %d), want unset", runReq.IdleTimeoutSecondsSet, runReq.IdleTimeoutSeconds)
	}
}

func TestStartMapsNonZeroIdleTimeout(t *testing.T) {
	idle := int64(900)
	req := validRequest()
	req.IdleTimeoutSeconds = &idle
	router := &fakeRouter{resp: runResponse(t, &pb.Session{Name: req.Name})}
	if _, err := New(router).Start(context.Background(), req); err != nil {
		t.Fatalf("Start: %v", err)
	}
	runReq := router.calls[0].payload.(*pb.RunRequest)
	if !runReq.IdleTimeoutSecondsSet || runReq.IdleTimeoutSeconds != idle {
		t.Fatalf("idle timeout = (%v, %d), want (true, %d)", runReq.IdleTimeoutSecondsSet, runReq.IdleTimeoutSeconds, idle)
	}
}

func TestStartUsesLaunchAndWorkspaceFallbacks(t *testing.T) {
	req := validRequest()
	router := &fakeRouter{resp: runResponse(t, &pb.Session{Name: "actual", State: "starting"})}
	got, err := New(router).Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got.AgentProfile != req.Launch.AgentProfile || got.AgentType != req.Launch.AgentType || got.EffectiveModel != req.Launch.Model || got.Cwd != req.Workspace.Cwd {
		t.Fatalf("fallback SessionRef = %+v", got)
	}
}

func TestStartPreservesRouteCauseAndDoesNotRetry(t *testing.T) {
	cause := errors.New("route unavailable")
	router := &fakeRouter{err: cause}
	_, err := New(router).Start(context.Background(), validRequest())
	assertStartError(t, err, ErrorRoute)
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(%v, cause) = false", err)
	}
	if len(router.calls) != 1 {
		t.Fatalf("Route calls = %d, want 1", len(router.calls))
	}
}

func TestStartPassesCallerDeadline(t *testing.T) {
	req := validRequest()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	router := &fakeRouter{err: ctx.Err()}
	_, err := New(router).Start(ctx, req)
	assertStartError(t, err, ErrorRoute)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline cause", err)
	}
	if len(router.ctxs) != 1 || router.ctxs[0] != ctx {
		t.Fatal("Start did not pass the caller context to Route")
	}
}

func TestStartPassesCallerCancellation(t *testing.T) {
	req := validRequest()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	router := &fakeRouter{err: ctx.Err()}
	_, err := New(router).Start(ctx, req)
	assertStartError(t, err, ErrorRoute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation cause", err)
	}
	if len(router.ctxs) != 1 || router.ctxs[0] != ctx {
		t.Fatal("Start did not pass the canceled caller context to Route")
	}
}

func TestStartRejectsInvalidRequestBeforeRouting(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StartRequest)
	}{
		{name: "target", mutate: func(req *StartRequest) { req.TargetID = "" }},
		{name: "name", mutate: func(req *StartRequest) { req.Name = "" }},
		{name: "agent type", mutate: func(req *StartRequest) { req.Launch.AgentType = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			tt.mutate(&req)
			router := &fakeRouter{}
			_, err := New(router).Start(context.Background(), req)
			assertStartError(t, err, ErrorInvalidRequest)
			if len(router.calls) != 0 {
				t.Fatalf("Route calls = %d, want 0", len(router.calls))
			}
		})
	}
}

func TestStartRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name string
		resp *pb.TunnelMsg
	}{
		{name: "nil tunnel message"},
		{name: "damaged payload", resp: &pb.TunnelMsg{Payload: []byte("{")}},
		{name: "nil session", resp: runResponse(t, nil)},
		{name: "empty session name", resp: runResponse(t, &pb.Session{})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(&fakeRouter{resp: tt.resp}).Start(context.Background(), validRequest())
			assertStartError(t, err, ErrorInvalidResponse)
		})
	}
}

func assertStartError(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var startErr *StartError
	if !errors.As(err, &startErr) {
		t.Fatalf("error = %T %v, want *StartError", err, err)
	}
	if startErr.Kind != want {
		t.Fatalf("error kind = %q, want %q", startErr.Kind, want)
	}
}
