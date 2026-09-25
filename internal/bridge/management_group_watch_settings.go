package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/amuxos/amux/internal/config"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func (b *Bridge) groupWatchManagementState(state managementState, chatID string) managementState {
	if b.db != nil && chatID != "" {
		if ws, err := b.db.ReadWorkspace(b.currentBridgeId(), chatID); err == nil && ws != nil {
			if ws.DefaultAgent != "" {
				state.DefaultAgent = ws.DefaultAgent
			}
			state.WorkspaceModelRef = ws.DefaultModel
			state.NativeModelsTargetRef = ws.DefaultTarget
		}
		if settings, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID); err == nil && settings != nil {
			state.Mode = settings.InteractionMode
			if settings.IsolationRequired {
				state.Mode = interactionModeThreadIsolated
			}
			if settings.DefaultAgent != "" {
				state.DefaultAgent = settings.DefaultAgent
			}
			state.DefaultRuntimeRef, state.DefaultModelRef = settings.DefaultRuntimeRef, settings.DefaultModelRef
			state.DefaultReasoningEffort = settings.DefaultReasoningEffort
			state.WorkspaceModelRef = ""
			state.NativeModelsTargetRef = settings.DefaultTarget
		}
	}
	state.GroupWatch = state.GroupWatch.ForChat(chatID)
	return state
}

func managementGroupWatchScopeDefaults(state managementState, draft managementGroupWatchDraft) managementGroupWatchDraft {
	if draft.Enabled == "" {
		draft.Enabled = "true"
	}
	if draft.Version == "" {
		draft.Version = "0"
	}
	if draft.MaxActive == "" {
		limit := state.GroupWatch.MaxActiveSessionsPerChat
		if limit < 1 {
			limit = config.DefaultFeishuGroupWatchMaxActiveSessionsPerChat
		}
		draft.MaxActive = strconv.Itoa(limit)
	}
	if draft.IdleSeconds == "" {
		seconds := state.GroupWatch.IdleTimeoutSeconds
		if seconds < 60 {
			seconds = config.DefaultFeishuGroupWatchIdleTimeoutSeconds
		}
		draft.IdleSeconds = strconv.Itoa(seconds)
	}
	return draft
}

func managementGroupWatchScopeFromCallback(action *callback.CallBackAction, state managementState, draft managementGroupWatchDraft) managementGroupWatchDraft {
	draft.ChatID = managementCallbackString(action, "chat_id")
	draft.ChatName = managementCallbackString(action, "chat_name")
	state.GroupWatch = state.GroupWatch.ForChat(draft.ChatID)
	seed := managementGroupWatchDraftFromState(state)
	seed.ChatID, seed.ChatName = draft.ChatID, draft.ChatName
	draft = managementGroupWatchScopeDefaults(state, seed)
	if saved, ok := state.GroupWatch.ChatSettings[draft.ChatID]; ok {
		draft.Enabled, draft.Version = strconv.FormatBool(saved.Enabled), strconv.FormatInt(saved.Version, 10)
	}
	for key, target := range map[string]*string{
		"group_watch_enabled": &draft.Enabled, "group_watch_version": &draft.Version,
		"group_watch_max_active": &draft.MaxActive, "group_watch_idle_seconds": &draft.IdleSeconds,
	} {
		if value, ok := managementDocumentCommentsCallbackValue(action, key); ok {
			*target = value
		}
	}
	if page, ok := managementDocumentCommentsCallbackValue(action, "group_watch_page"); ok {
		draft.Page, _ = strconv.Atoi(page)
	}
	return draft
}

func managementGroupWatchDraftValues(draft managementGroupWatchDraft) map[string]string {
	return map[string]string{
		"chat_id": draft.ChatID, "chat_name": draft.ChatName, "group_watch_version": draft.Version,
		"group_watch_enabled": draft.Enabled, "group_watch_max_active": draft.MaxActive, "group_watch_idle_seconds": draft.IdleSeconds,
		"group_watch_mode": draft.Mode, "group_watch_agent": draft.AgentRef, "group_watch_runtime_ref": draft.RuntimeRef,
		"group_watch_model_ref": draft.ModelRef, "group_watch_reasoning_effort": draft.ReasoningEffort,
		"group_watch_thinking_process": draft.ThinkingProcess, "group_watch_page": strconv.Itoa(draft.Page),
	}
}

