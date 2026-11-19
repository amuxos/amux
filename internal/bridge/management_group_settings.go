package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amuxos/amux/internal/modelservice"
	"strconv"
	"strings"
	"time"

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

// Authenticate the immutable scope without database or registry I/O so saves
// can acknowledge the Feishu callback before waiting for shared resources.
func (b *Bridge) authenticateGroupSettingsCardScope(action *callback.CallBackAction) (*GroupSettings, string, error) {
	bridgeID, chatID := managementCallbackString(action, "bridge_id"), managementCallbackString(action, "chat_id")
	chatName := managementCallbackString(action, "chat_name")
	version, err := strconv.ParseInt(managementCallbackString(action, "expected_version"), 10, 64)
	if err != nil || version < 0 || bridgeID != b.currentBridgeId() || chatID == "" {
		return nil, "", managementLocalizedError("management.invalid.group.settings.card.scope.send.amux.again.in")
	}
	want := b.groupSettingsScopeToken(bridgeID, chatID, chatName, version)
	got := managementCallbackString(action, "group_scope")
	if want == "" || !hmac.Equal([]byte(want), []byte(got)) {
		return nil, "", managementLocalizedError("management.invalid.group.settings.card.scope.send.amux.again.in")
	}
	return &GroupSettings{BridgeID: bridgeID, ChatID: chatID, Version: version}, chatName, nil
}

func (b *Bridge) groupSettingsFromCardScope(action *callback.CallBackAction) (*GroupSettings, string, error) {
	return b.groupSettingsFromCardScopeContext(context.Background(), action)
}

func (b *Bridge) groupSettingsFromCardScopeContext(ctx context.Context, action *callback.CallBackAction) (*GroupSettings, string, error) {
	scope, chatName, err := b.authenticateGroupSettingsCardScope(action)
	if err != nil {
		return nil, "", err
	}
	// Even an authentic old card must not modify a chat now known to be private.
	ws, err := b.db.ReadWorkspaceContext(ctx, scope.BridgeID, scope.ChatID)
	if err != nil {
		return nil, "", err
	}
	if ws.Kind == WorkspaceKindPrivate {
		return nil, "", managementLocalizedError("management.per.group.settings.are.unavailable.in.private.chat")
	}
	settings, err := b.groupSettingsDraftContext(ctx, scope.ChatID)
	if err != nil {
		return nil, "", err
	}
	if settings.Version != scope.Version {
		return nil, "", errGroupSettingsChanged
	}
	return settings, chatName, nil
}

// This deliberately avoids managementState: merely opening a group card must
// not seed a Bot state, workspace, group policy or Session.
func (b *Bridge) groupSettingsManagementState(settings *GroupSettings) (managementState, error) {
	return b.groupSettingsManagementStateContext(context.Background(), settings)
}

func (b *Bridge) groupSettingsManagementStateContext(ctx context.Context, settings *GroupSettings) (managementState, error) {
	bot, err := b.groupSettingsBotProfileStateContext(ctx)
	if err != nil {
		return managementState{}, err
	}
	cleanup, err := config.ReadDailyCleanupSettingsContext(ctx)
	if err != nil {
		return managementState{}, err
	}
	state := managementState{
		DailyCleanup: strconv.FormatBool(cleanup.Enabled),
		ProfileName:  b.ProfileName(), DefaultAgent: effectiveBotDefaultAgentRef(bot.DefaultAgent),
		Mode: settings.InteractionMode, GroupWatch: b.groupWatchSnapshot(), NativeModelsTargetRef: settings.DefaultTarget,
	}
	if settings.IsolationRequired {
		state.Mode = interactionModeThreadIsolated
	}
	state.Profiles = managementSelectableProfiles(managementProfiles(), state.DefaultAgent, settings.DefaultAgent)
	state.Runtimes, err = agentprofile.RuntimeCatalog()
	return state, err
}

func (b *Bridge) currentGroupSettingsCard(settings *GroupSettings, chatName string) (string, error) {
	return b.currentGroupSettingsCardContext(context.Background(), settings, chatName)
}

