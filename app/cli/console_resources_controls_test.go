package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
)

type consoleResourceTasks struct {
	pb.AmuxTaskClient
	raw       []byte
	instance  *pb.AgentInstance
	cancel    *pb.CancelSubtaskRequest
	respond   *pb.RespondSubtaskPermissionRequest
	send      *pb.SendAgentMessageRequest
	interrupt *pb.InterruptAgentInstanceRequest
	submit    *pb.SubmitSubtaskRequest
	rejected  bool
}

func (f *consoleResourceTasks) ListSubtasks(context.Context, *pb.ListSubtasksRequest, ...grpc.CallOption) (*pb.ListSubtasksResponse, error) {
	return &pb.ListSubtasksResponse{TasksJson: [][]byte{f.raw}}, nil
}
func (f *consoleResourceTasks) GetSubtask(context.Context, *pb.GetSubtaskRequest, ...grpc.CallOption) (*pb.GetSubtaskResponse, error) {
	return &pb.GetSubtaskResponse{TaskJson: f.raw}, nil
}
func (f *consoleResourceTasks) CancelSubtask(_ context.Context, req *pb.CancelSubtaskRequest, _ ...grpc.CallOption) (*pb.CancelSubtaskResponse, error) {
	f.cancel = req
	return &pb.CancelSubtaskResponse{Ok: !f.rejected}, nil
}
func (f *consoleResourceTasks) RespondSubtaskPermission(_ context.Context, req *pb.RespondSubtaskPermissionRequest, _ ...grpc.CallOption) (*pb.RespondSubtaskPermissionResponse, error) {
	f.respond = req
	return &pb.RespondSubtaskPermissionResponse{Ok: !f.rejected}, nil
}
func (f *consoleResourceTasks) ListAgentInstances(context.Context, *pb.ListAgentInstancesRequest, ...grpc.CallOption) (*pb.ListAgentInstancesResponse, error) {
	return &pb.ListAgentInstancesResponse{Instances: []*pb.AgentInstance{f.instance}}, nil
}
func (f *consoleResourceTasks) GetAgentInstance(context.Context, *pb.GetAgentInstanceRequest, ...grpc.CallOption) (*pb.GetAgentInstanceResponse, error) {
	return &pb.GetAgentInstanceResponse{Instance: f.instance}, nil
}
func (f *consoleResourceTasks) SendAgentMessage(_ context.Context, req *pb.SendAgentMessageRequest, _ ...grpc.CallOption) (*pb.SendAgentMessageResponse, error) {
	f.send = req
	return &pb.SendAgentMessageResponse{Ok: !f.rejected, PendingMessages: 2, Delivery: "mailbox"}, nil
}
func (f *consoleResourceTasks) InterruptAgentInstance(_ context.Context, req *pb.InterruptAgentInstanceRequest, _ ...grpc.CallOption) (*pb.InterruptAgentInstanceResponse, error) {
	f.interrupt = req
	return &pb.InterruptAgentInstanceResponse{Ok: !f.rejected}, nil
}

