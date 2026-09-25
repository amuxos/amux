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

func (b *Bridge) handleManagementCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (response *callback.CardActionTriggerResponse, handled bool, err error) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, false, nil
	}
	action := managementActionFromCallback(event.Event.Action)
	if action == "" {
		return nil, false, nil
	}
	defer func() {
		if event.Event.Operator != nil {
			b.localizeManagementToast(response, event.Event.Operator.OpenID)
		}
	}()
	if response, handled, err := b.handleGroupSettingsCardAction(ctx, event, action); handled {
		return response, true, err
	}
	openID := ""
	if event.Event.Operator != nil {
		openID = event.Event.Operator.OpenID
	}
	if !b.ownerConfirmed() || !b.canUseBridge(openID) {
		return cardToast("error", managementCopy("management.you.do.not.have.permission.to.manage.this.assistant")), true, nil
	}
	messageID := ""
	if event.Event.Context != nil {
		messageID = strings.TrimSpace(event.Event.Context.OpenMessageID)
	}
	if action != managementActionPermissionRecheck {
		b.invalidateFeishuPermissionRecheck(messageID)
	}
	b.advanceModelServiceEditor(messageID)
	if action == managementActionLanguage {
		return b.changeManagementLocale(openID, messageID, event.Event.Action), true, nil
	}
	if b.handleModelServiceEditor(event, action, openID, messageID) {
		return cardToast("success", managementCopy("management.model.service.updating")), true, nil
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
	documentCommentsCard := func(current managementState, notice any) string {
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
			return cardToast("error", managementCopy("management.cannot.read.experiment.settings.check.the.configuration.file")), true, nil
		}
		if action != managementActionExperimentsOpen {
			update, err := managementExperimentsSettingsFromCallback(event.Event.Action, saved)
			if err != nil {
				return cardToast("error", managementErrorText(err)), true, nil
			}
			saved, err = config.UpdateExperimentsSettings(update)
			if err != nil {
				return cardToast("error", managementCopy("management.cannot.save.experiment.settings.check.the.configuration.file")), true, nil
			}
		}
		if action == managementActionExperimentsSaveRestart {
			updateFocusedCard(renderManagementExperimentsRestartConfirmationCard(state))
			return cardToast("success", managementCopy("management.saved.confirm.the.restart.to.apply.changes")), true, nil
		}
		updateFocusedCard(renderManagementExperimentsCard(saved, config.GetExperimentsSettings()))
		if action == managementActionExperimentsSave {
			return cardToast("success", managementCopy("management.saved.restart.amux.to.apply.changes")), true, nil
		}
		return cardToast("success", managementCopy("management.experiments.opened")), true, nil
	case managementActionOnboardingEdit:
		updateFocusedCard(renderOnboardingDraftCard(state, managementDraftFromCallback(event.Event.Action, state)))
		return cardToast("success", managementCopy("management.configuration.selection.updated")), true, nil
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
			return cardToast("success", managementCopy("management.preparing.the.assistant.please.wait")), true, nil
		}
		if managementUsesThreadRouting(state.Mode) {
			updated, err := b.commitThreadManagementSettings(state, draft)
			if err != nil {
				if errors.Is(err, errBotProfileStateChanged) {
					updated = b.managementState()
					updateFocusedCard(renderOnboardingDraftCard(updated, managementDraftFromState(updated)))
					return cardToast("warning", managementCopy("management.configuration.changed.please.confirm.again")), true, nil
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
		return cardToast("success", managementCopy("management.assistant.configuration.saved")), true, nil
	case managementActionSettingsOpen:
		if surface == managementSurfaceAmux {
			updateCard(settingsSummaryCard(state), managementViewSettings)
		} else {
			updateSettingsEditCard(state, managementDraftFromState(state))
		}
		return cardToast("success", managementCopy("management.assistant.settings.opened")), true, nil
	case managementActionSettingsEdit:
		updateSettingsEditCard(state, managementDraftFromCallback(event.Event.Action, state))
		return cardToast("success", managementCopy("management.configuration.selection.updated")), true, nil
	case managementActionCapabilitiesOpen:
		updateFocusedCard(capabilitiesCard(state))
		return cardToast("success", managementCopy("management.integrations.opened")), true, nil
	case managementActionBotToBotOpen:
		updateFocusedCard(renderBotToBotCard(state))
		return cardToast("success", managementCopy("management.agent.collaboration.settings.opened")), true, nil
	case managementActionBotToBotSet:
		enabled, err := strconv.ParseBool(managementCallbackString(event.Event.Action, "enabled"))
		if err != nil {
			return cardToast("warning", managementCopy("management.invalid.agent.collaboration.settings.refresh.and.try.again")), true, nil
		}
		if !b.beginManagementBotToBotSettings() {
			return cardToast("warning", managementCopy("management.agent.collaboration.settings.are.updating.wait.for.the.card")), true, nil
		}
		b.runManagementBotToBotSettingsUpdate(openID, messageID, enabled, surface)
		if enabled {
			return cardToast("success", managementCopy("management.enabling.agent.collaboration")), true, nil
		}
		return cardToast("success", managementCopy("management.disabling.agent.collaboration")), true, nil
	case managementActionPermissionRecheck:
		version, ok := b.beginFeishuPermissionRecheck(messageID)
		if !ok {
			return cardToast("warning", managementCopy("management.checking.feishu.permissions.please.wait")), true, nil
		}
		b.runFeishuPermissionRecheck(openID, messageID, surface, version)
		return cardToast("success", managementCopy("management.rechecking.feishu.permissions")), true, nil
	case managementActionPermissionApply:
		version, ok := b.beginFeishuPermissionApply(messageID)
		if !ok {
			return cardToast("warning", managementCopy("management.a.permission.request.is.in.progress.please.wait")), true, nil
		}
		b.runFeishuPermissionApply(openID, messageID, surface, version)
		return cardToast("success", managementCopy("management.requesting.permission.from.the.administrator")), true, nil
	case managementActionGroupWatchOpen:
		updateFocusedCard(renderGroupWatchManagementCardWithNames(state, b.resolveGroupWatchNames(ctx, state.GroupWatch.ChatIDs)))
		return cardToast("success", managementCopy("management.group.watch.management.opened")), true, nil
	case managementActionGroupWatchEdit:
		draft := managementGroupWatchDraftFromCallback(event.Event.Action, state)
		updateFocusedCard(renderGroupWatchEditCard(state, draft))
		if managementGroupWatchDraftField(event.Event.Action) == "group_watch_agent" {
			return cardToast("success", managementCopy("management.available.models.updated")), true, nil
		}
		return cardToast("success", managementCopy("management.group.watch.execution.settings.opened")), true, nil
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
			return cardToast("warning", managementCopy("management.group.watch.execution.settings.are.updating.wait.for.the")), true, nil
		}
		b.runManagementGroupWatchExecutionUpdate(openID, messageID, draft, surface)
		return cardToast("success", managementCopy("management.saving.group.watch.task.settings")), true, nil
	case managementActionGroupWatchUpdate:
		operation := managementCallbackString(event.Event.Action, "operation")
		chatID := managementCallbackString(event.Event.Action, "chat_id")
		chatName := managementCallbackString(event.Event.Action, "chat_name")
		view := managementCallbackString(event.Event.Action, "view")
		updated, updateErr := b.updateGroupWatchForProfile(operation, chatID)
		if updateErr != nil {
			if errors.Is(updateErr, errGroupWatchInteraction) {
				return cardToast("warning", managementErrorText(updateErr)), true, nil
			}
			return cardToast("error", managementCopy("management.cannot.save.group.watch.settings.try.again.later")), true, nil
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
		return cardToast("success", managementGroupWatchSuccessText(operation)), true, nil
	case managementActionDocumentCommentsOpen:
		updateFocusedCard(documentCommentsCard(state, ""))
		return cardToast("success", managementCopy("management.document.comment.settings.opened")), true, nil
	case managementActionDocumentCommentsEdit:
		draft := managementDocumentCommentsDraftFromCallback(event.Event.Action, state)
		updateFocusedCard(renderDocumentCommentsEditCard(state, draft))
		if field := managementDocumentCommentsDraftField(event.Event.Action); field == "document_comments_agent" || field == "document_comments_runtime_ref" || field == "document_comments_model_ref" {
			return cardToast("success", managementCopy("management.available.execution.options.updated")), true, nil
		}
		return cardToast("success", managementCopy("management.document.comment.execution.settings.opened")), true, nil
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
			return cardToast("warning", managementCopy("management.document.comment.settings.are.updating.wait.for.the.card")), true, nil
		}
		b.runManagementDocumentExecutionUpdate(openID, messageID, draft, surface)
		return cardToast("success", managementCopy("management.saving.comment.task.settings")), true, nil
	case managementActionDocumentCommentsSet:
		enabled, err := strconv.ParseBool(managementCallbackString(event.Event.Action, "enabled"))
		if err != nil {
			return cardToast("warning", managementCopy("management.invalid.document.comment.settings.refresh.and.try.again")), true, nil
		}
		if !b.beginManagementDocumentSettings() {
			return cardToast("warning", managementCopy("management.document.comment.settings.are.updating.wait.for.the.card")), true, nil
		}
		b.runManagementDocumentSettingsUpdate(openID, messageID, enabled, surface)
		if enabled {
			return cardToast("success", managementCopy("management.enabling.document.comments")), true, nil
		}
		return cardToast("success", managementCopy("management.disabling.document.comments")), true, nil
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
			return cardToast("success", managementCopy("management.updating.assistant.settings.please.wait")), true, nil
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
				return cardToast("warning", managementCopy("management.configuration.changed.please.confirm.again")), true, nil
			}
			updateFocusedCard(renderSettingsConfirmationCard(state, draft, req, result))
			return cardToast("warning", managementCopy("management.confirm.changing.the.current.expert.or.model")), true, nil
		}
		if managementUsesThreadRouting(state.Mode) {
			updated, err := b.commitThreadManagementSettings(state, draft)
			if err != nil {
				if errors.Is(err, errBotProfileStateChanged) {
					updated = b.managementState()
					updateSettingsEditCard(updated, managementDraftFromState(updated))
					return cardToast("warning", managementCopy("management.configuration.changed.please.confirm.again")), true, nil
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
		return cardToast("success", managementCopy("management.assistant.settings.saved")), true, nil
	case managementActionSettingsConfirm:
		req, token, ok := managementConfirmedBotSettingsRequest(event.Event.Action, openID)
		if !ok || !managementValidDefaultAgent(state, req.DefaultAgent) || !b.consumeBotSettingsConfirmation(token, req) {
			updateSettingsEditCard(state, managementDraftFromState(state))
			return cardToast("warning", managementCopy("management.confirmation.expired.make.your.selection.again")), true, nil
		}
		draft := managementSettingsDraft{Interaction: req.Interaction, DefaultAgent: req.DefaultAgent,
			DefaultRuntimeRef: req.DefaultRuntimeRef, DefaultModelRef: req.DefaultModelRef,
			DefaultReasoningEffort: req.DefaultReasoningEffort,
			ThinkingProcess:        managementCallbackString(event.Event.Action, "thinking_process")}
		b.runManagementSettingsTransition(openID, messageID, state, draft, req, false, surface)
		return cardToast("success", managementCopy("management.processing.please.wait")), true, nil
	case managementActionSettingsRecoveryBlank:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok || !managementRecoveryStateMatches(state, req) || !managementValidDefaultAgent(state, req.DefaultAgent) || !b.consumeBotSettingsConfirmation(token, req) {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, managementCopy("management.recovery.action.expired.make.your.selection.again"))
		}
		req.RecoveryAction = BotSettingsRecoveryCreateBlank
		req.RecoveryAttemptID = token
		draft := managementSettingsDraft{Interaction: req.Interaction, DefaultAgent: req.DefaultAgent,
			DefaultRuntimeRef: req.DefaultRuntimeRef, DefaultModelRef: req.DefaultModelRef,
			DefaultReasoningEffort: req.DefaultReasoningEffort,
			ThinkingProcess:        managementCallbackString(event.Event.Action, "thinking_process")}
		b.runManagementSettingsTransition(openID, messageID, state, draft, req, false, surface)
		return cardToast("success", managementCopy("management.creating.a.blank.persistent.conversation.please.wait")), true, nil
	case managementActionSettingsRecoveryBind:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok || !managementRecoveryStateMatches(state, req) {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, managementCopy("management.recovery.action.expired.make.your.selection.again"))
		}
		updateFocusedCard(renderSettingsManualBindCard(state, req, managementCallbackString(event.Event.Action, "thinking_process"), token, ""))
		return cardToast("success", managementCopy("management.machine.side.binding.instructions.displayed")), true, nil
	case managementActionSettingsRecoveryCheck:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, managementCopy("management.recovery.action.expired.make.your.selection.again"))
		}
		if state.Mode == config.FeishuInteractionSingleton && botSessionReady(state.BotSession) {
			b.consumeBotSettingsConfirmation(token, req)
			updateCard(settingsSummaryCard(state), managementViewSettings)
			return cardToast("success", managementCopy("management.binding.detected")), true, nil
		}
		if !managementRecoveryStateMatches(state, req) {
			updateCard(settingsSummaryCard(state), managementViewSettings)
			return cardToast("warning", managementCopy("management.settings.changed.latest.state.refreshed")), true, nil
		}
		updateFocusedCard(renderSettingsManualBindCard(state, req, managementCallbackString(event.Event.Action, "thinking_process"), token, managementCopy("management.no.binding.detected.yet")))
		return cardToast("warning", managementCopy("management.no.binding.detected.yet")), true, nil
	case managementActionSettingsRecoveryBack:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok || !managementRecoveryStateMatches(state, req) {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, managementCopy("management.recovery.action.expired.make.your.selection.again"))
		}
		choice := botSettingsRecoveryChoice(BotProfileState{
			InteractionMode: state.Mode, DefaultAgent: state.DefaultAgent, Version: state.Version,
		}, token)
		updateFocusedCard(renderSettingsRecoveryChoiceCard(state, managementDraftFromState(state), req, choice, ""))
		return cardToast("success", managementCopy("management.returned.to.recovery.options")), true, nil
	case managementActionSettingsRecoveryDismiss:
		req, token, ok := b.validManagementRecoveryCallback(event.Event.Action, openID)
		if !ok || !managementRecoveryStateMatches(state, req) || !b.consumeBotSettingsConfirmation(token, req) {
			return b.expiredManagementRecoveryCallback(openID, messageID, state, surface, managementCopy("management.recovery.action.expired"))
		}
		updateCard(settingsSummaryCard(state), managementViewSettings)
		return cardToast("success", managementCopy("management.deferred.for.now")), true, nil
	case managementActionSettingsCancel:
		updateCard(settingsSummaryCard(state), managementViewSettings)
		return cardToast("success", managementCopy("management.changes.cancelled")), true, nil
	case managementActionPackLibraryOpen:
		b.runManagementAgentPackLibraryOpen(openID, messageID, surface)
		return cardToast("success", managementCopy("management.opening.the.expert.library")), true, nil
	case managementActionPackLibraryNavigate:
		view, ok := managementPackLibraryViewFromCallback(event.Event.Action)
		if !ok {
			return cardToast("warning", managementCopy("management.invalid.expert.library.page.open.it.again")), true, nil
		}
		if b.managementPackOperationActive(openID, messageID) {
			return cardToast("warning", managementCopy("management.an.expert.library.operation.is.in.progress.wait.for")), true, nil
		}
		b.runManagementAgentPackLibraryView(openID, messageID, view, surface)
		return cardToast("success", managementCopy("management.switching.expert.library.pages")), true, nil
	case managementActionPackManage:
		namespace := managementCallbackString(event.Event.Action, "namespace")
		view, ok := managementPackLibraryViewFromCallback(event.Event.Action)
		if namespace == "" || !ok {
			return cardToast("warning", managementCopy("management.cannot.identify.the.expert.library.to.manage.open.it")), true, nil
		}
		if b.managementPackOperationActive(openID, messageID) {
			return cardToast("warning", managementCopy("management.an.expert.library.operation.is.in.progress.wait.for")), true, nil
		}
		b.runManagementLoadedPackView(openID, messageID, namespace, view, surface)
		return cardToast("success", managementCopy("management.opening.expert.library.management")), true, nil
	case managementActionPackInstall:
		catalogID := managementCallbackString(event.Event.Action, "catalog_id")
		expectedGeneration := managementCallbackString(event.Event.Action, "expected_generation")
		if catalogID == "" || expectedGeneration == "" || b.managementPackActions == nil {
			return cardToast("error", managementCopy("management.cannot.identify.the.expert.to.add")), true, nil
		}
		started, consumed, consumeErr := b.beginAndConsumeManagementPackOperation(
			openID, messageID, action, catalogID, managementCallbackString(event.Event.Action, "operation_nonce"),
		)
		if consumeErr != nil {
			return cardToast("error", managementCopy("management.cannot.check.the.expert.library.operation.status.right.now")), true, nil
		}
		if !started {
			return cardToast("warning", managementCopy("management.an.expert.library.operation.is.in.progress.wait.for")), true, nil
		}
		if !consumed {
			return cardToast("warning", managementCopy("management.operation.submitted.wait.for.the.card.to.refresh")), true, nil
		}
		view, _ := managementPackLibraryViewFromCallback(event.Event.Action)
		b.runManagementAgentPackOperation(openID, messageID, managementActionPackInstall, catalogID, expectedGeneration, view, surface)
		return cardToast("success", managementCopy("management.adding.experts")), true, nil
	case managementActionPackSync:
		namespace := managementCallbackString(event.Event.Action, "namespace")
		expectedGeneration := managementCallbackString(event.Event.Action, "expected_generation")
		if namespace == "" || expectedGeneration == "" || b.managementPackActions == nil {
			return cardToast("error", managementCopy("management.cannot.identify.the.expert.to.update")), true, nil
		}
		started, consumed, consumeErr := b.beginAndConsumeManagementPackOperation(
			openID, messageID, action, namespace, managementCallbackString(event.Event.Action, "operation_nonce"),
		)
		if consumeErr != nil {
			return cardToast("error", managementCopy("management.cannot.check.the.expert.library.operation.status.right.now")), true, nil
		}
		if !started {
			return cardToast("warning", managementCopy("management.an.expert.library.operation.is.in.progress.wait.for")), true, nil
		}
		if !consumed {
			return cardToast("warning", managementCopy("management.operation.submitted.wait.for.the.card.to.refresh")), true, nil
		}
		view, _ := managementPackLibraryViewFromCallback(event.Event.Action)
		b.runManagementAgentPackOperation(openID, messageID, managementActionPackSync, namespace, expectedGeneration, view, surface)
		return cardToast("success", managementCopy("management.checking.for.updates")), true, nil
	case managementActionPackRemove:
		namespace := managementCallbackString(event.Event.Action, "namespace")
		expectedGeneration := managementCallbackString(event.Event.Action, "expected_generation")
		if namespace == "" || expectedGeneration == "" || b.managementPackActions == nil || b.db == nil {
			return cardToast("error", managementCopy("management.cannot.identify.the.expert.to.remove")), true, nil
		}
		started, consumed, consumeErr := b.beginAndConsumeManagementPackOperation(
			openID, messageID, action, namespace, managementCallbackString(event.Event.Action, "operation_nonce"),
		)
		if consumeErr != nil {
			return cardToast("error", managementCopy("management.cannot.check.the.expert.library.operation.status.right.now")), true, nil
		}
		if !started {
			return cardToast("warning", managementCopy("management.an.expert.library.operation.is.in.progress.wait.for")), true, nil
		}
		if !consumed {
			return cardToast("warning", managementCopy("management.operation.submitted.wait.for.the.card.to.refresh")), true, nil
		}
		references, err := b.db.BotDefaultAgentReferences(namespace)
		if err != nil {
			b.endManagementPackOperation(openID, messageID)
			return cardToast("error", managementCopy("management.cannot.check.expert.usage.right.now")), true, nil
		}
		if len(references) > 0 {
			b.endManagementPackOperation(openID, messageID)
			notice := managementCopy("management.experts.in.this.pack.are.defaults.for.d.feishu", len(references))
			view, _ := managementPackLibraryViewFromCallback(event.Event.Action)
			b.runManagementAgentPackLibraryViewWithNotice(openID, messageID, view, notice, surface)
			return cardToast("warning", managementCopy("management.this.expert.pack.is.still.used.by.feishu.assistants")), true, nil
		}
		view, _ := managementPackLibraryViewFromCallback(event.Event.Action)
		b.runManagementAgentPackOperation(openID, messageID, managementActionPackRemove, namespace, expectedGeneration, view, surface)
		return cardToast("success", managementCopy("management.removing.experts")), true, nil
	case managementActionStatusOpen:
		updateCard(overviewCard(state), managementViewOverview)
		return cardToast("success", managementCopy("management.assistant.overview.opened")), true, nil
	case managementActionSystemMaintenance:
		updateCard(systemMaintenanceCard(state), managementViewSystem)
		return cardToast("success", managementCopy("management.system.status.opened")), true, nil
	case managementActionSystemRestart, "system.restart.open":
		updateFocusedCard(renderSystemRestartConfirmationCard(state))
		return cardToast("warning", managementCopy("management.confirm.restarting.amux")), true, nil
	case managementActionSystemRestartOK:
		systemInstance := managementCallbackString(event.Event.Action, "system_instance")
		nonce := managementCallbackString(event.Event.Action, "confirmation_nonce")
		if !b.consumeManagementRestartConfirmation(state, systemInstance, nonce) {
			updateCard(systemMaintenanceCard(state), managementViewSystem)
			return cardToast("warning", managementCopy("management.confirmation.expired.try.again")), true, nil
		}
		if !b.beginManagementMaintenance("restart") {
			return cardToast("warning", managementCopy("management.a.system.maintenance.operation.is.already.in.progress")), true, nil
		}
		if b.db == nil {
			b.endManagementMaintenance("restart")
			return cardToast("error", managementCopy("management.cannot.schedule.status.recovery.after.restart.try.again.later")), true, nil
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
			return cardToast("error", managementCopy("management.cannot.schedule.status.recovery.after.restart.try.again.later")), true, nil
		}
		b.runManagementSystemRestart(openID, messageID, surface)
		return cardToast("success", managementCopy("management.restarting.amux")), true, nil
	case managementActionSystemUpdate, "system.update.open":
		if !b.beginManagementMaintenance("update_check") {
			return cardToast("warning", managementCopy("management.a.system.maintenance.operation.is.already.in.progress")), true, nil
		}
		b.runManagementUpdateCheck(openID, messageID, state, surface)
		return cardToast("success", managementCopy("management.checking.for.updates")), true, nil
	case managementActionSystemUpdateDismiss:
		version := managementCallbackString(event.Event.Action, "version")
		if b.managementActions == nil || version == "" {
			return cardToast("error", managementCopy("management.cannot.identify.the.version.to.dismiss")), true, nil
		}
		if err := b.managementActions.DismissUpdate(version); err != nil {
			log.Warn().Err(err).Str("version", version).Msg("bridge: dismiss management update prompt")
			return cardToast("error", managementCopy("management.cannot.save.update.notification.settings.right.now")), true, nil
		}
		updateCard(systemMaintenanceCard(b.managementState()), managementViewSystem)
		return cardToast("success", managementCopy("management.notifications.dismissed.for.this.version")), true, nil
	case managementActionSystemUpgrade:
		systemInstance := managementCallbackString(event.Event.Action, "system_instance")
		nonce := managementCallbackString(event.Event.Action, "confirmation_nonce")
		expectedVersion := managementCallbackString(event.Event.Action, "expected_version")
		if !b.consumeManagementUpgradeConfirmation(state, systemInstance, expectedVersion, nonce) {
			updateCard(systemMaintenanceCard(state), managementViewSystem)
			return cardToast("warning", managementCopy("management.confirmation.expired.check.for.updates.again")), true, nil
		}
		if !b.beginManagementMaintenance("upgrade") {
			return cardToast("warning", managementCopy("management.a.system.maintenance.operation.is.already.in.progress")), true, nil
		}
		if b.db == nil {
			b.endManagementMaintenance("upgrade")
			return cardToast("error", managementCopy("management.cannot.schedule.status.recovery.after.upgrade.try.again.later")), true, nil
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
			return cardToast("error", managementCopy("management.cannot.schedule.status.recovery.after.upgrade.try.again.later")), true, nil
		}
		b.runManagementSystemUpgrade(openID, messageID, expectedVersion, surface)
		return cardToast("success", managementCopy("management.upgrading.amux")), true, nil
	case managementActionHelpOpen:
		helpCard := renderManagementHelpCard(state)
		if surface == managementSurfaceAmux {
			helpCard = renderManagementCompactHelpCard()
		}
		updateCard(helpCard, managementViewHelp)
		return cardToast("success", managementCopy("management.help.opened")), true, nil
	default:
		return cardToast("warning", managementCopy("management.this.action.is.not.supported.yet")), true, nil
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
	NativeModelsStatus              string
	NativeModelsTargetRef           string
	NativeModelsTargetName          string
	ModelServices                   []managementModelService
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
			return state, managementLocalizedError("management.separate.topics.with.user.isolation.are.unavailable.in.this", reason)
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
