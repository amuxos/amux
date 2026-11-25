package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/gateway"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/webconsole"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func consoleTestOperator(t *testing.T) {
	t.Helper()
	t.Setenv("AMUX_HOME", t.TempDir())
	t.Setenv("AMUX_ACTOR_TOKEN", "")
	t.Setenv("AMUX_ACTOR_TOKEN_FILE", "")
}
func consoleTestJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func consoleTestCode(t *testing.T, err error, want string) {
	t.Helper()
	var resourceErr *consoleResourceError
	if !errors.As(err, &resourceErr) || resourceErr.ConsoleErrorCode() != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}

type consoleResourceHub struct {
	pb.AmuxdClient
	targets        []*pb.Target
	listCalls      int
	run            *pb.AmuxdRunRequest
	submit         *pb.AmuxdSubmitRequest
	permission     *pb.AmuxdRespondPermissionRequest
	queueCancel    *pb.AmuxdQueueCancelRequest
	sleep          *pb.AmuxdSleepRequest
	profile        agentprofile.Profile
	scheduleCreate  *pb.CreateScheduleRequest
	scheduleUpdate  *pb.UpdateScheduleRequest
	scheduleEnable  *pb.SetScheduleEnabledRequest
	scheduleDelete  *pb.DeleteScheduleRequest
	scheduleTrigger *pb.TriggerScheduleRequest
	schedule        *pb.ScheduleItem
	scheduleError  string
	rejected       bool
	botsPayload    string
}

// ConsoleManagement answers the overview's best-effort bot summary. Without a
// configured payload it behaves like a Hub that has no bot bridge.
func (f *consoleResourceHub) ConsoleManagement(_ context.Context, req *pb.ConsoleManagementRequest, _ ...grpc.CallOption) (*pb.ConsoleManagementResponse, error) {
	if f.botsPayload == "" || req.GetResource() != "bots" || req.GetAction() != "list" {
		return nil, status.Error(codes.Unavailable, "bridge unavailable")
	}
	return &pb.ConsoleManagementResponse{PayloadJson: []byte(f.botsPayload)}, nil
}

