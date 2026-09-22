package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/modelservice"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestManagementI18nAllPagesAndStableActions(t *testing.T) {
	state := managementState{Mode: config.FeishuInteractionThread, DefaultAgent: "custom/expert", Profiles: []managementProfileSummary{{Ref: "custom/expert", Name: "Expert", AgentType: "codex"}}, SystemStatus: managementSystemHealthy, Targets: 1}
	draft := managementDraftFromState(state)
	issue := capabilityPermissionIssue{Capability: capabilityGroupWatch, Name: "群值守", Scopes: []string{"im:message.group_msg"}, GrantURL: "https://example.com/permissions"}
	cards := map[string]string{
		"system":              renderSystemStatusCard(state),
		"maintenance":         renderSystemMaintenanceCardForSurface(state, managementSurfaceAmux),
		"restart_confirm":     renderSystemRestartConfirmationCard(state),
		"restarted":           renderSystemRecoveredCard(state),
		"recovery_attention":  renderSystemRecoveryAttentionCard(state),
		"update_check":        renderSystemUpdateCheckingCard(state),
		"update_result":       renderSystemUpdateResultCard(state, ManagementUpdateInfo{CurrentVersion: "v1.0", LatestVersion: "v2.0", UpdateAvailable: true}),
		"upgrading":           renderSystemUpgradingCard("v2.0"),
		"upgraded":            renderSystemUpgradeRecoveredCard(state, "v2.0"),
		"group_settings":      renderCurrentGroupSettingsCard(state, &GroupSettings{ChatID: "oc_test", InteractionMode: "thread"}, "", nil, map[string]string{"chat_name": "Example"}),
		"group_edit":          renderGroupSettingsEditCardAtPage(state, groupSettingsFormDraft(&GroupSettings{ChatID: "oc_test", InteractionMode: "thread"}), map[string]string{"chat_name": "Example"}, 0),
		"overview":            renderManagementOverviewCard(state),
		"onboarding":          renderOnboardingCard(state),
		"onboarding_done":     renderOnboardingCompleteCard(state, draft),
		"settings":            renderSettingsEntryCard(state),
		"settings_edit":       renderSettingsEditCard(state, draft),
		"settings_processing": renderSettingsProcessingCard(state, draft),
		"settings_failure":    renderSettingsFailureCardForSurface(state, draft, managementLocalizedError("management.error.target.unavailable"), managementSurfaceAmux),
		"recovery":            renderSettingsRecoveryChoiceCard(state, draft, BotSettingsRequest{}, BotSettingsResult{}, ""),
		"manual_bind":         renderSettingsManualBindCard(state, BotSettingsRequest{}, "", "token", ""),
		"capabilities":        renderCapabilitiesCard(state),
		"bot_to_bot":          renderBotToBotCard(state),
		"documents":           renderDocumentCommentsCardWithNotice(state, ""),
		"documents_edit":      renderDocumentCommentsEditCard(state, managementDocumentCommentsDraftFromState(state)),
		"watch":               renderGroupWatchManagementCardWithNames(state, nil),
		"watch_edit":          renderGroupWatchEditCard(state, managementGroupWatchDraftFromState(state)),
		"watch_confirm":       renderGroupWatchCommandConfirmationCard(state, "oc_group", "Example", "add"),
		"group_status":        renderGroupStatusCardWithGroupWatch(nil, "thread", managementCopy("management.not.included")),
		"permission":          renderCapabilityPermissionCard(issue),
		"permission_guide":    renderPermissionGrantGuideCard(issue, managementCopy("management.permission.failed"), "https://example.com/confirm"),
		"experiments":         renderManagementExperimentsCard(config.ExperimentsSettings{}, config.ExperimentsSettings{}),
		"library":             renderAgentPackLibraryCardViewForSurface(agentprofile.PackCatalogSnapshot{}, managementPackLibraryView{}, "", managementSurfaceAmux),
		"library_failure":     renderAgentPackLibraryFailureCard(),
		"library_progress":    renderAgentPackOperationCard(managementActionPackInstall),
		"help":                renderManagementHelpCard(state),
		"group_help":          renderWorkspaceHelpCard(),
		"entry_help":          renderSessionEntryHelpCard(),
		"topic_help":          renderBoundTopicHelpCard(),
	}
	for _, status := range []string{"", "signed_in", "signed_out", "expired", "pending", "unavailable", "setup_required", "installing"} {
		for _, count := range []int{1, 2} {
			serviceState := managementState{ModelServices: []managementModelService{{
				Service: modelservice.Service{ID: "external", Name: "External", Runtimes: []string{"codex"}},
				State:   modelservice.State{Status: status, CanInstall: true, LoginURL: "https://auth.example/login", Models: make([]modelservice.Model, count)},
			}}}
			cards[fmt.Sprintf("model_service_%s_%d", status, count)] = marshalNativeManagementCard(managementCardView{
				Elements: modelServiceElements(serviceState, "codex", map[string]string{"default_model_ref": "native-model", "default_reasoning_effort": "high"}, managementActionSettingsEdit),
			})
		}
	}
	for name, card := range cards {
		t.Run(name, func(t *testing.T) {
			if len(card) >= maxFeishuCardBytes {
				t.Fatalf("card exceeds size limit: %d", len(card))
			}
			for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
				selected := selectManagementCardLocale(card, locale)
				var doc any
				if err := json.Unmarshal([]byte(selected), &doc); err != nil {
					t.Fatal(err)
				}
				walkManagementTestText(doc, func(text string) {
					if strings.Contains(text, "%!") || strings.Contains(text, "management.") {
						t.Errorf("invalid %s copy: %q", locale, text)
					}
					if locale == i18n.English {
						// These are actual input commands, not translated aliases; language names
						// are intentionally displayed in their own language in the selector.
						for _, literal := range []string{"简体中文", "开启协作模式", "关闭协作模式", "切回负责人模式", "开启群值守", "关闭群值守", "中断", "停一下"} {
							text = strings.ReplaceAll(text, literal, "")
						}
						if strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
							t.Errorf("untranslated English product text: %q", text)
						}
					}
				})
			}
			var en, zh any
			json.Unmarshal([]byte(selectManagementCardLocale(card, i18n.English)), &en)
			json.Unmarshal([]byte(selectManagementCardLocale(card, i18n.Chinese)), &zh)
			stripManagementTestText(en)
			stripManagementTestText(zh)
			if !reflect.DeepEqual(en, zh) {
				t.Error("language changed card structure or callback payload")
			}
		})
	}
}
func walkManagementTestText(value any, visit func(string)) {
	switch node := value.(type) {
	case map[string]any:
		if content, ok := node["content"].(string); ok {
			visit(content)
		}
		for k, v := range node {
			if k != "value" && k != "behaviors" {
				walkManagementTestText(v, visit)
			}
		}
	case []any:
		for _, v := range node {
			walkManagementTestText(v, visit)
		}
	}
}
func stripManagementTestText(value any) {
	switch node := value.(type) {
	case map[string]any:
		delete(node, "content")
		delete(node, "i18n_content")
		if node["name"] == managementLanguageField {
			delete(node, "initial_option")
		}
		for k, v := range node {
			if k != "value" && k != "behaviors" {
				stripManagementTestText(v)
			}
		}
	case []any:
		for _, v := range node {
			stripManagementTestText(v)
		}
	}
}

