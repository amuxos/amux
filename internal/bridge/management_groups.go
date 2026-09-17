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
	modeField := managementSelectStaticElement("group_watch_mode", "选择任务配置", draft.Mode, []cardOption{{Text: cardTitle{Tag: "plain_text", Content: "继承助手默认"}, Value: managementDocumentCommentsModeInherit}, {Text: cardTitle{Tag: "plain_text", Content: "自定义"}, Value: managementDocumentCommentsModeCustom}})
	modeField.Required = false
	modeField.Behaviors = changeBehavior("group_watch_mode")
	elements := []cardElement{managementHintElement("保存后仅影响之后创建的值守话题；已有话题继续使用创建时的专家和模型。"), managementSettingsFieldRow("任务配置", modeField)}
	if draft.Mode == managementDocumentCommentsModeCustom {
		profileOptions := managementProfileOptions(state, false)
		profileField := managementHintElement("专家库中暂无可用专家，请先添加专家。")
		if len(profileOptions) > 0 {
			profileField = managementSelectStaticElement("group_watch_agent", "选择群值守使用的专家", draft.AgentRef, profileOptions)
			profileField.Required = false
			profileField.Behaviors = changeBehavior("group_watch_agent")
		}
		elements = append(elements, managementSettingsFieldRow("专家", profileField))
		runtimeField := managementSelectStaticElement("group_watch_runtime_ref", "选择运行引擎", draft.RuntimeRef, managementRuntimeOptions(state))
		runtimeField.Required = false
		runtimeField.Behaviors = changeBehavior("group_watch_runtime_ref")
		elements = append(elements, managementSettingsFieldRow("运行引擎（仅影响新值守话题）", runtimeField))
		if draft.RuntimeRef != "" {
			initialModelRef := draft.ModelRef
			if initialModelRef == "" {
				initialModelRef = managementProfileDefaultModelOption
			}
			modelField := managementSelectStaticElement("group_watch_model_ref", "选择该运行引擎支持的模型", initialModelRef, managementRuntimeModelOptions(state, draft.RuntimeRef))
			modelField.Required = false
			modelField.Behaviors = changeBehavior("group_watch_model_ref")
			elements = append(elements, managementSettingsFieldRow("模型（仅影响新值守话题）", modelField))
			initialReasoningEffort := draft.ReasoningEffort
			if initialReasoningEffort == "" {
				initialReasoningEffort = managementRuntimeDefaultReasoningOption
			}
			reasoningField := managementSelectStaticElement("group_watch_reasoning_effort", "选择思考强度", initialReasoningEffort, managementRuntimeReasoningOptions(state, draft.RuntimeRef, draft.ModelRef))
			reasoningField.Required = false
			reasoningField.Behaviors = changeBehavior("group_watch_reasoning_effort")
			elements = append(elements, managementSettingsFieldRow("思考强度（仅影响新值守话题）", reasoningField))
		}
	}
	thinkingProcessField := managementSelectStaticElement("group_watch_thinking_process", "选择处理过程展示方式", draft.ThinkingProcess, []cardOption{
		{Text: cardTitle{Tag: "plain_text", Content: "折叠展示（默认）"}, Value: config.FeishuThinkingProcessCollapsed},
		{Text: cardTitle{Tag: "plain_text", Content: "展开展示"}, Value: config.FeishuThinkingProcessExpanded},
		{Text: cardTitle{Tag: "plain_text", Content: "不展示"}, Value: config.FeishuThinkingProcessHidden},
	})
	thinkingProcessField.Required = false
	thinkingProcessField.Behaviors = changeBehavior("group_watch_thinking_process")
	elements = append(elements, managementSettingsFieldRow("处理过程展示", thinkingProcessField))
	submit := managementFormSubmitButton(managementActionGroupWatchSave, "保存")
	submit.Value, _ = json.Marshal(managementButtonValue(managementButton{Action: managementActionGroupWatchSave, Values: map[string]string{"group_watch_mode": draft.Mode, "group_watch_agent": draft.AgentRef, "group_watch_runtime_ref": draft.RuntimeRef, "group_watch_model_ref": draft.ModelRef, "group_watch_reasoning_effort": draft.ReasoningEffort, "group_watch_thinking_process": draft.ThinkingProcess}}))
	buttons := managementFormButtonRowElement(submit, managementCallbackButton(managementActionGroupWatchOpen, "取消", "default"))
	buttons.Margin = "4px 0px 0px 0px"
	elements = append(elements, cardElement{Tag: "form", Name: "group_watch_form", Direction: "vertical", VerticalSpacing: "8px", Elements: []cardElement{buttons}})
	return marshalNativeManagementCard(managementCardView{Title: "修改值守任务配置", Subtitle: "设置新值守话题使用的专家和执行参数", Summary: "修改群值守任务配置", Theme: "blue", Icon: managementIcon("edit_outlined", "blue"), Elements: elements})
}
func renderGroupWatchManagementCardWithNames(state managementState, names map[string]string) string {
	return renderGroupWatchManagementCardWithNoticeAndNames(state, "", names)
}
func renderGroupWatchManagementCardWithNoticeAndNames(state managementState, notice string, names map[string]string) string {
	status := "未配置"
	if len(state.GroupWatch.ChatIDs) > 0 {
		if state.GroupWatch.Enabled {
			status = fmt.Sprintf("已开启 · %d 个群", len(state.GroupWatch.ChatIDs))
		} else {
			status = fmt.Sprintf("已暂停 · %d 个群", len(state.GroupWatch.ChatIDs))
		}
	}
	executionMode := "继承助手默认"
	agentName := managementDefaultAgentName(state)
	executionDraft := managementGroupWatchDraftFromState(state)
	runtimeName := managementRuntimeLabel(state, executionDraft.RuntimeRef)
	modelName := managementRuntimeModelLabel(executionDraft.ModelRef)
	reasoningName := managementReasoningEffortLabel(executionDraft.ReasoningEffort)
	if state.GroupWatch.AgentProfileRef != "" {
		executionMode = "自定义"
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
	idleTimeout := (time.Duration(idleTimeoutSeconds) * time.Second).String()
	if idleTimeoutSeconds%(60*60) == 0 {
		idleTimeout = fmt.Sprintf("%d 小时", idleTimeoutSeconds/(60*60))
	}
	elements := []cardElement{managementSubpageBackElement("返回接入能力", managementActionCapabilitiesOpen, "amux.group_watch.back.capabilities"), managementDividerElement(), managementKeyValueListElement("amux_group_watch_summary", managementSummaryItem{Title: "当前状态", Body: status}, managementSummaryItem{Title: "任务配置", Body: executionMode}, managementSummaryItem{Title: "专家", Body: agentName}, managementSummaryItem{Title: "运行引擎", Body: runtimeName}, managementSummaryItem{Title: "模型", Body: modelName}, managementSummaryItem{Title: "思考强度", Body: reasoningName}, managementSummaryItem{Title: "处理过程展示", Body: managementThinkingProcessLabel(state.GroupWatch.ThinkingProcess)}, managementSummaryItem{Title: "每群活跃上限", Body: fmt.Sprintf("%d", maxActiveSessions)}, managementSummaryItem{Title: "自动休眠", Body: idleTimeout}), managementHintElement("在目标群发送 /amux，负责人会收到加入值守的入口。"), managementButtonRowElement([]managementButton{{Text: "修改任务配置", Type: "default", Action: managementActionGroupWatchEdit, Name: managementActionGroupWatchEdit}})}
	if strings.TrimSpace(notice) != "" {
		elements = append(elements, managementHintElement(truncateCardText(notice, 240)))
	}
	if state.GroupWatchPermission != nil {
		elements = append(elements, managementTextElement(capabilityPermissionNotice(*state.GroupWatchPermission), "normal", "orange", managementIconPtr("warning_outlined", "orange")))
		var permissionButtons []managementButton
		if len(state.GroupWatchPermission.Scopes) > 0 {
			permissionButtons = append(permissionButtons, managementButton{Text: "申请权限", Type: "primary", Action: managementActionPermissionApply, Name: "amux.capabilities.permissions.apply", Confirm: managementConfirm("向企业管理员申请这些权限？", "应用会向租户管理员发起权限授权申请；审核生效后能力自动恢复，可能需要发布应用版本。")})
		}
		if state.GroupWatchPermission.GrantURL != "" {
			permissionButtons = append(permissionButtons, managementButton{Text: "开发者后台", Type: "default", URL: state.GroupWatchPermission.GrantURL})
		}
		elements = append(elements, managementButtonRowElement(permissionButtons))
	}
	for index, chatID := range state.GroupWatch.ChatIDs {
		chatName := strings.TrimSpace(names[chatID])
		if chatName == "" {
			chatName = "未命名群"
		}
		elements = append(elements, managementTextElement(fmt.Sprintf("值守群：%s（%s）", chatName, shortChatID(chatID)), "normal", "default", nil), managementButtonRowElement([]managementButton{{Text: "移出值守", Type: "default", Action: managementActionGroupWatchUpdate, Name: fmt.Sprintf("%s.remove.%d", managementActionGroupWatchUpdate, index), Values: map[string]string{"operation": "remove", "chat_id": chatID, "chat_name": chatName, "view": "group_watch_list"}}}))
	}
	var buttons []managementButton
	if len(state.GroupWatch.ChatIDs) > 0 {
		operation, text := "resume", "恢复群值守"
		if state.GroupWatch.Enabled {
			operation, text = "pause", "暂停群值守"
		}
		buttons = append([]managementButton{{Text: text, Type: "primary", Action: managementActionGroupWatchUpdate, Name: managementActionGroupWatchUpdate + "." + operation, Values: map[string]string{"operation": operation}}}, buttons...)
	}
	return marshalNativeManagementCard(managementCardView{Title: "群值守", Subtitle: "自动处理指定群的新话题", Summary: "amux 群值守管理", Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: elements, Buttons: buttons})
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
	return marshalNativeManagementCard(managementCardView{Title: "正在保存值守任务配置", Subtitle: "只影响之后创建的值守话题", Summary: "正在保存群值守任务配置", Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementHintElement("正在检查专家和执行参数。")}})
}
func renderGroupStatusCardWithGroupWatch(ws *FeishuWorkspace, interactionMode, groupWatchStatus string) string {
	mode := "负责人模式"
	access := "只有负责人可以 @智能体 发起任务"
	if ws != nil && ws.Mode == WorkspaceModeShared {
		mode = "协作模式"
		access = "群成员都可以 @智能体 发起任务"
	}
	interaction := managementInteractionText(interactionMode)
	hint := "负责人可 @智能体 发送“开启协作模式”或“关闭协作模式”，以及“开启群值守”或“关闭群值守”。"
	if config.GetBridgeFeishuGroupSettingsEnabled() {
		hint = "负责人可在本群 @智能体 发送 /amux，并在私聊的“当前群设置”中修改。新执行参数只用于新会话，已有话题保留创建时的设置。\n" + hint
	} else {
		hint = managementInteractionDescription(interactionMode) + " 如需修改，请私聊智能体打开“助手设置”。\n" + hint
	}
	return marshalNativeManagementCard(managementCardView{Title: "当前群设置", Subtitle: access, Summary: "amux 当前群设置", Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: "使用权限", Body: mode}, managementSummaryItem{Title: "处理方式", Body: interaction}, managementSummaryItem{Title: "群值守", Body: valueOrPlaceholder(strings.TrimSpace(groupWatchStatus), "未加入")}, managementSummaryItem{Title: "开始任务", Body: "@智能体 + 任务"}), managementHintElement(hint)}})
}

