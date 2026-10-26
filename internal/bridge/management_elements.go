package bridge

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
)

func renderManagementCardForSurface(cardJSON string, surface managementSurface, active managementView) string {
	return renderManagementCardForSurfaceMode(cardJSON, surface, active, true)
}

func renderFocusedManagementCardForSurface(cardJSON string, surface managementSurface) string {
	return renderManagementCardForSurfaceMode(cardJSON, surface, "", false)
}

func renderManagementCardForSurfaceMode(cardJSON string, surface managementSurface, active managementView, showStableShell bool) string {
	if surface != managementSurfaceAmux {
		return cardJSON
	}
	var doc cardDoc
	if err := json.Unmarshal([]byte(cardJSON), &doc); err != nil {
		return cardJSON
	}
	if showStableShell {
		for _, element := range doc.Body.Elements {
			if element.ElementID == "amux_management_back" {
				return cardJSON
			}
		}
	}
	if showStableShell && active == managementViewLibrary {
		doc.Body.Elements = omitNavigationLibraryBackButtons(doc.Body.Elements)
		if last := len(doc.Body.Elements) - 1; last >= 0 && doc.Body.Elements[last].Tag == "hr" {
			doc.Body.Elements = doc.Body.Elements[:last]
		}
	}
	decorateManagementSurfaceElements(doc.Body.Elements, surface)
	if showStableShell && active != managementViewOverview {
		backElements := []cardElement{managementBackToOverviewElement()}
		decorateManagementSurfaceElements(backElements, surface)
		back := backElements[0]
		doc.Body.Elements = append([]cardElement{back, managementDividerElement()}, doc.Body.Elements...)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return cardJSON
	}
	return string(raw)
}

func managementBackToOverviewElement() cardElement {
	element := managementNavigationBackElement(managementCopy("management.back.to.overview"), managementActionStatusOpen, "amux.back.overview")
	element.ElementID = "amux_management_back"
	return element
}

func managementSubpageBackElement(text any, action string, name string) cardElement {
	element := managementNavigationBackElement(text, action, name)
	element.ElementID = strings.ReplaceAll(name, ".", "_")
	return element
}

func managementNavigationBackElement(text any, action string, name string) cardElement {
	element := managementButtonRowElement([]managementButton{{Text: text, Type: "default", Action: action, Name: name, Icon: managementIconPtr("left_outlined", "grey")}})
	if len(element.Columns) > 0 && len(element.Columns[0].Elements) > 0 {
		element.Columns[0].Elements[0].Size = "small"
	}
	return element
}

func managementNavigationButtonElement(button managementButton) cardElement {
	if button.Icon == nil {
		button.Icon = managementIconPtr("left_outlined", "grey")
	}
	element := managementButtonElement(button)
	element.Size = "small"
	return element
}

func omitNavigationLibraryBackButtons(elements []cardElement) []cardElement {
	filtered := make([]cardElement, 0, len(elements))
	for _, element := range elements {
		if isNavigationLibrarySettingsNavigationButton(element) {
			continue
		}
		element.Actions = omitNavigationLibraryBackButtons(element.Actions)
		element.Elements = omitNavigationLibraryBackButtons(element.Elements)
		columns := element.Columns[:0]
		for _, column := range element.Columns {
			column.Elements = omitNavigationLibraryBackButtons(column.Elements)
			if len(column.Elements) > 0 {
				columns = append(columns, column)
			}
		}
		element.Columns = columns
		if element.Tag == "column_set" && len(element.Actions) == 0 && len(element.Elements) == 0 && len(element.Columns) == 0 {
			continue
		}
		filtered = append(filtered, element)
	}
	return filtered
}

func isNavigationLibrarySettingsNavigationButton(element cardElement) bool {
	if element.Tag != "button" {
		return false
	}
	var value map[string]string
	return json.Unmarshal(element.Value, &value) == nil && value[managementActionKey] == managementActionSettingsOpen
}

func decorateManagementSurfaceElements(elements []cardElement, surface managementSurface) {
	for i := range elements {
		element := &elements[i]
		if (element.Tag == "button" || element.Tag == "select_static") && len(element.Value) > 0 {
			var value map[string]any
			if json.Unmarshal(element.Value, &value) == nil {
				value[managementSurfaceKey] = string(surface)
				element.Value, _ = json.Marshal(value)
			}
		}
		for behaviorIndex := range element.Behaviors {
			behavior := &element.Behaviors[behaviorIndex]
			if behavior.Type != "callback" || behavior.Value == nil {
				continue
			}
			behavior.Value[managementSurfaceKey] = string(surface)
		}
		decorateManagementSurfaceElements(element.Actions, surface)
		decorateManagementSurfaceElements(element.Elements, surface)
		for columnIndex := range element.Columns {
			decorateManagementSurfaceElements(element.Columns[columnIndex].Elements, surface)
		}
	}
}

