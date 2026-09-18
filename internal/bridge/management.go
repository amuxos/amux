package bridge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkapplication "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
)

const (
	managementActionKey     = "amux_action"
	managementSurfaceKey    = "amux_surface"
	managementDraftFieldKey = "amux_draft_field"

	managementActionOnboardingEdit          = "bot.onboarding.edit"
	managementActionOnboardingSubmit        = "bot.onboarding.submit"
	managementActionSettingsOpen            = "bot.settings.open"
	managementActionSettingsEdit            = "bot.settings.edit"
	managementActionSettingsSave            = "bot.settings.save"
	managementActionCapabilitiesOpen        = "bot.capabilities.open"
	managementActionExperimentsOpen         = "system.experiments.open"
	managementActionExperimentsSave         = "system.experiments.save"
	managementActionExperimentsSaveRestart  = "system.experiments.save_restart"
	managementActionPermissionRecheck       = "bot.permissions.recheck"
	managementActionPermissionApply         = "bot.permissions.apply"
	managementActionBotToBotOpen            = "bot.bot_to_bot.open"
	managementActionBotToBotSet             = "bot.bot_to_bot.set"
	managementActionDocumentCommentsOpen    = "bot.document_comments.open"
	managementActionDocumentCommentsSet     = "bot.document_comments.set"
	managementActionDocumentCommentsEdit    = "bot.document_comments.config.edit"
	managementActionDocumentCommentsSave    = "bot.document_comments.config.save"
	managementActionGroupWatchOpen          = "bot.group_watch.open"
	managementActionGroupWatchUpdate        = "bot.group_watch.update"
	managementActionGroupWatchEdit          = "bot.group_watch.config.edit"
	managementActionGroupWatchSave          = "bot.group_watch.config.save"
	managementActionSettingsConfirm         = "bot.settings.confirm"
	managementActionSettingsCancel          = "bot.settings.cancel"
	managementActionSettingsRecoveryBlank   = "bot.settings.recovery.blank"
	managementActionSettingsRecoveryBind    = "bot.settings.recovery.bind"
	managementActionSettingsRecoveryCheck   = "bot.settings.recovery.check"
	managementActionSettingsRecoveryBack    = "bot.settings.recovery.back"
	managementActionSettingsRecoveryDismiss = "bot.settings.recovery.dismiss"
	managementActionPackLibraryOpen         = "agent_pack.library.open"
	managementActionPackLibraryNavigate     = "agent_pack.library.navigate"
	managementActionPackManage              = "agent_pack.manage"
	managementActionPackInstall             = "agent_pack.install"
	managementActionPackSync                = "agent_pack.sync"
	managementActionPackRemove              = "agent_pack.remove"
	managementActionStatusOpen              = "system.status.open"
	managementActionSystemMaintenance       = "system.maintenance.open"
	managementActionSystemRestart           = "system.restart.request"
	managementActionSystemRestartOK         = "system.restart.confirm"
	managementActionSystemUpdate            = "system.update.check"
	managementActionSystemUpdateDismiss     = "system.update.dismiss"
	managementActionSystemUpgrade           = "system.update.install"
	managementActionHelpOpen                = "bot.help.open"

	botMenuSettings = "amux.bot.settings"
	botMenuStatus   = "amux.system.status"
	botMenuHelp     = "amux.help"

	managementMessageHandlingQuestion = "消息如何处理"
	managementMessageHandlingSummary  = "处理方式"

	// interactionModeThreadIsolated is the card-facing composite value for the
	// 每个话题独立(用户隔离) option. It is not a routing mode: it decomposes to
	// (thread, isolation_required=true) at the settings persistence boundary
	// (managementDecomposeInteraction) and recomposes from the DB read, so
	// the thread|singleton routing enum is untouched.
	interactionModeThreadIsolated = "thread_isolated"

	managementBuiltinExpertDescriptionMaxRunes = 32
	managementMarketPackDescriptionMaxRunes    = 96
	managementPackLibraryMaxElements           = 140
	managementPackLibraryMaxBytes              = 20 * 1024
	managementFocusedLibraryElementReserve     = 6
	managementFocusedLibraryByteReserve        = 1024
	managementRecoveryTTL                      = 30 * time.Minute
	managementRecoveryWait                     = 30 * time.Second
	managementRecoveryPollInterval             = time.Second
	managementDocumentCommentsModeInherit      = "inherit"
	managementDocumentCommentsModeCustom       = "custom"
)

type managementSurface string

const (
	managementSurfaceDefault managementSurface = ""
	// Keep the promoted /amux callback wire value compatible with cards sent
	// before the dashboard replaced the fixed navigation.
	managementSurfaceAmux managementSurface = "navigation"
)

type managementView string

const (
	managementViewOverview managementView = "overview"
	managementViewSettings managementView = "settings"
	managementViewLibrary  managementView = "library"
	managementViewSystem   managementView = "system"
	managementViewHelp     managementView = "help"
)

type managementSystemStatus string

const (
	managementSystemHealthy   managementSystemStatus = "healthy"
	managementSystemAttention managementSystemStatus = "attention"
)

func (b *Bridge) handleP2PChatEntered(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
	if event == nil || event.Event == nil || event.Event.OperatorId == nil {
		return nil
	}
	openID := stringPtr(event.Event.OperatorId.OpenId)
	if openID == "" {
		return nil
	}
	firstOwnerCapture := b.secrets != nil && strings.TrimSpace(b.secrets.OpenId) == ""
	if b.secrets != nil && strings.TrimSpace(b.secrets.OpenId) == "" {
		b.secrets.OpenId = openID
		_ = b.saveProfileSecrets()
		b.triggerFeishuPermissionAudit(ctx)
	}
	if !b.canUseBridge(openID) {
		_, _, err := b.notifyDM(ctx, openID, privateWorkspaceBlockedText())
		return err
	}
	if !firstOwnerCapture {
		return nil
	}
	if b.onboardingCardAlreadySent(openID) {
		return nil
	}
	return b.sendManagementCardToDM(ctx, openID, renderOnboardingCard(b.managementState()))
}

