package bridge

import (
	"context"
	"encoding/json"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/spf13/viper"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutableExtensionSettingsAndNativeModels(t *testing.T) {
	const child = "AMUX_TEST_EXECUTABLE_EXTENSION"
	if os.Getenv(child) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestExecutableExtensionSettingsAndNativeModels$")
		cmd.Env = append(os.Environ(), child+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return
	}
	if err := config.RegisterExtension(config.Extension{AgentExecutables: map[string]string{"custom": "Custom"}}); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(filepath.Join(t.TempDir(), "config.toml"))
	action := &callback.CallBackAction{FormValue: map[string]any{"completion_enabled": "false", "custom_executable": " ~/bin/custom team "}}
	update, err := managementExperimentsSettingsFromCallback(action, config.ExperimentsSettings{})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := config.UpdateExperimentsSettings(update)
	if err != nil || saved.AgentExecutables["custom"] != "~/bin/custom team" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	saved, err = config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{})
	if err != nil || saved.AgentExecutables["custom"] != "~/bin/custom team" {
		t.Fatalf("old card cleared launcher: %+v %v", saved, err)
	}
	if _, err = config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{AgentExecutables: map[string]string{"custom": "bad\nname"}}); err == nil {
		t.Fatal("invalid setting accepted")
	}
	action.FormValue["custom_executable"] = false
	if _, err = managementExperimentsSettingsFromCallback(action, saved); err == nil {
		t.Fatal("non-string accepted")
	}
	card := renderManagementExperimentsCard(saved, config.ExperimentsSettings{})
	for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
		rendered := selectManagementCardLocale(card, locale)
		if !json.Valid([]byte(rendered)) || !strings.Contains(rendered, "custom_executable") || !strings.Contains(rendered, "~/bin/custom team") || strings.Contains(rendered, "management.launcher.") || strings.Contains(rendered, "%!s") {
			t.Fatalf("bad %s card: %s", locale, rendered)
		}
	}
	saved, err = config.UpdateExperimentsSettings(config.ExperimentsSettingsUpdate{AgentExecutables: map[string]string{"custom": ""}})
	if err != nil || saved.AgentExecutables["custom"] != "" {
		t.Fatalf("reset: %+v %v", saved, err)
	}
	viper.Set(config.AgentExecutableKey("custom"), "~/bin/custom team")
	hub := &nativeModelsHub{result: &pb.ListNativeModelsResponse{Status: "ready", Models: []*pb.NativeModel{{Model: "future", ReasoningEfforts: []string{"ultra"}, DefaultReasoningEffort: "ultra", Variants: []*pb.NativeModelVariant{{DisplayName: "Future Max", BackendVariant: "future_backend__max"}}}}}}
	b := &Bridge{hub: hub}
	b.SetExecutionIdentityResolver(func(string, string, string, bool) (LaunchIdentity, error) {
		return LaunchIdentity{Identity: &pb.ExecutionIdentity{EffectivePrincipalId: "p"}}, nil
	})
	base := managementState{DefaultRuntimeRef: "custom", DefaultModelRef: "saved", Runtimes: []agentprofile.RuntimeOption{{Ref: "custom", Models: []agentprofile.RuntimeModelOption{{Ref: "stale", Kind: agentprofile.ModelKindNative}, {Ref: "provider/model", Kind: agentprofile.ModelKindProvider}}}}}
	state := b.loadNativeModels(context.Background(), base, "owner", "custom")
	if hub.request.AgentType != "custom" || hub.request.AgentExecutable != "~/bin/custom team" || hub.target != "remote" {
		t.Fatalf("wrong launcher: %+v", hub.request)
	}
	models := state.Runtimes[0].Models
	if len(models) != 3 || models[0].Ref != "provider/model" || models[1].Ref != "future" || models[2].BackendVariant != "future_backend__max" {
		t.Fatalf("models: %+v", models)
	}
	// The opaque selection must survive a later launch without this editor snapshot.
	selected, err := agentprofile.ResolveRuntimeModel(agentprofile.RuntimeOption{Ref: "custom"}, models[2].Ref)
	if err != nil || selected.AgentModel != "future" || selected.BackendVariant != "future_backend__max" {
		t.Fatalf("variant: %+v %v", selected, err)
	}
	for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
		card := marshalNativeManagementCard(managementCardView{Elements: nativeModelElements(state, "custom", nil, managementActionSettingsEdit)})
		rendered := selectManagementCardLocale(card, locale)
		if strings.Contains(rendered, "Codex") || strings.Contains(rendered, "management.model.native.") || !strings.Contains(rendered, "Custom") {
			t.Fatalf("wrong labels: %s", rendered)
		}
	}
	// The responsive editor must discover registered launchers and isolate its cache
	// when a saved executable changes, rather than reuse the previous catalog.
	scope := b.prepareEditorModelScope(base, "owner", "card", "custom")
	if scope.native == nil || scope.native.request.AgentType != "custom" {
		t.Fatal("editor skipped registered launcher")
	}
	preview := loadingEditorModels(base, "custom")
	for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese} {
		card := marshalNativeManagementCard(managementCardView{Elements: nativeModelElements(preview, "custom", nil, managementActionSettingsEdit)})
		rendered := selectManagementCardLocale(card, locale)
		if strings.Contains(rendered, "%!") || strings.Contains(rendered, "management.model.native.") || !strings.Contains(rendered, "Custom") {
			t.Fatalf("bad loading label: %s", rendered)
		}
	}
	if preview.NativeModelsStatus != "loading" || len(preview.Runtimes[0].Models) != 1 {
		t.Fatal("loading preview retained stale native models")
	}
	state = b.refreshEditorModels(context.Background(), base, "owner", "card", "custom", "", "status", false, func(managementState) {})
	if state.NativeModelsStatus != "ready" || len(state.Runtimes[0].Models) != 3 {
		t.Fatalf("editor discovery failed: %+v", state)
	}
	viper.Set(config.AgentExecutableKey("custom"), "~/bin/another-launcher")
	if next := b.prepareEditorModelScope(base, "owner", "card", "custom"); next.key == scope.key {
		t.Fatal("editor cache ignored launcher change")
	}
	hub.result = &pb.ListNativeModelsResponse{Status: "ready", Models: []*pb.NativeModel{{Model: "replacement"}}}
	state = b.refreshEditorModels(context.Background(), base, "owner", "card", "custom", "", "status", false, func(managementState) {})
	if len(state.Runtimes[0].Models) != 2 || state.Runtimes[0].Models[1].Ref != "replacement" || hub.request.AgentExecutable != "~/bin/another-launcher" {
		t.Fatalf("editor reused old launcher catalog: %+v", state)
	}
	hub.result = &pb.ListNativeModelsResponse{Status: "signed_out"}
	state = b.loadNativeModels(context.Background(), state, "owner", "custom")
	if len(state.Runtimes[0].Models) != 1 || base.Runtimes[0].Models[0].Ref != "stale" {
		t.Fatal("stale catalog or shared snapshot mutation")
	}
}