func renderManagementOverviewCard(state managementState) string {
	draft := managementDraftFromState(state)
	theme := "green"
	statusTitle := managementCopy("management.running.normally")
	statusSummary := managementCopy("management.d.runtimes.available.ready.for.tasks", state.Targets)
	statusColor := "default"
	statusIcon := managementIconPtr("check_outlined", "green")
	if state.SystemStatus != managementSystemHealthy {
		theme = "orange"
		statusTitle = managementCopy("management.cannot.start.new.tasks.right.now")
		statusSummary = managementCopy("management.no.runtimes.available.check.system.status")
		statusColor = "orange"
		statusIcon = managementIconPtr("warning_outlined", "orange")
	}
	elements := []cardElement{managementTextElement(statusTitle, "heading", statusColor, statusIcon), managementTextElement(statusSummary, "notation", statusColor, nil)}
	if state.SystemStatus == managementSystemHealthy && state.Update.ShouldPrompt {
		elements = append(elements, managementUpdateNoticeElement(state.Update))
	}
	elements = append(elements, managementDividerElement(), managementSectionTitleElement(managementCopy("management.current.configuration")), managementOverviewConfigElement(state, draft), managementDividerElement(), managementSectionTitleElement(managementCopy("management.manage")), managementOverviewActionsElement(state), managementTextElement(managementCopy("management.send.amux.again.to.refresh.status"), "notation", "grey", managementIconPtr("refresh_outlined", "grey")))
	if state.ScheduleSummary != nil {
		elements = append(elements, managementDividerElement(), managementTextElement(state.ScheduleSummary, "normal", "default", nil), managementFormButtonRowElement(scheduleButton("title", "list", 0, nil), scheduleButton("alerts", "alerts", 0, nil)))
	}
	elements = append(elements, managementLanguageElement())
	card := marshalNativeManagementCard(managementCardView{Title: "amux", Summary: "amux", Theme: theme, Icon: managementIcon("robot_outlined", theme), Elements: elements})
	return renderFocusedManagementCardForSurface(card, managementSurfaceAmux)
}

func managementOverviewConfigElement(state managementState, draft managementSettingsDraft) cardElement {
	element := managementVerticalSummaryElement("amux_overview_config",
		managementKeyValueRowElement(managementCopy("management.message.handling.summary"), managementModeSummary(draft)),
		managementTextElement(managementWorkspaceSummary(state), "notation", "grey", managementIconPtr("info_outlined", "grey")),
		managementKeyValueRowElement(managementCopy("management.default.expert"), managementProfileNameByRef(state, draft.DefaultAgent)),
		managementKeyValueRowElement(managementCopy("management.runtime"), managementRuntimeLabel(state, draft.DefaultRuntimeRef)),
		managementKeyValueRowElement(managementCopy("management.default.model"), managementRuntimeModelLabel(draft.DefaultModelRef)),
		managementKeyValueRowElement(managementCopy("management.reasoning.effort"), managementReasoningEffortLabel(draft.DefaultReasoningEffort)),
		managementKeyValueRowElement(managementCopy("management.activity.display"), managementThinkingProcessLabel(draft.ThinkingProcess)))
	element.Columns[0].BackgroundStyle = "blue-50"
	element.Columns[0].VerticalSpacing = "4px"
	return element
}

func managementKeyValueRowElement(label, value any) cardElement {
	return cardElement{
		Tag:               "column_set",
		FlexMode:          "none",
		HorizontalSpacing: "8px",
		Columns: []cardColumn{
			{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "top", Elements: []cardElement{managementTextElement(label, "notation", "grey", nil)}},
			{Tag: "column", Width: "weighted", Weight: 3, VerticalAlign: "top", Elements: []cardElement{managementTextElement(value, "normal", "default", nil)}},
		},
	}
}

func managementKeyValueListElement(elementID string, items ...managementSummaryItem) cardElement {
	rows := make([]cardElement, 0, len(items))
	for _, item := range items {
		rows = append(rows, managementKeyValueRowElement(item.Title, managementNonemptyText(item.Body, "—")))
	}
	element := managementVerticalSummaryElement(elementID, rows...)
	element.Columns[0].VerticalSpacing = "4px"
	return element
}

func managementTaskDefaultsElement(state managementState, draft managementSettingsDraft, elementID string) cardElement {
	element := managementVerticalSummaryElement(elementID,
		managementKeyValueRowElement(managementCopy("management.message.handling.summary"), managementModeSummary(draft)),
		managementTextElement(managementWorkspaceSummary(state), "notation", "grey", managementIconPtr("info_outlined", "grey")),
		managementKeyValueRowElement(managementCopy("management.default.expert"), managementProfileNameByRef(state, draft.DefaultAgent)),
		managementKeyValueRowElement(managementCopy("management.runtime"), managementRuntimeLabel(state, draft.DefaultRuntimeRef)),
		managementKeyValueRowElement(managementCopy("management.default.model"), managementRuntimeModelLabel(draft.DefaultModelRef)),
		managementKeyValueRowElement(managementCopy("management.reasoning.effort"), managementReasoningEffortLabel(draft.DefaultReasoningEffort)),
		managementKeyValueRowElement(managementCopy("management.activity.display"), managementThinkingProcessLabel(draft.ThinkingProcess)))
	element.Columns[0].BackgroundStyle = "blue-50"
	element.Columns[0].VerticalSpacing = "4px"
	return element
}

func managementVerticalSummaryElement(elementID string, rows ...cardElement) cardElement {
	return cardElement{Tag: "column_set", ElementID: elementID, FlexMode: "none", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalAlign: "top", VerticalSpacing: "4px", BackgroundStyle: "grey-50", Padding: "8px 12px 8px 12px", Elements: rows}}}
}

