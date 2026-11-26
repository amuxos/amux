package bridge

import (
	"strconv"
	"strings"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
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
	if raw, present := managementExperimentCallbackValue(action, "reply_end_enabled"); present {
		enabled, ok := raw.(string)
		enabled = strings.TrimSpace(enabled)
		if !ok || enabled != "true" && enabled != "false" {
			return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.reply.end.choose")
		}
		value := enabled == "true"
		update.ReplyEndNotification = &value
	}
	if raw, present := managementExperimentCallbackValue(action, "recovery_wait_sec"); present {
		text, ok := raw.(string)
		if !ok {
			return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.model.recovery.queue.wait.invalid")
		}
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			seconds, err := strconv.Atoi(trimmed)
			if err != nil || config.ValidateModelRecoveryQueueWaitSeconds(seconds) != nil {
				return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.model.recovery.queue.wait.invalid")
			}
			update.ModelRecoveryQueueWaitSeconds = &seconds
		}
	} else if raw, present := managementExperimentCallbackValue(action, "model_recovery_queue_wait_seconds"); present {
		text, ok := raw.(string)
		if !ok {
			return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.model.recovery.queue.wait.invalid")
		}
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			seconds, err := strconv.Atoi(trimmed)
			if err != nil || config.ValidateModelRecoveryQueueWaitSeconds(seconds) != nil {
				return config.ExperimentsSettingsUpdate{}, managementLocalizedError("management.model.recovery.queue.wait.invalid")
			}
			update.ModelRecoveryQueueWaitSeconds = &seconds
		}
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
	item := managementExperimentItemElement(managementCopy("management.long.conversation.completion.notifications"), summary, enabled, pending, []cardElement{
		managementSettingsFieldRow(managementCopy("management.notification.threshold.seconds"), seconds),
		managementHintElement(managementCopy("management.notify.the.turn.initiator.through.a.private.card.linking")),
	})
	groupEnabled := managementSelectStaticElement("group_settings_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.GroupSettingsEnabled), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	groupItem := managementExperimentItemElement(managementCopy("management.per.group.settings"), managementCopy("management.set.an.expert.runtime.model.reasoning.effort.and.conversation"), groupEnabled, saved.GroupSettingsEnabled != running.GroupSettingsEnabled, nil)
	memoryEnabled := managementSelectStaticElement("memory_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.MemoryEnabled), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	memoryItem := managementExperimentItemElement(managementCopy("management.agent.memory"), managementCopy("management.remember.preferences.and.experience.across.conversations.without.an.agent"), memoryEnabled, saved.MemoryEnabled != running.MemoryEnabled, []cardElement{
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
	// Live-applied switches inline their live status and omit redundant
	// collapsible panels so the card stays well within Feishu's 200-component
	// and 30 KB limits even with distribution extensions installed.
	steerSummary := joinManagementCopy(
		managementCopy("management.steer.foreground.description"),
		steerStatus,
		managementCopy("management.steer.foreground.applies.live"),
	)
	steerItem := managementExperimentItemElement(managementCopy("management.steer.foreground"), steerSummary, steerEnabled, saved.SteerForeground != running.SteerForeground, nil)
	bgEnabled := managementSelectStaticElement("show_background_tasks_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.ShowBackgroundTasks), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	bgItem := managementExperimentItemElement(managementCopy("management.show.background.tasks"), managementCopy("management.show.background.tasks.description"), bgEnabled, saved.ShowBackgroundTasks != running.ShowBackgroundTasks, nil)
	mentionEnabled := managementSelectStaticElement("mention_turn_initiator_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.MentionTurnInitiator), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	mentionItem := managementExperimentItemElement(managementCopy("management.mention.turn.initiator"), managementCopy("management.mention.turn.initiator.description"), mentionEnabled, saved.MentionTurnInitiator != running.MentionTurnInitiator, nil)
	replyEndEnabled := managementSelectStaticElement("reply_end_enabled", managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(saved.ReplyEndNotification), []cardOption{
		{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
		{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
	})
	replyEndItem := managementExperimentItemElement(managementCopy("management.reply.end.title"), managementCopy("management.reply.end.description"), replyEndEnabled, saved.ReplyEndNotification != running.ReplyEndNotification, nil)
	recoveryWaitInput := cardElement{Tag: "input", Name: "recovery_wait_sec", Placeholder: managementStyledTextPtr(managementCopy("management.currently.d.seconds.leave.blank.to.keep", saved.ModelRecoveryQueueWaitSeconds), cardTitle{Tag: "plain_text"})}
	recoveryWaitSummary := managementCopy("management.model.recovery.queue.wait.summary", saved.ModelRecoveryQueueWaitSeconds)
	if saved.ModelRecoveryQueueWaitSeconds == 0 {
		recoveryWaitSummary = managementCopy("management.model.recovery.queue.wait.disabled")
	}
	recoveryWaitStatus := managementCopy("management.model.recovery.queue.wait.currently.on", running.ModelRecoveryQueueWaitSeconds)
	if running.ModelRecoveryQueueWaitSeconds == 0 {
		recoveryWaitStatus = managementCopy("management.model.recovery.queue.wait.currently.off")
	}
	recoveryWaitItem := managementExperimentItemElement(
		managementCopy("management.model.recovery.queue.wait.title"),
		recoveryWaitSummary,
		cardElement{Tag: "div", Text: managementStyledTextPtr(recoveryWaitStatus, cardTitle{Tag: "plain_text"})},
		saved.ModelRecoveryQueueWaitSeconds != running.ModelRecoveryQueueWaitSeconds,
		[]cardElement{
			recoveryWaitInput,
			managementHintElement(managementCopy("management.model.recovery.queue.wait.hint")),
		},
	)
	executable := cardElement{Tag: "input", Name: "codex_executable", DefaultValue: saved.CodexExecutable, Placeholder: managementStyledTextPtr(managementCopy("management.codex.executable.placeholder"), cardTitle{Tag: "plain_text"})}
	executableItem := managementExperimentItemElement(managementCopy("management.codex.executable.title"), managementCopy("management.codex.executable.description"), cardElement{Tag: "div", Text: managementStyledTextPtr("Codex", cardTitle{Tag: "plain_text"})}, saved.CodexExecutable != running.CodexExecutable, []cardElement{executable, managementHintElement(managementCopy("management.codex.executable.scope"))})
	items := []cardElement{replyEndItem, item, groupItem, memoryItem, steerItem, bgItem, mentionItem, recoveryWaitItem, executableItem}
	for _, agent := range config.AgentExecutableNames() {
		if agent == "codex" {
			continue
		}
		name := agent + "_executable"
		label := config.AgentExecutableLabel(agent)
		input := cardElement{Tag: "input", Name: name, DefaultValue: saved.AgentExecutables[agent], Placeholder: managementStyledTextPtr(managementCopy("management.launcher.placeholder", agent), cardTitle{Tag: "plain_text"})}
		items = append(items, managementExperimentItemElement(managementCopy("management.launcher.title", label), managementCopy("management.launcher.description", label), cardElement{Tag: "div", Text: managementStyledTextPtr(label, cardTitle{Tag: "plain_text"})}, saved.AgentExecutables[agent] != running.AgentExecutables[agent], []cardElement{input, managementHintElement(managementCopy("management.launcher.scope"))}))
	}
	for _, toggle := range bridgeExtensions.snapshot().ExperimentToggles {
		if strings.TrimSpace(toggle.ID) == "" || strings.TrimSpace(toggle.FieldName) == "" || toggle.GetEnabled == nil {
			continue
		}
		cur := toggle.GetEnabled()
		title := i18n.Render(func(locale i18n.Locale) string {
			if locale == i18n.Chinese && toggle.TitleZH != "" {
				return toggle.TitleZH
			}
			if toggle.TitleEN != "" {
				return toggle.TitleEN
			}
			return toggle.TitleZH
		})
		summary := i18n.Render(func(locale i18n.Locale) string {
			if locale == i18n.Chinese && toggle.SummaryZH != "" {
				return toggle.SummaryZH
			}
			if toggle.SummaryEN != "" {
				return toggle.SummaryEN
			}
			return toggle.SummaryZH
		})
		control := managementSelectStaticElement(toggle.FieldName, managementCopy("management.choose.on.or.off.161"), strconv.FormatBool(cur), []cardOption{
			{Text: managementStyledText(managementCopy("management.off"), cardTitle{Tag: "plain_text"}), Value: "false"},
			{Text: managementStyledText(managementCopy("management.on"), cardTitle{Tag: "plain_text"}), Value: "true"},
		})
		items = append(items, managementExperimentItemElement(title, summary, control, false, nil))
	}
	items = append(items, managementFormButtonRowElement(managementFormSubmitButton(managementActionExperimentsSaveRestart, managementCopy("management.save.and.restart"))))
	elements := []cardElement{
		managementBackToOverviewElement(),
		managementDividerElement(),
		{Tag: "form", Name: "experiments_form", Direction: "vertical", VerticalSpacing: "12px", Elements: items},
		managementTextElement(managementCopy("management.applies.to.all.assistants.in.this.instance"), "notation", "grey", nil),
	}
	elements = append(elements, conversationExperimentElements()...)
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.experiments"), Summary: managementCopy("management.amux.experiments"), Theme: "wathet", Icon: managementIcon("version_outlined", "blue"), Elements: elements})
}

func joinManagementCopy(parts ...i18n.Text) i18n.Text {
	return i18n.Render(func(locale i18n.Locale) string {
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p.For(locale)); s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, " ")
	})
}

// Each experiment contributes a compact item to the shared page-level form.
func managementExperimentItemElement(title any, summary any, control cardElement, pending bool, advanced []cardElement) cardElement {
	heading := cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "12px", Columns: []cardColumn{
		{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(title, "normal", "default", nil)}},
		{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{control}},
	}}
	children := []cardElement{heading, managementTextElement(summary, "notation", "grey", nil)}
	if pending {
		children = append(children, managementTextElement(managementCopy("management.restart.pending.saved.settings.are.not.active.yet"), "notation", "orange", nil))
	}
	if len(advanced) > 0 {
		children = append(children, cardElement{Tag: "collapsible_panel", Expanded: false, Header: &panelHeader{Title: managementStyledText(managementCopy("management.advanced.settings"), cardTitle{Tag: "plain_text", TextSize: "notation", TextColor: "grey"})}, Elements: advanced})
	}
	return cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalSpacing: "4px", Padding: "10px 12px 10px 12px", BackgroundStyle: "grey-50", Elements: children}}}
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
