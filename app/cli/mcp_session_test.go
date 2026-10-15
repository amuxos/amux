package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/amuxos/amux/internal/agentprofile"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mcpSessionRunClient struct {
	pb.AmuxdClient
	getResp  *pb.GetAgentProfileResponse
	getErr   error
	listResp *pb.ListTargetsResponse
	runReq   *pb.AmuxdRunRequest
	runResp  *pb.RunResponse
}

func (f *mcpSessionRunClient) GetAgentProfile(_ context.Context, _ *pb.GetAgentProfileRequest, _ ...grpc.CallOption) (*pb.GetAgentProfileResponse, error) {
	return f.getResp, f.getErr
}

func (f *mcpSessionRunClient) Run(_ context.Context, req *pb.AmuxdRunRequest, _ ...grpc.CallOption) (*pb.RunResponse, error) {
	f.runReq = req
	return f.runResp, nil
}

func (f *mcpSessionRunClient) ListTargets(_ context.Context, _ *pb.ListTargetsRequest, _ ...grpc.CallOption) (*pb.ListTargetsResponse, error) {
	if f.listResp != nil {
		return f.listResp, nil
	}
	return &pb.ListTargetsResponse{Targets: []*pb.Target{{Id: "devbox"}}}, nil
}

func useMCPSessionRunClient(t *testing.T, client pb.AmuxdClient) {
	t.Helper()
	previous := newSessionClientForMCP
	newSessionClientForMCP = func() (pb.AmuxdClient, func() error, error) {
		return client, func() error { return nil }, nil
	}
	t.Cleanup(func() { newSessionClientForMCP = previous })
}

func useUnavailableMCPSessionClient(t *testing.T) error {
	t.Helper()
	unavailableErr := errors.New("test hub unavailable")
	previous := newSessionClientForMCP
	newSessionClientForMCP = func() (pb.AmuxdClient, func() error, error) {
		return nil, nil, unavailableErr
	}
	t.Cleanup(func() { newSessionClientForMCP = previous })
	return unavailableErr
}

func TestMCPSessionRunProjectsHubProfileLaunch(t *testing.T) {
	profile, ok := agentprofile.ResolveBuiltin("builtin/codex")
	if !ok {
		t.Fatal("builtin/codex profile is unavailable")
	}
	client := &mcpSessionRunClient{
		getResp: &pb.GetAgentProfileResponse{Profile: &pb.AgentProfileInfo{Profile: agentprofile.ToPBProfileSpec(profile)}},
		runResp: &pb.RunResponse{Session: &pb.Session{Id: "child-1", Name: "child"}},
	}
	useMCPSessionRunClient(t, client)

	res, err := (&mcpHandler{}).sessionRun(context.Background(), json.RawMessage(`{
		"profile":"builtin/codex","name":"child","target":"n37-252-155",
		"cwd":"/repo","repo":"https://example.com/repo.git","ref":"main","run_as":"self"
	}`))
	if err != nil {
		t.Fatalf("sessionRun: %v", err)
	}
	if res.IsError {
		t.Fatalf("sessionRun returned MCP error: %s", res.Content[0].Text)
	}
	got := client.runReq
	if got == nil {
		t.Fatal("hub Run was not called")
	}
	if got.AgentType != "codex" || got.AgentProfile != "builtin/codex" {
		t.Fatalf("launch identity = type %q profile %q, want codex/builtin/codex", got.AgentType, got.AgentProfile)
	}
	if got.ProfileSpec == nil || got.ProfileSpec.Ref != "builtin/codex" || got.ProfileSpec.Type != "codex" {
		t.Fatalf("profile_spec = %+v, want resolved builtin/codex snapshot", got.ProfileSpec)
	}
	if got.TargetId != "n37-252-155" || got.Name != "child" || got.Cwd != "/repo" ||
		got.Repo != "https://example.com/repo.git" || got.RepoRef != "main" || got.RunAs != "self" {
		t.Fatalf("request fields = %+v", got)
	}
}

