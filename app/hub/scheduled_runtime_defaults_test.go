package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
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
