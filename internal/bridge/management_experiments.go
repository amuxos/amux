package bridge

import (
	"strconv"
	"strings"

	"github.com/amuxos/amux/internal/config"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func managementCompletionSettingsFromCallback(action *callback.CallBackAction, saved config.CompletionNotificationSettings) (config.CompletionNotificationSettings, error) {
	value, _ := managementExperimentCallbackValue(action, "completion_enabled")
	enabled, _ := value.(string)
	enabled = strings.TrimSpace(enabled)
	if enabled != "true" && enabled != "false" {
		return saved, managementLocalizedError("management.choose.on.or.off")
	}
	saved.Enabled = enabled == "true"
	if value, present := managementExperimentCallbackValue(action, "completion_seconds"); present {
		raw, ok := value.(string)
		if !ok {
			return saved, managementLocalizedError("management.the.notification.threshold.must.be.an.integer.from.0")
		}
		if strings.TrimSpace(raw) == "" {
			return saved, nil
		}
		seconds, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || seconds < 0 || seconds > 604800 {
			return saved, managementLocalizedError("management.the.notification.threshold.must.be.an.integer.from.0")
		}
		saved.AfterSeconds = seconds
	}
	return saved, nil
}

func managementExperimentCallbackValue(action *callback.CallBackAction, key string) (any, bool) {
	if action == nil {
		return nil, false
	}
	if value, present := action.Value[key]; present {
		return value, true
	}
	value, present := action.FormValue[key]
	return value, present
}

func managementExperimentsSettingsFromCallback(action *callback.CallBackAction, saved config.ExperimentsSettings) (config.ExperimentsSettingsUpdate, error) {
	completion, err := managementCompletionSettingsFromCallback(action, saved.CompletionNotification)
	if err != nil {
		return config.ExperimentsSettingsUpdate{}, err
	}
	update := config.ExperimentsSettingsUpdate{CompletionNotification: completion}
	if raw, present := managementExperimentCallbackValue(action, "group_settings_enabled"); present {
		enabled, _ := raw.(string)
		enabled = strings.TrimSpace(enabled)
		if enabled != "true" && enabled != "false" {
			return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.choose.whether.to.enable.per.group.settings")
		}
		value := enabled == "true"
		update.GroupSettingsEnabled = &value
	}
	if raw, present := managementExperimentCallbackValue(action, "memory_enabled"); present {
		enabled, _ := raw.(string)
		enabled = strings.TrimSpace(enabled)
		if enabled != "true" && enabled != "false" {
			return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.choose.whether.to.enable.agent.memory")
		}
		value := enabled == "true"
		update.MemoryEnabled = &value
	}
	if raw, present := managementExperimentCallbackValue(action, "steer_foreground_enabled"); present {
		enabled, _ := raw.(string)
		enabled = strings.TrimSpace(enabled)
		if enabled != "true" && enabled != "false" {
			return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.choose.whether.to.enable.steer.foreground")
		}
		value := enabled == "true"
		update.SteerForeground = &value
	}
	if raw, present := managementExperimentCallbackValue(action, "show_background_tasks_enabled"); present {
		enabled, _ := raw.(string)
		enabled = strings.TrimSpace(enabled)
		if enabled != "true" && enabled != "false" {
			return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.choose.whether.to.show.background.tasks")
		}
		value := enabled == "true"
		update.ShowBackgroundTasks = &value
	}
	if raw, present := managementExperimentCallbackValue(action, "mention_turn_initiator_enabled"); present {
		enabled, _ := raw.(string)
		enabled = strings.TrimSpace(enabled)
		if enabled != "true" && enabled != "false" {
			return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.choose.whether.to.mention.turn.initiators")
		}
		value := enabled == "true"
		update.MentionTurnInitiator = &value
	}
	if raw, present := managementExperimentCallbackValue(action, "codex_executable"); present {
		value, ok := raw.(string)
		if !ok || !config.ValidAgentExecutable(value) {
			return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.codex.executable.invalid")
		}
		value = strings.TrimSpace(value)
		update.CodexExecutable = &value
	}
	for _, agent := range config.AgentExecutableNames() {
		if agent == "codex" {
			continue
		}
		if raw, present := managementExperimentCallbackValue(action, agent+"_executable"); present {
			value, ok := raw.(string)
			if !ok || !config.ValidAgentExecutable(value) {
				return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.launcher.invalid")
			}
			if update.AgentExecutables == nil {
				update.AgentExecutables = map[string]string{}
			}
			update.AgentExecutables[agent] = strings.TrimSpace(value)
		}
	}
	return update, nil
}

func renderManagementExperimentsCard(saved, running config.ExperimentsSettings) string {
	completion := saved.CompletionNotification
	enabled := managementSelectStaticElement("completion_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(completion.Enabled), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	seconds := cardElement{Tag: "input", Name: "completion_seconds", Placeholder: managementStyledTextPtr(managementCopy("management.currently.d.seconds.leave.blank.to.keep", completion.AfterSeconds), cardTitle{Tag: "plain_text"})}
	summary := managementCopy("management.notify.when.a.task.taking.at.least.d.seconds", completion.AfterSeconds)
	if completion.AfterSeconds == 0 {
		summary = managementCopy("management.notify.only.when.input.is.needed.or.a.task")
	}
	if !completion.Enabled {
		summary = managementCopy("management.notify.when.long.tasks.finish")
	}
	pending := completion != running.CompletionNotification
	item := managementExperimentItemElement("completion_notification", managementCopy("management.long.conversation.completion.notifications"), summary, enabled, pending, []cardElement{
		managementSettingsFieldRow(managementCopy("management.notification.threshold.seconds"), seconds),
		managementHintElement(managementCopy("management.notify.the.turn.initiator.through.a.private.card.linking")),
	})
	groupEnabled := managementSelectStaticElement("group_settings_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.GroupSettingsEnabled), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	groupItem := managementExperimentItemElement("group_settings", managementCopy("management.per.group.settings"), managementCopy("management.set.an.expert.runtime.model.reasoning.effort.and.conversation"), groupEnabled, saved.GroupSettingsEnabled != running.GroupSettingsEnabled, nil)
	memoryEnabled := managementSelectStaticElement("memory_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.MemoryEnabled), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	memoryItem := managementExperimentItemElement("agent_memory", managementCopy("management.agent.memory"), managementCopy("management.remember.preferences.and.experience.across.conversations.without.an.agent"), memoryEnabled, saved.MemoryEnabled != running.MemoryEnabled, []cardElement{
		managementHintElement(managementCopy("management.supports.codex.claude.and.pi.shared.across.runtimes.in")),
	})
	steerEnabled := managementSelectStaticElement("steer_foreground_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.SteerForeground), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	steerStatus := managementCopy("management.steer.foreground.currently.off")
	if running.SteerForeground {
		steerStatus = managementCopy("management.steer.foreground.currently.on")
	}
	// Applied live on save, so it never shows the restart-pending notice.
	steerItem := managementExperimentItemElement("steer_foreground", managementCopy("management.steer.foreground"), managementCopy("management.steer.foreground.description"), steerEnabled, saved.SteerForeground != running.SteerForeground, []cardElement{
		managementHintElement(steerStatus),
		managementHintElement(managementCopy("management.steer.foreground.applies.live")),
	})
	bgEnabled := managementSelectStaticElement("show_background_tasks_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.ShowBackgroundTasks), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	bgStatus := managementCopy("management.show.background.tasks.currently.off")
	if running.ShowBackgroundTasks {
		bgStatus = managementCopy("management.show.background.tasks.currently.on")
	}
	bgItem := managementExperimentItemElement("show_background_tasks", managementCopy("management.show.background.tasks"), managementCopy("management.show.background.tasks.description"), bgEnabled, saved.ShowBackgroundTasks != running.ShowBackgroundTasks, []cardElement{
		managementHintElement(bgStatus),
		managementHintElement(managementCopy("management.steer.foreground.applies.live")),
	})
	mentionEnabled := managementSelectStaticElement("mention_turn_initiator_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.MentionTurnInitiator), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	mentionStatus := managementCopy("management.mention.turn.initiator.currently.off")
	if running.MentionTurnInitiator {
		mentionStatus = managementCopy("management.mention.turn.initiator.currently.on")
	}
	mentionItem := managementExperimentItemElement("mention_turn_initiator", managementCopy("management.mention.turn.initiator"), managementCopy("management.mention.turn.initiator.description"), mentionEnabled, saved.MentionTurnInitiator != running.MentionTurnInitiator, []cardElement{
		managementHintElement(mentionStatus),
		managementHintElement(managementCopy("management.steer.foreground.applies.live")),
	})
	executable := cardElement{Tag: "input", Name: "codex_executable", DefaultValue: saved.CodexExecutable, Placeholder: managementStyledTextPtr(managementCopy("management.codex.executable.placeholder"), cardTitle{Tag: "plain_text"})}
	executableItem := managementExperimentItemElement("codex_executable", managementCopy("management.codex.executable.title"), managementCopy("management.codex.executable.description"), cardElement{Tag: "div", Text: managementStyledTextPtr("Codex", cardTitle{Tag: "plain_text"})}, saved.CodexExecutable != running.CodexExecutable, []cardElement{executable, managementHintElement(managementCopy("management.codex.executable.scope"))})
	elements := []cardElement{
		managementBackToOverviewElement(),
		{Tag: "form", Name: "experiments_form", Direction: "vertical", VerticalSpacing: "12px", Elements: []cardElement{
			item,
			groupItem,
			memoryItem,
			steerItem,
			bgItem,
			mentionItem,
			executableItem,
			managementFormButtonRowElement(managementFormSubmitButton(managementActionExperimentsSaveRestart, managementCopy("management.save.and.restart"))),
		}},
		managementTextElement(managementCopy("management.applies.to.all.assistants.in.this.instance"), "notation", "grey", nil),
	}
	for _, agent := range config.AgentExecutableNames() {
		if agent == "codex" {
			continue
		}
		name := agent + "_executable"
		label := config.AgentExecutableLabel(agent)
		input := cardElement{Tag: "input", Name: name, DefaultValue: saved.AgentExecutables[agent], Placeholder: managementStyledTextPtr(managementCopy("management.launcher.placeholder", agent), cardTitle{Tag: "plain_text"})}
		entry := managementExperimentItemElement(name, managementCopy("management.launcher.title", label), managementCopy("management.launcher.description", label), cardElement{Tag: "div", Text: managementStyledTextPtr(label, cardTitle{Tag: "plain_text"})}, saved.AgentExecutables[agent] != running.AgentExecutables[agent], []cardElement{input, managementHintElement(managementCopy("management.launcher.scope"))})
		form := &elements[1]
		n := len(form.Elements)
		form.Elements = append(form.Elements[:n-1], entry, form.Elements[n-1])
	}
	elements = append(elements, conversationExperimentElements()...)
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.experiments"), Summary: managementCopy("management.amux.experiments"), Theme: "blue", Elements: elements})
}

// Each experiment contributes a compact item to the shared page-level form.
// Field names and IDs belong to the feature, so adding an item needs no new layout.
func managementExperimentItemElement(id string, title any, summary any, control cardElement, pending bool, advanced []cardElement) cardElement {
	heading := cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "12px", Columns: []cardColumn{
		{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(title, "normal", "default", nil)}},
		{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{control}},
	}}
	heading.Columns[1].Elements[0].Width = "120px"
	children := []cardElement{heading, managementTextElement(summary, "notation", "grey", nil)}
	if pending {
		children = append(children, managementTextElement(managementCopy("management.restart.pending.saved.settings.are.not.active.yet"), "notation", "orange", nil))
	}
	if len(advanced) > 0 {
		children = append(children, cardElement{Tag: "collapsible_panel", ElementID: id + "_advanced", Expanded: false, Header: &panelHeader{Title: managementStyledText(managementCopy("management.advanced.settings"), cardTitle{Tag: "plain_text", TextSize: "notation", TextColor: "grey"})}, Elements: advanced})
	}
	return cardElement{Tag: "column_set", ElementID: id + "_item", FlexMode: "none", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalSpacing: "8px", Padding: "12px", BackgroundStyle: "grey-50", Elements: children}}}
}

func renderManagementExperimentsRestartConfirmationCard(state managementState) string {
	values := map[string]string{"system_instance": managementSystemInstanceID(state), "confirmation_nonce": newManagementConfirmationNonce()}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.confirm.restart"), Summary: managementCopy("management.experiment.settings.saved.restart.pending"), Theme: "orange", Elements: []cardElement{
		managementHintElement(managementCopy("management.experiment.settings.are.saved.restarting.briefly.takes.assistants.offline")),
	}, Buttons: []managementButton{
		{Text: managementCopy("management.confirm.restart"), Type: "primary", Action: managementActionSystemRestartOK, Values: values},
		{Text: managementCopy("management.cancel"), Type: "default", Action: managementActionExperimentsOpen},
	}})
}
