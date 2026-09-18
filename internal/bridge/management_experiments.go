package bridge

import (
	"fmt"
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
		return saved, fmt.Errorf("请选择开启或关闭")
	}
	saved.Enabled = enabled == "true"
	if value, present := managementExperimentCallbackValue(action, "completion_seconds"); present {
		raw, ok := value.(string)
		if !ok {
			return saved, fmt.Errorf("通知阈值须为 0 到 604800 秒的整数")
		}
		if strings.TrimSpace(raw) == "" {
			return saved, nil
		}
		seconds, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || seconds < 0 || seconds > 604800 {
			return saved, fmt.Errorf("通知阈值须为 0 到 604800 秒的整数")
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
			return config.ExperimentsSettingsUpdate{}, fmt.Errorf("请选择开启或关闭群独立配置")
		}
		value := enabled == "true"
		update.GroupSettingsEnabled = &value
	}
	if raw, present := managementExperimentCallbackValue(action, "memory_enabled"); present {
		enabled, _ := raw.(string)
		enabled = strings.TrimSpace(enabled)
		if enabled != "true" && enabled != "false" {
			return config.ExperimentsSettingsUpdate{}, fmt.Errorf("请选择开启或关闭 Agent 记忆")
		}
		value := enabled == "true"
		update.MemoryEnabled = &value
	}
	return update, nil
}

func renderManagementExperimentsCard(saved, running config.ExperimentsSettings) string {
	completion := saved.CompletionNotification
	enabled := managementSelectStaticElement("completion_enabled", "选择开关", strconv.FormatBool(completion.Enabled), []cardOption{
		{Text: cardTitle{Tag: "plain_text", Content: "关闭"}, Value: "false"},
		{Text: cardTitle{Tag: "plain_text", Content: "开启"}, Value: "true"},
	})
	seconds := cardElement{Tag: "input", Name: "completion_seconds", Placeholder: &cardTitle{Tag: "plain_text", Content: fmt.Sprintf("当前 %d 秒；留空保持", completion.AfterSeconds)}}
	summary := fmt.Sprintf("任务耗时达到 %d 秒，结束时提醒你", completion.AfterSeconds)
	if completion.AfterSeconds == 0 {
		summary = "仅在等待补充或失败时提醒你"
	}
	if !completion.Enabled {
		summary = "开启后，长任务结束时提醒你"
	}
	pending := completion != running.CompletionNotification
	item := managementExperimentItemElement("completion_notification", "长会话完成通知", summary, enabled, pending, []cardElement{
		managementSettingsFieldRow("通知阈值（秒）", seconds),
		managementHintElement("通过私聊卡片提醒本轮发起人，点击返回原话题，不在话题内追加消息。成功、等待补充和失败均达到阈值才提醒，不判断已读。智能体自动触发不提醒。0 关闭全部新增提醒。关闭开关则停止全部新增提醒。单个助手的阈值配置优先。"),
	})
	groupEnabled := managementSelectStaticElement("group_settings_enabled", "选择开关", strconv.FormatBool(saved.GroupSettingsEnabled), []cardOption{
		{Text: cardTitle{Tag: "plain_text", Content: "关闭"}, Value: "false"},
		{Text: cardTitle{Tag: "plain_text", Content: "开启"}, Value: "true"},
	})
	groupItem := managementExperimentItemElement("group_settings", "群独立配置", "为每个群单独设置专家、Runtime、模型、思考强度和会话模式。默认关闭；关闭后保留已保存配置，暂停这些群的新任务。", groupEnabled, saved.GroupSettingsEnabled != running.GroupSettingsEnabled, nil)
	memoryEnabled := managementSelectStaticElement("memory_enabled", "选择开关", strconv.FormatBool(saved.MemoryEnabled), []cardOption{
		{Text: cardTitle{Tag: "plain_text", Content: "关闭"}, Value: "false"},
		{Text: cardTitle{Tag: "plain_text", Content: "开启"}, Value: "true"},
	})
	memoryItem := managementExperimentItemElement("agent_memory", "Agent 记忆", "跨对话记住偏好与经验，无需 Agent Profile", memoryEnabled, saved.MemoryEnabled != running.MemoryEnabled, []cardElement{
		managementHintElement("支持 Codex、Claude、Pi。整个 amux 系统跨 Runtime 共享；用户隔离模式下按用户分开。默认关闭，开启并重启后用于新对话。关闭阻止读写并保留记忆文件，已进入旧对话的内容不会清除。"),
	})
	elements := []cardElement{
		managementBackToOverviewElement(),
		{Tag: "form", Name: "experiments_form", Direction: "vertical", VerticalSpacing: "12px", Elements: []cardElement{
			item,
			groupItem,
			memoryItem,
			managementFormButtonRowElement(managementFormSubmitButton(managementActionExperimentsSaveRestart, "保存并重启")),
		}},
		managementTextElement("对当前实例的全部助手生效", "notation", "grey", nil),
	}
	return marshalNativeManagementCard(managementCardView{Title: "实验功能", Summary: "amux 实验功能", Theme: "blue", Elements: elements})
}

// Each experiment contributes a compact item to the shared page-level form.
// Field names and IDs belong to the feature, so adding an item needs no new layout.
func managementExperimentItemElement(id, title, summary string, control cardElement, pending bool, advanced []cardElement) cardElement {
	heading := cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "12px", Columns: []cardColumn{
		{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(title, "normal", "default", nil)}},
		{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{control}},
	}}
	heading.Columns[1].Elements[0].Width = "120px"
	children := []cardElement{heading, managementTextElement(summary, "notation", "grey", nil)}
	if pending {
		children = append(children, managementTextElement("待重启 · 已保存的设置尚未生效", "notation", "orange", nil))
	}
	if len(advanced) > 0 {
		children = append(children, cardElement{Tag: "collapsible_panel", ElementID: id + "_advanced", Expanded: false, Header: &panelHeader{Title: cardTitle{Tag: "plain_text", Content: "高级设置", TextSize: "notation", TextColor: "grey"}}, Elements: advanced})
	}
	return cardElement{Tag: "column_set", ElementID: id + "_item", FlexMode: "none", Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, Direction: "vertical", VerticalSpacing: "8px", Padding: "12px", BackgroundStyle: "grey-50", Elements: children}}}
}

func renderManagementExperimentsRestartConfirmationCard(state managementState) string {
	values := map[string]string{"system_instance": managementSystemInstanceID(state), "confirmation_nonce": newManagementConfirmationNonce()}
	return marshalNativeManagementCard(managementCardView{Title: "确认重启", Summary: "实验设置已保存，等待重启", Theme: "orange", Elements: []cardElement{
		managementHintElement("实验设置已保存。重启时助手会短暂离线，现有会话不会删除。取消则保留设置，稍后重启生效。"),
	}, Buttons: []managementButton{
		{Text: "确认重启", Type: "primary", Action: managementActionSystemRestartOK, Values: values},
		{Text: "取消", Type: "default", Action: managementActionExperimentsOpen},
	}})
}
