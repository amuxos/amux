package bridge

import (
	"encoding/json"
	"fmt"
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
	element := managementNavigationBackElement("返回概览", managementActionStatusOpen, "amux.back.overview")
	element.ElementID = "amux_management_back"
	return element
}
func managementSubpageBackElement(text, action, name string) cardElement {
	element := managementNavigationBackElement(text, action, name)
	element.ElementID = strings.ReplaceAll(name, ".", "_")
	return element
}
func managementNavigationBackElement(text, action, name string) cardElement {
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
	statusTitle := "运行正常"
	statusSummary := fmt.Sprintf("可用运行环境 %d 个 · 可以开始任务", state.Targets)
	statusColor := "default"
	statusIcon := managementIconPtr("check_outlined", "green")
	if state.SystemStatus != managementSystemHealthy {
		theme = "orange"
		statusTitle = "暂时无法开始新任务"
		statusSummary = "没有可用运行环境 · 请查看「系统状态」"
		statusColor = "orange"
		statusIcon = managementIconPtr("warning_outlined", "orange")
	}
	elements := []cardElement{managementTextElement(statusTitle, "heading", statusColor, statusIcon), managementTextElement(statusSummary, "notation", statusColor, nil)}
	if state.SystemStatus == managementSystemHealthy && state.Update.ShouldPrompt {
		elements = append(elements, managementUpdateNoticeElement(state.Update))
	}
	elements = append(elements, managementDividerElement(), managementSectionTitleElement("当前配置"), managementOverviewConfigElement(state, draft), managementDividerElement(), managementSectionTitleElement("管理"), managementOverviewActionsElement(state), managementTextElement("重新发送 /amux 刷新状态", "notation", "grey", managementIconPtr("refresh_outlined", "grey")))
	card := marshalNativeManagementCard(managementCardView{Title: "amux", Summary: "amux", Theme: theme, Icon: managementIcon("robot_outlined", theme), Elements: elements})
	return renderFocusedManagementCardForSurface(card, managementSurfaceAmux)
}
func managementOverviewConfigElement(state managementState, draft managementSettingsDraft) cardElement {
	element := managementVerticalSummaryElement("amux_overview_config",
		managementKeyValueRowElement(managementMessageHandlingSummary, managementModeSummary(draft)),
		managementTextElement(managementWorkspaceSummary(state), "notation", "grey", managementIconPtr("info_outlined", "grey")),
		managementKeyValueRowElement("默认专家", managementProfileNameByRef(state, draft.DefaultAgent)),
		managementKeyValueRowElement("运行引擎", managementRuntimeLabel(state, draft.DefaultRuntimeRef)),
		managementKeyValueRowElement("默认模型", managementRuntimeModelLabel(draft.DefaultModelRef)),
		managementKeyValueRowElement("思考强度", managementReasoningEffortLabel(draft.DefaultReasoningEffort)),
		managementKeyValueRowElement("处理过程展示", managementThinkingProcessLabel(draft.ThinkingProcess)))
	element.Columns[0].BackgroundStyle = "blue-50"
	element.Columns[0].VerticalSpacing = "4px"
	return element
}
func managementKeyValueRowElement(label, value string) cardElement {
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
		rows = append(rows, managementKeyValueRowElement(item.Title, valueOrPlaceholder(strings.TrimSpace(item.Body), "—")))
	}
	element := managementVerticalSummaryElement(elementID, rows...)
	element.Columns[0].VerticalSpacing = "4px"
	return element
}
func managementTaskDefaultsElement(state managementState, draft managementSettingsDraft, elementID string) cardElement {
	element := managementVerticalSummaryElement(elementID,
		managementKeyValueRowElement(managementMessageHandlingSummary, managementModeSummary(draft)),
		managementTextElement(managementWorkspaceSummary(state), "notation", "grey", managementIconPtr("info_outlined", "grey")),
		managementKeyValueRowElement("默认专家", managementProfileNameByRef(state, draft.DefaultAgent)),
		managementKeyValueRowElement("运行引擎", managementRuntimeLabel(state, draft.DefaultRuntimeRef)),
		managementKeyValueRowElement("默认模型", managementRuntimeModelLabel(draft.DefaultModelRef)),
		managementKeyValueRowElement("思考强度", managementReasoningEffortLabel(draft.DefaultReasoningEffort)),
		managementKeyValueRowElement("处理过程展示", managementThinkingProcessLabel(draft.ThinkingProcess)))
	element.Columns[0].BackgroundStyle = "blue-50"
	element.Columns[0].VerticalSpacing = "4px"
	return element
}
func managementVerticalSummaryElement(elementID string, rows ...cardElement) cardElement {
	return cardElement{Tag: "column_set", ElementID: elementID, FlexMode: "none", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalAlign: "top", VerticalSpacing: "4px", BackgroundStyle: "grey-50", Padding: "8px 12px 8px 12px", Elements: rows}}}
}
func managementOverviewActionsElement(state managementState) cardElement {
	items := []managementButton{
		{Text: "系统状态", Type: "default", Action: managementActionSystemMaintenance, Name: "amux.overview.system", Icon: managementIconPtr("setting_outlined", "grey")},
		{Text: "助手设置", Type: "primary", Action: managementActionSettingsOpen, Name: "amux.overview.settings", Icon: managementIconPtr("setting_outlined", "blue")},
		{Text: "专家库", Type: "default", Action: managementActionPackLibraryOpen, Name: "amux.overview.library", Icon: managementIconPtr("robot_outlined", "grey")},
		{Text: "实验功能", Type: "default", Action: managementActionExperimentsOpen, Name: "amux.overview.experiments", Icon: managementIconPtr("setting_outlined", "grey")},
		{Text: "使用帮助", Type: "default", Action: managementActionHelpOpen, Name: "amux.overview.help", Icon: managementIconPtr("info_outlined", "grey")},
	}
	if state.SystemStatus != managementSystemHealthy || state.Update.ShouldPrompt {
		items[0].Type = "primary"
		items[0].Icon = managementIconPtr("setting_outlined", "blue")
		items[1].Type = "default"
		items[1].Icon = managementIconPtr("setting_outlined", "grey")
	}
	if state.SystemStatus == managementSystemHealthy && state.Update.ShouldPrompt {
		items[0].Text = "查看更新"
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
	if strings.TrimSpace(view.Subtitle) != "" {
		status := managementTextElement(strings.TrimSpace(view.Subtitle), "notation", "grey", nil)
		status.Margin = "0px 0px 2px 0px"
		elements = append([]cardElement{status}, elements...)
	}
	if len(view.Buttons) > 0 {
		elements = append(elements, managementButtonRowElement(view.Buttons))
	}
	doc := newCardDoc(elements)
	doc.Config.WideScreenMode = false
	doc.Config.WidthMode = ""
	doc.Config.Summary = &cardSummary{Content: valueOrPlaceholder(view.Summary, view.Title)}
	enableForward := false
	updateMulti := true
	doc.Config.EnableForward = &enableForward
	doc.Config.UpdateMulti = &updateMulti
	doc.Body.Direction = "vertical"
	doc.Body.VerticalSpacing = "8px"
	doc.Body.Padding = "12px 16px 16px 16px"
	if strings.TrimSpace(view.Title) != "" {
		header := &cardHeader{Title: cardTitle{Tag: "plain_text", Content: strings.TrimSpace(view.Title)}, Template: valueOrPlaceholder(view.Theme, "default"), Padding: "8px 16px 8px 16px"}
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
func managementConfirm(title, text string) *cardConfirm {
	return &cardConfirm{Title: cardTitle{Tag: "plain_text", Content: strings.TrimSpace(title)}, Text: cardTitle{Tag: "plain_text", Content: strings.TrimSpace(text)}}
}
func managementTextElement(content, size, color string, icon *cardIcon) cardElement {
	if strings.TrimSpace(size) == "" {
		size = "normal"
	}
	if strings.TrimSpace(color) == "" {
		color = "default"
	}
	return cardElement{Tag: "div", Text: &cardTitle{Tag: "plain_text", Content: strings.TrimSpace(content), TextSize: size, TextColor: color}, Icon: icon, Width: "fill"}
}
func managementSectionTitleElement(content string) cardElement {
	return managementTextElement(content, "normal", "grey", nil)
}
func managementDividerElement() cardElement {
	return cardElement{Tag: "hr", Margin: "2px 0px 2px 0px"}
}
func managementHintElement(text string) cardElement {
	element := managementTextElement(text, "notation", "grey", managementIconPtr("info_outlined", "grey"))
	element.Margin = "4px 0px 0px 0px"
	return element
}
func managementUpdateNoticeElement(update ManagementUpdateInfo) cardElement {
	text := "发现 amux 新版本 " + valueOrPlaceholder(update.LatestVersion, "未知")
	if current := strings.TrimSpace(update.CurrentVersion); current != "" {
		text += "（当前 " + current + "）"
	}
	element := managementTextElement(text, "notation", "orange", managementIconPtr("version_outlined", "orange"))
	element.Margin = "4px 0px 0px 0px"
	return element
}
func managementCompactSummaryElement(items ...managementSummaryItem) cardElement {
	columns := make([]cardColumn, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		body := strings.TrimSpace(item.Body)
		if body == "" {
			body = "—"
		}
		if title == "" {
			title = "当前设置"
		}
		columns = append(columns, cardColumn{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalAlign: "top", VerticalSpacing: "2px", BackgroundStyle: "grey-50", Padding: "8px 10px 8px 10px", Elements: []cardElement{managementTextElement(title, "notation", "grey", nil), managementTextElement(body, "normal", "default", nil)}})
	}
	return cardElement{Tag: "column_set", FlexMode: "flow", HorizontalSpacing: "8px", Margin: "2px 0px 2px 0px", Columns: columns}
}
func managementMaintenanceDetailsElement(state managementState) cardElement {
	return managementCompactSummaryElement(managementSummaryItem{Title: managementMessageHandlingSummary, Body: managementModeSummary(managementDraftFromState(state))}, managementSummaryItem{Title: "版本", Body: valueOrPlaceholder(state.SystemInfo.Version, "未知")}, managementSummaryItem{Title: "运行时长", Body: managementUptimeText(state.SystemInfo.StartedAt, time.Now())}, managementSummaryItem{Title: "已连接智能体", Body: fmt.Sprintf("%d 个", state.SystemInfo.BotCount)}, managementSummaryItem{Title: "Hub 主机 IP", Body: valueOrPlaceholder(state.SystemInfo.HubIP, "未知")})
}
func managementSettingsEditorElements(state managementState, name string, draft managementSettingsDraft, submitAction, submitText, cancelAction string, trailingAction *managementButton, leftHint cardElement) []cardElement {
	draft = managementNormalizeDraftForState(state, draft)
	submitButton := managementFormSubmitButton(submitAction, submitText)
	submitButton.Value, _ = json.Marshal(managementButtonValue(managementButton{Action: submitAction, Values: map[string]string{
		"interaction": draft.Interaction, "default_agent": draft.DefaultAgent,
		"default_runtime_ref": draft.DefaultRuntimeRef, "default_model_ref": draft.DefaultModelRef,
		"default_reasoning_effort": draft.DefaultReasoningEffort,
		"thinking_process":         draft.ThinkingProcess,
	}}))
	if state.Mode != config.FeishuInteractionSingleton && draft.Interaction == config.FeishuInteractionSingleton {
		submitButton.Confirm = managementConfirm("改为继续同一对话？", "在已授权位置中发给智能体的消息会接着同一段对话处理。")
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
			"thinking_process":         draft.ThinkingProcess, managementDraftFieldKey: field,
		}})}}
	}
	buttonElements := []cardElement{submitButton}
	if cancelAction != "" {
		buttonElements = append(buttonElements, managementCallbackButton(cancelAction, "取消", "default"))
	}
	buttons := managementFormButtonRowElement(buttonElements...)
	if trailingAction != nil {
		buttons = managementSplitActionRowElement(buttons, managementButtonElement(*trailingAction))
	}
	profileOptions := managementProfileOptions(state, true)
	interactionSelect := managementSelectStaticElement("interaction", "选择消息处理方式", draft.Interaction, []cardOption{{Text: cardTitle{Tag: "plain_text", Content: managementInteractionText(config.FeishuInteractionThread)}, Value: config.FeishuInteractionThread}, {Text: cardTitle{Tag: "plain_text", Content: managementInteractionText(interactionModeThreadIsolated)}, Value: interactionModeThreadIsolated}, {Text: cardTitle{Tag: "plain_text", Content: managementInteractionText(config.FeishuInteractionSingleton)}, Value: config.FeishuInteractionSingleton}})
	interactionSelect.Required = false
	interactionSelect.Behaviors = draftChangeBehavior("interaction")
	profileField := managementHintElement("专家库中暂无可用专家，请先添加专家。")
	if len(profileOptions) > 0 {
		profileField = managementSelectStaticElement("default_agent", "搜索专家、专家库或说明", draft.DefaultAgent, profileOptions)
		profileField.Required = false
		profileField.Behaviors = draftChangeBehavior("default_agent")
	}
	runtimeOptions := managementRuntimeOptions(state)
	runtimeField := managementSelectStaticElement("default_runtime_ref", "选择运行引擎", draft.DefaultRuntimeRef, runtimeOptions)
	runtimeField.Required = false
	runtimeField.Behaviors = draftChangeBehavior("default_runtime_ref")
	modelOptions := managementRuntimeModelOptions(state, draft.DefaultRuntimeRef)
	initialModelRef := draft.DefaultModelRef
	if initialModelRef == "" {
		initialModelRef = managementProfileDefaultModelOption
	}
	modelField := managementSelectStaticElement("default_model_ref", "选择该运行引擎支持的模型", initialModelRef, modelOptions)
	modelField.Required = false
	modelField.Behaviors = draftChangeBehavior("default_model_ref")
	reasoningOptions := managementRuntimeReasoningOptions(state, draft.DefaultRuntimeRef, draft.DefaultModelRef)
	initialReasoningEffort := draft.DefaultReasoningEffort
	if initialReasoningEffort == "" {
		initialReasoningEffort = managementRuntimeDefaultReasoningOption
	}
	reasoningField := managementSelectStaticElement("default_reasoning_effort", "选择思考强度", initialReasoningEffort, reasoningOptions)
	reasoningField.Required = false
	reasoningField.Behaviors = draftChangeBehavior("default_reasoning_effort")
	thinkingProcessField := managementSelectStaticElement("thinking_process", "选择处理过程展示方式", draft.ThinkingProcess, []cardOption{
		{Text: cardTitle{Tag: "plain_text", Content: "折叠展示（默认）"}, Value: config.FeishuThinkingProcessCollapsed},
		{Text: cardTitle{Tag: "plain_text", Content: "展开展示"}, Value: config.FeishuThinkingProcessExpanded},
		{Text: cardTitle{Tag: "plain_text", Content: "不展示"}, Value: config.FeishuThinkingProcessHidden},
	})
	thinkingProcessField.Required = false
	thinkingProcessField.Behaviors = draftChangeBehavior("thinking_process")
	hintRow := cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "16px", Columns: []cardColumn{managementSettingsHintColumn(leftHint), managementSettingsHintColumn(managementProfileLibraryHintElement(state))}}
	profileRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn("默认专家", profileField)}}
	runtimeRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn("运行引擎（仅影响新会话）", runtimeField)}}
	interactionRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn(managementMessageHandlingQuestion, interactionSelect)}}
	modelRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn("默认模型（仅影响新会话）", modelField)}}
	reasoningRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn("思考强度（仅影响新会话）", reasoningField)}}
	thinkingProcessRow := cardElement{Tag: "column_set", FlexMode: "none", Columns: []cardColumn{managementSettingsFieldColumn("处理过程展示", thinkingProcessField)}}
	buttons.Margin = "4px 0px 0px 0px"
	form := cardElement{Tag: "form", Name: name, Direction: "vertical", VerticalSpacing: "8px", Elements: []cardElement{buttons}}
	return []cardElement{hintRow, profileRow, runtimeRow, modelRow, reasoningRow, interactionRow, thinkingProcessRow, form}
}
func managementRuntimeOptions(state managementState) []cardOption {
	runtimes := managementRuntimeCatalog(state)
	options := make([]cardOption, 0, len(runtimes))
	for _, runtime := range runtimes {
		label := strings.TrimSpace(runtime.DisplayName)
		if label == "" {
			label = strings.TrimSpace(runtime.Ref)
		}
		options = append(options, cardOption{Text: cardTitle{Tag: "plain_text", Content: label}, Value: runtime.Ref})
	}
	return options
}
func managementRuntimeModelOptions(state managementState, runtimeRef string) []cardOption {
	runtime, ok := managementRuntimeByRef(state, runtimeRef)
	defaultLabel := "使用运行引擎默认模型"
	if ok {
		name := strings.TrimSpace(runtime.DisplayName)
		if name == "" {
			name = strings.TrimSpace(runtime.Ref)
		}
		defaultLabel = "使用 " + name + " 默认模型"
	}
	options := []cardOption{{Text: cardTitle{Tag: "plain_text", Content: defaultLabel}, Value: managementProfileDefaultModelOption}}
	if !ok {
		return options
	}
	for _, model := range runtime.Models {
		options = append(options, cardOption{Text: cardTitle{Tag: "plain_text", Content: model.Ref}, Value: model.Ref})
	}
	return options
}
func managementRuntimeReasoningOptions(state managementState, runtimeRef, modelRef string) []cardOption {
	options := []cardOption{{Text: cardTitle{Tag: "plain_text", Content: "使用运行引擎默认强度"}, Value: managementRuntimeDefaultReasoningOption}}
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
	for _, effort := range efforts {
		options = append(options, cardOption{Text: cardTitle{Tag: "plain_text", Content: effort}, Value: effort})
	}
	return options
}
func managementProfileReasoningOptions(state managementState, profileRef, modelRef string) []cardOption {
	options := []cardOption{{Text: cardTitle{Tag: "plain_text", Content: "使用运行引擎默认强度"}, Value: managementRuntimeDefaultReasoningOption}}
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
		options = append(options, cardOption{Text: cardTitle{Tag: "plain_text", Content: effort}, Value: effort})
	}
	return options
}
func managementProfileOptions(state managementState, includeCurrentOnly bool) []cardOption {
	options := make([]cardOption, 0, len(state.Profiles))
	for _, profile := range state.Profiles {
		if profile.CurrentOnly && !includeCurrentOnly {
			continue
		}
		label := managementProfileOptionLabel(profile)
		if profile.CurrentOnly {
			label = "保持当前（库中不可用） · " + label
		}
		options = append(options, cardOption{Text: cardTitle{Tag: "plain_text", Content: label}, Value: profile.Ref})
	}
	return options
}
func managementModelOptions(state managementState, agentRef string) []cardOption {
	options := []cardOption{{Text: cardTitle{Tag: "plain_text", Content: managementProfileDefaultModelOptionLabel(state, agentRef)}, Value: managementProfileDefaultModelOption}}
	for _, profile := range state.Profiles {
		if profile.Ref != agentRef {
			continue
		}
		for _, modelRef := range profile.SupportedModels {
			options = append(options, cardOption{Text: cardTitle{Tag: "plain_text", Content: modelRef}, Value: modelRef})
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
		return managementHintElement("当前专家库暂无可用专家。")
	}
	return managementHintElement(fmt.Sprintf("可搜索专家库中的全部专家，共 %d 个。", count))
}
func managementProfileOptionLabel(profile managementProfileSummary) string {
	ref := strings.TrimSpace(profile.Ref)
	if ref == "" {
		ref = strings.TrimSpace(profile.Name)
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = ref
	}
	if library := strings.TrimSpace(profile.LibraryName); library != "" {
		name += "（" + library + "）"
	}
	description := shortActivityText(strings.TrimSpace(profile.Description), 56)
	if description == "" {
		return name
	}
	return name + " · " + description
}
func managementSettingsHintColumn(element cardElement) cardColumn {
	return cardColumn{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalAlign: "top", Elements: []cardElement{element}}
}
func managementSettingsFieldColumn(label string, field cardElement) cardColumn {
	return cardColumn{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalAlign: "top", VerticalSpacing: "4px", Elements: []cardElement{managementTextElement(label, "notation", "grey", nil), field}}
}
func managementSelectStaticElement(name, placeholder, initial string, options []cardOption) cardElement {
	return cardElement{Tag: "select_static", Name: name, Options: options, InitialOption: initial, Width: "fill", Required: true, Placeholder: &cardTitle{Tag: "plain_text", Content: placeholder}}
}
func managementFormSubmitButton(action, text string) cardElement {
	raw, _ := json.Marshal(managementButtonValue(managementButton{Action: action}))
	return cardElement{Tag: "button", Name: action, Text: &cardTitle{Tag: "plain_text", Content: text}, Type: "primary", Size: "medium", ActionType: "form_submit", Value: raw}
}
func managementCallbackButton(action, text, buttonType string) cardElement {
	raw, _ := json.Marshal(managementButtonValue(managementButton{Action: action}))
	return cardElement{Tag: "button", Name: action, Text: &cardTitle{Tag: "plain_text", Content: text}, Type: buttonType, Size: "medium", Value: raw}
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
		return cardElement{Tag: "button", Text: &cardTitle{Tag: "plain_text", Content: button.Text}, Type: button.Type, Size: "medium", URL: strings.TrimSpace(button.URL), Icon: button.Icon}
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
func managementInteractionText(mode string) string {
	switch strings.TrimSpace(mode) {
	case config.FeishuInteractionSingleton:
		return "继续同一对话"
	case interactionModeThreadIsolated:
		return "每个话题独立（用户隔离）"
	default:
		return "每个话题独立"
	}
}
func managementInteractionDescription(mode string) string {
	switch strings.TrimSpace(mode) {
	case config.FeishuInteractionSingleton:
		return "发给智能体的消息会接着之前的对话处理。"
	case interactionModeThreadIsolated:
		return "每个新话题互不影响，且各自运行在独立的用户环境（隔离模式）。"
	default:
		return "每个新话题都从头开始，话题之间互不影响。"
	}
}
func managementWorkspaceSummary(state managementState) string {
	if strings.TrimSpace(state.Mode) != config.FeishuInteractionSingleton {
		if strings.TrimSpace(state.Mode) == interactionModeThreadIsolated {
			return "每个新话题使用隔离用户下的独立空工作区"
		}
		return "每个新话题使用独立空工作区"
	}
	if state.BotSession != nil {
		if cwd := strings.TrimSpace(state.BotSession.Cwd); cwd != "" {
			return "当前工作区：" + cwd
		}
	}
	return "当前工作区：未记录，请在运行 amux 的机器上重新绑定"
}
func managementInteractionHelpTitle(mode string) string {
	if strings.TrimSpace(mode) == config.FeishuInteractionSingleton {
		return "如何继续同一对话"
	}
	return "如何让每个话题独立"
}
func managementInteractionHelpBody(mode string) string {
	if strings.TrimSpace(mode) == config.FeishuInteractionSingleton {
		return "私聊智能体：直接发送消息\n群聊：@智能体 后发送消息\n" + managementInteractionDescription(mode)
	}
	return "私聊智能体：发送消息发起任务\n群聊：@智能体 后发送消息发起任务\n在对应飞书话题中继续讨论\n" + managementInteractionDescription(mode)
}
func managementMessageHandlingHintElement() cardElement {
	return managementHintElement(managementInteractionText(config.FeishuInteractionSingleton) + "：接着之前的对话处理。\n" + managementInteractionText(config.FeishuInteractionThread) + "：新话题从头开始，互不影响。\n" + managementInteractionText(interactionModeThreadIsolated) + "：新话题互不影响，且各自运行在独立的用户环境（隔离模式）。")
}
func managementGroupCollaborationHintText() string {
	return "需要让群里的其他人使用时，请在对应群里 @当前智能体 发送“开启协作模式”；需要恢复为仅负责人使用时，请同样 @当前智能体 发送“关闭协作模式”。"
}
func managementModeSummary(draft managementSettingsDraft) string {
	draft = managementNormalizeDraft(draft)
	return managementInteractionText(draft.Interaction)
}
func managementDefaultAgentName(state managementState) string {
	agent := effectiveBotDefaultAgentRef(state.DefaultAgent)
	return managementProfileNameByRef(state, agent)
}
func managementProfileNameByRef(state managementState, ref string) string {
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
			name += "（" + library + "）"
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
func managementUptimeText(startedAt, now time.Time) string {
	if startedAt.IsZero() || now.Before(startedAt) {
		return "未知"
	}
	duration := now.Sub(startedAt)
	days := int(duration / (24 * time.Hour))
	hours := int(duration/time.Hour) % 24
	minutes := int(duration/time.Minute) % 60
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	case days > 0:
		return fmt.Sprintf("%d 天", days)
	case hours > 0:
		return fmt.Sprintf("%d 小时", hours)
	case minutes > 0:
		return fmt.Sprintf("%d 分钟", minutes)
	default:
		return "不到 1 分钟"
	}
}
func valueOrPlaceholder(value, placeholder string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return placeholder
}

type managementButton struct {
	Text    string
	Type    string
	Action  string
	Name    string
	URL     string
	Values  map[string]string
	Icon    *cardIcon
	Confirm *cardConfirm
}
type managementSummaryItem struct {
	Title string
	Body  string
}
type managementCardView struct {
	Title    string
	Subtitle string
	Summary  string
	Theme    string
	Icon     cardIcon
	Elements []cardElement
	Buttons  []managementButton
}
