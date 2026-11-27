package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestGetActiveTurnReportsParentExecutionChoices(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	srv := &server{mgr: mgr, amuxDir: dir}
	sess, err := mgr.Create("parent", "claude", dir)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	launchSpec := launchSpecSnapshot(&pb.RunRequest{
		Name: "parent", AgentType: "claude",
		ProfileSpec: &pb.AgentProfileSpec{Model: "claude-opus-4-6", ReasoningEffort: "high"},
	})
	if err := mgr.RecordLaunchParams(sess, []string{"--effort", "high"}, nil, "cli", "local", "builtin/claude", "claude-opus-4-6", launchSpec); err != nil {
		t.Fatalf("record launch params: %v", err)
	}
	resp, err := srv.GetActiveTurn(context.Background(), &pb.ActiveTurnRequest{Name: "parent"})
	if err != nil {
		t.Fatalf("GetActiveTurn: %v", err)
	}
	if resp.GetEffectiveModel() != "claude-opus-4-6" || resp.GetReasoningEffort() != "high" {
		t.Fatalf("active turn = %+v, want the session model and effort", resp)
	}
	if resp.GetSessionId() != sess.ID {
		t.Fatalf("session id = %q, want %q", resp.GetSessionId(), sess.ID)
	}
	if resp.GetModelVariantRef() != "" {
		t.Fatalf("unexpected variant for an existing session without a saved variant: %q", resp.GetModelVariantRef())
	}
}

func TestGetActiveTurnReportsVariantForMatchingModel(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	srv := &server{mgr: mgr, amuxDir: dir}
	sess, err := mgr.Create("parent", "codex", dir)
	if err != nil {
		t.Fatal(err)
	}
	baseModel := "example-model"
	variantRef := agentprofile.NativeVariantRef(baseModel, "example-model__max")
	launchSpec := launchSpecSnapshot(&pb.RunRequest{
		ProfileSpec: &pb.AgentProfileSpec{
			Model: baseModel, ConfiguredModel: variantRef, RuntimeRef: "codex", ReasoningEffort: "xhigh",
		},
	})
	for _, test := range []struct {
		name, model, wantVariant string
	}{
		{"selected model", baseModel, variantRef},
		{"provider reports a different model", "other-model", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "local", "builtin/codex", test.model, launchSpec); err != nil {
				t.Fatal(err)
			}
			resp, err := srv.GetActiveTurn(context.Background(), &pb.ActiveTurnRequest{Name: "parent"})
			if err != nil {
				t.Fatal(err)
			}
			if resp.GetEffectiveModel() != test.model || resp.GetReasoningEffort() != "xhigh" || resp.GetModelVariantRef() != test.wantVariant {
				t.Fatalf("active turn = %+v, want model %q, effort xhigh and variant %q", resp, test.model, test.wantVariant)
			}
		})
	}
}
