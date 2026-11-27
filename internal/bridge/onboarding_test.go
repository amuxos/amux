package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestGuideCardsBothLanguagesAndNativeFormScope(t *testing.T) {
	t.Parallel()
	state := managementState{Mode: "thread", DefaultAgent: "builtin/codex", DefaultRuntimeRef: "codex", Runtimes: []agentprofile.RuntimeOption{{Ref: "codex", DefaultReasoningEfforts: []string{"low", "high", "xhigh"}}}, Profiles: []managementProfileSummary{{Ref: "builtin/codex", Name: "Codex"}}}
	for _, step := range []string{"welcome", "mode", "model", "applying", "tasks", "review_task", "running", "explore", "select", "select_review", "import", "create", "review_expert", "review_import", "done"} {
		t.Run(step, func(t *testing.T) {
			p := newOnboarding(state)
			p.Step = step
			p.Revision = 3
			p.Kind = "joke"
			p.Prompt = "Literal user request"
			p.Expert = onboardingExpert{Name: "Test expert", ID: "test-expert", GitURL: "git@example.com:owner/experts.git"}
			raw := renderGuideCard(state, p, nil)
			if len(raw) >= maxFeishuCardBytes {
				t.Fatalf("oversize card %d", len(raw))
			}
			for _, locale := range []i18n.Locale{"", i18n.English, i18n.Chinese, "fr"} {
				selected := selectManagementCardLocale(raw, locale)
				var doc any
				if err := json.Unmarshal([]byte(selected), &doc); err != nil {
					t.Fatal(err)
				}
				assertManagementFormScope(t, doc, false)
				names := map[string]int{}
				collectCardElementNames(t, selected, names)
				for name, n := range names {
					if n > 1 {
						t.Errorf("duplicate name %s", name)
					}
				}
				walkManagementTestText(doc, func(text string) {
					if strings.Contains(text, "guide.") || strings.Contains(text, "%!") {
						t.Errorf("invalid copy %q", text)
					}
					if locale == i18n.English && strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
						t.Errorf("untranslated English %q", text)
					}
				})
			}
			if step == "model" {
				if !strings.Contains(raw, `"name":"default_reasoning_effort"`) || strings.Contains(raw, `"tag":"collapsible_panel"`) {
					t.Fatal("reasoning must remain visible")
				}
			}
		})
	}
}

func TestGuideProgressPersistsAndRejectsStaleWriters(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	p := onboardingProgress{Step: "create", Expert: onboardingExpert{Name: "Saved draft"}}
	if err = db.saveOnboarding("bot-a", "owner", &p); err != nil {
		t.Fatal(err)
	}
	stale := p
	p.Expert.Skills = "New skill"
	if err = db.saveOnboarding("bot-a", "owner", &p); err != nil {
		t.Fatal(err)
	}
	if err = db.saveOnboarding("bot-a", "owner", &stale); !errors.Is(err, errBotProfileStateChanged) {
		t.Fatalf("stale write: %v", err)
	}
	db.Close()
	db, err = openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.loadOnboarding("bot-a", "owner")
	if err != nil || got.Expert.Name != "Saved draft" || got.Expert.Skills != "New skill" {
		t.Fatalf("lost draft %#v %v", got, err)
	}
	for _, ids := range [][2]string{{"bot-b", "owner"}, {"bot-a", "another"}} {
		got, err = db.loadOnboarding(ids[0], ids[1])
		if err != nil || got.Revision != 0 {
			t.Fatalf("cross-identity progress %#v %v", got, err)
		}
	}
}