func (b *Bridge) currentGroupSettingsCardContext(ctx context.Context, settings *GroupSettings, chatName string) (string, error) {
	return b.currentGroupSettingsCardWithNoticeContext(ctx, settings, chatName, "")
}

func (b *Bridge) currentGroupSettingsCardWithNoticeContext(ctx context.Context, settings *GroupSettings, chatName string, notice any) (string, error) {
	state, err := b.groupSettingsManagementStateContext(ctx, settings)
	if err != nil {
		return "", err
	}
	root, err := b.db.GetGroupSessionRootContext(ctx, settings.BridgeID, settings.ChatID)
	if err != nil {
		return "", err
	}
	var current *LarkChat
	if root != "" {
		current, err = b.db.FindByRootMsgContext(ctx, settings.BridgeID, root)
		if err != nil {
			return "", err
		}
		if current != nil && current.ChatID != settings.ChatID {
			return "", managementLocalizedError("management.the.current.persistent.conversation.does.not.belong.to.this")
		}
	}
	return renderCurrentGroupSettingsCardWithNotice(state, settings, root, current, b.groupSettingsCardScope(settings, chatName), notice), nil
}

func (b *Bridge) sendCurrentGroupSettingsCard(ctx context.Context, openID, chatID, chatName string) error {
	if !b.isBotOwner(openID) {
		return managementLocalizedError("management.only.the.owner.can.manage.group.settings")
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
		return managementLocalizedError("management.per.group.settings.are.unavailable.in.private.chat")
	}
	if b.groupSettingsScopeToken(settings.BridgeID, settings.ChatID, strings.TrimSpace(chatName), settings.Version) == "" {
		return managementLocalizedError("management.cannot.verify.the.group.settings.card.check.the.assistant")
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
		return cardToast("error", managementCopy("management.only.the.owner.can.manage.group.settings")), true, nil
	}
	if err := requireGroupSettingsExperiment(); err != nil {
		return groupSettingsErrorToast(err), true, nil
	}
	if action == managementActionGroupSettingsSave {
		scope, chatName, err := b.authenticateGroupSettingsCardScope(event.Event.Action)
		if err != nil {
			return groupSettingsErrorToast(err), true, nil
		}
		messageID := ""
		if event.Event.Context != nil {
			messageID = event.Event.Context.OpenMessageID
		}
		version, ok := b.beginGroupSettingsSave(scope.ChatID, messageID)
		if !ok {
			return cardToast("warning", managementCopy("management.group_settings.save_pending")), true, nil
		}
		b.runGroupSettingsSave(openID, messageID, chatName, scope, modelServiceDraftAction(event.Event.Action), version)
		return cardToast("success", managementCopy("management.group_settings.saving")), true, nil
	}
	settings, chatName, err := b.groupSettingsFromCardScope(event.Event.Action)
	if err != nil {
		return groupSettingsErrorToast(err), true, nil
	}
	messageID := ""
	if event.Event.Context != nil {
		messageID = event.Event.Context.OpenMessageID
	}
	version := b.advanceModelServiceEditor(messageID)
	update := func(card string) { b.updateManagementCardAtVersion(openID, messageID, card, version) }
	if action == managementActionGroupSettingsEdit {
		draft, err := groupSettingsDraftFromCallback(settings, modelServiceDraftAction(event.Event.Action))
		if err != nil {
			return groupSettingsErrorToast(err), true, nil
		}
		state, err := b.groupSettingsManagementState(settings)
		if err != nil {
			return groupSettingsErrorToast(err), true, nil
		}
		if b.hub != nil || (len(modelservice.List()) > 0 && b.resolveModelServicePrincipal != nil) {
			state.NativeModelsTargetRef = draft["group_target"]
			state.Mode = draft["group_interaction"]
			runtimeRef := draft["group_runtime"]
			if runtimeRef == managementGroupNativeRuntime {
				profileRef := groupSettingsOptional(draft["group_agent"], managementGroupInheritProfile)
				if profileRef == "" {
					profileRef = state.DefaultAgent
				}
				if profile, ok := managementProfileByRef(state, profileRef); ok {
					runtimeRef = profile.AgentType
				}
			}
			service, operation := modelServiceSelection(event.Event.Action)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
				defer cancel()
				for {
					current := b.refreshEditorModels(ctx, state, openID, messageID, runtimeRef, service, operation,
						forceEditorModelRefresh(event.Event.Action) || operation == "refresh", func(current managementState) {
							card := renderGroupSettingsEditCardAtPage(current, draft, b.groupSettingsCardScope(settings, chatName), groupSettingsPageFromAction(event.Event.Action))
							b.writeManagementCardAtVersion(ctx, openID, messageID, card, version)
						})
					if !b.modelServiceEditorCurrent(messageID, version) {
						return
					}
					pending := false
					for _, entry := range current.ModelServices {
						if entry.Service.ID == service && (entry.State.Status == "pending" || entry.State.Status == "installing") {
							pending = true
						}
					}
					if !pending {
						return
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(3 * time.Second):
					}
					if !b.modelServiceEditorCurrent(messageID, version) {
						return
					}
					operation = "refresh"
				}
			}()
			return cardToast("success", managementCopy("management.model.service.updating")), true, nil
		}
		update(renderGroupSettingsEditCardAtPage(state, draft, b.groupSettingsCardScope(settings, chatName), groupSettingsPageFromAction(event.Event.Action)))
		return cardToast("success", managementCopy("management.group.settings.opened.not.saved.yet")), true, nil
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
			return cardToast("error", managementCopy("management.choose.whether.to.add.or.remove.this.group.from")), true, nil
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
	var message any = managementCopy("management.current.group.settings.opened")
	if action == managementActionGroupConversationNew {
		message = managementCopy("management.new.persistent.conversation.ready.the.next.group.message.starts")
	} else if action == managementActionGroupSettingsWatch {
		message = managementGroupWatchSuccessText(managementCallbackString(event.Event.Action, "operation"))
	}
	return cardToast("success", message), true, nil
}