func (b *Bridge) handleBotMenu(ctx context.Context, event *larkapplication.P2BotMenuV6) error {
	if event == nil || event.Event == nil || event.Event.Operator == nil {
		return nil
	}
	openID := stringPtr(event.Event.Operator.OperatorId.OpenId)
	if openID == "" {
		return nil
	}
	key := strings.TrimSpace(stringPtr(event.Event.EventKey))
	switch key {
	case botMenuSettings, "settings", "assistant_settings":
		return b.sendOwnerManagementCard(ctx, openID, renderSettingsEntryCard(b.managementState()))
	case botMenuStatus, "amux.status", "status":
		return b.sendOwnerManagementCard(ctx, openID, renderSystemStatusCard(b.managementState()))
	case botMenuHelp, "help", "usage_help":
		return b.sendOwnerManagementCard(ctx, openID, renderManagementHelpCard(b.managementState()))
	default:
		return b.sendOwnerManagementCard(ctx, openID, renderManagementHelpCard(b.managementState()))
	}
}

func (b *Bridge) handleManagementCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool, error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, false, nil
	}
	action := managementActionFromCallback(event.Event.Action)
	if action == "" {
		return nil, false, nil
	}
	if response, handled, err := b.handleGroupSettingsCardAction(ctx, event, action); handled {
		return response, true, err
	}
	openID := ""
	if event.Event.Operator != nil {
		openID = event.Event.Operator.OpenID
	}
	if !b.ownerConfirmed() || !b.canUseBridge(openID) {
		return cardToast("error", "你没有权限管理这个智能体"), true, nil
	}
	messageID := ""
	if event.Event.Context != nil {
		messageID = strings.TrimSpace(event.Event.Context.OpenMessageID)
	}
	if action != managementActionPermissionRecheck {
		b.invalidateFeishuPermissionRecheck(messageID)
	}
	state := b.managementState()
	surface := managementSurfaceFromCallback(event.Event.Action)
	updateCard := func(card string, page managementView) {
		b.updateManagementCardFromCallback(
			openID,
			messageID,
			renderManagementCardForSurface(card, surface, page),
		)
	}
	updateFocusedCard := func(card string) {
		b.updateManagementCardFromCallback(
			openID,
			messageID,
			renderFocusedManagementCardForSurface(card, surface),
		)
	}
	settingsEditCard := func(current managementState, draft managementSettingsDraft) string {
		return renderSettingsEditCardForSurface(current, draft, surface)
	}
	updateSettingsEditCard := func(current managementState, draft managementSettingsDraft) {
		card := settingsEditCard(current, draft)
		updateFocusedCard(card)
	}
	settingsSummaryCard := func(current managementState) string {
		return renderSettingsCardForSurface(current, surface)
	}
	capabilitiesCard := func(current managementState) string {
		return renderCapabilitiesCard(current)
	}
	documentCommentsCard := func(current managementState, notice string) string {
		return renderDocumentCommentsCardWithNotice(current, notice)
	}
	systemMaintenanceCard := func(current managementState) string {
		return renderSystemMaintenanceCardForSurface(current, surface)
	}
	overviewCard := func(current managementState) string {
		if surface == managementSurfaceAmux {
			return renderManagementOverviewCard(current)
		}
		return renderSystemStatusCard(current)
	}
	switch action {
	case managementActionExperimentsOpen, managementActionExperimentsSave, managementActionExperimentsSaveRestart:
		saved, err := config.ReadExperimentsSettings()
		if err != nil {
			return cardToast("error", "无法读取实验功能配置，请检查配置文件"), true, nil
		}
		if action != managementActionExperimentsOpen {
			update, err := managementExperimentsSettingsFromCallback(event.Event.Action, saved)
			if err != nil {
				return cardToast("error", err.Error()), true, nil
			}
			saved, err = config.UpdateExperimentsSettings(update)
			if err != nil {
				return cardToast("error", "实验功能保存失败，请检查配置文件"), true, nil
			}
		}
		if action == managementActionExperimentsSaveRestart {
			updateFocusedCard(renderManagementExperimentsRestartConfirmationCard(state))
			return cardToast("success", "已保存，请确认重启"), true, nil
		}
		updateFocusedCard(renderManagementExperimentsCard(saved, config.GetExperimentsSettings()))
		if action == managementActionExperimentsSave {
			return cardToast("success", "已保存，重启 amux 后生效"), true, nil
		}
		return cardToast("success", "已打开实验功能"), true, nil
	case managementActionOnboardingEdit:
		updateFocusedCard(renderOnboardingDraftCard(state, managementDraftFromCallback(event.Event.Action, state)))
		return cardToast("success", "已更新配置选择"), true, nil
	case managementActionOnboardingSubmit:
		if message := managementProfileSubmissionError(event.Event.Action, state); message != "" {
			updateFocusedCard(renderOnboardingDraftCard(state, managementDraftFromState(state)))
			return cardToast("error", message), true, nil
		}
		if message := managementModelSubmissionError(event.Event.Action, state); message != "" {
			updateFocusedCard(renderOnboardingDraftCard(state, managementDraftFromState(state)))
			return cardToast("error", message), true, nil
		}
		if message := managementRuntimeSubmissionError(event.Event.Action, state); message != "" {
			updateFocusedCard(renderOnboardingDraftCard(state, managementDraftFromState(state)))
			return cardToast("error", message), true, nil
		}
		if message := managementReasoningSubmissionError(event.Event.Action, state); message != "" {
			updateFocusedCard(renderOnboardingDraftCard(state, managementDraftFromState(state)))
			return cardToast("error", message), true, nil
		}
		draft := managementDraftFromCallback(event.Event.Action, state)
		if draft.Interaction != state.Mode {
			req := managementBotSettingsRequest(state, draft, messageID, openID)
			b.runManagementSettingsTransition(openID, messageID, state, draft, req, true, surface)
			return cardToast("success", "正在准备助手，请稍候"), true, nil
		}
		if managementUsesThreadRouting(state.Mode) {
			updated, err := b.commitThreadManagementSettings(state, draft)
			if err != nil {
				if errors.Is(err, errBotProfileStateChanged) {
					updated = b.managementState()
					updateFocusedCard(renderOnboardingDraftCard(updated, managementDraftFromState(updated)))
					return cardToast("warning", "配置已变化，请重新确认"), true, nil
				}
				return cardToast("error", managementSettingsFailureMessage(err)), true, nil
			}
			state = updated
		}
		if draft.ThinkingProcess != state.ThinkingProcess {
			if err := b.saveManagementThinkingProcess(draft.ThinkingProcess); err != nil {
				return cardToast("error", managementSettingsFailureMessage(err)), true, nil
			}
			state = b.managementState()
		}
		draft = managementDraftFromState(state)
		updateCard(renderOnboardingCompleteCard(state, draft), managementViewSettings)
		return cardToast("success", "助手配置已保存"), true, nil
	case managementActionSettingsOpen:
		if surface == managementSurfaceAmux {
			updateCard(settingsSummaryCard(state), managementViewSettings)
		} else {
			updateSettingsEditCard(state, managementDraftFromState(state))
		}
		return cardToast("success", "已打开助手设置"), true, nil
	case managementActionSettingsEdit:
		updateSettingsEditCard(state, managementDraftFromCallback(event.Event.Action, state))
		return cardToast("success", "已更新配置选择"), true, nil
	case managementActionCapabilitiesOpen:
		updateFocusedCard(capabilitiesCard(state))
		return cardToast("success", "已打开接入能力"), true, nil
	case managementActionBotToBotOpen:
		updateFocusedCard(renderBotToBotCard(state))
		return cardToast("success", "已打开智能体协作设置"), true, nil
	case managementActionBotToBotSet:
		enabled, err := strconv.ParseBool(managementCallbackString(event.Event.Action, "enabled"))
		if err != nil {
			return cardToast("warning", "无法识别智能体协作设置，请刷新后重试"), true, nil
		}
		if !b.beginManagementBotToBotSettings() {
			return cardToast("warning", "智能体协作设置正在更新，请等待卡片刷新"), true, nil
		}
		b.runManagementBotToBotSettingsUpdate(openID, messageID, enabled, surface)
		if enabled {
			return cardToast("success", "正在开启智能体协作"), true, nil
		}
		return cardToast("success", "正在关闭智能体协作"), true, nil
	case managementActionPermissionRecheck:
		version, ok := b.beginFeishuPermissionRecheck(messageID)
		if !ok {
			return cardToast("warning", "飞书权限正在检测，请稍候"), true, nil
		}
		b.runFeishuPermissionRecheck(openID, messageID, surface, version)
		return cardToast("success", "正在重新检测飞书权限"), true, nil
	case managementActionPermissionApply:
		version, ok := b.beginFeishuPermissionApply(messageID)
		if !ok {
			return cardToast("warning", "权限申请正在进行，请稍候"), true, nil
		}
		b.runFeishuPermissionApply(openID, messageID, surface, version)
		return cardToast("success", "正在向管理员申请权限"), true, nil
	case managementActionGroupWatchOpen:
		updateFocusedCard(renderGroupWatchManagementCardWithNames(state, b.resolveGroupWatchNames(ctx, state.GroupWatch.ChatIDs)))
		return cardToast("success", "已打开群值守管理"), true, nil
	case managementActionGroupWatchEdit:
		draft := managementGroupWatchDraftFromCallback(event.Event.Action, state)
		updateFocusedCard(renderGroupWatchEditCard(state, draft))
		if managementGroupWatchDraftField(event.Event.Action) == "group_watch_agent" {
			return cardToast("success", "已更新可选模型"), true, nil
		}
		return cardToast("success", "已打开群值守执行配置"), true, nil
	case managementActionGroupWatchSave:
		if message := managementGroupWatchSubmissionError(event.Event.Action, state); message != "" {
			updateFocusedCard(renderGroupWatchEditCard(
				state,
				managementGroupWatchDraftFromCallback(event.Event.Action, state),
			))
			return cardToast("error", message), true, nil
		}
		draft := managementGroupWatchDraftFromCallback(event.Event.Action, state)
		if !b.beginManagementGroupWatchExecution() {
			return cardToast("warning", "群值守执行配置正在更新，请等待卡片刷新"), true, nil
		}
		b.runManagementGroupWatchExecutionUpdate(openID, messageID, draft, surface)
		return cardToast("success", "正在保存值守任务配置"), true, nil
	case managementActionGroupWatchUpdate:
		operation := managementCallbackString(event.Event.Action, "operation")
		chatID := managementCallbackString(event.Event.Action, "chat_id")
		chatName := managementCallbackString(event.Event.Action, "chat_name")
		view := managementCallbackString(event.Event.Action, "view")
		updated, updateErr := b.updateGroupWatchForProfile(operation, chatID)
		if updateErr != nil {
			if strings.Contains(updateErr.Error(), "每个话题独立") {
				return cardToast("warning", updateErr.Error()), true, nil
			}
			return cardToast("error", "群值守设置保存失败，请稍后重试"), true, nil
		}
		state = b.managementState()
		if view == "current_group" && strings.TrimSpace(chatID) != "" {
			joined := false
			for _, configured := range updated.ChatIDs {
				if strings.TrimSpace(configured) == strings.TrimSpace(chatID) {
					joined = true
					break
				}
			}
			updateFocusedCard(renderCurrentGroupWatchCard(state, chatID, chatName, joined))
		} else {
			updateFocusedCard(renderGroupWatchManagementCardWithNames(state, b.resolveGroupWatchNames(ctx, state.GroupWatch.ChatIDs)))
		}
		return cardToast("success", groupWatchOperationSuccessText(operation)), true, nil
	case managementActionDocumentCommentsOpen:
		updateFocusedCard(documentCommentsCard(state, ""))
		return cardToast("success", "已打开云文档评论设置"), true, nil
	case managementActionDocumentCommentsEdit:
		draft := managementDocumentCommentsDraftFromCallback(event.Event.Action, state)
		updateFocusedCard(renderDocumentCommentsEditCard(state, draft))
		if field := managementDocumentCommentsDraftField(event.Event.Action); field == "document_comments_agent" || field == "document_comments_runtime_ref" || field == "document_comments_model_ref" {
			return cardToast("success", "已更新可选执行参数"), true, nil
		}
		return cardToast("success", "已打开云文档评论执行配置"), true, nil
	case managementActionDocumentCommentsSave:
		if message := managementDocumentCommentsSubmissionError(event.Event.Action, state); message != "" {
			updateFocusedCard(renderDocumentCommentsEditCard(
				state,
				managementDocumentCommentsDraftFromCallback(event.Event.Action, state),
			))
			return cardToast("error", message), true, nil
		}
		draft := managementDocumentCommentsDraftFromCallback(event.Event.Action, state)
		if !b.beginManagementDocumentSettings() {
			return cardToast("warning", "云文档评论设置正在更新，请等待卡片刷新"), true, nil
		}
		b.runManagementDocumentExecutionUpdate(openID, messageID, draft, surface)
		return cardToast("success", "正在保存评论任务配置"), true, nil
	case managementActionDocumentCommentsSet:
		enabled, err := strconv.ParseBool(managementCallbackString(event.Event.Action, "enabled"))
		if err != nil {
			return cardToast("warning", "无法识别云文档评论设置，请刷新后重试"), true, nil
		}
		if !b.beginManagementDocumentSettings() {
			return cardToast("warning", "云文档评论设置正在更新，请等待卡片刷新"), true, nil
		}
		b.runManagementDocumentSettingsUpdate(openID, messageID, enabled, surface)
		if enabled {
			return cardToast("success", "正在开启云文档评论"), true, nil
		}
		return cardToast("success", "正在关闭云文档评论"), true, nil
	case managementActionSettingsSave:
		if message := managementProfileSubmissionError(event.Event.Action, state); message != "" {
			updateSettingsEditCard(state, managementDraftFromState(state))
			return cardToast("error", message), true, nil
		}
		if message := managementModelSubmissionError(event.Event.Action, state); message != "" {
			updateSettingsEditCard(state, managementDraftFromState(state))
			return cardToast("error", message), true, nil
		}
		if message := managementRuntimeSubmissionError(event.Event.Action, state); message != "" {
			updateSettingsEditCard(state, managementDraftFromState(state))
			return cardToast("error", message), true, nil
		}
		if message := managementReasoningSubmissionError(event.Event.Action, state); message != "" {
			updateSettingsEditCard(state, managementDraftFromState(state))
			return cardToast("error", message), true, nil
		}
		draft := managementDraftFromCallback(event.Event.Action, state)
		if draft.Interaction != state.Mode {
			req := managementBotSettingsRequest(state, draft, messageID, openID)
			b.runManagementSettingsTransition(openID, messageID, state, draft, req, false, surface)
			return cardToast("success", "正在更新助手设置，请稍候"), true, nil
		}
		if state.Mode == config.FeishuInteractionSingleton && managementExecutionDiffers(state, draft) {
			req := managementBotSettingsRequest(state, draft, messageID, openID)
			result, err := b.ApplyBotSettings(ctx, req)
			if err != nil {
				return cardToast("error", managementSettingsFailureMessage(err)), true, nil
			}
			if result.Status == BotSettingsStatusStateChanged {
				updated := b.managementState()
				updateSettingsEditCard(updated, managementDraftFromState(updated))
				return cardToast("warning", "配置已变化，请重新确认"), true, nil
			}
			updateFocusedCard(renderSettingsConfirmationCard(state, draft, req, result))
			return cardToast("warning", "请确认是否更换当前专家或模型"), true, nil
		}
		if managementUsesThreadRouting(state.Mode) {
			updated, err := b.commitThreadManagementSettings(state, draft)
			if err != nil {
				if errors.Is(err, errBotProfileStateChanged) {
					updated = b.managementState()
					updateSettingsEditCard(updated, managementDraftFromState(updated))
					return cardToast("warning", "配置已变化，请重新确认"), true, nil
				}
				return cardToast("error", managementSettingsFailureMessage(err)), true, nil
			}
			state = updated
		}
		if draft.ThinkingProcess != state.ThinkingProcess {
			if err := b.saveManagementThinkingProcess(draft.ThinkingProcess); err != nil {
				return cardToast("error", managementSettingsFailureMessage(err)), true, nil
			}
			state = b.managementState()
		}
		updateCard(settingsSummaryCard(state), managementViewSettings)
		return cardToast("success", "助手设置已保存"), true, nil
	case managementActionSettingsConfirm:
		req, token, ok := managementConfirmedBotSettingsRequest(event.Event.Action, openID)
		if !ok || !managementValidDefaultAgent(state, req.DefaultAgent) || !b.consumeBotSettingsConfirmation(token, req) {
			updateSettingsEditCard(state, managementDraftFromState(state))
			return cardToast("warning", "确认已失效，请重新选择"), true, nil
		}
		draft := managementSettingsDraft{Interaction: req.Interaction, DefaultAgent: req.DefaultAgent,
			DefaultRuntimeRef: req.DefaultRuntimeRef, DefaultModelRef: req.DefaultModelRef,
			DefaultReasoningEffort: req.DefaultReasoningEffort,
			ThinkingProcess:        managementCallbackString(event.Event.Action, "thinking_process")}
		b.runManagementSettingsTransition(openID, messageID, state, draft, req, false, surface)
		return cardToast("success", "正在处理中，请稍候"), true, nil
	case managementActionSettingsRecoveryBlank:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok || !managementRecoveryStateMatches(state, req) || !managementValidDefaultAgent(state, req.DefaultAgent) || !b.consumeBotSettingsConfirmation(token, req) {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, "恢复操作已失效，请重新选择")
		}
		req.RecoveryAction = BotSettingsRecoveryCreateBlank
		req.RecoveryAttemptID = token
		draft := managementSettingsDraft{Interaction: req.Interaction, DefaultAgent: req.DefaultAgent,
			DefaultRuntimeRef: req.DefaultRuntimeRef, DefaultModelRef: req.DefaultModelRef,
			DefaultReasoningEffort: req.DefaultReasoningEffort,
			ThinkingProcess:        managementCallbackString(event.Event.Action, "thinking_process")}
		b.runManagementSettingsTransition(openID, messageID, state, draft, req, false, surface)
		return cardToast("success", "正在新建空白连续对话，请稍候"), true, nil
	case managementActionSettingsRecoveryBind:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok || !managementRecoveryStateMatches(state, req) {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, "恢复操作已失效，请重新选择")
		}
		updateFocusedCard(renderSettingsManualBindCard(state, req, managementCallbackString(event.Event.Action, "thinking_process"), token, ""))
		return cardToast("success", "已显示机器侧绑定步骤"), true, nil
	case managementActionSettingsRecoveryCheck:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, "恢复操作已失效，请重新选择")
		}
		if state.Mode == config.FeishuInteractionSingleton && botSessionReady(state.BotSession) {
			b.consumeBotSettingsConfirmation(token, req)
			updateCard(settingsSummaryCard(state), managementViewSettings)
			return cardToast("success", "已检测到绑定"), true, nil
		}
		if !managementRecoveryStateMatches(state, req) {
			updateCard(settingsSummaryCard(state), managementViewSettings)
			return cardToast("warning", "设置已变化，已刷新最新状态"), true, nil
		}
		updateFocusedCard(renderSettingsManualBindCard(state, req, managementCallbackString(event.Event.Action, "thinking_process"), token, "尚未检测到绑定"))
		return cardToast("warning", "尚未检测到绑定"), true, nil
	case managementActionSettingsRecoveryBack:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok || !managementRecoveryStateMatches(state, req) {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, "恢复操作已失效，请重新选择")
		}
		choice := botSettingsRecoveryChoice(BotProfileState{
			InteractionMode: state.Mode, DefaultAgent: state.DefaultAgent, Version: state.Version,
		}, token)
		updateFocusedCard(renderSettingsRecoveryChoiceCard(state, managementDraftFromState(state), req, choice, ""))
		return cardToast("success", "已返回恢复选项"), true, nil
	case managementActionSettingsRecoveryDismiss:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok || !managementRecoveryStateMatches(state, req) || !b.consumeBotSettingsConfirmation(token, req) {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, "恢复操作已失效")
		}
		updateCard(settingsSummaryCard(state), managementViewSettings)
		return cardToast("success", "已暂不处理"), true, nil
	case managementActionSettingsCancel:
		updateCard(settingsSummaryCard(state), managementViewSettings)
		return cardToast("success", "已取消修改"), true, nil
	case managementActionPackLibraryOpen:
		b.runManagementAgentPackLibraryOpen(openID, messageID, surface)
		return cardToast("success", "正在打开专家库"), true, nil
	case managementActionPackLibraryNavigate:
		view, ok := managementPackLibraryViewFromCallback(event.Event.Action)
		if !ok {
			return cardToast("warning", "无法识别专家库页面，请重新打开"), true, nil
		}
		if b.managementPackOperationActive(openID, messageID) {
			return cardToast("warning", "已有专家库操作正在进行，请等待完成"), true, nil
		}
		b.runManagementAgentPackLibraryView(openID, messageID, view, surface)
		return cardToast("success", "正在切换专家库页面"), true, nil
	case managementActionPackManage:
		namespace := managementCallbackString(event.Event.Action, "namespace")
		view, ok := managementPackLibraryViewFromCallback(event.Event.Action)
		if namespace == "" || !ok {
			return cardToast("warning", "无法识别要管理的专家库，请重新打开"), true, nil
		}
		if b.managementPackOperationActive(openID, messageID) {
			return cardToast("warning", "已有专家库操作正在进行，请等待完成"), true, nil
		}
		b.runManagementLoadedPackView(openID, messageID, namespace, view, surface)
		return cardToast("success", "正在打开专家库管理"), true, nil
	case managementActionPackInstall:
		catalogID := managementCallbackString(event.Event.Action, "catalog_id")
		expectedGeneration := managementCallbackString(event.Event.Action, "expected_generation")
		if catalogID == "" || expectedGeneration == "" || b.managementPackActions == nil {
			return cardToast("error", "无法识别要添加的专家"), true, nil
		}
		started, consumed, consumeErr := b.beginAndConsumeManagementPackOperation(
			openID, messageID, action, catalogID, managementCallbackString(event.Event.Action, "operation_nonce"),
		)
		if consumeErr != nil {
			return cardToast("error", "暂时无法确认专家库操作状态"), true, nil
		}
		if !started {
			return cardToast("warning", "已有专家库操作正在进行，请等待完成"), true, nil
		}
		if !consumed {
			return cardToast("warning", "该操作已提交，请等待卡片刷新"), true, nil
		}
		view, _ := managementPackLibraryViewFromCallback(event.Event.Action)
		b.runManagementAgentPackOperation(openID, messageID, managementActionPackInstall, catalogID, expectedGeneration, view, surface)
		return cardToast("success", "正在添加专家"), true, nil
	case managementActionPackSync:
		namespace := managementCallbackString(event.Event.Action, "namespace")
		expectedGeneration := managementCallbackString(event.Event.Action, "expected_generation")
		if namespace == "" || expectedGeneration == "" || b.managementPackActions == nil {
			return cardToast("error", "无法识别要更新的专家"), true, nil
		}
		started, consumed, consumeErr := b.beginAndConsumeManagementPackOperation(
			openID, messageID, action, namespace, managementCallbackString(event.Event.Action, "operation_nonce"),
		)
		if consumeErr != nil {
			return cardToast("error", "暂时无法确认专家库操作状态"), true, nil
		}
		if !started {
			return cardToast("warning", "已有专家库操作正在进行，请等待完成"), true, nil
		}
		if !consumed {
			return cardToast("warning", "该操作已提交，请等待卡片刷新"), true, nil
		}
		view, _ := managementPackLibraryViewFromCallback(event.Event.Action)
		b.runManagementAgentPackOperation(openID, messageID, managementActionPackSync, namespace, expectedGeneration, view, surface)
		return cardToast("success", "正在检查更新"), true, nil
	case managementActionPackRemove:
		namespace := managementCallbackString(event.Event.Action, "namespace")
		expectedGeneration := managementCallbackString(event.Event.Action, "expected_generation")
		if namespace == "" || expectedGeneration == "" || b.managementPackActions == nil || b.db == nil {
			return cardToast("error", "无法识别要移除的专家"), true, nil
		}
		started, consumed, consumeErr := b.beginAndConsumeManagementPackOperation(
			openID, messageID, action, namespace, managementCallbackString(event.Event.Action, "operation_nonce"),
		)
		if consumeErr != nil {
			return cardToast("error", "暂时无法确认专家库操作状态"), true, nil
		}
		if !started {
			return cardToast("warning", "已有专家库操作正在进行，请等待完成"), true, nil
		}
		if !consumed {
			return cardToast("warning", "该操作已提交，请等待卡片刷新"), true, nil
		}
		references, err := b.db.BotDefaultAgentReferences(namespace)
		if err != nil {
			b.endManagementPackOperation(openID, messageID)
			return cardToast("error", "暂时无法检查专家使用情况"), true, nil
		}
		if len(references) > 0 {
			b.endManagementPackOperation(openID, messageID)
			notice := fmt.Sprintf("这组专家中有专家正被 %d 个飞书助手设为默认专家，请先更换后再移除。", len(references))
			view, _ := managementPackLibraryViewFromCallback(event.Event.Action)
			b.runManagementAgentPackLibraryViewWithNotice(openID, messageID, view, notice, surface)
			return cardToast("warning", "这组专家仍被飞书助手使用"), true, nil
		}
		view, _ := managementPackLibraryViewFromCallback(event.Event.Action)
		b.runManagementAgentPackOperation(openID, messageID, managementActionPackRemove, namespace, expectedGeneration, view, surface)
		return cardToast("success", "正在移除专家"), true, nil
	case managementActionStatusOpen:
		updateCard(overviewCard(state), managementViewOverview)
		return cardToast("success", "已打开助手概览"), true, nil
	case managementActionSystemMaintenance:
		updateCard(systemMaintenanceCard(state), managementViewSystem)
		return cardToast("success", "已打开系统状态"), true, nil
	case managementActionSystemRestart, "system.restart.open":
		updateFocusedCard(renderSystemRestartConfirmationCard(state))
		return cardToast("warning", "请确认是否重启 amux"), true, nil
	case managementActionSystemRestartOK:
		systemInstance := managementCallbackString(event.Event.Action, "system_instance")
		nonce := managementCallbackString(event.Event.Action, "confirmation_nonce")
		if !b.consumeManagementRestartConfirmation(state, systemInstance, nonce) {
			updateCard(systemMaintenanceCard(state), managementViewSystem)
			return cardToast("warning", "确认已失效，请重新操作"), true, nil
		}
		if !b.beginManagementMaintenance("restart") {
			return cardToast("warning", "已有系统维护操作正在进行"), true, nil
		}
		if b.db == nil {
			b.endManagementMaintenance("restart")
			return cardToast("error", "无法安排重启后的状态恢复，请稍后重试"), true, nil
		}
		if err := b.db.UpsertPendingManagementRecoveryForSurface(
			b.currentBridgeId(),
			openID,
			messageID,
			managementRecoveryRestart,
			"",
			string(surface),
		); err != nil {
			b.endManagementMaintenance("restart")
			log.Warn().Err(err).Msg("bridge: persist pending management recovery")
			return cardToast("error", "无法安排重启后的状态恢复，请稍后重试"), true, nil
		}
		b.runManagementSystemRestart(openID, messageID, surface)
		return cardToast("success", "正在重启 amux"), true, nil
	case managementActionSystemUpdate, "system.update.open":
		if !b.beginManagementMaintenance("update_check") {
			return cardToast("warning", "已有系统维护操作正在进行"), true, nil
		}
		b.runManagementUpdateCheck(openID, messageID, state, surface)
		return cardToast("success", "正在检查更新"), true, nil
	case managementActionSystemUpdateDismiss:
		version := managementCallbackString(event.Event.Action, "version")
		if b.managementActions == nil || version == "" {
			return cardToast("error", "无法识别要忽略的版本"), true, nil
		}
		if err := b.managementActions.DismissUpdate(version); err != nil {
			log.Warn().Err(err).Str("version", version).Msg("bridge: dismiss management update prompt")
			return cardToast("error", "暂时无法保存更新提示设置"), true, nil
		}
		updateCard(systemMaintenanceCard(b.managementState()), managementViewSystem)
		return cardToast("success", "此版本不再提示"), true, nil
	case managementActionSystemUpgrade:
		systemInstance := managementCallbackString(event.Event.Action, "system_instance")
		nonce := managementCallbackString(event.Event.Action, "confirmation_nonce")
		expectedVersion := managementCallbackString(event.Event.Action, "expected_version")
		if !b.consumeManagementUpgradeConfirmation(state, systemInstance, expectedVersion, nonce) {
			updateCard(systemMaintenanceCard(state), managementViewSystem)
			return cardToast("warning", "确认已失效，请重新检查更新"), true, nil
		}
		if !b.beginManagementMaintenance("upgrade") {
			return cardToast("warning", "已有系统维护操作正在进行"), true, nil
		}
		if b.db == nil {
			b.endManagementMaintenance("upgrade")
			return cardToast("error", "无法安排升级后的状态恢复，请稍后重试"), true, nil
		}
		if err := b.db.UpsertPendingManagementRecoveryForSurface(
			b.currentBridgeId(),
			openID,
			messageID,
			managementRecoveryUpgrade,
			expectedVersion,
			string(surface),
		); err != nil {
			b.endManagementMaintenance("upgrade")
			log.Warn().Err(err).Msg("bridge: persist pending upgrade recovery")
			return cardToast("error", "无法安排升级后的状态恢复，请稍后重试"), true, nil
		}
		b.runManagementSystemUpgrade(openID, messageID, expectedVersion, surface)
		return cardToast("success", "正在升级 amux"), true, nil
	case managementActionHelpOpen:
		helpCard := renderManagementHelpCard(state)
		if surface == managementSurfaceAmux {
			helpCard = renderManagementCompactHelpCard()
		}
		updateCard(helpCard, managementViewHelp)
		return cardToast("success", "已打开使用帮助"), true, nil
	default:
		return cardToast("warning", "暂不支持这个操作"), true, nil
	}
}