func guideTestBridge(t *testing.T) (*Bridge, <-chan string) {
	t.Helper()
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner"}
	cards := make(chan string, 100)
	b.managementCardPatch = func(_ context.Context, _ string, card string) error { cards <- card; return nil }
	b.dmInteractive = func(_ context.Context, _ string, card string) (string, string, error) {
		cards <- card
		return "guide-card", "", nil
	}
	b.rememberPrivateManagementCard("owner", "guide-card")
	return b, cards
}
func guideTestAction(t *testing.T, b *Bridge, action string, p onboardingProgress, form map[string]interface{}) *callback.CardActionTriggerResponse {
	t.Helper()
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: "owner"}, Context: &callback.Context{OpenMessageID: "guide-card", OpenChatID: "chat"}, Action: &callback.CallBackAction{Value: map[string]interface{}{managementActionKey: guideAction + action, "revision": strconv.FormatInt(p.Revision, 10)}, FormValue: form}}}
	result, handled, err := b.handleManagementCardAction(context.Background(), event)
	if err != nil || !handled || result == nil {
		t.Fatalf("action %s: %#v %v", action, result, err)
	}
	return result
}
func guideTestProgress(t *testing.T, b *Bridge) onboardingProgress {
	t.Helper()
	p, err := b.db.loadOnboarding(b.currentBridgeId(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGuidePendingWelcomeOnlyForNewSetupAndOnlyOnce(t *testing.T) {
	b, cards := guideTestBridge(t)
	// An ordinary upgrade has no pending marker and sends nothing.
	if err := b.deliverPendingOnboarding(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Fatal("existing installation received an unsolicited welcome")
	}
	if err := b.db.queueOnboarding(b.currentBridgeId(), "owner"); err != nil {
		t.Fatal(err)
	}
	if !guideTestProgress(t, b).PendingWelcome {
		t.Fatal("new setup did not persist the welcome marker")
	}
	for range 2 {
		if err := b.deliverPendingOnboarding(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	p := guideTestProgress(t, b)
	if len(cards) != 1 || p.PendingWelcome || p.Draft.Interaction == "" {
		t.Fatalf("delivery count=%d progress=%#v", len(cards), p)
	}
	p.Step = "create"
	p.Expert.Name = "Saved expert"
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	if err := b.db.queueOnboarding(b.currentBridgeId(), "owner"); err != nil {
		t.Fatal(err)
	}
	p = guideTestProgress(t, b)
	if p.PendingWelcome || p.Step != "create" || p.Expert.Name != "Saved expert" {
		t.Fatalf("setup retry reset an existing guide: %#v", p)
	}
}

func TestGuidePendingWelcomeDoesNotCrossOwner(t *testing.T) {
	b, cards := guideTestBridge(t)
	if err := b.db.queueOnboarding(b.currentBridgeId(), "another-owner"); err != nil {
		t.Fatal(err)
	}
	if err := b.deliverPendingOnboarding(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 || guideTestProgress(t, b).Revision != 0 {
		t.Fatal("welcome marker crossed owner identities")
	}
}

func TestGuideImportPreservesPersonalLibrary(t *testing.T) {
	b, _ := guideTestBridge(t)
	p := onboardingProgress{Step: "import", Expert: onboardingExpert{GitURL: "git@example.com:owner/personal.git"}}
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	guideTestAction(t, b, "import.preview", p, map[string]interface{}{"git_url": "git@example.com:team/shared.git"})
	p = guideTestProgress(t, b)
	if p.ImportGitURL != "git@example.com:team/shared.git" || p.Expert.GitURL != "git@example.com:owner/personal.git" {
		t.Fatalf("import replaced personal repository: %#v", p)
	}
	guideTestAction(t, b, "create", p, nil)
	p = guideTestProgress(t, b)
	if p.Expert.GitURL != "git@example.com:owner/personal.git" {
		t.Fatal("second expert lost its personal repository")
	}
}

func TestGuideCanonicalTurnIdentifiesActualSubmissionAndCompletion(t *testing.T) {
	b, _ := guideTestBridge(t)
	p := onboardingProgress{Step: "running", Submission: "dispatching", RootID: "request"}
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	b.observeGuideSubmission(&LarkChat{RootMsgID: "unrelated"}, "turn-other", nil)
	b.observeGuideSubmission(&LarkChat{RootMsgID: "request"}, "", nil)
	if guideTestProgress(t, b).Submission != "dispatching" {
		t.Fatal("unrelated turn accepted the guide request")
	}
	b.observeGuideSubmission(&LarkChat{RootMsgID: "request"}, "turn-real", nil)
	p = guideTestProgress(t, b)
	if p.Submission != "accepted" || p.TurnID != "turn-real" {
		t.Fatalf("canonical receipt was not saved: %#v", p)
	}
	b.observeGuideCompletion(&LarkChat{}, "turn-old", "done")
	if guideTestProgress(t, b).Result != "" {
		t.Fatal("old completion finished the current request")
	}
	b.observeGuideCompletion(&LarkChat{}, "turn-real", "done")
	if guideTestProgress(t, b).Result != "result.finished" {
		t.Fatal("actual completion was not observed")
	}
}

func TestGuideReentryPreservesDraftWithoutReplayingTask(t *testing.T) {
	b, _ := guideTestBridge(t)
	p := onboardingProgress{Step: "running", Submission: "accepted", RootID: "real-task", Expert: onboardingExpert{Name: "First expert", GitURL: "git@example.com:owner/experts.git"}}
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	guideTestAction(t, b, "open", p, nil)
	p = guideTestProgress(t, b)
	if p.Step != "welcome" || p.Resume != "running" || p.Expert.Name != "First expert" {
		t.Fatalf("reentry %#v", p)
	}
	guideTestAction(t, b, "resume", p, nil)
	p = guideTestProgress(t, b)
	if p.Step != "running" || p.RootID != "real-task" || p.Submission != "accepted" {
		t.Fatalf("resume %#v", p)
	}
	guideTestAction(t, b, "done", p, nil)
	p = guideTestProgress(t, b)
	guideTestAction(t, b, "open", p, nil)
	p = guideTestProgress(t, b)
	if p.Step != "welcome" || p.Expert.Name != "First expert" {
		t.Fatalf("completed guide must reopen %#v", p)
	}
}

func TestGuideNavigationReturnsUpdatedCardInCallback(t *testing.T) {
	b, _ := guideTestBridge(t)
	p := newOnboarding(b.managementState())
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	response := guideTestAction(t, b, "start", p, nil)
	if response.Card == nil || response.Card.Type != "raw" || response.Toast != nil {
		t.Fatalf("navigation must replace the card directly: %#v", response)
	}
	current := guideTestProgress(t, b)
	// A click from the previously displayed revision returns the latest card,
	// without executing that stale operation or leaving the old view in place.
	stale := guideTestAction(t, b, "create", p, nil)
	if stale.Card == nil || stale.Toast == nil || stale.Toast.Type != "warning" {
		t.Fatalf("stale callback did not return the current view: %#v", stale)
	}
	if after := guideTestProgress(t, b); after.Revision != current.Revision || after.Step != "mode" {
		t.Fatalf("stale callback performed work: %#v", after)
	}
	raw, err := json.Marshal(stale.Card.Data)
	if err != nil || !strings.Contains(string(raw), `"revision":"`+strconv.FormatInt(current.Revision, 10)+`"`) {
		t.Fatalf("response does not expose the current revision: %s %v", raw, err)
	}
}

func TestGuideBackNavigationPreservesExpertAndLibraryDrafts(t *testing.T) {
	b, _ := guideTestBridge(t)
	for _, tc := range []struct{ step, action, want string }{
		{"import", "explore", "explore"}, {"create", "explore", "explore"},
		{"select", "explore", "explore"}, {"explore", "tasks", "tasks"},
		{"tasks", "back.model", "model"}, {"model", "back.mode", "mode"},
	} {
		p := guideTestProgress(t, b)
		p.Step = tc.step
		p.Expert = onboardingExpert{Name: "Saved expert", GitURL: "git@example.com:owner/personal.git"}
		p.ImportGitURL = "git@example.com:team/shared.git"
		if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
			t.Fatal(err)
		}
		card := renderGuideCard(b.managementState(), p, nil)
		if !strings.Contains(card, `"amux_action":"`+guideAction+tc.action+`"`) {
			t.Fatalf("%s has no Back action", tc.step)
		}
		guideTestAction(t, b, tc.action, p, nil)
		p = guideTestProgress(t, b)
		if p.Step != tc.want || p.Expert.Name != "Saved expert" || p.ImportGitURL != "git@example.com:team/shared.git" {
			t.Fatalf("Back lost a saved draft: %#v", p)
		}
	}
}

func TestGuideTaskExamplesHaveSeparateFullWidthActions(t *testing.T) {
	t.Parallel()
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		p := onboardingProgress{Step: "tasks", Revision: 1}
		raw := selectManagementCardLocale(renderGuideCard(managementState{}, p, nil), locale)
		var doc any
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatal(err)
		}
		var walk func(any)
		buttons := 0
		walk = func(value any) {
			switch value := value.(type) {
			case map[string]any:
				if value["element_id"] == "amux_management_back" {
					return // The shared navigation row is separate from task actions.
				}
				if value["tag"] == "column_set" {
					t.Fatal("task actions must not share a horizontal row")
				}
				if value["tag"] == "button" {
					buttons++
					if value["width"] != "fill" {
						t.Fatal("task action does not use the card width")
					}
				}
				for _, child := range value {
					walk(child)
				}
			case []any:
				for _, child := range value {
					walk(child)
				}
			}
		}
		walk(doc)
		if buttons != 6 {
			t.Fatalf("actions=%d, expected three samples, explore, back and finish", buttons)
		}
	}
}

func TestGuideOwnerAndOriginalPrivateCardRequired(t *testing.T) {
	b, _ := guideTestBridge(t)
	for _, tc := range []struct{ owner, card string }{{"stranger", "guide-card"}, {"owner", "forwarded-card"}} {
		event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: tc.owner}, Context: &callback.Context{OpenMessageID: tc.card}, Action: &callback.CallBackAction{Value: map[string]interface{}{managementActionKey: managementActionGuideOpen}}}}
		resp, handled, err := b.handleManagementCardAction(context.Background(), event)
		if err != nil || !handled || resp.Toast.Type != "error" {
			t.Fatalf("unauthorized response %#v %v", resp, err)
		}
	}
	if p := guideTestProgress(t, b); p.Revision != 0 {
		t.Fatalf("unauthorized state write %#v", p)
	}
}

