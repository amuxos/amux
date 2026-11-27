package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestAmuxEverySubpageKeepsOverviewBack(t *testing.T) {
	t.Parallel()
	state := managementState{Mode: config.FeishuInteractionThread, DefaultAgent: "builtin/codex", DefaultRuntimeRef: "codex", Profiles: []managementProfileSummary{{Ref: "builtin/codex", Name: "Codex"}}}
	draft := managementDraftFromState(state)
	cards := map[string]string{
		"settings":          renderSettingsCardForSurface(state, managementSurfaceAmux),
		"settings_edit":     renderSettingsEditCardForSurface(state, draft, managementSurfaceAmux),
		"settings_confirm":  renderSettingsConfirmationCard(state, draft, BotSettingsRequest{}, BotSettingsResult{}),
		"settings_loading":  renderSettingsProcessingCard(state, draft),
		"settings_failed":   renderSettingsFailureCardForSurface(state, draft, errors.New("test failure"), managementSurfaceAmux),
		"settings_recovery": renderSettingsRecoveryChoiceCard(state, draft, BotSettingsRequest{}, BotSettingsResult{}, ""),
		"settings_bind":     renderSettingsManualBindCard(state, BotSettingsRequest{}, "", "test-token", ""),
		"integrations":      renderCapabilitiesCard(state),
		"collaboration":     renderBotToBotCard(state),
		"document_comments": renderDocumentCommentsCardWithNotice(state, ""),
		"document_edit":     renderDocumentCommentsEditCard(state, managementDocumentCommentsDraftFromState(state)),
		"group_watch":       renderGroupWatchManagementCardWithNames(state, nil),
		"watch_edit":        renderGroupWatchEditCard(state, managementGroupWatchDraftFromState(state)),
		"system":            renderSystemMaintenanceCardForSurface(state, managementSurfaceAmux),
		"restart_confirm":   renderSystemRestartConfirmationCard(state),
		"restart_loading":   renderSystemRestartingCard(),
		"update_loading":    renderSystemUpdateCheckingCard(state),
		"update_result":     renderSystemUpdateResultCard(state, ManagementUpdateInfo{}),
		"system_failed":     renderSystemMaintenanceFailureCard(managementCopy("management.try.again.later"), managementActionSystemUpdate, managementCopy("management.retry")),
		"library_loading":   renderAgentPackOperationCard(managementActionPackSync),
		"library_failed":    renderAgentPackLibraryFailureCard(),
		"library_manage":    renderLoadedPackManagementCard(agentprofile.LoadedPackState{Namespace: "team", DisplayName: "Experts"}, managementPackLibraryView{Section: managementPackLibrarySectionLoaded, Page: 2}),
		"experiments":       renderManagementExperimentsCard(config.ExperimentsSettings{}, config.ExperimentsSettings{}),
		"experiments_saved": renderManagementExperimentsRestartConfirmationCard(state),
		"help":              renderManagementCompactHelpCard(),
	}
	for _, step := range []string{"welcome", "mode", "model", "applying", "tasks", "review_task", "running", "explore", "select", "select_review", "import", "create", "review_expert", "review_import", "done"} {
		cards["guide_"+step] = renderGuideCard(state, onboardingProgress{Step: step}, nil)
	}
	for name, raw := range cards {
		t.Run(name, func(t *testing.T) {
			raw = renderFocusedManagementCardForSurface(raw, managementSurfaceAmux)
			for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese, "fr"} {
				card := selectManagementCardLocale(raw, locale)
				var doc cardDoc
				if err := json.Unmarshal([]byte(card), &doc); err != nil {
					t.Fatal(err)
				}
				if len(doc.Body.Elements) == 0 || doc.Body.Elements[0].ElementID != "amux_management_back" || strings.Count(card, `"element_id":"amux_management_back"`) != 1 {
					t.Fatalf("%s page has no unique top return action", locale)
				}
				back := findManagementButtonByAction(t, card, managementActionStatusOpen)
				var value map[string]string
				if err := json.Unmarshal(back.Value, &value); err != nil || value[managementSurfaceKey] != string(managementSurfaceAmux) || back.ActionType == "form_submit" {
					t.Fatalf("return must navigate within /amux without submitting a form: %#v", back)
				}
				var anyDoc any
				if err := json.Unmarshal([]byte(card), &anyDoc); err != nil {
					t.Fatal(err)
				}
				assertManagementFormScope(t, anyDoc, false)
				if len(card) >= maxFeishuCardBytes {
					t.Fatal("navigation exceeded the card budget")
				}
			}
			if again := renderFocusedManagementCardForSurface(raw, managementSurfaceAmux); again != raw {
				t.Fatal("repeated rendering changed navigation")
			}
		})
	}
	if strings.Contains(renderManagementOverviewCard(state), `"element_id":"amux_management_back"`) {
		t.Fatal("overview should not return to itself")
	}
	plain := renderSettingsProcessingCard(state, draft)
	if renderFocusedManagementCardForSurface(plain, managementSurfaceDefault) != plain {
		t.Fatal("changed a card outside /amux")
	}
}