func (f *consoleResourceTasks) SubmitSubtask(_ context.Context, req *pb.SubmitSubtaskRequest, _ ...grpc.CallOption) (*pb.SubmitSubtaskResponse, error) {
	f.submit = req
	return &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"instance-task","status":"scheduled"}`)}, nil
}

func TestConsoleResourceInstanceSpawnAndFollowupUseHubAdmission(t *testing.T) {
	consoleTestOperator(t)
	client := &consoleResourceTasks{}
	backend := &consoleBackend{tasks: client}
	for _, action := range []string{"spawn", "followup"} {
		data := `{"agent":"builtin/codex","task":"work","runtime":"codex","model":"native-model","permission_policy":"escalate-to-main","extra_args":["--native-setting"],"workspace":{"policy":"temp-dir"}}`
		if action == "followup" {
			data = `{"agent":"builtin/codex","task":"follow","agent_instance_id":"agent-existing","run_as":"self"}`
		}
		value, err := backend.executeResource(context.Background(), "instances", action, json.RawMessage(data))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(consoleTestJSON(t, value), `"task_id":"instance-task"`) {
			t.Fatal("missing task handle")
		}
		var spec task.Spec
		if err := json.Unmarshal(client.submit.SpecJson, &spec); err != nil {
			t.Fatal(err)
		}
		if spec.Mode != task.ModePersistent || spec.Blocking || spec.ResolvedProfile != nil || spec.ExecutionIdentity != nil || spec.ParentSessionID != "" || spec.ParentTaskID != "" {
			t.Fatalf("unsafe spec=%+v", spec)
		}
		if action == "spawn" {
			if !strings.HasPrefix(spec.AgentInstanceID, "agent-") || spec.PermissionPolicy != task.PolicyEscalateToMain || spec.Model != "native-model" || spec.Runtime != "codex" {
				t.Fatalf("spawn spec=%+v", spec)
			}
		} else if spec.AgentInstanceID != "agent-existing" || spec.RunAs != "self" {
			t.Fatalf("followup spec=%+v", spec)
		}
	}
	for _, data := range []string{`{"agent":"builtin/codex","task":"work","execution_identity":{}}`, `{"agent":"builtin/codex","task":"work","parent_session_id":"forged"}`, `{"agent":"builtin/codex","task":"work","agent_executable":"/tmp/evil"}`, `{"agent":"builtin/codex","task":"work","agent_instance_id":"existing"}`, `{"agent":"builtin/codex","task":"work","permission_policy":"bad"}`} {
		client.submit = nil
		if _, err := backend.executeResource(context.Background(), "instances", "spawn", json.RawMessage(data)); err == nil {
			t.Fatal("accepted privileged/invalid input")
		}
		if client.submit != nil {
			t.Fatal("invalid request reached admission")
		}
	}
}

func TestConsoleResourceTaskSnapshotsDoNotExposeStoredSpecs(t *testing.T) {
	consoleTestOperator(t)
	// Unknown frozen launch/spec fields must stay server-side, even when the
	// transport JSON stores more information than the public TaskRun DTO.
	client := &consoleResourceTasks{raw: []byte(`{"id":"task-1","status":"running","agent_profile":"builtin/codex","error":"TOKEN=error-secret","spec":{"env":["TOKEN=spec-secret"]},"resolved_profile":{"env":["TOKEN=profile-secret"]},"execution_identity":{"principal_id":"private-id"}}`)}
	backend := &consoleBackend{tasks: client}
	for _, resource := range []string{"tasks", "task"} {
		value, err := backend.snapshotResource(context.Background(), resource, url.Values{"task_id": {"task-1"}})
		if err != nil {
			t.Fatal(err)
		}
		raw := consoleTestJSON(t, value)
		for _, secret := range []string{"spec-secret", "profile-secret", "error-secret", "private-id", "execution_identity", "resolved_profile"} {
			if strings.Contains(raw, secret) {
				t.Fatalf("%s leaked in %s", secret, raw)
			}
		}
		if !strings.Contains(raw, `"task_id":"task-1"`) {
			t.Fatalf("missing task: %s", raw)
		}
	}
	client.raw = []byte(`broken`)
	_, err := backend.snapshotResource(context.Background(), "tasks", nil)
	consoleTestCode(t, err, "backend_response_invalid")
}
func TestConsoleResourceTaskMutationsPreserveExplicitDeny(t *testing.T) {
	consoleTestOperator(t)
	client := &consoleResourceTasks{}
	backend := &consoleBackend{tasks: client}
	if _, err := backend.executeResource(context.Background(), "tasks", "cancel", json.RawMessage(`{"task_id":"task-1"}`)); err != nil {
		t.Fatal(err)
	}
	if client.cancel.GetId() != "task-1" {
		t.Fatalf("cancel=%v", client.cancel)
	}
	if _, err := backend.executeResource(context.Background(), "tasks", "respond", json.RawMessage(`{"task_id":"task-1","permission_id":"p","allow":false,"option_id":"deny","reason":"operator"}`)); err != nil {
		t.Fatal(err)
	}
	if client.respond.GetAllow() || client.respond.GetPermissionId() != "p" || client.respond.GetOptionId() != "deny" || client.respond.GetReason() != "operator" {
		t.Fatalf("respond=%v", client.respond)
	}
	client.rejected = true
	_, err := backend.executeResource(context.Background(), "tasks", "cancel", json.RawMessage(`{"task_id":"task-1"}`))
	consoleTestCode(t, err, "operation_rejected")
}
func TestConsoleResourceInstanceSnapshotsAndControlledActions(t *testing.T) {
	consoleTestOperator(t)
	client := &consoleResourceTasks{instance: &pb.AgentInstance{Id: "agent-1", Name: "agent", AgentProfile: "builtin/codex", Status: "idle", HomeRef: "private-home", WorkspaceRef: "private-workspace", ExecutionIdentity: &pb.ExecutionIdentity{EffectivePrincipalId: "private-principal"}}}
	backend := &consoleBackend{tasks: client}
	for _, resource := range []string{"instances", "instance"} {
		value, err := backend.snapshotResource(context.Background(), resource, url.Values{"agent_instance_id": {"agent-1"}})
		if err != nil {
			t.Fatal(err)
		}
		raw := consoleTestJSON(t, value)
		for _, private := range []string{"private-home", "private-workspace", "private-principal"} {
			if strings.Contains(raw, private) {
				t.Fatalf("unsafe instance=%s", raw)
			}
		}
	}
	for _, action := range []string{"send", "steer"} {
		if _, err := backend.executeResource(context.Background(), "instances", action, json.RawMessage(`{"agent_instance_id":"agent-1","text":"hello"}`)); err != nil {
			t.Fatal(err)
		}
		if client.send.GetId() != "agent-1" || client.send.GetText() != "hello" || client.send.GetSource() != "console" || client.send.GetSteer() != (action == "steer") {
			t.Fatalf("send=%v", client.send)
		}
	}
	if _, err := backend.executeResource(context.Background(), "instances", "interrupt", json.RawMessage(`{"agent_instance_id":"agent-1"}`)); err != nil {
		t.Fatal(err)
	}
	client.rejected = true
	_, err := backend.executeResource(context.Background(), "instances", "steer", json.RawMessage(`{"agent_instance_id":"agent-1","text":"hello"}`))
	consoleTestCode(t, err, "operation_rejected")
}
func TestConsoleResourceCatalogNeverSerializesProvider(t *testing.T) {
	consoleTestOperator(t)
	backend := &consoleBackend{hub: consoleTestHub()}
	value, err := backend.snapshotResource(context.Background(), "catalogs", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := consoleTestJSON(t, value)
	for _, field := range []string{`"Provider"`, `"provider"`, `"env"`, `"base_url"`} {
		if strings.Contains(raw, field) {
			t.Fatalf("catalog leaked provider configuration: %s", raw)
		}
	}
	if !strings.Contains(raw, `"runtimes"`) || !strings.Contains(raw, `"targets":[{"id":"runtime-a","name":"worker"}]`) {
		t.Fatalf("catalog shape=%s", raw)
	}
}
func TestConsoleResourcePackInputsRejectDangerousURLsAndRequireConfirmation(t *testing.T) {
	consoleTestOperator(t)
	for _, raw := range []string{"/tmp/repo", "../repo", "file:///tmp/repo", "ext::sh -c something", "git://example.com/repo", "https://user:password@example.com/repo", "https://example.com/repo?token=secret", "https://localhost/repo", "https://127.0.0.1/repo", "ssh://127.0.0.1/repo", "git@localhost:repo", "-u"} {
		t.Run(raw, func(t *testing.T) {
			if err := consoleValidateRemotePackURL(raw); err == nil {
				t.Fatalf("accepted %q", raw)
			}
		})
	}
	for _, raw := range []string{"https://example.com/team/repo.git", "git@example.com:team/repo.git", "ssh://git@example.com/team/repo.git"} {
		if err := consoleValidateRemotePackURL(raw); err != nil {
			t.Fatalf("rejected %q: %v", raw, err)
		}
	}
	for _, namespace := range []string{"builtin", "local", "../team", "-C", "x/y", ""} {
		if err := consoleValidatePackNamespace(namespace); err == nil {
			t.Fatalf("accepted namespace %q", namespace)
		}
	}
	_, err := (&consoleBackend{}).executeResource(context.Background(), "packs", "install", json.RawMessage(`{"namespace":"team","url":"https://example.com/team/repo.git"}`))
	consoleTestCode(t, err, "confirmation_required")
}
func TestConsoleResourcePackRegistryAPICallsAndRedaction(t *testing.T) {
	consoleTestOperator(t)
	ctx := context.Background()
	oldInstall, oldSync, oldRollback := consoleInstallPack, consoleSyncPack, consoleRollbackPack
	t.Cleanup(func() { consoleInstallPack = oldInstall; consoleSyncPack = oldSync; consoleRollbackPack = oldRollback })
	pack := agentprofile.Pack{Namespace: "team", SourceKind: agentprofile.PackSourceGit, GitURL: "https://example.com/team/repo.git", CurrentRevision: "abc", Versions: []agentprofile.PackVersion{{Revision: "abc"}}, LastError: "TOKEN=stored-secret"}
	var installCalls, syncCalls, rollbackCalls int
	consoleInstallPack = func(_ context.Context, path, namespace, rawURL string, opts agentprofile.AddGitPackOptions) (agentprofile.Pack, error) {
		installCalls++
		if path != defaultAgentPackRegistryPath() || namespace != "team" || rawURL != pack.GitURL || opts.Ref != "main" || opts.AutoSync {
			t.Fatalf("bad install arguments: %s %s %s %+v", path, namespace, rawURL, opts)
		}
		return pack, nil
	}
	consoleSyncPack = func(_ context.Context, path, namespace string) (agentprofile.Pack, error) {
		syncCalls++
		if namespace != "team" {
			t.Fatal(namespace)
		}
		return pack, nil
	}
	consoleRollbackPack = func(_ context.Context, path, namespace, revision string) (agentprofile.Pack, error) {
		rollbackCalls++
		if namespace != "team" || revision != "abc" {
			t.Fatalf("rollback %s %s", namespace, revision)
		}
		return pack, nil
	}
	backend := &consoleBackend{}
	value, err := backend.executeResource(ctx, "packs", "install", json.RawMessage(`{"namespace":"team","url":"https://example.com/team/repo.git","ref":"main","confirm":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if installCalls != 1 || strings.Contains(consoleTestJSON(t, value), "stored-secret") {
		t.Fatal("install response or call invalid")
	}
	if err := agentprofile.SavePackRegistry(defaultAgentPackRegistryPath(), agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{"team": pack}}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.executeResource(ctx, "packs", "sync", json.RawMessage(`{"namespace":"team"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.executeResource(ctx, "packs", "rollback", json.RawMessage(`{"namespace":"team","revision":"abc","confirm":true}`)); err != nil {
		t.Fatal(err)
	}
	if syncCalls != 1 || rollbackCalls != 1 {
		t.Fatalf("sync=%d rollback=%d", syncCalls, rollbackCalls)
	}
	_, err = backend.executeResource(ctx, "packs", "rollback", json.RawMessage(`{"namespace":"team","revision":"unrecorded","confirm":true}`))
	consoleTestCode(t, err, "invalid_field")
	pack.GitURL = "https://user:secret@example.com/repo?token=secret"
	pack.LastError = "secret"
	if raw := consoleTestJSON(t, consolePackView(pack)); strings.Contains(raw, "secret") {
		t.Fatalf("unsafe pack=%s", raw)
	}
	consoleSyncPack = func(context.Context, string, string) (agentprofile.Pack, error) {
		return agentprofile.Pack{}, errors.New("api_key=failed-secret")
	}
	_, err = backend.executeResource(ctx, "packs", "sync", json.RawMessage(`{"namespace":"team"}`))
	consoleTestCode(t, err, "operation_failed")
	if strings.Contains(err.Error(), "failed-secret") {
		t.Fatal("leaked git output")
	}
}
func TestConsoleResourceTaskResultPreservesUserContent(t *testing.T) {
	run := &task.TaskRun{ID: "task-1", Result: &task.TaskResult{Summary: "user-visible result"}}
	if raw := consoleTestJSON(t, consoleTaskView(run)); !strings.Contains(raw, "user-visible result") {
		t.Fatal("removed task result content")
	}
}
