package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

// managementSettingsState owns settings-editor progress state.
type managementSettingsState struct {
	settingsMu                sync.Mutex
	modelServiceVersions      map[string]uint64
	onboardingCardSentByOpen  map[string]bool
	documentSettingsActive    bool
	botToBotSettingsActive    bool
	groupWatchExecutionActive bool
	permissionRecheckActive   bool
	permissionRecheckMessage  string
	permissionRecheckVersion  uint64
	permissionApplyActive     bool
	permissionApplyMessage    string
	permissionApplyVersion    uint64
}

func managementActionFromCallback(action *callback.CallBackAction) string {
	if action == nil {
		return ""
	}
	if v, ok := action.Value[managementActionKey].(string); ok {
		return strings.TrimSpace(v)
	}
	if v, ok := action.Value["action"].(string); ok && strings.HasPrefix(v, "bot.") {
		return strings.TrimSpace(v)
	}
	if v, ok := action.FormValue[managementActionKey].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func managementDraftFromCallback(action *callback.CallBackAction, state managementState) managementSettingsDraft {
	draft := managementDraftFromState(state)
	if action == nil {
		return draft
	}
	if v, ok := action.Value["interaction"].(string); ok && managementValidInteraction(v) {
		draft.Interaction = strings.TrimSpace(v)
	}
	if v, ok := action.FormValue["interaction"].(string); ok && managementValidInteraction(v) {
		draft.Interaction = strings.TrimSpace(v)
	}
	draftField := managementDraftFieldFromCallback(action, state)
	if draftField == "interaction" {
		if selected := strings.TrimSpace(action.Option); managementValidInteraction(selected) {
			draft.Interaction = selected
		}
	}
	if v, ok := action.Value["default_agent"].(string); ok && managementValidDefaultAgent(state, v) {
		draft.DefaultAgent = strings.TrimSpace(v)
	}
	if v, ok := action.FormValue["default_agent"].(string); ok && managementValidDefaultAgent(state, v) {
		draft.DefaultAgent = strings.TrimSpace(v)
	}
	if v, ok := action.Value["default_runtime_ref"].(string); ok {
		draft.DefaultRuntimeRef = strings.TrimSpace(v)
	}
	if v, ok := action.FormValue["default_runtime_ref"].(string); ok {
		draft.DefaultRuntimeRef = strings.TrimSpace(v)
	}
	if v, ok := action.Value["default_model_ref"].(string); ok {
		draft.DefaultModelRef = managementModelRefFromForm(v)
	}
	if v, ok := action.FormValue["default_model_ref"].(string); ok {
		draft.DefaultModelRef = managementModelRefFromForm(v)
	}
	if draftField == "default_model_ref" {
		draft.DefaultModelRef = managementModelRefFromForm(action.Option)
	}
	if v, ok := action.Value["default_reasoning_effort"].(string); ok {
		draft.DefaultReasoningEffort = managementReasoningEffortFromForm(v)
	}
	if v, ok := action.FormValue["default_reasoning_effort"].(string); ok {
		draft.DefaultReasoningEffort = managementReasoningEffortFromForm(v)
	}
	if v, ok := action.Value["thinking_process"].(string); ok && managementValidThinkingProcess(v) {
		draft.ThinkingProcess = strings.TrimSpace(v)
	}
	if v, ok := action.FormValue["thinking_process"].(string); ok && managementValidThinkingProcess(v) {
		draft.ThinkingProcess = strings.TrimSpace(v)
	}
	if draftField == "default_runtime_ref" {
		draft.DefaultRuntimeRef = strings.TrimSpace(action.Option)
		if !managementValidRuntimeModel(state, draft.DefaultRuntimeRef, draft.DefaultModelRef) {
			draft.DefaultModelRef = ""
		}
	}
	if draftField == "default_reasoning_effort" {
		draft.DefaultReasoningEffort = managementReasoningEffortFromForm(action.Option)
	}
	if draftField == "thinking_process" && managementValidThinkingProcess(action.Option) {
		draft.ThinkingProcess = strings.TrimSpace(action.Option)
	}
	if draftField == "default_agent" {
		if selected := strings.TrimSpace(action.Option); managementValidDefaultAgent(state, selected) {
			draft.DefaultAgent = selected
		}
	}
	if !managementValidRuntimeReasoningEffort(state, draft.DefaultRuntimeRef, draft.DefaultModelRef, draft.DefaultReasoningEffort) {
		draft.DefaultReasoningEffort = ""
	}
	return managementNormalizeDraftForState(state, draft)
}

func managementDraftFieldFromCallback(action *callback.CallBackAction, state managementState) string {
	if action == nil {
		return ""
	}
	for _, field := range []string{"interaction", "default_agent", "default_runtime_ref", "default_model_ref", "default_reasoning_effort", "thinking_process"} {
		if strings.TrimSpace(action.Name) == field {
			return field
		}
	}
	if field, ok := action.Value[managementDraftFieldKey].(string); ok {
		switch strings.TrimSpace(field) {
		case "interaction", "default_agent", "default_runtime_ref", "default_model_ref", "default_reasoning_effort", "thinking_process":
			return strings.TrimSpace(field)
		}
	}
	actionName, _ := action.Value[managementActionKey].(string)
	if actionName != managementActionSettingsEdit && actionName != managementActionOnboardingEdit {
		return ""
	}
	option := strings.TrimSpace(action.Option)
	if option != "" {
		if managementValidDefaultAgent(state, option) {
			return "default_agent"
		}
		if managementValidInteraction(option) {
			return "interaction"
		}
		if managementValidRuntime(state, option) {
			return "default_runtime_ref"
		}
		runtimeRef := managementEffectiveRuntimeRef(state)
		if value, ok := action.Value["default_runtime_ref"].(string); ok && managementValidRuntime(state, value) {
			runtimeRef = strings.TrimSpace(value)
		}
		if option == managementProfileDefaultModelOption || managementValidRuntimeModel(state, runtimeRef, managementModelRefFromForm(option)) {
			return "default_model_ref"
		}
		if option == managementRuntimeDefaultReasoningOption || managementValidRuntimeReasoningEffort(state,
			managementCallbackString(action, "default_runtime_ref"), managementCallbackString(action, "default_model_ref"), managementReasoningEffortFromForm(option)) {
			return "default_reasoning_effort"
		}
		if managementValidThinkingProcess(option) {
			return "thinking_process"
		}
	}
	if action.Name != actionName {
		return ""
	}
	_, ok := action.FormValue["default_agent"]
	if ok {
		return "default_agent"
	}
	return ""
}

func managementModelRefFromForm(value string) string {
	value = strings.TrimSpace(value)
	if value == managementProfileDefaultModelOption {
		return ""
	}
	return value
}

func managementReasoningEffortFromForm(value string) string {
	value = strings.TrimSpace(value)
	if value == managementRuntimeDefaultReasoningOption {
		return ""
	}
	return value
}

func managementProfileSubmissionError(action *callback.CallBackAction, state managementState) any {
	value, submitted := managementSubmittedDefaultAgent(action)
	if submitted {
		if managementValidDefaultAgent(state, value) {
			return ""
		}
		return managementCopy("management.this.expert.is.no.longer.in.the.library.refresh")
	}
	if strings.TrimSpace(state.DefaultAgent) == "" {
		return managementCopy("management.no.experts.are.available.add.experts.first")
	}
	return ""
}

func managementModelSubmissionError(action *callback.CallBackAction, state managementState) any {
	modelRef, submitted := managementSubmittedDefaultModel(action)
	if !submitted || modelRef == "" {
		return ""
	}
	runtimeRef := managementCallbackString(action, "default_runtime_ref")
	if runtimeRef == "" {
		runtimeRef = managementDraftFromState(state).DefaultRuntimeRef
	}
	if managementValidRuntimeModel(state, runtimeRef, modelRef) {
		return ""
	}
	return managementCopy("management.this.model.is.not.supported.by.the.selected.runtime")
}

func managementRuntimeSubmissionError(action *callback.CallBackAction, state managementState) any {
	runtimeRef := managementCallbackString(action, "default_runtime_ref")
	if runtimeRef == "" || managementValidRuntime(state, runtimeRef) {
		return ""
	}
	return managementCopy("management.this.runtime.is.unavailable.refresh.and.choose.again")
}

func managementReasoningSubmissionError(action *callback.CallBackAction, state managementState) any {
	effort := managementReasoningEffortFromForm(managementCallbackString(action, "default_reasoning_effort"))
	if effort == "" {
		return ""
	}
	draft := managementDraftFromCallback(action, state)
	if managementValidRuntimeReasoningEffort(state, draft.DefaultRuntimeRef, draft.DefaultModelRef, effort) {
		return ""
	}
	return managementCopy("management.this.reasoning.effort.is.unavailable.for.the.selected.runtime")
}

func managementSubmittedDefaultAgent(action *callback.CallBackAction) (string, bool) {
	if action == nil {
		return "", false
	}
	if value, ok := action.FormValue["default_agent"].(string); ok {
		return strings.TrimSpace(value), true
	}
	if value, ok := action.Value["default_agent"].(string); ok {
		return strings.TrimSpace(value), true
	}
	return "", false
}

func managementSubmittedDefaultModel(action *callback.CallBackAction) (string, bool) {
	if action == nil {
		return "", false
	}
	if value, ok := action.FormValue["default_model_ref"].(string); ok {
		return managementModelRefFromForm(value), true
	}
	if value, ok := action.Value["default_model_ref"].(string); ok {
		return managementModelRefFromForm(value), true
	}
	return "", false
}

func managementBotSettingsRequest(state managementState, draft managementSettingsDraft, messageID, openID string) BotSettingsRequest {
	draft = managementNormalizeDraftForState(state, draft)
	interaction, isolationRequired := managementDecomposeInteraction(draft.Interaction)
	return BotSettingsRequest{Interaction: interaction, IsolationRequired: isolationRequired, DefaultAgent: draft.DefaultAgent,
		DefaultRuntimeRef: draft.DefaultRuntimeRef, DefaultModelRef: draft.DefaultModelRef,
		DefaultReasoningEffort: draft.DefaultReasoningEffort, ExpectedVersion: state.Version,
		RequestID: fmt.Sprintf("%s:%d:%s:%s", strings.TrimSpace(messageID), state.Version, draft.Interaction,
			strings.Join([]string{draft.DefaultAgent, draft.DefaultRuntimeRef, draft.DefaultModelRef, draft.DefaultReasoningEffort}, ":")), OpenID: strings.TrimSpace(openID)}
}

func managementDecomposeInteraction(value string) (string, bool) {
	v := strings.TrimSpace(value)
	if v == interactionModeThreadIsolated {
		return config.FeishuInteractionThread, true
	}
	return v, false
}

func managementUsesThreadRouting(value string) bool {
	interaction, _ := managementDecomposeInteraction(value)
	return interaction == config.FeishuInteractionThread
}

func managementConfirmedBotSettingsRequest(action *callback.CallBackAction, openID string) (BotSettingsRequest, string, bool) {
	interaction := managementCallbackString(action, "interaction")
	defaultAgent := managementCallbackString(action, "default_agent")
	defaultRuntimeRef := managementCallbackString(action, "default_runtime_ref")
	defaultModelRef := managementCallbackString(action, "default_model_ref")
	defaultReasoningEffort := managementCallbackString(action, "default_reasoning_effort")
	requestID := managementCallbackString(action, "request_id")
	token := managementCallbackString(action, "confirmation_token")
	version, err := strconv.ParseInt(managementCallbackString(action, "expected_version"), 10, 64)
	if err != nil || !managementValidInteraction(interaction) || defaultAgent == "" || requestID == "" || token == "" {
		return BotSettingsRequest{}, "", false
	}
	routingInteraction, isolationRequired := managementDecomposeInteraction(interaction)
	return BotSettingsRequest{Interaction: routingInteraction, IsolationRequired: isolationRequired, DefaultAgent: defaultAgent,
		DefaultRuntimeRef: defaultRuntimeRef, DefaultModelRef: defaultModelRef,
		DefaultReasoningEffort: defaultReasoningEffort, ExpectedVersion: version, Confirmed: true,
		RequestID: requestID, OpenID: strings.TrimSpace(openID)}, token, true
}

func (b *Bridge) validManagementRecoveryCallback(action *callback.CallBackAction, openID string) (BotSettingsRequest, string, bool) {
	req, token, ok := managementConfirmedBotSettingsRequest(action, openID)
	if !ok || !b.validateBotSettingsConfirmation(token, req) {
		return BotSettingsRequest{}, "", false
	}
	return req, token, true
}

func (b *Bridge) expiredManagementRecoveryCallback(openID string, messageID string, state managementState, surface managementSurface, message any) (*callback.CardActionTriggerResponse, bool, error) {
	card := renderManagementCardForSurface(renderSettingsCardForSurface(state, surface), surface, managementViewSettings)
	b.updateManagementCardFromCallback(openID, messageID, card)
	return cardToast("warning", message), true, nil
}

func managementCallbackString(action *callback.CallBackAction, key string) string {
	if action == nil {
		return ""
	}
	if value, ok := action.Value[key].(string); ok {
		return strings.TrimSpace(value)
	}
	if value, ok := action.FormValue[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func managementSurfaceFromCallback(action *callback.CallBackAction) managementSurface {
	return normalizeManagementSurface(managementCallbackString(action, managementSurfaceKey))
}

func normalizeManagementSurface(value string) managementSurface {
	switch managementSurface(strings.TrimSpace(value)) {
	case managementSurfaceAmux, managementSurface("amux1"):
		return managementSurfaceAmux
	default:
		return managementSurfaceDefault
	}
}

func (b *Bridge) runManagementSettingsTransition(openID, messageID string, previous managementState, draft managementSettingsDraft, req BotSettingsRequest, onboarding bool, surface managementSurface) {
	draft = managementNormalizeDraftForState(previous, draft)
	go func() {
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		processingCard := renderSettingsProcessingCard(previous, draft)
		if req.RecoveryAction == BotSettingsRecoveryCreateBlank {
			processingCard = renderSettingsBlankRecoveryProcessingCard(previous, draft)
		}
		processingCard = renderFocusedManagementCardForSurface(processingCard, surface)
		b.writeManagementCardFromCallback(patchCtx, openID, messageID, processingCard)
		patchCancel()
		transitionCtx, transitionCancel := context.WithTimeout(context.Background(), 45*time.Second)
		result, err := b.ApplyBotSettings(transitionCtx, req)
		transitionCancel()
		if err == nil && result.Status == BotSettingsStatusSuccess &&
			draft.ThinkingProcess != previous.ThinkingProcess {
			err = b.saveManagementThinkingProcess(draft.ThinkingProcess)
		}
		var finalCard string
		showStableShell := true
		if err != nil {
			current := b.managementState()
			if errors.Is(err, errBotSettingsBlankRecoveryFailed) && managementUsesThreadRouting(current.Mode) && current.Version == req.ExpectedVersion {
				retryReq := req
				retryReq.Confirmed = false
				retryReq.RecoveryAction = ""
				retryReq.RecoveryAttemptID = ""
				profileState, stateErr := b.currentBotProfileState()
				if stateErr == nil {
					choice := b.issueBotSettingsRecoveryChoice(profileState, retryReq)
					finalCard = renderSettingsRecoveryChoiceCard(current, managementDraftFromState(current), retryReq, choice, managementSettingsFailureMessage(err))
					showStableShell = false
				}
			}
			if finalCard == "" {
				finalCard = renderSettingsFailureCardForSurface(current, draft, err, surface)
				showStableShell = false
			}
		} else {
			switch result.Status {
			case BotSettingsStatusSuccess:
				current := b.managementState()
				if onboarding {
					finalCard = renderOnboardingCompleteCard(current, managementDraftFromState(current))
				} else {
					finalCard = renderSettingsCardForSurface(current, surface)
				}
			case BotSettingsStatusStateChanged:
				current := b.managementState()
				finalCard = renderSettingsFailureCardForSurface(current, managementDraftFromState(current), errBotProfileStateChanged, surface)
				showStableShell = false
			case BotSettingsStatusNeedsConfirmation:
				current := b.managementState()
				finalCard = renderSettingsConfirmationCard(current, draft, req, result)
				showStableShell = false
			case BotSettingsStatusNeedsRecoveryChoice:
				current := b.managementState()
				finalCard = renderSettingsRecoveryChoiceCard(current, draft, req, result, "")
				showStableShell = false
			default:
				current := b.managementState()
				finalCard = renderSettingsFailureCardForSurface(current, draft, fmt.Errorf("unexpected transition result %q", result.Status), surface)
				showStableShell = false
			}
		}
		if showStableShell {
			finalCard = renderManagementCardForSurface(finalCard, surface, managementViewSettings)
		} else {
			finalCard = renderFocusedManagementCardForSurface(finalCard, surface)
		}
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer finalCancel()
		b.writeManagementCardFromCallback(finalCtx, openID, messageID, finalCard)
	}()
}

func (c *managementSettingsState) beginManagementDocumentSettings() bool {
	if c == nil {
		return false
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	if c.documentSettingsActive {
		return false
	}
	c.documentSettingsActive = true
	return true
}

func (c *managementSettingsState) endManagementDocumentSettings() {
	if c == nil {
		return
	}
	c.settingsMu.Lock()
	c.documentSettingsActive = false
	c.settingsMu.Unlock()
}

func (c *managementSettingsState) beginManagementBotToBotSettings() bool {
	if c == nil {
		return false
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	if c.botToBotSettingsActive {
		return false
	}
	c.botToBotSettingsActive = true
	return true
}

func (c *managementSettingsState) endManagementBotToBotSettings() {
	if c == nil {
		return
	}
	c.settingsMu.Lock()
	c.botToBotSettingsActive = false
	c.settingsMu.Unlock()
}

func (b *Bridge) runManagementBotToBotSettingsUpdate(openID, messageID string, enabled bool, surface managementSurface) {
	go func() {
		defer b.endManagementBotToBotSettings()
		err := b.setBotToBotForProfile(enabled)
		var notice any = managementCopy("management.settings.saved.checking.feishu.permissions.for.enabled.capabilities")
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Bool("enabled", enabled).Msg("bridge: update Bot-to-Bot setting")
			notice = managementCopy("management.could.not.save.agent.collaboration.settings.settings.are.unchanged")
		} else {
			b.triggerFeishuPermissionAudit(context.Background())
		}
		card := renderFocusedManagementCardForSurface(renderBotToBotCardWithNotice(b.managementState(), notice), surface)
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer patchCancel()
		b.writeManagementCardFromCallback(patchCtx, openID, messageID, card)
	}()
}

func (c *managementSettingsState) beginManagementGroupWatchExecution() bool {
	if c == nil {
		return false
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	if c.groupWatchExecutionActive {
		return false
	}
	c.groupWatchExecutionActive = true
	return true
}

func (c *managementSettingsState) endManagementGroupWatchExecution() {
	if c == nil {
		return
	}
	c.settingsMu.Lock()
	c.groupWatchExecutionActive = false
	c.settingsMu.Unlock()
}

func (b *Bridge) runManagementDocumentSettingsUpdate(openID, messageID string, enabled bool, surface managementSurface) {
	go func() {
		defer b.endManagementDocumentSettings()
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		processingCard := renderFocusedManagementCardForSurface(renderDocumentCommentsProcessingCard(enabled), surface)
		b.writeManagementCardFromCallback(patchCtx, openID, messageID, processingCard)
		patchCancel()
		updateCtx, updateCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := b.setDocumentCommentsForProfile(updateCtx, enabled)
		updateCancel()
		var notice any = ""
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Bool("enabled", enabled).Msg("bridge: update document comment setting")
			if enabled {
				notice = managementCopy("management.could.not.enable.document.comments.settings.are.unchanged.check")
			} else {
				notice = managementCopy("management.could.not.disable.document.comments.settings.are.unchanged.try")
			}
		}
		state := b.managementState()
		finalCard := renderDocumentCommentsCardWithNotice(state, notice)
		finalCard = renderFocusedManagementCardForSurface(finalCard, surface)
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer finalCancel()
		b.writeManagementCardFromCallback(finalCtx, openID, messageID, finalCard)
	}()
}

func (b *Bridge) runManagementDocumentExecutionUpdate(openID, messageID string, draft managementDocumentCommentsDraft, surface managementSurface) {
	go func() {
		defer b.endManagementDocumentSettings()
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		processingCard := renderFocusedManagementCardForSurface(renderDocumentCommentsExecutionProcessingCard(), surface)
		b.writeManagementCardFromCallback(patchCtx, openID, messageID, processingCard)
		patchCancel()
		err := b.saveManagementDocumentExecution(draft)
		var notice any = managementCopy("management.task.settings.saved.they.apply.only.to.new.comment")
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: save document comment execution")
			notice = managementCopy("management.could.not.save.task.settings.settings.are.unchanged.refresh")
		}
		state := b.managementState()
		finalCard := renderDocumentCommentsCardWithNotice(state, notice)
		finalCard = renderFocusedManagementCardForSurface(finalCard, surface)
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer finalCancel()
		b.writeManagementCardFromCallback(finalCtx, openID, messageID, finalCard)
	}()
}

func (b *Bridge) saveManagementDocumentExecution(draft managementDocumentCommentsDraft) error {
	save := func() error {
		state := b.managementState()
		agentRef, runtimeRef, modelRef, reasoningEffort := "", "", "", ""
		if draft.Mode == managementDocumentCommentsModeCustom {
			agentRef, runtimeRef = strings.TrimSpace(draft.AgentRef), strings.TrimSpace(draft.RuntimeRef)
			modelRef, reasoningEffort = strings.TrimSpace(draft.ModelRef), strings.TrimSpace(draft.ReasoningEffort)
			if !managementValidDocumentCommentsAgent(state, agentRef) {
				return fmt.Errorf("document comment Agent Profile %q is unavailable", agentRef)
			}
			if !managementValidRuntime(state, runtimeRef) {
				return fmt.Errorf("document comment runtime %q is unavailable", runtimeRef)
			}
			if !managementValidRuntimeModel(state, runtimeRef, modelRef) {
				return fmt.Errorf("document comment model %q is not supported by runtime %s", modelRef, runtimeRef)
			}
			if !managementValidRuntimeReasoningEffort(state, runtimeRef, modelRef, reasoningEffort) {
				return fmt.Errorf("document comment reasoning effort %q is not supported by runtime %s model %s", reasoningEffort, runtimeRef, modelRef)
			}
			if _, err := resolveBridgeAgentLaunchWithSelection(agentRef, b.db.workspaceDefaults.Args, runtimeRef, modelRef, reasoningEffort); err != nil {
				return err
			}
		}
		return config.SetBridgeFeishuDocumentCommentsExecution(b.ProfileName(), agentRef, runtimeRef, modelRef, reasoningEffort)
	}
	if b.managementPackActions != nil {
		return b.managementPackActions.WithRegistryStable(save)
	}
	return save()
}

func (b *Bridge) runManagementGroupWatchExecutionUpdate(openID, messageID string, draft managementGroupWatchDraft, surface managementSurface) {
	go func() {
		defer b.endManagementGroupWatchExecution()
		patchCtx, patchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		processingCard := renderFocusedManagementCardForSurface(renderGroupWatchExecutionProcessingCard(), surface)
		b.writeManagementCardFromCallback(patchCtx, openID, messageID, processingCard)
		patchCancel()
		err := b.saveManagementGroupWatchExecution(draft)
		var notice any = managementCopy("management.task.settings.saved.they.apply.only.to.new.watch")
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: save group watch execution")
			notice = managementCopy("management.could.not.save.task.settings.settings.are.unchanged.refresh.540")
		}
		state := b.managementState()
		nameCtx, nameCancel := context.WithTimeout(context.Background(), 10*time.Second)
		names := b.resolveGroupWatchNames(nameCtx, state.GroupWatch.ChatIDs)
		nameCancel()
		finalCard := renderGroupWatchManagementCardWithNoticeAndNames(state, notice, names)
		if err == nil && draft.ChatID != "" {
			state = b.groupWatchManagementState(state, draft.ChatID)
			fresh := managementGroupWatchDraftFromCallback(&callback.CallBackAction{Value: map[string]any{"chat_id": draft.ChatID, "chat_name": draft.ChatName}}, state)
			finalCard = renderGroupWatchGroupSummary(state, fresh)
		}
		finalCard = renderFocusedManagementCardForSurface(finalCard, surface)
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer finalCancel()
		b.writeManagementCardFromCallback(finalCtx, openID, messageID, finalCard)
	}()
}

func (b *Bridge) saveManagementGroupWatchExecution(draft managementGroupWatchDraft) error {
	save := func() error {
		state := b.managementState()
		agentRef, runtimeRef, modelRef, reasoningEffort := "", "", "", ""
		if draft.Mode == managementDocumentCommentsModeCustom {
			agentRef, runtimeRef = strings.TrimSpace(draft.AgentRef), strings.TrimSpace(draft.RuntimeRef)
			modelRef, reasoningEffort = strings.TrimSpace(draft.ModelRef), strings.TrimSpace(draft.ReasoningEffort)
			if !managementValidDocumentCommentsAgent(state, agentRef) {
				return fmt.Errorf("group watch Agent Profile %q is unavailable", agentRef)
			}
			if !managementValidRuntime(state, runtimeRef) {
				return fmt.Errorf("group watch runtime %q is unavailable", runtimeRef)
			}
			if !managementValidRuntimeModel(state, runtimeRef, modelRef) {
				return fmt.Errorf("group watch model %q is not supported by runtime %s", modelRef, runtimeRef)
			}
			if !managementValidRuntimeReasoningEffort(state, runtimeRef, modelRef, reasoningEffort) {
				return fmt.Errorf("group watch reasoning effort %q is not supported by runtime %s model %s", reasoningEffort, runtimeRef, modelRef)
			}
			if _, err := resolveBridgeAgentLaunchWithSelection(agentRef, b.db.workspaceDefaults.Args, runtimeRef, modelRef, reasoningEffort); err != nil {
				return err
			}
		}
		if draft.ChatID != "" {
			return b.applyGroupWatchChatSettings(draft, agentRef, runtimeRef, modelRef, reasoningEffort)
		}
		return b.applyGroupWatchExecutionSettings(agentRef, runtimeRef, modelRef, reasoningEffort, draft.ThinkingProcess)
	}
	if b.managementPackActions != nil {
		return b.managementPackActions.WithRegistryStable(save)
	}
	return save()
}

func managementDraftFromState(state managementState) managementSettingsDraft {
	interaction := strings.TrimSpace(state.Mode)
	if !managementValidInteraction(interaction) {
		interaction = config.FeishuInteractionThread
	}
	return managementNormalizeDraftForState(state, managementSettingsDraft{
		Interaction: interaction, DefaultAgent: strings.TrimSpace(state.DefaultAgent),
		DefaultRuntimeRef: managementEffectiveRuntimeRef(state), DefaultModelRef: strings.TrimSpace(state.DefaultModelRef),
		DefaultReasoningEffort: strings.TrimSpace(state.DefaultReasoningEffort),
		ThinkingProcess:        strings.TrimSpace(state.ThinkingProcess),
	})
}

func managementValidInteraction(value string) bool {
	switch strings.TrimSpace(value) {
	case config.FeishuInteractionThread, config.FeishuInteractionSingleton, interactionModeThreadIsolated:
		return true
	default:
		return false
	}
}

func managementRecoveryStateMatches(state managementState, req BotSettingsRequest) bool {
	return managementUsesThreadRouting(state.Mode) && state.Version == req.ExpectedVersion &&
		strings.TrimSpace(state.DefaultAgent) == strings.TrimSpace(req.DefaultAgent) &&
		strings.TrimSpace(state.DefaultRuntimeRef) == strings.TrimSpace(req.DefaultRuntimeRef) &&
		strings.TrimSpace(state.DefaultModelRef) == strings.TrimSpace(req.DefaultModelRef) &&
		strings.TrimSpace(state.DefaultReasoningEffort) == strings.TrimSpace(req.DefaultReasoningEffort)
}

func managementNormalizeDraft(draft managementSettingsDraft) managementSettingsDraft {
	if !managementValidInteraction(draft.Interaction) {
		draft.Interaction = config.FeishuInteractionThread
	}
	return draft
}

func managementNormalizeDraftForState(state managementState, draft managementSettingsDraft) managementSettingsDraft {
	draft = managementNormalizeDraft(draft)
	if !managementValidDefaultAgent(state, draft.DefaultAgent) {
		draft.DefaultAgent = strings.TrimSpace(state.DefaultAgent)
	}
	if !managementValidRuntime(state, draft.DefaultRuntimeRef) {
		draft.DefaultRuntimeRef = managementEffectiveRuntimeRef(state)
	}
	if !managementValidRuntimeModel(state, draft.DefaultRuntimeRef, draft.DefaultModelRef) {
		draft.DefaultModelRef = ""
	}
	if !managementValidRuntimeReasoningEffort(state, draft.DefaultRuntimeRef, draft.DefaultModelRef, draft.DefaultReasoningEffort) {
		draft.DefaultReasoningEffort = ""
	}
	if !managementValidThinkingProcess(draft.ThinkingProcess) {
		draft.ThinkingProcess = state.ThinkingProcess
	}
	if !managementValidThinkingProcess(draft.ThinkingProcess) {
		draft.ThinkingProcess = config.DefaultFeishuThinkingProcess
	}
	return draft
}

func managementValidThinkingProcess(value string) bool {
	return config.ValidateFeishuThinkingProcess(strings.TrimSpace(value)) == nil
}

func managementThinkingProcessLabel(value string) any {
	switch strings.TrimSpace(value) {
	case config.FeishuThinkingProcessCollapsed:
		return managementCopy("management.collapsed")
	case config.FeishuThinkingProcessHidden:
		return managementCopy("management.hidden")
	default:
		return managementCopy("management.expanded")
	}
}

func managementEffectiveRuntimeRef(state managementState) string {
	if ref := strings.TrimSpace(state.DefaultRuntimeRef); ref != "" {
		return ref
	}
	profile, ok := managementProfileByRef(state, state.DefaultAgent)
	if ok && managementValidRuntime(state, profile.AgentType) {
		return strings.TrimSpace(profile.AgentType)
	}
	runtimes := managementRuntimeCatalog(state)
	if len(runtimes) > 0 {
		return strings.TrimSpace(runtimes[0].Ref)
	}
	return ""
}

func managementValidRuntime(state managementState, runtimeRef string) bool {
	runtimeRef = strings.TrimSpace(runtimeRef)
	if runtimeRef == "" {
		return false
	}
	_, ok := managementRuntimeByRef(state, runtimeRef)
	return ok
}

func managementRuntimeByRef(state managementState, runtimeRef string) (agentprofile.RuntimeOption, bool) {
	runtimeRef = strings.TrimSpace(runtimeRef)
	for _, runtime := range managementRuntimeCatalog(state) {
		if strings.TrimSpace(runtime.Ref) == runtimeRef {
			return runtime, true
		}
	}
	return agentprofile.RuntimeOption{}, false
}

func managementRuntimeCatalog(state managementState) []agentprofile.RuntimeOption {
	if len(state.Runtimes) > 0 {
		return state.Runtimes
	}
	runtimes, _ := agentprofile.RuntimeCatalog()
	return runtimes
}

func managementValidRuntimeModel(state managementState, runtimeRef, modelRef string) bool {
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return managementValidRuntime(state, runtimeRef)
	}
	runtime, ok := managementRuntimeByRef(state, runtimeRef)
	if !ok {
		return false
	}
	_, err := agentprofile.ResolveRuntimeModel(runtime, modelRef)
	return err == nil
}

func managementValidRuntimeReasoningEffort(state managementState, runtimeRef, modelRef, effort string) bool {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return managementValidRuntimeModel(state, runtimeRef, modelRef)
	}
	runtime, ok := managementRuntimeByRef(state, runtimeRef)
	if !ok {
		return false
	}
	model, err := agentprofile.ResolveRuntimeModel(runtime, modelRef)
	return err == nil && agentprofile.ValidateRuntimeReasoningEffort(runtime, model, effort) == nil
}

