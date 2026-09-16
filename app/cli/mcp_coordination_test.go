package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/mcp"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

func TestMCPToolsIncludeCoordinationSurface(t *testing.T) {
	for _, name := range []string{
		"amux.collaborator_list",
		"amux.delegation_create",
		"amux.delegation_get",
		"amux.delegation_wait",
		"amux.delegation_intervene",
		"amux.delegation_cancel",
	} {
		tool := findMCPTool(t, amuxTools(), name)
		if !json.Valid(tool.InputSchema) {
			t.Errorf("%s has invalid input schema: %s", name, tool.InputSchema)
		}
	}
}

func TestMCPCoordinationToolsExposeCoordinatorSafetyContract(t *testing.T) {
	list := findMCPTool(t, amuxTools(), "amux.collaborator_list")
	for _, want := range []string{"external_bot", "当前 Session", "飞书群", "实际委派", "能力发现请使用 amux.profile_list"} {
		if !strings.Contains(list.Description, want) {
			t.Errorf("collaborator_list contract missing %q", want)
		}
	}
	if !strings.Contains(string(list.InputSchema), `"kind"`) {
		t.Errorf("collaborator_list schema missing kind: %s", list.InputSchema)
	}
	create := findMCPTool(t, amuxTools(), "amux.delegation_create")
	for _, want := range []string{"创建预算", "默认最多 2 次", "硬上限", "external_untrusted", "明确要求或确认", "当前 Session"} {
		if !strings.Contains(create.Description, want) && !strings.Contains(string(create.InputSchema), want) {
			t.Errorf("delegation_create contract missing %q", want)
		}
	}

	wait := findMCPTool(t, amuxTools(), "amux.delegation_wait")
	for _, want := range []string{"尝试账本", "实际创建"} {
		if !strings.Contains(wait.Description, want) {
			t.Errorf("delegation_wait contract missing %q", want)
		}
	}
	for _, want := range []string{`"return_on"`, `"event"`, `"actionable"`} {
		if !strings.Contains(string(wait.InputSchema), want) {
			t.Errorf("delegation_wait schema missing %q: %s", want, wait.InputSchema)
		}
	}

	cancel := findMCPTool(t, amuxTools(), "amux.delegation_cancel")
	for _, want := range []string{"停止信号", "不得自动创建替代委派"} {
		if !strings.Contains(cancel.Description, want) {
			t.Errorf("delegation_cancel contract missing %q", want)
		}
	}
}