func TestGuideDuplicateSendDoesNotReplayExternalWork(t *testing.T) {
	b, _ := guideTestBridge(t)
	var sends atomic.Int32
	b.chatNotice = func(context.Context, string, string) (string, string, error) {
		sends.Add(1)
		return "", "", errors.New("network outcome unknown")
	}
	p := onboardingProgress{Step: "review_task", Kind: "meetings", Prompt: "User request", TurnID: "previous-turn"}
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	first := guideTestAction(t, b, "send", p, nil)
	if first.Toast.Type != "success" {
		t.Fatalf("send %#v", first)
	}
	guideTestAction(t, b, "send", p, nil)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p = guideTestProgress(t, b)
		if p.Submission == "uncertain" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sends.Load() != 1 || p.Submission != "uncertain" || p.TurnID != "" {
		t.Fatalf("sends=%d progress=%#v", sends.Load(), p)
	}
	guideTestAction(t, b, "open", p, nil)
	if sends.Load() != 1 {
		t.Fatal("reentry resent a request")
	}
}

func TestGuideExpertDraftAndMultipleExpertsShareRemoteLibrary(t *testing.T) {
	b, _ := guideTestBridge(t)
	p := onboardingProgress{Step: "create", Expert: onboardingExpert{GitURL: "git@example.com:owner/experts.git"}}
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	form := map[string]interface{}{"expert_name": "Office", "expert_id": "office", "responsibility": "Meetings", "git_url": p.Expert.GitURL, "skills": "Import a calendar Skill"}
	guideTestAction(t, b, "create.preview", p, form)
	p = guideTestProgress(t, b)
	if p.Step != "review_expert" || p.Expert.ID != "office" {
		t.Fatalf("preview %#v", p)
	}
	guideTestAction(t, b, "create", p, nil)
	p = guideTestProgress(t, b)
	form["expert_name"] = "Reviewer"
	form["expert_id"] = "reviewer"
	form["responsibility"] = "Code review"
	guideTestAction(t, b, "create.preview", p, form)
	p = guideTestProgress(t, b)
	if p.Expert.GitURL != "git@example.com:owner/experts.git" || p.Expert.ID != "reviewer" {
		t.Fatalf("second expert %#v", p)
	}
	// Reviewing a draft does not claim publication or alter the Pack registry.
	if p.Step != "review_expert" || p.RootID != "" {
		t.Fatalf("preview performed work %#v", p)
	}
}