func managementExecutionDiffers(state managementState, draft managementSettingsDraft) bool {
	return strings.TrimSpace(draft.DefaultAgent) != strings.TrimSpace(state.DefaultAgent) ||
		strings.TrimSpace(draft.DefaultRuntimeRef) != strings.TrimSpace(state.DefaultRuntimeRef) ||
		strings.TrimSpace(draft.DefaultModelRef) != strings.TrimSpace(state.DefaultModelRef) ||
		strings.TrimSpace(draft.DefaultReasoningEffort) != strings.TrimSpace(state.DefaultReasoningEffort)
}

func managementValidDefaultModel(state managementState, agentRef, modelRef string) bool {
	agentRef = strings.TrimSpace(agentRef)
	modelRef = strings.TrimSpace(modelRef)
	if modelRef == "" {
		return true
	}
	for _, profile := range state.Profiles {
		if strings.TrimSpace(profile.Ref) != agentRef {
			continue
		}
		if profile.SupportedModels == nil {
			return true
		}
		for _, supported := range profile.SupportedModels {
			if strings.TrimSpace(supported) == modelRef {
				return true
			}
		}
		return false
	}
	return false
}

func managementValidDefaultAgent(state managementState, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, profile := range state.Profiles {
		if profile.Ref == value {
			return !profile.CurrentOnly || value == strings.TrimSpace(state.DefaultAgent)
		}
	}
	return false
}

