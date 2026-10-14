package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentinstance"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPersistentFollowupInheritsAndRejectsPermissionPolicyChanges(t *testing.T) {
	store := newTestAgentInstanceStore(t)
	if _, err := store.Put(agentinstance.Instance{ID: "frozen-policy", AgentType: "claude", PermissionPolicy: "deny-all"}); err != nil {
		t.Fatal(err)
	}
	r := newTaskRouter(&fakeRouterHub{}, &fakeScheduler{}, newTaskIndex(), store)
	for _, agent := range []string{"", "claude"} {
		spec := task.Spec{Agent: agent, Mode: task.ModePersistent, AgentInstanceID: "frozen-policy"}
		if err := r.hydratePersistentFollowupIdentity(&spec); err != nil {
			t.Fatal(err)
		}
		if spec.PermissionPolicy != task.PolicyDenyAll {
			t.Fatalf("followup policy = %q", spec.PermissionPolicy)
		}
		if _, err := r.claimPersistentInstanceIdentity(spec); err != nil {
			t.Fatal(err)
		}
		spec.PermissionPolicy = task.PolicyAllow
		_, err := r.claimPersistentInstanceIdentity(spec)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), "deny-all") || !strings.Contains(err.Error(), "新建") {
			t.Fatalf("policy change = %v", err)
		}
	}
	in, err := store.Get("frozen-policy")
	if err != nil || instanceRecordToProto(in).PermissionPolicy != "deny-all" {
		t.Fatalf("instance readback = %+v, %v", in, err)
	}
}

func TestScheduledTaskCompilesNativePermissionsBeforeDispatch(t *testing.T) {
	for _, policy := range []task.PermissionPolicy{"", task.PolicyDenyAll} {
		t.Run(string(policy), func(t *testing.T) {
			hub := &fakeRouterHub{targets: targets("t1"), routeFn: func(_, method string, payload interface{}) (*pb.TunnelMsg, error) {
				if method != pb.TaskMethodSubmit {
					return nil, fmt.Errorf("unexpected method %s", method)
				}
				var got task.Spec
				if err := json.Unmarshal(payload.(*pb.SubmitSubtaskRequest).SpecJson, &got); err != nil {
					t.Fatal(err)
				}
				want, _ := agentprofile.TaskPermissionArgs("codex", policy, nil, false)
				if !slices.Equal(got.ExtraArgs, want) || got.ResolvedProfile.AgentsContent != "review changes" {
					t.Fatalf("scheduled execution = %+v", got)
				}
				return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"scheduled-policy","status":"pending"}`)}), nil
			}}
			sched := &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
				return &registry.Placement{TargetID: "t1", ReservationId: "r1"}, nil
			}}
			r := newTaskRouter(hub, sched, newTaskIndex(), nil)
			r.resolveProfile = func(spec *task.Spec) error { return agentprofile.ResolveTaskSpec(spec, "/missing/registry") }
			encoded, _ := json.Marshal(task.Spec{Agent: "codex", Task: "review", PermissionPolicy: policy, ExtraArgs: []string{"--yolo"}, ResolvedProfile: &pb.AgentProfileSpec{Ref: "local/reviewer", Type: "codex", AgentsContent: "review changes"}})
			if _, err := r.SubmitScheduledTask(context.Background(), encoded); err != nil {
				t.Fatal(err)
			}
		})
	}
}
