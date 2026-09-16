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
func managementProfileSubmissionError(action *callback.CallBackAction, state managementState) string {
	value, submitted := managementSubmittedDefaultAgent(action)
	if submitted {
		if managementValidDefaultAgent(state, value) {
			return ""
		}
		return "该专家已不在专家库中，请刷新后重新选择"
	}
	if strings.TrimSpace(state.DefaultAgent) == "" {
		return "专家库中暂无可用专家，请先添加专家"
	}
	return ""
}
func managementModelSubmissionError(action *callback.CallBackAction, state managementState) string {
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
	return "该模型不在所选运行引擎的支持列表中，请重新选择"
}
func managementRuntimeSubmissionError(action *callback.CallBackAction, state managementState) string {
	runtimeRef := managementCallbackString(action, "default_runtime_ref")
	if runtimeRef == "" || managementValidRuntime(state, runtimeRef) {
		return ""
	}
	return "该运行引擎不可用，请刷新后重新选择"
}
func managementReasoningSubmissionError(action *callback.CallBackAction, state managementState) string {
	effort := managementReasoningEffortFromForm(managementCallbackString(action, "default_reasoning_effort"))
	if effort == "" {
		return ""
	}
	draft := managementDraftFromCallback(action, state)
	if managementValidRuntimeReasoningEffort(state, draft.DefaultRuntimeRef, draft.DefaultModelRef, effort) {
		return ""
	}
	return "该思考强度不适用于所选运行引擎和模型，请重新选择"
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
func (b *Bridge) expiredManagementRecoveryCallback(openID, messageID string, state managementState, surface managementSurface, message string) (*callback.CardActionTriggerResponse, bool, error) {
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
		notice := "设置已保存，正在按当前已开启能力校验飞书权限。"
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Bool("enabled", enabled).Msg("bridge: update Bot-to-Bot setting")
			notice = "智能体协作设置保存失败，当前设置没有改变。请稍后重试。"
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
		notice := ""
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Bool("enabled", enabled).Msg("bridge: update document comment setting")
			if enabled {
				notice = "云文档评论开启失败，当前设置没有改变。请检查助手的云文档权限或稍后重试。"
			} else {
				notice = "云文档评论关闭失败，当前设置没有改变。请稍后重试。"
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
		notice := "任务配置已保存，仅影响新建评论话题。"
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: save document comment execution")
			notice = "任务配置保存失败，当前设置未更改。请刷新后重新选择专家和执行参数。"
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
		notice := "任务配置已保存，仅影响之后创建的值守话题。"
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: save group watch execution")
			notice = "任务配置保存失败，当前设置未更改。请刷新后重新选择专家和模型。"
		}
		state := b.managementState()
		nameCtx, nameCancel := context.WithTimeout(context.Background(), 10*time.Second)
		names := b.resolveGroupWatchNames(nameCtx, state.GroupWatch.ChatIDs)
		nameCancel()
		finalCard := renderGroupWatchManagementCardWithNoticeAndNames(state, notice, names)
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

func managementThinkingProcessLabel(value string) string {
	switch strings.TrimSpace(value) {
	case config.FeishuThinkingProcessCollapsed:
		return "折叠展示"
	case config.FeishuThinkingProcessHidden:
		return "不展示"
	default:
		return "展开展示"
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
	for _, profile := range // managementDecomposeInteraction maps the card-facing composite value
	// thread_isolated back to (thread, isolation_required=true); every plain mode
	// maps to itself with isolation_required=false. The routing enum (thread |
	// singleton) stays untouched — isolation only rides on thread.
	state.Profiles {
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
	if b.dmInteractive != nil {
		_, _, err := b.dmInteractive(ctx, openID, cardJSON)
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _, err := b.sendInteractiveToDM(sendCtx, openID, cardJSON, "amux management card")
	return err
}
func (b *Bridge) notifyThreadHelpCard(ctx context.Context, rootMsgID, cardJSON string) error {
	if b.threadNotice != nil {
		return b.threadNotice(ctx, rootMsgID, cardJSON)
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
	appendProfile := func(profile agentprofile.Profile, libraryName string) {
		out = append(out, managementProfileSummaryFromProfile(profile, libraryName))
	}
	for _, profile := range agentprofile.Builtins() {
		appendProfile(profile, "预置")
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
				profileLibraryName := libraryName
				if profile.Source == agentprofile.SourceLocal {
					profileLibraryName = "本地"
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
func managementProfileSummaryFromProfile(profile agentprofile.Profile, libraryName string) managementProfileSummary {
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = profile.Ref()
	}
	supportedModels := profile.SupportedModels
	if supportedModels != nil {
		supportedModels = append([]string{}, supportedModels...)
	}
	return managementProfileSummary{Ref: profile.Ref(), Name: name, LibraryName: strings.TrimSpace(libraryName), Description: strings.TrimSpace(profile.Description), AgentType: strings.TrimSpace(profile.Type), Model: profileDisplayModel(profile), SupportedModels: supportedModels, Development: strings.HasSuffix(profile.Namespace, "-dev")}
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
		managementSummaryItem{Title: "当前选择", Body: managementModeSummary(draft)},
		managementSummaryItem{Title: "默认专家", Body: managementProfileNameByRef(state, draft.DefaultAgent)},
		managementSummaryItem{Title: "运行引擎", Body: managementRuntimeLabel(state, draft.DefaultRuntimeRef)},
		managementSummaryItem{Title: "默认模型", Body: managementRuntimeModelLabel(draft.DefaultModelRef)},
		managementSummaryItem{Title: "思考强度", Body: managementReasoningEffortLabel(draft.DefaultReasoningEffort)},
		managementSummaryItem{Title: "处理过程展示", Body: managementThinkingProcessLabel(draft.ThinkingProcess)}), managementDividerElement(), managementMessageHandlingHintElement(), managementProfileLibraryHintElement(state)}
	elements = append(elements, managementSettingsEditorElements(state, "onboarding_form", draft, managementActionOnboardingSubmit, "完成配置", "", nil, managementTextElement("", "notation", "grey", nil))...)
	elements = append(elements, managementHintElement(managementGroupCollaborationHintText()))
	return marshalNativeManagementCard(managementCardView{Title: "配置助手", Subtitle: "设置新任务的处理方式、专家和模型", Summary: "配置 amux 助手", Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: elements})
}
func renderOnboardingCompleteCard(state managementState, draft managementSettingsDraft) string {
	draft = managementNormalizeDraftForState(state, draft)
	elements := []cardElement{managementCompactSummaryElement(
		managementSummaryItem{Title: "当前配置", Body: managementModeSummary(draft)},
		managementSummaryItem{Title: "默认专家", Body: managementProfileNameByRef(state, draft.DefaultAgent)},
		managementSummaryItem{Title: "运行引擎", Body: managementRuntimeLabel(state, draft.DefaultRuntimeRef)},
		managementSummaryItem{Title: "默认模型", Body: managementRuntimeModelLabel(draft.DefaultModelRef)},
		managementSummaryItem{Title: "思考强度", Body: managementReasoningEffortLabel(draft.DefaultReasoningEffort)},
		managementSummaryItem{Title: "处理过程展示", Body: managementThinkingProcessLabel(draft.ThinkingProcess)})}
	return marshalNativeManagementCard(managementCardView{Title: "助手已准备好", Subtitle: "现在可以直接发送任务", Summary: "amux 助手已准备好", Theme: "green", Icon: managementIcon("check_outlined", "green"), Elements: elements, Buttons: []managementButton{{Text: "助手设置", Type: "primary", Action: managementActionSettingsOpen, Icon: managementIconPtr("setting_outlined", "blue")}, {Text: "返回概览", Type: "default", Action: managementActionStatusOpen, Icon: managementIconPtr("info_outlined", "grey")}}})
}
func renderSettingsCardForSurface(state managementState, surface managementSurface) string {
	draft := managementDraftFromState(state)
	settingsAction := managementButton{Text: "修改默认配置", Type: "primary", Action: managementActionSettingsEdit, Icon: managementIconPtr("edit_outlined", "blue")}
	capabilitiesAction := managementButton{Text: "管理接入能力", Type: "default", Action: managementActionCapabilitiesOpen, Icon: managementIconPtr("setting_outlined", "blue")}
	capabilities := managementCapabilityViews(state)
	elements := []cardElement{managementSectionTitleElement("任务默认配置"), managementTaskDefaultsElement(state, draft, "amux_settings_task_defaults"), managementButtonRowElement([]managementButton{settingsAction}), managementDividerElement(), managementSectionTitleElement("接入能力"), managementCapabilitiesSummaryElement(capabilities), managementButtonRowElement([]managementButton{capabilitiesAction})}
	elements = append(elements, managementHintElement("以上设置仅影响当前智能体"))
	var buttons []managementButton
	if surface != managementSurfaceAmux {
		if state.CanManagePacks {
			buttons = append(buttons, managementButton{Text: "专家库", Type: "default", Action: managementActionPackLibraryOpen})
		}
		buttons = append(buttons, managementButton{Text: "返回概览", Type: "default", Action: managementActionStatusOpen})
	}
	return marshalNativeManagementCard(managementCardView{Title: "助手设置", Summary: "amux 助手设置", Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: elements, Buttons: buttons})
}
func managementCapabilityViews(state managementState) []managementCapabilityView {
	groupWatchStatus := "未配置"
	groupWatchEnabled := state.GroupWatch.Enabled && len(state.GroupWatch.ChatIDs) > 0
	if len(state.GroupWatch.ChatIDs) > 0 {
		if groupWatchEnabled {
			groupWatchStatus = fmt.Sprintf("已开启 · %d 个群", len(state.GroupWatch.ChatIDs))
		} else {
			groupWatchStatus = fmt.Sprintf("已暂停 · %d 个群", len(state.GroupWatch.ChatIDs))
		}
	}
	documentCommentsStatus := "已关闭"
	if state.DocumentCommentsEnabled {
		documentCommentsStatus = "已开启"
	}
	botToBotStatus := "已关闭"
	botToBotEnabled := state.BotToBot.Enabled && state.Mode == config.FeishuInteractionThread
	if state.BotToBot.Enabled {
		if botToBotEnabled {
			botToBotStatus = "已开启"
		} else {
			botToBotStatus = "切换到每个话题独立后生效"
		}
	}
	return []managementCapabilityView{{ID: "group_watch", Title: "群值守", Status: groupWatchStatus, Summary: "自动处理指定群的新话题", Enabled: groupWatchEnabled, OpenAction: managementActionGroupWatchOpen}, {ID: "document_comments", Title: "云文档评论", Status: documentCommentsStatus, Summary: "在评论中 @智能体 发起任务", Enabled: state.DocumentCommentsEnabled, OpenAction: managementActionDocumentCommentsOpen}, {ID: "bot_to_bot", Title: "智能体 @ 智能体", Status: botToBotStatus, Summary: "接收其他智能体的任务，也可调用群内智能体", Enabled: botToBotEnabled, OpenAction: managementActionBotToBotOpen}}
}
func managementCapabilitiesSummaryElement(capabilities []managementCapabilityView) cardElement {
	enabled := 0
	previews := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability.Enabled {
			enabled++
		}
		previews = append(previews, capability.Title+" · "+capability.Status)
	}
	rows := []cardElement{managementKeyValueRowElement("接入能力", fmt.Sprintf("%d 项 · %d 项已开启", len(capabilities), enabled))}
	if len(previews) > 0 {
		rows = append(rows, managementTextElement(strings.Join(previews, "\n"), "notation", "grey", nil))
	}
	element := managementVerticalSummaryElement("amux_settings_capabilities_summary", rows...)
	element.Columns[0].VerticalSpacing = "4px"
	return element
}
func renderCapabilitiesCard(state managementState) string {
	return renderCapabilitiesCardWithNotice(state, "")
}
func renderCapabilitiesCardWithNotice(state managementState, notice string) string {
	capabilities := managementCapabilityViews(state)
	enabled := 0
	for _, capability := range capabilities {
		if capability.Enabled {
			enabled++
		}
	}
	elements := []cardElement{managementSubpageBackElement("返回助手设置", managementActionSettingsOpen, "amux.capabilities.back.settings"), managementDividerElement(), managementTextElement(fmt.Sprintf("共 %d 项能力 · %d 项已开启", len(capabilities), enabled), "notation", "grey", nil)}
	if strings.TrimSpace(notice) != "" {
		elements = append(elements, managementTextElement(truncateCardText(notice, 240), "normal", "orange", managementIconPtr("warning_outlined", "orange")))
	}
	if state.PermissionAudit != nil {
		elements = append(elements, managementTextElement(capabilityPermissionNotice(*state.PermissionAudit), "normal", "orange", managementIconPtr("warning_outlined", "orange")))
		var permissionButtons []managementButton
		if len(state.PermissionAudit.Scopes) > 0 {
			permissionButtons = append(permissionButtons, managementButton{Text: "申请权限", Type: "primary", Action: managementActionPermissionApply, Name: "amux.capabilities.permissions.apply", Confirm: managementConfirm("向企业管理员申请这些权限？", "应用会向租户管理员发起权限授权申请；审核生效后能力自动恢复，可能需要发布应用版本。")})
		}
		if state.PermissionAudit.GrantURL != "" {
			permissionButtons = append(permissionButtons, managementButton{Text: "开发者后台", Type: "default", URL: state.PermissionAudit.GrantURL})
		}
		permissionButtons = append(permissionButtons, managementButton{Text: "检查飞书权限", Type: "default", Action: managementActionPermissionRecheck, Name: "amux.capabilities.permissions.recheck"})
		elements = append(elements, managementButtonRowElement(permissionButtons))
	} else if state.PermissionAuditFailed {
		elements = append(elements, managementTextElement("飞书权限 · 暂时无法检测", "normal", "orange", managementIconPtr("warning_outlined", "orange")), managementButtonRowElement([]managementButton{{Text: "检查飞书权限", Type: "default", Action: managementActionPermissionRecheck, Name: "amux.capabilities.permissions.recheck"}}))
	} else if state.PermissionAuditChecked {
		elements = append(elements, managementTextElement("飞书权限 · 当前未发现缺失", "normal", "green", managementIconPtr("check_outlined", "green")), managementButtonRowElement([]managementButton{{Text: "检查飞书权限", Type: "default", Action: managementActionPermissionRecheck, Name: "amux.capabilities.permissions.recheck"}}))
	} else {
		elements = append(elements, managementTextElement("飞书权限 · 尚未检测", "normal", "default", managementIconPtr("info_outlined", "grey")), managementButtonRowElement([]managementButton{{Text: "检查飞书权限", Type: "default", Action: managementActionPermissionRecheck, Name: "amux.capabilities.permissions.recheck"}}))
	}
	for index, capability := range capabilities {
		if index > 0 {
			elements = append(elements, managementDividerElement())
		}
		elements = append(elements, managementCapabilityDirectoryElements(capability)...)
	}
	elements = append(elements, managementHintElement("以上设置仅影响当前智能体"))
	return marshalNativeManagementCard(managementCardView{Title: "接入能力", Summary: "amux 接入能力", Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: elements})
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
		notice := "检测完成，当前已开启能力所需权限均已满足。"
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: manual Feishu permission self-check failed")
			notice = "权限检测暂时失败，请稍后重试；现有能力状态未被改写。"
		} else if b.capabilityPermissionIssue(capabilityFeishuIntegration) != nil {
			notice = "检测完成，仍有权限需要补齐并发布应用版本。"
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
		notice := "已向管理员发起权限申请；免审权限立即生效，需审核权限在管理员审核通过后自动恢复。"
		var card string
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: apply Feishu scopes failed")
			notice = feishuScopeApplyErrorNotice(err)
			if isFeishuScopeApplyCode(err, 212002) {
				auditCtx, auditCancel := context.WithTimeout(context.Background(), 15*time.Second)
				auditErr := b.auditFeishuPermissions(auditCtx, false)
				auditCancel()
				if auditErr == nil && b.capabilityPermissionIssue(capabilityFeishuIntegration) == nil {
					notice = "重新检测完成，当前已开启能力所需权限均已满足。"
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
	open := managementButton{Text: "打开设置", Type: "default", Action: capability.OpenAction, Name: "amux.capability." + capability.ID + ".open"}
	return []cardElement{summary, managementButtonRowElement([]managementButton{open})}
}
func renderBotToBotCard(state managementState) string {
	return renderBotToBotCardWithNotice(state, "")
}
func renderBotToBotCardWithNotice(state managementState, notice string) string {
	status := "已关闭"
	actionText := "开启智能体协作"
	actionType := "primary"
	target := "true"
	if state.BotToBot.Enabled {
		actionText = "关闭智能体协作"
		actionType = "default"
		target = "false"
		if state.Mode == config.FeishuInteractionThread {
			status = "已开启"
		} else {
			status = "切换到每个话题独立后生效"
		}
	}
	maxTurns := state.BotToBot.MaxTurnsPerThread
	if maxTurns < 1 {
		maxTurns = 10
	}
	elements := []cardElement{managementSubpageBackElement("返回接入能力", managementActionCapabilitiesOpen, "amux.bot_to_bot.back.capabilities"), managementDividerElement(), managementKeyValueListElement("amux_bot_to_bot_summary", managementSummaryItem{Title: "当前状态", Body: status}, managementSummaryItem{Title: "执行方式", Body: "单任务串行"}, managementSummaryItem{Title: "每话题最多", Body: fmt.Sprintf("%d 轮", maxTurns)}), managementSectionTitleElement("如何使用"), managementVerticalSummaryElement("amux_bot_to_bot_usage", managementTextElement("开启后，在「每个话题独立」且允许群成员使用的群中，其他智能体可以 @当前智能体 交接任务；经用户明确要求或确认后，当前智能体也可以调用群内智能体。发送前会再次确认目标仍在群中。", "normal", "default", nil))}
	if strings.TrimSpace(notice) != "" {
		elements = append(elements, managementHintElement(truncateCardText(notice, 240)))
	}
	elements = append(elements, managementButtonRowElement([]managementButton{{Text: actionText, Type: actionType, Action: managementActionBotToBotSet, Name: "amux.bot_to_bot.enabled.set", Values: map[string]string{"enabled": target}}}), managementHintElement("以上设置仅影响当前智能体"))
	return marshalNativeManagementCard(managementCardView{Title: "智能体 @ 智能体", Summary: "amux 智能体协作设置", Theme: "blue", Icon: managementIcon("robot_outlined", "blue"), Elements: elements})
}
func renderDocumentCommentsCardWithNotice(state managementState, notice string) string {
	status := "已关闭"
	actionText := "开启云文档评论"
	actionType := "primary"
	target := "true"
	if state.DocumentCommentsEnabled {
		status = "已开启"
		actionText = "关闭云文档评论"
		actionType = "default"
		target = "false"
	}
	executionMode := "继承助手默认"
	agentName := managementDefaultAgentName(state)
	executionDraft := managementDocumentCommentsDraftFromState(state)
	runtimeName := managementRuntimeLabel(state, executionDraft.RuntimeRef)
	modelName := managementRuntimeModelLabel(executionDraft.ModelRef)
	reasoningName := managementReasoningEffortLabel(executionDraft.ReasoningEffort)
	if state.DocumentCommentsAgentRef != "" {
		executionMode = "自定义"
		agentName = managementProfileNameByRef(state, state.DocumentCommentsAgentRef)
	}
	elements := []cardElement{managementSubpageBackElement("返回接入能力", managementActionCapabilitiesOpen, "amux.document_comments.back.capabilities"), managementDividerElement(), managementKeyValueListElement("amux_document_comments_summary", managementSummaryItem{Title: "当前状态", Body: status}, managementSummaryItem{Title: "任务配置", Body: executionMode}, managementSummaryItem{Title: "专家", Body: agentName}, managementSummaryItem{Title: "运行引擎", Body: runtimeName}, managementSummaryItem{Title: "模型", Body: modelName}, managementSummaryItem{Title: "思考强度", Body: reasoningName}), managementSectionTitleElement("如何使用"), managementVerticalSummaryElement("amux_document_comments_usage", managementTextElement("负责人在云文档评论中 @当前智能体 并提出要求，即可开始处理该评论话题。", "normal", "default", nil))}
	if strings.TrimSpace(notice) != "" {
		elements = append(elements, managementHintElement(truncateCardText(notice, 240)))
	}
	if state.DocumentPermission != nil {
		elements = append(elements, managementTextElement(capabilityPermissionNotice(*state.DocumentPermission), "normal", "orange", managementIconPtr("warning_outlined", "orange")))
		var permissionButtons []managementButton
		if len(state.DocumentPermission.Scopes) > 0 {
			permissionButtons = append(permissionButtons, managementButton{Text: "申请权限", Type: "primary", Action: managementActionPermissionApply, Name: "amux.capabilities.permissions.apply", Confirm: managementConfirm("向企业管理员申请这些权限？", "应用会向租户管理员发起权限授权申请；审核生效后能力自动恢复，可能需要发布应用版本。")})
		}
		if state.DocumentPermission.GrantURL != "" {
			permissionButtons = append(permissionButtons, managementButton{Text: "开发者后台", Type: "default", URL: state.DocumentPermission.GrantURL})
		}
		elements = append(elements, managementButtonRowElement(permissionButtons))
	}
	executionActionType := "primary"
	if !state.DocumentCommentsEnabled {
		executionActionType = "default"
	}
	elements = append(elements, managementButtonRowElement([]managementButton{{Text: "修改任务配置", Type: executionActionType, Action: managementActionDocumentCommentsEdit}, {Text: actionText, Type: actionType, Action: managementActionDocumentCommentsSet, Values: map[string]string{"enabled": target}}}), managementHintElement("以上设置仅影响当前智能体"))
	return marshalNativeManagementCard(managementCardView{Title: "云文档评论", Summary: "amux 云文档评论设置", Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: elements})
}
func renderDocumentCommentsEditCard(state managementState, draft managementDocumentCommentsDraft) string {
	draft = managementNormalizeDocumentCommentsDraft(state, draft)
	changeBehavior := func(field string) []cardBehavior {
		return []cardBehavior{{Type: "callback", Value: managementButtonValue(managementButton{Action: managementActionDocumentCommentsEdit, Values: map[string]string{"document_comments_mode": draft.Mode, "document_comments_agent": draft.AgentRef, "document_comments_runtime_ref": draft.RuntimeRef, "document_comments_model_ref": draft.ModelRef, "document_comments_reasoning_effort": draft.ReasoningEffort, managementDraftFieldKey: field}})}}
	}
	modeField := managementSelectStaticElement("document_comments_mode", "选择任务配置", draft.Mode, []cardOption{{Text: cardTitle{Tag: "plain_text", Content: "继承助手默认"}, Value: managementDocumentCommentsModeInherit}, {Text: cardTitle{Tag: "plain_text", Content: "自定义"}, Value: managementDocumentCommentsModeCustom}})
	modeField.Required = false
	modeField.Behaviors = changeBehavior("document_comments_mode")
	elements := []cardElement{managementHintElement("保存后仅影响新建评论话题；已有评论话题继续使用创建时的专家和执行参数。"), managementSettingsFieldRow("任务配置", modeField)}
	if draft.Mode == managementDocumentCommentsModeCustom {
		profileOptions := managementProfileOptions(state, false)
		profileField := managementHintElement("专家库中暂无可用专家，请先添加专家。")
		if len(profileOptions) > 0 {
			profileField = managementSelectStaticElement("document_comments_agent", "选择云文档评论使用的专家", draft.AgentRef, profileOptions)
			profileField.Required = false
			profileField.Behaviors = changeBehavior("document_comments_agent")
		}
		elements = append(elements, managementSettingsFieldRow("专家", profileField))
		runtimeField := managementSelectStaticElement("document_comments_runtime_ref", "选择运行引擎", draft.RuntimeRef, managementRuntimeOptions(state))
		runtimeField.Required = false
		runtimeField.Behaviors = changeBehavior("document_comments_runtime_ref")
		elements = append(elements, managementSettingsFieldRow("运行引擎（仅影响新评论话题）", runtimeField))
		if draft.RuntimeRef != "" {
			initialModelRef := draft.ModelRef
			if initialModelRef == "" {
				initialModelRef = managementProfileDefaultModelOption
			}
			modelField := managementSelectStaticElement("document_comments_model_ref", "选择该运行引擎支持的模型", initialModelRef, managementRuntimeModelOptions(state, draft.RuntimeRef))
			modelField.Required = false
			modelField.Behaviors = changeBehavior("document_comments_model_ref")
			elements = append(elements, managementSettingsFieldRow("模型（仅影响新评论话题）", modelField))
			initialReasoningEffort := draft.ReasoningEffort
			if initialReasoningEffort == "" {
				initialReasoningEffort = managementRuntimeDefaultReasoningOption
			}
			reasoningField := managementSelectStaticElement("document_comments_reasoning_effort", "选择思考强度", initialReasoningEffort, managementRuntimeReasoningOptions(state, draft.RuntimeRef, draft.ModelRef))
			reasoningField.Required = false
			reasoningField.Behaviors = changeBehavior("document_comments_reasoning_effort")
			elements = append(elements, managementSettingsFieldRow("思考强度（仅影响新评论话题）", reasoningField))
		}
	}
	submit := managementFormSubmitButton(managementActionDocumentCommentsSave, "保存")
	submit.Value, _ = json.Marshal(managementButtonValue(managementButton{Action: managementActionDocumentCommentsSave, Values: map[string]string{"document_comments_mode": draft.Mode, "document_comments_agent": draft.AgentRef, "document_comments_runtime_ref": draft.RuntimeRef, "document_comments_model_ref": draft.ModelRef, "document_comments_reasoning_effort": draft.ReasoningEffort}}))
	buttons := managementFormButtonRowElement(submit, managementCallbackButton(managementActionDocumentCommentsOpen, "取消", "default"))
	buttons.Margin = "4px 0px 0px 0px"
	elements = append(elements, cardElement{Tag: "form", Name: "document_comments_form", Direction: "vertical", VerticalSpacing: "8px", Elements: []cardElement{buttons}})
	return marshalNativeManagementCard(managementCardView{Title: "修改评论任务配置", Subtitle: "设置新评论话题使用的专家和执行参数", Summary: "修改云文档评论任务配置", Theme: "blue", Icon: managementIcon("edit_outlined", "blue"), Elements: elements})
}
func managementSettingsFieldRow(label string, field cardElement) cardElement {
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
func managementDocumentCommentsDraftFromCallback(action *callback.
	CallBackAction, state managementState) managementDocumentCommentsDraft {
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
		draft.Mode = strings.TrimSpace(action.
			Option)
	case "document_comments_agent":
		draft.AgentRef = strings.TrimSpace(action.Option)
	case "document_comments_runtime_ref":
		draft.RuntimeRef = strings.TrimSpace(action.Option)
	case "document_comments_model_ref":
		draft.ModelRef = managementModelRefFromForm(action.
			Option)
	case "document_comments_reasoning_effort":
		draft.ReasoningEffort = managementReasoningEffortFromForm(action.Option)
	}
	return managementNormalizeDocumentCommentsDraft(state, draft)
}
func managementDocumentCommentsCallbackValue(action *callback.
	CallBackAction, key string) (string, bool) {
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
func managementDocumentCommentsSubmissionError(action *callback.CallBackAction, state managementState) string {
	mode, _ := managementDocumentCommentsCallbackValue(action, "document_comments_mode")
	switch mode {
	case managementDocumentCommentsModeInherit:
		return ""
	case managementDocumentCommentsModeCustom:
	default:
		return "请选择云文档评论的任务配置"

	}
	agentRef, _ :=
		managementDocumentCommentsCallbackValue(action, "document_comments_agent")
	if !managementValidDocumentCommentsAgent(
		state, agentRef) {
		return "该专家已不在专家库中，请刷新后重新选择"
	}
	runtimeRef, _ := managementDocumentCommentsCallbackValue(action, "document_comments_runtime_ref")
	if !managementValidRuntime(state, runtimeRef) {
		return "请选择云文档评论使用的运行引擎"
	}
	modelValue, _ := managementDocumentCommentsCallbackValue(action, "document_comments_model_ref")
	modelRef := managementModelRefFromForm(modelValue)
	if !managementValidRuntimeModel(state, runtimeRef, modelRef) {
		return "该模型不在所选运行引擎的支持列表中，请重新选择"
	}
	reasoningValue, _ := managementDocumentCommentsCallbackValue(action, "document_comments_reasoning_effort")
	reasoningEffort := managementReasoningEffortFromForm(reasoningValue)
	if !managementValidRuntimeReasoningEffort(state, runtimeRef, modelRef, reasoningEffort) {
		return "该思考强度不受所选运行引擎和模型支持，请重新选择"
	}
	return ""
}
func managementNormalizeDocumentCommentsDraft(state managementState, draft managementDocumentCommentsDraft) managementDocumentCommentsDraft {
	if draft.Mode !=
		managementDocumentCommentsModeCustom {
		draft.Mode = managementDocumentCommentsModeInherit
	}
	if draft.Mode == managementDocumentCommentsModeInherit {
		if !managementValidDocumentCommentsAgent(state, draft.
			AgentRef) {
			draft.
				AgentRef = managementFirstDocumentCommentsAgent(state)
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
		draft.
			AgentRef,
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
	if managementValidDocumentCommentsAgent(state, state.
		DefaultAgent,
	) {
		return state.DefaultAgent
	}
	for _, profile := range state.Profiles {
		if !profile.CurrentOnly {
			return profile.
				Ref
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
	if action == nil {
		return managementNormalizeGroupWatchDraft(state, draft)
	}
	if value, ok := managementDocumentCommentsCallbackValue(action,
		"group_watch_mode"); ok {
		draft.
			Mode = value
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
	case "group_watch_mode":
		draft.
			Mode = strings.TrimSpace(action.Option)
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
		"group_watch_agent", "group_watch_runtime_ref", "group_watch_model_ref", "group_watch_reasoning_effort", "group_watch_thinking_process",
	} {
		if strings.TrimSpace(action.
			Name) == field {
			return field
		}
	}
	if field, ok := action.Value[managementDraftFieldKey].(string); ok {
		return strings.TrimSpace(field)
	}
	return ""
}
func managementGroupWatchSubmissionError(action *callback.
	CallBackAction, state managementState) string {
	thinkingProcess, _ := managementDocumentCommentsCallbackValue(action, "group_watch_thinking_process")
	if !managementValidThinkingProcess(thinkingProcess) {
		return "请选择群值守的处理过程展示方式"
	}
	mode, _ := managementDocumentCommentsCallbackValue(action, "group_watch_mode")
	switch mode {
	case managementDocumentCommentsModeInherit:
		return ""
	case managementDocumentCommentsModeCustom:
	default:
		return "请选择群值守的任务配置"
	}
	agentRef, _ := managementDocumentCommentsCallbackValue(action, "group_watch_agent")
	if !managementValidDocumentCommentsAgent(state, agentRef) {
		return "该专家已不在专家库中，请刷新后重新选择"
	}
	runtimeRef, _ := managementDocumentCommentsCallbackValue(action, "group_watch_runtime_ref")
	if !managementValidRuntime(state, runtimeRef) {
		return "请选择群值守使用的运行引擎"
	}
	modelValue, _ := managementDocumentCommentsCallbackValue(action, "group_watch_model_ref")
	modelRef := managementModelRefFromForm(modelValue)
	if !managementValidRuntimeModel(state, runtimeRef, modelRef) {
		return "该模型不在所选运行引擎的支持列表中，请重新选择"
	}
	reasoningValue, _ := managementDocumentCommentsCallbackValue(action, "group_watch_reasoning_effort")
	reasoningEffort := managementReasoningEffortFromForm(reasoningValue)
	if !managementValidRuntimeReasoningEffort(state, runtimeRef, modelRef, reasoningEffort) {
		return "该思考强度不受所选运行引擎和模型支持，请重新选择"
	}
	return ""
}
func managementNormalizeGroupWatchDraft(state managementState, draft managementGroupWatchDraft) managementGroupWatchDraft {
	if !managementValidThinkingProcess(draft.ThinkingProcess) {
		draft.ThinkingProcess = state.GroupWatch.ThinkingProcess
	}
	if !managementValidThinkingProcess(draft.ThinkingProcess) {
		draft.ThinkingProcess = config.DefaultFeishuThinkingProcess
	}
	if draft.
		Mode != managementDocumentCommentsModeCustom {
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
	action := "开启"
	if !enabled {
		action = "关闭"
	}
	return marshalNativeManagementCard(managementCardView{Title: "正在" +
		action + "云文档评论",
		Subtitle: "只影响当前智能体",
		Summary:  "正在更新云文档评论设置",

		Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementHintElement("正在检查飞书权限并保存设置。")}})
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
		libraryAction = &managementButton{Text: "专家库", Type: "default", Action: managementActionPackLibraryOpen}
	}
	elements := managementSettingsEditorElements(state,
		"settings_form", draft, managementActionSettingsSave,
		"保存", managementActionSettingsCancel,
		libraryAction, managementMessageHandlingHintElement())
	return marshalNativeManagementCard(managementCardView{Title: "修改默认配置", Subtitle: "保存后生效", Summary: "修改 amux 助手默认配置", Theme: "blue", Icon: managementIcon("edit_outlined", "blue"), Elements: elements})
}

func renderSettingsConfirmationCard(state managementState, draft managementSettingsDraft, req BotSettingsRequest, result BotSettingsResult) string {
	draft = managementNormalizeDraftForState(state, draft)
	values := map[string]string{"interaction": req.Interaction, "default_agent": req.DefaultAgent,
		"default_runtime_ref": req.DefaultRuntimeRef, "default_model_ref": req.DefaultModelRef,
		"default_reasoning_effort": req.DefaultReasoningEffort,
		"thinking_process":         draft.ThinkingProcess,
		"expected_version":         strconv.FormatInt(req.ExpectedVersion, 10), "request_id": req.RequestID,
		"confirmation_token": result.ConfirmationToken}
	title := "确认更换默认专家"
	subtitle := "当前对话和工作区将继续保留"
	confirmText := "确认更换"
	summary := managementCompactSummaryElement(managementSummaryItem{Title: "当前默认专家",

		Body: managementDefaultAgentName(state)}, managementSummaryItem{Title: "新的默认专家", Body: managementProfileNameByRef(state, draft.DefaultAgent)}, managementSummaryItem{Title: "使用模型", Body: managementEffectiveModelLabel(state, draft.DefaultAgent, draft.DefaultModelRef)})
	if state.DefaultAgent == draft.DefaultAgent && state.DefaultModelRef != draft.
		DefaultModelRef {
		title = "确认更换默认模型"
		summary = managementCompactSummaryElement(managementSummaryItem{Title: "当前默认模型", Body: managementEffectiveModelLabel(state, state.DefaultAgent, state.DefaultModelRef)}, managementSummaryItem{Title: "新的默认模型", Body: managementEffectiveModelLabel(state, draft.DefaultAgent,
			draft.DefaultModelRef)})
	}
	if state.Mode != config.FeishuInteractionSingleton && req.Interaction == config.FeishuInteractionSingleton && state.DefaultAgent == req.DefaultAgent && state.DefaultModelRef == req.DefaultModelRef {
		title = "确认重新创建连续对话"
		subtitle = "原对话上下文无法恢复"

		confirmText = "确认创建"
		summary = managementCompactSummaryElement(managementSummaryItem{Title: "当前处理方式", Body: managementInteractionText(state.Mode)}, managementSummaryItem{Title: "切换为", Body: managementInteractionText(req.Interaction)})
	}
	elements := []cardElement{summary, managementHintElement(result.ImpactSummary)}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: subtitle, Summary: "需要确认助手设置", Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: elements, Buttons: []managementButton{{Text: confirmText, Type: "primary", Action: managementActionSettingsConfirm, Values: values}, {Text: "取消", Type: "default", Action: managementActionSettingsCancel}}})
}
func managementEffectiveModelLabel(state managementState, agentRef, ref string) string {
	if ref = strings.TrimSpace(ref); ref != "" {
		return ref
	}
	defaultModel := managementDefaultModelDisplayFor(state, agentRef)
	if defaultModel.Unavailable {
		return "专家不可用，无法确定默认模型"
	}
	if model := defaultModel.ProfileModel; model != "" {
		return "专家默认 · " + model
	}
	if agentName := defaultModel.AgentName; agentName != "" {
		return agentName + " 默认模型"
	}
	return "专家默认模型"
}
func managementProfileDefaultModelOptionLabel(state managementState, agentRef string) string {
	defaultModel := managementDefaultModelDisplayFor(state, agentRef)
	if defaultModel.Unavailable {
		return "专家不可用，无法确定默认模型"
	}
	if model := defaultModel.ProfileModel; model != "" {
		return "使用专家默认模型（" + model + "）"
	}
	if agentName := defaultModel.AgentName; agentName != "" {
		return "使用 " + agentName + " 默认模型"
	}
	return "使用专家默认模型"
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
func renderSettingsRecoveryChoiceCard(state managementState, draft managementSettingsDraft, req BotSettingsRequest, result BotSettingsResult, notice string) string {
	draft = managementNormalizeDraftForState(state, draft)
	values := managementBotSettingsRecoveryValues(req, draft.ThinkingProcess, result.ConfirmationToken)
	elements := []cardElement{managementHintElement("当前仍为「每个话题独立」，没有修改现有设置。"), managementHintElement(valueOrFallback(result.ImpactSummary, "原连续对话和项目目录均不可用，请选择恢复方式。"))}
	if strings.TrimSpace(notice) != "" {
		elements = append(elements, managementHintElement(notice))
	}
	return marshalNativeManagementCard(managementCardView{Title: "无法恢复原连续对话", Subtitle: "请选择如何继续", Summary: "原连续对话无法恢复", Theme: "orange", Icon: managementIcon("warning_outlined", "orange"), Elements: elements, Buttons: []managementButton{{Text: "新建空白连续对话", Type: "primary", Action: managementActionSettingsRecoveryBlank, Values: values, Confirm: managementConfirm("新建空白连续对话？", "新工作区不包含原项目文件和历史上下文。")}, {Text: "绑定已有项目", Type: "default", Action: managementActionSettingsRecoveryBind, Values: values}, {Text: "暂不处理", Type: "default", Action: managementActionSettingsRecoveryDismiss, Values: values}}})
}
func managementBotSettingsRecoveryValues(req BotSettingsRequest, thinkingProcess, token string) map[string]string {
	return map[string]string{"interaction": req.Interaction, "default_agent": req.DefaultAgent,
		"default_runtime_ref": req.DefaultRuntimeRef, "default_model_ref": req.DefaultModelRef,
		"default_reasoning_effort": req.DefaultReasoningEffort,
		"thinking_process":         thinkingProcess,
		"expected_version":         strconv.FormatInt(req.ExpectedVersion, 10), "request_id": req.RequestID,
		"confirmation_token": token}
}
func renderSettingsManualBindCard(state managementState, req BotSettingsRequest, thinkingProcess, token, notice string) string {
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
	elements := []cardElement{managementHintElement("在运行 amux 的机器上进入项目目录，执行："), managementTextElement(command, "notation", "default", nil)}
	if strings.TrimSpace(notice) != "" {
		elements = append(elements, managementHintElement(notice))
	}
	values := managementBotSettingsRecoveryValues(req, thinkingProcess, token)
	return marshalNativeManagementCard(managementCardView{Title: "绑定已有项目", Subtitle: "绑定完成前仍使用「每个话题独立」", Summary: "绑定已有项目", Theme: "blue", Icon: managementIcon("link_outlined", "blue"), Elements: elements, Buttons: []managementButton{{Text: "检查绑定结果", Type: "primary", Action: managementActionSettingsRecoveryCheck, Values: values}, {Text: "返回恢复选项", Type: "default", Action: managementActionSettingsRecoveryBack, Values: values}}})
}
func renderSettingsProcessingCard(state managementState, draft managementSettingsDraft) string {
	draft = managementNormalizeDraftForState(state, draft)
	title := "正在保存默认配置"
	subtitle := "完成后自动刷新"
	if draft.Interaction == config.FeishuInteractionSingleton {
		title = "正在准备新的连续对话"
		if state.Mode == config.FeishuInteractionSingleton {
			subtitle = "当前对话仍可使用；准备完成后自动切换"
		}
	}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: subtitle, Summary: title, Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementMessageHandlingSummary, Body: managementInteractionText(draft.Interaction)}, managementSummaryItem{Title: "默认专家", Body: managementProfileNameByRef(state, draft.DefaultAgent)}, managementSummaryItem{Title: "默认模型", Body: managementEffectiveModelLabel(state, draft.DefaultAgent, draft.DefaultModelRef)})}})
}
func renderSettingsBlankRecoveryProcessingCard(state managementState, draft managementSettingsDraft) string {
	draft = managementNormalizeDraftForState(state, draft)
	return marshalNativeManagementCard(managementCardView{Title: "正在新建空白连续对话", Subtitle: "创建完成后自动切换", Summary: "正在新建空白连续对话", Theme: "blue", Icon: managementIcon("refresh_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementMessageHandlingSummary, Body: managementInteractionText(draft.Interaction)}, managementSummaryItem{Title: "默认专家", Body: managementProfileNameByRef(state, draft.DefaultAgent)}, managementSummaryItem{Title: "默认模型", Body: managementEffectiveModelLabel(state, draft.DefaultAgent, draft.DefaultModelRef)})}})
}
func renderSettingsFailureCardForSurface(state managementState, draft managementSettingsDraft, transitionErr error, surface managementSurface) string {
	draft = managementNormalizeDraftForState(state, draft)
	message := managementSettingsFailureMessage(transitionErr)
	var libraryAction *managementButton
	if state.CanManagePacks && surface != managementSurfaceAmux {
		libraryAction = &managementButton{Text: "专家库", Type: "default", Action: managementActionPackLibraryOpen}
	}
	elements := []cardElement{managementHintElement(message), managementProfileLibraryHintElement(state)}
	elements = append(elements, managementSettingsEditorElements(state, "settings_form", draft, managementActionSettingsSave, "重试", managementActionSettingsCancel, libraryAction, managementTextElement("", "notation", "grey", nil))...)
	return marshalNativeManagementCard(managementCardView{Title: "保存失败", Subtitle: "设置未更改，可重新尝试", Summary: "amux 助手设置失败", Theme: "red", Icon: managementIcon("warning_outlined", "red"), Elements: elements})
}

func managementSettingsFailureMessage(transitionErr error) string {
	if errors.Is(transitionErr, errBotProfileStateChanged) {
		return "配置已发生变化，已刷新为最新状态，请重新选择。"
	}
	if transitionErr == nil {
		return "失败原因：未返回具体错误。当前设置没有改变。"
	}
	reason := strings.TrimSpace(transitionErr.Error())
	if errors.Is(transitionErr, errBotSettingsBlankRecoveryFailed) {
		reason = strings.TrimSpace(strings.TrimPrefix(reason, errBotSettingsBlankRecoveryFailed.Error()))
		reason = strings.TrimSpace(strings.TrimPrefix(reason, ":"))
	}
	reason = strings.Join(strings.Fields(reason), " ")
	reason = subtaskSecretAssignmentRE.ReplaceAllString(reason, `${1}${2}[REDACTED]`)
	reason = subtaskBearerRE.ReplaceAllString(reason, "Bearer [REDACTED]")
	reason = subtaskSecretTokenRE.ReplaceAllString(reason, "[REDACTED]")
	if reason == "" {
		reason = "未返回具体错误"
	}
	const maxReasonRunes = 240
	return "失败原因：" + truncateCardText(reason, maxReasonRunes) + "。当前设置没有改变。"
}

type managementProfileSummary struct {
	Ref             string
	Name            string
	LibraryName     string
	Description     string
	AgentType       string
	Model           string
	SupportedModels []string
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

func managementRuntimeLabel(state managementState, runtimeRef string) string {
	runtime, ok := managementRuntimeByRef(state, runtimeRef)
	if !ok {
		return valueOrPlaceholder(strings.TrimSpace(runtimeRef), "未选择")
	}
	if label := strings.TrimSpace(runtime.DisplayName); label != "" {
		return label
	}
	return runtime.Ref
}
func managementRuntimeModelLabel(modelRef string) string {
	if modelRef = strings.TrimSpace(modelRef); modelRef != "" {
		return modelRef
	}
	return "运行引擎默认模型"
}
func managementReasoningEffortLabel(effort string) string {
	if effort = strings.TrimSpace(effort); effort != "" {
		return effort
	}
	return "运行引擎默认强度"
}

type managementDocumentCommentsDraft struct {
	Mode            string
	AgentRef        string
	RuntimeRef      string
	ModelRef        string
	ReasoningEffort string
}
type managementGroupWatchDraft struct {
	Mode            string
	AgentRef        string
	RuntimeRef      string
	ModelRef        string
	ReasoningEffort string
	ThinkingProcess string
}

type managementCapabilityView struct {
	ID         string
	Title      string
	Status     string
	Summary    string
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
		managementCardView{Title: "正在保存评论任务配置", Subtitle: "只影响之后创建的评论话题",
			Summary: "正在保存云文档评论任务配置",
			Theme:   "blue",

			Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementHintElement("正在检查专家和执行参数。")}})
}
