package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

var ErrOnboardingOwnerRequired = errors.New("onboarding owner is not configured")

const guideAction = "bot.guide."
const managementActionGuideOpen = guideAction + "open"
const guideEdit = guideAction + "edit"
const guideSave = guideAction + "save"

// Progress contains drafts and receipts, never credentials. A submitted request
// stays submitted across restarts; reentry must not replay external work.
type onboardingProgress struct {
	Revision          int64
	Step              string
	Resume            string
	Draft             managementSettingsDraft
	ConfigVersion     int64
	Expert            onboardingExpert
	Kind              string
	Prompt            string
	RootID            string
	MessageID         string
	Submission        string
	Result            string
	DispatchStartedAt int64
	TurnID            string
	ReasoningSelected bool
	PendingWelcome    bool
	ImportGitURL      string
}

type onboardingExpert struct {
	Name, ID, Responsibility, Username, GitURL, Skills string
	SearchSkills                                       bool
}

func (db *bridgeDB) loadOnboarding(bridgeID, openID string) (onboardingProgress, error) {
	var raw string
	var revision int64
	err := db.db.QueryRow(`SELECT revision,body FROM feishu_onboarding WHERE bridge_id=? AND open_id=?`, bridgeID, openID).Scan(&revision, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return onboardingProgress{}, nil
	}
	if err != nil {
		return onboardingProgress{}, err
	}
	var p onboardingProgress
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return p, err
	}
	p.Revision = revision
	return p, nil
}