func TestMCPSessionRunForwardsPinnedPackProfileSnapshot(t *testing.T) {
	client := &mcpSessionRunClient{
		getResp: &pb.GetAgentProfileResponse{Profile: &pb.AgentProfileInfo{Profile: &pb.AgentProfileSpec{
			Ref:         "team/coder",
			PinnedRef:   "team/coder@rev-1",
			Namespace:   "team",
			Name:        "coder",
			Revision:    "rev-1",
			Type:        "codex",
			Model:       "gpt-5.4",
			DefaultArgs: []string{"--sandbox", "workspace-write"},
			DefaultEnv:  []string{"MODE=review"},
		}}},
		runResp: &pb.RunResponse{Session: &pb.Session{Id: "child-pack", Name: "child"}},
	}
	useMCPSessionRunClient(t, client)

	res, err := (&mcpHandler{}).sessionRun(context.Background(), json.RawMessage(`{"profile":"team/coder","name":"child"}`))
	if err != nil {
		t.Fatalf("sessionRun: %v", err)
	}
	if res.IsError {
		t.Fatalf("sessionRun returned MCP error: %s", res.Content[0].Text)
	}
	got := client.runReq
	if got == nil || got.AgentType != "codex" || got.AgentProfile != "team/coder@rev-1" {
		t.Fatalf("launch request = %+v, want pinned team/coder@rev-1", got)
	}
	if got.ProfileSpec == nil || got.ProfileSpec.Ref != "team/coder" || got.ProfileSpec.PinnedRef != "team/coder@rev-1" {
		t.Fatalf("profile snapshot = %+v, want pinned team/coder@rev-1", got.ProfileSpec)
	}
	if !equalRunStrings(got.ExtraArgs, []string{"--model", "gpt-5.4", "--sandbox", "workspace-write"}) {
		t.Fatalf("extra args = %#v", got.ExtraArgs)
	}
	if !equalRunStrings(got.Env, []string{"MODE=review"}) {
		t.Fatalf("env = %#v", got.Env)
	}
	if !strings.Contains(res.Content[0].Text, `"target":"devbox"`) {
		t.Fatalf("session_run result = %s, want resolved target", res.Content[0].Text)
	}
}

func TestMCPSessionRunFallsBackToLegacyAgentOnlyOnProfileNotFound(t *testing.T) {
	client := &mcpSessionRunClient{
		getErr:  status.Error(codes.NotFound, "unknown agent profile"),
		runResp: &pb.RunResponse{Session: &pb.Session{Id: "child-raw", Name: "child"}},
	}
	useMCPSessionRunClient(t, client)

	res, err := (&mcpHandler{}).sessionRun(context.Background(), json.RawMessage(`{"profile":"codex","name":"child"}`))
	if err != nil {
		t.Fatalf("sessionRun: %v", err)
	}
	if res.IsError {
		t.Fatalf("sessionRun returned MCP error: %s", res.Content[0].Text)
	}
	got := client.runReq
	if got == nil || got.AgentType != "codex" || got.AgentProfile != "" || got.ProfileSpec != nil {
		t.Fatalf("raw launch request = %+v, want legacy codex without profile snapshot", got)
	}
}

func TestMCPSessionRunDoesNotFallbackOnProfileControlPlaneFailure(t *testing.T) {
	client := &mcpSessionRunClient{getErr: status.Error(codes.Internal, "registry unavailable")}
	useMCPSessionRunClient(t, client)

	res, err := (&mcpHandler{}).sessionRun(context.Background(), json.RawMessage(`{"profile":"codex","name":"child"}`))
	if err != nil {
		t.Fatalf("sessionRun: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "registry unavailable") {
		t.Fatalf("sessionRun = %+v, want registry failure", res)
	}
	if client.runReq != nil {
		t.Fatalf("hub Run called after profile control-plane failure: %+v", client.runReq)
	}
}

func TestMCPSessionRunRejectsUnknownQualifiedProfile(t *testing.T) {
	client := &mcpSessionRunClient{getErr: status.Error(codes.NotFound, "unknown agent profile team/missing")}
	useMCPSessionRunClient(t, client)

	res, err := (&mcpHandler{}).sessionRun(context.Background(), json.RawMessage(`{"profile":"team/missing","name":"child"}`))
	if err != nil {
		t.Fatalf("sessionRun: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "unknown agent profile") {
		t.Fatalf("sessionRun = %+v, want unknown profile error", res)
	}
	if client.runReq != nil {
		t.Fatalf("hub Run called for unknown qualified profile: %+v", client.runReq)
	}
}

func TestMCPToolsIncludeCoreSessionTools(t *testing.T) {
	for _, name := range []string{
		"amux.session_list", "amux.session_run", "amux.session_send",
		"amux.session_interrupt", "amux.session_sleep", "amux.session_resume",
		"amux.session_stop", "amux.session_restart", "amux.session_stream",
	} {
		found := false
		for _, tool := range amuxTools() {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("missing MCP tool %s", name)
		}
	}
}

func TestMCPSessionCreationDescriptionsAvoidPreflightTargetList(t *testing.T) {
	run := findMCPTool(t, amuxTools(), "amux.session_run")
	for _, want := range []string{"未指定 target 时先直接调用", "唯一连接 target", "多个 target"} {
		if !strings.Contains(run.Description, want) {
			t.Errorf("session_run description missing %q: %q", want, run.Description)
		}
	}

	list := findMCPTool(t, amuxTools(), "amux.session_list")
	for _, want := range []string{"创建 Session 时不要预先调用", "session_run", "多个 target"} {
		if !strings.Contains(list.Description, want) {
			t.Errorf("session_list description missing %q: %q", want, list.Description)
		}
	}
}

func TestMCPToolsIncludeSessionQueueAndPruneTools(t *testing.T) {
	for _, name := range []string{
		"amux.session_queue_list", "amux.session_queue_clear",
		"amux.session_queue_cancel", "amux.session_prune",
	} {
		found := false
		for _, tool := range amuxTools() {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("missing MCP tool %s", name)
		}
	}
}

func TestMCPSessionRunSchemaRequiresProfileAndName(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.session_run")
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("session_run schema parse: %v", err)
	}
	if !sameStrings(schema.Required, []string{"profile", "name"}) {
		t.Fatalf("session_run required = %v, want [profile name]", schema.Required)
	}
	for _, field := range []string{"profile", "name", "target", "cwd", "repo", "ref", "run_as", "runtime", "model", "args"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("session_run schema missing %s property", field)
		}
	}
}