func groupSettingsErrorToast(err error) *callback.CardActionTriggerResponse {
	if errors.Is(err, errGroupSettingsChanged) {
		return cardToast("warning", managementCopy("management.group.settings.changed.send.amux.again.in.the.target"))
	}
	return cardToast("error", managementTruncateText(managementErrorText(err), 240))
}

type managementGroupSettingsDraft map[string]string

func groupIdleTimeoutInput(settings *GroupSettings) string {
	if settings.IdleTimeoutInherited {
		return ""
	}
	return strconv.Itoa(settings.IdleTimeoutSeconds)
}

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
		"group_args": string(argsJSON), "group_idle_seconds": groupIdleTimeoutInput(settings),
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
			return nil, managementLocalizedError("management.invalid.format.for.group.settings.field.s", key)
		}
		draft[key] = strings.TrimSpace(text)
	}
	for _, values := range []map[string]any{action.Value, action.FormValue} {
		if value, ok := values["daily_cleanup"]; ok {
			text, valid := value.(string)
			if !valid {
				return nil, managementLocalizedError("cleanup.invalid")
			}
			draft["daily_cleanup"] = text
		}
	}
	if (action.Name == "group_model" || action.Name == "group_interaction" || action.Name == "daily_cleanup") && action.Option != "" {
		draft[action.Name] = action.Option
	}
	return draft, nil
}