func TestMCPCoordinationWaitActionableSuppressesProgressUntilTerminal(t *testing.T) {
	deadline := time.Now().Add(time.Minute).UnixMilli()
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{
		{Snapshot: coordinationWaitSnapshot("running", deadline, 2, &pb.CoordinationEvent{Seq: 2, Kind: "output", Status: "running", Content: "working"})},
		{Snapshot: coordinationWaitSnapshot("running", deadline, 3, &pb.CoordinationEvent{Seq: 3, Kind: "status_changed", Status: "running"})},
		{Snapshot: &pb.CoordinationSnapshot{
			Delegation: &pb.CoordinationDelegation{Id: "delegation-1", CollaboratorId: "agent-profile:team/reviewer@abc123", Status: "succeeded", DeadlineUnixMs: deadline, Result: &pb.CoordinationResult{Summary: "done", TrustLevel: "internal"}},
			Events:     []*pb.CoordinationEvent{{Seq: 4, Kind: "result", Status: "succeeded", Result: &pb.CoordinationResult{Summary: "done", TrustLevel: "internal"}}},
			NextSeq:    4,
		}},
	}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"timeout_seconds":5,"return_on":"actionable"}`))
	if err != nil || result.IsError {
		t.Fatalf("wait actionable = %#v err=%v", result, err)
	}
	if len(fake.waitRequests) != 3 {
		t.Fatalf("wait calls = %d, want 3", len(fake.waitRequests))
	}
	for i, wantSeq := range []uint64{1, 2, 3} {
		if request := fake.waitRequests[i]; request.FromSeq != wantSeq || request.TimeoutSeconds != 5 {
			t.Fatalf("wait request %d = %#v, want from_seq=%d timeout=5", i, request, wantSeq)
		}
	}
	view := decodeMCPTextView(t, result)
	if view["status"] != "succeeded" || view["next_seq"] != float64(4) || view["wait_timed_out"] != false || view["suppressed_event_count"] != float64(2) {
		t.Fatalf("wait actionable view = %#v", view)
	}
	events, ok := view["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("wait actionable events = %#v", view["events"])
	}
	event, ok := events[0].(map[string]any)
	if !ok || event["kind"] != "result" {
		t.Fatalf("wait actionable event = %#v", events[0])
	}
}

func TestMCPCoordinationWaitActionableReturnsPermissionImmediately(t *testing.T) {
	deadline := time.Now().Add(time.Minute).UnixMilli()
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{
		{Snapshot: coordinationWaitSnapshot("waiting", deadline, 2, &pb.CoordinationEvent{Seq: 2, Kind: "status_changed", Status: "waiting"})},
		{Snapshot: coordinationWaitSnapshot("waiting", deadline, 3, &pb.CoordinationEvent{Seq: 3, Kind: "permission_requested", Status: "waiting", Permission: &pb.CoordinationPermissionRequest{Id: "permission-1", Summary: "run tests"}})},
	}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"return_on":"actionable"}`))
	if err != nil || result.IsError {
		t.Fatalf("wait permission = %#v err=%v", result, err)
	}
	view := decodeMCPTextView(t, result)
	events, ok := view["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("wait permission events = %#v", view["events"])
	}
	event, ok := events[0].(map[string]any)
	if !ok {
		t.Fatalf("wait permission event = %#v", events[0])
	}
	permission, ok := event["permission"].(map[string]any)
	if !ok || len(fake.waitRequests) != 2 || permission["request_id"] != "permission-1" || view["suppressed_event_count"] != float64(1) {
		t.Fatalf("wait permission view = %#v requests=%d", view, len(fake.waitRequests))
	}
}

func TestMCPCoordinationWaitActionableHidesInternalPollTimeout(t *testing.T) {
	deadline := time.Now().Add(time.Minute).UnixMilli()
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{
		{Snapshot: coordinationWaitSnapshot("running", deadline, 1, nil), TimedOut: true},
		{Snapshot: &pb.CoordinationSnapshot{Delegation: &pb.CoordinationDelegation{Id: "delegation-1", Status: "succeeded", DeadlineUnixMs: deadline, Result: &pb.CoordinationResult{Summary: "done"}}, NextSeq: 1}},
	}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"return_on":"actionable"}`))
	if err != nil || result.IsError {
		t.Fatalf("wait across poll timeout = %#v err=%v", result, err)
	}
	view := decodeMCPTextView(t, result)
	if len(fake.waitRequests) != 2 || view["status"] != "succeeded" || view["wait_timed_out"] != false {
		t.Fatalf("wait across poll timeout view = %#v requests=%d", view, len(fake.waitRequests))
	}
}

func TestMCPCoordinationWaitActionableSuppressesOutputOnTerminalSnapshot(t *testing.T) {
	deadline := time.Now().Add(time.Minute).UnixMilli()
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{{Snapshot: &pb.CoordinationSnapshot{
		Delegation: &pb.CoordinationDelegation{Id: "delegation-1", Status: "succeeded", DeadlineUnixMs: deadline, Result: &pb.CoordinationResult{Summary: "done"}},
		Events:     []*pb.CoordinationEvent{{Seq: 2, Kind: "output", Status: "running", Content: "final transcript chunk"}},
		NextSeq:    2,
	}}}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"return_on":"actionable"}`))
	if err != nil || result.IsError {
		t.Fatalf("wait terminal output = %#v err=%v", result, err)
	}
	view := decodeMCPTextView(t, result)
	events, ok := view["events"].([]any)
	if !ok || len(events) != 0 || view["status"] != "succeeded" || view["suppressed_event_count"] != float64(1) || strings.Contains(result.Content[0].Text, "final transcript chunk") {
		t.Fatalf("wait terminal output view = %#v", view)
	}
}

