package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

type fakeScheduleDeliveryHub struct {
	pb.AmuxdClient
	createReq *pb.CreateScheduleRequest
	updateReq *pb.UpdateScheduleRequest
	item      *pb.ScheduleItem
}

func (f *fakeScheduleDeliveryHub) CreateSchedule(_ context.Context, req *pb.CreateScheduleRequest, _ ...grpc.CallOption) (*pb.CreateScheduleResponse, error) {
	f.createReq = req
	target := req.DeliveryTarget
	if target == "" {
		target = schedule.DeliveryTargetOrigin
	}
	policy := req.DeliveryPolicy
	if policy == "" {
		policy = schedule.DeliveryPolicyAlways
	}
	return &pb.CreateScheduleResponse{
		Id:             77,
		NextRunAt:      1800000000,
		DeliveryTarget: target,
		DeliveryPolicy: policy,
	}, nil
}

func (f *fakeScheduleDeliveryHub) UpdateSchedule(_ context.Context, req *pb.UpdateScheduleRequest, _ ...grpc.CallOption) (*pb.UpdateScheduleResponse, error) {
	f.updateReq = req
	dt := schedule.DeliveryTargetNone
	if req.DeliveryTarget != nil {
		dt = *req.DeliveryTarget
	}
	dp := schedule.DeliveryPolicyAlways
	if req.DeliveryPolicy != nil {
		dp = *req.DeliveryPolicy
	}
	return &pb.UpdateScheduleResponse{
		Schedule: &pb.ScheduleItem{
			Id:             req.Id,
			Name:           "updated",
			Spec:           "daily 09:00",
			DeliveryTarget: dt,
			DeliveryPolicy: dp,
			NextRunAt:      1800000000,
		},
	}, nil
}

func (f *fakeScheduleDeliveryHub) GetSchedule(_ context.Context, _ *pb.GetScheduleRequest, _ ...grpc.CallOption) (*pb.GetScheduleResponse, error) {
	return &pb.GetScheduleResponse{Schedule: f.item}, nil
}