func (b *Bridge) sendOwnerManagementCard(ctx context.Context, openID, cardJSON string) error {
	if !b.ownerConfirmed() || !b.canUseBridge(openID) {
		_, _, err := b.notifyDM(ctx, openID, ownerConfirmationRequiredText())
		return err
	}
	return b.sendManagementCardToDM(ctx, openID, cardJSON)
}

func (b *Bridge) sendManagementCardToDM(ctx context.Context, openID, cardJSON string) error {
	cardJSON = b.managementCardForRecipient(openID, cardJSON)
	var messageID string
	var err error
	if b.dmInteractive != nil {
		messageID, _, err = b.dmInteractive(ctx, openID, cardJSON)
	} else {
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		messageID, _, err = b.sendInteractiveToDM(sendCtx, openID, cardJSON, "amux management card")
	}
	if err == nil {
		b.rememberPrivateManagementCard(openID, messageID)
	}
	return err
}

func (b *Bridge) notifyThreadHelpCard(ctx context.Context, rootMsgID, cardJSON string) error {
	if b.threadNotice != nil {
		return b.threadNotice(ctx, rootMsgID, cardJSON)
	}
	if target, ok, err := b.directReplyTargetForRoot(ctx, rootMsgID); ok || err != nil {
		if err != nil {
			return err
		}
		_, _, err := b.sendInteractiveToDirectTarget(ctx, target, cardJSON, "amux help card")
		return err
	}
	_, _, err := b.sendInteractiveToThread(ctx, rootMsgID, cardJSON, "amux help card")
	return err
}

func (b *Bridge) notifyRootHelpCard(ctx context.Context, chatID, rootMsgID, cardJSON string) error {
	if strings.TrimSpace(rootMsgID) != "" {
		return b.notifyThreadHelpCard(ctx, rootMsgID, cardJSON)
	}
	if b.chatNotice != nil {
		_, _, err := b.chatNotice(ctx, chatID, cardJSON)
		return err
	}
	_, _, err := b.sendInteractiveToChat(ctx, chatID, cardJSON, "amux help card")
	return err
}