func managementOverviewActionsElement(state managementState) cardElement {
	items := []managementButton{
		{Text: managementCopy("management.system.status"), Type: "default", Action: managementActionSystemMaintenance, Name: "amux.overview.system", Icon: managementIconPtr("setting_outlined", "grey")},
		{Text: managementCopy("management.assistant.settings"), Type: "primary", Action: managementActionSettingsOpen, Name: "amux.overview.settings", Icon: managementIconPtr("setting_outlined", "blue")},
		{Text: managementCopy("management.expert.library"), Type: "default", Action: managementActionPackLibraryOpen, Name: "amux.overview.library", Icon: managementIconPtr("robot_outlined", "grey")},
		{Text: managementCopy("management.experiments"), Type: "default", Action: managementActionExperimentsOpen, Name: "amux.overview.experiments", Icon: managementIconPtr("setting_outlined", "grey")},
		{Text: managementCopy("management.help"), Type: "default", Action: managementActionHelpOpen, Name: "amux.overview.help", Icon: managementIconPtr("info_outlined", "grey")},
	}
	if state.SystemStatus != managementSystemHealthy || state.Update.ShouldPrompt {
		items[0].Type = "primary"
		items[0].Icon = managementIconPtr("setting_outlined", "blue")
		items[1].Type = "default"
		items[1].Icon = managementIconPtr("setting_outlined", "grey")
	}
	if state.SystemStatus == managementSystemHealthy && state.Update.ShouldPrompt {
		items[0].Text = managementCopy("management.view.update")
	}
	columns := make([]cardColumn, 0, len(items))
	for _, item := range items {
		button := managementButtonElement(item)
		button.Width = "fill"
		columns = append(columns, cardColumn{Tag: "column", Width: "weighted", Weight: 1, HorizontalAlign: "center", Elements: []cardElement{button}})
	}
	return cardElement{Tag: "column_set", ElementID: "amux_overview_actions", FlexMode: "bisect", HorizontalSpacing: "8px", Columns: columns}
}

func marshalNativeManagementCard(view managementCardView) string {
	elements := append([]cardElement(nil), view.Elements...)
	if strings.TrimSpace(managementDefaultText(view.Subtitle)) != "" {
		status := managementTextElement(view.Subtitle, "notation", "grey", nil)
		status.Margin = "0px 0px 2px 0px"
		elements = append([]cardElement{status}, elements...)
	}
	if len(view.Buttons) > 0 {
		elements = append(elements, managementButtonRowElement(view.Buttons))
	}
	doc := newCardDoc(elements)
	doc.Config.WideScreenMode = false
	doc.Config.WidthMode = ""
	doc.Config.Summary = managementSummary(managementNonemptyText(view.Summary, view.Title))
	enableForward := false
	updateMulti := true
	doc.Config.EnableForward = &enableForward
	doc.Config.UpdateMulti = &updateMulti
	doc.Body.Direction = "vertical"
	doc.Body.VerticalSpacing = "8px"
	doc.Body.Padding = "12px 16px 16px 16px"
	if strings.TrimSpace(managementDefaultText(view.Title)) != "" {
		header := &cardHeader{Title: managementPlainText(view.Title), Template: valueOrPlaceholder(view.Theme, "default"), Padding: "8px 16px 8px 16px"}
		if strings.TrimSpace(view.Icon.Token) != "" {
			icon := view.Icon
			header.Icon = &icon
		}
		doc.Header = header
	}
	raw, _ := json.Marshal(doc)
	return string(raw)
}

func managementIcon(token, color string) cardIcon {
	return cardIcon{Tag: "standard_icon", Token: token, Color: color}
}

func managementIconPtr(token, color string) *cardIcon {
	icon := managementIcon(token, color)
	return &icon
}

func managementConfirm(title, text any) *cardConfirm {
	return &cardConfirm{Title: managementPlainText(title), Text: managementPlainText(text)}
}

func managementTextElement(content any, size, color string, icon *cardIcon) cardElement {
	if strings.TrimSpace(size) == "" {
		size = "normal"
	}
	if strings.TrimSpace(color) == "" {
		color = "default"
	}
	text := managementPlainText(content)
	text.TextSize = size
	text.TextColor = color
	return cardElement{Tag: "div", Text: &text, Icon: icon, Width: "fill"}
}

func managementSectionTitleElement(content any) cardElement {
	return managementTextElement(content, "normal", "grey", nil)
}

func managementDividerElement() cardElement {
	return cardElement{Tag: "hr", Margin: "2px 0px 2px 0px"}
}

func managementHintElement(text any) cardElement {
	element := managementTextElement(text, "notation", "grey", managementIconPtr("info_outlined", "grey"))
	element.Margin = "4px 0px 0px 0px"
	return element
}

func managementUpdateNoticeElement(update ManagementUpdateInfo) cardElement {
	text := managementConcat(managementCopy("management.new.amux.version.available"), managementValueOrPlaceholder(update.LatestVersion, managementCopy("management.unknown")))
	if current := strings.TrimSpace(update.CurrentVersion); current != "" {
		text = managementConcat(text, managementConcat(managementCopy("management.current"), current, "）"))
	}
	element := managementTextElement(text, "notation", "orange", managementIconPtr("version_outlined", "orange"))
	element.Margin = "4px 0px 0px 0px"
	return element
}

