package bridge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/config"
)

var errGroupSettingsExperimentDisabled = errors.New("群独立配置实验已关闭。请负责人在私聊 /amux → 实验功能中开启“群独立配置”并重启。已保存的群配置和会话保留，启用前暂停接收新任务。")

func requireGroupSettingsExperiment() error {
	if !config.GetBridgeFeishuGroupSettingsEnabled() {
		return errGroupSettingsExperimentDisabled
	}
	return nil
}

// groupSettingsDraft reads the current configuration without opting a group in.
// Empty runtime/model/effort in a saved record mean the selected Profile's
// defaults, not a live inheritance from the Bot's execution settings.
func (b *Bridge) groupSettingsDraft(chatID string) (*GroupSettings, error) {
	if err := requireGroupSettingsExperiment(); err != nil {
		return nil, err
	}
	chatID = strings.TrimSpace(chatID)
	if b == nil || b.db == nil || chatID == "" {
		return nil, fmt.Errorf("group settings require a bridge and chat_id")
	}
	if saved, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID); err != nil || saved != nil {
		return saved, err
	}
	state, err := b.currentBotProfileStateReadOnly()
	if err != nil {
		return nil, err
	}
	ws, err := b.db.ReadWorkspace(b.currentBridgeId(), chatID)
	if err != nil {
		return nil, err
	}
	if ws.Kind == WorkspaceKindPrivate {
		return nil, fmt.Errorf("私聊不支持群独立设置")
	}
	modelRef := b.effectiveBotLaunchModel(state.DefaultRuntimeRef, state.DefaultModelRef)
	if state.DefaultRuntimeRef == "" && ws.DefaultModel != "" {
		modelRef = ws.DefaultModel
	}
	return &GroupSettings{
		BridgeID: b.currentBridgeId(), ChatID: chatID, InteractionMode: state.InteractionMode,
		IsolationRequired: state.IsolationRequired, DefaultAgent: effectiveBotDefaultAgentRef(state.DefaultAgent),
		DefaultRuntimeRef: state.DefaultRuntimeRef, DefaultModelRef: modelRef,
		DefaultReasoningEffort: state.DefaultReasoningEffort,
		ThinkingProcess:        config.GetBridgeFeishuPresentationForProfile(b.ProfileName()).ThinkingProcess,
		Mode:                   ws.Mode, DefaultTarget: ws.DefaultTarget, DefaultCwd: ws.DefaultCwd,
		DefaultArgs: append([]string(nil), ws.DefaultArgs...), IdleTimeoutSeconds: ws.IdleTimeoutSeconds,
		ThreadContextLimit: ws.ThreadContextLimit, MaxSessionsTotal: ws.MaxSessionsTotal, MaxSessionsPerUser: ws.MaxSessionsPerUser,
	}, nil
}

func (b *Bridge) groupSettingsAgent(settings *GroupSettings) (string, error) {
	if ref := strings.TrimSpace(settings.DefaultAgent); ref != "" {
		return ref, nil
	}
	state, err := b.currentBotProfileStateReadOnly()
	if err != nil {
		return "", err
	}
	return effectiveBotDefaultAgentRef(state.DefaultAgent), nil
}

func validateGroupSettingsValues(settings *GroupSettings) error {
	if settings == nil {
		return fmt.Errorf("群设置不能为空")
	}
	if err := validateGroupSettings(*settings); err != nil {
		return err
	}
	if settings.DefaultCwd != "" && !filepath.IsAbs(settings.DefaultCwd) {
		return fmt.Errorf("工作目录必须使用目标机器上的绝对路径")
	}
	for _, arg := range settings.DefaultArgs {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("附加参数不能包含 NUL 字符")
		}
	}
	return nil
}