func (c *managementSettingsState) onboardingCardAlreadySent(openID string) bool {
	openID = strings.TrimSpace(openID)
	if openID == "" {
		return true
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	if c.onboardingCardSentByOpen == nil {
		c.onboardingCardSentByOpen = make(map[string]bool)
	}
	if c.onboardingCardSentByOpen[openID] {
		return true
	}
	c.onboardingCardSentByOpen[openID] = true
	return false
}

func managementProfiles() []managementProfileSummary {
	var out []managementProfileSummary
	appendProfile := func(profile agentprofile.Profile, libraryName any) {
		out = append(out, managementProfileSummaryFromProfile(profile, libraryName))
	}
	for _, profile := range agentprofile.Builtins() {
		appendProfile(profile, managementCopy("management.built.in"))
	}
	if registry, err := agentprofile.LoadPackRegistry(agentprofile.DefaultPackRegistryPath()); err == nil {
		results := agentprofile.ListPackProfilesByPack(registry)
		displaySources := make([]agentprofile.PackDisplaySource, 0, len(results))
		for _, result := range results {
			if result.Err != nil || strings.HasSuffix(result.Namespace, "-dev") {
				continue
			}
			pack := registry.Packs[result.Namespace]
			displaySources = append(displaySources, agentprofile.PackDisplaySource{Key: result.Namespace, Namespace: result.Namespace, GitURL: pack.GitURL, DisplayName: pack.DisplayName})
		}
		displayNames := agentprofile.DisambiguatePackDisplayNames(displaySources)
		for _, result := range results {
			if result.Err != nil {
				log.Warn().Err(result.Err).Str("pack_namespace", result.Namespace).Msg("skipping unavailable Agent Pack in Feishu profile catalog")
				continue
			}
			libraryName := displayNames[result.Namespace]
			if strings.HasSuffix(result.Namespace, "-dev") {
				libraryName = agentprofile.PackRepositoryName(registry.Packs[result.Namespace].GitURL)
			}
			for _, profile := range result.Profiles {
				var profileLibraryName any = libraryName
				if profile.Source == agentprofile.SourceLocal {
					profileLibraryName = managementCopy("management.local")
				}
				appendProfile(profile, profileLibraryName)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Ref < out[j].Ref
	})
	return out
}

func managementProfileSummaryFromProfile(profile agentprofile.Profile, libraryName any) managementProfileSummary {
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = profile.Ref()
	}
	agentType, model := profile.Type, profileDisplayModel(profile)
	preset, callerDefaults := agentprofile.BuiltinLaunchDefaults(profile)
	if callerDefaults {
		agentType, model = preset.Runtime, preset.Model
	}
	supportedModels := profile.SupportedModels
	if supportedModels != nil {
		supportedModels = append([]string{}, supportedModels...)
	}
	return managementProfileSummary{Ref: profile.Ref(), Name: name, LibraryName: strings.TrimSpace(managementDefaultText(libraryName)), LibraryLabel: libraryName, Description: strings.TrimSpace(profile.Description), AgentType: strings.TrimSpace(agentType), Model: model, SupportedModels: supportedModels, CallerDefaults: callerDefaults, Development: strings.HasSuffix(profile.Namespace, "-dev")}
}

func managementSelectableProfiles(catalog []managementProfileSummary, currentRefs ...string) []managementProfileSummary {
	out := make([]managementProfileSummary, 0, len(catalog)+len(currentRefs))
	selected := make(map[string]struct{}, len(catalog))
	for _, profile := range catalog {
		ref := strings.TrimSpace(profile.Ref)
		if ref == "" || profile.Development {
			continue
		}
		if _, exists := selected[ref]; exists {
			continue
		}
		profile.Ref = ref
		selected[ref] = struct{}{}
		out = append(out, profile)
	}
	currentOnly := make([]managementProfileSummary, 0, len(currentRefs))
	for _, currentRef := range currentRefs {
		currentRef = strings.TrimSpace(currentRef)
		if currentRef == "" {
			continue
		}
		if _, selectable := selected[currentRef]; selectable {
			continue
		}
		current := managementProfileSummary{Ref: currentRef, Name: currentRef}
		for _, profile := range catalog {
			if strings.TrimSpace(profile.Ref) == currentRef || strings.EqualFold(strings.TrimSpace(profile.Name), currentRef) {
				current = profile
				break
			}
		}
		current.Ref = currentRef
		current.CurrentOnly = true
		currentOnly = append(currentOnly, current)
		selected[currentRef] = struct{}{}
	}
	return append(currentOnly, out...)
}

func profileDisplayModel(profile agentprofile.Profile) string {
	for _, value := range []string{profile.Model, profile.ConfiguredModel} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if profile.Provider != nil && strings.TrimSpace(profile.Provider.UpstreamModel) != "" {
		return strings.TrimSpace(profile.Provider.UpstreamModel)
	}
	return ""
}

func renderOnboardingCard(state managementState) string {
	return renderOnboardingDraftCard(state, managementDraftFromState(state))
}

func renderOnboardingDraftCard(state managementState, draft managementSettingsDraft) string {
	draft = managementNormalizeDraftForState(state, draft)
	elements := []cardElement{managementCompactSummaryElement(
		managementSummaryItem{Title: managementCopy("management.current.selection"), Body: managementModeSummary(draft)},
		managementSummaryItem{Title: managementCopy("management.default.expert"), Body: managementProfileNameByRef(state, draft.DefaultAgent)},
		managementSummaryItem{Title: managementCopy("management.runtime"), Body: managementRuntimeLabel(state, draft.DefaultRuntimeRef)},
		managementSummaryItem{Title: managementCopy("management.default.model"), Body: managementRuntimeModelLabel(draft.DefaultModelRef)},
		managementSummaryItem{Title: managementCopy("management.reasoning.effort"), Body: managementReasoningEffortLabel(draft.DefaultReasoningEffort)},
		managementSummaryItem{Title: managementCopy("management.activity.display"), Body: managementThinkingProcessLabel(draft.ThinkingProcess)}), managementDividerElement(), managementMessageHandlingHintElement(), managementProfileLibraryHintElement(state)}
	elements = append(elements, managementSettingsEditorElements(state, "onboarding_form", draft, managementActionOnboardingSubmit, managementCopy("management.complete.setup"), "", nil, managementTextElement("", "notation", "grey", nil))...)
	elements = append(elements, managementHintElement(managementGroupCollaborationHintText()))
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.configure.assistant"), Subtitle: managementCopy("management.set.message.handling.expert.and.model.for.new.tasks"), Summary: managementCopy("management.configure.amux.assistant"), Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: elements})
}

func renderOnboardingCompleteCard(state managementState, draft managementSettingsDraft) string {
	draft = managementNormalizeDraftForState(state, draft)
	elements := []cardElement{managementCompactSummaryElement(
		managementSummaryItem{Title: managementCopy("management.current.configuration"), Body: managementModeSummary(draft)},
		managementSummaryItem{Title: managementCopy("management.default.expert"), Body: managementProfileNameByRef(state, draft.DefaultAgent)},
		managementSummaryItem{Title: managementCopy("management.runtime"), Body: managementRuntimeLabel(state, draft.DefaultRuntimeRef)},
		managementSummaryItem{Title: managementCopy("management.default.model"), Body: managementRuntimeModelLabel(draft.DefaultModelRef)},
		managementSummaryItem{Title: managementCopy("management.reasoning.effort"), Body: managementReasoningEffortLabel(draft.DefaultReasoningEffort)},
		managementSummaryItem{Title: managementCopy("management.activity.display"), Body: managementThinkingProcessLabel(draft.ThinkingProcess)})}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.assistant.ready"), Subtitle: managementCopy("management.you.can.now.send.tasks.directly"), Summary: managementCopy("management.amux.assistant.ready"), Theme: "green", Icon: managementIcon("check_outlined", "green"), Elements: elements, Buttons: []managementButton{{Text: managementCopy("management.assistant.settings"), Type: "primary", Action: managementActionSettingsOpen, Icon: managementIconPtr("setting_outlined", "blue")}, {Text: managementCopy("management.back.to.overview"), Type: "default", Action: managementActionStatusOpen, Icon: managementIconPtr("info_outlined", "grey")}}})
}

func renderSettingsCardForSurface(state managementState, surface managementSurface) string {
	draft := managementDraftFromState(state)
	settingsAction := managementButton{Text: managementCopy("management.edit.defaults"), Type: "primary", Action: managementActionSettingsEdit, Icon: managementIconPtr("edit_outlined", "blue")}
	capabilitiesAction := managementButton{Text: managementCopy("management.manage.integrations"), Type: "default", Action: managementActionCapabilitiesOpen, Icon: managementIconPtr("setting_outlined", "blue")}
	capabilities := managementCapabilityViews(state)
	elements := []cardElement{managementSectionTitleElement(managementCopy("management.task.defaults")), managementTaskDefaultsElement(state, draft, "amux_settings_task_defaults"), managementButtonRowElement([]managementButton{settingsAction}), managementDividerElement(), managementSectionTitleElement(managementCopy("management.integrations")), managementCapabilitiesSummaryElement(capabilities), managementButtonRowElement([]managementButton{capabilitiesAction})}
	elements = append(elements, managementHintElement(managementCopy("management.these.settings.affect.only.this.assistant")))
	var buttons []managementButton
	if surface != managementSurfaceAmux {
		if state.CanManagePacks {
			buttons = append(buttons, managementButton{Text: managementCopy("management.expert.library"), Type: "default", Action: managementActionPackLibraryOpen})
		}
		buttons = append(buttons, managementButton{Text: managementCopy("management.back.to.overview"), Type: "default", Action: managementActionStatusOpen})
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.assistant.settings"), Summary: managementCopy("management.amux.assistant.settings"), Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: elements, Buttons: buttons})
}

func managementCapabilityViews(state managementState) []managementCapabilityView {
	groupWatchStatus := managementCopy("management.not.configured")
	groupWatchEnabled := state.GroupWatch.Enabled && len(state.GroupWatch.ChatIDs) > 0
	if len(state.GroupWatch.ChatIDs) > 0 {
		if groupWatchEnabled {
			groupWatchStatus = managementCopy("management.enabled.d.groups", len(state.GroupWatch.ChatIDs))
		} else {
			groupWatchStatus = managementCopy("management.paused.d.groups", len(state.GroupWatch.ChatIDs))
		}
	}
	documentCommentsStatus := managementCopy("management.disabled")
	if state.DocumentCommentsEnabled {
		documentCommentsStatus = managementCopy("management.enabled")
	}
	botToBotStatus := managementCopy("management.disabled")
	botToBotEnabled := state.BotToBot.Enabled && state.Mode == config.FeishuInteractionThread
	if state.BotToBot.Enabled {
		if botToBotEnabled {
			botToBotStatus = managementCopy("management.enabled")
		} else {
			botToBotStatus = managementCopy("management.takes.effect.with.separate.conversations.per.topic")
		}
	}
	return []managementCapabilityView{{ID: "group_watch", Title: managementCopy("management.group.watch.312"), Status: groupWatchStatus, Summary: managementCopy("management.automatically.handle.new.topics.in.selected.groups"), Enabled: groupWatchEnabled, OpenAction: managementActionGroupWatchOpen}, {ID: "document_comments", Title: managementCopy("management.document.comments"), Status: documentCommentsStatus, Summary: managementCopy("management.mention.the.assistant.in.a.comment.to.start.a"), Enabled: state.DocumentCommentsEnabled, OpenAction: managementActionDocumentCommentsOpen}, {ID: "bot_to_bot", Title: managementCopy("management.agent.to.agent"), Status: botToBotStatus, Summary: managementCopy("management.receive.tasks.from.other.agents.and.call.agents.in"), Enabled: botToBotEnabled, OpenAction: managementActionBotToBotOpen}}
}

func managementCapabilitiesSummaryElement(capabilities []managementCapabilityView) cardElement {
	enabled := 0
	previews := make([]any, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability.Enabled {
			enabled++
		}
		previews = append(previews, managementConcat(capability.Title, " · ", capability.Status))
	}
	rows := []cardElement{managementKeyValueRowElement(managementCopy("management.integrations"), managementCopy("management.d.capabilities.d.enabled", len(capabilities), enabled))}
	if len(previews) > 0 {
		rows = append(rows, managementTextElement(managementJoin("\n", previews...), "notation", "grey", nil))
	}
	element := managementVerticalSummaryElement("amux_settings_capabilities_summary", rows...)
	element.Columns[0].VerticalSpacing = "4px"
	return element
}

func renderCapabilitiesCard(state managementState) string {
	return renderCapabilitiesCardWithNotice(state, "")
}

func renderCapabilitiesCardWithNotice(state managementState, notice any) string {
	capabilities := managementCapabilityViews(state)
	enabled := 0
	for _, capability := range capabilities {
		if capability.Enabled {
			enabled++
		}
	}
	elements := []cardElement{managementSubpageBackElement(managementCopy("management.back.to.assistant.settings"), managementActionSettingsOpen, "amux.capabilities.back.settings"), managementDividerElement(), managementTextElement(managementCopy("management.d.capabilities.total.d.enabled", len(capabilities), enabled), "notation", "grey", nil)}
	if strings.TrimSpace(managementDefaultText(notice)) != "" {
		elements = append(elements, managementTextElement(managementTruncateText(notice, 240), "normal", "orange", managementIconPtr("warning_outlined", "orange")))
	}
	if state.PermissionAudit != nil {
		elements = append(elements, managementTextElement(managementCapabilityPermissionNotice(*state.PermissionAudit), "normal", "orange", managementIconPtr("warning_outlined", "orange")))
		var permissionButtons []managementButton
		if len(state.PermissionAudit.Scopes) > 0 {
			permissionButtons = append(permissionButtons, managementButton{Text: managementCopy("management.request.permissions"), Type: "primary", Action: managementActionPermissionApply, Name: "amux.capabilities.permissions.apply", Confirm: managementConfirm(managementCopy("management.request.these.permissions.from.the.organization.administrator"), managementCopy("management.the.app.will.request.permissions.from.the.tenant.administrator"))})
		}
		if state.PermissionAudit.GrantURL != "" {
			permissionButtons = append(permissionButtons, managementButton{Text: managementCopy("management.developer.console"), Type: "default", URL: state.PermissionAudit.GrantURL})
		}
		permissionButtons = append(permissionButtons, managementButton{Text: managementCopy("management.check.feishu.permissions"), Type: "default", Action: managementActionPermissionRecheck, Name: "amux.capabilities.permissions.recheck"})
		elements = append(elements, managementButtonRowElement(permissionButtons))
	} else if state.PermissionAuditFailed {
		elements = append(elements, managementTextElement(managementCopy("management.feishu.permissions.check.unavailable"), "normal", "orange", managementIconPtr("warning_outlined", "orange")), managementButtonRowElement([]managementButton{{Text: managementCopy("management.check.feishu.permissions"), Type: "default", Action: managementActionPermissionRecheck, Name: "amux.capabilities.permissions.recheck"}}))
	} else if state.PermissionAuditChecked {
		elements = append(elements, managementTextElement(managementCopy("management.feishu.permissions.none.missing"), "normal", "green", managementIconPtr("check_outlined", "green")), managementButtonRowElement([]managementButton{{Text: managementCopy("management.check.feishu.permissions"), Type: "default", Action: managementActionPermissionRecheck, Name: "amux.capabilities.permissions.recheck"}}))
	} else {
		elements = append(elements, managementTextElement(managementCopy("management.feishu.permissions.not.checked.yet"), "normal", "default", managementIconPtr("info_outlined", "grey")), managementButtonRowElement([]managementButton{{Text: managementCopy("management.check.feishu.permissions"), Type: "default", Action: managementActionPermissionRecheck, Name: "amux.capabilities.permissions.recheck"}}))
	}
	for index, capability := range capabilities {
		if index > 0 {
			elements = append(elements, managementDividerElement())
		}
		elements = append(elements, managementCapabilityDirectoryElements(capability)...)
	}
	elements = append(elements, managementHintElement(managementCopy("management.these.settings.affect.only.this.assistant")))
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.integrations"), Summary: managementCopy("management.amux.integrations"), Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: elements})
}

func (c *managementSettingsState) beginFeishuPermissionRecheck(messageID string) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	if c.permissionRecheckActive {
		return c.permissionRecheckVersion, false
	}
	c.permissionRecheckVersion++
	c.permissionRecheckActive = true
	c.permissionRecheckMessage = strings.TrimSpace(messageID)
	return c.permissionRecheckVersion, true
}

func (c *managementSettingsState) invalidateFeishuPermissionRecheck(messageID string) {
	if c == nil {
		return
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	if !c.permissionRecheckActive {
		return
	}
	messageID = strings.TrimSpace(messageID)
	if messageID != "" && c.permissionRecheckMessage != "" && messageID != c.permissionRecheckMessage {
		return
	}
	c.permissionRecheckVersion++
	c.permissionRecheckActive = false
	c.permissionRecheckMessage = ""
}

func (c *managementSettingsState) feishuPermissionRecheckCurrent(messageID string, version uint64) bool {
	if c == nil {
		return false
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	return c.permissionRecheckActive && c.permissionRecheckVersion == version && c.permissionRecheckMessage == strings.TrimSpace(messageID)
}

func (c *managementSettingsState) finishFeishuPermissionRecheck(messageID string, version uint64) {
	if c == nil {
		return
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	if c.permissionRecheckActive && c.permissionRecheckVersion == version && c.permissionRecheckMessage == strings.TrimSpace(messageID) {
		c.permissionRecheckActive = false
		c.permissionRecheckMessage = ""
	}
}

func (b *Bridge) runFeishuPermissionRecheck(openID, messageID string, surface managementSurface, version uint64) {
	if b == nil {
		return
	}
	go func() {
		defer b.finishFeishuPermissionRecheck(messageID, version)
		auditCtx, auditCancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := b.auditFeishuPermissions(auditCtx, false)
		auditCancel()
		if err == nil && b.capabilityPermissionIssue(capabilityDocumentComments) == nil {
			recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = b.recoverDocumentCommentSource(recoverCtx, false)
			recoverCancel()
		}
		var notice any = managementCopy("management.check.complete.all.permissions.required.by.enabled.capabilities.are")
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: manual Feishu permission self-check failed")
			notice = managementCopy("management.permission.check.failed.temporarily.try.again.later.existing.capability")
		} else if b.capabilityPermissionIssue(capabilityFeishuIntegration) != nil {
			notice = managementCopy("management.check.complete.some.permissions.still.need.to.be.granted")
		}
		card := renderFocusedManagementCardForSurface(renderCapabilitiesCardWithNotice(b.managementState(), notice), surface)
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sendCancel()
		b.writeManagementCardFromCallbackIf(sendCtx, openID, messageID, card, func() bool {
			return b.feishuPermissionRecheckCurrent(messageID, version)
		})
	}()
}

func (c *managementSettingsState) beginFeishuPermissionApply(messageID string) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	if c.permissionApplyActive {
		return c.permissionApplyVersion, false
	}
	c.permissionApplyVersion++
	c.permissionApplyActive = true
	c.permissionApplyMessage = strings.TrimSpace(messageID)
	return c.permissionApplyVersion, true
}

func (c *managementSettingsState) feishuPermissionApplyCurrent(messageID string, version uint64) bool {
	if c == nil {
		return false
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	return c.permissionApplyActive && c.permissionApplyVersion == version && c.permissionApplyMessage == strings.TrimSpace(messageID)
}

func (c *managementSettingsState) finishFeishuPermissionApply(messageID string, version uint64) {
	if c == nil {
		return
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	if c.permissionApplyActive && c.permissionApplyVersion == version && c.permissionApplyMessage == strings.TrimSpace(messageID) {
		c.permissionApplyActive = false
		c.permissionApplyMessage = ""
	}
}

func (b *Bridge) runFeishuPermissionApply(openID, messageID string, surface managementSurface, version uint64) {
	if b == nil {
		return
	}
	go func() {
		defer b.finishFeishuPermissionApply(messageID, version)
		applyCtx, applyCancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := b.applyFeishuCapabilityScopes(applyCtx)
		applyCancel()
		var notice any = managementCopy("management.permission.request.sent.to.the.administrator.exempt.permissions.take")
		var card string
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: apply Feishu scopes failed")
			notice = managementScopeApplyErrorNotice(err)
			if isFeishuScopeApplyCode(err, 212002) {
				auditCtx, auditCancel := context.WithTimeout(context.Background(), 15*time.Second)
				auditErr := b.auditFeishuPermissions(auditCtx, false)
				auditCancel()
				if auditErr == nil && b.capabilityPermissionIssue(capabilityFeishuIntegration) == nil {
					notice = managementCopy("management.recheck.complete.all.permissions.required.by.enabled.capabilities.are")
					card = renderFocusedManagementCardForSurface(renderCapabilitiesCardWithNotice(b.managementState(), notice), surface)
				}
			}
			if issue := b.capabilityPermissionIssue(capabilityFeishuIntegration); issue != nil {
				regCtx, regCancel := context.WithTimeout(context.Background(), 15*time.Second)
				confirmURL, regErr := b.feishuRegistrationGrantURL(regCtx, issue.Scopes)
				regCancel()
				if regErr != nil {
					log.Warn().Err(regErr).Str("profile", b.ProfileName()).Msg("bridge: build scope grant confirm URL failed")
				}
				card = renderFocusedManagementCardForSurface(renderPermissionGrantGuideCard(*issue, notice, confirmURL), surface)
			}
		}
		if card == "" {
			auditCtx, auditCancel := context.WithTimeout(context.Background(), 15*time.Second)
			auditErr := b.auditFeishuPermissions(auditCtx, false)
			auditCancel()
			if auditErr == nil && b.capabilityPermissionIssue(capabilityDocumentComments) == nil {
				recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = b.recoverDocumentCommentSource(recoverCtx, false)
				recoverCancel()
			}
			card = renderFocusedManagementCardForSurface(renderCapabilitiesCardWithNotice(b.managementState(), notice), surface)
		}
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sendCancel()
		b.writeManagementCardFromCallbackIf(sendCtx, openID, messageID, card, func() bool {
			return b.feishuPermissionApplyCurrent(messageID, version)
		})
	}()
}

func managementCapabilityDirectoryElements(capability managementCapabilityView) []cardElement {
	summary := managementVerticalSummaryElement("amux_capability_"+capability.ID, managementKeyValueRowElement(capability.Title, capability.Status), managementTextElement(capability.Summary, "notation", "grey", nil))
	summary.Columns[0].VerticalSpacing = "4px"
	open := managementButton{Text: managementCopy("management.open.settings"), Type: "default", Action: capability.OpenAction, Name: "amux.capability." + capability.ID + ".open"}
	return []cardElement{summary, managementButtonRowElement([]managementButton{open})}
}

func renderBotToBotCard(state managementState) string {
	return renderBotToBotCardWithNotice(state, "")
}

func renderBotToBotCardWithNotice(state managementState, notice any) string {
	status := managementCopy("management.disabled")
	actionText := managementCopy("management.enable.agent.collaboration")
	actionType := "primary"
	target := "true"
	if state.BotToBot.Enabled {
		actionText = managementCopy("management.disable.agent.collaboration")
		actionType = "default"
		target = "false"
		if state.Mode == config.FeishuInteractionThread {
			status = managementCopy("management.enabled")
		} else {
			status = managementCopy("management.takes.effect.with.separate.conversations.per.topic")
		}
	}
	maxTurns := state.BotToBot.MaxTurnsPerThread
	if maxTurns < 1 {
		maxTurns = 10
	}
	elements := []cardElement{managementSubpageBackElement(managementCopy("management.back.to.integrations"), managementActionCapabilitiesOpen, "amux.bot_to_bot.back.capabilities"), managementDividerElement(), managementKeyValueListElement("amux_bot_to_bot_summary", managementSummaryItem{Title: managementCopy("management.current.status"), Body: status}, managementSummaryItem{Title: managementCopy("management.execution.mode"), Body: managementCopy("management.one.task.at.a.time")}, managementSummaryItem{Title: managementCopy("management.maximum.per.topic"), Body: managementCopy("management.d.turns", maxTurns)}), managementSectionTitleElement(managementCopy("management.how.to.use")), managementVerticalSummaryElement("amux_bot_to_bot_usage", managementTextElement(managementCopy("management.when.enabled.in.groups.that.use.separate.topics.and"), "normal", "default", nil))}
	if strings.TrimSpace(managementDefaultText(notice)) != "" {
		elements = append(elements, managementHintElement(managementTruncateText(notice, 240)))
	}
	elements = append(elements, managementButtonRowElement([]managementButton{{Text: actionText, Type: actionType, Action: managementActionBotToBotSet, Name: "amux.bot_to_bot.enabled.set", Values: map[string]string{"enabled": target}}}), managementHintElement(managementCopy("management.these.settings.affect.only.this.assistant")))
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.agent.to.agent"), Summary: managementCopy("management.amux.agent.collaboration.settings"), Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: elements})
}

func renderDocumentCommentsCardWithNotice(state managementState, notice any) string {
	status := managementCopy("management.disabled")
	actionText := managementCopy("management.enable.document.comments")
	actionType := "primary"
	target := "true"
	if state.DocumentCommentsEnabled {
		status = managementCopy("management.enabled")
		actionText = managementCopy("management.disable.document.comments")
		actionType = "default"
		target = "false"
	}
	executionMode := managementCopy("management.inherit.assistant.defaults")
	agentName := managementDefaultAgentName(state)
	executionDraft := managementDocumentCommentsDraftFromState(state)
	runtimeName := managementRuntimeLabel(state, executionDraft.RuntimeRef)
	modelName := managementRuntimeModelLabel(executionDraft.ModelRef)
	reasoningName := managementReasoningEffortLabel(executionDraft.ReasoningEffort)
	if state.DocumentCommentsAgentRef != "" {
		executionMode = managementCopy("management.custom")
		agentName = managementProfileNameByRef(state, state.DocumentCommentsAgentRef)
	}
	elements := []cardElement{managementSubpageBackElement(managementCopy("management.back.to.integrations"), managementActionCapabilitiesOpen, "amux.document_comments.back.capabilities"), managementDividerElement(), managementKeyValueListElement("amux_document_comments_summary", managementSummaryItem{Title: managementCopy("management.current.status"), Body: status}, managementSummaryItem{Title: managementCopy("management.task.configuration"), Body: executionMode}, managementSummaryItem{Title: managementCopy("management.expert"), Body: agentName}, managementSummaryItem{Title: managementCopy("management.runtime"), Body: runtimeName}, managementSummaryItem{Title: managementCopy("management.model"), Body: modelName}, managementSummaryItem{Title: managementCopy("management.reasoning.effort"), Body: reasoningName}), managementSectionTitleElement(managementCopy("management.how.to.use")), managementVerticalSummaryElement("amux_document_comments_usage", managementTextElement(managementCopy("management.the.owner.can.mention.this.assistant.in.a.document"), "normal", "default", nil))}
	if strings.TrimSpace(managementDefaultText(notice)) != "" {
		elements = append(elements, managementHintElement(managementTruncateText(notice, 240)))
	}
	if state.DocumentPermission != nil {
		elements = append(elements, managementTextElement(managementCapabilityPermissionNotice(*state.DocumentPermission), "normal", "orange", managementIconPtr("warning_outlined", "orange")))
		var permissionButtons []managementButton
		if len(state.DocumentPermission.Scopes) > 0 {
			permissionButtons = append(permissionButtons, managementButton{Text: managementCopy("management.request.permissions"), Type: "primary", Action: managementActionPermissionApply, Name: "amux.capabilities.permissions.apply", Confirm: managementConfirm(managementCopy("management.request.these.permissions.from.the.organization.administrator"), managementCopy("management.the.app.will.request.permissions.from.the.tenant.administrator"))})
		}
		if state.DocumentPermission.GrantURL != "" {
			permissionButtons = append(permissionButtons, managementButton{Text: managementCopy("management.developer.console"), Type: "default", URL: state.DocumentPermission.GrantURL})
		}
		elements = append(elements, managementButtonRowElement(permissionButtons))
	}
	executionActionType := "primary"
	if !state.DocumentCommentsEnabled {
		executionActionType = "default"
	}
	elements = append(elements, managementButtonRowElement([]managementButton{{Text: managementCopy("management.edit.task.settings"), Type: executionActionType, Action: managementActionDocumentCommentsEdit}, {Text: actionText, Type: actionType, Action: managementActionDocumentCommentsSet, Values: map[string]string{"enabled": target}}}), managementHintElement(managementCopy("management.these.settings.affect.only.this.assistant")))
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.document.comments"), Summary: managementCopy("management.amux.document.comment.settings"), Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: elements})
}

func renderDocumentCommentsEditCard(state managementState, draft managementDocumentCommentsDraft) string {
	draft = managementNormalizeDocumentCommentsDraft(state, draft)
	changeBehavior := func(field string) []cardBehavior {
		return []cardBehavior{{Type: "callback", Value: managementButtonValue(managementButton{Action: managementActionDocumentCommentsEdit, Values: map[string]string{"document_comments_mode": draft.Mode, "document_comments_agent": draft.AgentRef, "document_comments_runtime_ref": draft.RuntimeRef, "document_comments_model_ref": draft.ModelRef, "document_comments_reasoning_effort": draft.ReasoningEffort, managementDraftFieldKey: field}})}}
	}
	modeField := managementSelectStaticElement("document_comments_mode", managementCopy("management.choose.task.configuration"), draft.Mode, []cardOption{{Text: managementStyledText(managementCopy("management.inherit.assistant.defaults"), cardTitle{Tag: "plain_text"}), Value: managementDocumentCommentsModeInherit}, {Text: managementStyledText(managementCopy("management.custom"), cardTitle{Tag: "plain_text"}), Value: managementDocumentCommentsModeCustom}})
	modeField.Required = false
	modeField.Behaviors = changeBehavior("document_comments_mode")
	elements := []cardElement{managementHintElement(managementCopy("management.changes.apply.only.to.new.comment.topics.existing.topics")), managementSettingsFieldRow(managementCopy("management.task.configuration"), modeField)}
	if draft.Mode == managementDocumentCommentsModeCustom {
		profileOptions := managementProfileOptions(state, false)
		profileField := managementHintElement(managementCopy("management.no.experts.are.available.add.experts.to.the.library"))
		if len(profileOptions) > 0 {
			profileField = managementSelectStaticElement("document_comments_agent", managementCopy("management.choose.the.expert.for.document.comments"), draft.AgentRef, profileOptions)
			profileField.Required = false
			profileField.Behaviors = changeBehavior("document_comments_agent")
		}
		elements = append(elements, managementSettingsFieldRow(managementCopy("management.expert"), profileField))
		runtimeField := managementSelectStaticElement("document_comments_runtime_ref", managementCopy("management.choose.a.runtime"), draft.RuntimeRef, managementRuntimeOptions(state))
		runtimeField.Required = false
		runtimeField.Behaviors = changeBehavior("document_comments_runtime_ref")
		elements = append(elements, managementSettingsFieldRow(managementCopy("management.runtime.new.comment.topics.only"), runtimeField))
		if draft.RuntimeRef != "" {
			initialModelRef := draft.ModelRef
			if initialModelRef == "" {
				initialModelRef = managementProfileDefaultModelOption
			}
			modelField := managementSelectStaticElement("document_comments_model_ref", managementCopy("management.choose.a.model.supported.by.this.runtime"), initialModelRef, managementRuntimeModelOptions(managementSelectedModelState(state, draft.ModelRef, draft.ReasoningEffort), draft.RuntimeRef))
			modelField.Required = false
			modelField.Behaviors = changeBehavior("document_comments_model_ref")
			elements = append(elements, managementSettingsFieldRow(managementCopy("management.model.new.comment.topics.only"), modelField))
			elements = append(elements, modelServiceElements(state, draft.RuntimeRef, map[string]string{"document_comments_mode": draft.Mode, "document_comments_agent": draft.AgentRef, "document_comments_runtime_ref": draft.RuntimeRef, "document_comments_model_ref": draft.ModelRef, "document_comments_reasoning_effort": draft.ReasoningEffort}, managementActionDocumentCommentsEdit)...)
			initialReasoningEffort := draft.ReasoningEffort
			if initialReasoningEffort == "" {
				initialReasoningEffort = managementRuntimeDefaultReasoningOption
			}
			reasoningField := managementSelectStaticElement("document_comments_reasoning_effort", managementCopy("management.choose.reasoning.effort"), initialReasoningEffort, managementRuntimeReasoningOptions(managementSelectedModelState(state, draft.ModelRef, draft.ReasoningEffort), draft.RuntimeRef, draft.ModelRef))
			reasoningField.Required = false
			reasoningField.Behaviors = changeBehavior("document_comments_reasoning_effort")
			elements = append(elements, managementSettingsFieldRow(managementCopy("management.reasoning.effort.new.comment.topics.only"), reasoningField))
		}
	}
	submit := managementFormSubmitButton(managementActionDocumentCommentsSave, managementCopy("management.save"))
	submit.Value, _ = json.Marshal(managementButtonValue(managementButton{Action: managementActionDocumentCommentsSave, Values: map[string]string{"document_comments_mode": draft.Mode, "document_comments_agent": draft.AgentRef, "document_comments_runtime_ref": draft.RuntimeRef, "document_comments_model_ref": draft.ModelRef, "document_comments_reasoning_effort": draft.ReasoningEffort}}))
	buttons := managementFormButtonRowElement(submit, managementCallbackButton(managementActionDocumentCommentsOpen, managementCopy("management.cancel"), "default"))
	buttons.Margin = "4px 0px 0px 0px"
	elements = append(elements, cardElement{Tag: "form", Name: "document_comments_form", Direction: "vertical", VerticalSpacing: "8px", Elements: []cardElement{buttons}})
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.edit.comment.task.settings"), Subtitle: managementCopy("management.set.the.expert.and.execution.options.for.new.comment"), Summary: managementCopy("management.edit.document.comment.task.settings"), Theme: "blue", Icon: managementIcon("edit_outlined", "blue"), Elements: elements})
}

func managementSettingsFieldRow(label any, field cardElement) cardElement {
	return cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn(label, field)}}
}

func managementDocumentCommentsDraftFromState(state managementState) managementDocumentCommentsDraft {
	if state.DocumentCommentsAgentRef != "" {
		runtimeRef := strings.TrimSpace(state.DocumentCommentsRuntimeRef)
		if runtimeRef == "" {
			if profile, ok := managementProfileByRef(state, state.DocumentCommentsAgentRef); ok && managementValidRuntime(state, profile.AgentType) {
				runtimeRef = strings.TrimSpace(profile.AgentType)
			}
		}
		return managementDocumentCommentsDraft{Mode: managementDocumentCommentsModeCustom, AgentRef: state.DocumentCommentsAgentRef, RuntimeRef: runtimeRef, ModelRef: state.DocumentCommentsModelRef, ReasoningEffort: state.DocumentCommentsReasoningEffort}
	}
	modelRef := strings.TrimSpace(state.DefaultModelRef)
	if strings.TrimSpace(state.DefaultRuntimeRef) == "" {
		modelRef = effectiveNewSessionModelRef(state.WorkspaceModelRef, modelRef)
	}
	return managementDocumentCommentsDraft{Mode: managementDocumentCommentsModeInherit, AgentRef: state.DefaultAgent, RuntimeRef: managementEffectiveRuntimeRef(state), ModelRef: modelRef, ReasoningEffort: state.DefaultReasoningEffort}
}

func managementDocumentCommentsDraftFromCallback(action *callback.CallBackAction, state managementState) managementDocumentCommentsDraft {
	draft := managementDocumentCommentsDraftFromState(state)
	if action == nil {
		return managementNormalizeDocumentCommentsDraft(state,
			draft,
		)
	}
	if value, ok :=
		managementDocumentCommentsCallbackValue(action, "document_comments_mode"); ok {
		draft.Mode = value
	}
	if value, ok := managementDocumentCommentsCallbackValue(
		action,
		"document_comments_agent"); ok {
		draft.AgentRef = value
	}
	if value, ok := managementDocumentCommentsCallbackValue(action,
		"document_comments_model_ref"); ok {
		draft.ModelRef = managementModelRefFromForm(value)
	}
	if value, ok := managementDocumentCommentsCallbackValue(action, "document_comments_runtime_ref"); ok {
		draft.RuntimeRef = strings.TrimSpace(value)
	}
	if value, ok := managementDocumentCommentsCallbackValue(action, "document_comments_reasoning_effort"); ok {
		draft.ReasoningEffort = managementReasoningEffortFromForm(value)
	}
	field := managementDocumentCommentsDraftField(action)
	switch field {
	case "document_comments_mode":
		draft.Mode = strings.TrimSpace(action.Option)
	case "document_comments_agent":
		draft.AgentRef = strings.TrimSpace(action.Option)
	case "document_comments_runtime_ref":
		draft.RuntimeRef = strings.TrimSpace(action.Option)
	case "document_comments_model_ref":
		draft.ModelRef = managementModelRefFromForm(action.Option)
	case "document_comments_reasoning_effort":
		draft.ReasoningEffort = managementReasoningEffortFromForm(action.Option)
	}
	return managementNormalizeDocumentCommentsDraft(state, draft)
}

func managementDocumentCommentsCallbackValue(action *callback.CallBackAction, key string) (string, bool) {
	if action == nil {
		return "", false
	}
	if value, ok := action.FormValue[key].(string); ok {
		return strings.TrimSpace(value), true
	}
	if value, ok := action.Value[key].(string); ok {
		return strings.TrimSpace(value), true
	}
	return "", false
}

func managementDocumentCommentsDraftField(action *callback.CallBackAction) string {
	if action == nil {
		return ""
	}
	for _, field := range []string{"document_comments_mode",
		"document_comments_agent", "document_comments_runtime_ref", "document_comments_model_ref", "document_comments_reasoning_effort",
	} {
		if strings.TrimSpace(action.Name) == field {
			return field
		}
	}
	if field,
		ok := action.Value[managementDraftFieldKey].(string); ok {
		return strings.TrimSpace(field)
	}
	return ""
}

func managementDocumentCommentsSubmissionError(action *callback.CallBackAction, state managementState) any {
	mode, _ := managementDocumentCommentsCallbackValue(action, "document_comments_mode")
	switch mode {
	case managementDocumentCommentsModeInherit:
		return ""
	case managementDocumentCommentsModeCustom:
	default:
		return managementCopy("management.choose.a.task.configuration.for.document.comments")

	}
	agentRef, _ :=
		managementDocumentCommentsCallbackValue(action, "document_comments_agent")
	if !managementValidDocumentCommentsAgent(
		state, agentRef) {
		return managementCopy("management.this.expert.is.no.longer.in.the.library.refresh")
	}
	runtimeRef, _ := managementDocumentCommentsCallbackValue(action, "document_comments_runtime_ref")
	if !managementValidRuntime(state, runtimeRef) {
		return managementCopy("management.choose.the.runtime.for.document.comments")
	}
	modelValue, _ := managementDocumentCommentsCallbackValue(action, "document_comments_model_ref")
	modelRef := managementModelRefFromForm(modelValue)
	if !managementValidRuntimeModel(state, runtimeRef, modelRef) {
		return managementCopy("management.this.model.is.not.supported.by.the.selected.runtime")
	}
	reasoningValue, _ := managementDocumentCommentsCallbackValue(action, "document_comments_reasoning_effort")
	reasoningEffort := managementReasoningEffortFromForm(reasoningValue)
	if !managementValidRuntimeReasoningEffort(state, runtimeRef, modelRef, reasoningEffort) {
		return managementCopy("management.this.reasoning.effort.is.not.supported.by.the.selected")
	}
	return ""
}

func managementNormalizeDocumentCommentsDraft(state managementState, draft managementDocumentCommentsDraft) managementDocumentCommentsDraft {
	if draft.Mode !=
		managementDocumentCommentsModeCustom {
		draft.Mode = managementDocumentCommentsModeInherit
	}
	if draft.Mode == managementDocumentCommentsModeInherit {
		if !managementValidDocumentCommentsAgent(state, draft.AgentRef) {
			draft.AgentRef = managementFirstDocumentCommentsAgent(state)
		}
		if !managementValidRuntime(state, draft.RuntimeRef) {
			draft.RuntimeRef = managementEffectiveRuntimeRef(state)
		}
		if !managementValidRuntimeModel(state, draft.RuntimeRef, draft.ModelRef) {
			draft.ModelRef = ""
		}
		if !managementValidRuntimeReasoningEffort(state, draft.RuntimeRef, draft.ModelRef, draft.ReasoningEffort) {
			draft.ReasoningEffort = ""
		}
		return draft
	}
	if !managementValidDocumentCommentsAgent(state,
		draft.AgentRef,
	) {
		draft.AgentRef = managementFirstDocumentCommentsAgent(state)
	}
	if !managementValidRuntime(state, draft.RuntimeRef) {
		draft.RuntimeRef = managementEffectiveRuntimeRef(state)
	}
	if !managementValidRuntimeModel(state, draft.RuntimeRef, draft.ModelRef) {
		draft.ModelRef = ""
	}
	if !managementValidRuntimeReasoningEffort(state, draft.RuntimeRef, draft.ModelRef, draft.ReasoningEffort) {
		draft.ReasoningEffort = ""
	}
	return draft
}

func managementValidDocumentCommentsAgent(state managementState, ref string) bool {
	ref = strings.TrimSpace(ref)
	for _, profile := range state.Profiles {
		if profile.Ref == ref {
			return !profile.CurrentOnly
		}
	}
	return false
}

func managementFirstDocumentCommentsAgent(state managementState) string {
	if managementValidDocumentCommentsAgent(state, state.DefaultAgent) {
		return state.DefaultAgent
	}
	for _, profile := range state.Profiles {
		if !profile.CurrentOnly {
			return profile.Ref
		}
	}
	return ""
}

func managementGroupWatchDraftFromState(state managementState) managementGroupWatchDraft {
	if state.GroupWatch.AgentProfileRef != "" {
		runtimeRef := strings.TrimSpace(state.GroupWatch.RuntimeRef)
		if runtimeRef == "" {
			if profile, ok := managementProfileByRef(state, state.GroupWatch.AgentProfileRef); ok && managementValidRuntime(state, profile.AgentType) {
				runtimeRef = strings.TrimSpace(profile.AgentType)
			}
		}
		return managementGroupWatchDraft{Mode: managementDocumentCommentsModeCustom, AgentRef: state.GroupWatch.AgentProfileRef, RuntimeRef: runtimeRef, ModelRef: state.GroupWatch.ModelRef, ReasoningEffort: state.GroupWatch.ReasoningEffort, ThinkingProcess: state.GroupWatch.ThinkingProcess}
	}
	modelRef := strings.TrimSpace(state.DefaultModelRef)
	if strings.TrimSpace(state.DefaultRuntimeRef) == "" {
		modelRef = effectiveNewSessionModelRef(state.WorkspaceModelRef, modelRef)
	}
	return managementGroupWatchDraft{Mode: managementDocumentCommentsModeInherit, AgentRef: state.DefaultAgent, RuntimeRef: managementEffectiveRuntimeRef(state), ModelRef: modelRef, ReasoningEffort: state.DefaultReasoningEffort, ThinkingProcess: state.GroupWatch.ThinkingProcess}
}

func managementGroupWatchDraftFromCallback(action *callback.CallBackAction, state managementState) managementGroupWatchDraft {
	draft := managementGroupWatchDraftFromState(state)
	if action != nil {
		draft = managementGroupWatchScopeFromCallback(action, state, draft)
	}
	if action == nil {
		return managementNormalizeGroupWatchDraft(state, draft)
	}
	if value, ok := managementDocumentCommentsCallbackValue(action,
		"group_watch_mode"); ok {
		draft.Mode = value
	}
	if value, ok := managementDocumentCommentsCallbackValue(action,
		"group_watch_agent",
	); ok {
		draft.AgentRef = value
	}
	if value, ok := managementDocumentCommentsCallbackValue(action, "group_watch_runtime_ref"); ok {
		draft.RuntimeRef = strings.TrimSpace(value)
	}
	if value, ok := managementDocumentCommentsCallbackValue(action, "group_watch_model_ref"); ok {
		draft.ModelRef = managementModelRefFromForm(value)
	}
	if value, ok := managementDocumentCommentsCallbackValue(action, "group_watch_reasoning_effort"); ok {
		draft.ReasoningEffort = managementReasoningEffortFromForm(value)
	}
	if value, ok := managementDocumentCommentsCallbackValue(action, "group_watch_thinking_process"); ok && managementValidThinkingProcess(value) {
		draft.ThinkingProcess = strings.TrimSpace(value)
	}
	field := managementGroupWatchDraftField(action)
	switch field {
	case "group_watch_enabled":
		draft.Enabled = strings.TrimSpace(action.Option)
	case "group_watch_mode":
		draft.Mode = strings.TrimSpace(action.Option)
	case "group_watch_agent":
		draft.AgentRef = strings.TrimSpace(action.Option)
	case "group_watch_runtime_ref":
		draft.RuntimeRef = strings.TrimSpace(action.Option)
	case "group_watch_model_ref":
		draft.ModelRef = managementModelRefFromForm(action.Option)
	case "group_watch_reasoning_effort":
		draft.ReasoningEffort = managementReasoningEffortFromForm(action.Option)
	case "group_watch_thinking_process":
		if managementValidThinkingProcess(action.Option) {
			draft.ThinkingProcess = strings.TrimSpace(action.Option)
		}
	}
	return managementNormalizeGroupWatchDraft(state, draft)
}

func managementGroupWatchDraftField(action *callback.CallBackAction) string {
	if action ==
		nil {
		return ""
	}
	for _, field := range []string{"group_watch_mode",
		"group_watch_agent", "group_watch_runtime_ref", "group_watch_model_ref", "group_watch_reasoning_effort", "group_watch_thinking_process", "group_watch_enabled",
	} {
		if strings.TrimSpace(action.Name) == field {
			return field
		}
	}
	if field, ok := action.Value[managementDraftFieldKey].(string); ok {
		return strings.TrimSpace(field)
	}
	return ""
}

func managementGroupWatchSubmissionError(action *callback.CallBackAction, state managementState) any {
	if message := managementGroupWatchScopeSubmissionError(action, state); message != "" {
		return message
	}
	thinkingProcess, _ := managementDocumentCommentsCallbackValue(action, "group_watch_thinking_process")
	if !managementValidThinkingProcess(thinkingProcess) {
		return managementCopy("management.choose.the.activity.display.for.group.watch")
	}
	mode, _ := managementDocumentCommentsCallbackValue(action, "group_watch_mode")
	switch mode {
	case managementDocumentCommentsModeInherit:
		return ""
	case managementDocumentCommentsModeCustom:
	default:
		return managementCopy("management.choose.a.task.configuration.for.group.watch")
	}
	agentRef, _ := managementDocumentCommentsCallbackValue(action, "group_watch_agent")
	if !managementValidDocumentCommentsAgent(state, agentRef) {
		return managementCopy("management.this.expert.is.no.longer.in.the.library.refresh")
	}
	runtimeRef, _ := managementDocumentCommentsCallbackValue(action, "group_watch_runtime_ref")
	if !managementValidRuntime(state, runtimeRef) {
		return managementCopy("management.choose.the.runtime.for.group.watch")
	}
	modelValue, _ := managementDocumentCommentsCallbackValue(action, "group_watch_model_ref")
	modelRef := managementModelRefFromForm(modelValue)
	if !managementValidRuntimeModel(state, runtimeRef, modelRef) {
		return managementCopy("management.this.model.is.not.supported.by.the.selected.runtime")
	}
	reasoningValue, _ := managementDocumentCommentsCallbackValue(action, "group_watch_reasoning_effort")
	reasoningEffort := managementReasoningEffortFromForm(reasoningValue)
	if !managementValidRuntimeReasoningEffort(state, runtimeRef, modelRef, reasoningEffort) {
		return managementCopy("management.this.reasoning.effort.is.not.supported.by.the.selected")
	}
	return ""
}

func managementNormalizeGroupWatchDraft(state managementState, draft managementGroupWatchDraft) managementGroupWatchDraft {
	draft = managementGroupWatchScopeDefaults(state, draft)
	if !managementValidThinkingProcess(draft.ThinkingProcess) {
		draft.ThinkingProcess = state.GroupWatch.ThinkingProcess
	}
	if !managementValidThinkingProcess(draft.ThinkingProcess) {
		draft.ThinkingProcess = config.DefaultFeishuThinkingProcess
	}
	if draft.Mode != managementDocumentCommentsModeCustom {
		draft.Mode = managementDocumentCommentsModeInherit
	}
	if draft.Mode ==
		managementDocumentCommentsModeInherit {
		if !managementValidDocumentCommentsAgent(state, draft.AgentRef) {
			draft.AgentRef = managementFirstDocumentCommentsAgent(state)
		}
		if !managementValidRuntime(state, draft.RuntimeRef) {
			draft.RuntimeRef = managementEffectiveRuntimeRef(state)
		}
		if !managementValidRuntimeModel(state, draft.RuntimeRef, draft.ModelRef) {
			draft.ModelRef = ""
		}
		if !managementValidRuntimeReasoningEffort(state, draft.RuntimeRef, draft.ModelRef, draft.ReasoningEffort) {
			draft.ReasoningEffort = ""
		}
		return draft
	}
	if !managementValidDocumentCommentsAgent(state,
		draft.AgentRef) {
		draft.AgentRef = managementFirstDocumentCommentsAgent(state)
	}
	if !managementValidRuntime(state, draft.RuntimeRef) {
		draft.RuntimeRef = managementEffectiveRuntimeRef(state)
	}
	if !managementValidRuntimeModel(state, draft.RuntimeRef, draft.ModelRef) {
		draft.ModelRef = ""
	}
	if !managementValidRuntimeReasoningEffort(state, draft.RuntimeRef, draft.ModelRef, draft.ReasoningEffort) {
		draft.ReasoningEffort = ""
	}
	return draft
}

func renderDocumentCommentsProcessingCard(enabled bool) string {
	action := managementCopy("management.on")
	if !enabled {
		action = managementCopy("management.off")
	}
	return marshalNativeManagementCard(managementCardView{Title: managementConcat(managementCopy("management.processing"), action, managementCopy("management.document.comments")),
		Subtitle: managementCopy("management.affects.only.this.assistant"),
		Summary:  managementCopy("management.updating.document.comment.settings"),

		Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementHintElement(managementCopy("management.checking.feishu.permissions.and.saving.settings"))}})
}

