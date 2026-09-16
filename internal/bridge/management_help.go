package bridge

import "github.com/amuxos/amux/internal/bridge/command"

func renderManagementHelpCard(state managementState) string {
	mode := managementNormalizeDraft(managementDraftFromState(state)).Interaction
	elements := []cardElement{
		managementCompactSummaryElement(managementSummaryItem{Title: managementMessageHandlingSummary, Body: managementInteractionText(mode)}),
		managementSectionTitleElement(managementInteractionHelpTitle(mode)),
		managementHelpBlockElement(managementTextElement(managementInteractionHelpBody(mode), "normal", "default", nil)),
		managementSectionTitleElement("云文档评论"),
		managementHelpBlockElement(
			managementTextElement("开启：前往「助手设置 → 接入能力」开启云文档评论。", "normal", "default", managementIconPtr("setting_outlined", "grey")),
			managementTextElement("使用：在评论中 @智能体 并提出要求；仅负责人可用。", "normal", "default", managementIconPtr("robot_outlined", "grey")),
			managementTextElement("话题：每条主评论独立处理；在同一评论下继续 @智能体 可沿用原话题。", "normal", "default", managementIconPtr("info_outlined", "grey")),
			managementTextElement("专家：新话题使用当前默认专家；已创建话题不会随默认专家变化。", "normal", "default", managementIconPtr("info_outlined", "grey")),
			managementTextElement("正文：助手只通过评论回复，不会修改文档正文。", "normal", "default", managementIconPtr("info_outlined", "grey"))),
		managementSectionTitleElement("群值守"),
		managementHelpBlockElement(
			managementTextElement("开启：在目标群 @智能体 发送“开启群值守”，再由负责人确认。", "normal", "default", managementIconPtr("setting_outlined", "grey")),
			managementTextElement("关闭：在目标群 @智能体 发送“关闭群值守”，确认后移出当前群。", "normal", "default", managementIconPtr("setting_outlined", "grey")),
			managementTextElement("管理：私聊发送 /amux，前往「助手设置 → 接入能力 → 群值守」。", "normal", "default", managementIconPtr("info_outlined", "grey"))),
		managementSectionTitleElement("让群里的其他人使用"),
		managementHelpBlockElement(managementTextElement(managementGroupCollaborationHintText(), "normal", "default", nil)),
	}
	elements = append(elements, managementCommandHelpElements(command.ScopePrivate)...)
	return marshalNativeManagementCard(managementCardView{Title: "使用帮助", Subtitle: "直接发送任务即可开始", Summary: "amux 使用帮助", Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: elements})
}
func renderManagementCompactHelpCard() string {
	return marshalNativeManagementCard(managementCardView{Title: "使用帮助", Subtitle: "选择当前场景的使用方式", Summary: "amux 使用帮助", Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: []cardElement{managementSectionTitleElement("开始任务"), managementHelpBlockElement(managementTextElement("私聊：直接发送任务", "normal", "default", managementIconPtr("robot_outlined", "grey")), managementTextElement("群聊：@智能体 + 任务", "normal", "default", managementIconPtr("robot_outlined", "grey")), managementTextElement("话题：继续回复即可补充要求", "normal", "default", managementIconPtr("info_outlined", "grey"))), managementSectionTitleElement("其他入口"), managementHelpBlockElement(managementTextElement("云文档：在评论中 @智能体", "normal", "default", managementIconPtr("info_outlined", "grey")), managementTextElement("/help：查看当前场景的完整命令", "normal", "default", managementIconPtr("info_outlined", "grey")))}})
}
func renderWorkspaceHelpCard() string {
	elements := []cardElement{managementHelpBlockElement(managementTextElement("@智能体 + 任务即可开始，无需额外配置。", "normal", "default", nil)), managementSectionTitleElement("群聊入口"), managementHelpBlockElement(managementTextElement("@智能体 + 任务　让助手开始处理", "normal", "default", managementIconPtr("robot_outlined", "grey")))}
	elements = append(elements, managementCommandHelpElements(command.ScopeGroup)...)
	return marshalNativeManagementCard(managementCardView{Title: "群聊帮助", Subtitle: "@智能体 + 任务即可开始", Summary: "amux 群聊帮助", Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: elements})
}
func renderSessionEntryHelpCard() string {
	elements := []cardElement{managementHelpBlockElement(managementTextElement("@智能体 + 任务即可在这个话题开始。", "normal", "default", nil)), managementSectionTitleElement("开始任务"), managementHelpBlockElement(managementTextElement("@智能体 + 任务　让助手创建任务", "normal", "default", managementIconPtr("robot_outlined", "grey")))}
	elements = append(elements, managementCommandHelpElements(command.ScopeFreshTopic)...)
	return marshalNativeManagementCard(managementCardView{Title: "开始任务", Subtitle: "@智能体 + 任务即可开始", Summary: "amux 任务入口帮助", Theme: "default", Icon: managementIcon("robot_outlined", "grey"), Elements: elements})
}
func renderBoundTopicHelpCard() string {
	elements := []cardElement{managementHelpBlockElement(managementTextElement("继续发送消息即可与助手协作。", "normal", "default", nil))}
	elements = append(elements, managementCommandHelpElements(command.ScopeActiveTopic)...)
	return marshalNativeManagementCard(managementCardView{Title: "话题帮助", Subtitle: "继续发送消息即可协作", Summary: "amux 话题帮助", Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: elements})
}
func managementCommandHelpElements(scope command.Scope) []cardElement {
	items := command.Help(scope)
	elements := make([]cardElement, 0, len(items)*2)
	currentSection := ""
	var rows []cardElement
	flush := func() {
		if currentSection == "" || len(rows) == 0 {
			return
		}
		elements = append(elements, managementSectionTitleElement(currentSection), managementHelpBlockElement(rows...))
		rows = nil
	}
	for _, item := range items {
		if item.Section != currentSection {
			flush()
			currentSection = item.Section
		}
		rows = append(rows, managementTextElement(item.Command+"　"+item.Description, "normal", "default", managementIconPtr("info_outlined", "grey")))
	}
	flush()
	return elements
}

func managementHelpBlockElement(rows ...cardElement) cardElement {
	element := managementVerticalSummaryElement("", rows...)
	element.Columns[0].VerticalSpacing = "6px"
	return element
}
