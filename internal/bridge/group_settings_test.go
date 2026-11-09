package bridge

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/viper"
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

func requireGroupSettingsNotWritten(t *testing.T, b *Bridge, chatID string, configBefore []byte) {
	t.Helper()
	if got, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID); err != nil || got != nil {
		t.Fatalf("canceled save wrote group settings: %+v, %v", got, err)
	}
	for _, table := range []string{"feishu_workspaces", "feishu_bot_profiles", "feishu_profile_defaults"} {
		var count int
		if err := b.db.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("canceled save wrote %s: count=%d err=%v", table, count, err)
		}
	}
	configAfter, err := os.ReadFile(viper.ConfigFileUsed())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(configBefore, configAfter) {
		t.Fatal("canceled save modified the installation config")
	}
}

func TestGroupSettingsContextBoundsRegistryWait(t *testing.T) {
	loadBridgeTestConfig(t, "")
	enableGroupSettingsExperiment(t)
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	draft, err := b.groupSettingsDraft("group-a")
	if err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(viper.ConfigFileUsed())
	if err != nil {
		t.Fatal(err)
	}
	b.managementPackActions = &contextBlockingManagementAgentPackActions{
		fakeManagementAgentPackActions: &fakeManagementAgentPackActions{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	saved, err := b.applyGroupSettings(ctx, draft, 0, "owner")
	if !errors.Is(err, context.DeadlineExceeded) || saved != nil {
		t.Fatalf("registry wait returned settings=%+v err=%v", saved, err)
	}
	requireGroupSettingsNotWritten(t, b, draft.ChatID, configBefore)
}

func TestGroupSettingsContextBoundsLockWaits(t *testing.T) {
	for _, held := range []string{"group-watch", "route-map", "route", "database"} {
		t.Run(held, func(t *testing.T) {
			loadBridgeTestConfig(t, "")
			enableGroupSettingsExperiment(t)
			b, _ := newBridgeForInputTest(t)
			b.secrets = &Secrets{OpenId: "owner"}
			draft, err := b.groupSettingsDraft("group-a")
			if err != nil {
				t.Fatal(err)
			}
			draft.Mode = WorkspaceModeShared
			configBefore, err := os.ReadFile(viper.ConfigFileUsed())
			if err != nil {
				t.Fatal(err)
			}
			actions := &lockOrderManagementActions{
				fakeManagementAgentPackActions: &fakeManagementAgentPackActions{},
				attempts:                       make(chan struct{}, 1), acquired: make(chan struct{}, 1), release: make(chan struct{}),
			}
			close(actions.release)
			b.managementPackActions = actions
			key := groupSettingsRouteKey(b.currentBridgeId(), draft.ChatID)
			var release func()
			switch held {
			case "group-watch":
				b.groupWatchSettingsMu.Lock()
				release = b.groupWatchSettingsMu.Unlock
			case "route-map":
				b.routeMu.Lock()
				release = b.routeMu.Unlock
			case "route":
				release = b.lockRoute(key)
			case "database":
				conn, err := b.db.db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				release = func() { _ = conn.Close() }
			}
			release = sync.OnceFunc(release)
			defer release()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				saved, err := b.applyGroupSettings(ctx, draft, 0, "owner")
				if saved != nil {
					err = errors.New("canceled save returned non-nil settings")
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%s wait error = %v, want deadline exceeded", held, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("save did not cancel while %s remained occupied", held)
			}
			if !actions.mu.TryLock() {
				t.Fatal("canceled save retained the registry lock")
			}
			actions.mu.Unlock()
			release()
			if !b.groupWatchSettingsMu.TryLock() {
				t.Fatal("canceled save retained the group-watch lock")
			}
			b.groupWatchSettingsMu.Unlock()
			lockCtx, lockCancel := context.WithTimeout(context.Background(), time.Second)
			defer lockCancel()
			unlock, err := b.lockGroupSettingsRouteContext(lockCtx, key)
			if err != nil {
				t.Fatalf("canceled save retained the route lock: %v", err)
			}
			unlock()
			requireGroupSettingsNotWritten(t, b, draft.ChatID, configBefore)
		})
	}
}

func TestGroupSettingsContextCanceledDuringValidationDoesNotWrite(t *testing.T) {
	loadBridgeTestConfig(t, "")
	enableGroupSettingsExperiment(t)
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	draft, err := b.groupSettingsDraft("group-a")
	if err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(viper.ConfigFileUsed())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	draft.IsolationRequired = true
	b.SetIsolationSupportChecker(func() (bool, string) {
		cancel()
		return true, ""
	})
	if saved, err := b.applyGroupSettings(ctx, draft, 0, "owner"); !errors.Is(err, context.Canceled) || saved != nil {
		t.Fatalf("validation cancellation = settings=%+v err=%v", saved, err)
	}
	requireGroupSettingsNotWritten(t, b, draft.ChatID, configBefore)
}

func TestGroupSettingsDraftContextBoundsDatabaseWait(t *testing.T) {
	loadBridgeTestConfig(t, "")
	enableGroupSettingsExperiment(t)
	b, _ := newBridgeForInputTest(t)
	configBefore, err := os.ReadFile(viper.ConfigFileUsed())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := b.db.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if draft, err := b.groupSettingsDraftContext(ctx, "group-a"); !errors.Is(err, context.DeadlineExceeded) || draft != nil {
		t.Fatalf("draft database wait = %+v, %v", draft, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	requireGroupSettingsNotWritten(t, b, "group-a", configBefore)
}
