package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/viper"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func consoleTestBinding(t *testing.T, b *Bridge, chatID, root string) {
	t.Helper()
	if err := b.db.UpsertChat(LarkChat{BridgeId: b.currentBridgeId(), ChatID: chatID, RootMsgID: root, TargetID: "local", SessionName: root, Status: ChatStatusActive, DisplayTitle: "Topic title, not group name"}); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleManagementReadsDoNotSeedOrCallFeishu(t *testing.T) {
	enableGroupSettingsExperiment(t)
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner", AppId: "test-app", AppSecret: "do-not-return-secret", BotName: "Test Bot"}
	b.chatNameLookup = func(context.Context, string) (string, error) { t.Fatal("Console read called Feishu"); return "", nil }
	consoleTestBinding(t, b, "oc_group", "root-1")
	consoleTestBinding(t, b, "oc_group", "root-2")
	consoleTestBinding(t, b, "oc_private", "root-private")
	if _, err := b.db.db.Exec(`INSERT INTO feishu_workspaces(bridge_id,chat_id,kind,mode,default_agent,default_target,default_cwd,default_model,default_args_json,thread_context_limit,max_sessions_total,max_sessions_per_user,idle_timeout_seconds,updated_at) VALUES(?,?,'private','owner_only','','','','','[]',20,500,0,0,0)`, b.currentBridgeId(), "oc_private"); err != nil {
		t.Fatal(err)
	}
	bot, err := b.ConsoleBot(context.Background())
	if err != nil || bot.Name != "Test Bot" || bot.Settings.InteractionMode != "thread" {
		t.Fatalf("bot=%+v err=%v", bot, err)
	}
	groups, err := b.ConsoleGroups(context.Background())
	if err != nil || len(groups) != 1 || groups[0].ChatID != "oc_group" || groups[0].Title != "oc_group" {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	out, err := b.ConsoleGroupSettings(context.Background(), "oc_group")
	if err != nil || out.Settings.Version != 0 || out.SettingsSource != "bot_defaults" {
		t.Fatalf("draft=%+v err=%v", out, err)
	}
	payload, _ := json.Marshal([]any{bot, groups, out})
	if strings.Contains(string(payload), "do-not-return-secret") || strings.Contains(string(payload), "app_secret") {
		t.Fatalf("secret leaked: %s", payload)
	}
	for _, table := range []string{"feishu_bot_profiles", "feishu_group_settings"} {
		var count int
		if err := b.db.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("read seeded %s count=%d err=%v", table, count, err)
		}
	}
	if len(hub.routes) != 0 {
		t.Fatal("read drove execution")
	}
	if _, err := b.ConsoleGroupSettings(context.Background(), "oc_unknown"); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown group=%v", err)
	}
	if _, err := b.ConsoleGroupSettings(context.Background(), "oc_private"); status.Code(err) != codes.NotFound {
		t.Fatalf("private group=%v", err)
	}
}

func TestConsoleManagementBotUsesLiveTransitionAndVersion(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner", AppSecret: "secret"}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), "thread")
	if err != nil {
		t.Fatal(err)
	}
	b.managementPackActions = &fakeManagementAgentPackActions{}
	input := ConsoleBotSave{Profile: b.ProfileName(), ExpectedVersion: state.Version, RequestID: "console-save", Settings: &ConsoleBotSettings{DefaultAgent: "builtin/claude", InteractionMode: "thread"}}
	out, err := b.ConsoleSaveBot(context.Background(), input)
	if err != nil || out.Status != "success" || out.Bot.Version != state.Version+1 {
		t.Fatalf("save=%+v err=%v", out, err)
	}
	live, err := b.currentBotProfileStateReadOnly()
	if err != nil || live.DefaultAgent != "builtin/claude" || live.Version != out.Bot.Version {
		t.Fatalf("live state=%+v err=%v", live, err)
	}
	if projected, err := b.db.ProfileDefaultAgent(b.currentBridgeId()); err != nil || projected != "builtin/claude" {
		t.Fatalf("transition did not project legacy settings: %q %v", projected, err)
	}
	input.Settings.DefaultAgent = "builtin/codex"
	out, err = b.ConsoleSaveBot(context.Background(), input)
	if err != nil || out.Status != "state_changed" || out.Bot.Settings.DefaultAgent != "builtin/claude" {
		t.Fatalf("stale save=%+v err=%v", out, err)
	}
	if len(hub.routes) != 0 {
		t.Fatal("thread settings unexpectedly drove execution")
	}
}

