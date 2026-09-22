package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/rs/zerolog/log"
)

func renderGroupWatchEditCard(state managementState, draft managementGroupWatchDraft) string {
	draft = managementNormalizeGroupWatchDraft(state, draft)
	changeBehavior := func(field string) []cardBehavior {
		return []cardBehavior{{Type: "callback", Value: managementButtonValue(managementButton{Action: managementActionGroupWatchEdit, Values: map[string]string{"group_watch_mode": draft.Mode, "group_watch_agent": draft.AgentRef, "group_watch_runtime_ref": draft.RuntimeRef, "group_watch_model_ref": draft.ModelRef, "group_watch_reasoning_effort": draft.ReasoningEffort, "group_watch_thinking_process": draft.ThinkingProcess, managementDraftFieldKey: field}})}}
	}
	modeField := managementSelectStaticElement("group_watch_mode", managementCopy("management.choose.task.configuration"), draft.Mode, []cardOption{{Text: managementStyledText(managementCopy("management.inherit.assistant.defaults"), cardTitle{Tag: "plain_text"}), Value: managementDocumentCommentsModeInherit}, {Text: managementStyledText(managementCopy("management.custom"), cardTitle{Tag: "plain_text"}), Value: managementDocumentCommentsModeCustom}})
	modeField.Required = false
	modeField.Behaviors = changeBehavior("group_watch_mode")
	elements := []cardElement{managementHintElement(managementCopy("management.changes.apply.only.to.new.watch.topics.existing.topics")), managementSettingsFieldRow(managementCopy("management.task.configuration"), modeField)}
	if draft.Mode == managementDocumentCommentsModeCustom {
		profileOptions := managementProfileOptions(state, false)
		profileField := managementHintElement(managementCopy("management.no.experts.are.available.add.experts.to.the.library"))
		if len(profileOptions) > 0 {
			profileField = managementSelectStaticElement("group_watch_agent", managementCopy("management.choose.the.expert.for.group.watch"), draft.AgentRef, profileOptions)
			profileField.Required = false
			profileField.Behaviors = changeBehavior("group_watch_agent")
		}
		elements = append(elements, managementSettingsFieldRow(managementCopy("management.expert"), profileField))
		runtimeField := managementSelectStaticElement("group_watch_runtime_ref", managementCopy("management.choose.a.runtime"), draft.RuntimeRef, managementRuntimeOptions(state))
		runtimeField.Required = false
		runtimeField.Behaviors = changeBehavior("group_watch_runtime_ref")
		elements = append(elements, managementSettingsFieldRow(managementCopy("management.runtime.new.watch.topics.only"), runtimeField))
		if draft.RuntimeRef != "" {
			initialModelRef := draft.ModelRef
			if initialModelRef == "" {
				initialModelRef = managementProfileDefaultModelOption
			}
			modelField := managementSelectStaticElement("group_watch_model_ref", managementCopy("management.choose.a.model.supported.by.this.runtime"), initialModelRef, managementRuntimeModelOptions(state, draft.RuntimeRef))
			modelField.Required = false
			modelField.Behaviors = changeBehavior("group_watch_model_ref")
			elements = append(elements, managementSettingsFieldRow(managementCopy("management.model.new.watch.topics.only"), modelField))
			elements = append(elements, modelServiceElements(state, draft.RuntimeRef, map[string]string{"group_watch_mode": draft.Mode, "group_watch_agent": draft.AgentRef, "group_watch_runtime_ref": draft.RuntimeRef, "group_watch_model_ref": draft.ModelRef, "group_watch_reasoning_effort": draft.ReasoningEffort}, managementActionGroupWatchEdit)...)
			initialReasoningEffort := draft.ReasoningEffort
			if initialReasoningEffort == "" {
				initialReasoningEffort = managementRuntimeDefaultReasoningOption
			}
			reasoningField := managementSelectStaticElement("group_watch_reasoning_effort", managementCopy("management.choose.reasoning.effort"), initialReasoningEffort, managementRuntimeReasoningOptions(state, draft.RuntimeRef, draft.ModelRef))
			reasoningField.Required = false
			reasoningField.Behaviors = changeBehavior("group_watch_reasoning_effort")
			elements = append(elements, managementSettingsFieldRow(managementCopy("management.reasoning.effort.new.watch.topics.only"), reasoningField))
		}
	}
	thinkingProcessField := managementSelectStaticElement("group_watch_thinking_process", managementCopy("management.choose.how.activity.is.displayed"), draft.ThinkingProcess, []cardOption{
		{Text: managementStyledText(managementCopy("management.collapsed.default"), cardTitle{Tag: "plain_text"}), Value: config.FeishuThinkingProcessCollapsed},
		{Text: managementStyledText(managementCopy("management.expanded"), cardTitle{Tag: "plain_text"}), Value: config.FeishuThinkingProcessExpanded},
		{Text: managementStyledText(managementCopy("management.hidden"), cardTitle{Tag: "plain_text"}), Value: config.FeishuThinkingProcessHidden},
	})
	thinkingProcessField.Required = false
	thinkingProcessField.Behaviors = changeBehavior("group_watch_thinking_process")
	elements = append(elements, managementSettingsFieldRow(managementCopy("management.activity.display"), thinkingProcessField))
	submit := managementFormSubmitButton(managementActionGroupWatchSave, managementCopy("management.save"))
	submit.Value, _ = json.Marshal(managementButtonValue(managementButton{Action: managementActionGroupWatchSave, Values: map[string]string{"group_watch_mode": draft.Mode, "group_watch_agent": draft.AgentRef, "group_watch_runtime_ref": draft.RuntimeRef, "group_watch_model_ref": draft.ModelRef, "group_watch_reasoning_effort": draft.ReasoningEffort, "group_watch_thinking_process": draft.ThinkingProcess}}))
	buttons := managementFormButtonRowElement(submit, managementCallbackButton(managementActionGroupWatchOpen, managementCopy("management.cancel"), "default"))
	buttons.Margin = "4px 0px 0px 0px"
	elements = append(elements, cardElement{Tag: "form", Name: "group_watch_form", Direction: "vertical", VerticalSpacing: "8px", Elements: []cardElement{buttons}})
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.edit.watch.task.settings"), Subtitle: managementCopy("management.set.the.expert.and.execution.options.for.new.watch"), Summary: managementCopy("management.edit.group.watch.task.settings"), Theme: "blue", Icon: managementIcon("edit_outlined", "blue"), Elements: elements})
}