// Leave enough time for the card.action.trigger response to reach Feishu
// before the detached lifecycle command stops this amuxd process.

// A successful call means the detached restart command started. The
// process will exit, so the in-memory guard naturally resets.

// Allow Feishu to receive the callback response and processing card
// before the installed binary restarts this amuxd.

// The installed update command owns the restart. A new amuxd will
// consume the persisted recovery record.

const managementProfileDefaultModelOption = "__profile_default__"
const managementRuntimeDefaultReasoningOption = "__runtime_default_reasoning__"

// managementDecomposeInteraction maps the card-facing composite value
// thread_isolated back to (thread, isolation_required=true); every plain mode
// maps to itself with isolation_required=false. The routing enum (thread |
// singleton) stays untouched — isolation only rides on thread.

// Cards sent by the removed experimental command may still be visible
// or pending recovery during an upgrade. Keep them on the current /amux surface.

type managementState struct {
	ProfileName                     string
	OwnerOpenID                     string
	Mode                            string
	DefaultAgent                    string
	DefaultRuntimeRef               string
	DefaultModelRef                 string
	DefaultReasoningEffort          string
	ThinkingProcess                 string
	WorkspaceModelRef               string
	DocumentCommentsEnabled         bool
	DocumentCommentsAgentRef        string
	DocumentCommentsRuntimeRef      string
	DocumentCommentsModelRef        string
	DocumentCommentsReasoningEffort string
	BotSession                      *BotSession
	Profiles                        []managementProfileSummary
	Runtimes                        []agentprofile.RuntimeOption
	Targets                         int
	Sessions                        int
	Running                         int
	SystemStatus                    managementSystemStatus
	Version                         int64
	SystemInfo                      ManagementSystemInfo
	Update                          ManagementUpdateInfo
	CanManagePacks                  bool
	BotToBot                        config.BridgeFeishuBotToBotConfig
	GroupWatch                      config.BridgeFeishuGroupWatchConfig
	GroupWatchPermission            *capabilityPermissionIssue
	DocumentPermission              *capabilityPermissionIssue
	PermissionAudit                 *capabilityPermissionIssue
	PermissionAuditChecked          bool
	PermissionAuditFailed           bool
}