func (db *bridgeDB) saveOnboarding(bridgeID, openID string, p *onboardingProgress) error {
	if bridgeID == "" || openID == "" {
		return fmt.Errorf("missing onboarding identity")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var res sql.Result
	if p.Revision == 0 {
		res, err = db.db.Exec(`INSERT OR IGNORE INTO feishu_onboarding(bridge_id,open_id,revision,body) VALUES(?,?,1,?)`, bridgeID, openID, string(raw))
	} else {
		res, err = db.db.Exec(`UPDATE feishu_onboarding SET revision=revision+1,body=? WHERE bridge_id=? AND open_id=? AND revision=?`, string(raw), bridgeID, openID, p.Revision)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errBotProfileStateChanged
	}
	p.Revision++
	return nil
}

func guideCopy(key string, args ...any) i18n.Text { return managementCopy("guide."+key, args...) }
func guideButton(p onboardingProgress, action, key, kind string) managementButton {
	return managementButton{Text: guideCopy(key), Type: kind, Action: guideAction + action, Name: guideAction + action, Values: map[string]string{"revision": strconv.FormatInt(p.Revision, 10)}}
}
func guideField(name, key, value string, required bool) cardElement {
	return cardElement{Tag: "input", Name: name, DefaultValue: value, Placeholder: managementPlainTextPtr(guideCopy(key)), Required: required, Width: "fill"}
}
func guideForm(p onboardingProgress, action, key string, fields ...cardElement) cardElement {
	var labeled []cardElement
	for _, field := range fields {
		if field.Placeholder != nil {
			labeled = append(labeled, cardElement{Tag: "div", Text: field.Placeholder})
		}
		labeled = append(labeled, field)
	}
	fields = append(labeled, guideSubmitButton(p, action, key, "primary"))
	return cardElement{Tag: "form", Name: "onboarding_" + action, Direction: "vertical", VerticalSpacing: "8px", Elements: fields}
}

func guideSubmitButton(p onboardingProgress, action, key, kind string) cardElement {
	button := managementFormSubmitButton(guideAction+action, guideCopy(key))
	button.Value, _ = json.Marshal(managementButtonValue(guideButton(p, action, key, kind)))
	button.Type = kind
	button.Width = "fill"
	return button
}
func guideDraftState(state managementState, p onboardingProgress) managementState {
	state.Mode = p.Draft.Interaction
	state.DefaultAgent = p.Draft.DefaultAgent
	state.DefaultRuntimeRef = p.Draft.DefaultRuntimeRef
	state.DefaultModelRef = p.Draft.DefaultModelRef
	state.DefaultReasoningEffort = p.Draft.DefaultReasoningEffort
	return state
}
func guideStrongest(state managementState, draft managementSettingsDraft) string {
	options := managementRuntimeReasoningOptions(managementSelectedModelState(state, draft.DefaultModelRef, ""), draft.DefaultRuntimeRef, draft.DefaultModelRef)
	// Only choose a capability supplied by the catalog, never invent one for an
	// unknown native model. Runtime-specific levels retain their native spelling.
	for _, want := range []string{"max", "xhigh", "high"} {
		for _, o := range options {
			if o.Value == want {
				return want
			}
		}
	}
	return ""
}
func newOnboarding(state managementState) onboardingProgress {
	draft := managementDraftFromState(state)
	if draft.DefaultReasoningEffort == "" {
		draft.DefaultReasoningEffort = guideStrongest(state, draft)
	}
	return onboardingProgress{Step: "welcome", Draft: draft, ConfigVersion: state.Version, ReasoningSelected: state.DefaultReasoningEffort != ""}
}

func renderGuideCard(state managementState, p onboardingProgress, notice any) string {
	if p.Step == "" {
		p = newOnboarding(state)
	}
	elements := []cardElement{}
	if notice != nil {
		elements = append(elements, managementTextElement(notice, "normal", "orange", nil))
	}
	add := func(key string) {
		elements = append(elements, managementTextElement(guideCopy(key), "normal", "default", nil))
	}
	var buttons []managementButton
	title := p.Step
	switch p.Step {
	case "welcome":
		add("welcome.body")
		elements = append(elements, managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.default.expert"), Body: managementProfileNameByRef(state, state.DefaultAgent)}, managementSummaryItem{Title: managementCopy("management.default.model"), Body: managementRuntimeModelLabel(state.DefaultModelRef)}))
		if p.Resume != "" {
			buttons = append(buttons, guideButton(p, "resume", "resume", "primary"))
		}
		buttons = append(buttons, guideButton(p, "start", "start", "primary"), guideButton(p, "create", "create", "default"), guideButton(p, "done", "direct", "default"))
	case "mode":
		add("mode.body")
		field := managementSelectStaticElement("interaction", guideCopy("mode.choose"), p.Draft.Interaction, []cardOption{{Text: managementPlainText(managementInteractionText(config.FeishuInteractionThread)), Value: config.FeishuInteractionThread}, {Text: managementPlainText(managementInteractionText(config.FeishuInteractionSingleton)), Value: config.FeishuInteractionSingleton}})
		if p.Draft.Interaction == interactionModeThreadIsolated {
			field.Options = append(field.Options, cardOption{Text: managementPlainText(managementInteractionText(interactionModeThreadIsolated)), Value: interactionModeThreadIsolated})
		}
		elements = append(elements, guideForm(p, "mode", "next", field), managementTextElement(guideCopy("mode.shared.title"), "heading-3", "default", nil), managementTextElement(guideCopy("mode.shared.body"), "normal", "default", nil))
	case "model":
		add("model.body")
		add("model.reasoning")
		elements = append(elements, managementSettingsEditorElements(state, "guide_model", p.Draft, guideSave, guideCopy("save.task"), "", nil, cardElement{})...)
		stampGuideElements(elements, p)
	case "applying":
		add("applying.body")
	case "tasks":
		add("tasks.body")
		for _, kind := range []string{"meetings", "room", "joke"} {
			add("sample." + kind)
			button := managementButtonElement(guideButton(p, kind, "sample.action."+kind, "primary"))
			button.Width = "fill"
			elements = append(elements, button, managementDividerElement())
		}
		buttons = append(buttons, guideButton(p, "explore", "explore", "default"))
	case "review_task":
		add("task.review")
		elements = append(elements, managementTextElement(p.Prompt, "normal", "default", nil))
		if p.Kind == "joke" {
			add("schedule.body")
		}
		buttons = append(buttons, guideButton(p, "send", "send", "primary"), guideButton(p, "tasks", "back", "default"))
	case "running":
		add("task.sent")
		if p.Result != "" {
			elements = append(elements, managementTextElement(guideCopy("task.status", guideCopy(p.Result)), "notation", "grey", nil))
		}
		if p.Kind == "joke" {
			add("schedule.body")
			add("schedule.cancel")
		}
		buttons = append(buttons, guideButton(p, "refresh", "refresh", "default"), guideButton(p, "explore", "explore", "default"))
	case "explore":
		add("expert.body")
		add("skill.body")
		add("library.body")
		buttons = append(buttons, guideButton(p, "select", "select", "primary"), guideButton(p, "import", "import", "default"), guideButton(p, "create", "create", "default"))
	case "select":
		add("expert.body")
		var coordinatorName any = managementCopy("management.default.expert")
		for _, profile := range state.Profiles {
			if distributionCoordinationProfile(profile.Ref) {
				coordinatorName = profile.Name
				break
			}
		}
		elements = append(elements, managementTextElement(guideCopy("select.body", coordinatorName, coordinatorName), "normal", "default", nil))
		profiles := append([]managementProfileSummary{}, state.Profiles...)
		sort.SliceStable(profiles, func(i, j int) bool { return guideProfileRank(profiles[i]) < guideProfileRank(profiles[j]) })
		state.Profiles = profiles
		field := managementSelectStaticElement("default_agent", guideCopy("select"), p.Draft.DefaultAgent, managementProfileOptions(state, true))
		elements = append(elements, guideForm(p, "select.preview", "next", field))
		buttons = append(buttons, guideButton(p, "coordinator", "coordinator.try", "default"))
	case "select_review":
		add("select.confirm")
		elements = append(elements, managementTextElement(managementProfileNameByRef(state, p.Draft.DefaultAgent), "heading-3", "default", nil))
		if profile, err := agentprofile.ResolveProfile(agentprofile.DefaultPackRegistryPath(), p.Draft.DefaultAgent); err == nil {
			elements = append(elements, managementTextElement(profile.Description, "normal", "default", nil), managementTextElement(guideCopy("expert.skills", strings.Join(profile.Skills, ", ")), "normal", "default", nil))
		}
		buttons = append(buttons, guideButton(p, "skill", "skill.manage", "default"))
		buttons = append(buttons, guideButton(p, "switch", "switch", "primary"), guideButton(p, "select", "back", "default"))
	case "import":
		add("library.body")
		add("import.body")
		elements = append(elements, guideForm(p, "import.preview", "next", guideField("git_url", "git", p.ImportGitURL, true)))
	case "create":
		add("expert.body")
		add("library.body")
		add("create.body")
		options := bridgeExtensions.snapshot().Onboarding
		form := guideForm(p, "create.preview", "preview", guideField("expert_name", "name", p.Expert.Name, true), guideField("expert_id", "id", p.Expert.ID, true), guideField("responsibility", "responsibility", p.Expert.Responsibility, true), guideField("username", "username", p.Expert.Username, false), guideField("git_url", "git", p.Expert.GitURL, false), guideField("skills", "skills", p.Expert.Skills, false))
		if options.SkillCatalogURL != "" {
			// Both actions submit the same native form, including unsaved input.
			form.Elements = append(form.Elements, guideSubmitButton(p, "create.skills.preview", "skill.catalog", "default"))
		}
		elements = append(elements, form)
		add("skill.body")
		add("skill.maintenance")
	case "review_expert":
		add("expert.review")
		elements = append(elements, managementKeyValueListElement("expert_review", managementSummaryItem{Title: guideCopy("name"), Body: p.Expert.Name}, managementSummaryItem{Title: guideCopy("id"), Body: p.Expert.ID}, managementSummaryItem{Title: guideCopy("responsibility"), Body: p.Expert.Responsibility}, managementSummaryItem{Title: guideCopy("git"), Body: p.Expert.GitURL}, managementSummaryItem{Title: guideCopy("skills"), Body: p.Expert.Skills}))
		if p.Expert.SearchSkills {
			add("skill.catalog.plan")
		}
		buttons = append(buttons, guideButton(p, "send.expert", "prepare", "primary"), guideButton(p, "create", "back", "default"))
	case "review_import":
		add("import.review")
		elements = append(elements, managementTextElement(p.ImportGitURL, "normal", "default", nil))
		buttons = append(buttons, guideButton(p, "send.import", "send", "primary"), guideButton(p, "import", "back", "default"))
	case "done":
		add("done.body")
		buttons = append(buttons, managementButton{Text: managementCopy("management.expert.library"), Action: managementActionPackLibraryOpen, Type: "primary"}, guideButton(p, "open", "reopen", "default"))
	default:
		title = "welcome"
		add("welcome.body")
		buttons = append(buttons, guideButton(p, "start", "start", "primary"))
	}
	if back := map[string]string{"mode": "open", "model": "back.mode", "tasks": "back.model", "explore": "tasks", "select": "explore", "import": "explore", "create": "explore", "running": "tasks"}[p.Step]; back != "" {
		buttons = append(buttons, guideButton(p, back, "back.step", "default"))
	}
	if p.Step != "welcome" && p.Step != "done" && p.Step != "applying" {
		buttons = append(buttons, guideButton(p, "done", "finish", "default"))
	}
	// The native card is narrow on both desktop and mobile. Each action has its
	// own row instead of competing with long labels in a flowing column set.
	for _, action := range buttons {
		button := managementButtonElement(action)
		button.Width = "fill"
		elements = append(elements, button)
	}
	card := marshalNativeManagementCard(managementCardView{Title: guideCopy("title." + title), Summary: guideCopy("title"), Subtitle: guideCopy("subtitle"), Theme: "blue", Elements: elements})
	return renderFocusedManagementCardForSurface(card, managementSurfaceAmux)
}

func guideProfileRank(p managementProfileSummary) int {
	if distributionCoordinationProfile(p.Ref) {
		return 0
	}
	if strings.HasPrefix(p.Ref, "builtin/") {
		return 1
	}
	return 2
}

func stampGuideElements(elements []cardElement, p onboardingProgress) {
	for i := range elements {
		e := &elements[i]
		if len(e.Value) > 0 {
			var values map[string]string
			if json.Unmarshal(e.Value, &values) == nil {
				values["revision"] = strconv.FormatInt(p.Revision, 10)
				e.Value, _ = json.Marshal(values)
			}
		}
		for j := range e.Behaviors {
			if e.Behaviors[j].Value != nil {
				e.Behaviors[j].Value["revision"] = strconv.FormatInt(p.Revision, 10)
			}
		}
		stampGuideElements(e.Elements, p)
		for j := range e.Columns {
			stampGuideElements(e.Columns[j].Elements, p)
		}
	}
}

var guideUsernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
var guideIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func guideRemoteGit(raw string) bool {
	raw = strings.TrimSpace(raw)
	if strings.ContainsAny(raw, "\n\r\t ") || strings.HasPrefix(raw, "-") {
		return false
	}
	if strings.HasPrefix(raw, "git@") {
		parts := strings.SplitN(raw, ":", 2)
		return len(parts) == 2 && len(parts[0]) > 4 && parts[1] != "" && !strings.HasPrefix(parts[1], "/")
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "ssh" || u.Scheme == "https") && u.Host != "" && u.Path != "" && (u.User == nil || (u.Scheme == "ssh" && !strings.Contains(u.User.String(), ":")))
}

func (b *Bridge) handleGuideAction(ctx context.Context, event *callback.CardActionTriggerEvent, action, openID, messageID string) (*callback.CardActionTriggerResponse, bool, error) {
	if !strings.HasPrefix(action, guideAction) {
		return nil, false, nil
	}
	if b.db == nil || !b.isBotOwner(openID) || !b.db.isPrivateManagementCard(b.currentBridgeId(), openID, messageID) {
		return cardToast("error", guideCopy("private")), true, nil
	}
	p, err := b.db.loadOnboarding(b.currentBridgeId(), openID)
	if err != nil {
		return cardToast("error", guideCopy("storage")), true, nil
	}
	state := b.managementState()
	a := event.Event.Action
	var inlineCard string
	respond := func(kind string, content any) *callback.CardActionTriggerResponse {
		if inlineCard == "" {
			return cardToast(kind, content)
		}
		response := cardToastWithCard(kind, content, inlineCard)
		if kind == "success" {
			response.Toast = nil
		}
		return response
	}
	update := func(notice any) {
		card := renderGuideCard(state, p, notice)
		if p.Step == "applying" || p.Step == "running" {
			b.patchGuideCard(openID, messageID, p, card)
			return
		}
		// Return navigation and draft edits in the callback itself. An asynchronous
		// message PATCH can lag behind the toast and leave the visible form stale.
		inlineCard = b.managementCardForPatch(openID, messageID, card)
	}
	if p.Step == "running" && (p.Submission == "pending" || p.Submission == "dispatching") {
		if time.Now().Unix()-p.DispatchStartedAt <= 90 {
			update(guideCopy("busy"))
			return respond("warning", guideCopy("busy")), true, nil
		}
		p.Submission = "uncertain"
		p.Result = "task.uncertain"
		if b.db.saveOnboarding(b.currentBridgeId(), openID, &p) != nil {
			return respond("warning", guideCopy("stale")), true, nil
		}
	}
	if action == guideAction+"refresh" {
		update(nil)
		return respond("success", guideCopy("updated")), true, nil
	}
	if action == managementActionGuideOpen {
		if p.Step == "applying" {
			p.Step = "model"
			p.ConfigVersion = state.Version
		}
		if p.Step == "" {
			p = newOnboarding(state)
		} else if p.Step != "welcome" {
			p.Resume = p.Step
			p.Step = "welcome"
		}
	} else {
		revision, parseErr := strconv.ParseInt(managementCallbackString(a, "revision"), 10, 64)
		if parseErr != nil || revision != p.Revision || p.Revision == 0 {
			update(guideCopy("stale"))
			return respond("warning", guideCopy("stale")), true, nil
		}
		switch strings.TrimPrefix(action, guideAction) {
		case "back.mode":
			p.Step = "mode"
		case "back.model":
			p.Step = "model"
		case "resume":
			if p.Resume != "" {
				p.Step = p.Resume
				p.Resume = ""
			}
		case "start":
			p.Step = "mode"
			if p.ConfigVersion != state.Version || p.Draft.Interaction == "" {
				p.ConfigVersion = state.Version
				p.Draft = managementDraftFromState(state)
				p.ReasoningSelected = state.DefaultReasoningEffort != ""
				if p.Draft.DefaultReasoningEffort == "" {
					p.Draft.DefaultReasoningEffort = guideStrongest(state, p.Draft)
				}
			}
		case "mode":
			if p.Step != "mode" {
				return respond("warning", guideCopy("stale")), true, nil
			}
			mode := managementCallbackString(a, "interaction")
			if mode != config.FeishuInteractionThread && mode != config.FeishuInteractionSingleton && mode != interactionModeThreadIsolated {
				return respond("error", guideCopy("invalid")), true, nil
			}
			// A required isolation policy cannot be removed by the tutorial.
			if state.Mode == interactionModeThreadIsolated && mode != interactionModeThreadIsolated {
				return respond("error", guideCopy("isolation")), true, nil
			}
			p.Draft.Interaction = mode
			p.Step = "model"
		case "edit":
			if p.Step != "model" {
				return respond("warning", guideCopy("stale")), true, nil
			}
			old := p.Draft
			if managementDraftFieldFromCallback(a, state) == "default_reasoning_effort" {
				p.ReasoningSelected = true
			}
			p.Draft = managementDraftFromCallback(modelServiceDraftAction(a), guideDraftState(state, p))
			if old.DefaultRuntimeRef != p.Draft.DefaultRuntimeRef || old.DefaultModelRef != p.Draft.DefaultModelRef {
				if !p.ReasoningSelected {
					p.Draft.DefaultReasoningEffort = guideStrongest(state, p.Draft)
				}
			}
		case "save":
			if p.Step != "model" {
				return respond("warning", guideCopy("stale")), true, nil
			}
			if p.ConfigVersion != state.Version {
				p.ConfigVersion = state.Version
				if b.db.saveOnboarding(b.currentBridgeId(), openID, &p) != nil {
					return respond("warning", guideCopy("stale")), true, nil
				}
				update(guideCopy("config.changed"))
				return respond("warning", guideCopy("config.changed")), true, nil
			}
			p.Draft = managementDraftFromCallback(a, guideDraftState(state, p))
			if state.Mode == interactionModeThreadIsolated && p.Draft.Interaction != interactionModeThreadIsolated {
				return respond("error", guideCopy("isolation")), true, nil
			}
			for _, validate := range []func(*callback.CallBackAction, managementState) any{managementProfileSubmissionError, managementRuntimeSubmissionError, managementModelSubmissionError, managementReasoningSubmissionError} {
				if message := validate(a, state); message != "" {
					return respond("error", message), true, nil
				}
			}
			p.Step = "applying"
			p.DispatchStartedAt = time.Now().Unix()
		case "tasks", "explore", "select", "import", "create":
			p.Step = strings.TrimPrefix(action, guideAction)
			if p.Step == "select" {
				p.Draft = managementDraftFromState(state)
				p.ConfigVersion = state.Version
			}
		case "meetings", "room", "joke", "coordinator":
			p.Kind = strings.TrimPrefix(action, guideAction)
			p.Prompt = guideCopy("sample." + p.Kind).For(b.guideLocale(openID))
			p.Step = "review_task"
		case "select.preview":
			if p.Step != "select" {
				return respond("warning", guideCopy("stale")), true, nil
			}
			ref := managementCallbackString(a, "default_agent")
			if !managementValidDefaultAgent(state, ref) {
				return respond("error", guideCopy("invalid")), true, nil
			}
			p.Draft.DefaultAgent = ref
			p.Step = "select_review"
		case "switch":
			if p.Step != "select_review" || p.ConfigVersion != state.Version || !managementValidDefaultAgent(state, p.Draft.DefaultAgent) {
				return respond("warning", guideCopy("config.changed")), true, nil
			}
			p.Draft = managementSettingsDraft{Interaction: state.Mode, DefaultAgent: p.Draft.DefaultAgent, DefaultRuntimeRef: managementEffectiveRuntimeRef(state), DefaultModelRef: state.DefaultModelRef, DefaultReasoningEffort: state.DefaultReasoningEffort, ThinkingProcess: state.ThinkingProcess, DailyCleanup: state.DailyCleanup}
			p.Step = "applying"
			p.DispatchStartedAt = time.Now().Unix()
		case "skill":
			if p.Step != "select_review" {
				return respond("warning", guideCopy("stale")), true, nil
			}
			p.Kind = "skill"
			p.Prompt = guideCopy("prompt.skill", p.Draft.DefaultAgent).For(b.guideLocale(openID))
			p.Step = "review_task"
		case "create.preview", "create.skills.preview":
			if p.Step != "create" {
				return respond("warning", guideCopy("stale")), true, nil
			}
			p.Expert.SearchSkills = action == guideAction+"create.skills.preview"
			if p.Expert.SearchSkills && bridgeExtensions.snapshot().Onboarding.SkillCatalogURL == "" {
				return respond("error", guideCopy("invalid")), true, nil
			}
			p.Expert.Name = managementCallbackString(a, "expert_name")
			p.Expert.ID = managementCallbackString(a, "expert_id")
			p.Expert.Responsibility = managementCallbackString(a, "responsibility")
			p.Expert.Skills = managementCallbackString(a, "skills")
			username := managementCallbackString(a, "username")
			gitURL := managementCallbackString(a, "git_url")
			template := bridgeExtensions.snapshot().Onboarding.GitURLTemplate
			if username != "" && !guideUsernamePattern.MatchString(username) {
				return respond("error", guideCopy("invalid")), true, nil
			}
			gitURL = guidePersonalGit(template, p.Expert.Username, username, gitURL)
			p.Expert.Username = username
			p.Expert.GitURL = gitURL
			if p.Expert.Name == "" || len([]rune(p.Expert.Name)) > 100 || !guideIDPattern.MatchString(p.Expert.ID) || p.Expert.Responsibility == "" || len(p.Expert.Responsibility) > 4000 || len(p.Expert.Skills) > 8000 || !guideRemoteGit(gitURL) {
				if b.db.saveOnboarding(b.currentBridgeId(), openID, &p) != nil {
					return respond("error", guideCopy("storage")), true, nil
				}
				update(guideCopy("invalid.expert"))
				return respond("error", guideCopy("invalid.expert")), true, nil
			}
			p.Step = "review_expert"
		case "import.preview":
			if p.Step != "import" {
				return respond("warning", guideCopy("stale")), true, nil
			}
			p.ImportGitURL = managementCallbackString(a, "git_url")
			if !guideRemoteGit(p.ImportGitURL) {
				return respond("error", guideCopy("invalid.git")), true, nil
			}
			p.Step = "review_import"
		case "send", "send.expert", "send.import":
			expected := map[string]string{"send": "review_task", "send.expert": "review_expert", "send.import": "review_import"}[strings.TrimPrefix(action, guideAction)]
			if p.Step != expected {
				return respond("warning", guideCopy("stale")), true, nil
			}
			if expected == "review_expert" {
				p.Kind = "expert"
				p.Prompt = guideCopy("prompt.expert", p.Expert.Name, p.Expert.ID, p.Expert.Responsibility, p.Expert.GitURL, p.Expert.Skills).For(b.guideLocale(openID))
				if p.Expert.SearchSkills {
					p.Prompt += "\n\n" + guideCopy("prompt.skill.search", bridgeExtensions.snapshot().Onboarding.SkillCatalogURL).For(b.guideLocale(openID))
				}
			}
			if expected == "review_import" {
				p.Kind = "import"
				p.Prompt = guideCopy("prompt.import", p.ImportGitURL).For(b.guideLocale(openID))
			}
			p.Step = "running"
			p.Submission = "pending"
			p.DispatchStartedAt = time.Now().Unix()
			p.RootID = ""
			p.Result = ""
			p.TurnID = ""
		case "refresh":
		case "done":
			p.Step = "done"
		default:
			return respond("error", guideCopy("invalid")), true, nil
		}
	}
	p.PendingWelcome = false
	p.MessageID = messageID
	if err := b.db.saveOnboarding(b.currentBridgeId(), openID, &p); err != nil {
		return respond("warning", guideCopy("stale")), true, nil
	}
	update(nil)
	if p.Step == "applying" {
		b.runManagementSettingsTransition(openID, messageID, state, p.Draft, managementBotSettingsRequest(state, p.Draft, messageID, openID), true, managementSurfaceAmux)
	}
	if p.Step == "model" {
		b.refreshGuideModels(openID, messageID, p, a)
	}
	if p.Step == "create" && p.Expert.Username == "" {
		b.resolveGuideUsername(openID, messageID, p)
	}
	if p.Step == "running" && p.Submission == "pending" && strings.HasPrefix(action, guideAction+"send") {
		chatID := ""
		if event.Event.Context != nil {
			chatID = event.Event.Context.OpenChatID
		}
		go b.dispatchGuideTask(openID, chatID, p)
	}
	notice := guideCopy("updated")
	if p.Step == "applying" {
		notice = guideCopy("saving")
	}
	if p.Step == "running" {
		notice = guideCopy("submitting")
	}
	return respond("success", notice), true, nil
}

func (b *Bridge) guideLocale(openID string) i18n.Locale {
	locale := b.managementLocale(openID)
	if locale == "" {
		return i18n.Chinese
	}
	return locale
}

func (b *Bridge) completeGuideSettings(openID string, state managementState) string {
	if b.db != nil {
		p, err := b.db.loadOnboarding(b.currentBridgeId(), openID)
		if err == nil && p.Step == "applying" {
			p.Step = "tasks"
			p.Draft = managementDraftFromState(state)
			p.ConfigVersion = state.Version
			if b.db.saveOnboarding(b.currentBridgeId(), openID, &p) == nil {
				return renderGuideCard(state, p, nil)
			}
		}
	}
	return renderOnboardingCompleteCard(state, managementDraftFromState(state))
}

func (b *Bridge) resolveGuideUsername(openID, messageID string, p onboardingProgress) {
	options := bridgeExtensions.snapshot().Onboarding
	if options.ResolveUsername == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		name, err := options.ResolveUsername(ctx)
		if err != nil || !guideUsernamePattern.MatchString(name) {
			return
		}
		current, err := b.db.loadOnboarding(b.currentBridgeId(), openID)
		if err != nil || current.Revision != p.Revision {
			return
		}
		current.Expert.Username = name
		if current.Expert.GitURL == "" {
			current.Expert.GitURL = strings.ReplaceAll(options.GitURLTemplate, "{username}", name)
		}
		if b.db.saveOnboarding(b.currentBridgeId(), openID, &current) == nil {
			b.patchGuideCard(openID, messageID, current, renderGuideCard(b.managementState(), current, nil))
		}
	}()
}

func (b *Bridge) refreshGuideModels(openID, messageID string, p onboardingProgress, a *callback.CallBackAction) {
	service, operation := modelServiceSelection(a)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		for {
			state := guideDraftState(b.managementState(), p)
			if config.SupportsAgentExecutable(p.Draft.DefaultRuntimeRef) {
				state = b.loadNativeModels(ctx, state, openID, p.Draft.DefaultRuntimeRef)
			}
			state = b.loadModelServices(ctx, state, openID, service, operation)
			current, err := b.db.loadOnboarding(b.currentBridgeId(), openID)
			if err != nil || current.Revision != p.Revision || current.Step != "model" {
				return
			}
			if !current.ReasoningSelected {
				strongest := guideStrongest(state, current.Draft)
				if strongest != "" && strongest != current.Draft.DefaultReasoningEffort {
					current.Draft.DefaultReasoningEffort = strongest
					if b.db.saveOnboarding(b.currentBridgeId(), openID, &current) != nil {
						return
					}
					p = current
				}
			}
			b.patchGuideCard(openID, messageID, current, renderGuideCard(state, current, nil))
			pending := false
			for _, entry := range state.ModelServices {
				if entry.Service.ID == service && (entry.State.Status == "pending" || entry.State.Status == "installing") {
					pending = true
				}
			}
			if !pending {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			operation = "refresh"
		}
	}()
}

func (b *Bridge) dispatchGuideTask(openID, chatID string, p onboardingProgress) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	fail := func() {
		current, err := b.db.loadOnboarding(b.currentBridgeId(), openID)
		if err != nil || current.Revision != p.Revision {
			return
		}
		current.Submission = "uncertain"
		current.Result = "task.uncertain"
		if b.db.saveOnboarding(b.currentBridgeId(), openID, &current) == nil {
			b.patchGuideCard(openID, p.MessageID, current, renderGuideCard(b.managementState(), current, guideCopy("task.uncertain")))
		}
	}
	current, readErr := b.db.loadOnboarding(b.currentBridgeId(), openID)
	if readErr != nil || current.Revision != p.Revision {
		return
	}
	if chatID == "" {
		fail()
		return
	}
	if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), chatID, WorkspaceKindPrivate); err != nil {
		fail()
		return
	}
	// The root is a real Feishu message, so ordinary reply routing, authorization
	// and later schedule replies retain their normal conversation binding.
	card := marshalNativeManagementCard(managementCardView{Title: guideCopy("task.request"), Summary: guideCopy("task.request"), Theme: "blue", Elements: []cardElement{managementTextElement(p.Prompt, "normal", "default", nil)}})
	var rootID string
	var err error
	if b.chatNotice != nil {
		rootID, _, err = b.chatNotice(ctx, chatID, b.managementCardForRecipient(openID, card))
	} else {
		rootID, _, err = b.sendInteractiveToChat(ctx, chatID, b.managementCardForRecipient(openID, card), "amux onboarding task")
	}
	if err != nil {
		fail()
		return
	}
	p.RootID = rootID
	p.Submission = "dispatching"
	if err = b.db.saveOnboarding(b.currentBridgeId(), openID, &p); err != nil {
		return
	}
	prompt := p.Prompt
	if p.Kind == "joke" {
		prompt += "\n" + guideCopy("prompt.schedule").For(b.guideLocale(openID))
	}
	prompt += "\n" + bridgeExtensions.snapshot().Onboarding.PromptGuidance
	mention := b.textBotMentionInfo(prompt)
	mention.Mentioned = true
	if p.Kind == "coordinator" {
		coordinator := ""
		for _, profile := range b.managementState().Profiles {
			if distributionCoordinationProfile(profile.Ref) {
				coordinator = profile.Ref
				break
			}
		}
		if coordinator == "" {
			fail()
			return
		}
		err = b.routeToSessionInChatWithMentionOptions(ctx, chatID, rootID, rootID, openID, prompt, mention, routeInputOptions{agentProfileRef: coordinator})
	} else if handled, routeErr := b.routeBotSessionInput(ctx, botSessionInput{ChatID: chatID, MessageID: rootID, OpenID: openID, Text: prompt, ReplyKind: botSessionReplyDM}); handled || routeErr != nil {
		err = routeErr
	} else {
		err = b.routeToSessionInChatWithMention(ctx, chatID, rootID, rootID, openID, prompt, mention)
	}
	if err != nil {
		fail()
		return
	}
	current, loadErr := b.db.loadOnboarding(b.currentBridgeId(), openID)
	if loadErr == nil && current.RootID == p.RootID && current.Submission == "dispatching" {
		fail()
	}
	// Accepted state is written only by observeGuideSubmission with a real TurnID.
}

