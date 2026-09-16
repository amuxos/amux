package bridge

import (
	"context"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/config"
)

func TestGroupTopicDefaultRuntimeValidatesAndLaunchesReasoningEffort(t *testing.T) {
	b, hub, notices := groupRouteTestBridge(t)
	draft, err := b.groupSettingsDraft("default-runtime")
	if err != nil {
		t.Fatal(err)
	}
	draft.InteractionMode, draft.DefaultAgent = config.FeishuInteractionThread, "builtin/codex"
	draft.DefaultRuntimeRef, draft.DefaultModelRef = "", "group-native-model"
	draft.DefaultArgs = []string{"--verbose"}
	draft.DefaultReasoningEffort = "invalid"
	if _, err := b.applyGroupSettings(context.Background(), draft, draft.Version, "owner"); err == nil || !strings.Contains(err.Error(), "reasoning effort") {
		t.Fatalf("invalid default-runtime reasoning was accepted: %v", err)
	}
	if saved, err := b.db.ReadGroupSettings(b.currentBridgeId(), draft.ChatID); err != nil || saved != nil {
		t.Fatalf("invalid draft persisted: %+v, err=%v", saved, err)
	}
	draft.DefaultReasoningEffort = "high"
	if _, err := b.applyGroupSettings(context.Background(), draft, draft.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	routeGroupTestInput(t, b, draft.ChatID, "default-runtime-root", "default-runtime-root")
	runs := groupTestRuns(hub)
	if len(runs) != 1 {
		t.Fatalf("runs=%d notices=%v", len(runs), *notices)
	}
	run := runs[0]
	if run.AgentProfile != "builtin/codex" || run.AgentType != "codex" || run.ProfileSpec.GetRuntimeRef() != "" || run.ProfileSpec.GetModel() != "group-native-model" || run.ProfileSpec.GetReasoningEffort() != "high" {
		t.Fatalf("wrong default-runtime group launch: %+v", run)
	}
	args := strings.Join(run.ExtraArgs, " ")
	if !strings.Contains(args, `model_reasoning_effort="high"`) || !strings.Contains(args, "--verbose") {
		t.Fatalf("group reasoning/default args not materialized: %q", run.ExtraArgs)
	}
}