func (draft managementGroupSettingsDraft) settings(base *GroupSettings) (*GroupSettings, error) {
	settings := *base
	interaction := draft["group_interaction"]
	if !managementValidInteraction(interaction) {
		return nil, managementLocalizedError("management.choose.a.valid.group.conversation.mode")
	}
	settings.InteractionMode, settings.IsolationRequired = managementDecomposeInteraction(interaction)
	settings.DefaultAgent = groupSettingsOptional(draft["group_agent"], managementGroupInheritProfile)
	settings.DefaultRuntimeRef = groupSettingsOptional(draft["group_runtime"], managementGroupNativeRuntime)
	settings.DefaultModelRef = managementModelRefFromForm(draft["group_model"])
	if settings.DefaultModelRef == managementGroupNativeModel {
		settings.DefaultModelRef = draft["group_native_model"]
		if settings.DefaultModelRef == "" {
			return nil, managementLocalizedError("management.enter.a.native.model.name.or.select.the.default")
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
		return nil, managementLocalizedError("management.extra.arguments.must.be.a.json.string.array.such")
	}
	settings.DefaultArgs = make([]string, 0, len(args))
	for _, arg := range args {
		value, ok := arg.(string)
		if !ok {
			return nil, managementLocalizedError("management.extra.arguments.must.be.a.json.string.array")
		}
		settings.DefaultArgs = append(settings.DefaultArgs, value)
	}
	for _, field := range []struct {
		key    string
		label  any
		target *int
	}{
		{"group_idle_seconds", managementCopy("management.idle.timeout.in.seconds"), &settings.IdleTimeoutSeconds},
		{"group_context_limit", managementCopy("management.context.message.count"), &settings.ThreadContextLimit},
		{"group_max_total", managementCopy("management.group.session.limit"), &settings.MaxSessionsTotal},
		{"group_max_per_user", managementCopy("management.session.limit.per.user"), &settings.MaxSessionsPerUser},
	} {
		if field.key == "group_idle_seconds" {
			settings.IdleTimeoutInherited = draft[field.key] == ""
			if settings.IdleTimeoutInherited {
				settings.IdleTimeoutSeconds = 0
				continue
			}
		}
		value, err := strconv.Atoi(draft[field.key])
		if err != nil || value < 0 {
			return nil, managementLocalizedError("management.s.must.be.a.nonnegative.integer", field.label)
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

func groupSettingsScopedButton(action string, label any, buttonType string, scope map[string]string) managementButton {
	return managementButton{Action: action, Text: label, Type: buttonType, Values: scope}
}

func renderCurrentGroupSettingsCard(state managementState, settings *GroupSettings, root string, current *LarkChat, scope map[string]string) string {
	return renderCurrentGroupSettingsCardWithNotice(state, settings, root, current, scope, "")
}

func renderCurrentGroupSettingsCardWithNotice(state managementState, settings *GroupSettings, root string, current *LarkChat, scope map[string]string, notice any) string {
	agentRef, source := settings.DefaultAgent, managementCopy("management.selected.for.this.group")
	if agentRef == "" {
		agentRef, source = state.DefaultAgent, managementCopy("management.follow.the.assistant.default.expert")
	}
	policy := managementCopy("management.independent.group.settings")
	if settings.Version == 0 {
		policy = managementCopy("management.using.assistant.settings.group.settings.not.saved.yet")
		source = managementCopy("management.using.assistant.settings")
	}
	interaction := settings.InteractionMode
	if settings.IsolationRequired {
		interaction = interactionModeThreadIsolated
	}
	interactionText := managementInteractionText(interaction)
	if interaction == config.FeishuInteractionSingleton {
		interactionText = managementCopy("management.continue.the.same.conversation.in.this.group")
		if settings.Version == 0 {
			interactionText = managementCopy("management.assistant.conversation.currently.also.covers.other.groups.or.private")
		}
	}
	access := managementCopy("management.only.the.owner.can.start.tasks")
	if settings.Mode == WorkspaceModeShared {
		access = managementCopy("management.group.members.can.start.tasks.subject.to.assistant.and")
	}
	runtime := managementRuntimeLabel(state, settings.DefaultRuntimeRef)
	if settings.DefaultRuntimeRef == "" {
		runtime = managementCopy("management.the.selected.expert.s.native.runtime")
	}
	args, _ := json.Marshal(settings.DefaultArgs)
	if settings.DefaultArgs == nil {
		args = []byte("[]")
	}
	elements := []cardElement{
		managementCompactSummaryElement(
			managementSummaryItem{Title: managementCopy("management.group.name"), Body: managementValueOrPlaceholder(scope["chat_name"], managementCopy("management.unnamed.group"))},
			managementSummaryItem{Title: managementCopy("management.group.id"), Body: settings.ChatID},
			managementSummaryItem{Title: managementCopy("management.current.assistant"), Body: state.ProfileName},
			managementSummaryItem{Title: managementCopy("management.settings.source"), Body: policy}),
		managementKeyValueListElement("group_settings_future_defaults",
			managementSummaryItem{Title: managementCopy("management.default.expert.for.new.sessions"), Body: managementConcat(managementProfileNameByRef(state, agentRef), " · ", agentRef, "（", source, "）")},
			managementSummaryItem{Title: managementCopy("management.message.handling"), Body: interactionText},
			managementSummaryItem{Title: managementCopy("management.access"), Body: access},
			managementSummaryItem{Title: managementCopy("management.runtime"), Body: runtime},
			managementSummaryItem{Title: managementCopy("management.model"), Body: managementRuntimeModelLabel(settings.DefaultModelRef)},
			managementSummaryItem{Title: managementCopy("management.reasoning.effort"), Body: managementReasoningEffortLabel(settings.DefaultReasoningEffort)},
			managementSummaryItem{Title: managementCopy("management.activity.display"), Body: managementThinkingProcessLabel(settings.ThinkingProcess)},
			managementSummaryItem{Title: managementCopy("management.target.machine"), Body: managementValueOrPlaceholder(settings.DefaultTarget, managementCopy("management.automatic"))},
			managementSummaryItem{Title: managementCopy("management.working.directory"), Body: managementValueOrPlaceholder(settings.DefaultCwd, managementCopy("management.runtime.default.directory"))},
			managementSummaryItem{Title: managementCopy("management.extra.arguments"), Body: string(args)},
			managementSummaryItem{Title: managementCopy("management.idle.timeout.in.seconds"), Body: managementValueOrPlaceholder(groupIdleTimeoutInput(settings), managementCopy("workspace.idle.inherit"))},
			managementSummaryItem{Title: managementCopy("management.context.message.count"), Body: strconv.Itoa(settings.ThreadContextLimit)},
			managementSummaryItem{Title: managementCopy("management.group.session.limit.per.user.limit"), Body: fmt.Sprintf("%d / %d", settings.MaxSessionsTotal, settings.MaxSessionsPerUser)}),
		managementHintElement(managementCopy("management.saved.execution.options.apply.only.to.new.sessions.existing")),
	}
	if managementDefaultText(notice) != "" {
		elements = append([]cardElement{managementHintElement(notice)}, elements...)
	}
	buttons := []managementButton{groupSettingsScopedButton(managementActionGroupSettingsEdit, managementCopy("management.edit.group.settings"), "primary", scope)}
	if settings.InteractionMode == config.FeishuInteractionSingleton && settings.Version > 0 {
		status := managementCopy("management.not.created.yet.the.next.valid.task.starts.this")
		var profile, model any = "—", "—"
		if root != "" {
			status = managementCopy("management.reply.directly.in.this.group")
			if current == nil {
				status = managementConcat(status, managementCopy("management.session.binding.unavailable.start.a.new.conversation"))
			} else {
				profile = valueOrPlaceholder(current.AgentProfile, current.AgentType)
				model = managementRuntimeModelLabel(current.EffectiveModel)
				if strings.TrimSpace(current.EffectiveModel) == "" {
					model = managementCopy("management.not.recorded")
				}
				status = managementConcat(status, " · "+current.SessionName)
			}
		}
		elements = append(elements, managementKeyValueListElement("group_settings_current_session",
			managementSummaryItem{Title: managementCopy("management.current.persistent.conversation"), Body: status},
			managementSummaryItem{Title: managementCopy("management.current.session.expert"), Body: profile},
			managementSummaryItem{Title: managementCopy("management.current.session.model"), Body: model}))
		reset := groupSettingsScopedButton(managementActionGroupConversationNew, managementCopy("management.new.persistent.conversation"), "default", scope)
		reset.Confirm = managementConfirm(managementCopy("management.start.a.new.conversation.with.blank.context"), managementCopy("management.the.current.session.stops.accepting.new.input.history.is"))
		buttons = append(buttons, reset)
	}
	joinedWatch := groupWatchConfigHasChat(state.GroupWatch, settings.ChatID)
	watchText := managementConcat(managementCopy("management.group.watch"), managementGroupWatchCommandStatus(state, settings.ChatID))
	if joinedWatch {
		buttons = append(buttons, groupWatchSettingsButton(settings.ChatID, scope["chat_name"]))
		if watch := state.GroupWatch.ForChat(settings.ChatID); watch.AgentProfileRef != "" {
			watchText = managementConcat(watchText, managementConcat(managementCopy("management.watch.uses.a.separate.expert"), watch.AgentProfileRef))
		}
	}
	elements = append(elements, managementHintElement(watchText))
	if joinedWatch || settings.InteractionMode == config.FeishuInteractionThread {
		values := make(map[string]string, len(scope)+1)
		for key, value := range scope {
			values[key] = value
		}
		values["operation"] = "add"
		watch := groupSettingsScopedButton(managementActionGroupSettingsWatch, managementCopy("management.add.this.group.to.watch"), "default", values)
		watch.Confirm = managementConfirm(managementCopy("management.confirm.adding.this.group.to.watch"), managementCopy("management.new.topics.in.this.group.will.be.handled.automatically"))
		if joinedWatch {
			watch.Text, values["operation"], watch.Confirm = managementCopy("management.remove.this.group.from.watch"), "remove", nil
		}
		buttons = append(buttons, watch)
	}
	return marshalNativeManagementCard(managementCardView{Title: managementCopy("management.current.group.settings"), Subtitle: managementCopy("management.manage.only.this.assistant.s.settings.in.this.group"), Summary: managementCopy("management.amux.current.group.settings"), Theme: "wathet", Icon: managementIcon("setting_outlined", "blue"), Elements: elements, Buttons: buttons})
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
func managementGroupSettingsEditFields(state managementState, draft managementGroupSettingsDraft, scope map[string]string) []cardElement {
	selectedAgent := groupSettingsOptional(draft["group_agent"], managementGroupInheritProfile)
	if selectedAgent == "" {
		selectedAgent = state.DefaultAgent
	}
	runtimeRef := groupSettingsOptional(draft["group_runtime"], managementGroupNativeRuntime)
	modelRef := managementModelRefFromForm(draft["group_model"])
	if modelRef == managementGroupNativeModel {
		modelRef = draft["group_native_model"]
	}
	profileOptions := append([]cardOption{{Text: managementStyledText(managementConcat(managementCopy("management.follow.assistant.default.expert"), state.DefaultAgent), cardTitle{Tag: "plain_text"}), Value: managementGroupInheritProfile}}, managementProfileOptions(state, true)...)
	runtimeOptions := append([]cardOption{{Text: managementStyledText(managementCopy("management.use.the.selected.expert.s.native.runtime"), cardTitle{Tag: "plain_text"}), Value: managementGroupNativeRuntime}}, managementRuntimeOptions(state)...)
	modelState := managementSelectedModelState(state, modelRef, draft["group_reasoning"])
	modelOptions := managementModelOptions(modelState, selectedAgent)
	effectiveRuntime := runtimeRef
	if runtimeRef != "" {
		modelOptions = managementRuntimeModelOptions(modelState, runtimeRef)
	} else if profile, ok := managementProfileByRef(state, selectedAgent); ok {
		effectiveRuntime = profile.AgentType
	}
	modelOptions = append(modelOptions, cardOption{Text: managementPlainText(managementCopy("management.enter.a.native.model.name")), Value: managementGroupNativeModel})
	values := make(map[string]string, len(scope)+len(draft))
	for key, value := range scope {
		values[key] = value
	}
	for key, value := range draft {
		values[key] = value
	}
	form := []cardElement{}
	selectField := func(label any, key string, options []cardOption) {
		initial := draft[key]
		found := false
		for _, option := range options {
			if option.Value == initial {
				found = true
				break
			}
		}
		if initial != "" && !found {
			var currentLabel any = initial
			if key == "group_model" {
				currentLabel = managementRuntimeModelLabel(initial)
			}
			options = append(options, cardOption{Text: managementPlainText(managementConcat(managementCopy("management.current.value.validated.on.save"), currentLabel)), Value: initial})
		}
		field := managementSelectStaticElement(key, managementCopy("management.select.an.option"), initial, options)
		if key == "group_model" || key == "group_interaction" || key == "daily_cleanup" {
			field.Behaviors = []cardBehavior{{Type: "callback", Value: managementButtonValue(groupSettingsScopedButton(managementActionGroupSettingsEdit, "", "", values))}}
		}
		form = append(form, managementSettingsFieldRow(label, field))
	}
	inputField := func(label any, key string, placeholder any) {
		form = append(form, managementSettingsFieldRow(label, cardElement{Tag: "input", Name: key, DefaultValue: draft[key], Width: "fill", Placeholder: managementStyledTextPtr(placeholder, cardTitle{Tag: "plain_text"})}))
	}
	selectField(managementCopy("management.group.conversation.mode"), "group_interaction", []cardOption{
		{Text: managementStyledText(managementCopy("management.continue.the.same.conversation.in.this.group"), cardTitle{Tag: "plain_text"}), Value: config.FeishuInteractionSingleton},
		{Text: managementStyledText(managementCopy("management.separate.conversation.per.topic"), cardTitle{Tag: "plain_text"}), Value: config.FeishuInteractionThread},
		{Text: managementStyledText(managementCopy("management.separate.topics.user.isolation"), cardTitle{Tag: "plain_text"}), Value: interactionModeThreadIsolated},
	})
	if managementUsesThreadRouting(draft["group_interaction"]) {
		initial := draft["daily_cleanup"]
		if initial == "" {
			initial = state.DailyCleanup
		}
		field := managementSelectStaticElement("daily_cleanup", managementCopy("cleanup.choose"), dailyCleanupInitial(initial), dailyCleanupOptions())
		field.Behaviors = []cardBehavior{{Type: "callback", Value: managementButtonValue(groupSettingsScopedButton(managementActionGroupSettingsEdit, "", "", values))}}
		form = append(form, managementSettingsFieldRow(managementCopy("cleanup.title"), field), managementHintElement(managementCopy("cleanup.hint")))
	}
	selectField(managementCopy("management.default.expert.for.new.sessions"), "group_agent", profileOptions)
	selectField(managementCopy("management.runtime"), "group_runtime", runtimeOptions)
	selectField(managementCopy("management.model"), "group_model", modelOptions)
	form = append(form, modelServiceElements(state, effectiveRuntime, values, managementActionGroupSettingsEdit)...)
	inputField(managementCopy("management.native.model.name.when.selected.above"), "group_native_model", managementCopy("management.for.example.a.model.name.supported.by.the.runtime"))
	reasoningOptions := managementRuntimeReasoningOptions(state, effectiveRuntime, modelRef)
	if runtimeRef == "" {
		reasoningOptions = managementProfileReasoningOptions(state, selectedAgent, modelRef)
	}
	selectField(managementCopy("management.reasoning.effort"), "group_reasoning", reasoningOptions)
	selectField(managementCopy("management.activity.display"), "group_thinking", []cardOption{
		{Text: managementStyledText(managementCopy("management.collapsed"), cardTitle{Tag: "plain_text"}), Value: config.FeishuThinkingProcessCollapsed},
		{Text: managementStyledText(managementCopy("management.expanded"), cardTitle{Tag: "plain_text"}), Value: config.FeishuThinkingProcessExpanded},
		{Text: managementStyledText(managementCopy("management.hidden"), cardTitle{Tag: "plain_text"}), Value: config.FeishuThinkingProcessHidden},
	})
	selectField(managementCopy("management.who.can.start.tasks"), "group_access", []cardOption{
		{Text: managementStyledText(managementCopy("management.owner.only"), cardTitle{Tag: "plain_text"}), Value: WorkspaceModeOwnerOnly},
		{Text: managementStyledText(managementCopy("management.group.members.subject.to.assistant.and.execution.permissions"), cardTitle{Tag: "plain_text"}), Value: WorkspaceModeShared},
	})
	inputField(managementCopy("management.target.machine"), "group_target", managementCopy("management.leave.blank.for.automatic.selection"))
	inputField(managementCopy("management.working.directory"), "group_cwd", managementCopy("management.absolute.path.on.the.target.machine.leave.blank.for"))
	inputField(managementCopy("management.extra.arguments.json.string.array"), "group_args", `["--flag", "value with spaces"]`)
	inputField(managementCopy("management.idle.timeout.seconds.0.to.disable.or.at.least"), "group_idle_seconds", managementCopy("workspace.idle.inherit"))
	inputField(managementCopy("management.context.message.count"), "group_context_limit", managementCopy("management.nonnegative.integer"))
	inputField(managementCopy("management.group.session.limit.0.for.unlimited"), "group_max_total", managementCopy("management.nonnegative.integer"))
	inputField(managementCopy("management.session.limit.per.user.0.for.unlimited"), "group_max_per_user", managementCopy("management.nonnegative.integer"))
	return form
}

// renderGroupSettingsEditCardAtPage renders one editor page. A page past the
// end falls back to the last page so a shrinking catalog cannot strand the user
// on an empty card.
func renderGroupSettingsEditCardAtPage(state managementState, draft managementGroupSettingsDraft, scope map[string]string, page int) string {
	pageScope := make(map[string]string, len(scope)+1)
	for k, v := range scope {
		pageScope[k] = v
	}
	pageScope[groupSettingsPageKey] = strconv.Itoa(page)
	pages := paginateManagementGroupSettings(managementGroupSettingsEditFields(state, draft, pageScope), draft, scope)
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
				managementHintElement(managementCopy("management.showing.d.d.options.current.value.retained", len(kept), total)))
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
	submit := func(action string, label any) cardElement {
		button := managementFormSubmitButton(action, label)
		button.Value, _ = json.Marshal(managementButtonValue(groupSettingsScopedButton(action, label, "primary", values)))
		return button
	}
	save := submit(managementActionGroupSettingsSave, managementCopy("management.save.group.settings"))
	save.Confirm = managementConfirm(managementCopy("management.save.these.group.settings"), managementCopy("management.execution.options.affect.only.new.sessions.existing.topics.keep"))
	refresh := submit(managementActionGroupSettingsEdit, managementCopy("management.refresh.model.options"))
	refresh.Type = "default"
	if pages > 1 {
		form = append(form, managementFormButtonRowElement(save, refresh, groupSettingsPageButton(scope, draft, page-1, managementCopy("management.previous.page"), page > 0), groupSettingsPageButton(scope, draft, page+1, managementCopy("management.next.page"), page < pages-1)))
	} else {
		form = append(form, managementFormButtonRowElement(save, refresh))
	}
	elements := []cardElement{
		managementHintElement(managementCopy("management.new.topics.and.new.persistent.conversations.use.these.group")),
		managementHintElement(managementCopy("management.after.changing.the.expert.or.runtime.click.refresh.model")),
	}
	if pages > 1 {
		elements = append(elements, managementHintElement(managementCopy("management.page.d.d.of.settings.changing.pages.preserves.entered", page+1, pages)))
	}
	elements = append(elements, cardElement{Tag: "form", Name: "group_settings_form", Direction: "vertical", VerticalSpacing: "8px", Elements: form})
	return marshalNativeManagementCard(managementCardView{
		Title: managementCopy("management.edit.group.settings"), Subtitle: managementConcat(managementValueOrPlaceholder(scope["chat_name"], managementCopy("management.unnamed.group")), " · ", scope["chat_id"]), Summary: managementCopy("management.amux.group.settings.editor"), Theme: "wathet", Icon: managementIcon("edit_outlined", "blue"),
		Elements: elements,
		Buttons:  []managementButton{groupSettingsScopedButton(managementActionGroupSettingsOpen, managementCopy("management.cancel"), "default", scope)},
	})
}

// groupSettingsPageButton renders a page switch. Disabled edges stay visible so
// the row keeps its shape across pages; they carry no action when disabled.
//
// Feishu requires every element name in a card to be unique, so the name is
// suffixed per direction rather than reusing the refresh button's action name.
func groupSettingsPageButton(scope map[string]string, draft managementGroupSettingsDraft, page int, label any, enabled bool) cardElement {
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