func renderGroupWatchManagementCardWithNames(state managementState, names map[string]string) string {
	return renderGroupWatchManagementCardWithNoticeAndNames(state, "", names)
}

func renderGroupWatchManagementCardWithNoticeAndNames(state managementState, notice any, names map[string]string) string {
	var status any = managementCopy("management.not.configured")
	if len(state.GroupWatch.ChatIDs) > 0 {
		if state.GroupWatch.Enabled {
			status = managementCopy("management.enabled.d.groups", len(state.GroupWatch.ChatIDs))
		} else {
			status = managementCopy("management.paused.d.groups", len(state.GroupWatch.ChatIDs))
		}
	}
	executionMode := managementCopy("management.inherit.assistant.defaults")
	agentName := managementDefaultAgentName(state)
	executionDraft := managementGroupWatchDraftFromState(state)
	runtimeName := managementRuntimeLabel(state, executionDraft.RuntimeRef)
	modelName := managementRuntimeModelLabel(executionDraft.ModelRef)
	reasoningName := managementReasoningEffortLabel(executionDraft.ReasoningEffort)
	if state.GroupWatch.AgentProfileRef != "" {
		executionMode = managementCopy("management.custom")
		agentName = managementProfileNameByRef(state, state.GroupWatch.AgentProfileRef)
	}
	maxActiveSessions := state.GroupWatch.MaxActiveSessionsPerChat
	if maxActiveSessions < 1 {
		maxActiveSessions = config.DefaultFeishuGroupWatchMaxActiveSessionsPerChat
	}
	idleTimeoutSeconds := state.GroupWatch.IdleTimeoutSeconds
	if idleTimeoutSeconds < 60 {
		idleTimeoutSeconds = config.DefaultFeishuGroupWatchIdleTimeoutSeconds
	}
	var idleTimeout any = (time.Duration(idleTimeoutSeconds) * time.Second).String()
	if idleTimeoutSeconds%(60*60) == 0 {
		idleTimeout = managementCopy("management.d.hours", idleTimeoutSeconds/(60*60))
	}
	elements := []cardElement{managementSubpageBackElement(managementCopy("management.back.to.integrations"), managementActionCapabilitiesOpen, "amux.group_watch.back.capabilities"), managementDividerElement(), managementKeyValueListElement("amux_group_watch_summary", managementSummaryItem{Title: managementCopy("management.current.status"), Body: status}, managementSummaryItem{Title: managementCopy("management.task.configuration"), Body: executionMode}, managementSummaryItem{Title: managementCopy("management.expert"), Body: agentName}, managementSummaryItem{Title: managementCopy("management.runtime"), Body: runtimeName}, managementSummaryItem{Title: managementCopy("management.model"), Body: modelName}, managementSummaryItem{Title: managementCopy("management.reasoning.effort"), Body: reasoningName}, managementSummaryItem{Title: managementCopy("management.activity.display"), Body: managementThinkingProcessLabel(state.GroupWatch.ThinkingProcess)}, managementSummaryItem{Title: managementCopy("management.active.topic.limit.per.group"), Body: fmt.Sprintf("%d", maxActiveSessions)}, managementSummaryItem{Title: managementCopy("management.automatic.sleep"), Body: idleTimeout}), managementHintElement(managementCopy("management.send.amux.in.the.target.group.the.owner.will")), managementButtonRowElement([]managementButton{{Text: managementCopy("management.edit.task.settings"), Type: "default", Action: managementActionGroupWatchEdit, Name: managementActionGroupWatchEdit}})}
	if strings.TrimSpace(managementDefaultText(notice)) != "" {
		elements = append(elements, managementHintElement(managementTruncateText(notice, 240)))
	}
	if state.GroupWatchPermission != nil {
		elements = append(elements, managementTextElement(managementCapabilityPermissionNotice(*state.GroupWatchPermission), "normal", "orange", managementIconPtr("warning_outlined", "orange")))
		var permissionButtons []managementButton
		if len(state.GroupWatchPermission.Scopes) > 0 {
			permissionButtons = append(permissionButtons, managementButton{Text: managementCopy("management.request.permissions"), Type: "primary", Action: managementActionPermissionApply, Name: "amux.capabilities.permissions.apply", Confirm: managementConfirm(managementCopy("management.request.these.permissions.from.the.organization.administrator"), managementCopy("management.the.app.will.request.permissions.from.the.tenant.administrator"))})
		}
		if state.GroupWatchPermission.GrantURL != "" {
			permissionButtons = append(permissionButtons, managementButton{Text: managementCopy("management.developer.console"), Type: "default", URL: state.GroupWatchPermission.GrantURL})
		}
		elements = append(elements, managementButtonRowElement(permissionButtons))
	}
	for index, chatID := range state.GroupWatch.ChatIDs {
		chatName := strings.TrimSpace(names[chatID])
		elements = append(elements, managementTextElement(managementCopy("management.watched.group.s.s", managementValueOrPlaceholder(chatName, managementCopy("management.unnamed.group")), shortChatID(chatID)), "normal", "default", nil), managementButtonRowElement([]managementButton{{Text: managementCopy("management.remove.from.watch"), Type: "default", Action: managementActionGroupWatchUpdate, Name: fmt.Sprintf("%s.remove.%d", managementActionGroupWatchUpdate, index), Values: map[string]string{"operation": "remove", "chat_id": chatID, "chat_name": chatName, "view": "group_watch_list"}}}))
	}
	var buttons []managementButton
	if len(state.GroupWatch.ChatIDs) > 0 {
		operation, text := "resume", managementCopy("management.resume.group.watch")
		if state.GroupWatch.Enabled {
			operation, text = "pause", managementCopy("management.pause.group.watch")
		}
		buttons = append([]managementButton{{Text: text, Type: "primary", Action: managementActionGroupWatchUpdate, Name: managementActionGroupWatchUpdate + "." + operation, Values: map[string]string{"operation": operation}}}, buttons...)
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.group.watch.312"), Subtitle: managementCopy("management.automatically.handle.new.topics.in.selected.groups"), Summary: managementCopy("management.amux.group.watch.management"), Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: elements, Buttons: buttons})
}

func (b *Bridge) resolveGroupWatchNames(ctx context.Context, chatIDs []string) map[string]string {
	names := make(map[string]string, len(chatIDs))
	lookup := b.chatNameLookup
	if lookup == nil {
		lookup = b.fetchChatName
	}
	for _, chatID := range chatIDs {
		chatID = strings.TrimSpace(chatID)
		if chatID == "" {
			continue
		}
		name, err := lookup(ctx, chatID)
		if err != nil {
			log.Warn().Err(err).Str("chat_id", chatID).Msg("bridge: resolve group watch chat name")
			continue
		}
		names[chatID] = strings.TrimSpace(name)
	}
	return names
}

func renderGroupWatchExecutionProcessingCard() string {
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.saving.group.watch.task.settings"), Subtitle: managementCopy("management.affects.only.newly.created.watch.topics"), Summary: managementCopy("management.saving.group.watch.task.settings.316"), Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementHintElement(managementCopy("management.checking.the.expert.and.execution.options"))}})
}

func renderGroupStatusCardWithGroupWatch(ws *FeishuWorkspace, interactionMode string, groupWatchStatus any) string {
	mode := managementCopy("management.owner.mode")
	access := managementCopy("management.only.the.owner.can.mention.the.assistant.to.start")
	if ws != nil && ws.Mode == WorkspaceModeShared {
		mode = managementCopy("management.collaboration.mode")
		access = managementCopy("management.group.members.can.mention.the.assistant.to.start.tasks")
	}
	interaction := managementInteractionText(interactionMode)
	hint := managementCopy("management.the.owner.can.mention.the.assistant.and.send.or")
	if config.GetBridgeFeishuGroupSettingsEnabled() {
		hint = managementConcat(managementCopy("management.the.owner.can.mention.the.assistant.with.amux.in"), hint)
	} else {
		hint = managementConcat(managementInteractionDescription(interactionMode), managementCopy("management.to.change.this.open.assistant.settings.in.private.chat"), hint)
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.current.group.settings"), Subtitle: access, Summary: managementCopy("management.amux.current.group.settings"), Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.access"), Body: mode}, managementSummaryItem{Title: managementCopy("management.message.handling"), Body: interaction}, managementSummaryItem{Title: managementCopy("management.group.watch.312"), Body: managementNonemptyText(groupWatchStatus, managementCopy("management.not.included"))}, managementSummaryItem{Title: managementCopy("management.start.a.task"), Body: managementCopy("management.mention.assistant.task")}), managementHintElement(hint)}})
}