func TestMCPCoordinationWaitActionableRejectsMissingPermissionEvent(t *testing.T) {
	deadline := time.Now().Add(time.Minute).UnixMilli()
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{
		{Snapshot: coordinationWaitSnapshot("waiting", deadline, 2, &pb.CoordinationEvent{Seq: 2, Kind: "status_changed", Status: "waiting"})},
		{Snapshot: coordinationWaitSnapshot("waiting", deadline, 2, nil), TimedOut: true},
	}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"return_on":"actionable"}`))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].Text, "waiting for permission") {
		t.Fatalf("missing permission wait = %#v err=%v", result, err)
	}
}

func TestMCPCoordinationWaitActionableStopsAtDelegationDeadline(t *testing.T) {
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{{
		Snapshot: coordinationWaitSnapshot("running", time.Now().Add(-time.Second).UnixMilli(), 1, nil), TimedOut: true,
	}}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"return_on":"actionable"}`))
	if err != nil || result.IsError {
		t.Fatalf("wait deadline = %#v err=%v", result, err)
	}
	view := decodeMCPTextView(t, result)
	if len(fake.waitRequests) != 1 || view["status"] != "running" || view["wait_timed_out"] != true {
		t.Fatalf("wait deadline view = %#v requests=%d", view, len(fake.waitRequests))
	}
}

func TestMCPCoordinationWaitActionableRejectsNonAdvancingResponse(t *testing.T) {
	deadline := time.Now().Add(time.Minute).UnixMilli()
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{{Snapshot: coordinationWaitSnapshot("running", deadline, 1, nil)}}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"return_on":"actionable"}`))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].Text, "without an event, terminal state, or timeout") {
		t.Fatalf("non-advancing wait = %#v err=%v", result, err)
	}
}

func TestMCPCoordinationWaitActionableRejectsCursorAdvanceWithoutEvent(t *testing.T) {
	deadline := time.Now().Add(time.Minute).UnixMilli()
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{{Snapshot: coordinationWaitSnapshot("running", deadline, 2, nil)}}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"return_on":"actionable"}`))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].Text, "without an event, terminal state, or timeout") {
		t.Fatalf("cursor-only wait = %#v err=%v", result, err)
	}
}

func TestMCPCoordinationWaitActionableRejectsInconsistentEventCursor(t *testing.T) {
	deadline := time.Now().Add(time.Minute).UnixMilli()
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{{
		Snapshot: coordinationWaitSnapshot("running", deadline, 2, &pb.CoordinationEvent{Seq: 3, Kind: "output", Content: "working"}),
	}}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"return_on":"actionable"}`))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].Text, "inconsistent with the delegation cursor") {
		t.Fatalf("inconsistent event cursor wait = %#v err=%v", result, err)
	}
}

func TestMCPCoordinationWaitActionableRejectsMissingDeadline(t *testing.T) {
	fake := &fakeCoordinationClient{waitResponses: []*pb.WaitDelegationResponse{{
		Snapshot: coordinationWaitSnapshot("running", 0, 1, nil), TimedOut: true,
	}}}
	withCoordinationClient(t, fake)

	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"return_on":"actionable"}`))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].Text, "without a deadline") {
		t.Fatalf("missing deadline wait = %#v err=%v", result, err)
	}
}