func managementGroupWatchScopeSubmissionError(action *callback.CallBackAction, state managementState) any {
	chatID := managementCallbackString(action, "chat_id")
	if chatID == "" {
		return ""
	} // Legacy callers; live callbacks require a group.
	if !groupWatchConfigHasChat(state.GroupWatch, chatID) {
		return managementCopy("management.watch.group_unavailable")
	}
	get := func(key string) string {
		value, _ := managementDocumentCommentsCallbackValue(action, key)
		return value
	}
	if enabled := get("group_watch_enabled"); enabled != "true" && enabled != "false" {
		return managementCopy("management.watch.choose_status")
	}
	maxActive, err := strconv.Atoi(get("group_watch_max_active"))
	if err != nil || maxActive < 1 || maxActive > config.MaxFeishuGroupWatchActiveSessionsPerChat {
		return managementCopy("management.watch.invalid_limit")
	}
	idle, err := strconv.Atoi(get("group_watch_idle_seconds"))
	if err != nil || idle < 60 || idle > config.MaxFeishuGroupWatchIdleTimeoutSeconds {
		return managementCopy("management.watch.invalid_idle")
	}
	version, err := strconv.ParseInt(get("group_watch_version"), 10, 64)
	if err != nil || version < 0 || version != state.GroupWatch.ChatSettings[chatID].Version {
		return managementCopy("management.watch.stale")
	}
	return ""
}

func (b *Bridge) applyGroupWatchChatSettings(draft managementGroupWatchDraft, agentRef, runtimeRef, modelRef, effort string) error {
	b.groupWatchSettingsMu.Lock()
	defer b.groupWatchSettingsMu.Unlock()
	current := b.groupWatchSnapshot()
	if !groupWatchConfigHasChat(current, draft.ChatID) {
		return fmt.Errorf("group is no longer enrolled in watch")
	}
	maxActive, err := strconv.Atoi(draft.MaxActive)
	if err != nil {
		return err
	}
	idle, err := strconv.Atoi(draft.IdleSeconds)
	if err != nil {
		return err
	}
	version, err := strconv.ParseInt(draft.Version, 10, 64)
	if err != nil || version < 0 {
		return fmt.Errorf("invalid group watch settings version")
	}
	if draft.Enabled != "true" && draft.Enabled != "false" {
		return fmt.Errorf("invalid group watch enabled state")
	}
	value := config.BridgeFeishuGroupWatchChatConfig{
		Enabled: draft.Enabled == "true", AgentProfileRef: agentRef, RuntimeRef: runtimeRef,
		ModelRef: modelRef, ReasoningEffort: effort, ThinkingProcess: draft.ThinkingProcess,
		MaxActiveSessionsPerChat: maxActive, IdleTimeoutSeconds: idle,
	}
	if value.Enabled {
		mode, err := b.interactionModeForChat(draft.ChatID)
		if err != nil {
			return err
		}
		if mode == config.FeishuInteractionSingleton {
			return errGroupWatchInteraction
		}
		if _, err := b.newSessionPolicy(context.Background(), draft.ChatID); err != nil {
			return err
		}
	}
	if err := config.SetBridgeFeishuGroupWatchChat(b.ProfileName(), draft.ChatID, value, version); err != nil {
		return err
	}
	b.setGroupWatchConfig(config.GetBridgeFeishuGroupWatchForProfile(b.ProfileName()))
	return nil
}

func groupWatchSettingsButton(chatID, chatName string) managementButton {
	return managementButton{Text: managementCopy("management.watch.edit_group"), Type: "default", Action: managementActionGroupWatchEdit,
		Name: managementActionGroupWatchEdit + "." + strings.TrimSpace(chatID), Values: map[string]string{"chat_id": chatID, "chat_name": chatName}}
}