func TestGuideSkillCatalogSubmitsExpertDraftToAgent(t *testing.T) {
	const childEnv = "AMUX_TEST_GUIDE_SKILL_CATALOG_CHILD"
	if os.Getenv(childEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestGuideSkillCatalogSubmitsExpertDraftToAgent$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated catalog test: %v\n%s", err, out)
		}
		return
	}
	const catalog = "https://skills.example.com/catalog"
	if err := RegisterExtension(Extension{Onboarding: OnboardingOptions{SkillCatalogURL: catalog}}); err != nil {
		t.Fatal(err)
	}
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese} {
		t.Run(string(locale), func(t *testing.T) {
			b, _ := guideTestBridge(t)
			if err := b.db.setManagementLocale(b.currentBridgeId(), "owner", string(locale)); err != nil {
				t.Fatal(err)
			}
			p := onboardingProgress{Step: "create", Expert: onboardingExpert{Name: "Old draft", GitURL: "git@example.com:owner/experts.git"}}
			if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
				t.Fatal(err)
			}
			raw := selectManagementCardLocale(renderGuideCard(b.managementState(), p, nil), locale)
			button := findManagementButtonByAction(t, raw, guideAction+"create.skills.preview")
			if button.ActionType != "form_submit" || button.Width != "fill" || button.URL != "" || strings.Contains(raw, catalog) {
				t.Fatalf("catalog must submit the form rather than open a website: %#v", button)
			}
			var doc any
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				t.Fatal(err)
			}
			assertManagementFormScope(t, doc, false)
			form := map[string]interface{}{"expert_name": "Office", "expert_id": "office", "responsibility": "Plan meetings", "git_url": p.Expert.GitURL, "skills": "calendar"}
			guideTestAction(t, b, "create.skills.preview", p, form)
			p = guideTestProgress(t, b)
			if p.Step != "review_expert" || !p.Expert.SearchSkills || p.Expert.Name != "Office" || p.Expert.Responsibility != "Plan meetings" || p.Expert.Skills != "calendar" || p.RootID != "" {
				t.Fatalf("catalog preview lost unsaved input or dispatched early: %#v", p)
			}
			guideTestAction(t, b, "create", p, nil)
			p = guideTestProgress(t, b)
			if !p.Expert.SearchSkills || p.Expert.Name != "Office" {
				t.Fatal("back lost catalog draft")
			}
			guideTestAction(t, b, "create.skills.preview", p, form)
			p = guideTestProgress(t, b)
			var sends atomic.Int32
			b.chatNotice = func(context.Context, string, string) (string, string, error) {
				sends.Add(1)
				return "", "", errors.New("test transport unavailable")
			}
			guideTestAction(t, b, "send.expert", p, nil)
			guideTestAction(t, b, "send.expert", p, nil)
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				p = guideTestProgress(t, b)
				if p.Submission == "uncertain" {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			for _, required := range []string{"Office", "office", "Plan meetings", "calendar", "git@example.com:owner/experts.git", catalog, "SKILL.md"} {
				if !strings.Contains(p.Prompt, required) {
					t.Errorf("Agent request lost %q", required)
				}
			}
			if sends.Load() != 1 || p.Step != "running" || p.Submission != "uncertain" {
				t.Fatalf("send count=%d progress=%#v", sends.Load(), p)
			}
		})
	}
}

