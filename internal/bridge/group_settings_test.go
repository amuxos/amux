package bridge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type lockOrderManagementActions struct {
	*fakeManagementAgentPackActions
	mu       sync.Mutex
	attempts chan struct{}
	acquired chan struct{}
	release  chan struct{}
}

func (a *lockOrderManagementActions) WithRegistryStable(commit func() error) error {
	a.attempts <- struct{}{}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acquired <- struct{}{}
	select {
	case <-a.release:
	default:
		<-a.release
	}
	return commit()
}

func (a *lockOrderManagementActions) WithRegistryStableContext(ctx context.Context, commit func() error) error {
	return a.WithRegistryStable(commit)
}

func TestGroupSettingsAndWatchExecutionUseRegistryFirstLockOrder(t *testing.T) {
	enableGroupSettingsExperiment(t)
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	actions := &lockOrderManagementActions{
		fakeManagementAgentPackActions: &fakeManagementAgentPackActions{},
		attempts:                       make(chan struct{}, 2),
		acquired:                       make(chan struct{}, 2),
		release:                        make(chan struct{}),
	}
	b.managementPackActions = actions

	watchDone := make(chan error, 1)
	go func() {
		watchDone <- b.saveManagementGroupWatchExecution(managementGroupWatchDraft{ThinkingProcess: config.FeishuThinkingProcessCollapsed})
	}()
	<-actions.attempts
	<-actions.acquired

	draft, err := b.groupSettingsDraft("group-a")
	if err != nil {
		t.Fatal(err)
	}
	settingsDone := make(chan error, 1)
	go func() {
		_, err := b.applyGroupSettings(context.Background(), draft, 0, "owner")
		settingsDone <- err
	}()
	<-actions.attempts
	close(actions.release)

	for name, done := range map[string]<-chan error{"watch": watchDone, "settings": settingsDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s save: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s save deadlocked", name)
		}
	}
}

func TestGroupSettingsDraftAndIndependentLaunchDefaults(t *testing.T) {
	enableGroupSettingsExperiment(t)
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	hub.targets = []*pb.Target{{Id: "local", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	draft, err := b.groupSettingsDraft("group-a")
	if err != nil {
		t.Fatal(err)
	}
	if draft.Version != 0 || draft.DefaultAgent != "builtin/codex" {
		t.Fatalf("draft = %+v", draft)
	}
	if stored, err := b.db.ReadGroupSettings(b.currentBridgeId(), "group-a"); err != nil || stored != nil {
		t.Fatalf("read created group policy: %+v, %v", stored, err)
	}
	draft.DefaultRuntimeRef = "codex"
	draft.DefaultModelRef = "custom-native-model"
	draft.DefaultReasoningEffort = "high"
	draft.ThinkingProcess = config.FeishuThinkingProcessHidden
	draft.DefaultTarget = "local"
	draft.DefaultCwd = "/project/group-a"
	draft.IdleTimeoutSeconds = 180
	saved, err := b.applyGroupSettings(context.Background(), draft, draft.Version, "owner")
	if err != nil {
		t.Fatal(err)
	}
	setBridgeTestBotDefaultAgent(t, b, "builtin/claude")
	got, err := b.groupSettingsDraft("group-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultAgent != "builtin/codex" || got.DefaultModelRef != "custom-native-model" || got.DefaultReasoningEffort != "high" || got.DefaultCwd != "/project/group-a" || got.Version != saved.Version {
		t.Fatalf("saved group followed Bot changes: %+v", got)
	}
	other, err := b.groupSettingsDraft("group-b")
	if err != nil {
		t.Fatal(err)
	}
	if other.DefaultAgent != "builtin/claude" || other.Version != 0 {
		t.Fatalf("other group = %+v", other)
	}
}

func TestGroupSettingsRejectInvalidSaveWithoutPartialWorkspaceWrite(t *testing.T) {
	enableGroupSettingsExperiment(t)
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	draft, err := b.groupSettingsDraft("group-a")
	if err != nil {
		t.Fatal(err)
	}
	draft.Mode = WorkspaceModeShared
	draft.DefaultRuntimeRef = "missing/runtime"
	if _, err := b.applyGroupSettings(context.Background(), draft, 0, "owner"); err == nil {
		t.Fatal("accepted invalid runtime")
	}
	if got, err := b.db.ReadGroupSettings(b.currentBridgeId(), "group-a"); err != nil || got != nil {
		t.Fatalf("partial group save: %+v %v", got, err)
	}
	ws, err := b.db.ReadWorkspace(b.currentBridgeId(), "group-a")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Mode != WorkspaceModeOwnerOnly {
		t.Fatalf("partially changed workspace: %+v", ws)
	}
	if _, err := b.applyGroupSettings(context.Background(), draft, 0, "member"); err == nil {
		t.Fatal("member saved group configuration")
	}
}

func TestGroupSettingsRejectUnsupportedIsolationAndWatchConflict(t *testing.T) {
	enableGroupSettingsExperiment(t)
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	b.SetIsolationSupportChecker(func() (bool, string) { return false, "no isolated runtime" })
	draft, err := b.groupSettingsDraft("group-a")
	if err != nil {
		t.Fatal(err)
	}
	draft.IsolationRequired = true
	if _, err := b.applyGroupSettings(context.Background(), draft, 0, "owner"); err == nil || !strings.Contains(err.Error(), "no isolated runtime") {
		t.Fatalf("isolation save = %v", err)
	}
	draft.IsolationRequired = false
	draft.InteractionMode = config.FeishuInteractionSingleton
	b.setGroupWatchConfig(config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"group-a"}})
	if _, err := b.applyGroupSettings(context.Background(), draft, 0, "owner"); err == nil {
		t.Fatal("accepted singleton with active group watch")
	}
}