func renderGroupSettingsPausedCard(chatID, chatName string) string {
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.current.group.settings"), Summary: managementCopy("management.per.group.settings.experiment.is.disabled.new.tasks.in"), Theme: "orange", Elements: []cardElement{
		managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.group.name"), Body: managementValueOrPlaceholder(chatName, managementCopy("management.current.group"))}, managementSummaryItem{Title: managementCopy("management.group.id"), Body: chatID}),
		managementHintElement(errGroupSettingsExperimentDisabled.Error()),
	}})
}

func renderGroupWatchCommandConfirmationCard(state managementState, chatID, chatName, operation string) string {
	chatID = strings.TrimSpace(chatID)
	chatName = strings.TrimSpace(chatName)
	operation = strings.TrimSpace(operation)
	actionText, title := managementCopy("management.confirm.enable"), managementCopy("management.enable.group.watch")
	confirmTitle := managementCopy("management.enable.group.watch.332")
	confirmBody := managementCopy("management.new.topics.in.s.will.be.handled.automatically", managementValueOrPlaceholder(chatName, managementCopy("management.unnamed.group")))
	if operation == "add" && !state.GroupWatch.Enabled && len(state.GroupWatch.ChatIDs) > 0 {
		otherGroups := len(state.GroupWatch.ChatIDs)
		if groupWatchConfigHasChat(state.GroupWatch, chatID) {
			otherGroups--
		}
		if otherGroups > 0 {
			confirmBody = managementConcat(confirmBody, managementCopy("management.watch.will.also.resume.for.d.other.configured.groups", otherGroups))
		}
	}
	if operation == "remove" {
		actionText, title = managementCopy("management.confirm.disable"), managementCopy("management.disable.group.watch")
		confirmTitle = managementCopy("management.disable.group.watch.337")
		confirmBody = managementCopy("management.new.topics.in.s.will.no.longer.be.handled", chatName)
	}
	action := managementButton{Text: actionText, Type: "primary", Action: managementActionGroupWatchUpdate, Name: managementActionGroupWatchUpdate + ".command." + operation, Values: map[string]string{"operation": operation, "chat_id": chatID, "chat_name": chatName, "view": "current_group"}, Confirm: managementConfirm(confirmTitle, confirmBody)}
	expertName := managementDefaultAgentName(state)
	if state.GroupWatch.AgentProfileRef != "" {
		expertName = managementProfileNameByRef(state, state.GroupWatch.AgentProfileRef)
	}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: managementCopy("management.check.the.group.name.before.confirming"), Summary: managementCopy("management.amux.group.watch.confirmation"), Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.group.name"), Body: managementValueOrPlaceholder(chatName, managementCopy("management.unnamed.group"))}, managementSummaryItem{Title: managementCopy("management.group.id"), Body: shortChatID(chatID)}, managementSummaryItem{Title: managementCopy("management.current.status"), Body: managementGroupWatchCommandStatus(state, chatID)}, managementSummaryItem{Title: managementCopy("management.execution.expert"), Body: expertName}), managementHintElement(managementCopy("management.affects.only.this.assistant.full.settings.are.in.private"))}, Buttons: []managementButton{action}})
}