func TestMCPSessionRunResolvesPortableProfile(t *testing.T) {
	client := &mcpSessionRunClient{
		getResp: &pb.GetAgentProfileResponse{Profile: &pb.AgentProfileInfo{Profile: &pb.AgentProfileSpec{Namespace: "team", Name: "reviewer", Ref: "team/reviewer", AgentsContent: "Review changes", Envs: []string{"REVIEW_TOKEN"}}}},
		runResp: &pb.RunResponse{Session: &pb.Session{Id: "portable", Name: "child"}},
	}
	useMCPSessionRunClient(t, client)
	res, err := (&mcpHandler{}).sessionRun(context.Background(), json.RawMessage(`{"profile":"team/reviewer","name":"child","runtime":"claude","model":"review-model","args":["--permission-mode","plan"]}`))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v error=%v", res, err)
	}
	if client.runReq == nil || client.runReq.AgentType != "claude" || client.runReq.ProfileSpec.RuntimeRef != "claude" || client.runReq.ProfileSpec.Model != "review-model" {
		t.Fatalf("request=%+v", client.runReq)
	}
	if !strings.Contains(strings.Join(client.runReq.ExtraArgs, " "), "--permission-mode plan") {
		t.Fatal("permission arguments lost")
	}
	res, err = (&mcpHandler{}).sessionRun(context.Background(), json.RawMessage(`{"profile":"team/reviewer","name":"child","runtime":"codex","model":"review-model"}`))
	if err != nil || res.IsError {
		t.Fatalf("default launch: %+v, %v", res, err)
	}
	if !strings.Contains(strings.Join(client.runReq.ExtraArgs, " "), `approval_policy="never" -c sandbox_mode="danger-full-access"`) {
		t.Fatalf("MCP external expert did not default to bypass: %q", client.runReq.ExtraArgs)
	}
	client.runReq = nil
	res, err = (&mcpHandler{}).sessionRun(context.Background(), json.RawMessage(`{"profile":"team/reviewer","name":"child"}`))
	if err != nil || !res.IsError || client.runReq != nil {
		t.Fatalf("missing runtime reached execution: result=%+v error=%v", res, err)
	}
}

func TestMCPSessionRefSchemasRequireSession(t *testing.T) {
	for _, name := range []string{
		"amux.session_interrupt", "amux.session_sleep", "amux.session_resume",
		"amux.session_stop", "amux.session_restart", "amux.session_stream",
	} {
		t.Run(name, func(t *testing.T) {
			tool := findMCPTool(t, amuxTools(), name)
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			}
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Fatalf("%s schema parse: %v", name, err)
			}
			if !sameStrings(schema.Required, []string{"session"}) {
				t.Fatalf("%s required = %v, want [session]", name, schema.Required)
			}
			for _, field := range []string{"session", "target"} {
				if _, ok := schema.Properties[field]; !ok {
					t.Errorf("%s schema missing %s property", name, field)
				}
			}
		})
	}
}