func managementCompactSummaryElement(items ...managementSummaryItem) cardElement {
	columns := make([]cardColumn, 0, len(items))
	for _, item := range items {
		title := item.Title
		body := item.Body
		if managementDefaultText(body) == "" {
			body = "—"
		}
		if managementDefaultText(title) == "" {
			title = managementCopy("management.current.settings")
		}
		columns = append(columns, cardColumn{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalAlign: "top", VerticalSpacing: "2px", BackgroundStyle: "grey-50", Padding: "8px 10px 8px 10px", Elements: []cardElement{managementTextElement(title, "notation", "grey", nil), managementTextElement(body, "normal", "default", nil)}})
	}
	return cardElement{Tag: "column_set", FlexMode: "flow", HorizontalSpacing: "8px", Margin: "2px 0px 2px 0px", Columns: columns}
}

func managementMaintenanceDetailsElement(state managementState) cardElement {
	return managementCompactSummaryElement(managementSummaryItem{Title: managementCopy("management.message.handling.summary"), Body: managementModeSummary(managementDraftFromState(state))}, managementSummaryItem{Title: managementCopy("management.version"), Body: managementValueOrPlaceholder(state.SystemInfo.Version, managementCopy("management.unknown"))}, managementSummaryItem{Title: managementCopy("management.uptime"), Body: managementUptimeText(state.SystemInfo.StartedAt, time.Now())}, managementSummaryItem{Title: managementCopy("management.connected.assistants"), Body: managementCopy("management.d", state.SystemInfo.BotCount)}, managementSummaryItem{Title: managementCopy("management.hub.host.ip"), Body: managementValueOrPlaceholder(state.SystemInfo.HubIP, managementCopy("management.unknown"))})
}