func TestGuideRunningOmitsMessageJump(t *testing.T) {
	t.Parallel()
	p := onboardingProgress{Step: "running", RootID: "real-request-id", Submission: "accepted"}
	for _, locale := range []i18n.Locale{i18n.English, i18n.Chinese, "fr"} {
		raw := selectManagementCardLocale(renderGuideCard(managementState{}, p, nil), locale)
		if strings.Contains(raw, "message/open") || strings.Contains(raw, `"url"`) {
			t.Fatal("task progress still contains the ineffective message jump")
		}
		findManagementButtonByAction(t, raw, guideAction+"refresh")
		findManagementButtonByAction(t, raw, guideAction+"explore")
	}
}

func TestGuideStrongestEffortPreservesExplicitPreferences(t *testing.T) {
	t.Parallel()
	state := managementState{Mode: config.FeishuInteractionThread, DefaultRuntimeRef: "codex", Runtimes: []agentprofile.RuntimeOption{{Ref: "codex", DefaultReasoningEfforts: []string{"low", "high", "xhigh"}, NativeDefaultCapabilitiesKnown: true}}}
	if got := newOnboarding(state).Draft.DefaultReasoningEffort; got != "xhigh" {
		t.Fatalf("strongest=%q", got)
	}
	state.DefaultReasoningEffort = "low"
	if got := newOnboarding(state).Draft.DefaultReasoningEffort; got != "low" {
		t.Fatalf("saved preference replaced: %q", got)
	}
	state.DefaultReasoningEffort = ""
	state.Runtimes[0].DefaultReasoningEfforts = nil
	if got := newOnboarding(state).Draft.DefaultReasoningEffort; got != "" {
		t.Fatalf("invented effort %q", got)
	}
}