func (b *Bridge) managementState() managementState {
	state := managementState{
		ProfileName:  b.ProfileName(),
		SystemStatus: managementSystemAttention,
	}
	if b != nil {
		state.SystemInfo = b.managementSystemInfo
		if b.managementActions != nil {
			state.Update = b.managementActions.UpdateSnapshot()
		}
		state.CanManagePacks = b.managementPackActions != nil
		state.ThinkingProcess = config.GetBridgeFeishuPresentationForProfile(b.ProfileName()).ThinkingProcess
		state.DocumentCommentsEnabled = b.documentCommentsActive()
		documentComments := config.GetBridgeFeishuDocumentCommentsForProfile(b.ProfileName())
		state.DocumentCommentsAgentRef = documentComments.AgentProfileRef
		state.DocumentCommentsRuntimeRef = documentComments.RuntimeRef
		state.DocumentCommentsModelRef = documentComments.ModelRef
		state.DocumentCommentsReasoningEffort = documentComments.ReasoningEffort
		state.BotToBot = b.botToBotSnapshot()
		state.GroupWatch = b.groupWatchSnapshot()
		state.GroupWatchPermission = b.capabilityPermissionIssue(capabilityGroupWatch)
		state.DocumentPermission = b.capabilityPermissionIssue(capabilityDocumentComments)
		state.PermissionAudit = b.capabilityPermissionIssue(capabilityFeishuIntegration)
		state.PermissionAuditChecked, state.PermissionAuditFailed = b.feishuPermissionAuditHealth()
	}
	if b != nil && b.secrets != nil {
		state.OwnerOpenID = strings.TrimSpace(b.secrets.OpenId)
	}
	fallbackMode := config.FeishuInteractionThread
	if b != nil {
		if mode, err := b.interactionMode(); err == nil && managementValidInteraction(mode) {
			fallbackMode = mode
		}
	}
	state.Mode = fallbackMode
	if b != nil && b.db != nil {
		state.WorkspaceModelRef = strings.TrimSpace(b.db.workspaceDefaults.Model)
		if profileState, err := b.db.GetBotProfileState(b.currentBridgeId(), fallbackMode); err == nil {
			state.Mode = profileState.InteractionMode
			if profileState.InteractionMode == config.FeishuInteractionThread && profileState.IsolationRequired {
				state.Mode = interactionModeThreadIsolated
			}
			state.DefaultAgent = strings.TrimSpace(profileState.DefaultAgent)
			state.DefaultRuntimeRef = strings.TrimSpace(profileState.DefaultRuntimeRef)
			state.DefaultModelRef = strings.TrimSpace(profileState.DefaultModelRef)
			state.DefaultReasoningEffort = strings.TrimSpace(profileState.DefaultReasoningEffort)
			state.Version = profileState.Version
		}
		if bs, err := b.db.GetBotSession(b.currentBridgeId()); err == nil {
			state.BotSession = bs
		}
	}
	catalog := managementProfiles()
	state.Profiles = managementSelectableProfiles(
		catalog,
		state.DefaultAgent,
		state.DocumentCommentsAgentRef,
		state.GroupWatch.AgentProfileRef,
	)
	if runtimes, err := agentprofile.RuntimeCatalog(); err == nil {
		state.Runtimes = runtimes
	}
	if b != nil && b.hub != nil {
		targets := b.hub.ListTargets()
		state.Targets = len(targets)
		if state.Targets > 0 {
			state.SystemStatus = managementSystemHealthy
		}
		for _, target := range targets {
			for _, session := range target.Sessions {
				state.Sessions++
				if strings.EqualFold(session.State, "running") {
					state.Running++
				}
			}
		}
	}
	return state
}