func managementSettingsEditorElements(state managementState, name string, draft managementSettingsDraft, submitAction string, submitText any, cancelAction string, trailingAction *managementButton, leftHint cardElement) []cardElement {
	draft = managementNormalizeDraftForState(state, draft)
	submitButton := managementFormSubmitButton(submitAction, submitText)
	submitButton.Value, _ = json.Marshal(managementButtonValue(managementButton{Action: submitAction, Values: map[string]string{
		"interaction": draft.Interaction, "default_agent": draft.DefaultAgent,
		"default_runtime_ref": draft.DefaultRuntimeRef, "default_model_ref": draft.DefaultModelRef,
		"default_reasoning_effort": draft.DefaultReasoningEffort,
		"thinking_process":         draft.ThinkingProcess,
		"daily_cleanup":            draft.DailyCleanup,
	}}))
	if state.Mode != config.FeishuInteractionSingleton && draft.Interaction == config.FeishuInteractionSingleton {
		submitButton.Confirm = managementConfirm(managementCopy("management.continue.the.same.conversation"), managementCopy("management.messages.sent.to.the.assistant.from.authorized.locations.will"))
	}
	draftChangeAction := managementActionSettingsEdit
	if submitAction == managementActionOnboardingSubmit {
		draftChangeAction = managementActionOnboardingEdit
	}
	draftChangeBehavior := func(field string) []cardBehavior {
		return []cardBehavior{{Type: "callback", Value: managementButtonValue(managementButton{Action: draftChangeAction, Values: map[string]string{
			"interaction": draft.Interaction, "default_agent": draft.DefaultAgent,
			"default_runtime_ref": draft.DefaultRuntimeRef, "default_model_ref": draft.DefaultModelRef,
			"default_reasoning_effort": draft.DefaultReasoningEffort,
			"thinking_process":         draft.ThinkingProcess,
			"daily_cleanup":            draft.DailyCleanup, managementDraftFieldKey: field,
		}})}}
	}
	buttonElements := []cardElement{submitButton}
	if cancelAction != "" {
		buttonElements = append(buttonElements, managementCallbackButton(cancelAction, managementCopy("management.cancel"), "default"))
	}
	buttons := managementFormButtonRowElement(buttonElements...)
	if trailingAction != nil {
		buttons = managementSplitActionRowElement(buttons, managementButtonElement(*trailingAction))
	}
	profileOptions := managementProfileOptions(state, true)
	interactionSelect := managementSelectStaticElement("interaction", managementCopy("management.choose.how.messages.are.handled"), draft.Interaction, []cardOption{{Text: managementStyledText(managementInteractionText(config.FeishuInteractionThread), cardTitle{Tag: "plain_text"}), Value: config.FeishuInteractionThread}, {Text: managementStyledText(managementInteractionText(interactionModeThreadIsolated), cardTitle{Tag: "plain_text"}), Value: interactionModeThreadIsolated}, {Text: managementStyledText(managementInteractionText(config.FeishuInteractionSingleton), cardTitle{Tag: "plain_text"}), Value: config.FeishuInteractionSingleton}})
	interactionSelect.Required = false
	interactionSelect.Behaviors = draftChangeBehavior("interaction")
	profileField := managementHintElement(managementCopy("management.no.experts.are.available.add.experts.to.the.library"))
	if len(profileOptions) > 0 {
		profileField = managementSelectStaticElement("default_agent", managementCopy("management.search.experts.libraries.or.descriptions"), draft.DefaultAgent, profileOptions)
		profileField.Required = false
		profileField.Behaviors = draftChangeBehavior("default_agent")
	}
	runtimeOptions := managementRuntimeOptions(state)
	runtimeField := managementSelectStaticElement("default_runtime_ref", managementCopy("management.choose.a.runtime"), draft.DefaultRuntimeRef, runtimeOptions)
	runtimeField.Required = false
	runtimeField.Behaviors = draftChangeBehavior("default_runtime_ref")
	modelState := state
	modelState.DefaultModelRef = draft.DefaultModelRef
	modelState.DefaultReasoningEffort = draft.DefaultReasoningEffort
	modelOptions := managementRuntimeModelOptions(modelState, draft.DefaultRuntimeRef)
	initialModelRef := draft.DefaultModelRef
	if initialModelRef == "" {
		initialModelRef = managementProfileDefaultModelOption
	}
	modelField := managementSelectStaticElement("default_model_ref", managementCopy("management.choose.a.model.supported.by.this.runtime"), initialModelRef, modelOptions)
	modelField.Required = false
	modelField.Behaviors = draftChangeBehavior("default_model_ref")
	reasoningOptions := managementRuntimeReasoningOptions(modelState, draft.DefaultRuntimeRef, draft.DefaultModelRef)
	initialReasoningEffort := draft.DefaultReasoningEffort
	if initialReasoningEffort == "" {
		initialReasoningEffort = managementRuntimeDefaultReasoningOption
	}
	reasoningField := managementSelectStaticElement("default_reasoning_effort", managementCopy("management.choose.reasoning.effort"), initialReasoningEffort, reasoningOptions)
	reasoningField.Required = false
	reasoningField.Behaviors = draftChangeBehavior("default_reasoning_effort")
	thinkingProcessField := managementSelectStaticElement("thinking_process", managementCopy("management.choose.how.activity.is.displayed"), draft.ThinkingProcess, []cardOption{
		{Text: managementStyledText(managementCopy("management.collapsed.default"), cardTitle{Tag: "plain_text"}), Value: config.FeishuThinkingProcessCollapsed},
		{Text: managementStyledText(managementCopy("management.expanded"), cardTitle{Tag: "plain_text"}), Value: config.FeishuThinkingProcessExpanded},
		{Text: managementStyledText(managementCopy("management.hidden"), cardTitle{Tag: "plain_text"}), Value: config.FeishuThinkingProcessHidden},
	})
	thinkingProcessField.Required = false
	thinkingProcessField.Behaviors = draftChangeBehavior("thinking_process")
	hintRow := cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "16px", Columns: []cardColumn{managementSettingsHintColumn(leftHint), managementSettingsHintColumn(managementProfileLibraryHintElement(state))}}
	profileRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn(managementCopy("management.default.expert"), profileField)}}
	runtimeRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn(managementCopy("management.runtime.new.sessions.only"), runtimeField)}}
	interactionRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn(managementCopy("management.message.handling.question"), interactionSelect)}}
	modelRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn(managementCopy("management.default.model.new.sessions.only"), modelField)}}
	reasoningRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn(managementCopy("management.reasoning.effort.new.sessions.only"), reasoningField)}}
	thinkingProcessRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn(managementCopy("management.activity.display"), thinkingProcessField)}}
	buttons.Margin = "4px 0px 0px 0px"
	form := cardElement{Tag: "form", Name: name, Direction: "vertical", VerticalSpacing: "8px", Elements: []cardElement{buttons}}
	values := map[string]string{"interaction": draft.Interaction, "default_agent": draft.DefaultAgent, "default_runtime_ref": draft.DefaultRuntimeRef, "default_model_ref": draft.DefaultModelRef, "default_reasoning_effort": draft.DefaultReasoningEffort, "thinking_process": draft.ThinkingProcess, "daily_cleanup": draft.DailyCleanup}
	elements := []cardElement{hintRow, profileRow, runtimeRow, modelRow}
	elements = append(elements, modelServiceElements(state, draft.DefaultRuntimeRef, values, draftChangeAction)...)
	elements = append(elements, reasoningRow, interactionRow)
	if managementUsesThreadRouting(draft.Interaction) {
		field := managementSelectStaticElement("daily_cleanup", managementCopy("cleanup.choose"), dailyCleanupInitial(draft.DailyCleanup), dailyCleanupOptions())
		field.Required = false
		field.Behaviors = draftChangeBehavior("daily_cleanup")
		elements = append(elements, managementTextElement(managementConcat(managementCopy("cleanup.title"), " · ", managementCopy("cleanup.hint")), "notation", "grey", nil), field)
	}
	return append(elements, thinkingProcessRow, form)
}

func managementRuntimeOptions(state managementState) []cardOption {
	runtimes := managementRuntimeCatalog(state)
	options := make([]cardOption, 0, len(runtimes))
	for _, runtime := range runtimes {
		label := strings.TrimSpace(runtime.DisplayName)
		if label == "" {
			label = strings.TrimSpace(runtime.Ref)
		}
		options = append(options, cardOption{Text: managementStyledText(label, cardTitle{Tag: "plain_text"}), Value: runtime.Ref})
	}
	return options
}