func TestManagementI18nPreservesLiteralUserContent(t *testing.T) {
	literal := "运行正常 /tmp/中文 %s <user>"
	card := marshalNativeManagementCard(managementCardView{Title: managementCopy("management.current.task"), Elements: []cardElement{managementTextElement(literal, "normal", "default", nil)}, Buttons: []managementButton{{Text: managementCopy("management.save"), Action: "action", Values: map[string]string{"path": literal}}}})
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		var doc cardDoc
		if err := json.Unmarshal([]byte(selectManagementCardLocale(card, locale)), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Body.Elements[0].Text.Content != literal {
			t.Fatal("user content changed")
		}
		button := findManagementButtonByAction(t, selectManagementCardLocale(card, locale), "action")
		var values map[string]string
		json.Unmarshal(button.Value, &values)
		if values["path"] != literal {
			t.Fatal("callback payload changed")
		}
	}
}

func TestManagementI18nLocaleIsolationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ bot, user, locale string }{{"a", "one", "en"}, {"a", "two", "zh-CN"}, {"b", "one", "zh-CN"}} {
		if err := db.setManagementLocale(v.bot, v.user, v.locale); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.recordPrivateManagementCard("a", "one", "private-card"); err != nil {
		t.Fatal(err)
	}
	db.db.Close()
	db, err = openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.db.Close()
	for _, v := range []struct {
		bot, user string
		want      i18n.Locale
	}{{"a", "one", i18n.English}, {"a", "two", i18n.Chinese}, {"b", "one", i18n.Chinese}, {"b", "two", ""}} {
		got, err := db.managementLocale(v.bot, v.user)
		if err != nil || got != v.want {
			t.Fatalf("%+v: %s %v", v, got, err)
		}
	}
	if !db.isPrivateManagementCard("a", "one", "private-card") || db.isPrivateManagementCard("a", "two", "private-card") || db.isPrivateManagementCard("b", "one", "private-card") {
		t.Fatal("private receipt scope changed")
	}
	if err := db.setManagementLocale("a", "one", "auto"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.managementLocale("a", "one"); got != "" {
		t.Fatal(got)
	}
	if err := db.setManagementLocale("a", "one", "fr"); err == nil {
		t.Fatal("accepted unsupported preference")
	}
}

func TestManagementI18nCallbackDeliveryAndSharedCards(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner", AppId: "test-app"}
	var sent, patched string
	patches := make(chan string, 8)
	nextPatch := func() string {
		select {
		case card := <-patches:
			return card
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for language patch")
			return ""
		}
	}
	b.dmInteractive = func(_ context.Context, _, card string) (string, string, error) {
		sent = card
		return "private-card", "", nil
	}
	b.managementCardPatch = func(_ context.Context, _, card string) error { patches <- card; return nil }
	card := renderManagementOverviewCard(b.managementState())
	if err := b.sendManagementCardToDM(context.Background(), "owner", card); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent, "i18n_content") {
		t.Fatal("auto mode lost native translations")
	}
	event := func(user, message, locale string) *callback.CardActionTriggerEvent {
		return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: user}, Context: &callback.Context{OpenMessageID: message}, Action: &callback.CallBackAction{Value: map[string]any{managementActionKey: managementActionLanguage}, Option: locale}}}
	}
	for _, e := range []*callback.CardActionTriggerEvent{event("other", "private-card", "en"), event("owner", "shared-card", "en"), event("owner", "private-card", "fr")} {
		response, handled, err := b.handleManagementCardAction(context.Background(), e)
		if err != nil || !handled || response.Toast.Type == "success" {
			t.Fatal(response, handled, err)
		}
		if b.managementLocale("owner") != "" {
			t.Fatal("invalid callback changed preference")
		}
	}
	response, handled, err := b.handleManagementCardAction(context.Background(), event("owner", "private-card", "en"))
	if err != nil || !handled || response.Toast.Content != "Interface language saved" {
		t.Fatal(response, handled, err)
	}
	patched = nextPatch()
	if strings.Contains(patched, "i18n_content") || !strings.Contains(patched, "Interface language") {
		t.Fatal("private update lost preference")
	}
	// Delayed/background updates resolve the current persisted preference at delivery.
	b.writeManagementCardFromCallback(context.Background(), "owner", "private-card", card)
	patched = nextPatch()
	if strings.Contains(patched, "i18n_content") {
		t.Fatal("background refresh reverted language")
	}
	b.writeManagementCardFromCallback(context.Background(), "owner", "shared-card", card)
	patched = nextPatch()
	if !strings.Contains(patched, "i18n_content") {
		t.Fatal("personal preference changed shared card")
	}
	b.managementCardPatch = func(context.Context, string, string) error { return errors.New("deleted message") }
	b.writeManagementCardFromCallbackWithFallback(context.Background(), "owner", "private-card", card, renderAgentPackLibraryFailureCard())
	if strings.Contains(sent, "i18n_content") || !strings.Contains(sent, "Expert library unavailable") {
		t.Fatal("fallback DM lost preference")
	}
	b.managementCardPatch = func(_ context.Context, _, card string) error { patches <- card; return nil }
	response, _, err = b.handleManagementCardAction(context.Background(), event("owner", "private-card", "auto"))
	if err != nil || response.Toast.I18nContent["en_us"] != "Interface language saved" || b.managementLocale("owner") != "" {
		t.Fatal("auto reset failed")
	}

	if card := nextPatch(); !strings.Contains(card, "i18n_content") {
		t.Fatal("automatic card did not restore translations")
	}
}

