package bridge

import (
	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/distribution"
)

func renderManagementHelpCard(state managementState) string {
	mode := managementNormalizeDraft(managementDraftFromState(state)).Interaction
	elements := []cardElement{
		managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.message.handling.summary"), Body: managementInteractionText(mode)}),
		managementSectionTitleElement(managementInteractionHelpTitle(mode)),
		managementHelpBlockElement(managementTextElement(managementInteractionHelpBody(mode), "normal", "default", nil)),
		managementSectionTitleElement(managementCopy("management.document.comments")),
		managementHelpBlockElement(
			managementTextElement(managementCopy("management.enable.go.to.assistant.settings.integrations.and.enable.document"), "normal", "default", managementIconPtr("setting_outlined", "grey")),
			managementTextElement(managementCopy("management.use.mention.the.assistant.in.a.comment.with.your"), "normal", "default", managementIconPtr("robot_outlined", "grey")),
			managementTextElement(managementCopy("management.topics.each.root.comment.is.independent.mention.the.assistant"), "normal", "default", managementIconPtr("info_outlined", "grey")),
			managementTextElement(managementCopy("management.expert.new.topics.use.the.current.default.expert.existing"), "normal", "default", managementIconPtr("info_outlined", "grey")),
			managementTextElement(managementCopy("management.body.the.assistant.replies.through.comments.and.does.not"), "normal", "default", managementIconPtr("info_outlined", "grey"))),
		managementSectionTitleElement(managementCopy("management.group.watch.312")),
		managementHelpBlockElement(
			managementTextElement(managementCopy("management.enable.mention.the.assistant.in.the.target.group.and"), "normal", "default", managementIconPtr("setting_outlined", "grey")),
			managementTextElement(managementCopy("management.disable.mention.the.assistant.in.the.target.group.and"), "normal", "default", managementIconPtr("setting_outlined", "grey")),
			managementTextElement(managementCopy("management.manage.send.amux.privately.then.open.assistant.settings.integrations"), "normal", "default", managementIconPtr("info_outlined", "grey"))),
		managementSectionTitleElement(managementCopy("management.let.other.group.members.use.the.assistant")),
		managementHelpBlockElement(managementTextElement(managementGroupCollaborationHintText(), "normal", "default", nil)),
	}
	elements = append(elements, managementCommandHelpElements(command.ScopePrivate)...)
	if state.GoalSupported {
		elements = append(elements, managementSectionTitleElement(sessionMessage("goal.title")), managementHintElement(sessionMessage("goal.usage")))
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.help"), Subtitle: managementCopy("management.send.a.task.directly.to.begin"), Summary: managementCopy("management.amux.help"), Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: elements})
}

func renderManagementCompactHelpCard() string {
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.help"), Subtitle: managementCopy("management.choose.instructions.for.your.current.context"), Summary: managementCopy("management.amux.help"), Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: []cardElement{managementSectionTitleElement(managementCopy("management.start.a.task")), managementHelpBlockElement(managementTextElement(managementCopy("management.private.chat.send.a.task.directly"), "normal", "default", managementIconPtr("robot_outlined", "grey")), managementTextElement(managementCopy("management.group.chat.mention.the.assistant.with.a.task"), "normal", "default", managementIconPtr("robot_outlined", "grey")), managementTextElement(managementCopy("management.topic.reply.to.add.requirements"), "normal", "default", managementIconPtr("info_outlined", "grey"))), managementSectionTitleElement(managementCopy("management.other.entry.points")), managementHelpBlockElement(managementTextElement(managementCopy("management.documents.mention.the.assistant.in.a.comment"), "normal", "default", managementIconPtr("info_outlined", "grey")), managementTextElement(managementCopy("management.help.view.all.commands.for.the.current.context"), "normal", "default", managementIconPtr("info_outlined", "grey")))}})
}

func renderWorkspaceHelpCard() string {
	elements := []cardElement{managementHelpBlockElement(managementTextElement(managementCopy("management.mention.the.assistant.with.a.task.to.begin.no"), "normal", "default", nil)), managementSectionTitleElement(managementCopy("management.group.chat.entry")), managementHelpBlockElement(managementTextElement(managementCopy("management.mention.assistant.task.start.working"), "normal", "default", managementIconPtr("robot_outlined", "grey")))}
	elements = append(elements, managementCommandHelpElements(command.ScopeGroup)...)
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.group.chat.help"), Subtitle: managementCopy("management.mention.the.assistant.with.a.task.to.begin"), Summary: managementCopy("management.amux.group.chat.help"), Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: elements})
}

func renderSessionEntryHelpCard() string {
	elements := []cardElement{managementHelpBlockElement(managementTextElement(managementCopy("management.mention.the.assistant.with.a.task.to.begin.in"), "normal", "default", nil)), managementSectionTitleElement(managementCopy("management.start.a.task")), managementHelpBlockElement(managementTextElement(managementCopy("management.mention.assistant.task.create.a.task"), "normal", "default", managementIconPtr("robot_outlined", "grey")))}
	elements = append(elements, managementCommandHelpElements(command.ScopeFreshTopic)...)
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.start.a.task"), Subtitle: managementCopy("management.mention.the.assistant.with.a.task.to.begin"), Summary: managementCopy("management.amux.task.entry.help"), Theme: "default", Icon: managementIcon("robot_outlined", "grey"), Elements: elements})
}

func renderBoundTopicHelpCard() string {
	return renderBoundTopicHelpCardWithGoal(false)
}

func renderBoundTopicHelpCardWithGoal(supported bool) string {
	elements := []cardElement{managementHelpBlockElement(managementTextElement(managementCopy("management.keep.sending.messages.to.collaborate.with.the.assistant"), "normal", "default", nil))}
	elements = append(elements, managementCommandHelpElements(command.ScopeActiveTopic)...)
	if supported {
		elements = append(elements, managementSectionTitleElement(sessionMessage("goal.title")), managementHintElement(sessionMessage("goal.usage")))
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.topic.help"), Subtitle: managementCopy("management.keep.sending.messages.to.collaborate"), Summary: managementCopy("management.amux.topic.help"), Theme: "default", Icon: managementIcon("info_outlined", "grey"), Elements: elements})
}

func managementCommandHelpElements(scope command.Scope) []cardElement {
	items := command.Help(scope)
	elements := make([]cardElement, 0, len(items)*2)
	currentSection := ""
	var sectionText any
	var rows []cardElement
	flush := func() {
		if currentSection == "" || len(rows) == 0 {
			return
		}
		elements = append(elements, managementSectionTitleElement(sectionText), managementHelpBlockElement(rows...))
		rows = nil
	}
	for _, item := range items {
		if item.Section != currentSection {
			flush()
			currentSection = item.Section
			sectionText = item.Section
			if item.SectionID != "agent" {
				sectionText = managementCopy("management.help.section." + item.SectionID)
			}
		}
		description := managementCopy("management.help." + string(scope) + "." + string(item.ID))
		if scope == command.ScopeActiveTopic && item.ID == command.CommandSkill {
			description = managementCopy("management.help.active_topic.skill", distribution.CodexCompatibleLabel)
		}
		rows = append(rows, managementTextElement(managementConcat(item.Command, "　", description), "normal", "default", managementIconPtr("info_outlined", "grey")))
	}
	flush()
	return elements
}

func managementHelpBlockElement(rows ...cardElement) cardElement {
	element := managementVerticalSummaryElement("", rows...)
	element.Columns[0].VerticalSpacing = "6px"
	return element
}