func renderGroupSettingsPausedCard(chatID, chatName string) string {
	return marshalNativeManagementCard(managementCardView{Title: "当前群设置", Summary: "群独立配置实验已关闭，本群已暂停新任务", Theme: "orange", Elements: []cardElement{
		managementCompactSummaryElement(managementSummaryItem{Title: "群名称", Body: valueOrPlaceholder(chatName, "当前群")}, managementSummaryItem{Title: "群标识", Body: chatID}),
		managementHintElement(errGroupSettingsExperimentDisabled.Error()),
	}})
}
func renderGroupWatchCommandConfirmationCard(state managementState, chatID, chatName, operation string) string {
	chatID = strings.TrimSpace(chatID)
	chatName = strings.TrimSpace(chatName)
	if chatName == "" {
		chatName = "未命名群"
	}
	operation = strings.TrimSpace(operation)
	actionText, title := "确认开启", "开启群值守"
	confirmTitle := "确认开启群值守？"
	confirmBody := fmt.Sprintf("开启后，“%s”的新话题会自动交给助手处理。", chatName)
	if operation == "add" && !state.GroupWatch.Enabled && len(state.GroupWatch.ChatIDs) > 0 {
		otherGroups := len(state.GroupWatch.ChatIDs)
		if groupWatchConfigHasChat(state.GroupWatch, chatID) {
			otherGroups--
		}
		if otherGroups > 0 {
			confirmBody += fmt.Sprintf(" 当前智能体另有 %d 个已配置群，也会一并恢复值守。", otherGroups)
		}
	}
	if operation == "remove" {
		actionText, title = "确认关闭", "关闭群值守"
		confirmTitle = "确认关闭群值守？"
		confirmBody = fmt.Sprintf("关闭后，“%s”的新话题不再自动处理；已有话题不受影响。", chatName)
	}
	action := managementButton{Text: actionText, Type: "primary", Action: managementActionGroupWatchUpdate, Name: managementActionGroupWatchUpdate + ".command." + operation, Values: map[string]string{"operation": operation, "chat_id": chatID, "chat_name": chatName, "view": "current_group"}, Confirm: managementConfirm(confirmTitle, confirmBody)}
	expertName := managementDefaultAgentName(state)
	if state.GroupWatch.AgentProfileRef != "" {
		expertName = managementProfileNameByRef(state, state.GroupWatch.AgentProfileRef)
	}
	return marshalNativeManagementCard(managementCardView{Title: title, Subtitle: "请核对群名称后确认", Summary: "amux 群值守确认", Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: "群名称", Body: chatName}, managementSummaryItem{Title: "群标识", Body: shortChatID(chatID)}, managementSummaryItem{Title: "当前状态", Body: managementGroupWatchCommandStatus(state, chatID)}, managementSummaryItem{Title: "执行专家", Body: expertName}), managementHintElement("仅影响当前智能体；完整设置可在私聊 /amux 的“接入能力 → 群值守”中管理。")}, Buttons: []managementButton{action}})
}
func managementGroupWatchCommandStatus(state managementState, chatID string) string {
	if !groupWatchConfigHasChat(state.GroupWatch, chatID) {
		return "未加入"
	}
	if !state.GroupWatch.Enabled {
		return "已暂停"
	}
	return "已开启"
}
func renderCurrentGroupWatchCard(state managementState, chatID, chatName string, joined bool) string {
	chatID = strings.TrimSpace(chatID)
	chatName = strings.TrimSpace(chatName)
	if chatName == "" {
		chatName = "未命名群"
	}
	status := "未加入"
	actionText := "将本群加入值守"
	if joined {
		status = "已加入"
		actionText = "将本群移出值守"
	}
	actionButton := managementButton{Text: actionText, Type: "primary", Action: managementActionGroupWatchUpdate, Values: map[string]string{"operation": "add", "chat_id": chatID, "chat_name": chatName, "view": "current_group"}}
	if !joined {
		actionButton.Confirm = managementConfirm("确认加入群值守？", fmt.Sprintf("加入后，“%s”的新话题会自动交给助手处理。", chatName))
	} else {
		actionButton.Values["operation"] = "remove"
	}
	hint := "群值守会自动处理群成员和智能体发起的新话题；每个话题独立处理。"
	buttons := []managementButton{{Text: "返回助手设置", Type: "default", Action: managementActionSettingsOpen}}
	if joined || state.Mode != config.FeishuInteractionSingleton {
		buttons = append([]managementButton{actionButton}, buttons...)
	} else {
		hint = "群值守仅支持“每个话题独立”。请先返回助手修改处理方式。"
	}
	return marshalNativeManagementCard(managementCardView{Title: "当前群管理", Subtitle: "仅管理当前智能体在本群的设置", Summary: "amux 当前群管理", Theme: "blue", Icon: managementIcon("setting_outlined", "blue"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: "群名称", Body: chatName}, managementSummaryItem{Title: "群标识", Body: shortChatID(chatID)}, managementSummaryItem{Title: "群值守", Body: status}, managementSummaryItem{Title: "默认专家", Body: managementDefaultAgentName(state)}), managementHintElement("你刚刚在该群发送了 /amux"), managementHintElement(hint)}, Buttons: buttons})
}
func shortChatID(chatID string) string {
	chatID = strings.TrimSpace(chatID)
	if len(chatID) <= 4 {
		return chatID
	}
	return "…" + chatID[len(chatID)-4:]
}
func renderActiveTopicStatusCard(chat *LarkChat) string {
	title := "当前任务"
	expert := "默认专家"
	status := "运行中"
	if chat != nil {
		if value := strings.TrimSpace(chat.DisplayTitle); value != "" {
			title = value
		}
		expert = strings.TrimSpace(chat.AgentProfile)
		if expert == "" {
			expert = strings.TrimSpace(chat.AgentType)
		}
		if expert == "" {
			expert = "默认专家"
		}
		if value := strings.TrimSpace(chat.Status); value != "" {
			status = value
		}
	}
	return marshalNativeManagementCard(managementCardView{Title: "当前任务", Subtitle: title, Summary: "amux 当前任务", Theme: "default", Icon: managementIcon("robot_outlined", "grey"), Elements: []cardElement{managementCompactSummaryElement(managementSummaryItem{Title: "专家", Body: expert}, managementSummaryItem{Title: "状态", Body: status}), managementHintElement("继续发送消息即可协作；使用 /interrupt 停止当前回答，使用 /stop 结束任务。")}})
}