func TestConsoleManagementConfirmationIsBoundAndOneUse(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	state := seedSingletonBotSettings(t, b, "builtin/codex", "singleton")
	input := ConsoleBotSave{Profile: b.ProfileName(), ExpectedVersion: state.Version, RequestID: "console-confirm", Settings: &ConsoleBotSettings{DefaultAgent: "builtin/claude", InteractionMode: "singleton"}}
	out, err := b.ConsoleSaveBot(context.Background(), input)
	if err != nil || out.Status != "needs_confirmation" || out.ConfirmationToken == "" || out.Impact == nil || out.Impact.English == "" || out.Impact.Chinese == "" {
		t.Fatalf("confirmation=%+v err=%v", out, err)
	}
	input.Confirmed = true
	input.ConfirmationToken = out.ConfirmationToken
	input.RequestID = "different-request"
	if _, err = b.ConsoleSaveBot(context.Background(), input); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unbound request accepted: %v", err)
	}
	input.RequestID = "console-confirm"
	// No matching target in the fake hub: the real transition must fail rather
	// than report saved. A valid token is nevertheless consumed exactly once.
	if _, err = b.ConsoleSaveBot(context.Background(), input); status.Convert(err).Message() != "console_bot_transition_failed" {
		t.Fatalf("did not call live transition: %v", err)
	}
	if _, err = b.ConsoleSaveBot(context.Background(), input); status.Convert(err).Message() != "console_confirmation_expired" {
		t.Fatalf("replayed confirmation: %v", err)
	}
	live, _ := b.currentBotProfileStateReadOnly()
	if live.Version != state.Version || live.DefaultAgent != "builtin/codex" {
		t.Fatalf("failed confirmation mutated state: %+v", live)
	}
}

func TestConsoleManagementRecoveryAndContextStatuses(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	hub.targets = []*pb.Target{{Id: "local", Name: "local"}}
	setBridgeTestBotDefaultAgent(t, b, "builtin/codex")
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), config.FeishuInteractionThread)
	if err != nil {
		t.Fatal(err)
	}
	input := ConsoleBotSave{Profile: b.ProfileName(), ExpectedVersion: state.Version, Settings: &ConsoleBotSettings{DefaultAgent: "builtin/codex", InteractionMode: "singleton"}}
	out, err := b.ConsoleSaveBot(context.Background(), input)
	if err != nil || out.Status != "needs_recovery_choice" || out.ConfirmationToken == "" {
		t.Fatalf("recovery=%+v err=%v", out, err)
	}
	input.RecoveryAction = "create_blank"
	if _, err := b.ConsoleSaveBot(context.Background(), input); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unconfirmed recovery: %v", err)
	}
	input.RecoveryAction = ""
	b.managementPackActions = &contextBlockingManagementAgentPackActions{fakeManagementAgentPackActions: &fakeManagementAgentPackActions{}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := b.ConsoleSaveBot(ctx, input); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("context=%v", err)
	}
}

func TestConsoleManagementGroupSharesLiveValidationAndCAS(t *testing.T) {
	b, _ := groupSettingsCardTestBridge(t)
	consoleTestBinding(t, b, "oc_group", "root")
	draft, err := b.ConsoleGroupSettings(context.Background(), "oc_group")
	if err != nil {
		t.Fatal(err)
	}
	settings := *draft.Settings
	settings.Mode = "shared"
	settings.UpdatedBy = "forged-caller"
	out, err := b.ConsoleSaveGroup(context.Background(), "oc_group", &settings, 0)
	if err != nil || out.Status != "success" || out.Settings.Version != 1 || out.Settings.UpdatedBy != "owner" {
		t.Fatalf("save=%+v err=%v", out, err)
	}
	saved, err := b.db.ReadGroupSettings(b.currentBridgeId(), "oc_group")
	if err != nil || saved.Mode != "shared" || saved.Version != out.Settings.Version {
		t.Fatalf("live record=%+v err=%v", saved, err)
	}
	ws, err := b.db.ReadWorkspace(b.currentBridgeId(), "oc_group")
	if err != nil || ws.Mode != "shared" {
		t.Fatalf("workspace not atomically projected: %+v %v", ws, err)
	}
	out, err = b.ConsoleSaveGroup(context.Background(), "oc_group", &settings, 0)
	if err != nil || out.Status != "state_changed" || out.Settings.Version != 1 {
		t.Fatalf("conflict=%+v err=%v", out, err)
	}
	settings.BridgeID = "feishu:other"
	if _, err = b.ConsoleSaveGroup(context.Background(), "oc_group", &settings, 1); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("cross-bridge scope: %v", err)
	}
	settings.BridgeID = b.currentBridgeId()
	settings.DefaultTarget = "unknown-target"
	if _, err = b.ConsoleSaveGroup(context.Background(), "oc_group", &settings, 1); status.Convert(err).Message() != "console_group_save_failed" {
		t.Fatalf("live target validation bypassed: %v", err)
	}
	settings.DefaultTarget = ""
	viper.Set("bridge.feishu.group_settings.enabled", false)
	groups, err := b.ConsoleGroups(context.Background())
	if err != nil || len(groups) != 1 || len(groups[0].Actions) != 0 {
		t.Fatalf("disabled list=%+v err=%v", groups, err)
	}
	if _, err = b.ConsoleSaveGroup(context.Background(), "oc_group", &settings, 1); status.Convert(err).Message() != "console_group_settings_disabled" {
		t.Fatalf("experiment bypassed: %v", err)
	}
}
