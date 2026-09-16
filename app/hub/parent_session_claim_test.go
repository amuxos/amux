package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestSubmitRejectsForeignParentSessionClaim pins the isolating-fleet boundary a submitted
// parent_session_id must respect. The field is caller-supplied — `amux mcp`
// fills it from AMUX_SESSION_ID, but any value is accepted on the wire — and the
// router resolves it to that session's frozen execution identity and inherits
// it. Nothing else in the submit path re-checks who is calling, so without this
// rule a caller who learns another user's session id submits work that runs as
// that user: their principal, their HOME, their credentials.
func TestSubmitRejectsForeignParentSessionClaim(t *testing.T) {
	principals, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer principals.Close()

	victim, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "victim"})
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "attacker"})
	if err != nil {
		t.Fatal(err)
	}
	attackerCred, err := principals.IssueAgentCredential(attacker.ID, "launch-attacker", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}

	victimIdentity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: victim.ID,
		EffectivePrincipalID:   victim.ID,
		Source:                 principal.SourceMCP,
	}
	hub := &fakeRouterHub{
		targets: []*pb.Target{{
			Id: "t1", Capabilities: &pb.TargetCapabilities{IsolationHelperProtocol: "1"},
			Sessions: []*pb.Session{{
				Id:                "victim-session",
				ExecutionIdentity: principal.ToProtoIdentity(victimIdentity),
			}},
		}},
		routeFn: func(_, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if method != pb.TaskMethodSubmit {
				return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
			}
			req, ok := payload.(*pb.SubmitSubtaskRequest)
			if !ok {
				return nil, status.Errorf(codes.Internal, "unexpected payload %T", payload)
			}
			var routed task.Spec
			if err := json.Unmarshal(req.SpecJson, &routed); err != nil {
				return nil, status.Errorf(codes.Internal, "decode routed spec: %v", err)
			}
			if routed.ExecutionIdentity != nil {
				t.Errorf("task reached the runtime as principal %q (origin %q); the attacker is %q",
					routed.ExecutionIdentity.EffectivePrincipalID,
					routed.ExecutionIdentity.OriginActorPrincipalID, attacker.ID)
			}
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-1","status":"pending"}`)}), nil
		},
	}
	router := newTaskRouter(hub, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}, newTaskIndex(), nil)
	router.principals = principals

	specJSON, err := json.Marshal(task.Spec{
		Agent: "claude", Task: "read the victim's workspace", ParentSessionID: "victim-session",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = router.SubmitSubtask(actorCtx(attackerCred.Token), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
	if code := status.Code(err); code != codes.PermissionDenied {
		t.Fatalf("submit with another principal's parent_session_id: code = %v, want PermissionDenied (err=%v)", code, err)
	}
}

func TestSubmitAllowsForeignParentSessionClaimWithoutIsolation(t *testing.T) {
	principals, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer principals.Close()

	victim, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "victim"})
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "attacker"})
	if err != nil {
		t.Fatal(err)
	}
	attackerCred, err := principals.IssueAgentCredential(attacker.ID, "launch-attacker", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}
	victimIdentity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: victim.ID,
		EffectivePrincipalID:   victim.ID,
		Source:                 principal.SourceMCP,
	}
	var routedPrincipal string
	hub := &fakeRouterHub{
		targets: []*pb.Target{{
			Id: "t1",
			Sessions: []*pb.Session{{
				Id: "victim-session", Name: "victim-session",
				ExecutionIdentity: principal.ToProtoIdentity(victimIdentity),
			}},
		}},
		routeFn: func(_, method string, payload interface{}) (*pb.TunnelMsg, error) {
			switch method {
			case pb.TaskMethodResolveParentTurn:
				return respJSON(t, &pb.ActiveTurnResponse{Active: true, TurnId: "victim-turn", SessionId: "victim-session"}), nil
			case pb.TaskMethodSubmit:
				req := payload.(*pb.SubmitSubtaskRequest)
				var routed task.Spec
				if err := json.Unmarshal(req.SpecJson, &routed); err != nil {
					return nil, status.Errorf(codes.Internal, "decode routed spec: %v", err)
				}
				if routed.ExecutionIdentity != nil {
					routedPrincipal = routed.ExecutionIdentity.EffectivePrincipalID
				}
				return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-1","status":"pending"}`)}), nil
			default:
				return nil, status.Errorf(codes.Internal, "unexpected route %s", method)
			}
		},
	}
	router := newTaskRouter(hub, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}, newTaskIndex(), nil)
	router.principals = principals

	specJSON, err := json.Marshal(task.Spec{
		Agent: "claude", Task: "shared work", ParentSessionID: "victim-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.SubmitSubtask(actorCtx(attackerCred.Token), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("submit with foreign parent without isolation: %v", err)
	}
	if routedPrincipal != victim.ID {
		t.Fatalf("routed principal = %q, want inherited victim %q", routedPrincipal, victim.ID)
	}
}