func TestMCPSessionSendSchemaRequiresSessionAndText(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.session_send")
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("session_send schema parse: %v", err)
	}
	if !sameStrings(schema.Required, []string{"session", "text"}) {
		t.Fatalf("session_send required = %v, want [session text]", schema.Required)
	}
}

func TestMCPSessionQueueCancelSchemaRequiresSessionAndQueueID(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.session_queue_cancel")
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("session_queue_cancel schema parse: %v", err)
	}
	if !sameStrings(schema.Required, []string{"session", "queue_id"}) {
		t.Fatalf("session_queue_cancel required = %v, want [session queue_id]", schema.Required)
	}
	for _, field := range []string{"session", "target", "queue_id"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("session_queue_cancel schema missing %s property", field)
		}
	}
}

// TestMCPSessionPruneSchemaRequiresNothing pins that prune's target is
// optional: omitting it prunes the hub default target.
func TestMCPSessionPruneSchemaRequiresNothing(t *testing.T) {
	tool := findMCPTool(t, amuxTools(), "amux.session_prune")
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("session_prune schema parse: %v", err)
	}
	if len(schema.Required) != 0 {
		t.Fatalf("session_prune required = %v, want none", schema.Required)
	}
	if _, ok := schema.Properties["target"]; !ok {
		t.Error("session_prune schema missing target property")
	}
}

