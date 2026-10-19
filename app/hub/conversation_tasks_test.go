package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestConversationTaskUsesFrozenIdentityAfterTurnEnds(t *testing.T) {
	for _, mode := range []string{"legacy", "passthrough", "linux-dac", "service"} {
		t.Run(mode, func(t *testing.T) {
			store, identity, owner, other := authzTestPrincipals(t)
			identity.Source = principal.SourceFeishu
			identity.IsolationBackend = principal.BackendLinuxDAC
			if mode == "passthrough" {
				identity.IsolationBackend = principal.BackendPassthrough
			}
			if mode == "service" {
				service, err := store.Create(principal.Principal{Kind: principal.KindService, DisplayName: "shared"})
				if err != nil {
					t.Fatal(err)
				}
				if err := store.GrantServiceMember(service.ID, owner.ID); err != nil {
					t.Fatal(err)
				}
				identity.EffectivePrincipalID = service.ID
				identity.DelegationID = principal.ServiceMembershipAuthorityID(service.ID, owner.ID)
			}
			if mode == "legacy" {
				identity = principal.LegacyHostIdentity(principal.SourceFeishu)
			}
			session := &pb.Session{Id: "session-id", Name: "conversation", ExecutionIdentity: principal.ToProtoIdentity(identity)}
			target := &pb.Target{Id: "target", Sessions: []*pb.Session{session}}
			if mode != "legacy" {
				target.Capabilities = &pb.TargetCapabilities{IsolationHelperProtocol: "1"}
			}
			var routed task.Spec
			var saved []byte
			hub := &fakeRouterHub{targets: []*pb.Target{target}, routeFn: func(_ string, method string, payload interface{}) (*pb.TunnelMsg, error) {
				switch method {
				case pb.TaskMethodSubmit:
					req := payload.(*pb.SubmitSubtaskRequest)
					if err := json.Unmarshal(req.SpecJson, &routed); err != nil {
						t.Fatal(err)
					}
					if identity.Identified() && (req.AgentActorToken == "" || req.LaunchId == "") {
						t.Fatal("identified child lacks a launch credential")
					}
					if !identity.Identified() && req.AgentActorToken != "" {
						t.Fatal("legacy child gained an actor credential")
					}
					saved, _ = json.Marshal(map[string]any{"id": "task-1", "status": "succeeded", "execution_identity": routed.ExecutionIdentity, "result": map[string]string{"summary": "draft"}})
					return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: saved}), nil
				case pb.TaskMethodGet:
					return respJSON(t, &pb.GetSubtaskResponse{TaskJson: saved}), nil
				default:
					return nil, fmt.Errorf("background work must not resolve an active parent turn: %s", method)
				}
			}}
			router := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
				return &registry.Placement{TargetID: "target", ReservationId: "r1"}, nil
			}}, newTaskIndex(), nil)
			router.principals = store
			reader := &bridgeSubtaskReader{router: router}
			spec, _ := json.Marshal(task.Spec{Agent: "codex", Task: "prepare draft", PermissionPolicy: task.PolicyDenyAll, Workspace: task.WorkspaceSpec{Policy: task.WorkspaceTempDir}})
			id, err := reader.SubmitConversationTask(context.Background(), "target", "conversation", spec)
			if err != nil {
				t.Fatal(err)
			}
			got := routed.ExecutionIdentity
			if id != "task-1" || got == nil || got.IdentityMode != identity.IdentityMode || got.OriginActorPrincipalID != identity.OriginActorPrincipalID || got.EffectivePrincipalID != identity.EffectivePrincipalID || got.IsolationBackend != identity.IsolationBackend || got.DelegationID != identity.DelegationID {
				t.Fatalf("identity changed: got=%+v want=%+v", got, identity)
			}
			if routed.ParentSessionID != "" || routed.ParentTurnID != "" || routed.NotifyParent == nil || *routed.NotifyParent || routed.PermissionPolicy != task.PolicyDenyAll {
				t.Fatalf("unexpected background semantics: %+v", routed)
			}
			// A restarted Hub has no cached placement or credentials from submit.
			router.index = newTaskIndex()
			if raw, err := reader.ReadConversationTask(context.Background(), "target", "conversation", id); err != nil || string(raw) != string(saved) {
				t.Fatalf("restart read: %s, %v", raw, err)
			}
			if mode == "legacy" {
				target.Capabilities = &pb.TargetCapabilities{IsolationHelperProtocol: "1"}
				if _, err := reader.SubmitConversationTask(context.Background(), "target", "conversation", spec); err == nil {
					t.Fatal("anonymous task accepted on isolated host")
				}
				return
			}
			foreign := identity
			foreign.OriginActorPrincipalID, foreign.EffectivePrincipalID, foreign.DelegationID = other.ID, other.ID, ""
			foreignRaw, _ := json.Marshal(map[string]any{"execution_identity": foreign})
			original := saved
			saved = foreignRaw
			if _, err := reader.ReadConversationTask(context.Background(), "target", "conversation", id); err == nil {
				t.Fatal("read another principal's task")
			}
			saved = original
			if mode == "service" {
				if err := store.RevokeServiceMember(identity.EffectivePrincipalID, owner.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := reader.SubmitConversationTask(context.Background(), "target", "conversation", spec); err == nil {
					t.Fatal("revoked authority submitted")
				}
				if _, err := reader.ReadConversationTask(context.Background(), "target", "conversation", id); err == nil {
					t.Fatal("revoked authority read task")
				}
			}
		})
	}
}

func TestConversationTaskRejectsCallerIdentityAndMissingSession(t *testing.T) {
	hub := &fakeRouterHub{targets: []*pb.Target{{Id: "target", Sessions: []*pb.Session{{Name: "conversation"}}}}, routeFn: func(string, string, interface{}) (*pb.TunnelMsg, error) {
		t.Fatal("invalid task reached runtime")
		return nil, nil
	}}
	reader := &bridgeSubtaskReader{router: newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), nil)}
	for _, raw := range []string{
		`{"execution_identity":{"identity_mode":"legacy-host"}}`, `{"run_as":"other"}`, `{"parent_session_id":"other"}`, `{"parent_task_id":"other"}`, `{"parent_turn_id":"other"}`, `{"agent_instance_id":"other"}`, `{"mode":"persistent"}`,
	} {
		if _, err := reader.SubmitConversationTask(context.Background(), "target", "conversation", []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, pair := range [][2]string{{"other", "conversation"}, {"target", "missing"}, {"", "conversation"}} {
		if _, err := reader.SubmitConversationTask(context.Background(), pair[0], pair[1], []byte(`{}`)); err == nil {
			t.Fatalf("accepted missing binding %v", pair)
		}
	}
}
