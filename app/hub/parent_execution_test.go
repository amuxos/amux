package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func parentExecutionTargets(agentType string) []*pb.Target {
	return []*pb.Target{{Id: "t1", Sessions: []*pb.Session{{Id: "parent-session", Name: "parent-name", AgentType: agentType}}}}
}

func newParentExecutionRouter(t *testing.T, hub *fakeRouterHub, resolve func(*task.Spec) error) *taskRouter {
	t.Helper()
	router := newTaskRouter(hub, &fakeScheduler{acquireFn: func(registry.ScheduleRequest) (*registry.Placement, error) {
		return &registry.Placement{TargetID: "t1", ReservationId: "rsv1"}, nil
	}}, newTaskIndex(), nil)
	router.resolveProfile = resolve
	return router
}

func parentExecutionHub(t *testing.T, routed *task.Spec, variant string) *fakeRouterHub {
	t.Helper()
	return &fakeRouterHub{
		targets: parentExecutionTargets("claude"),
		routeFn: func(_, method string, payload interface{}) (*pb.TunnelMsg, error) {
			switch method {
			case pb.TaskMethodResolveParentTurn:
				return respJSON(t, &pb.ActiveTurnResponse{Active: true, TurnId: "parent-turn", SessionId: "parent-session",
					EffectiveModel: "parent-model", ReasoningEffort: "high", ModelVariantRef: variant}), nil
			case pb.TaskMethodSubmit:
				req, ok := payload.(*pb.SubmitSubtaskRequest)
				if !ok {
					return nil, fmt.Errorf("payload = %T, want SubmitSubtaskRequest", payload)
				}
				if err := json.Unmarshal(req.SpecJson, routed); err != nil {
					return nil, fmt.Errorf("decode routed spec: %w", err)
				}
				return respJSON(t, &pb.SubmitSubtaskResponse{TaskJson: []byte(`{"id":"task-1","status":"pending"}`)}), nil
			}
			return nil, fmt.Errorf("unexpected route %s", method)
		},
	}
}

func submitForInheritance(t *testing.T, router *taskRouter, spec task.Spec) {
	t.Helper()
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.SubmitSubtask(context.Background(), &pb.SubmitSubtaskRequest{SpecJson: specJSON}); err != nil {
		t.Fatalf("SubmitSubtask: %v", err)
	}
}

func TestRouterSubmitInheritsParentExecutionDefaults(t *testing.T) {
	var resolved []task.Spec
	var routed task.Spec
	router := newParentExecutionRouter(t, parentExecutionHub(t, &routed, ""), func(spec *task.Spec) error {
		resolved = append(resolved, *spec)
		spec.Agent = "claude"
		spec.ResolvedProfile = &pb.AgentProfileSpec{Type: "claude", Model: spec.Model, ReasoningEffort: spec.ReasoningEffort}
		return nil
	})
	submitForInheritance(t, router, task.Spec{Agent: "expert", Task: "do work", ParentSessionID: "parent-session"})
	if len(resolved) != 2 {
		t.Fatalf("resolve calls = %d, want the probe and the inherited re-resolution", len(resolved))
	}
	if resolved[1].Model != "parent-model" || resolved[1].ReasoningEffort != "high" {
		t.Fatalf("inherited spec = %+v, want the parent model and effort", resolved[1])
	}
	if routed.Model != "parent-model" || routed.ReasoningEffort != "high" || routed.ResolvedProfile == nil {
		t.Fatalf("routed spec = %+v, want inherited execution choices", routed)
	}
}

func TestRouterSubmitDoesNotInheritAcrossEngines(t *testing.T) {
	var resolved []task.Spec
	var routed task.Spec
	hub := parentExecutionHub(t, &routed, "parent-variant")
	hub.targets = parentExecutionTargets("codex")
	router := newParentExecutionRouter(t, hub, func(spec *task.Spec) error {
		resolved = append(resolved, *spec)
		spec.Agent = "claude"
		spec.ResolvedProfile = &pb.AgentProfileSpec{Type: "claude", Model: spec.Model}
		return nil
	})
	submitForInheritance(t, router, task.Spec{Agent: "expert", Task: "do work", ParentSessionID: "parent-session"})
	if len(resolved) != 1 {
		t.Fatalf("resolve calls = %d, want the caller default only", len(resolved))
	}
	if routed.Model != "" {
		t.Fatalf("routed model = %q, want the profile default", routed.Model)
	}
}