func managementRuntimeModelOptions(state managementState, runtimeRef string) []cardOption {
	runtime, ok := managementRuntimeByRef(state, runtimeRef)
	defaultLabel := managementCopy("management.use.the.runtime.default.model")
	if ok {
		name := strings.TrimSpace(runtime.DisplayName)
		if name == "" {
			name = strings.TrimSpace(runtime.Ref)
		}
		defaultLabel = managementConcat(managementCopy("management.use"), name, managementCopy("management.default.model.130"))
	}
	options := []cardOption{{Text: managementStyledText(defaultLabel, cardTitle{Tag: "plain_text"}), Value: managementProfileDefaultModelOption}}
	if !ok {
		return options
	}
	if current := strings.TrimSpace(state.DefaultModelRef); current != "" {
		found := false
		for _, model := range runtime.Models {
			if model.Ref == current {
				found = true
			}
		}
		if !found {
			if model, err := agentprofile.ResolveRuntimeModel(runtime, current); err == nil && (model.DisplayName != "" || runtimeRef == "codex") {
				runtime.Models = append(runtime.Models, model)
			}
		}
	}
	seen := make(map[string]bool)
	for _, option := range modelServiceOptions(state, runtimeRef) {
		options = append(options, option)
		for _, service := range state.ModelServices {
			if option.Value != modelServiceEntryPrefix+service.Service.ID {
				continue
			}
			for _, model := range service.State.Models {
				if seen[model.Ref] {
					continue
				}
				options = append(options, cardOption{Text: cardTitle{Tag: "plain_text", Content: model.Label}, Value: model.Ref})
				seen[model.Ref] = true
			}
		}
	}
	for _, model := range runtime.Models {
		if seen[model.Ref] {
			continue
		}
		options = append(options, cardOption{Text: managementPlainText(valueOrPlaceholder(model.DisplayName, model.Ref)), Value: model.Ref})
	}
	return options
}

func managementRuntimeReasoningOptions(state managementState, runtimeRef, modelRef string) []cardOption {
	options := []cardOption{{Text: managementStyledText(managementCopy("management.use.the.runtime.default.effort"), cardTitle{Tag: "plain_text"}), Value: managementRuntimeDefaultReasoningOption}}
	runtime, ok := managementRuntimeByRef(state, runtimeRef)
	if !ok {
		return options
	}
	model, err := agentprofile.ResolveRuntimeModel(runtime, modelRef)
	if err != nil {
		return options
	}
	efforts := model.SupportedReasoningEfforts
	if strings.TrimSpace(model.Ref) == "" {
		efforts = runtime.DefaultReasoningEfforts
	}
	if model.NativeCapabilitiesUnknown {
		efforts = nil
		// Retain a saved/unsaved effort while discovery is unavailable. Do not
		// invent additional capabilities for an unknown model.
		if state.DefaultReasoningEffort != "" {
			efforts = []string{state.DefaultReasoningEffort}
		}
	}
	for _, effort := range efforts {
		options = append(options, cardOption{Text: managementStyledText(effort, cardTitle{Tag: "plain_text"}), Value: effort})
	}
	return options
}

func managementSelectedModelState(state managementState, model, effort string) managementState {
	state.DefaultModelRef, state.DefaultReasoningEffort = model, effort
	return state
}

func managementProfileReasoningOptions(state managementState, profileRef, modelRef string) []cardOption {
	options := []cardOption{{Text: managementStyledText(managementCopy("management.use.the.runtime.default.effort"), cardTitle{Tag: "plain_text"}), Value: managementRuntimeDefaultReasoningOption}}
	profile, ok := managementProfileByRef(state, profileRef)
	if !ok {
		return options
	}
	runtime, ok := managementRuntimeByRef(state, profile.AgentType)
	if !ok {
		return options
	}
	if strings.TrimSpace(modelRef) == "" {
		modelRef = profile.Model
	}
	model := agentprofile.ProfileNativeRuntimeModel(runtime, modelRef)
	for _, effort := range model.SupportedReasoningEfforts {
		options = append(options, cardOption{Text: managementStyledText(effort, cardTitle{Tag: "plain_text"}), Value: effort})
	}
	return options
}

func managementProfileOptions(state managementState, includeCurrentOnly bool) []cardOption {
	options := make([]cardOption, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		if profile.CurrentOnly && !includeCurrentOnly {
			continue
		}
		var label any = managementProfileOptionLabel(profile)
		if profile.CurrentOnly {
			label = managementConcat(managementCopy("management.keep.current.unavailable.in.library"), label)
		}
		options = append(options, cardOption{Text: managementStyledText(label, cardTitle{Tag: "plain_text"}), Value: profile.Ref})
	}
	return options
}