func (b *Bridge) commitThreadManagementSettings(state managementState, draft managementSettingsDraft) (managementState, error) {
	draft = managementNormalizeDraftForState(state, draft)
	if b == nil || b.db == nil {
		return state, fmt.Errorf("bridge db unavailable")
	}
	if b.managementPackActions != nil {
		var updated managementState
		err := b.managementPackActions.WithRegistryStable(func() error {
			var commitErr error
			updated, commitErr = b.commitThreadManagementSettingsStable(state, draft)
			return commitErr
		})
		return updated, err
	}
	return b.commitThreadManagementSettingsStable(state, draft)
}

func (b *Bridge) commitThreadManagementSettingsStable(state managementState, draft managementSettingsDraft) (managementState, error) {
	if managementExecutionDiffers(state, draft) {
		if _, err := resolveBridgeAgentLaunchWithSelection(draft.DefaultAgent, b.db.workspaceDefaults.Args,
			draft.DefaultRuntimeRef, draft.DefaultModelRef, draft.DefaultReasoningEffort); err != nil {
			return state, err
		}
	}
	if draft.Interaction == interactionModeThreadIsolated {
		if ok, reason := b.isolationLaunchSupported(); !ok {
			return state, fmt.Errorf("当前环境不支持「每个话题独立（用户隔离）」：%s。该模式需要已启用用户隔离的 Linux 环境，设置未被修改", reason)
		}
	}
	if _, err := b.db.CommitThreadBotSettingsWithLaunch(b.currentBridgeId(), state.Version, draft.DefaultAgent,
		draft.DefaultRuntimeRef, draft.DefaultModelRef, draft.DefaultReasoningEffort,
		draft.Interaction == interactionModeThreadIsolated); err != nil {
		return state, err
	}
	if err := b.db.SetProfileDefaultAgent(b.currentBridgeId(), draft.DefaultAgent); err != nil {
		log.Warn().Err(err).Str("agent_profile", draft.DefaultAgent).Msg("failed to update legacy feishu profile default projection")
	}
	return b.managementState(), nil
}

func (b *Bridge) saveManagementThinkingProcess(mode string) error {
	if b == nil {
		return fmt.Errorf("bridge unavailable")
	}
	return config.SetBridgeFeishuThinkingProcess(b.ProfileName(), mode)
}

// Rebuild after clamping so every visible mutation carries the exact
// section and page the user acted on.

/*
	The legacy single-card renderer below intentionally remains absent: every
	section must pass through the bounded paginator before reaching Feishu.
*/