func (f *consoleResourceHub) ListTargets(context.Context, *pb.ListTargetsRequest, ...grpc.CallOption) (*pb.ListTargetsResponse, error) {
	f.listCalls++
	return &pb.ListTargetsResponse{Targets: f.targets}, nil
}
func (f *consoleResourceHub) GetAgentProfile(context.Context, *pb.GetAgentProfileRequest, ...grpc.CallOption) (*pb.GetAgentProfileResponse, error) {
	return &pb.GetAgentProfileResponse{Profile: &pb.AgentProfileInfo{Profile: agentprofile.ToPBProfileSpec(f.profile)}}, nil
}
func (f *consoleResourceHub) ListAgentProfiles(context.Context, *pb.ListAgentProfilesRequest, ...grpc.CallOption) (*pb.ListAgentProfilesResponse, error) {
	return &pb.ListAgentProfilesResponse{Profiles: []*pb.AgentProfileInfo{{Profile: agentprofile.ToPBProfileSpec(f.profile)}}}, nil
}
func (f *consoleResourceHub) Run(_ context.Context, req *pb.AmuxdRunRequest, _ ...grpc.CallOption) (*pb.RunResponse, error) {
	f.run = req
	return &pb.RunResponse{Session: &pb.Session{Name: req.Name, Env: []string{"TOKEN=run-secret"}, ExtraArgs: []string{"--key=run-secret"}}, Warnings: []string{"api_key=run-secret"}}, nil
}
func (f *consoleResourceHub) Submit(_ context.Context, req *pb.AmuxdSubmitRequest, _ ...grpc.CallOption) (*pb.SubmitResponse, error) {
	f.submit = req
	return &pb.SubmitResponse{Ok: !f.rejected}, nil
}
func (f *consoleResourceHub) Sleep(_ context.Context, req *pb.AmuxdSleepRequest, _ ...grpc.CallOption) (*pb.SleepResponse, error) {
	f.sleep = req
	return &pb.SleepResponse{Ok: !f.rejected}, nil
}
func (f *consoleResourceHub) QueueList(context.Context, *pb.AmuxdQueueListRequest, ...grpc.CallOption) (*pb.QueueListResponse, error) {
	return &pb.QueueListResponse{}, nil
}
func (f *consoleResourceHub) QueueCancel(_ context.Context, req *pb.AmuxdQueueCancelRequest, _ ...grpc.CallOption) (*pb.QueueCancelResponse, error) {
	f.queueCancel = req
	return &pb.QueueCancelResponse{Ok: !f.rejected}, nil
}
func (f *consoleResourceHub) RespondPermission(_ context.Context, req *pb.AmuxdRespondPermissionRequest, _ ...grpc.CallOption) (*pb.RespondPermissionResponse, error) {
	f.permission = req
	return &pb.RespondPermissionResponse{Ok: !f.rejected}, nil
}
func (f *consoleResourceHub) CreateSchedule(_ context.Context, req *pb.CreateScheduleRequest, _ ...grpc.CallOption) (*pb.CreateScheduleResponse, error) {
	f.scheduleCreate = req
	return &pb.CreateScheduleResponse{Id: 7, NextRunAt: 20, Error: f.scheduleError}, nil
}
func (f *consoleResourceHub) UpdateSchedule(_ context.Context, req *pb.UpdateScheduleRequest, _ ...grpc.CallOption) (*pb.UpdateScheduleResponse, error) {
	f.scheduleUpdate = req
	return &pb.UpdateScheduleResponse{Schedule: f.schedule, Error: f.scheduleError}, nil
}
func (f *consoleResourceHub) SetScheduleEnabled(_ context.Context, req *pb.SetScheduleEnabledRequest, _ ...grpc.CallOption) (*pb.SetScheduleEnabledResponse, error) {
	f.scheduleEnable = req
	return &pb.SetScheduleEnabledResponse{Ok: !f.rejected}, nil
}
func (f *consoleResourceHub) DeleteSchedule(_ context.Context, req *pb.DeleteScheduleRequest, _ ...grpc.CallOption) (*pb.DeleteScheduleResponse, error) {
	f.scheduleDelete = req
	return &pb.DeleteScheduleResponse{Ok: !f.rejected}, nil
}
func (f *consoleResourceHub) TriggerSchedule(_ context.Context, req *pb.TriggerScheduleRequest, _ ...grpc.CallOption) (*pb.TriggerScheduleResponse, error) {
	f.scheduleTrigger = req
	if f.scheduleError != "" {
		return &pb.TriggerScheduleResponse{Ok: false, Error: f.scheduleError}, nil
	}
	return &pb.TriggerScheduleResponse{Ok: !f.rejected, ExecutionId: 99, Status: "ok:sent"}, nil
}
func (f *consoleResourceHub) ListSchedules(context.Context, *pb.ListSchedulesRequest, ...grpc.CallOption) (*pb.ListSchedulesResponse, error) {
	return &pb.ListSchedulesResponse{Schedules: []*pb.ScheduleItem{f.schedule}}, nil
}
func (f *consoleResourceHub) GetSchedule(context.Context, *pb.GetScheduleRequest, ...grpc.CallOption) (*pb.GetScheduleResponse, error) {
	return &pb.GetScheduleResponse{Schedule: f.schedule}, nil
}
func consoleTestHub() *consoleResourceHub {
	profile, _ := agentprofile.ResolveBuiltin("builtin/codex")
	return &consoleResourceHub{profile: profile, targets: []*pb.Target{{Id: "runtime-a", Name: "worker", Sessions: []*pb.Session{{Id: "session-id", Name: "chat", State: "running", Env: []string{"TOKEN=stored-secret"}, ExtraArgs: []string{"--api-key=stored-secret"}}}}}}
}

