package hub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSubmitSubtaskMintsActorCredentialForIdentifiedTask(t *testing.T) {
	store, identity, _, _ := authzTestPrincipals(t)
	var gotReq *pb.SubmitSubtaskRequest
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(_, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if method != pb.TaskMethodSubmit {
				return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
			}
			req, ok := payload.(*pb.SubmitSubtaskRequest)
			if !ok {
				return nil, status.Errorf(codes.Internal, "unexpected payload %T", payload)
			}
			gotReq = req
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-1","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}, newTaskIndex(), nil)
	r.principals = store

	spec := task.Spec{Agent: "claude", Task: "work", ExecutionIdentity: &identity}
	specJSON, _ := json.Marshal(spec)
	ctx := context.WithValue(context.Background(), scheduledSubmitContextKey{}, true)
	if _, err := r.SubmitSubtask(ctx, &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask: %v", err)
	}
	if gotReq == nil {
		t.Fatal("route was not called")
	}
	if gotReq.LaunchId == "" {
		t.Error("wire launch_id is empty")
	}
	if gotReq.AgentActorToken == "" {
		t.Error("wire agent_actor_token is empty for an identified task")
	}
	if gotReq.AgentActorTokenExpiresAt == 0 {
		t.Error("wire agent_actor_token_expires_at is zero")
	}
	// The credential must not leak into the persisted spec: spec_json is what
	// the runner records, and a token there would be replayed on every restart.
	var routed struct {
		AgentActorToken string `json:"agent_actor_token"`
		LaunchID        string `json:"launch_id"`
	}
	if err := json.Unmarshal(gotReq.SpecJson, &routed); err != nil {
		t.Fatalf("decode routed spec: %v", err)
	}
	if routed.AgentActorToken != "" || routed.LaunchID != "" {
		t.Errorf("credential material leaked into spec_json: %s", gotReq.SpecJson)
	}
}

func TestSubmitSubtaskSendsNoCredentialForLegacyTask(t *testing.T) {
	var gotReq *pb.SubmitSubtaskRequest
	hub := &fakeRouterHub{
		targets: targets("t1"),
		routeFn: func(_, method string, payload interface{}) (*pb.TunnelMsg, error) {
			req, _ := payload.(*pb.SubmitSubtaskRequest)
			gotReq = req
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-1","status":"pending"}`)}), nil
		},
	}
	r := newTaskRouter(hub, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}, newTaskIndex(), nil)
	r.principals = agentTokenTestStore(t)

	specJSON, _ := json.Marshal(task.Spec{Agent: "claude", Task: "work"})
	if _, err := r.SubmitSubtask(authlessCtx(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask legacy: %v", err)
	}
	if gotReq.AgentActorToken != "" {
		t.Errorf("legacy task carried a credential; want none")
	}
	if gotReq.LaunchId != "" {
		t.Errorf("legacy task carried a launch id; want none")
	}
}

func TestScheduleRequestTreatsIsolationGlobalEnvAsSatisfied(t *testing.T) {
	r := &taskRouter{
		isolationGlobalEnv: []string{"GLM_API_KEY=shared-secret"},
		credentialProvider: func() (task.CredentialProvider, error) {
			t.Fatal("credential provider called for isolation global env")
			return nil, nil
		},
	}
	req, err := r.scheduleRequestFromSpec(context.Background(), task.Spec{
		Agent: "codex",
		Task:  "work",
		ExecutionIdentity: &principal.ExecutionIdentity{
			IdentityMode:         principal.IdentityModeIdentified,
			EffectivePrincipalID: "principal-1",
		},
		ResolvedProfile: &pb.AgentProfileSpec{
			Type: "codex",
			Envs: []string{"GLM_API_KEY"},
		},
	})
	if err != nil {
		t.Fatalf("scheduleRequestFromSpec: %v", err)
	}
	if len(req.CredentialProfiles) != 0 || req.CredentialProfile != "" {
		t.Fatalf("shared env produced credential requirements: %+v", req)
	}
}