// TestMCPHandleDispatchesCoreSessionTools proves the dispatch switch routes
// each session tool to a real handler: every valid call must reach the injected
// session-client factory and return its error, never "unknown tool".
func TestMCPHandleDispatchesCoreSessionTools(t *testing.T) {
	unavailableErr := useUnavailableMCPSessionClient(t)
	h := &mcpHandler{}
	for _, tc := range []struct {
		name string
		args string
	}{
		{"amux.session_list", `{}`},
		{"amux.session_run", `{"profile":"builtin/codex","name":"s1"}`},
		{"amux.session_send", `{"session":"s1","text":"hi"}`},
		{"amux.session_interrupt", `{"session":"s1"}`},
		{"amux.session_sleep", `{"session":"s1"}`},
		{"amux.session_resume", `{"session":"s1"}`},
		{"amux.session_stop", `{"session":"s1"}`},
		{"amux.session_restart", `{"session":"s1"}`},
		{"amux.session_stream", `{"session":"s1"}`},
		{"amux.session_queue_list", `{"session":"s1"}`},
		{"amux.session_queue_clear", `{"session":"s1"}`},
		{"amux.session_queue_cancel", `{"session":"s1","queue_id":"q1"}`},
		{"amux.session_prune", `{}`},
		{"amux.session_prune", `{"target":"dev"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := h.handle(context.Background(), tc.name, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("handle %s: %v", tc.name, err)
			}
			if !res.IsError {
				t.Fatalf("handle %s succeeded; want injected client error: %s", tc.name, res.Content[0].Text)
			}
			if !strings.Contains(res.Content[0].Text, unavailableErr.Error()) {
				t.Fatalf("handle %s did not reach the session client factory: %s", tc.name, res.Content[0].Text)
			}
			if strings.Contains(res.Content[0].Text, "unknown tool") {
				t.Fatalf("handle %s routed to unknown tool: %s", tc.name, res.Content[0].Text)
			}
		})
	}
}

// TestMCPHandleSessionToolsValidateBeforeDial pins the argument checks that
// run before the hub dial, so they hold even with the hub down.
func TestMCPHandleSessionToolsValidateBeforeDial(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	t.Setenv("AMUX_HUB_ADDR", "unix:///nonexistent-amux-session-tools-test.sock")
	h := &mcpHandler{}
	for _, tc := range []struct {
		name    string
		args    string
		wantMsg string
	}{
		{"amux.session_run", `{"profile":"builtin/codex"}`, "name is required"},
		{"amux.session_run", `{"name":"s1"}`, "profile is required"},
		{"amux.session_send", `{"text":"hi"}`, "session is required"},
		{"amux.session_send", `{"session":"s1"}`, "text is required"},
		{"amux.session_interrupt", `{}`, "session is required"},
		{"amux.session_stream", `{}`, "session is required"},
		{"amux.session_queue_list", `{}`, "session is required"},
		{"amux.session_queue_clear", `{}`, "session is required"},
		{"amux.session_queue_cancel", `{}`, "session is required"},
		{"amux.session_queue_cancel", `{"session":"s1"}`, "queue_id is required"},
	} {
		t.Run(tc.name+tc.wantMsg, func(t *testing.T) {
			res, err := h.handle(context.Background(), tc.name, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("handle %s: %v", tc.name, err)
			}
			if !res.IsError || !strings.Contains(res.Content[0].Text, tc.wantMsg) {
				t.Fatalf("handle %s = %v, want error containing %q", tc.name, res, tc.wantMsg)
			}
		})
	}
}

// fakeAttachStream plays scripted events with optional delays, then blocks
// until ctx is done, mirroring an Attach stream that has gone quiet.
type fakeAttachStream struct {
	ctx    context.Context
	events []fakeAttachItem
}

type fakeAttachItem struct {
	delay time.Duration
	ev    *pb.AttachEvent
	err   error
}

func (f *fakeAttachStream) Recv() (*pb.AttachEvent, error) {
	if len(f.events) == 0 {
		<-f.ctx.Done()
		return nil, f.ctx.Err()
	}
	item := f.events[0]
	f.events = f.events[1:]
	if item.delay > 0 {
		select {
		case <-time.After(item.delay):
		case <-f.ctx.Done():
			return nil, f.ctx.Err()
		}
	}
	return item.ev, item.err
}

func TestDrainSessionTranscriptAggregatesUntilIdle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := &fakeAttachStream{ctx: ctx, events: []fakeAttachItem{
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("hello ")}},
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("world")}},
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT}},
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_STATE_CHANGE, State: "turn_done"}},
	}}
	transcript, state, truncated, err := drainSessionTranscript(ctx, stream, 64<<10, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if transcript != "hello world" {
		t.Errorf("transcript = %q, want %q", transcript, "hello world")
	}
	if state != "turn_done" {
		t.Errorf("state = %q, want turn_done", state)
	}
	if truncated {
		t.Error("truncated = true, want false")
	}
}

func TestDrainSessionTranscriptReturnsOnStreamEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := &fakeAttachStream{ctx: ctx, events: []fakeAttachItem{
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("x")}},
		{err: io.EOF},
	}}
	transcript, _, truncated, err := drainSessionTranscript(ctx, stream, 64<<10, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if transcript != "x" || truncated {
		t.Errorf("transcript = %q truncated = %v, want %q false", transcript, truncated, "x")
	}
}

func TestDrainSessionTranscriptCapsTranscript(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := &fakeAttachStream{ctx: ctx, events: []fakeAttachItem{
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("ab")}},
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("cdef")}},
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("gh")}},
	}}
	transcript, _, truncated, err := drainSessionTranscript(ctx, stream, 4, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if transcript != "abcd" {
		t.Errorf("transcript = %q, want %q", transcript, "abcd")
	}
	if !truncated {
		t.Error("truncated = false, want true")
	}
}

// TestDrainSessionTranscriptHeartbeatDoesNotExtendIdle pins that keepalive
// heartbeats are not activity: a stream that goes quiet after one heartbeat
// still ends the drain at the idle grace, and the later live output is left
// for the next call.
func TestDrainSessionTranscriptHeartbeatDoesNotExtendIdle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := &fakeAttachStream{ctx: ctx, events: []fakeAttachItem{
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("a")}},
		{delay: 80 * time.Millisecond, ev: &pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT}},
		{delay: 80 * time.Millisecond, ev: &pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("b")}},
	}}
	transcript, _, _, err := drainSessionTranscript(ctx, stream, 64<<10, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if transcript != "a" {
		t.Errorf("transcript = %q, want %q (heartbeat must not extend the idle grace)", transcript, "a")
	}
}

func TestDrainSessionTranscriptPropagatesStreamError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	boom := errors.New("boom")
	stream := &fakeAttachStream{ctx: ctx, events: []fakeAttachItem{
		{ev: &pb.AttachEvent{Kind: pb.AttachEvent_OUTPUT, Data: []byte("x")}},
		{err: boom},
	}}
	_, _, _, err := drainSessionTranscript(ctx, stream, 64<<10, 50*time.Millisecond)
	if !errors.Is(err, boom) {
		t.Fatalf("drain err = %v, want %v", err, boom)
	}
}

func TestDrainSessionTranscriptHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	// No events at all: the drain must return what it has at the deadline
	// rather than block on Recv forever.
	stream := &fakeAttachStream{ctx: ctx}
	transcript, _, truncated, err := drainSessionTranscript(ctx, stream, 64<<10, time.Hour)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if transcript != "" || truncated {
		t.Errorf("transcript = %q truncated = %v, want empty false", transcript, truncated)
	}
}