func TestConsoleResourceSessionsRedactWithoutMutatingSource(t *testing.T) {
	consoleTestOperator(t)
	hub := consoleTestHub()
	backend := &consoleBackend{hub: hub}
	for _, resource := range []string{"sessions", "overview", "session"} {
		value, err := backend.snapshotResource(context.Background(), resource, url.Values{"name": {"chat"}, "target": {"runtime-a"}})
		if err != nil {
			t.Fatal(err)
		}
		raw := consoleTestJSON(t, value)
		if strings.Contains(raw, "stored-secret") || strings.Contains(raw, "extra_args") || strings.Contains(raw, `"env"`) {
			t.Fatalf("secret leaked: %s", raw)
		}
		if resource == "session" {
			if !strings.Contains(raw, `"queue":[]`) {
				t.Fatalf("queue shape = %s", raw)
			}
		}
	}
	if hub.targets[0].Sessions[0].Env[0] != "TOKEN=stored-secret" {
		t.Fatal("mutated backend session")
	}
}
func TestConsoleResourceSessionCreateUsesProfileProjection(t *testing.T) {
	consoleTestOperator(t)
	hub := consoleTestHub()
	backend := &consoleBackend{hub: hub}
	value, err := backend.executeResource(context.Background(), "sessions", "create", json.RawMessage(`{"profile":"builtin/codex","name":"new","target":"runtime-a","cwd":"/repo","run_as":"self"}`))
	if err != nil {
		t.Fatal(err)
	}
	if hub.run == nil || hub.run.AgentType != "codex" || hub.run.AgentProfile != "builtin/codex" || hub.run.ProfileSpec == nil || hub.run.Cwd != "/repo" || hub.run.RunAs != "self" {
		t.Fatalf("unprojected request: %v", hub.run)
	}
	if raw := consoleTestJSON(t, value); strings.Contains(raw, "run-secret") {
		t.Fatalf("launch secret leaked: %s", raw)
	}
	if hub.profile.Type != "" || hub.profile.Model != "" || hub.profile.PermissionPolicy != "" {
		t.Fatal("v2 Profile acquired execution configuration")
	}
}
func TestConsoleResourceRejectsIdentityAndInvalidFieldsBeforeCalls(t *testing.T) {
	consoleTestOperator(t)
	hub := consoleTestHub()
	backend := &consoleBackend{hub: hub}
	tests := []struct{ resource, action, data string }{
		{"sessions", "create", `{"name":"new","profile":"builtin/codex","actor_token":"secret"}`},
		{"sessions", "create", `{"name":"new","profile":"builtin/codex","env":["TOKEN=x"]}`},
		{"sessions", "create", `{"profile":"builtin/codex"}`},
		{"sessions", "create", `{"name":"new","profile":"builtin/codex","repo":"/local"}`},
		{"sessions", "submit", `{"name":"chat","text":" "}`},
		{"sessions", "queue-cancel", `{"name":"chat"}`},
		{"sessions", "respond-permission", `{"name":"chat","permission_id":"p"}`},
		{"sessions", "sleep", `{"name":"chat","target":"a","target_id":"b"}`},
		{"tasks", "respond", `{"task_id":"t","permission_id":"p"}`},
		{"schedules", "create", `{"spec":"@every 1h","task":{"agent":"builtin/codex","task":"hello","execution_identity":{}}}`},
		{"schedules", "update", `{"schedule_id":0,"name":"new"}`},
		{"sessions", "submit", `null`},
		{"sessions", "submit", `{} {}`},
		{"sessions", "sleep", `{"name":"chat","text":"ignored"}`},
		{"schedules", "disable", `{"schedule_id":7,"task":{"agent":"builtin/codex","task":"ignored"}}`},
		{"tasks", "cancel", `{"task_id":"t","allow":true}`},
		{"packs", "sync", `{"namespace":"team","url":"https://example.com/ignored"}`},
	}
	for _, tt := range tests {
		t.Run(tt.resource+"-"+tt.action+tt.data, func(t *testing.T) {
			if _, err := backend.executeResource(context.Background(), tt.resource, tt.action, json.RawMessage(tt.data)); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	if hub.listCalls != 0 || hub.run != nil || hub.submit != nil || hub.scheduleCreate != nil {
		t.Fatal("invalid request reached Hub")
	}
}
func TestConsoleResourceSessionMutationRoutingAndRejection(t *testing.T) {
	consoleTestOperator(t)
	hub := consoleTestHub()
	backend := &consoleBackend{hub: hub}
	if _, err := backend.executeResource(context.Background(), "sessions", "submit", json.RawMessage(`{"name":"session-id","target":"worker","text":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	if hub.submit.Name != "chat" || hub.submit.TargetId != "runtime-a" || hub.submit.Source != "console" || !hub.submit.Async {
		t.Fatalf("submit = %v", hub.submit)
	}
	if _, err := backend.executeResource(context.Background(), "sessions", "respond-permission", json.RawMessage(`{"name":"chat","permission_id":"permission-1","option_id":"option-real"}`)); err != nil {
		t.Fatal(err)
	}
	if hub.permission.PermissionId != "permission-1" || hub.permission.OptionId != "option-real" || hub.permission.TargetId != "runtime-a" {
		t.Fatalf("permission = %v", hub.permission)
	}
	if _, err := backend.executeResource(context.Background(), "sessions", "respond-permission", json.RawMessage(`{"name":"chat","permission_id":"p-multi","turn_id":"turn-1","option_ids":["choice-a","choice-b"]}`)); err != nil {
		t.Fatal(err)
	}
	if len(hub.permission.OptionIds) != 2 || hub.permission.TurnId != "turn-1" {
		t.Fatalf("multi-select lost: %v", hub.permission)
	}
	if _, err := backend.executeResource(context.Background(), "sessions", "respond-permission", json.RawMessage(`{"name":"chat","permission_id":"p-cancel","cancel":true}`)); err != nil {
		t.Fatal(err)
	}
	if !hub.permission.Cancel {
		t.Fatal("cancel lost")
	}
	if _, err := backend.executeResource(context.Background(), "sessions", "queue-cancel", json.RawMessage(`{"name":"chat","queue_id":"queue-1"}`)); err != nil {
		t.Fatal(err)
	}
	if hub.queueCancel.Id != "queue-1" {
		t.Fatal("queue id not forwarded")
	}
	hub.rejected = true
	_, err := backend.executeResource(context.Background(), "sessions", "sleep", json.RawMessage(`{"name":"chat"}`))
	consoleTestCode(t, err, "operation_rejected")
}
func TestConsoleResourceAmbiguousSessionAndUnknownResource(t *testing.T) {
	consoleTestOperator(t)
	hub := consoleTestHub()
	hub.targets = append(hub.targets, &pb.Target{Id: "runtime-b", Sessions: []*pb.Session{{Name: "chat"}}})
	backend := &consoleBackend{hub: hub}
	_, err := backend.snapshotResource(context.Background(), "session", url.Values{"name": {"chat"}})
	consoleTestCode(t, err, "ambiguous_session")
	if _, err := backend.snapshotResource(context.Background(), "bots", nil); !errors.Is(err, errConsoleResourceNotFound) {
		t.Fatalf("unknown GET err=%v", err)
	}
	if _, err := backend.executeResource(context.Background(), "settings", "save", json.RawMessage(`{}`)); !errors.Is(err, errConsoleResourceNotFound) {
		t.Fatalf("unknown POST err=%v", err)
	}
	_, err = backend.executeResource(context.Background(), "schedules", "run-now", json.RawMessage(`{"schedule_id":7}`))
	consoleTestCode(t, err, "unsupported_action")
}
func TestConsoleResourceActorCannotReadOperatorLocalData(t *testing.T) {
	consoleTestOperator(t)
	t.Setenv("AMUX_ACTOR_TOKEN", "actor-secret")
	backend := &consoleBackend{}
	for _, resource := range []string{"sessions", "profiles", "packs", "credentials", "catalogs"} {
		_, err := backend.snapshotResource(context.Background(), resource, nil)
		consoleTestCode(t, err, "operator_required")
	}
	_, err := backend.executeResource(context.Background(), "packs", "sync", json.RawMessage(`{"namespace":"team"}`))
	consoleTestCode(t, err, "operator_required")
}
func TestConsoleResourceErrorsNeverRenderBackendSecrets(t *testing.T) {
	original := status.Error(codes.PermissionDenied, "TOKEN=do-not-return --api-key=secret")
	err := consoleOperationError(original)
	consoleTestCode(t, err, "permission_denied")
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "TOKEN") {
		t.Fatal("error leaked backend diagnostic")
	}
	if !errors.Is(err, original) {
		t.Fatal("backend cause was lost")
	}
	err = consoleOperationError(clientop.InvalidError(clientop.FieldAgent, "secret", "expected"))
	consoleTestCode(t, err, "invalid_field")
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("field error leaked input")
	}
	err = consoleOperationError(status.Error(codes.NotFound, "private"))
	if !errors.Is(err, webconsole.ErrNotFound) {
		t.Fatal("not-found status missing")
	}
}
func TestConsoleResourceScheduleValidationForwardingAndErrors(t *testing.T) {
	consoleTestOperator(t)
	hub := consoleTestHub()
	backend := &consoleBackend{hub: hub}
	value, err := backend.executeResource(context.Background(), "schedules", "create", json.RawMessage(`{"name":"daily","spec":"@every 1h","task":{"agent":"builtin/codex","task":"work","runtime":"codex","model":"example","permission_policy":"deny-all"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(consoleTestJSON(t, value), `"schedule_id":7`) {
		t.Fatal("missing schedule id")
	}
	var spec task.Spec
	if err := json.Unmarshal(hub.scheduleCreate.TaskSpecJson, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Runtime != "codex" || spec.Model != "example" || spec.PermissionPolicy != task.PolicyDenyAll || hub.scheduleCreate.CreatedBy != "console" {
		t.Fatalf("schedule request = %v, spec = %+v", hub.scheduleCreate, spec)
	}
	if _, err := backend.executeResource(context.Background(), "schedules", "create", json.RawMessage(`{"schedule_type":"session-prompt","spec":"@every 1h","session_name":"chat","prompt":"hello","run_mode":"reuse-session"}`)); err != nil {
		t.Fatal(err)
	}
	if hub.scheduleCreate.TargetId != "runtime-a" || hub.scheduleCreate.SessionName != "chat" || hub.scheduleCreate.ScheduleType != "session-prompt" {
		t.Fatalf("session schedule = %v", hub.scheduleCreate)
	}
	if _, err := backend.executeResource(context.Background(), "schedules", "update", json.RawMessage(`{"schedule_id":7,"name":"updated"}`)); err != nil {
		t.Fatal(err)
	}
	if hub.scheduleUpdate.GetId() != 7 || hub.scheduleUpdate.GetName() != "updated" || hub.scheduleUpdate.Spec != nil {
		t.Fatalf("update = %v", hub.scheduleUpdate)
	}
	if _, err := backend.executeResource(context.Background(), "schedules", "disable", json.RawMessage(`{"schedule_id":7}`)); err != nil {
		t.Fatal(err)
	}
	if hub.scheduleEnable.GetEnabled() {
		t.Fatal("disable lost")
	}
	if _, err := backend.executeResource(context.Background(), "schedules", "trigger", json.RawMessage(`{"schedule_id":7}`)); err != nil {
		t.Fatal(err)
	}
	if hub.scheduleTrigger.GetId() != 7 {
		t.Fatal("trigger lost")
	}
	if _, err := backend.executeResource(context.Background(), "schedules", "delete", json.RawMessage(`{"schedule_id":7}`)); err != nil {
		t.Fatal(err)
	}
	hub.scheduleError = "TOKEN=secret"
	_, err = backend.executeResource(context.Background(), "schedules", "create", json.RawMessage(`{"spec":"@every 1h","task":{"agent":"builtin/codex","task":"work"}}`))
	consoleTestCode(t, err, "operation_failed")
	_, err = backend.executeResource(context.Background(), "schedules", "update", json.RawMessage(`{"schedule_id":7,"name":"updated"}`))
	consoleTestCode(t, err, "operation_failed")
}
func TestConsoleResourceScheduleSafeEditPreservesHiddenNativeArgs(t *testing.T) {
	consoleTestOperator(t)
	hub := consoleTestHub()
	hub.schedule = &pb.ScheduleItem{Id: 7, TaskSpecJson: []byte(`{"agent":"builtin/codex","task":"original","extra_args":["--secret=native-value"],"resolved_profile":{"env":["TOKEN=launch-secret"]}}`)}
	backend := &consoleBackend{hub: hub}
	value, err := backend.snapshotResource(context.Background(), "schedule", url.Values{"schedule_id": {"7"}})
	if err != nil {
		t.Fatal(err)
	}
	if raw := consoleTestJSON(t, value); strings.Contains(raw, "native-value") || strings.Contains(raw, "launch-secret") || !strings.Contains(raw, `"task":"original"`) {
		t.Fatalf("unsafe/edit projection=%s", raw)
	}
	for _, explicit := range []bool{false, true} {
		data := `{"schedule_id":7,"task":{"agent":"builtin/codex","task":"updated"}}`
		if explicit {
			data = `{"schedule_id":7,"task":{"agent":"builtin/codex","task":"updated","extra_args":[]}}`
		}
		if _, err := backend.executeResource(context.Background(), "schedules", "update", json.RawMessage(data)); err != nil {
			t.Fatal(err)
		}
		var spec task.Spec
		if err := json.Unmarshal(hub.scheduleUpdate.TaskSpecJson, &spec); err != nil {
			t.Fatal(err)
		}
		if explicit && len(spec.ExtraArgs) != 0 || !explicit && (len(spec.ExtraArgs) != 1 || spec.ExtraArgs[0] != "--secret=native-value") {
			t.Fatalf("native args not preserved/cleared: %+v", spec.ExtraArgs)
		}
		if spec.ResolvedProfile != nil {
			t.Fatal("replayed frozen launch from browser edit")
		}
	}
}

func TestConsoleResourceScheduleAndProfileSnapshotsRedact(t *testing.T) {
	consoleTestOperator(t)
	hub := consoleTestHub()
	hub.schedule = &pb.ScheduleItem{Id: 7, Prompt: "hello", TaskSpecJson: []byte(`{"resolved_profile":{"env":["TOKEN=task-secret"]}}`)}
	backend := &consoleBackend{hub: hub}
	for _, resource := range []string{"schedule", "schedules", "profiles", "profile"} {
		value, err := backend.snapshotResource(context.Background(), resource, url.Values{"schedule_id": {"7"}, "ref": {"builtin/codex"}})
		if err != nil {
			t.Fatal(err)
		}
		raw := consoleTestJSON(t, value)
		if strings.Contains(raw, "task_spec_json") || strings.Contains(raw, "task-secret") || strings.Contains(raw, `"default_env":[`) {
			t.Fatalf("unsafe snapshot = %s", raw)
		}
	}
	profile := hub.profile
	profile.DefaultEnv = []string{"TOKEN=profile-secret"}
	profile.DefaultArgs = []string{"--token=profile-secret"}
	profile.MCPConfig = "profile-secret"
	if raw := consoleTestJSON(t, consoleProfileView(profile)); strings.Contains(raw, "profile-secret") {
		t.Fatalf("unsafe profile = %s", raw)
	}
	if len(hub.schedule.TaskSpecJson) == 0 {
		t.Fatal("modified stored schedule")
	}
}
func TestConsoleResourceCredentialsContainMetadataOnly(t *testing.T) {
	consoleTestOperator(t)
	path := credentialProfilePath("test")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"kind":"long-lived","env":{"TOKEN":"credential-secret"},"files":{"auth":"file-secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	value, err := (&consoleBackend{}).snapshotResource(context.Background(), "credentials", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	if strings.Contains(raw, "credential-secret") || strings.Contains(raw, "file-secret") || strings.Contains(raw, filepath.Dir(path)) {
		t.Fatalf("unsafe credential = %s", raw)
	}
	if !strings.Contains(raw, `"env_names":["TOKEN"]`) {
		t.Fatalf("metadata missing = %s", raw)
	}
}
func TestConsoleFindSessionUsesExplicitTarget(t *testing.T) {
	entries := []*gateway.SessionEntry{{Session: &pb.Session{Name: "same"}, TargetID: "a"}, {Session: &pb.Session{Name: "same"}, TargetID: "b"}}
	entry, err := consoleFindSession(entries, "same", "b")
	if err != nil || entry.TargetID != "b" {
		t.Fatalf("entry=%v err=%v", entry, err)
	}
}