func renderSettingsEntryCard(state managementState) string {
	return renderSettingsEditCard(state, managementDraftFromState(state))
}

func renderSettingsEditCard(state managementState, draft managementSettingsDraft) string {
	return renderSettingsEditCardForSurface(state, draft, managementSurfaceDefault)
}

func renderSettingsEditCardForSurface(state managementState, draft managementSettingsDraft, surface managementSurface) string {
	draft = managementNormalizeDraftForState(state, draft)
	var libraryAction *managementButton
	if state.CanManagePacks && surface != managementSurfaceAmux {
		libraryAction = &managementButton{Text: managementCopy("management.expert.library"), Type: "default", Action: managementActionPackLibraryOpen}
	}
	elements := managementSettingsEditorElements(state,
		"settings_form", draft, managementActionSettingsSave, managementCopy("management.save"), managementActionSettingsCancel,
		libraryAction, managementMessageHandlingHintElement())
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.edit.defaults"), Subtitle: managementCopy("management.takes.effect.when.saved"), Summary: managementCopy("management.edit.amux.assistant.defaults"), Theme: "blue", Icon: managementIcon("edit_outlined", "blue"), Elements: elements})
}

func renderSettingsConfirmationCard(state managementState, draft managementSettingsDraft, req BotSettingsRequest, result BotSettingsResult) string {
	draft = managementNormalizeDraftForState(state, draft)
	values := map[string]string{"interaction": req.Interaction, "default_agent": req.DefaultAgent,
		"default_runtime_ref": req.DefaultRuntimeRef, "default_model_ref": req.DefaultModelRef,
		"default_reasoning_effort": req.DefaultReasoningEffort,
		"thinking_process":         draft.ThinkingProcess,
		"expected_version":         strconv.FormatInt(req.ExpectedVersion, 10), "request_id": req.RequestID,
		"confirmation_token": result.ConfirmationToken}
	title := managementCopy("management.confirm.default.expert.change")
	subtitle := managementCopy("management.the.current.conversation.and.workspace.will.be.retained")
	confirmText := managementCopy("management.confirm.change")
	summary := managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.current.default.expert"),

		Body: managementDefaultAgentName(state)}, managementSummaryItem{Title: managementCopy("management.new.default.expert"), Body: managementProfileNameByRef(state, draft.DefaultAgent)}, managementSummaryItem{Title: managementCopy("management.model.in.use"), Body: managementEffectiveModelLabel(state, draft.DefaultAgent, draft.DefaultModelRef)})
	if state.DefaultAgent == draft.DefaultAgent && state.DefaultModelRef != draft.DefaultModelRef {
		title = managementCopy("management.confirm.default.model.change")
		summary = managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.current.default.model"), Body: managementEffectiveModelLabel(state, state.DefaultAgent, state.DefaultModelRef)}, managementSummaryItem{Title: managementCopy("management.new.default.model"), Body: managementEffectiveModelLabel(state, draft.DefaultAgent,
			draft.DefaultModelRef)})
	}
	if state.Mode != config.FeishuInteractionSingleton && req.Interaction == config.FeishuInteractionSingleton && state.DefaultAgent == req.DefaultAgent && state.DefaultModelRef == req.DefaultModelRef {
		title = managementCopy("management.confirm.recreating.the.persistent.conversation")
		subtitle = managementCopy("management.the.original.conversation.context.cannot.be.recovered")

		confirmText = managementCopy("management.confirm.creation")
		summary = managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.current.message.handling"), Body: managementInteractionText(state.Mode)}, managementSummaryItem{Title: managementCopy("management.switch.to"), Body: managementInteractionText(req.Interaction)})
	}
	elements := []cardElement{summary, managementHintElement(managementImpactText(result))}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: subtitle, Summary: managementCopy("management.assistant.settings.need.confirmation"), Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: elements, Buttons: []managementButton{{Text: confirmText, Type: "primary", Action: managementActionSettingsConfirm, Values: values}, {Text: managementCopy("management.cancel"), Type: "default", Action: managementActionSettingsCancel}}})
}