func (b *Bridge) validateGroupSettings(settings *GroupSettings) error {
	if err := validateGroupSettingsValues(settings); err != nil {
		return err
	}
	if settings.BridgeID != b.currentBridgeId() {
		return fmt.Errorf("群设置不属于当前智能体")
	}
	ws, err := b.db.ReadWorkspace(settings.BridgeID, settings.ChatID)
	if err != nil {
		return err
	}
	if ws.Kind == WorkspaceKindPrivate {
		return fmt.Errorf("私聊不支持群独立设置")
	}
	agent, err := b.groupSettingsAgent(settings)
	if err != nil {
		return err
	}
	if _, err := resolveBridgeAgentLaunchWithSelection(agent, settings.DefaultArgs, settings.DefaultRuntimeRef, settings.DefaultModelRef, settings.DefaultReasoningEffort); err != nil {
		return err
	}
	if settings.DefaultTarget != "" && b.hub != nil {
		if _, ok := b.resolveTarget(settings.DefaultTarget); !ok {
			return fmt.Errorf("找不到唯一 target: %s", settings.DefaultTarget)
		}
	}
	if settings.IsolationRequired && b.hub != nil {
		if ok, reason := b.isolationLaunchSupported(); !ok {
			return fmt.Errorf("当前环境不支持用户隔离：%s", reason)
		}
	}
	if settings.InteractionMode == config.FeishuInteractionSingleton {
		watch := b.groupWatchSnapshot()
		if watch.Enabled && groupWatchConfigHasChat(watch, settings.ChatID) {
			return fmt.Errorf("请先关闭本群值守，再切换到本群连续会话")
		}
	}
	return nil
}

func groupSettingsRouteKey(bridgeID, chatID string) string {
	return "group-settings:" + bridgeID + ":" + chatID
}

func (b *Bridge) applyGroupSettings(ctx context.Context, settings *GroupSettings, expectedVersion int64, openID string) (*GroupSettings, error) {
	if !b.isBotOwner(openID) {
		return nil, fmt.Errorf("只有负责人可以修改群设置")
	}
	if err := requireGroupSettingsExperiment(); err != nil {
		return nil, err
	}
	if settings == nil {
		return nil, fmt.Errorf("群设置不能为空")
	}
	var saved *GroupSettings
	commit := func() error {
		b.groupWatchSettingsMu.Lock()
		defer b.groupWatchSettingsMu.Unlock()
		unlock := b.lockRoute(groupSettingsRouteKey(b.currentBridgeId(), settings.ChatID))
		defer unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.validateGroupSettings(settings); err != nil {
			return err
		}
		copy := *settings
		copy.UpdatedBy = openID
		var err error
		saved, err = b.db.SaveGroupSettings(copy, expectedVersion)
		return err
	}
	var err error
	if b.managementPackActions != nil {
		err = b.managementPackActions.WithRegistryStable(commit)
	} else {
		err = commit()
	}
	return saved, err
}

func (b *Bridge) newGroupConversation(chatID string, version int64, openID string) error {
	if !b.isBotOwner(openID) {
		return fmt.Errorf("只有负责人可以新开本群连续会话")
	}
	if err := requireGroupSettingsExperiment(); err != nil {
		return err
	}
	unlock := b.lockRoute(groupSettingsRouteKey(b.currentBridgeId(), chatID))
	defer unlock()
	settings, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID)
	if err != nil {
		return err
	}
	if settings == nil || settings.InteractionMode != config.FeishuInteractionSingleton {
		return fmt.Errorf("本群尚未启用独立连续会话")
	}
	return b.db.ClearGroupSessionRoot(b.currentBridgeId(), chatID, version)
}

func (b *Bridge) interactionModeForChat(chatID string) (string, error) {
	if b != nil && b.db != nil && chatID != "" {
		settings, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID)
		if err != nil {
			return "", err
		}
		if settings != nil {
			if err := requireGroupSettingsExperiment(); err != nil {
				return "", err
			}
			return settings.InteractionMode, nil
		}
	}
	return b.interactionMode()
}

// Legacy workspace commands update the same complete snapshot once a group has
// opted in, so access and execution defaults cannot diverge between surfaces.
func (b *Bridge) updateGroupWorkspace(ctx context.Context, chatID, openID string, update func(*GroupSettings), legacy func() error) error {
	settings, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID)
	if err != nil {
		return err
	}
	if settings == nil {
		return legacy()
	}
	update(settings)
	_, err = b.applyGroupSettings(ctx, settings, settings.Version, openID)
	return err
}

type groupLaunchPolicyKey struct{}

// A nil policy still records that this input used the legacy Bot-wide policy.
type groupLaunchPolicy struct {
	settings          *GroupSettings
	isolationRequired bool
	shared            bool
}

func groupSettingsWorkspace(ws *FeishuWorkspace, settings *GroupSettings) *FeishuWorkspace {
	if settings == nil {
		return ws
	}
	copy := *ws
	copy.Mode, copy.DefaultAgent = settings.Mode, settings.DefaultAgent
	copy.DefaultTarget, copy.DefaultCwd, copy.DefaultModel = settings.DefaultTarget, settings.DefaultCwd, settings.DefaultModelRef
	copy.DefaultArgs = append([]string(nil), settings.DefaultArgs...)
	copy.IdleTimeoutSeconds, copy.ThreadContextLimit = settings.IdleTimeoutSeconds, settings.ThreadContextLimit
	copy.MaxSessionsTotal, copy.MaxSessionsPerUser = settings.MaxSessionsTotal, settings.MaxSessionsPerUser
	return &copy
}