func managementModelOptions(state managementState, agentRef string) []cardOption {
	options := []cardOption{{Text: managementStyledText(managementProfileDefaultModelOptionLabel(state, agentRef), cardTitle{Tag: "plain_text"}), Value: managementProfileDefaultModelOption}}
	for _, profile := range state.Profiles {
		if profile.Ref != agentRef {
			continue
		}
		if profile.CallerDefaults {
			runtimeOptions := managementRuntimeModelOptions(state, profile.AgentType)
			// Keep the shortcut's default model label; only explicit choices
			// come from the runtime catalog and execution-scoped discovery.
			return append(options, runtimeOptions[1:]...)
		}
		for _, modelRef := range profile.SupportedModels {
			options = append(options, cardOption{Text: managementStyledText(modelRef, cardTitle{Tag: "plain_text"}), Value: modelRef})
		}
		break
	}
	return options
}

func managementProfileLibraryHintElement(state managementState) cardElement {
	count := 0
	for _, profile := range state.Profiles {
		if !profile.CurrentOnly {
			count++
		}
	}
	if count == 0 {
		return managementHintElement(managementCopy("management.no.experts.are.available.in.the.library"))
	}
	return managementHintElement(managementCopy("management.search.all.d.experts.in.the.library", count))
}

func managementProfileOptionLabel(profile managementProfileSummary) any {
	name := valueOrPlaceholder(profile.Name, profile.Ref)
	var label any = name
	if library := strings.TrimSpace(profile.LibraryName); library != "" {
		label = managementConcat(name, "（", managementNonemptyText(profile.LibraryLabel, library), "）")
	}
	// Leave room for runtime discovery and daily cleanup controls in bilingual cards.
	description := shortActivityText(strings.TrimSpace(profile.Description), 36)
	if description != "" {
		label = managementConcat(label, " · ", description)
	}
	return label
}

func managementSettingsHintColumn(element cardElement) cardColumn {
	return cardColumn{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalAlign: "top", Elements: []cardElement{element}}
}

func managementSettingsFieldColumn(label any, field cardElement) cardColumn {
	return cardColumn{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalAlign: "top", VerticalSpacing: "4px", Elements: []cardElement{managementTextElement(label, "notation", "grey", nil), field}}
}

func managementSelectStaticElement(name string, placeholder any, initial string, options []cardOption) cardElement {
	return cardElement{Tag: "select_static", Name: name, Options: options, InitialOption: initial, Width: "fill", Required: true, Placeholder: managementPlainTextPtr(placeholder)}
}

func managementFormSubmitButton(action string, text any) cardElement {
	raw, _ := json.Marshal(managementButtonValue(managementButton{Action: action}))
	return cardElement{Tag: "button", Name: action, Text: managementPlainTextPtr(text), Type: "primary", Size: "medium", ActionType: "form_submit", Value: raw}
}

func managementCallbackButton(action string, text any, buttonType string) cardElement {
	raw, _ := json.Marshal(managementButtonValue(managementButton{Action: action}))
	return cardElement{Tag: "button", Name: action, Text: managementPlainTextPtr(text), Type: buttonType, Size: "medium", Value: raw}
}

func managementFormButtonRowElement(buttons ...cardElement) cardElement {
	columns := make([]cardColumn, 0, len(buttons))
	for _, button := range buttons {
		columns = append(columns, cardColumn{Tag: "column", Width: "auto", VerticalAlign: "top", Elements: []cardElement{button}})
	}
	return cardElement{Tag: "column_set", FlexMode: "flow", HorizontalSpacing: "8px", Columns: columns}
}

func managementButtonRowElement(buttons []managementButton) cardElement {
	columns := make([]cardColumn, 0, len(buttons))
	for _, button := range buttons {
		columns = append(columns, cardColumn{Tag: "column", Width: "auto", VerticalAlign: "top", Elements: []cardElement{managementButtonElement(button)}})
	}
	return cardElement{Tag: "column_set", FlexMode: "flow", HorizontalSpacing: "8px", Columns: columns}
}

func managementSplitActionRowElement(left, right cardElement) cardElement {
	if left.Tag == "" {
		return cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, HorizontalAlign: "right", Elements: []cardElement{right}}}}
	}
	return cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "8px", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, Elements: []cardElement{left}}, {Tag: "column", Width: "auto", HorizontalAlign: "right", Elements: []cardElement{right}}}}
}

func managementButtonElement(button managementButton) cardElement {
	if button.Type == "default" && button.Icon != nil && button.Icon.Color == "blue" {
		icon := *button.Icon
		icon.Color = "grey"
		button.Icon = &icon
	}
	if strings.TrimSpace(button.URL) != "" {
		return cardElement{Tag: "button", Text: managementPlainTextPtr(button.Text), Type: button.Type, Size: "medium", URL: strings.TrimSpace(button.URL), Icon: button.Icon}
	}
	element := managementCallbackButton(button.Action, button.Text, button.Type)
	if strings.TrimSpace(button.Name) != "" {
		element.Name = strings.TrimSpace(button.Name)
	}
	raw, _ := json.Marshal(managementButtonValue(button))
	element.Value = raw
	element.Icon = button.Icon
	element.Confirm = button.Confirm
	return element
}

func managementButtonValue(button managementButton) map[string]string {
	value := make(map[string]string, len(button.Values)+1)
	value[managementActionKey] = button.Action
	for k, v := range button.Values {
		value[k] = v
	}
	return value
}