func managementEffectiveModelLabel(state managementState, agentRef, ref string) any {
	if ref = strings.TrimSpace(ref); ref != "" {
		return managementRuntimeModelLabel(ref)
	}
	if profile, ok := managementProfileByRef(state, agentRef); ok && !profile.CurrentOnly && profile.AgentType == "" {
		return managementCopy("management.choose.a.runtime")
	}
	defaultModel := managementDefaultModelDisplayFor(state, agentRef)
	if defaultModel.Unavailable {
		return managementCopy("management.expert.unavailable.cannot.determine.the.default.model")
	}
	if model := defaultModel.ProfileModel; model != "" {
		return managementConcat(managementCopy("management.expert.default"), managementRuntimeModelLabel(model))
	}
	if agentName := defaultModel.AgentName; agentName != "" {
		return managementConcat(agentName, managementCopy("management.default.model.130"))
	}
	return managementCopy("management.expert.default.model")
}

func managementProfileDefaultModelOptionLabel(state managementState, agentRef string) any {
	if profile, ok := managementProfileByRef(state, agentRef); ok && !profile.CurrentOnly && profile.AgentType == "" {
		return managementCopy("management.choose.a.runtime")
	}
	defaultModel := managementDefaultModelDisplayFor(state, agentRef)
	if defaultModel.Unavailable {
		return managementCopy("management.expert.unavailable.cannot.determine.the.default.model")
	}
	if model := defaultModel.ProfileModel; model != "" {
		return managementConcat(managementCopy("management.use.expert.default.model"), managementRuntimeModelLabel(model), "）")
	}
	if agentName := defaultModel.AgentName; agentName != "" {
		return managementConcat(managementCopy("management.use"), agentName, managementCopy("management.default.model.130"))
	}
	return managementCopy("management.use.expert.default.model.627")
}