func (b *Bridge) patchGuideCard(openID, messageID string, p onboardingProgress, card string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b.writeManagementCardFromCallbackIf(ctx, openID, messageID, card, func() bool {
			current, err := b.db.loadOnboarding(b.currentBridgeId(), openID)
			return err == nil && current.Revision == p.Revision
		})
	}()
}
func (b *Bridge) sendGuideWelcome(ctx context.Context, openID string) error {
	state := b.managementState()
	p, err := b.db.loadOnboarding(b.currentBridgeId(), openID)
	if err != nil {
		return err
	}
	if p.Step == "" || p.Draft.Interaction == "" {
		revision := p.Revision
		p = newOnboarding(state)
		p.Revision = revision
	}
	p.PendingWelcome = false
	if err = b.db.saveOnboarding(b.currentBridgeId(), openID, &p); err != nil {
		return err
	}
	return b.sendManagementCardToDM(ctx, openID, renderGuideCard(state, p, nil))
}

// SendOnboardingGuide delivers the real product card to the saved owner. It
// does not start another event consumer or change an existing conversation.
func SendOnboardingGuide(ctx context.Context, profile string) error {
	secrets, err := LoadProfileSecrets(profile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(secrets.OpenId) == "" || strings.TrimSpace(secrets.AppId) == "" || strings.TrimSpace(secrets.AppSecret) == "" {
		return ErrOnboardingOwnerRequired
	}
	db, err := openBridgeDBForProfile(DefaultDBPath(), profile)
	if err != nil {
		return err
	}
	defer db.Close()
	b := &Bridge{db: db, profile: profile, secrets: secrets, bridgeID: BridgeIdFromAppId(secrets.AppId), client: lark.NewClient(secrets.AppId, secrets.AppSecret, larkClientOptions()...)}
	return b.sendGuideWelcome(ctx, secrets.OpenId)
}

func guidePersonalGit(template, previousUsername, username, gitURL string) string {
	if template != "" && username != "" && (gitURL == "" || gitURL == strings.ReplaceAll(template, "{username}", previousUsername)) {
		return strings.ReplaceAll(template, "{username}", username)
	}
	return gitURL
}

func (b *Bridge) observeGuideSubmission(chat *LarkChat, turnID string, inputs []turnInputRef) {
	if chat == nil || b.db == nil || b.secrets == nil || turnID == "" {
		return
	}
	owner := b.secrets.OpenId
	p, err := b.db.loadOnboarding(b.currentBridgeId(), owner)
	if err != nil || p.Step != "running" || p.Submission != "dispatching" {
		return
	}
	matched := chat.RootMsgID == p.RootID
	for _, in := range inputs {
		if in.MessageID == p.RootID && in.OpenID == owner {
			matched = true
		}
	}
	if !matched {
		return
	}
	// In continuous mode the accepted turn identifies the actual conversation.
	p.TurnID = turnID
	p.Submission = "accepted"
	if b.db.saveOnboarding(b.currentBridgeId(), owner, &p) == nil {
		b.patchGuideCard(owner, p.MessageID, p, renderGuideCard(b.managementState(), p, nil))
	}
}
func (b *Bridge) observeGuideCompletion(chat *LarkChat, turnID, status string) {
	if chat == nil || b.db == nil || b.secrets == nil {
		return
	}
	owner := b.secrets.OpenId
	p, err := b.db.loadOnboarding(b.currentBridgeId(), owner)
	if err != nil || p.Step != "running" || p.TurnID == "" || p.TurnID != turnID {
		return
	}
	key := "result.finished"
	if status != "done" && status != "completed" {
		key = "result.failed"
	}
	p.Result = key
	if b.db.saveOnboarding(b.currentBridgeId(), owner, &p) == nil {
		b.patchGuideCard(owner, p.MessageID, p, renderGuideCard(b.managementState(), p, nil))
	}
}

func (b *Bridge) guideSettingsMatch(openID, messageID string, req BotSettingsRequest) bool {
	if b.db == nil {
		return false
	}
	p, err := b.db.loadOnboarding(b.currentBridgeId(), openID)
	if err != nil || p.Step != "applying" || p.MessageID != messageID {
		return false
	}
	interaction, isolation := managementDecomposeInteraction(p.Draft.Interaction)
	return req.Interaction == interaction && req.IsolationRequired == isolation && req.DefaultAgent == p.Draft.DefaultAgent && req.DefaultRuntimeRef == p.Draft.DefaultRuntimeRef && req.DefaultModelRef == p.Draft.DefaultModelRef && req.DefaultReasoningEffort == p.Draft.DefaultReasoningEffort
}

// QueueOnboardingGuide is used only by a newly configured Bot. Existing Bots
// have no marker, so upgrading does not opt their owners into automatic cards.
func QueueOnboardingGuide(profile, appID, owner string) error {
	if appID == "" || owner == "" {
		return nil
	}
	db, err := openBridgeDBForProfile(DefaultDBPath(), profile)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.queueOnboarding(BridgeIdFromAppId(appID), owner); err != nil {
		return err
	}
	return db.initDiscovery(BridgeIdFromAppId(appID), owner, time.Now())
}
func (db *bridgeDB) queueOnboarding(bridgeID, owner string) error {
	p, err := db.loadOnboarding(bridgeID, owner)
	if err != nil || p.Revision != 0 {
		return err
	}
	p = onboardingProgress{Step: "welcome", PendingWelcome: true}
	return db.saveOnboarding(bridgeID, owner, &p)
}
func (b *Bridge) deliverPendingOnboarding(ctx context.Context) error {
	if b.db == nil || b.secrets == nil || !b.ownerConfirmed() {
		return nil
	}
	owner := b.secrets.OpenId
	p, err := b.db.loadOnboarding(b.currentBridgeId(), owner)
	if err != nil || !p.PendingWelcome {
		return err
	}
	revision := p.Revision
	p = newOnboarding(b.managementState())
	p.Revision = revision
	// Claim before delivery. An uncertain Feishu outcome must not be replayed
	// after reconnect; /amux always offers explicit reentry if delivery fails.
	if err = b.db.saveOnboarding(b.currentBridgeId(), owner, &p); err != nil {
		return err
	}
	return b.sendManagementCardToDM(ctx, owner, renderGuideCard(b.managementState(), p, nil))
}