func managementInteractionText(mode string) any {
	switch strings.TrimSpace(mode) {
	case config.FeishuInteractionSingleton:
		return managementCopy("management.continue.the.same.conversation.135")
	case interactionModeThreadIsolated:
		return managementCopy("management.separate.topics.user.isolation")
	default:
		return managementCopy("management.separate.conversation.per.topic")
	}
}

func managementInteractionDescription(mode string) any {
	switch strings.TrimSpace(mode) {
	case config.FeishuInteractionSingleton:
		return managementCopy("management.messages.sent.to.the.assistant.continue.the.previous.conversation")
	case interactionModeThreadIsolated:
		return managementCopy("management.each.new.topic.is.independent.and.runs.in.a")
	default:
		return managementCopy("management.each.new.topic.starts.fresh.independently.of.other.topics")
	}
}

func managementWorkspaceSummary(state managementState) any {
	if strings.TrimSpace(state.Mode) != config.FeishuInteractionSingleton {
		if strings.TrimSpace(state.Mode) == interactionModeThreadIsolated {
			return managementCopy("management.each.new.topic.uses.an.empty.workspace.under.its")
		}
		return managementCopy("management.each.new.topic.uses.a.separate.empty.workspace")
	}
	if state.BotSession != nil {
		if cwd := strings.TrimSpace(state.BotSession.Cwd); cwd != "" {
			return managementConcat(managementCopy("management.current.workspace"), cwd)
		}
	}
	return managementCopy("management.current.workspace.not.recorded.rebind.it.on.the.machine")
}

func managementInteractionHelpTitle(mode string) any {
	if strings.TrimSpace(mode) == config.FeishuInteractionSingleton {
		return managementCopy("management.continue.the.same.conversation.144")
	}
	return managementCopy("management.use.separate.conversations.per.topic")
}

func managementInteractionHelpBody(mode string) any {
	if strings.TrimSpace(mode) == config.FeishuInteractionSingleton {
		return managementConcat(managementCopy("management.private.chat.send.a.message.directly.group.chat.mention"), managementInteractionDescription(mode))
	}
	return managementConcat(managementCopy("management.private.chat.send.a.message.to.start.a.task"), managementInteractionDescription(mode))
}

func managementMessageHandlingHintElement() cardElement {
	return managementHintElement(managementConcat(managementInteractionText(config.FeishuInteractionSingleton), managementCopy("management.continues.the.previous.conversation"), managementInteractionText(config.FeishuInteractionThread), managementCopy("management.starts.each.new.topic.independently"), managementInteractionText(interactionModeThreadIsolated), managementCopy("management.keeps.topics.independent.each.in.a.separate.user.environment")))
}

func managementGroupCollaborationHintText() any {
	return managementCopy("management.to.let.other.group.members.use.the.assistant.mention")
}

func managementModeSummary(draft managementSettingsDraft) any {
	draft = managementNormalizeDraft(draft)
	return managementInteractionText(draft.Interaction)
}

func managementDefaultAgentName(state managementState) any {
	agent := effectiveBotDefaultAgentRef(state.DefaultAgent)
	return managementProfileNameByRef(state, agent)
}

func managementProfileNameByRef(state managementState, ref string) any {
	ref = strings.TrimSpace(ref)
	for _, profile := range state.Profiles {
		if strings.TrimSpace(profile.Ref) != ref {
			continue
		}
		name := strings.TrimSpace(profile.Name)
		if name == "" || name == ref {
			name = displayProfileRefWithoutRevision(ref)
		}
		if library := strings.TrimSpace(profile.LibraryName); library != "" {
			return managementConcat(name, "（", managementNonemptyText(profile.LibraryLabel, library), "）")
		}
		return name
	}
	return displayProfileRefWithoutRevision(ref)
}

func displayProfileRefWithoutRevision(ref string) string {
	ref = strings.TrimSpace(ref)
	slash := strings.LastIndex(ref, "/")
	at := strings.LastIndex(ref, "@")
	if slash >= 0 && at > slash && at < len(ref)-1 {
		return ref[:at]
	}
	return ref
}

func managementUptimeText(startedAt, now time.Time) any {
	if startedAt.IsZero() || now.Before(startedAt) {
		return managementCopy("management.unknown")
	}
	duration := now.Sub(startedAt)
	days := int(duration / (24 * time.Hour))
	hours := int(duration/time.Hour) % 24
	minutes := int(duration/time.Minute) % 60
	switch {
	case days > 0 && hours > 0:
		return managementCopy("management.d.days.d.hours", managementCopy("management.d.days", days), managementCopy("management.d.hours", hours))
	case days > 0:
		return managementCopy("management.d.days", days)
	case hours > 0:
		return managementCopy("management.d.hours", hours)
	case minutes > 0:
		return managementCopy("management.d.minutes", minutes)
	default:
		return managementCopy("management.less.than.1.minute")
	}
}

func valueOrPlaceholder(value, placeholder string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return placeholder
}

type managementButton struct {
	Text    any
	Type    string
	Action  string
	Name    string
	URL     string
	Values  map[string]string
	Icon    *cardIcon
	Confirm *cardConfirm
}
type managementSummaryItem struct {
	Title any
	Body  any
}
type managementCardView struct {
	Title    any
	Subtitle any
	Summary  any
	Theme    string
	Icon     cardIcon
	Elements []cardElement
	Buttons  []managementButton
}