func TestRouterSubmitKeepsExplicitCallerModel(t *testing.T) {
	var resolved []task.Spec
	var routed task.Spec
	router := newParentExecutionRouter(t, parentExecutionHub(t, &routed, "parent-variant"), func(spec *task.Spec) error {
		resolved = append(resolved, *spec)
		spec.Agent = "claude"
		spec.ResolvedProfile = &pb.AgentProfileSpec{Type: "claude", Model: spec.Model}
		return nil
	})
	submitForInheritance(t, router, task.Spec{Agent: "expert", Model: "chosen-model", Task: "do work", ParentSessionID: "parent-session"})
	if len(resolved) != 1 || routed.Model != "chosen-model" {
		t.Fatalf("resolve calls = %d routed model = %q, want the caller model", len(resolved), routed.Model)
	}
}

func TestRouterSubmitDropsInheritedEffortThatDoesNotResolve(t *testing.T) {
	var resolved []task.Spec
	var routed task.Spec
	router := newParentExecutionRouter(t, parentExecutionHub(t, &routed, ""), func(spec *task.Spec) error {
		resolved = append(resolved, *spec)
		if strings.TrimSpace(spec.ReasoningEffort) == "high" && strings.TrimSpace(spec.Model) == "parent-model" {
			return errors.New("reasoning effort high is not supported")
		}
		spec.Agent = "claude"
		spec.ResolvedProfile = &pb.AgentProfileSpec{Type: "claude", Model: spec.Model, ReasoningEffort: spec.ReasoningEffort}
		return nil
	})
	submitForInheritance(t, router, task.Spec{Agent: "expert", Task: "do work", ParentSessionID: "parent-session"})
	if len(resolved) != 3 {
		t.Fatalf("resolve calls = %d, want probe, inherited pair and inherited model", len(resolved))
	}
	if routed.Model != "parent-model" || routed.ReasoningEffort != "" {
		t.Fatalf("routed spec = %+v, want the inherited model without the refused effort", routed)
	}
}

func TestRouterSubmitInheritsParentModelVariant(t *testing.T) {
	variantRef := agentprofile.NativeVariantRef("parent-model", "parent-model__max")
	for _, test := range []struct {
		name, rejectedModel, wantModel string
		wantVariant                    bool
	}{
		{"inherited variant", "", variantRef, true},
		{"variant unavailable", variantRef, "parent-model", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var routed task.Spec
			hub := parentExecutionHub(t, &routed, variantRef)
			hub.targets = parentExecutionTargets("codex")
			router := newParentExecutionRouter(t, hub, func(spec *task.Spec) error {
				if test.rejectedModel != "" && spec.Model == test.rejectedModel {
					return errors.New("model variant unavailable")
				}
				return agentprofile.ResolveTaskSpec(spec, "")
			})
			submitForInheritance(t, router, task.Spec{Agent: "builtin/codex", Task: "do work", ParentSessionID: "parent-session"})
			if routed.ResolvedProfile.GetConfiguredModel() != test.wantModel || routed.ResolvedProfile.GetReasoningEffort() != "high" {
				t.Fatalf("routed profile = %+v, want model %q and effort high", routed.ResolvedProfile, test.wantModel)
			}
			var variant string
			for _, arg := range routed.ExtraArgs {
				if value, ok := agentprofile.CodexConfigValue(arg, "model_backend_variant"); ok {
					variant = value
				}
			}
			wantVariant := ""
			if test.wantVariant {
				wantVariant = "parent-model__max"
			}
			if variant != wantVariant {
				t.Fatalf("routed backend variant = %q, want %q", variant, wantVariant)
			}
		})
	}
}