func withGroupLaunchPolicy(ctx context.Context, settings *GroupSettings, isolationRequired, shared bool) context.Context {
	return context.WithValue(ctx, groupLaunchPolicyKey{}, groupLaunchPolicy{settings, isolationRequired, shared})
}

func (b *Bridge) newSessionPolicy(ctx context.Context, chatID string) (groupLaunchPolicy, error) {
	if policy, ok := ctx.Value(groupLaunchPolicyKey{}).(groupLaunchPolicy); ok {
		if policy.settings != nil {
			if err := requireGroupSettingsExperiment(); err != nil {
				return groupLaunchPolicy{}, err
			}
		}
		return policy, nil
	}
	if b != nil && b.db != nil && chatID != "" {
		settings, err := b.db.ReadGroupSettings(b.currentBridgeId(), chatID)
		if err != nil {
			return groupLaunchPolicy{}, err
		}
		if settings != nil {
			if err := requireGroupSettingsExperiment(); err != nil {
				return groupLaunchPolicy{}, err
			}
			return groupLaunchPolicy{settings: settings, isolationRequired: settings.IsolationRequired}, nil
		}
	}
	return groupLaunchPolicy{isolationRequired: b.currentBotIsolationRequired()}, nil
}

// GroupSettingsInDB exposes the local operator surface; it uses the same
// validation and workspace transaction as the live owner's settings card.
func GroupSettingsInDB(path, profile, bridgeID, chatID string) (*GroupSettings, string, error) {
	if err := requireGroupSettingsExperiment(); err != nil {
		return nil, "", err
	}
	db, err := openBridgeDBForProfile(path, profile)
	if err != nil {
		return nil, "", err
	}
	defer db.Close()
	b := &Bridge{db: db, profile: profile, bridgeID: bridgeID}
	settings, err := b.groupSettingsDraft(chatID)
	if err != nil {
		return nil, "", err
	}
	root, err := db.GetGroupSessionRoot(bridgeID, chatID)
	return settings, root, err
}

func SaveGroupSettingsInDB(path, profile string, settings *GroupSettings, version int64) (*GroupSettings, error) {
	if err := requireGroupSettingsExperiment(); err != nil {
		return nil, err
	}
	if settings == nil {
		return nil, fmt.Errorf("group settings are required")
	}
	db, err := openBridgeDBForProfile(path, profile)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	b := &Bridge{db: db, profile: profile, bridgeID: settings.BridgeID}
	b.setGroupWatchConfig(config.GetBridgeFeishuGroupWatchForProfile(profile))
	if err := b.validateGroupSettings(settings); err != nil {
		return nil, err
	}
	copy := *settings
	copy.UpdatedBy = "operator"
	return db.SaveGroupSettings(copy, version)
}

func NewGroupConversationInDB(path, profile, bridgeID, chatID string, version int64) error {
	if err := requireGroupSettingsExperiment(); err != nil {
		return err
	}
	db, err := openBridgeDBForProfile(path, profile)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.ClearGroupSessionRoot(bridgeID, chatID, version)
}

// ValidateGroupWatchInteractionInDB validates a candidate installation config
// against saved group policies without opting groups in or changing their state.
// Disabled independent groups remain paused instead of inheriting the Bot mode.
func ValidateGroupWatchInteractionInDB(path, profile, bridgeID string, watch config.BridgeFeishuGroupWatchConfig, botMode string, groupSettingsEnabled bool) error {
	if !watch.Enabled || len(watch.ChatIDs) == 0 {
		return nil
	}
	db, err := openBridgeDBForProfile(path, profile)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, chatID := range watch.ChatIDs {
		mode := botMode
		settings, err := db.ReadGroupSettings(bridgeID, chatID)
		if err != nil {
			return err
		}
		if settings != nil {
			if !groupSettingsEnabled {
				continue
			}
			mode = settings.InteractionMode
		}
		if err := config.ValidateBridgeFeishuGroupWatchInteraction(watch, mode); err != nil {
			return fmt.Errorf("群 %s: %w", chatID, err)
		}
	}
	return nil
}
