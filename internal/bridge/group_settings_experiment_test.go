package bridge

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/spf13/viper"
)

func enableGroupSettingsExperiment(t *testing.T) {
	t.Helper()
	previous := viper.GetBool("bridge.feishu.group_settings.enabled")
	viper.Set("bridge.feishu.group_settings.enabled", true)
	t.Cleanup(func() { viper.Set("bridge.feishu.group_settings.enabled", previous) })
}

func TestGroupSettingsExperimentDefaultOffPreservesLegacyManagement(t *testing.T) {
	for _, body := range []string{"", "[bridge.feishu.group_settings]\nenabled = false\n"} {
		t.Run(body, func(t *testing.T) {
			loadBridgeTestConfig(t, body)
			b, hub := newBridgeForInputTest(t)
			b.secrets = &Secrets{OpenId: "owner", AppSecret: "test-secret"}
			b.chatNameLookup = func(context.Context, string) (string, error) { return "Test group", nil }
			b.threadNotice = func(context.Context, string, string) error { return nil }
			var card string
			b.dmInteractive = func(_ context.Context, _, content string) (string, string, error) {
				card = content
				return "dm", "card", nil
			}
			if err := b.handleMessage(context.Background(), feishuMessageEvent("group", "a", "root", "owner", "/amux")); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(card, "将本群加入值守") || strings.Contains(card, "修改本群设置") {
				t.Fatalf("disabled group editor or missing legacy watch: %s", card)
			}
			if _, err := b.groupSettingsDraft("a"); err == nil || !strings.Contains(err.Error(), "实验") {
				t.Fatalf("disabled draft = %v", err)
			}
			if saved, _ := b.db.ReadGroupSettings(b.currentBridgeId(), "a"); saved != nil || len(hub.routes) != 0 {
				t.Fatal("read-only management created group settings or execution")
			}
		})
	}
}