func TestAmuxNestedBackTargetsParent(t *testing.T) {
	t.Parallel()
	state := managementState{Mode: config.FeishuInteractionThread}
	for name, tc := range map[string]struct{ card, action string }{
		"defaults": {renderSettingsEditCardForSurface(state, managementDraftFromState(state), managementSurfaceAmux), managementActionStatusOpen},
		"comments": {renderDocumentCommentsEditCard(state, managementDocumentCommentsDraftFromState(state)), managementActionDocumentCommentsOpen},
		"watch":    {renderGroupWatchEditCard(state, managementGroupWatchDraftFromState(state)), managementActionGroupWatchOpen},
		"library":  {renderLoadedPackManagementCard(agentprofile.LoadedPackState{Namespace: "team"}, managementPackLibraryView{Section: managementPackLibrarySectionLoaded, Page: 2}), managementActionPackLibraryNavigate},
	} {
		t.Run(name, func(t *testing.T) {
			card := renderFocusedManagementCardForSurface(tc.card, managementSurfaceAmux)
			button := findManagementButtonByAction(t, card, tc.action)
			if button.ActionType == "form_submit" {
				t.Fatal("return submits unsaved data")
			}
			if name == "library" {
				var v map[string]string
				json.Unmarshal(button.Value, &v)
				if v["page"] != "2" || v["section"] != string(managementPackLibrarySectionLoaded) {
					t.Fatal("library return lost its page")
				}
			}
		})
	}
	entry := schedule.Entry{Task: schedule.Task{ID: 42}}
	findManagementButtonByAction(t, renderScheduleDetail(entry, false), "schedule.home")
	findManagementButtonByAction(t, renderScheduleDetail(entry, false), "schedule.list")
	back := findManagementButtonByAction(t, renderScheduleDetail(entry, true), "schedule.detail")
	var v map[string]string
	json.Unmarshal(back.Value, &v)
	if v["schedule_id"] != "42" {
		t.Fatal("result return targets another schedule")
	}
}

func TestAmuxReturnFromGuidePreservesProgressAndSettings(t *testing.T) {
	b, cards := guideTestBridge(t)
	p := onboardingProgress{Step: "create", Expert: onboardingExpert{Name: "Office", GitURL: "git@example.com:owner/experts.git"}}
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	before := b.managementState()
	back := findManagementButtonByAction(t, renderGuideCard(before, p, nil), managementActionStatusOpen)
	var value map[string]interface{}
	json.Unmarshal(back.Value, &value)
	response, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "owner"}, Context: &callback.Context{OpenMessageID: "guide-card"}, Action: &callback.CallBackAction{Value: value},
	}})
	if err != nil || !handled || response == nil {
		t.Fatal(response, handled, err)
	}
	select {
	case card := <-cards:
		if !strings.Contains(card, "amux_overview_actions") || strings.Contains(card, "amux_management_back") {
			t.Fatal("return did not open the overview")
		}
	case <-time.After(time.Second):
		t.Fatal("return did not update the card")
	}
	if after := guideTestProgress(t, b); !reflect.DeepEqual(p, after) {
		t.Fatal("return changed the saved guide draft")
	}
	if after := b.managementState(); after.Version != before.Version || after.DefaultAgent != before.DefaultAgent {
		t.Fatal("return applied unsaved settings")
	}
}