func TestMCPCoordinationWaitActionablePropagatesCancellation(t *testing.T) {
	fake := &fakeCoordinationClient{waitFn: func(ctx context.Context, _ *pb.WaitDelegationRequest) (*pb.WaitDelegationResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	withCoordinationClient(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := (&mcpHandler{}).handle(ctx, "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","return_on":"actionable"}`))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].Text, context.Canceled.Error()) {
		t.Fatalf("cancelled wait = %#v err=%v", result, err)
	}
}

func TestMCPCoordinationWaitRejectsUnknownReturnPolicy(t *testing.T) {
	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","return_on":"terminal"}`))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].Text, "return_on must be event or actionable") {
		t.Fatalf("unknown return policy = %#v err=%v", result, err)
	}
}

func TestMCPCoordinationWaitRejectsTimeoutAboveBackendMaximum(t *testing.T) {
	result, err := (&mcpHandler{}).handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","timeout_seconds":31}`))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].Text, "must not exceed 30") {
		t.Fatalf("oversized wait timeout = %#v err=%v", result, err)
	}
}

func TestMCPCoordinationDelegatesAndWaitsWithOpaqueIDs(t *testing.T) {
	t.Setenv("AMUX_SESSION_ID", "session-main")
	fake := &fakeCoordinationClient{
		delegateResponse: &pb.DelegateResponse{
			Created: true,
			Snapshot: &pb.CoordinationSnapshot{
				Delegation: &pb.CoordinationDelegation{Id: "delegation-1", CollaboratorId: "agent-profile:team/reviewer@abc123", Status: "pending"},
				NextSeq:    1,
			},
		},
		waitResponse: &pb.WaitDelegationResponse{
			Snapshot: &pb.CoordinationSnapshot{
				Delegation: &pb.CoordinationDelegation{Id: "delegation-1", CollaboratorId: "agent-profile:team/reviewer@abc123", Status: "succeeded", Result: &pb.CoordinationResult{Summary: "review complete", TrustLevel: "internal"}},
				Events:     []*pb.CoordinationEvent{{Seq: 2, Kind: "status_changed", Status: "running"}, {Seq: 3, Kind: "result", Status: "succeeded"}},
				NextSeq:    3,
			},
		},
	}
	withCoordinationClient(t, fake)
	handler := &mcpHandler{}
	delegated, err := handler.handle(context.Background(), "amux.delegation_create", json.RawMessage(`{"collaborator_id":"agent-profile:team/reviewer@abc123","task":"review this"}`))
	if err != nil || delegated.IsError {
		t.Fatalf("delegate = %#v err=%v", delegated, err)
	}
	if fake.delegateRequest.CollaboratorId != "agent-profile:team/reviewer@abc123" || fake.delegateRequest.Input != "review this" {
		t.Fatalf("delegate request = %#v", fake.delegateRequest)
	}
	if fake.delegateRequest.InitiatorRef != "amux-session:session-main" || !strings.HasPrefix(fake.delegateRequest.ScopeRef, "amux-session:session-main:coordination:") || fake.delegateRequest.IdempotencyKey == "" {
		t.Fatalf("delegate provenance = %#v", fake.delegateRequest)
	}
	var delegatedView map[string]any
	if err := json.Unmarshal([]byte(delegated.Content[0].Text), &delegatedView); err != nil {
		t.Fatalf("decode delegate result: %v", err)
	}
	if delegatedView["delegation_id"] != "delegation-1" || delegatedView["status"] != "pending" {
		t.Fatalf("delegate result = %#v", delegatedView)
	}

	waited, err := handler.handle(context.Background(), "amux.delegation_wait", json.RawMessage(`{"delegation_id":"delegation-1","from_seq":1,"timeout_seconds":5}`))
	if err != nil || waited.IsError {
		t.Fatalf("wait = %#v err=%v", waited, err)
	}
	if fake.waitRequest.DelegationId != "delegation-1" || fake.waitRequest.FromSeq != 1 || fake.waitRequest.TimeoutSeconds != 5 {
		t.Fatalf("wait request = %#v", fake.waitRequest)
	}
	var waitedView map[string]any
	if err := json.Unmarshal([]byte(waited.Content[0].Text), &waitedView); err != nil {
		t.Fatalf("decode wait result: %v", err)
	}
	if waitedView["status"] != "succeeded" || waitedView["next_seq"] != float64(3) || waitedView["wait_timed_out"] != false {
		t.Fatalf("wait result = %#v", waitedView)
	}
}

