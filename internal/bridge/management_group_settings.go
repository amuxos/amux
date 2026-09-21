package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const (
	managementActionGroupSettingsOpen    = "bot.group_settings.open"
	managementActionGroupSettingsEdit    = "bot.group_settings.edit"
	managementActionGroupSettingsSave    = "bot.group_settings.save"
	managementActionGroupSettingsWatch   = "bot.group_settings.watch"
	managementActionGroupConversationNew = "bot.group_settings.new_conversation"
	managementGroupInheritProfile        = "__bot_profile__"
	managementGroupNativeRuntime         = "__profile_runtime__"
	managementGroupNativeModel           = "__native_model__"
)

// Scope is authenticated separately from editable fields. A private card may
// only operate on the Bot, group and version for which it was issued.
func (b *Bridge) groupSettingsScopeToken(bridgeID, chatID, chatName string, version int64) string {
	if b == nil || b.secrets == nil || b.secrets.AppSecret == "" {
		return ""
	}
	payload, _ := json.Marshal([]string{"group-settings-v1", bridgeID, chatID, chatName, strconv.FormatInt(version, 10), b.secrets.OpenId})
	mac := hmac.New(sha256.New, []byte(b.secrets.AppSecret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (b *Bridge) groupSettingsCardScope(settings *GroupSettings, chatName string) map[string]string {
	chatName = strings.TrimSpace(chatName)
	return map[string]string{
		"bridge_id": settings.BridgeID, "chat_id": settings.ChatID, "chat_name": chatName,
		"expected_version": strconv.FormatInt(settings.Version, 10),
		"group_scope":      b.groupSettingsScopeToken(settings.BridgeID, settings.ChatID, chatName, settings.Version),
	}
}

func (b *Bridge) groupSettingsFromCardScope(action *callback.CallBackAction) (*GroupSettings, string, error) {
	bridgeID, chatID := managementCallbackString(action, "bridge_id"), managementCallbackString(action, "chat_id")
	chatName := managementCallbackString(action, "chat_name")
	version, err := strconv.ParseInt(managementCallbackString(action, "expected_version"), 10, 64)
	if err != nil || version < 0 || bridgeID != b.currentBridgeId() || chatID == "" {
		return nil, "", fmt.Errorf("群设置卡片范围无效，请在目标群重新发送 /amux")
	}
	want := b.groupSettingsScopeToken(bridgeID, chatID, chatName, version)
	got := managementCallbackString(action, "group_scope")
	if want == "" || !hmac.Equal([]byte(want), []byte(got)) {
		return nil, "", fmt.Errorf("群设置卡片范围无效，请在目标群重新发送 /amux")
	}
	// Even an authentic old card must not modify a chat now known to be private.
	ws, err := b.db.ReadWorkspace(bridgeID, chatID)
	if err != nil {
		return nil, "", err
	}
	if ws.Kind == WorkspaceKindPrivate {
		return nil, "", fmt.Errorf("私聊不支持群独立设置")
	}
	settings, err := b.groupSettingsDraft(chatID)
	if err != nil {
		return nil, "", err
	}
	if settings.Version != version {
		return nil, "", errGroupSettingsChanged
	}
	return settings, chatName, nil
}

// This deliberately avoids managementState: merely opening a group card must
// not seed a Bot state, workspace, group policy or Session.
func (b *Bridge) groupSettingsManagementState(settings *GroupSettings) (managementState, error) {
	bot, err := b.currentBotProfileStateReadOnly()
	if err != nil {
		return managementState{}, err
	}
	state := managementState{
		ProfileName: b.ProfileName(), DefaultAgent: effectiveBotDefaultAgentRef(bot.DefaultAgent),
		Mode: bot.InteractionMode, GroupWatch: b.groupWatchSnapshot(),
	}
	state.Profiles = managementSelectableProfiles(managementProfiles(), state.DefaultAgent, settings.DefaultAgent)
	state.Runtimes, err = agentprofile.RuntimeCatalog()
	return state, err
}

func (b *Bridge) currentGroupSettingsCard(settings *GroupSettings, chatName string) (string, error) {
	state, err := b.groupSettingsManagementState(settings)
	if err != nil {
		return "", err
	}
	root, err := b.db.GetGroupSessionRoot(settings.BridgeID, settings.ChatID)
	if err != nil {
		return "", err
	}
	var current *LarkChat
	if root != "" {
		current, err = b.db.FindByRootMsg(settings.BridgeID, root)
		if err != nil {
			return "", err
		}
		if current != nil && current.ChatID != settings.ChatID {
			return "", fmt.Errorf("当前连续会话不属于本群")
		}
	}
	return renderCurrentGroupSettingsCard(state, settings, root, current, b.groupSettingsCardScope(settings, chatName)), nil
}

func (b *Bridge) sendCurrentGroupSettingsCard(ctx context.Context, openID, chatID, chatName string) error {
	if !b.isBotOwner(openID) {
		return fmt.Errorf("只有负责人可以管理群设置")
	}
	settings, err := b.groupSettingsDraft(chatID)
	if err != nil {
		return err
	}
	workspace, err := b.db.ReadWorkspace(settings.BridgeID, settings.ChatID)
	if err != nil {
		return err
	}
	if workspace.Kind == WorkspaceKindPrivate {
		return fmt.Errorf("私聊不支持群独立设置")
	}
	if b.groupSettingsScopeToken(settings.BridgeID, settings.ChatID, strings.TrimSpace(chatName), settings.Version) == "" {
		return fmt.Errorf("无法验证群设置卡片，请检查智能体凭据")
	}
	card, err := b.currentGroupSettingsCard(settings, chatName)
	if err != nil {
		return err
	}
	return b.sendManagementCardToDM(ctx, openID, card)
}

func (b *Bridge) handleGroupSettingsCardAction(ctx context.Context, event *callback.CardActionTriggerEvent, action string) (*callback.CardActionTriggerResponse, bool, error) {
	switch action {
	case managementActionGroupSettingsOpen, managementActionGroupSettingsEdit, managementActionGroupSettingsSave, managementActionGroupSettingsWatch, managementActionGroupConversationNew:
	default:
		return nil, false, nil
	}
	openID := ""
	if event.Event.Operator != nil {
		openID = event.Event.Operator.OpenID
	}
	if !b.isBotOwner(openID) {
		return cardToast("error", "只有负责人可以管理群设置"), true, nil
	}
	if err := requireGroupSettingsExperiment(); err != nil {
		return groupSettingsErrorToast(err), true, nil
	}
	settings, chatName, err := b.groupSettingsFromCardScope(event.Event.Action)
	if err != nil {
		return groupSettingsErrorToast(err), true, nil
	}
	messageID := ""
	if event.Event.Context != nil {
		messageID = event.Event.Context.OpenMessageID
	}
	update := func(card string) { b.updateManagementCardFromCallback(openID, messageID, card) }
	if action == managementActionGroupSettingsEdit || action == managementActionGroupSettingsSave {
		draft, err := groupSettingsDraftFromCallback(settings, event.Event.Action)
		if err != nil {
			return groupSettingsErrorToast(err), true, nil
		}
		if action == managementActionGroupSettingsEdit {
			state, err := b.groupSettingsManagementState(settings)
			if err != nil {
				return groupSettingsErrorToast(err), true, nil
			}
			update(renderGroupSettingsEditCardAtPage(state, draft, b.groupSettingsCardScope(settings, chatName), groupSettingsPageFromAction(event.Event.Action)))
			return cardToast("success", "已打开本群设置，尚未保存"), true, nil
		}
		submitted, err := draft.settings(settings)
		if err == nil {
			settings, err = b.applyGroupSettings(ctx, submitted, settings.Version, openID)
		}
		if err != nil {
			return groupSettingsErrorToast(err), true, nil
		}
	}
	if action == managementActionGroupConversationNew {
		if err := b.newGroupConversation(settings.ChatID, settings.Version, openID); err != nil {
			return groupSettingsErrorToast(err), true, nil
		}
		settings, err = b.groupSettingsDraft(settings.ChatID)
		if err != nil {
			return groupSettingsErrorToast(err), true, nil
		}
	}
	if action == managementActionGroupSettingsWatch {
		operation := managementCallbackString(event.Event.Action, "operation")
		if operation != "add" && operation != "remove" {
			return cardToast("error", "请选择加入或移出本群值守"), true, nil
		}
		if _, err := b.updateGroupWatchForProfile(operation, settings.ChatID); err != nil {
			return groupSettingsErrorToast(err), true, nil
		}
	}
	card, err := b.currentGroupSettingsCard(settings, chatName)
	if err != nil {
		return groupSettingsErrorToast(err), true, nil
	}
	update(card)
	message := "已打开当前群设置"
	if action == managementActionGroupSettingsSave {
		message = "本群设置已保存，仅新会话采用新的执行参数"
	} else if action == managementActionGroupConversationNew {
		message = "已新开连续会话，下一条群消息从空白上下文开始"
	} else if action == managementActionGroupSettingsWatch {
		message = groupWatchOperationSuccessText(managementCallbackString(event.Event.Action, "operation"))
	}
	return cardToast("success", message), true, nil
}

func groupSettingsErrorToast(err error) *callback.CardActionTriggerResponse {
	if errors.Is(err, errGroupSettingsChanged) {
		return cardToast("warning", "群设置已变化，请在目标群重新发送 /amux 后再修改")
	}
	return cardToast("error", truncateCardText(err.Error(), 240))
}

type managementGroupSettingsDraft map[string]string

func groupSettingsFormDraft(settings *GroupSettings) managementGroupSettingsDraft {
	args := settings.DefaultArgs
	if args == nil {
		args = []string{}
	}
	argsJSON, _ := json.Marshal(args)
	interaction := settings.InteractionMode
	if settings.IsolationRequired {
		interaction = interactionModeThreadIsolated
	}
	return managementGroupSettingsDraft{
		"group_interaction":  interaction,
		"group_agent":        valueOrPlaceholder(settings.DefaultAgent, managementGroupInheritProfile),
		"group_runtime":      valueOrPlaceholder(settings.DefaultRuntimeRef, managementGroupNativeRuntime),
		"group_model":        valueOrPlaceholder(settings.DefaultModelRef, managementProfileDefaultModelOption),
		"group_native_model": "",
		"group_reasoning":    valueOrPlaceholder(settings.DefaultReasoningEffort, managementRuntimeDefaultReasoningOption),
		"group_thinking":     settings.ThinkingProcess, "group_access": settings.Mode,
		"group_target": settings.DefaultTarget, "group_cwd": settings.DefaultCwd,
		"group_args": string(argsJSON), "group_idle_seconds": strconv.Itoa(settings.IdleTimeoutSeconds),
		"group_context_limit": strconv.Itoa(settings.ThreadContextLimit),
		"group_max_total":     strconv.Itoa(settings.MaxSessionsTotal), "group_max_per_user": strconv.Itoa(settings.MaxSessionsPerUser),
	}
}

func groupSettingsDraftFromCallback(settings *GroupSettings, action *callback.CallBackAction) (managementGroupSettingsDraft, error) {
	draft := groupSettingsFormDraft(settings)
	for key := range draft {
		// Form values take precedence over the complete rendered draft carried by
		// the button, including intentional clearing of optional text inputs.
		value, exists := action.FormValue[key]
		if !exists {
			value, exists = action.Value[key]
		}
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("群设置字段 %s 格式无效", key)
		}
		draft[key] = strings.TrimSpace(text)
	}
	return draft, nil
}

func (draft managementGroupSettingsDraft) settings(base *GroupSettings) (*GroupSettings, error) {
	settings := *base
	interaction := draft["group_interaction"]
	if !managementValidInteraction(interaction) {
		return nil, fmt.Errorf("请选择有效的群内处理方式")
	}
	settings.InteractionMode, settings.IsolationRequired = managementDecomposeInteraction(interaction)
	settings.DefaultAgent = groupSettingsOptional(draft["group_agent"], managementGroupInheritProfile)
	settings.DefaultRuntimeRef = groupSettingsOptional(draft["group_runtime"], managementGroupNativeRuntime)
	settings.DefaultModelRef = managementModelRefFromForm(draft["group_model"])
	if settings.DefaultModelRef == managementGroupNativeModel {
		settings.DefaultModelRef = draft["group_native_model"]
		if settings.DefaultModelRef == "" {
			return nil, fmt.Errorf("请填写原生模型名称，或选择默认模型")
		}
	}
	settings.DefaultReasoningEffort = managementReasoningEffortFromForm(draft["group_reasoning"])
	settings.ThinkingProcess, settings.Mode = draft["group_thinking"], draft["group_access"]
	settings.DefaultTarget, settings.DefaultCwd = draft["group_target"], draft["group_cwd"]
	rawArgs := draft["group_args"]
	if rawArgs == "" {
		rawArgs = "[]"
	}
	var args []any
	if !strings.HasPrefix(rawArgs, "[") || json.Unmarshal([]byte(rawArgs), &args) != nil {
		return nil, fmt.Errorf("附加参数须为 JSON 字符串数组，例如 [\"--flag\", \"value with spaces\"]")
	}
	settings.DefaultArgs = make([]string, 0, len(args))
	for _, arg := range args {
		value, ok := arg.(string)
		if !ok {
			return nil, fmt.Errorf("附加参数须为 JSON 字符串数组")
		}
		settings.DefaultArgs = append(settings.DefaultArgs, value)
	}
	for _, field := range []struct {
		key, label string
		target     *int
	}{
		{"group_idle_seconds", "空闲回收秒数", &settings.IdleTimeoutSeconds},
		{"group_context_limit", "上下文条数", &settings.ThreadContextLimit},
		{"group_max_total", "群会话总上限", &settings.MaxSessionsTotal},
		{"group_max_per_user", "每用户会话上限", &settings.MaxSessionsPerUser},
	} {
		value, err := strconv.Atoi(draft[field.key])
		if err != nil || value < 0 {
			return nil, fmt.Errorf("%s须为非负整数", field.label)
		}
		*field.target = value
	}
	return &settings, nil
}

func groupSettingsOptional(value, sentinel string) string {
	if value == sentinel {
		return ""
	}
	return value
}

func groupSettingsScopedButton(action, label, buttonType string, scope map[string]string) managementButton {
	return managementButton{Action: action, Text: label, Type: buttonType, Values: scope}
}

func renderCurrentGroupSettingsCard(state managementState, settings *GroupSettings, root string, current *LarkChat, scope map[string]string) string {
	agentRef, source := settings.DefaultAgent, "本群指定"
	if agentRef == "" {
		agentRef, source = state.DefaultAgent, "跟随助手默认专家"
	}
	policy := "本群独立设置"
	if settings.Version == 0 {
		policy = "沿用助手设置（尚未保存本群设置）"
		source = "沿用助手设置"
	}
	interaction := settings.InteractionMode
	if settings.IsolationRequired {
		interaction = interactionModeThreadIsolated
	}
	interactionText := managementInteractionText(interaction)
	if interaction == config.FeishuInteractionSingleton {
		interactionText = "本群继续同一对话"
		if settings.Version == 0 {
			interactionText = "助手连续会话（现有范围含其他群或私聊）"
		}
	}
	access := "只有负责人可发起任务"
	if settings.Mode == WorkspaceModeShared {
		access = "群成员可发起任务（仍需满足助手与执行权限）"
	}
	runtime := managementRuntimeLabel(state, settings.DefaultRuntimeRef)
	if settings.DefaultRuntimeRef == "" {
		runtime = "所选专家的原生运行引擎"
	}
	args, _ := json.Marshal(settings.DefaultArgs)
	if settings.DefaultArgs == nil {
		args = []byte("[]")
	}
	elements := []cardElement{
		managementCompactSummaryElement(
			managementSummaryItem{Title: "群名称", Body: valueOrPlaceholder(scope["chat_name"], "未命名群")},
			managementSummaryItem{Title: "群标识", Body: settings.ChatID},
			managementSummaryItem{Title: "当前助手", Body: state.ProfileName},
			managementSummaryItem{Title: "设置来源", Body: policy}),
		managementKeyValueListElement("group_settings_future_defaults",
			managementSummaryItem{Title: "新会话默认专家", Body: managementProfileNameByRef(state, agentRef) + " · " + agentRef + "（" + source + "）"},
			managementSummaryItem{Title: "处理方式", Body: interactionText},
			managementSummaryItem{Title: "使用权限", Body: access},
			managementSummaryItem{Title: "运行引擎", Body: runtime},
			managementSummaryItem{Title: "模型", Body: managementRuntimeModelLabel(settings.DefaultModelRef)},
			managementSummaryItem{Title: "思考强度", Body: managementReasoningEffortLabel(settings.DefaultReasoningEffort)},
			managementSummaryItem{Title: "处理过程展示", Body: managementThinkingProcessLabel(settings.ThinkingProcess)},
			managementSummaryItem{Title: "目标机器", Body: valueOrPlaceholder(settings.DefaultTarget, "自动选择")},
			managementSummaryItem{Title: "工作目录", Body: valueOrPlaceholder(settings.DefaultCwd, "运行环境默认目录")},
			managementSummaryItem{Title: "附加参数", Body: string(args)},
			managementSummaryItem{Title: "空闲回收秒数", Body: strconv.Itoa(settings.IdleTimeoutSeconds)},
			managementSummaryItem{Title: "上下文条数", Body: strconv.Itoa(settings.ThreadContextLimit)},
			managementSummaryItem{Title: "群会话总上限 / 每用户上限", Body: fmt.Sprintf("%d / %d", settings.MaxSessionsTotal, settings.MaxSessionsPerUser)}),
		managementHintElement("保存后的执行参数只用于新会话。已有话题与当前连续会话保留创建时的专家、模型、执行身份和环境；开启用户隔离不会迁移已有话题。"),
	}
	buttons := []managementButton{groupSettingsScopedButton(managementActionGroupSettingsEdit, "修改本群设置", "primary", scope)}
	if settings.InteractionMode == config.FeishuInteractionSingleton && settings.Version > 0 {
		status := "尚未创建；下一条有效任务开始本群连续会话，直接在群聊中回复"
		profile, model := "—", "—"
		if root != "" {
			status = "直接在本群回复"
			if current == nil {
				status += "（会话绑定不可用，请新开连续会话）"
			} else {
				profile = valueOrPlaceholder(current.AgentProfile, current.AgentType)
				model = valueOrPlaceholder(current.EffectiveModel, "未记录")
				status += " · " + current.SessionName
			}
		}
		elements = append(elements, managementKeyValueListElement("group_settings_current_session",
			managementSummaryItem{Title: "当前连续会话", Body: status},
			managementSummaryItem{Title: "当前会话专家", Body: profile},
			managementSummaryItem{Title: "当前会话模型", Body: model}))
		reset := groupSettingsScopedButton(managementActionGroupConversationNew, "新开连续会话", "default", scope)
		reset.Confirm = managementConfirm("从空白上下文开始新的连续会话？", "当前会话不再接受新输入；历史保留，已接受的任务继续完成。下一条群消息使用本群的新会话默认设置，结果直接在本群回复。")
		buttons = append(buttons, reset)
	}
	joinedWatch := groupWatchConfigHasChat(state.GroupWatch, settings.ChatID)
	watchText := "群值守：" + managementGroupWatchCommandStatus(state, settings.ChatID)
	if joinedWatch {
		if state.GroupWatch.AgentProfileRef != "" {
			watchText += "；值守使用独立专家：" + state.GroupWatch.AgentProfileRef
		}
	}
	elements = append(elements, managementHintElement(watchText))
	if joinedWatch || settings.InteractionMode == config.FeishuInteractionThread {
		values := make(map[string]string, len(scope)+1)
		for key, value := range scope {
			values[key] = value
		}
		values["operation"] = "add"
		watch := groupSettingsScopedButton(managementActionGroupSettingsWatch, "将本群加入值守", "default", values)
		watch.Confirm = managementConfirm("确认加入群值守？", "本群的新话题会自动交给助手处理。如果已有群值守被暂停，加入本群会一并恢复这些已配置群的值守。")
		if joinedWatch {
			watch.Text, values["operation"], watch.Confirm = "将本群移出值守", "remove", nil
		}
		buttons = append(buttons, watch)
	}
	return marshalNativeManagementCard(managementCardView{Title: "当前群设置", Subtitle: "仅管理当前助手在本群的设置", Summary: "amux 当前群设置", Theme: "blue", Elements: elements, Buttons: buttons})
}

// Feishu rejects a card above managementGroupSettingsLimitElements tagged nodes:
// every element carrying a tag counts, including the plain_text inside each
// select option. The editor is one form whose size tracks the profile and model
// catalogs, so it is budgeted and paginated instead of assumed to fit.
const (
	// managementGroupSettingsLimitElements is Feishu's per-card element ceiling.
	// Every element carrying a tag counts, including the plain_text inside each
	// select option. Verified against the live API: 200 is accepted, 201 is
	// rejected with ErrCode 11310 / "element exceeds the limit".
	managementGroupSettingsLimitElements = 200
	// managementGroupSettingsElementReserve keeps the card clear of the ceiling.
	// managementCardTaggedNodeCount matches Feishu's accounting exactly on real
	// editor cards, so this only guards against a future element type the
	// counter does not model.
	managementGroupSettingsElementReserve = 1
	// managementGroupSettingsMaxBytes is Feishu's separate size ceiling, which
	// rejects with "card over max size" (ErrCode 200860) independently of the
	// element count. Measured at ~150KB serialized, so this stays well clear.
	managementGroupSettingsMaxBytes = 128 * 1024
	// groupSettingsPageKey carries the editor page through the buttons that
	// re-render it, so changing page or refreshing model options keeps both the
	// page and every value already entered in the form.
	groupSettingsPageKey = "group_page"
)

func groupSettingsPageFromAction(action *callback.CallBackAction) int {
	page, err := strconv.Atoi(managementCallbackString(action, groupSettingsPageKey))
	if err != nil || page < 0 {
		return 0
	}
	return page
}

// managementGroupSettingsEditFields builds the editor rows in display order.
// Pagination runs afterwards so a large catalog cannot push the card past the
// element limit.
func managementGroupSettingsEditFields(state managementState, draft managementGroupSettingsDraft) []cardElement {
	selectedAgent := groupSettingsOptional(draft["group_agent"], managementGroupInheritProfile)
	if selectedAgent == "" {
		selectedAgent = state.DefaultAgent
	}
	runtimeRef := groupSettingsOptional(draft["group_runtime"], managementGroupNativeRuntime)
	modelRef := managementModelRefFromForm(draft["group_model"])
	if modelRef == managementGroupNativeModel {
		modelRef = draft["group_native_model"]
	}
	profileOptions := append([]cardOption{{Text: cardTitle{Tag: "plain_text", Content: "跟随助手默认专家 · " + state.DefaultAgent}, Value: managementGroupInheritProfile}}, managementProfileOptions(state, true)...)
	runtimeOptions := append([]cardOption{{Text: cardTitle{Tag: "plain_text", Content: "使用所选专家的原生运行引擎"}, Value: managementGroupNativeRuntime}}, managementRuntimeOptions(state)...)
	modelOptions := managementModelOptions(state, selectedAgent)
	effectiveRuntime := runtimeRef
	if runtimeRef != "" {
		modelOptions = managementRuntimeModelOptions(state, runtimeRef)
	} else if profile, ok := managementProfileByRef(state, selectedAgent); ok {
		effectiveRuntime = profile.AgentType
	}
	modelOptions = append(modelOptions, cardOption{Text: cardTitle{Tag: "plain_text", Content: "输入原生模型名称"}, Value: managementGroupNativeModel})
	form := []cardElement{}
	selectField := func(label, key string, options []cardOption) {
		initial := draft[key]
		found := false
		for _, option := range options {
			if option.Value == initial {
				found = true
				break
			}
		}
		if initial != "" && !found {
			options = append(options, cardOption{Text: cardTitle{Tag: "plain_text", Content: "当前值（保存时检查） · " + initial}, Value: initial})
		}
		form = append(form, managementSettingsFieldRow(label, managementSelectStaticElement(key, "请选择", initial, options)))
	}
	inputField := func(label, key, placeholder string) {
		form = append(form, managementSettingsFieldRow(label, cardElement{Tag: "input", Name: key, DefaultValue: draft[key], Width: "fill", Placeholder: &cardTitle{Tag: "plain_text", Content: placeholder}}))
	}
	selectField("群内处理方式", "group_interaction", []cardOption{
		{Text: cardTitle{Tag: "plain_text", Content: "本群继续同一对话"}, Value: config.FeishuInteractionSingleton},
		{Text: cardTitle{Tag: "plain_text", Content: "每个话题独立"}, Value: config.FeishuInteractionThread},
		{Text: cardTitle{Tag: "plain_text", Content: "每个话题独立（用户隔离）"}, Value: interactionModeThreadIsolated},
	})
	selectField("新会话默认专家", "group_agent", profileOptions)
	selectField("运行引擎", "group_runtime", runtimeOptions)
	selectField("模型", "group_model", modelOptions)
	inputField("原生模型名称（上方选择“输入原生模型名称”时使用）", "group_native_model", "例如引擎支持的模型名")
	reasoningOptions := managementRuntimeReasoningOptions(state, effectiveRuntime, modelRef)
	if runtimeRef == "" {
		reasoningOptions = managementProfileReasoningOptions(state, selectedAgent, modelRef)
	}
	selectField("思考强度", "group_reasoning", reasoningOptions)
	selectField("处理过程展示", "group_thinking", []cardOption{
		{Text: cardTitle{Tag: "plain_text", Content: "折叠展示"}, Value: config.FeishuThinkingProcessCollapsed},
		{Text: cardTitle{Tag: "plain_text", Content: "展开展示"}, Value: config.FeishuThinkingProcessExpanded},
		{Text: cardTitle{Tag: "plain_text", Content: "不展示"}, Value: config.FeishuThinkingProcessHidden},
	})
	selectField("谁可以发起任务", "group_access", []cardOption{
		{Text: cardTitle{Tag: "plain_text", Content: "只有负责人"}, Value: WorkspaceModeOwnerOnly},
		{Text: cardTitle{Tag: "plain_text", Content: "群成员（仍需满足助手与执行权限）"}, Value: WorkspaceModeShared},
	})
	inputField("目标机器", "group_target", "留空自动选择")
	inputField("工作目录", "group_cwd", "目标机器上的绝对路径；留空使用默认目录")
	inputField("附加参数（JSON 字符串数组）", "group_args", `["--flag", "value with spaces"]`)
	inputField("空闲回收时间（秒，0 关闭，或至少 60）", "group_idle_seconds", "非负整数")
	inputField("上下文条数", "group_context_limit", "非负整数")
	inputField("群会话总上限（0 不限）", "group_max_total", "非负整数")
	inputField("每用户会话上限（0 不限）", "group_max_per_user", "非负整数")
	return form
}

// renderGroupSettingsEditCardAtPage renders one editor page. A page past the
// end falls back to the last page so a shrinking catalog cannot strand the user
// on an empty card.
func renderGroupSettingsEditCardAtPage(state managementState, draft managementGroupSettingsDraft, scope map[string]string, page int) string {
	pages := paginateManagementGroupSettings(managementGroupSettingsEditFields(state, draft), draft, scope)
	if page >= len(pages) {
		page = len(pages) - 1
	}
	if page < 0 {
		page = 0
	}
	return renderGroupSettingsEditCardPage(draft, scope, pages[page], page, len(pages))
}

// groupSettingsEditNodeBudget is the node allowance for the finished card. The
// Feishu limit is exact, so the reserve only absorbs drift between the counted
// nodes and what Feishu charges.
func groupSettingsEditNodeBudget() int {
	return managementGroupSettingsLimitElements - managementGroupSettingsElementReserve
}

// paginateManagementGroupSettings groups rows into pages that each render
// within the element budget. Each candidate page is measured by rendering the
// real card, so element costs stay correct as rows change.
//
// A card that fits on one page is never split. The single-page attempt is
// measured without the page indicator, because that is the card the user would
// actually get; only a card that genuinely overflows pays for the indicator.
func paginateManagementGroupSettings(rows []cardElement, draft managementGroupSettingsDraft, scope map[string]string) [][]cardElement {
	if len(rows) == 0 {
		return [][]cardElement{{}}
	}
	// A row is atomic: a select carries its whole option list and cannot be
	// split across pages. Shrink any single row that would not fit alone, so a
	// pathological catalog degrades to a shorter option list instead of an
	// over-limit card that Feishu rejects outright.
	rows = groupSettingsFitRows(rows, draft, scope)
	if groupSettingsPageFits(rows, draft, scope, 1) {
		return [][]cardElement{rows}
	}
	return groupSettingsPaginateRows(rows, draft, scope, 2)
}

// groupSettingsFitRows truncates the option list of any row that cannot fit a
// page by itself. The option currently selected is always preserved so the
// rendered card still shows the effective value.
func groupSettingsFitRows(rows []cardElement, draft managementGroupSettingsDraft, scope map[string]string) []cardElement {
	out := make([]cardElement, 0, len(rows))
	for _, row := range rows {
		if groupSettingsPageFits([]cardElement{row}, draft, scope, 2) {
			out = append(out, row)
			continue
		}
		out = append(out, groupSettingsShrinkRow(row, draft, scope))
	}
	return out
}

// groupSettingsShrinkRow binary-searches the largest option count that lets the
// row fit a page on its own, keeping the selected option in the list.
func groupSettingsShrinkRow(row cardElement, draft managementGroupSettingsDraft, scope map[string]string) cardElement {
	total := managementRowOptionCount(row)
	if total == 0 {
		return row
	}
	best := row
	lo, hi := 1, total
	for lo <= hi {
		mid := (lo + hi) / 2
		candidate := groupSettingsRowWithOptions(row, mid)
		if groupSettingsPageFits([]cardElement{candidate}, draft, scope, 2) {
			best = candidate
			lo = mid + 1
			continue
		}
		hi = mid - 1
	}
	return best
}

// managementRowOptionCount returns the option count of the row's select, or 0
// when the row holds no select.
func managementRowOptionCount(row cardElement) int {
	if option, ok := managementRowSelect(&row); ok {
		return len(option.Options)
	}
	return 0
}

// groupSettingsRowWithOptions returns a copy of the row whose select carries at
// most max options. The initial option is kept first when truncation would
// otherwise drop it, so the card never loses the effective value, and a note is
// added under the control so the shortened list is visible rather than silent.
func groupSettingsRowWithOptions(row cardElement, max int) cardElement {
	clone := row
	clone.Columns = append([]cardColumn(nil), row.Columns...)
	for ci := range clone.Columns {
		elements := append([]cardElement(nil), clone.Columns[ci].Elements...)
		for ei := range elements {
			element := &elements[ei]
			if element.Tag != "select_static" || len(element.Options) <= max {
				continue
			}
			total := len(element.Options)
			// Keep the selected option first so truncation cannot hide the
			// value the card is actually showing.
			kept := make([]cardOption, 0, max)
			for _, option := range element.Options {
				if option.Value == element.InitialOption {
					kept = append(kept, option)
					break
				}
			}
			for _, option := range element.Options {
				if len(kept) >= max {
					break
				}
				if option.Value == element.InitialOption {
					continue
				}
				kept = append(kept, option)
			}
			element.Options = kept
			// Append the note after the loop over elements so the walk cannot
			// revisit it as a select.
			elements = append(elements,
				managementHintElement(fmt.Sprintf("选项过多，仅显示 %d/%d 项；当前值已保留。", len(kept), total)))
		}
		clone.Columns[ci].Elements = elements
	}
	return clone
}

// managementRowSelect returns the select element of a two-element field row.
func managementRowSelect(row *cardElement) (*cardElement, bool) {
	for ci := range row.Columns {
		for ei := range row.Columns[ci].Elements {
			if row.Columns[ci].Elements[ei].Tag == "select_static" {
				return &row.Columns[ci].Elements[ei], true
			}
		}
	}
	return nil, false
}

// groupSettingsPaginateRows splits rows using the card shape of a multi-page
// card when pagesHint is above one, so the indicator's own nodes are budgeted.
func groupSettingsPaginateRows(rows []cardElement, draft managementGroupSettingsDraft, scope map[string]string, pagesHint int) [][]cardElement {
	pages := make([][]cardElement, 0, 1)
	current := make([]cardElement, 0, len(rows))
	for _, row := range rows {
		candidate := append(append([]cardElement(nil), current...), row)
		if len(current) > 0 && !groupSettingsPageFits(candidate, draft, scope, pagesHint) {
			pages = append(pages, current)
			current = []cardElement{row}
			continue
		}
		current = candidate
	}
	pages = append(pages, current)
	return pages
}

// groupSettingsPageFits reports whether a page of rows renders within budget.
func groupSettingsPageFits(rows []cardElement, draft managementGroupSettingsDraft, scope map[string]string, pagesHint int) bool {
	card := renderGroupSettingsEditCardPage(draft, scope, rows, 0, pagesHint)
	if len(card) > managementGroupSettingsMaxBytes {
		return false
	}
	return managementCardTaggedNodeCount(card) <= groupSettingsEditNodeBudget()
}

// renderGroupSettingsEditCardPage assembles one editor page. Every button
// carries the full draft plus the page it should re-render, so switching pages
// or refreshing model options never discards values already entered.
func renderGroupSettingsEditCardPage(draft managementGroupSettingsDraft, scope map[string]string, rows []cardElement, page, pages int) string {
	values := make(map[string]string, len(scope)+len(draft)+1)
	for key, value := range scope {
		values[key] = value
	}
	for key, value := range draft {
		values[key] = value
	}
	values[groupSettingsPageKey] = strconv.Itoa(page)
	form := make([]cardElement, 0, len(rows)+1)
	form = append(form, rows...)
	submit := func(action, label string) cardElement {
		button := managementFormSubmitButton(action, label)
		button.Value, _ = json.Marshal(managementButtonValue(groupSettingsScopedButton(action, label, "primary", values)))
		return button
	}
	save := submit(managementActionGroupSettingsSave, "保存本群设置")
	save.Confirm = managementConfirm("保存本群设置？", "执行参数只影响新会话，已有话题保留原设置。切换连续会话模式会停用原连续会话入口，已接受任务继续完成；新连续会话从空白上下文开始。首次保存后本群与助手全局连续会话分开。")
	refresh := submit(managementActionGroupSettingsEdit, "更新模型选项")
	refresh.Type = "default"
	if pages > 1 {
		form = append(form, managementFormButtonRowElement(save, refresh, groupSettingsPageButton(scope, draft, page-1, "上一页", page > 0), groupSettingsPageButton(scope, draft, page+1, "下一页", page < pages-1)))
	} else {
		form = append(form, managementFormButtonRowElement(save, refresh))
	}
	elements := []cardElement{
		managementHintElement("新话题和新开的连续会话使用本群设置；已有会话不变。仅专家可选择跟随助手，其余字段保存后独立于助手默认值。"),
		managementHintElement("更换专家或运行引擎后可点击“更新模型选项”；所有已填值都会保留，不兼容的组合将在保存时明确报错。"),
	}
	if pages > 1 {
		elements = append(elements, managementHintElement(fmt.Sprintf("设置项较多，当前第 %d/%d 页；切换分页不会丢失已填内容。", page+1, pages)))
	}
	elements = append(elements, cardElement{Tag: "form", Name: "group_settings_form", Direction: "vertical", VerticalSpacing: "8px", Elements: form})
	return marshalNativeManagementCard(managementCardView{
		Title: "修改本群设置", Subtitle: valueOrPlaceholder(scope["chat_name"], "未命名群") + " · " + scope["chat_id"], Summary: "amux 本群设置编辑", Theme: "blue",
		Elements: elements,
		Buttons:  []managementButton{groupSettingsScopedButton(managementActionGroupSettingsOpen, "取消", "default", scope)},
	})
}

// groupSettingsPageButton renders a page switch. Disabled edges stay visible so
// the row keeps its shape across pages; they carry no action when disabled.
//
// Feishu requires every element name in a card to be unique, so the name is
// suffixed per direction rather than reusing the refresh button's action name.
func groupSettingsPageButton(scope map[string]string, draft managementGroupSettingsDraft, page int, label string, enabled bool) cardElement {
	button := managementFormSubmitButton(managementActionGroupSettingsEdit, label)
	button.Type = "default"
	button.Name = fmt.Sprintf("%s.page.%d", managementActionGroupSettingsEdit, page)
	if !enabled {
		button.Disabled = true
		return button
	}
	values := make(map[string]string, len(scope)+len(draft)+1)
	for key, value := range scope {
		values[key] = value
	}
	for key, value := range draft {
		values[key] = value
	}
	values[groupSettingsPageKey] = strconv.Itoa(page)
	button.Value, _ = json.Marshal(managementButtonValue(groupSettingsScopedButton(managementActionGroupSettingsEdit, label, "default", values)))
	return button
}