func TestMCPScheduleCreateInheritsSessionAgentAndModel(t *testing.T) {
	hub := &fakeScheduleDeliveryHub{}
	origClient := newScheduleClientForMCP
	origResolver := newScheduleSessionResolverForMCP
	defer func() {
		newScheduleClientForMCP = origClient
		newScheduleSessionResolverForMCP = origResolver
	}()
	newScheduleClientForMCP = func() (pb.AmuxdClient, func() error, error) {
		return hub, func() error { return nil }, nil
	}
	newScheduleSessionResolverForMCP = func() (*sessionResolver, error) {
		return resolverForTests([]targetSessions{{
			target: targetRef{ID: "local", Name: "local"},
			sessions: []*sessionEntry{{
				Session: pb.Session{
					Id:             "sess-live",
					Name:           "work",
					AgentType:      "claude",
					AgentProfile:   "builtin/claude",
					EffectiveModel: "claude-sonnet-4-6",
				},
			}},
		}}), nil
	}
	t.Setenv("AMUX_SESSION_ID", "sess-live")

	h := &mcpHandler{}

	// Case 1: omit task.agent and task.model -> inherit both from live session.
	res, err := h.createSchedule(context.Background(), json.RawMessage(`{
		"name": "daily-summary",
		"spec": "daily 09:00",
		"delivery_policy": "on_non_empty",
		"task": {"task": "summarize open blockers"}
	}`))
	if err != nil || res.IsError {
		t.Fatalf("createSchedule case 1 failed: err=%v res=%+v", err, res)
	}
	var spec task.Spec
	if err := json.Unmarshal(hub.createReq.TaskSpecJson, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Agent != "builtin/claude" || spec.Model != "claude-sonnet-4-6" {
		t.Fatalf("inherited spec = (%q, %q), want (builtin/claude, claude-sonnet-4-6)", spec.Agent, spec.Model)
	}
	if hub.createReq.DeliveryPolicy != schedule.DeliveryPolicyOnNonEmpty {
		t.Fatalf("delivery_policy = %q, want on_non_empty", hub.createReq.DeliveryPolicy)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	if out["execution_mode"] != "isolated" || out["delivery_target"] != "origin" || out["agent"] != "builtin/claude" || out["model"] != "claude-sonnet-4-6" {
		t.Fatalf("unexpected MCP create response: %+v", out)
	}

	// Case 2: explicit override to a different specialist profile -> do NOT inherit incompatible model.
	res2, err := h.createSchedule(context.Background(), json.RawMessage(`{
		"spec": "daily 10:00",
		"task": {"agent": "builtin/codex", "task": "run checks"}
	}`))
	if err != nil || res2.IsError {
		t.Fatalf("createSchedule case 2 failed: err=%v res=%+v", err, res2)
	}
	var spec2 task.Spec
	if err := json.Unmarshal(hub.createReq.TaskSpecJson, &spec2); err != nil {
		t.Fatal(err)
	}
	if spec2.Agent != "builtin/codex" || spec2.Model != "" {
		t.Fatalf("cross-profile spec = (%q, %q), want (builtin/codex, empty model)", spec2.Agent, spec2.Model)
	}
}

func TestCLIScheduleUpdateDeliveryFlagsAndJSON(t *testing.T) {
	hub := &fakeScheduleDeliveryHub{}
	orig := newScheduleClient
	defer func() { newScheduleClient = orig }()
	newScheduleClient = func() (pb.AmuxdClient, func() error, error) {
		return hub, func() error { return nil }, nil
	}

	cmd := newRootCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"schedule", "update", "77", "--delivery-target", "creator_dm", "--delivery-policy", "on_failure", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("schedule update failed: %v", err)
	}
	if hub.updateReq == nil || hub.updateReq.DeliveryTarget == nil || *hub.updateReq.DeliveryTarget != "creator_dm" {
		t.Fatalf("updateReq delivery_target = %+v", hub.updateReq)
	}
	if hub.updateReq.DeliveryPolicy == nil || *hub.updateReq.DeliveryPolicy != "on_failure" {
		t.Fatalf("updateReq delivery_policy = %+v", hub.updateReq)
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["delivery_target"] != "creator_dm" || out["delivery_policy"] != "on_failure" {
		t.Fatalf("JSON output = %+v", out)
	}
}

func TestConsoleScheduleCreateAndUpdateForwardsDelivery(t *testing.T) {
	hub := &fakeScheduleDeliveryHub{}
	backend := &consoleBackend{hub: hub}

	_, err := backend.executeResource(context.Background(), "schedules", "create", json.RawMessage(`{
		"schedule_type": "subtask",
		"spec": "daily 09:00",
		"timezone": "Asia/Shanghai",
		"name": "brief",
		"delivery_target": "origin",
		"delivery_policy": "on_non_empty",
		"task": {"agent": "builtin/claude", "task": "check metrics"}
	}`))
	if err != nil {
		t.Fatalf("console create failed: %v", err)
	}
	if hub.createReq.DeliveryTarget != "origin" || hub.createReq.DeliveryPolicy != "on_non_empty" || hub.createReq.Timezone != "Asia/Shanghai" {
		t.Fatalf("console create forwarded wrong fields: %+v", hub.createReq)
	}

	_, err = backend.executeResource(context.Background(), "schedules", "update", json.RawMessage(`{
		"schedule_id": 77,
		"delivery_target": "creator_dm",
		"delivery_policy": "on_failure"
	}`))
	if err != nil {
		t.Fatalf("console update failed: %v", err)
	}
	if hub.updateReq.DeliveryTarget == nil || *hub.updateReq.DeliveryTarget != "creator_dm" || hub.updateReq.DeliveryPolicy == nil || *hub.updateReq.DeliveryPolicy != "on_failure" {
		t.Fatalf("console update forwarded wrong fields: %+v", hub.updateReq)
	}
}

func TestMCPListSchedulesIncludesCreatedAtAndDeliveryFields(t *testing.T) {
	view, err := listSchedulesMCPView([]*pb.ScheduleItem{{
		Id:             42,
		Name:           "daily-check",
		ScheduleType:   schedule.ScheduleTypeSubtask,
		Spec:           "daily 09:00",
		DeliveryTarget: schedule.DeliveryTargetOrigin,
		DeliveryPolicy: schedule.DeliveryPolicyOnNonEmpty,
		CreatedAt:      1795590000,
		Enabled:        true,
	}}, "")
	if err != nil {
		t.Fatalf("listSchedulesMCPView: %v", err)
	}
	rows := view["schedules"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["created_at"] != int64(1795590000) || rows[0]["created_at_iso"] == "" {
		t.Fatalf("missing created_at provenance in MCP list: %+v", rows[0])
	}
	if rows[0]["delivery_target"] != "origin" || rows[0]["delivery_policy"] != "on_non_empty" || rows[0]["execution_mode"] != "isolated" {
		t.Fatalf("unexpected delivery/mode fields: %+v", rows[0])
	}
}

func TestScheduleDeliveryTargetHidesCreatorDMInMCPAndCompletion(t *testing.T) {
	values, _ := completeScheduleDeliveryTargets(nil, nil, "")
	for _, val := range values {
		if strings.HasPrefix(val, schedule.DeliveryTargetCreatorDM+"\t") || val == schedule.DeliveryTargetCreatorDM {
			t.Fatalf("shell completion should hide creator_dm: %+v", values)
		}
	}
	for _, toolName := range []string{"amux.schedule_create", "amux.schedule_update"} {
		tool := findMCPTool(t, amuxTools(), toolName)
		if strings.Contains(string(tool.InputSchema), `"creator_dm"`) {
			t.Fatalf("%s schema should hide creator_dm: %s", toolName, string(tool.InputSchema))
		}
	}
}
