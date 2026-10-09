package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func installScheduledPortableProfile(t *testing.T) {
	t.Helper()
	t.Setenv("AMUX_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dist", "agent-pack.manifest.json"), []byte(`{"schema_version":"v2","pack_namespace":"team","profiles":{"reporter":{"description":"Daily report"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := agentprofile.SavePackRegistry(agentprofile.DefaultPackRegistryPath(), agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{
		"team": {Namespace: "team", SourceKind: agentprofile.PackSourceGit, MaterializedPath: root},
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyScheduledPortableTaskUsesDefaultRuntime(t *testing.T) {
	installScheduledPortableProfile(t)
	sched := &fakeScheduler{acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
		if req.AgentType != "codex" {
			t.Fatalf("placement agent = %q, want default codex", req.AgentType)
		}
		return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
	}}
	routed := false
	hub := &fakeRouterHub{targets: targets("t1"), routeFn: func(_, _ string, payload interface{}) (*pb.TunnelMsg, error) {
		var spec task.Spec
		if err := json.Unmarshal(payload.(*pb.SubmitSubtaskRequest).SpecJson, &spec); err != nil {
			t.Fatal(err)
		}
		if spec.ResolvedProfile == nil || spec.ResolvedProfile.RuntimeRef != "codex" || spec.ResolvedProfile.Namespace != "team" || spec.ResolvedProfile.Name != "reporter" {
			t.Fatalf("lost default runtime or expert content: %+v", spec.ResolvedProfile)
		}
		routed = true
		return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-report","status":"pending"}`)}), nil
	}}
	router := newTaskRouter(hub, sched, newTaskIndex(), nil)
	id, err := router.SubmitScheduledTask(context.Background(), mustTaskSpecJson(t, task.Spec{Agent: "team/reporter", Task: "Write the daily report"}))
	if err != nil {
		t.Fatalf("legacy schedule without runtime failed before creating a Task: %v", err)
	}
	if id != "task-report" || !routed {
		t.Fatalf("task = %q, routed = %v", id, routed)
	}
}

func TestScheduledPortableTaskAcceptsOmittedExecutionChoices(t *testing.T) {
	installScheduledPortableProfile(t)
	srv := &hubServer{schedule: newScheduleTestStore(t)}
	created, err := srv.CreateSchedule(context.Background(), &pb.CreateScheduleRequest{
		ScheduleType: schedule.ScheduleTypeSubtask, Spec: "daily 10:00",
		TaskSpecJson: mustTaskSpecJson(t, task.Spec{Agent: "team/reporter", Task: "Write the daily report"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := srv.GetSchedule(context.Background(), &pb.GetScheduleRequest{Id: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	var spec task.Spec
	if err := json.Unmarshal(got.Schedule.TaskSpecJson, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Agent != "team/reporter" || spec.Runtime != "" || spec.Model != "" || spec.ResolvedProfile != nil {
		t.Fatalf("schedule must preserve caller intent and resolve defaults at trigger time: %+v", spec)
	}
	updated, err := srv.UpdateSchedule(context.Background(), &pb.UpdateScheduleRequest{Id: created.Id,
		TaskSpecJson: mustTaskSpecJson(t, task.Spec{Agent: "team/reporter", Runtime: "claude", Model: "review-model", Task: "Updated report"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(updated.Schedule.TaskSpecJson, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Runtime != "claude" || spec.Model != "review-model" || spec.Agent != "team/reporter" {
		t.Fatalf("explicit update was replaced by defaults: %+v", spec)
	}
}

func TestPortablePersistentFollowupPreservesSelectedRuntime(t *testing.T) {
	installScheduledPortableProfile(t)
	store := newTestAgentInstanceStore(t)
	var routed []task.Spec
	sched := &fakeScheduler{acquireFn: func(req registry.ScheduleRequest) (*registry.Placement, error) {
		if req.AgentType != "claude" {
			t.Fatalf("placement agent = %q, want original claude", req.AgentType)
		}
		return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
	}}
	hub := &fakeRouterHub{targets: targets("t1"), routeFn: func(_, _ string, payload interface{}) (*pb.TunnelMsg, error) {
		var spec task.Spec
		if err := json.Unmarshal(payload.(*pb.SubmitSubtaskRequest).SpecJson, &spec); err != nil {
			t.Fatal(err)
		}
		routed = append(routed, spec)
		return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-persistent","status":"pending"}`)}), nil
	}}
	router := newTaskRouter(hub, sched, newTaskIndex(), store)
	initial := task.Spec{Agent: "team/reporter", Runtime: "claude", Model: "review-model", Task: "Start report",
		Mode: task.ModePersistent, AgentInstanceID: "reporter-instance"}
	if _, err := router.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: mustTaskSpecJson(t, initial)}); err != nil {
		t.Fatal(err)
	}
	followup := task.Spec{Task: "Continue report", Mode: task.ModePersistent, AgentInstanceID: initial.AgentInstanceID}
	if _, err := router.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: mustTaskSpecJson(t, followup)}); err != nil {
		t.Fatalf("followup without execution choices: %v", err)
	}
	if len(routed) != 2 {
		t.Fatalf("routed %d tasks, want creation and followup", len(routed))
	}
	for _, spec := range routed {
		if spec.ResolvedProfile == nil || spec.ResolvedProfile.RuntimeRef != "claude" || spec.ResolvedProfile.ModelRef != "review-model" || spec.ResolvedProfile.Namespace != "team" {
			t.Fatalf("lost original execution identity: %+v", spec.ResolvedProfile)
		}
	}
	// Explicit changes must still be rejected rather than silently overwritten.
	followup.Runtime = "codex"
	_, err := router.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: mustTaskSpecJson(t, followup)})
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "fixed to agent type") {
		t.Fatalf("explicit runtime change = %v, want identity conflict", err)
	}
}