// TestSubmitInheritsOwnParentSession is the other half of the boundary: the
// ordinary path, where an agent submits a child task naming the session it is
// itself running in, must keep inheriting that session's identity. Refusing a
// foreign claim is only correct if this still works.
func TestSubmitInheritsOwnParentSession(t *testing.T) {
	principals, err := principal.OpenStore(filepath.Join(t.TempDir(), "principals.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer principals.Close()

	owner, err := principals.Create(principal.Principal{Kind: principal.KindHuman, DisplayName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := principals.IssueAgentCredential(owner.ID, "launch-owner", principal.AgentCredentialTTL)
	if err != nil {
		t.Fatal(err)
	}

	ownerIdentity := principal.ExecutionIdentity{
		IdentityMode:           principal.IdentityModeIdentified,
		OriginActorPrincipalID: owner.ID,
		EffectivePrincipalID:   owner.ID,
		Source:                 principal.SourceMCP,
	}
	var routedPrincipal, routedParentTaskID, routedParentTurnID string
	hub := &fakeRouterHub{
		targets: []*pb.Target{{
			Id: "t1",
			Sessions: []*pb.Session{{
				Id:                "own-session",
				Name:              "own-session-name",
				OwnerTaskId:       "parent-task",
				ExecutionIdentity: principal.ToProtoIdentity(ownerIdentity),
			}},
		}},
		routeFn: func(_, method string, payload interface{}) (*pb.TunnelMsg, error) {
			if method == pb.TaskMethodResolveParentTurn {
				return respJSON(t, &pb.ActiveTurnResponse{Active: true, TurnId: "parent-turn", SessionId: "own-session"}), nil
			}
			req, ok := payload.(*pb.SubmitSubtaskRequest)
			if !ok {
				return nil, status.Errorf(codes.Internal, "unexpected payload %T", payload)
			}
			var routed task.Spec
			if err := json.Unmarshal(req.SpecJson, &routed); err != nil {
				return nil, status.Errorf(codes.Internal, "decode routed spec: %v", err)
			}
			if routed.ExecutionIdentity != nil {
				routedPrincipal = routed.ExecutionIdentity.EffectivePrincipalID
			}
			routedParentTaskID = routed.ParentTaskID
			routedParentTurnID = routed.ParentTurnID
			return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-1","status":"pending"}`)}), nil
		},
	}
	router := newTaskRouter(hub, &fakeScheduler{
		acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
			return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
		},
	}, newTaskIndex(), nil)
	router.principals = principals

	specJSON, err := json.Marshal(task.Spec{
		Agent: "claude", Task: "do my own work", ParentSessionID: "own-session",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := router.SubmitSubtask(actorCtx(cred.Token), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("submit naming own parent session: %v", err)
	}
	if routedPrincipal != owner.ID {
		t.Fatalf("routed as principal %q, want the parent session's %q", routedPrincipal, owner.ID)
	}
	if routedParentTaskID != "parent-task" || routedParentTurnID != "parent-turn" {
		t.Fatalf("routed provenance parent_task=%q parent_turn=%q", routedParentTaskID, routedParentTurnID)
	}
}

func TestSubmitRejectsMismatchedParentTaskID(t *testing.T) {
	hub := &fakeRouterHub{targets: []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{
		Id: "parent-session", Name: "parent", OwnerTaskId: "actual-parent-task",
	}}}}}
	router := newTaskRouter(hub, &fakeScheduler{}, newTaskIndex(), nil)
	raw, err := json.Marshal(task.Spec{Agent: "claude", Task: "work", ParentSessionID: "parent-session", ParentTaskID: "forged-task"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.SubmitSubtask(authlessCtx(), &pb.SubmitSubtaskRequest{SpecJson: raw})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("mismatched parent_task_id code=%v err=%v", status.Code(err), err)
	}
}

func TestScheduledSubmitCannotSupplyParentTurnID(t *testing.T) {
	router := newTaskRouter(&fakeRouterHub{}, &fakeScheduler{}, newTaskIndex(), nil)
	raw, err := json.Marshal(task.Spec{
		Agent:        "claude",
		Task:         "scheduled",
		ParentTurnID: "forged-parent-turn",
		ExecutionIdentity: &principal.ExecutionIdentity{
			IdentityMode: principal.IdentityModeLegacyHost,
			Source:       principal.SourceSchedule,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), scheduledSubmitContextKey{}, true)
	if _, err := router.SubmitSubtask(ctx, &pb.SubmitSubtaskRequest{SpecJson: raw}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("scheduled caller parent_turn_id error = %v, want InvalidArgument", err)
	}
}