func TestMCPCoordinationListsIntervenesAndCancels(t *testing.T) {
	t.Setenv("AMUX_SESSION_ID", "session-current-group")
	fake := &fakeCoordinationClient{
		listResponse: &pb.ListCollaboratorsResponse{Collaborators: []*pb.CollaboratorDescriptor{
			{Id: "agent-profile:team/reviewer@abc123", Name: "reviewer", Kind: "internal_agent", Availability: "configured", Capabilities: []string{"progress"}, Skills: []string{"code-review"}},
			{Id: "external-bot:docman", Name: "docman", Kind: "external_bot", Availability: "best_effort"},
		}},
		interveneResponse: &pb.InterveneDelegationResponse{Accepted: true, Delegation: &pb.CoordinationDelegation{Id: "delegation-1", Status: "waiting"}},
		cancelResponse:    &pb.CancelDelegationResponse{Accepted: true, Delegation: &pb.CoordinationDelegation{Id: "delegation-1", Status: "waiting"}},
	}
	withCoordinationClient(t, fake)
	handler := &mcpHandler{}

	listed, err := handler.handle(context.Background(), "amux.collaborator_list", json.RawMessage(`{}`))
	if err != nil || listed.IsError || !strings.Contains(listed.Content[0].Text, "agent-profile:team/reviewer@abc123") {
		t.Fatalf("list = %#v err=%v", listed, err)
	}
	if got := fake.listRequest.GetInitiatorRef(); got != "amux-session:session-current-group" {
		t.Fatalf("list initiator_ref = %q, want current session provenance", got)
	}
	external, err := handler.handle(context.Background(), "amux.collaborator_list", json.RawMessage(`{"kind":"external_bot"}`))
	if err != nil || external.IsError || !strings.Contains(external.Content[0].Text, "external-bot:docman") || strings.Contains(external.Content[0].Text, "agent-profile:team/reviewer") {
		t.Fatalf("external-only list = %#v err=%v", external, err)
	}
	invalid, err := handler.handle(context.Background(), "amux.collaborator_list", json.RawMessage(`{"kind":"bot"}`))
	if err != nil || !invalid.IsError || !strings.Contains(invalid.Content[0].Text, "internal_agent or external_bot") {
		t.Fatalf("invalid kind = %#v err=%v", invalid, err)
	}
	intervened, err := handler.handle(context.Background(), "amux.delegation_intervene", json.RawMessage(`{"delegation_id":"delegation-1","request_id":"permission-1","decision":"allow","option_id":"allow-once"}`))
	if err != nil || intervened.IsError || fake.interveneRequest.Decision != "allow" || fake.interveneRequest.RequestId != "permission-1" {
		t.Fatalf("intervene = %#v request=%#v err=%v", intervened, fake.interveneRequest, err)
	}
	cancelled, err := handler.handle(context.Background(), "amux.delegation_cancel", json.RawMessage(`{"delegation_id":"delegation-1"}`))
	if err != nil || cancelled.IsError || fake.cancelRequest.DelegationId != "delegation-1" {
		t.Fatalf("cancel = %#v request=%#v err=%v", cancelled, fake.cancelRequest, err)
	}
}

func withCoordinationClient(t *testing.T, client pb.CoordinationServiceClient) {
	t.Helper()
	original := newCoordinationClient
	newCoordinationClient = func() (pb.CoordinationServiceClient, func() error, error) {
		return client, func() error { return nil }, nil
	}
	t.Cleanup(func() { newCoordinationClient = original })
}