func TestManagementI18nHelpCatalogParity(t *testing.T) {
	for _, scope := range []command.Scope{command.ScopePrivate, command.ScopeGroup, command.ScopeFreshTopic, command.ScopeActiveTopic} {
		for _, item := range command.Help(scope) {
			key := "management.help." + string(scope) + "." + string(item.ID)
			args := []any{}
			if scope == command.ScopeActiveTopic && item.ID == command.CommandSkill {
				args = append(args, "Codex")
			}
			text := managementCopy(key, args...)
			if text.For(i18n.Chinese) != item.Description {
				t.Errorf("help parity %s: %q != %q", key, text.For(i18n.Chinese), item.Description)
			}
		}
	}
	for _, n := range []int{0, 1, 2} {
		text := managementCopy("management.d.hours", n).For(i18n.English)
		want := fmt.Sprintf("%d hours", n)
		if n == 1 {
			want = "1 hour"
		}
		if text != want {
			t.Errorf("plural: %q != %q", text, want)
		}
	}
}

func TestManagementI18nRestartRecoveryKeepsLanguage(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	hub.targets = []*pb.Target{{Id: "local", Name: "local"}}
	bridgeID := b.currentBridgeId()
	if err := b.db.setManagementLocale(bridgeID, "owner", "en"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.recordPrivateManagementCard(bridgeID, "owner", "restart-card"); err != nil {
		t.Fatal(err)
	}
	if err := b.db.UpsertPendingManagementRecovery(bridgeID, "owner", "restart-card"); err != nil {
		t.Fatal(err)
	}
	var card string
	b.managementCardPatch = func(_ context.Context, messageID, content string) error {
		if messageID != "restart-card" {
			t.Fatal(messageID)
		}
		card = content
		return nil
	}
	done, err := b.recoverPendingManagementCardOnce(context.Background(), false)
	if err != nil || !done {
		t.Fatal(done, err)
	}
	if strings.Contains(card, "i18n_content") || !strings.Contains(card, "amux recovered") {
		t.Fatal("restart recovery lost saved language")
	}
}

func TestManagementI18nPermissionPlainTextFallback(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner", AppId: "cli_test"}
	b.dmInteractive = func(context.Context, string, string) (string, string, error) {
		return "", "", errors.New("card send failed")
	}
	var delivered string
	b.dmNotice = func(_ context.Context, openID, text string) (string, string, error) {
		if openID != "owner" {
			t.Fatal(openID)
		}
		delivered = text
		return "notice", "", nil
	}
	issue := capabilityPermissionIssue{Capability: capabilityGroupWatch, Name: "群值守", Scopes: []string{"im:message.group_msg"}, GrantURL: "https://example.com/permissions"}
	for _, locale := range []string{"en", "zh-CN", "auto"} {
		if err := b.db.setManagementLocale(b.currentBridgeId(), "owner", locale); err != nil {
			t.Fatal(err)
		}
		if err := b.notifyCapabilityPermissionIssue(context.Background(), issue); err != nil {
			t.Fatal(err)
		}
		en, zh := strings.Contains(delivered, "Request permissions: https://example.com/permissions"), strings.Contains(delivered, "申请权限：https://example.com/permissions")
		if en != (locale != "zh-CN") || zh != (locale != "en") {
			t.Fatalf("fallback %s: %q", locale, delivered)
		}
	}
}

func TestManagementI18nLanguageCallbackDoesNotWaitForNetwork(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	b.rememberPrivateManagementCard("owner", "private-card")
	release, finished := make(chan struct{}), make(chan struct{})
	b.managementCardPatch = func(context.Context, string, string) error { <-release; close(finished); return nil }
	response := make(chan *callback.CardActionTriggerResponse, 1)
	go func() {
		response <- b.changeManagementLocale("owner", "private-card", &callback.CallBackAction{Option: "en"})
	}()
	select {
	case got := <-response:
		if got.Toast.Type != "success" {
			t.Error(got)
		}
	case <-time.After(time.Second):
		t.Error("language callback waited for the network")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("patch did not finish")
	}
}