func managementDefaultModelDisplayFor(state managementState, agentRef string) managementDefaultModelDisplay {
	profile, ok := managementProfileByRef(state, agentRef)
	if !ok {
		return managementDefaultModelDisplay{}
	}
	return managementDefaultModelDisplay{ProfileModel: strings.TrimSpace(profile.Model), AgentName: managementAgentTypeName(profile.AgentType), Unavailable: profile.CurrentOnly && strings.TrimSpace(profile.Model) == "" && strings.TrimSpace(profile.AgentType) == ""}
}

func managementProfileByRef(state managementState, ref string) (managementProfileSummary, bool) {
	ref = strings.TrimSpace(ref)
	for _, profile := range state.Profiles {
		if strings.TrimSpace(profile.Ref) == ref {
			return profile, true
		}
	}
	return managementProfileSummary{}, false
}

func managementAgentTypeName(agentType string) string {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	case "pi":
		return "Pi"
	default:
		return distributionAgentTypeName(agentType)
	}
}

func renderSettingsRecoveryChoiceCard(state managementState, draft managementSettingsDraft, req BotSettingsRequest, result BotSettingsResult, notice any) string {
	draft = managementNormalizeDraftForState(state, draft)
	values := managementBotSettingsRecoveryValues(req, draft.ThinkingProcess, result.ConfirmationToken)
	elements := []cardElement{managementHintElement(managementCopy("management.separate.conversations.per.topic.are.still.active.existing.settings")), managementHintElement(managementNonemptyText(managementImpactText(result), managementCopy("management.the.original.conversation.and.project.directory.are.unavailable.choose")))}
	if strings.TrimSpace(managementDefaultText(notice)) != "" {
		elements = append(elements, managementHintElement(notice))
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.cannot.recover.the.original.conversation"), Subtitle: managementCopy("management.choose.how.to.continue"), Summary: managementCopy("management.original.conversation.cannot.be.recovered"), Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: elements, Buttons: []managementButton{{Text: managementCopy("management.new.blank.persistent.conversation"), Type: "primary", Action: managementActionSettingsRecoveryBlank, Values: values, Confirm: managementConfirm(managementCopy("management.create.a.blank.persistent.conversation"), managementCopy("management.the.new.workspace.will.not.contain.the.original.project"))}, {Text: managementCopy("management.bind.an.existing.project"), Type: "default", Action: managementActionSettingsRecoveryBind, Values: values}, {Text: managementCopy("management.not.now"), Type: "default", Action: managementActionSettingsRecoveryDismiss, Values: values}}})
}

func managementBotSettingsRecoveryValues(req BotSettingsRequest, thinkingProcess, token string) map[string]string {
	return map[string]string{"interaction": req.Interaction, "default_agent": req.DefaultAgent,
		"default_runtime_ref": req.DefaultRuntimeRef, "default_model_ref": req.DefaultModelRef,
		"default_reasoning_effort": req.DefaultReasoningEffort,
		"thinking_process":         thinkingProcess,
		"expected_version":         strconv.FormatInt(req.ExpectedVersion, 10), "request_id": req.RequestID,
		"confirmation_token": token}
}

func renderSettingsManualBindCard(state managementState, req BotSettingsRequest, thinkingProcess string, token string, notice any) string {
	profileName := strings.TrimSpace(state.ProfileName)
	if profileName == "" {
		profileName = DefaultProfileName
	}
	profileSessionName := sanitizeManagedSessionName(profileName)
	if profileSessionName == "" {
		profileSessionName = DefaultProfileName
	}
	sessionName := "feishu-" + profileSessionName + "-main"
	command := fmt.Sprintf("cd /path/to/project\namux session run %s \\\n  -n %s \\\n  --cwd \"$PWD\"\namux bridge feishu bot-session bind \\\n  --profile %s \\\n  %s", strings.TrimSpace(req.DefaultAgent), sessionName, profileName, sessionName)
	elements := []cardElement{managementHintElement(managementCopy("management.on.the.machine.running.amux.enter.the.project.directory")), managementTextElement(command, "notation", "default", nil)}
	if strings.TrimSpace(managementDefaultText(notice)) != "" {
		elements = append(elements, managementHintElement(notice))
	}
	values := managementBotSettingsRecoveryValues(req, thinkingProcess, token)
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.bind.an.existing.project"), Subtitle: managementCopy("management.separate.conversations.per.topic.remain.active.until.binding.completes"), Summary: managementCopy("management.bind.an.existing.project"), Theme: "blue", Icon: managementIcon("link_outlined", "blue"), Elements: elements, Buttons: []managementButton{{Text: managementCopy("management.check.binding"), Type: "primary", Action: managementActionSettingsRecoveryCheck, Values: values}, {Text: managementCopy("management.back.to.recovery.options"), Type: "default", Action: managementActionSettingsRecoveryBack, Values: values}}})
}

func renderSettingsProcessingCard(state managementState, draft managementSettingsDraft) string {
	draft = managementNormalizeDraftForState(state, draft)
	title := managementCopy("management.saving.defaults")
	subtitle := managementCopy("management.refreshes.automatically.when.complete.643")
	if draft.Interaction == config.FeishuInteractionSingleton {
		title = managementCopy("management.preparing.a.new.persistent.conversation")
		if state.Mode == config.FeishuInteractionSingleton {
			subtitle = managementCopy("management.the.current.conversation.remains.usable.switches.automatically.when.ready")
		}
	}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: subtitle, Summary: title, Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.message.handling.summary"), Body: managementInteractionText(draft.Interaction)}, managementSummaryItem{Title: managementCopy("management.default.expert"), Body: managementProfileNameByRef(state, draft.DefaultAgent)}, managementSummaryItem{Title: managementCopy("management.default.model"), Body: managementEffectiveModelLabel(state, draft.DefaultAgent, draft.DefaultModelRef)})}})
}

func renderSettingsBlankRecoveryProcessingCard(state managementState, draft managementSettingsDraft) string {
	draft = managementNormalizeDraftForState(state, draft)
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.creating.a.blank.persistent.conversation"), Subtitle: managementCopy("management.switches.automatically.when.created"), Summary: managementCopy("management.creating.a.blank.persistent.conversation"), Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.message.handling.summary"), Body: managementInteractionText(draft.Interaction)}, managementSummaryItem{Title: managementCopy("management.default.expert"), Body: managementProfileNameByRef(state, draft.DefaultAgent)}, managementSummaryItem{Title: managementCopy("management.default.model"), Body: managementEffectiveModelLabel(state, draft.DefaultAgent, draft.DefaultModelRef)})}})
}