func renderGroupWatchGroupSummary(state managementState, draft managementGroupWatchDraft) string {
	mode := managementCopy("management.watch.inherit_group")
	if draft.Mode == managementDocumentCommentsModeCustom {
		mode = managementCopy("management.custom")
	}
	return marshalNativeManagementCard(managementCardView{
		Title: managementCopy("management.watch.group_saved"), Subtitle: managementConcat(draft.ChatName, " · ", draft.ChatID), Theme: "blue",
		Elements: []cardElement{managementKeyValueListElement("amux_watch_group_summary",
			managementSummaryItem{Title: managementCopy("management.current.status"), Body: managementGroupWatchCommandStatus(state, draft.ChatID)},
			managementSummaryItem{Title: managementCopy("management.task.configuration"), Body: mode},
			managementSummaryItem{Title: managementCopy("management.expert"), Body: managementProfileNameByRef(state, draft.AgentRef)},
			managementSummaryItem{Title: managementCopy("management.runtime"), Body: managementRuntimeLabel(state, draft.RuntimeRef)},
			managementSummaryItem{Title: managementCopy("management.model"), Body: managementRuntimeModelLabel(draft.ModelRef)},
			managementSummaryItem{Title: managementCopy("management.reasoning.effort"), Body: managementReasoningEffortLabel(draft.ReasoningEffort)},
			managementSummaryItem{Title: managementCopy("management.activity.display"), Body: managementThinkingProcessLabel(draft.ThinkingProcess)},
			managementSummaryItem{Title: managementCopy("management.watch.limit_label"), Body: draft.MaxActive},
			managementSummaryItem{Title: managementCopy("management.watch.idle_label"), Body: draft.IdleSeconds}),
			managementHintElement(managementCopy("management.watch.only_this_group"))},
		Buttons: []managementButton{groupWatchSettingsButton(draft.ChatID, draft.ChatName), {Text: managementCopy("management.group.watch.312"), Type: "default", Action: managementActionGroupWatchOpen}},
	})
}

func renderGroupWatchEditorPages(state managementState, draft managementGroupWatchDraft, rows []cardElement) string {
	render := func(rows []cardElement, page, pages int) string {
		submit := func(action string, label any, targetPage int) cardElement {
			button := managementFormSubmitButton(action, label)
			v := managementGroupWatchDraftValues(draft)
			v["group_watch_page"] = strconv.Itoa(targetPage)
			button.Value, _ = json.Marshal(managementButtonValue(managementButton{Action: action, Values: v}))
			return button
		}
		buttons := []cardElement{submit(managementActionGroupWatchSave, managementCopy("management.save"), page)}
		refresh := submit(managementActionGroupWatchEdit, managementCopy("management.refresh.model.options"), page)
		refresh.Type = "default"
		buttons = append(buttons, refresh)
		if page > 0 {
			buttons = append(buttons, submit(managementActionGroupWatchEdit, managementCopy("management.previous.page"), page-1))
		}
		if page+1 < pages {
			buttons = append(buttons, submit(managementActionGroupWatchEdit, managementCopy("management.next.page"), page+1))
		}
		form := append(append([]cardElement(nil), rows...), managementFormButtonRowElement(buttons...))
		elements := []cardElement{managementHintElement(managementCopy("management.watch.only_this_group"))}
		if !state.GroupWatch.Enabled {
			elements = append(elements, managementHintElement(managementCopy("management.watch.global_paused")))
		}
		if pages > 1 {
			elements = append(elements, managementHintElement(managementCopy("management.page.d.d.of.settings.changing.pages.preserves.entered", page+1, pages)))
		}
		elements = append(elements, cardElement{Tag: "form", Name: "group_watch_form", Direction: "vertical", VerticalSpacing: "8px", Elements: form})
		return marshalNativeManagementCard(managementCardView{
			Title: managementCopy("management.edit.watch.task.settings"), Subtitle: managementConcat(managementValueOrPlaceholder(draft.ChatName, managementCopy("management.unnamed.group")), " · ", draft.ChatID),
			Summary: managementCopy("management.edit.group.watch.task.settings"), Theme: "blue", Icon: managementIcon("edit_outlined", "blue"), Elements: elements,
			Buttons: []managementButton{{Text: managementCopy("management.cancel"), Type: "default", Action: managementActionGroupWatchOpen}},
		})
	}
	fits := func(rows []cardElement, pages int) bool {
		card := render(rows, 1, pages)
		// Also reserve the surrounding /amux navigation elements.
		return managementCardTaggedNodeCount(card) <= managementGroupSettingsLimitElements-12 && len(card) <= managementGroupSettingsMaxBytes
	}
	if fits(rows, 1) {
		return render(rows, 0, 1)
	}
	pages := [][]cardElement{}
	current := []cardElement{}
	for _, row := range rows {
		if !fits([]cardElement{row}, 3) {
			for count := managementRowOptionCount(row) - 1; count > 0; count-- {
				candidate := groupSettingsRowWithOptions(row, count)
				if fits([]cardElement{candidate}, 3) {
					row = candidate
					break
				}
			}
		}
		candidate := append(append([]cardElement(nil), current...), row)
		if len(current) > 0 && !fits(candidate, 3) {
			pages = append(pages, current)
			current = nil
		}
		current = append(current, row)
	}
	pages = append(pages, current)
	page := min(max(draft.Page, 0), len(pages)-1)
	return render(pages[page], page, len(pages))
}