func managementGroupWatchCommandStatus(state managementState, chatID string) any {
	if !groupWatchConfigHasChat(state.GroupWatch, chatID) {
		return managementCopy("management.not.included")
	}
	if !state.GroupWatch.Enabled {
		return managementCopy("management.paused")
	}
	return managementCopy("management.enabled")
}

func renderCurrentGroupWatchCard(state managementState, chatID, chatName string, joined bool) string {
	chatID = strings.TrimSpace(chatID)
	chatName = strings.TrimSpace(chatName)
	var status any = managementCopy("management.not.included")
	actionText := managementCopy("management.add.this.group.to.watch")
	if joined {
		status = managementCopy("management.included")
		actionText = managementCopy("management.remove.this.group.from.watch")
	}
	actionButton := managementButton{Text: actionText, Type: "primary", Action: managementActionGroupWatchUpdate, Values: map[string]string{"operation": "add", "chat_id": chatID, "chat_name": chatName, "view": "current_group"}}
	if !joined {
		actionButton.Confirm = managementConfirm(managementCopy("management.confirm.adding.this.group.to.watch"), managementCopy("management.new.topics.in.s.will.be.handled.automatically.after", chatName))
	} else {
		actionButton.Values["operation"] = "remove"
	}
	hint := managementCopy("management.group.watch.handles.new.topics.started.by.members.and")
	buttons := []managementButton{{Text: managementCopy("management.back.to.assistant.settings"), Type: "default", Action: managementActionSettingsOpen}}
	if joined || state.Mode != config.FeishuInteractionSingleton {
		buttons = append([]managementButton{actionButton}, buttons...)
	} else {
		hint = managementCopy("management.group.watch.requires.separate.conversations.per.topic.change.message")
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.current.group.management"), Subtitle: managementCopy("management.manage.only.this.assistant.in.this.group"), Summary: managementCopy("management.amux.current.group.management"), Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.group.name"), Body: managementValueOrPlaceholder(chatName, managementCopy("management.unnamed.group"))}, managementSummaryItem{Title: managementCopy("management.group.id"), Body: shortChatID(chatID)}, managementSummaryItem{Title: managementCopy("management.group.watch.312"), Body: status}, managementSummaryItem{Title: managementCopy("management.default.expert"), Body: managementDefaultAgentName(state)}), managementHintElement(managementCopy("management.you.just.sent.amux.in.this.group")), managementHintElement(hint)}, Buttons: buttons})
}

func shortChatID(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if len(chatID) <= 4 {
		return chatID
	}
	return "…" + chatID[len(chatID)-4:]
}

func renderActiveTopicStatusCard(chat *LarkChat) string {
	var title any = managementCopy("management.current.task")
	var expert any = managementCopy("management.default.expert")
	var status any = managementCopy("management.running")
	if chat != nil {
		if value := strings.TrimSpace(chat.DisplayTitle); value != "" {
			title = value
		}
		expert = strings.TrimSpace(chat.AgentProfile)
		if expert == "" {
			expert = strings.TrimSpace(chat.AgentType)
		}
		if expert == "" {
			expert = managementCopy("management.default.expert")
		}
		if value := strings.TrimSpace(chat.Status); value != "" {
			status = value
		}
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.current.task"), Subtitle: title, Summary: managementCopy("management.amux.current.task"), Theme: "default", Icon: managementIcon("robot_outlined", "grey"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.expert"), Body: expert}, managementSummaryItem{Title: managementCopy("management.status"), Body: status}), managementHintElement(managementCopy("management.continue.sending.messages.to.collaborate.use.interrupt.to.stop"))}})
}