func renderSettingsFailureCardForSurface(state managementState, draft managementSettingsDraft, transitionErr error, surface managementSurface) string {
	draft = managementNormalizeDraftForState(state, draft)
	message := managementSettingsFailureMessage(transitionErr)
	var libraryAction *managementButton
	if state.CanManagePacks && surface != managementSurfaceAmux {
		libraryAction = &managementButton{Text: managementCopy("management.expert.library"), Type: "default", Action: managementActionPackLibraryOpen}
	}
	elements := []cardElement{managementHintElement(message), managementProfileLibraryHintElement(state)}
	elements = append(elements, managementSettingsEditorElements(state, "settings_form", draft, managementActionSettingsSave, managementCopy("management.retry"), managementActionSettingsCancel, libraryAction, managementTextElement("", "notation", "grey", nil))...)
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.save.failed"), Subtitle: managementCopy("management.settings.unchanged.you.can.try.again"), Summary: managementCopy("management.amux.assistant.settings.failed"), Theme: "red", Icon: managementIcon("warning_outlined", "red"), Elements: elements})
}

func managementSettingsFailureMessage(transitionErr error) any {
	if errors.Is(transitionErr, errBotProfileStateChanged) {
		return managementCopy("management.configuration.changed.the.latest.state.has.been.refreshed.make")
	}
	if transitionErr == nil {
		return managementCopy("management.failure.reason.no.specific.error.returned.current.settings.are")
	}
	reason := managementText(managementErrorText(transitionErr)).Map(func(reason string) string {
		if errors.Is(transitionErr, errBotSettingsBlankRecoveryFailed) {
			reason = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reason), errBotSettingsBlankRecoveryFailed.Error()))
			reason = strings.TrimSpace(strings.TrimPrefix(reason, ":"))
		}
		reason = strings.Join(strings.Fields(reason), " ")
		reason = subtaskSecretAssignmentRE.ReplaceAllString(reason, `${1}${2}[REDACTED]`)
		reason = subtaskBearerRE.ReplaceAllString(reason, "Bearer [REDACTED]")
		reason = subtaskSecretTokenRE.ReplaceAllString(reason, "[REDACTED]")
		return truncateCardText(reason, 240)
	})
	if managementDefaultText(reason) == "" {
		return managementCopy("management.failure.reason.no.specific.error.returned.current.settings.are")
	}
	return managementConcat(managementCopy("management.failure.reason"), reason, managementCopy("management.current.settings.are.unchanged"))
}

type managementProfileSummary struct {
	Ref             string
	Name            string
	LibraryName     string
	LibraryLabel    any
	Description     string
	AgentType       string
	Model           string
	SupportedModels []string
	CallerDefaults  bool
	Development     bool
	CurrentOnly     bool
}
type managementSettingsDraft struct {
	Interaction            string
	DefaultAgent           string
	DefaultRuntimeRef      string
	DefaultModelRef        string
	DefaultReasoningEffort string
	ThinkingProcess        string
}

func managementRuntimeLabel(state managementState, runtimeRef string) any {
	runtime, ok := managementRuntimeByRef(state, runtimeRef)
	if !ok {
		return managementValueOrPlaceholder(strings.TrimSpace(runtimeRef), managementCopy("management.not.selected"))
	}
	if label := strings.TrimSpace(runtime.DisplayName); label != "" {
		return label
	}
	return runtime.Ref
}

func managementRuntimeModelLabel(modelRef string) any {
	if modelRef = strings.TrimSpace(modelRef); modelRef != "" {
		if runtimes, err := agentprofile.RuntimeCatalog(); err == nil {
			for _, runtime := range runtimes {
				if model, err := agentprofile.ResolveRuntimeModel(runtime, modelRef); err == nil && model.DisplayName != "" {
					return model.DisplayName
				}
			}
		}
		return modelRef
	}
	return managementCopy("management.runtime.default.model")
}

func managementReasoningEffortLabel(effort string) any {
	if effort = strings.TrimSpace(effort); effort != "" {
		return effort
	}
	return managementCopy("management.runtime.default.effort")
}

type managementDocumentCommentsDraft struct {
	Mode            string
	AgentRef        string
	RuntimeRef      string
	ModelRef        string
	ReasoningEffort string
}
type managementGroupWatchDraft struct {
	ChatID          string
	ChatName        string
	Version         string
	Enabled         string
	MaxActive       string
	IdleSeconds     string
	Page            int
	Mode            string
	AgentRef        string
	RuntimeRef      string
	ModelRef        string
	ReasoningEffort string
	ThinkingProcess string
}

type managementCapabilityView struct {
	ID         string
	Title      any
	Status     any
	Summary    any
	Enabled    bool
	OpenAction string
}

type managementDefaultModelDisplay struct {
	ProfileModel string
	AgentName    string
	Unavailable  bool
}

func renderDocumentCommentsExecutionProcessingCard() string {
	return marshalNativeManagementCard(
		managementCardView{Title: managementCopy("management.saving.comment.task.settings"), Subtitle: managementCopy("management.affects.only.newly.created.comment.topics"),
			Summary: managementCopy("management.saving.document.comment.task.settings"),
			Theme:   "blue",

			Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementHintElement(managementCopy("management.checking.the.expert.and.execution.options"))}})
}