type fakeCoordinationClient struct {
	listResponse      *pb.ListCollaboratorsResponse
	delegateResponse  *pb.DelegateResponse
	waitResponse      *pb.WaitDelegationResponse
	waitResponses     []*pb.WaitDelegationResponse
	waitFn            func(context.Context, *pb.WaitDelegationRequest) (*pb.WaitDelegationResponse, error)
	waitErr           error
	getResponse       *pb.GetDelegationResponse
	interveneResponse *pb.InterveneDelegationResponse
	cancelResponse    *pb.CancelDelegationResponse
	listRequest       *pb.ListCollaboratorsRequest
	delegateRequest   *pb.DelegateRequest
	waitRequest       *pb.WaitDelegationRequest
	waitRequests      []*pb.WaitDelegationRequest
	getRequest        *pb.GetDelegationRequest
	interveneRequest  *pb.InterveneDelegationRequest
	cancelRequest     *pb.CancelDelegationRequest
}

func (f *fakeCoordinationClient) ListCollaborators(_ context.Context, request *pb.ListCollaboratorsRequest, _ ...grpc.CallOption) (*pb.ListCollaboratorsResponse, error) {
	f.listRequest = request
	return f.listResponse, nil
}

func (f *fakeCoordinationClient) Delegate(_ context.Context, request *pb.DelegateRequest, _ ...grpc.CallOption) (*pb.DelegateResponse, error) {
	f.delegateRequest = request
	return f.delegateResponse, nil
}

func (f *fakeCoordinationClient) GetDelegation(_ context.Context, request *pb.GetDelegationRequest, _ ...grpc.CallOption) (*pb.GetDelegationResponse, error) {
	f.getRequest = request
	return f.getResponse, nil
}

func (f *fakeCoordinationClient) WaitDelegation(ctx context.Context, request *pb.WaitDelegationRequest, _ ...grpc.CallOption) (*pb.WaitDelegationResponse, error) {
	f.waitRequest = request
	f.waitRequests = append(f.waitRequests, request)
	if f.waitFn != nil {
		return f.waitFn(ctx, request)
	}
	if len(f.waitResponses) > 0 {
		response := f.waitResponses[0]
		f.waitResponses = f.waitResponses[1:]
		return response, nil
	}
	if f.waitErr != nil {
		return nil, f.waitErr
	}
	if f.waitResponse == nil {
		return nil, errors.New("fake coordination wait response exhausted")
	}
	return f.waitResponse, nil
}

func (f *fakeCoordinationClient) InterveneDelegation(_ context.Context, request *pb.InterveneDelegationRequest, _ ...grpc.CallOption) (*pb.InterveneDelegationResponse, error) {
	f.interveneRequest = request
	return f.interveneResponse, nil
}

func (f *fakeCoordinationClient) CancelDelegation(_ context.Context, request *pb.CancelDelegationRequest, _ ...grpc.CallOption) (*pb.CancelDelegationResponse, error) {
	f.cancelRequest = request
	return f.cancelResponse, nil
}

func coordinationWaitSnapshot(status string, deadline int64, nextSeq uint64, event *pb.CoordinationEvent) *pb.CoordinationSnapshot {
	events := []*pb.CoordinationEvent(nil)
	if event != nil {
		events = []*pb.CoordinationEvent{event}
	}
	return &pb.CoordinationSnapshot{
		Delegation: &pb.CoordinationDelegation{Id: "delegation-1", CollaboratorId: "agent-profile:team/reviewer@abc123", Status: status, DeadlineUnixMs: deadline},
		Events:     events,
		NextSeq:    nextSeq,
	}
}

func decodeMCPTextView(t *testing.T, result mcp.ToolResult) map[string]any {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("MCP result content = %#v, want one text item", result.Content)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &view); err != nil {
		t.Fatalf("decode MCP result: %v\n%s", err, result.Content[0].Text)
	}
	return view
}