func TestGuideModelDiscoveryUsesDraftExecutionModeAndStrongestEffort(t *testing.T) {
	b, cards := guideTestBridge(t)
	setCurrentBotProfile(t, b, "builtin/codex")
	setCurrentBotRuntime(t, b, "codex")
	h := &nativeModelsHub{Hub: b.hub, result: &pb.ListNativeModelsResponse{Status: "ready", Models: []*pb.NativeModel{{Model: "new-model", DisplayName: "New model", ReasoningEfforts: []string{"low", "xhigh"}, IsDefault: true}}}}
	b.hub = h
	b.SetExecutionIdentityResolver(func(owner, ref, target string, shared bool) (LaunchIdentity, error) {
		if owner != "owner" || target != "remote" || !shared {
			t.Errorf("model discovery did not use the draft's continuous-conversation environment: %s %s %v", owner, target, shared)
		}
		return LaunchIdentity{Identity: &pb.ExecutionIdentity{EffectivePrincipalId: "owner-principal"}}, nil
	})
	p := newOnboarding(b.managementState())
	p.Step = "model"
	p.Draft.Interaction = config.FeishuInteractionSingleton
	p.Draft.DefaultReasoningEffort = ""
	p.ReasoningSelected = false
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	b.refreshGuideModels("owner", "guide-card", p, nil)
	card := waitForManagementCard(t, cards, func(card string) bool { return strings.Contains(card, "New model") })
	if card == "" {
		t.Fatal("discovered models did not reach the guide")
	}
	if h.request == nil || h.request.AgentType != "codex" {
		t.Fatalf("wrong native engine: %+v", h.request)
	}
	if actual := guideTestProgress(t, b); actual.Draft.DefaultReasoningEffort != "xhigh" {
		t.Fatalf("strongest discovered effort was not selected: %+v", actual)
	}
}

func TestGuideRemoteGitAndUsernameChanges(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"/local/path", "../experts", "file:///tmp/experts", "-bad", "git@example.com:repo\nnext", "https://user:password@example.com/repo"} {
		if guideRemoteGit(raw) {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"git@example.com:owner/experts.git", "https://example.com/owner/experts.git"} {
		if !guideRemoteGit(raw) {
			t.Errorf("rejected %q", raw)
		}
	}
	template := "git@example.com:user/{username}.git"
	if got := guidePersonalGit(template, "alice", "bob", "git@example.com:user/alice.git"); got != "git@example.com:user/bob.git" {
		t.Fatal(got)
	}
	if got := guidePersonalGit(template, "alice", "bob", "git@example.com:team/custom.git"); got != "git@example.com:team/custom.git" {
		t.Fatal(got)
	}
}

func TestGuideSavesConfigurationThroughManagedTransition(t *testing.T) {
	b, _ := guideTestBridge(t)
	setCurrentBotProfile(t, b, "builtin/codex")
	setCurrentBotRuntime(t, b, "codex")
	state := b.managementState()
	p := newOnboarding(state)
	p.Step = "model"
	if err := b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	old := p
	resp := guideTestAction(t, b, "save", p, nil)
	if resp.Toast.Type != "success" {
		t.Fatalf("save: %#v", resp)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p = guideTestProgress(t, b)
		if p.Step == "tasks" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p.Step != "tasks" {
		t.Fatalf("settings never completed %#v", p)
	}
	saved := b.managementState()
	if saved.DefaultAgent != state.DefaultAgent || saved.DefaultRuntimeRef != "codex" || p.Draft.DefaultReasoningEffort != saved.DefaultReasoningEffort {
		t.Fatalf("incorrect settings %#v", saved)
	}
	guideTestAction(t, b, "save", old, nil)
	if got := b.managementState().Version; got != saved.Version {
		t.Fatalf("duplicate callback changed settings: %d != %d", got, saved.Version)
	}
}

func TestGuidePreservesRequiredIsolation(t *testing.T) {
	b, _ := guideTestBridge(t)
	setCurrentBotProfile(t, b, "builtin/codex")
	setCurrentBotRuntime(t, b, "codex")
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), "thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.db.CommitThreadBotSettingsWithLaunch(b.currentBridgeId(), state.Version, state.DefaultAgent, "codex", "", "", true); err != nil {
		t.Fatal(err)
	}
	p := newOnboarding(b.managementState())
	p.Step = "mode"
	if err = b.db.saveOnboarding(b.currentBridgeId(), "owner", &p); err != nil {
		t.Fatal(err)
	}
	resp := guideTestAction(t, b, "mode", p, map[string]interface{}{"interaction": "singleton"})
	if resp.Toast.Type != "error" {
		t.Fatalf("isolation downgrade accepted %#v", resp)
	}
	if got := guideTestProgress(t, b); got.Draft.Interaction != interactionModeThreadIsolated || got.Revision != p.Revision {
		t.Fatalf("isolation changed %#v", got)
	}
}