func TestGroupSettingsExperimentDisabledRejectsOldCardsAndOperatorWrites(t *testing.T) {
	b, _ := groupSettingsCardTestBridge(t)
	settings := groupSettingsTestDraft(t, b, "a")
	settings.InteractionMode = config.FeishuInteractionSingleton
	settings, err := b.applyGroupSettings(context.Background(), settings, 0, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.UpsertChat(LarkChat{BridgeId: settings.BridgeID, ChatID: "a", RootMsgID: "anchor", TargetID: "local", SessionName: "existing", Status: ChatStatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := b.db.SetGroupSessionRoot(settings.BridgeID, "a", settings.Version, "anchor"); err != nil {
		t.Fatal(err)
	}
	viper.Set("bridge.feishu.group_settings.enabled", false)
	var notices []string
	b.threadNotice = func(_ context.Context, _, text string) error { notices = append(notices, text); return nil }
	for _, command := range []string{"/workspace interaction thread", "/workspace default-agent builtin/claude"} {
		if err := b.handleWorkspaceRootCommandReply(context.Background(), "a", "command", "owner", command); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.handleNaturalWorkspaceDefaultAgent(context.Background(), "a", "natural", "owner", "builtin/claude"); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 3 {
		t.Fatalf("disabled command notices = %v", notices)
	}
	for _, notice := range notices {
		if !strings.Contains(notice, "实验") {
			t.Fatalf("missing experiment guidance: %s", notice)
		}
	}
	for _, action := range []string{managementActionGroupSettingsOpen, managementActionGroupSettingsEdit, managementActionGroupSettingsSave, managementActionGroupSettingsWatch, managementActionGroupConversationNew} {
		response := groupSettingsTestCallback(t, b, groupSettingsTestAction(b, settings, action, nil), "error")
		if !strings.Contains(response.Toast.Content, "实验") {
			t.Fatalf("disabled callback: %+v", response)
		}
	}
	if _, err := b.applyGroupSettings(context.Background(), settings, settings.Version, "owner"); err == nil {
		t.Fatal("disabled save succeeded")
	}
	if err := b.newGroupConversation("a", settings.Version, "owner"); err == nil {
		t.Fatal("disabled reset succeeded")
	}
	path := filepath.Join(t.TempDir(), "bridge.db")
	if _, _, err := GroupSettingsInDB(path, "default", settings.BridgeID, "a"); err == nil {
		t.Fatal("disabled operator read succeeded")
	}
	if _, err := SaveGroupSettingsInDB(path, "default", settings, settings.Version); err == nil {
		t.Fatal("disabled operator save succeeded")
	}
	if err := NewGroupConversationInDB(path, "default", settings.BridgeID, "a", settings.Version); err == nil {
		t.Fatal("disabled operator reset succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled command opened a database: %v", err)
	}
	after, _ := b.db.ReadGroupSettings(settings.BridgeID, "a")
	root, _ := b.db.GetGroupSessionRoot(settings.BridgeID, "a")
	if !reflect.DeepEqual(settings, after) || root != "anchor" {
		t.Fatal("disabled operation changed saved state")
	}
}

func TestGroupSettingsExperimentDisabledPausesSavedPolicies(t *testing.T) {
	for _, mode := range []string{config.FeishuInteractionSingleton, config.FeishuInteractionThread} {
		t.Run(mode, func(t *testing.T) {
			b, hub, notices := groupRouteTestBridge(t)
			settings := saveGroupRouteTestSettings(t, b, "a", mode)
			if mode == config.FeishuInteractionThread {
				settings.IsolationRequired = true
				var err error
				settings, err = b.db.SaveGroupSettings(*settings, settings.Version)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := b.db.UpsertChat(LarkChat{BridgeId: settings.BridgeID, ChatID: "a", RootMsgID: "root", TargetID: "local", SessionName: "existing", Status: ChatStatusActive}); err != nil {
				t.Fatal(err)
			}
			if mode == config.FeishuInteractionSingleton {
				if err := b.db.SetGroupSessionRoot(settings.BridgeID, "a", settings.Version, "root"); err != nil {
					t.Fatal(err)
				}
			}
			viper.Set("bridge.feishu.group_settings.enabled", false)
			if handled, err := b.routeBotSessionInput(context.Background(), botSessionInput{ReplyKind: botSessionReplyGroup, ChatID: "a", OpenID: "owner"}); err != nil || handled {
				t.Fatalf("saved group fell into global singleton: %v, %v", handled, err)
			}
			for _, text := range []string{"do work", "/run codex", "/bind local/existing"} {
				if err := b.routeToSessionInChatWithMention(context.Background(), "a", "root", text, "owner", text, botMentionInfo{Mentioned: true}); err != nil {
					t.Fatal(err)
				}
			}
			if len(*notices) != 3 || !strings.Contains((*notices)[0], "实验") {
				t.Fatalf("pause notice = %v", *notices)
			}
			if handled, err := b.routeGroupSessionInput(context.Background(), "a", "watch", "watch", "owner", "automatic", botMentionInfo{}, routeInputOptions{source: routeInputSourceGroupWatch}); !handled || err != nil {
				t.Fatalf("watch = %v %v", handled, err)
			}
			if len(*notices) != 3 {
				t.Fatal("paused automatic work sent a notice")
			}
			if _, err := b.interactionModeForChat("a"); err == nil {
				t.Fatal("paused group eligible for automation")
			}
			if _, err := b.newSessionPolicy(context.Background(), "a"); err == nil {
				t.Fatal("launch bypassed gate")
			}
			if _, err := b.newSessionPolicy(withGroupLaunchPolicy(context.Background(), settings, settings.IsolationRequired, false), "a"); err == nil {
				t.Fatal("cached launch policy bypassed gate")
			}
			if len(hub.routes) != 0 {
				t.Fatalf("disabled group called runtime: %+v", hub.routes)
			}
			after, _ := b.db.ReadGroupSettings(settings.BridgeID, "a")
			if !reflect.DeepEqual(settings, after) {
				t.Fatal("pause changed settings")
			}
			if chat, err := b.db.FindByRootMsg(settings.BridgeID, "root"); err != nil || chat == nil || chat.Status != ChatStatusActive {
				t.Fatal("pause changed existing binding")
			}
			if mode == config.FeishuInteractionSingleton {
				if root, err := b.db.GetGroupSessionRoot(settings.BridgeID, "a"); err != nil || root != "root" {
					t.Fatal("pause retired current singleton")
				}
			}
			if handled, err := b.routeGroupSessionInput(context.Background(), "unconfigured", "legacy", "legacy", "owner", "task", botMentionInfo{}, routeInputOptions{}); handled || err != nil {
				t.Fatalf("unconfigured group blocked: %v %v", handled, err)
			}
			if _, err := b.newSessionPolicy(context.Background(), "unconfigured"); err != nil {
				t.Fatal(err)
			}
			viper.Set("bridge.feishu.group_settings.enabled", true)
			policy, err := b.newSessionPolicy(context.Background(), "a")
			if err != nil || policy.settings == nil || policy.isolationRequired != settings.IsolationRequired {
				t.Fatalf("re-enabled policy = %+v %v", policy, err)
			}
		})
	}
}
