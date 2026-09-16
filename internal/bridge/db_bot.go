package bridge

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/config"
)

func (b *bridgeDB) ProfileDefaultAgent(bridgeID string) (string, error) {
	if strings.TrimSpace(bridgeID) == "" {
		return "", fmt.Errorf("profile default bridge_id is required")
	}
	var agent string
	err := b.db.QueryRow(`SELECT default_agent FROM feishu_profile_defaults WHERE bridge_id=?`, bridgeID).Scan(&agent)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(agent), nil
}
func (b *bridgeDB) SetProfileDefaultAgent(bridgeID, agent string) error {
	if strings.TrimSpace(bridgeID) == "" {
		return fmt.Errorf("profile default bridge_id is required")
	}
	_, err := b.db.Exec(`
		INSERT INTO feishu_profile_defaults (bridge_id, default_agent)
		VALUES (?, ?)
		ON CONFLICT(bridge_id) DO UPDATE SET
			default_agent=excluded.default_agent,
			updated_at=strftime('%s','now')`, bridgeID, strings.TrimSpace(agent))
	return err
}

func (b *bridgeDB) ConfigureThreadBotDefaultAgent(bridgeID, agent string) (BotProfileState, error) {
	return b.configureThreadBotLaunchDefaults(bridgeID, agent, "", "", false)
}

func (b *bridgeDB) ConfigureThreadBotLaunchDefaults(bridgeID, agent, runtimeRef, modelRef string) (BotProfileState, error) {
	return b.configureThreadBotLaunchDefaults(bridgeID, agent, runtimeRef, modelRef, true)
}

func (b *bridgeDB) configureThreadBotLaunchDefaults(bridgeID, agent, runtimeRef, modelRef string, replaceLaunch bool) (BotProfileState, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	agent = strings.TrimSpace(agent)
	runtimeRef = strings.TrimSpace(runtimeRef)
	modelRef = strings.TrimSpace(modelRef)
	if bridgeID == "" {
		return BotProfileState{}, fmt.Errorf("bot profile bridge_id is required")
	}
	if agent == "" {
		return BotProfileState{}, fmt.Errorf("bot profile default agent is required")
	}
	if replaceLaunch && runtimeRef == "" {
		return BotProfileState{}, fmt.Errorf("bot profile default runtime is required")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return BotProfileState{}, err
	}
	defer tx.Rollback()
	state, err := getBotProfileStateTx(tx, bridgeID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var hasEnabledBinding bool
		if err := tx.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM feishu_bot_sessions
				WHERE bridge_id=? AND enabled=1
			)`, bridgeID).Scan(&hasEnabledBinding); err != nil {
			return BotProfileState{}, err
		}
		if hasEnabledBinding {
			return BotProfileState{}, fmt.Errorf("Feishu Bot Profile has an enabled singleton binding; run bot-session off before changing its onboarding default agent")
		}
		if replaceLaunch {
			if _, err := tx.Exec(`
				INSERT INTO feishu_bot_profiles (
					bridge_id, interaction_mode, default_agent_profile,
					default_runtime_ref, default_model_ref, state_version
				) VALUES (?, ?, ?, ?, ?, 1)`, bridgeID, config.FeishuInteractionThread, agent, runtimeRef, modelRef); err != nil {
				return BotProfileState{}, err
			}
		} else if _, err := tx.Exec(`
			INSERT INTO feishu_bot_profiles (
				bridge_id, interaction_mode, default_agent_profile, state_version
			) VALUES (?, ?, ?, 1)`, bridgeID, config.FeishuInteractionThread, agent); err != nil {
			return BotProfileState{}, err
		}
	case err != nil:
		return BotProfileState{}, err
	case state.InteractionMode == config.FeishuInteractionSingleton:
		return BotProfileState{}, fmt.Errorf("Feishu Bot Profile is in singleton mode; run bot-session off before changing its onboarding default agent")
	case state.InteractionMode != config.FeishuInteractionThread:
		return BotProfileState{}, fmt.Errorf("invalid stored interaction mode %q", state.InteractionMode)
	default:
		launchChanged := replaceLaunch && (state.DefaultRuntimeRef != runtimeRef || state.DefaultModelRef != modelRef)
		if replaceLaunch && (state.DefaultAgent != agent || launchChanged) {
			reasoningEffort := state.DefaultReasoningEffort
			if launchChanged {
				reasoningEffort = ""
			}
			if _, err := tx.Exec(`
				UPDATE feishu_bot_profiles
				SET default_agent_profile=?, default_runtime_ref=?, default_model_ref=?,
					default_reasoning_effort=?, state_version=state_version+1,
					updated_at=strftime('%s','now')
				WHERE bridge_id=?`, agent, runtimeRef, modelRef, reasoningEffort, bridgeID); err != nil {
				return BotProfileState{}, err
			}
		} else if !replaceLaunch && state.DefaultAgent != agent {
			if _, err := tx.Exec(`
				UPDATE feishu_bot_profiles
				SET default_agent_profile=?, state_version=state_version+1,
					updated_at=strftime('%s','now')
				WHERE bridge_id=?`, agent, bridgeID); err != nil {
				return BotProfileState{}, err
			}
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO feishu_profile_defaults (bridge_id, default_agent)
		VALUES (?, ?)
		ON CONFLICT(bridge_id) DO UPDATE SET
			default_agent=excluded.default_agent,
			updated_at=strftime('%s','now')`, bridgeID, agent); err != nil {
		return BotProfileState{}, err
	}
	state, err = getBotProfileStateTx(tx, bridgeID)
	if err != nil {
		return BotProfileState{}, err
	}
	if err := tx.Commit(); err != nil {
		return BotProfileState{}, err
	}
	return state, nil
}
func (b *bridgeDB) GetBotProfileState(bridgeID, fallbackMode string) (BotProfileState, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	fallbackMode = strings.TrimSpace(fallbackMode)
	if bridgeID == "" {
		return BotProfileState{}, fmt.Errorf("bot profile bridge_id is required")
	}
	if !validBotInteractionMode(fallbackMode) {
		return BotProfileState{}, fmt.Errorf("invalid fallback interaction mode %q", fallbackMode)
	}
	tx, err := b.db.Begin()
	if err != nil {
		return BotProfileState{}, err
	}
	defer tx.Rollback()
	state, err := getBotProfileStateTx(tx, bridgeID)
	if err == sql.ErrNoRows {
		var defaultAgent string
		err = tx.QueryRow(`SELECT default_agent FROM feishu_profile_defaults WHERE bridge_id=?`, bridgeID).Scan(&defaultAgent)
		if err != nil && err != sql.ErrNoRows {
			return BotProfileState{}, err
		}
		if _, err := tx.Exec(`
			INSERT INTO feishu_bot_profiles (bridge_id, interaction_mode, default_agent_profile, state_version)
			VALUES (?, ?, ?, 1)`, bridgeID, fallbackMode, strings.TrimSpace(defaultAgent)); err != nil {
			return BotProfileState{}, err
		}
		state = BotProfileState{BridgeID: bridgeID, InteractionMode: fallbackMode, DefaultAgent: strings.TrimSpace(defaultAgent), Version: 1}
	} else if err != nil {
		return BotProfileState{}, err
	}
	if !validBotInteractionMode(state.InteractionMode) {
		return BotProfileState{}, fmt.Errorf("invalid stored interaction mode %q", state.InteractionMode)
	}
	if err := tx.Commit(); err != nil {
		return BotProfileState{}, err
	}
	return state, nil
}

func (b *bridgeDB) ReadBotProfileState(bridgeID, fallbackMode string) (BotProfileState, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	fallbackMode = strings.TrimSpace(fallbackMode)
	if bridgeID == "" {
		return BotProfileState{}, fmt.Errorf("bot profile bridge_id is required")
	}
	if !validBotInteractionMode(fallbackMode) {
		return BotProfileState{}, fmt.Errorf("invalid fallback interaction mode %q", fallbackMode)
	}
	var state BotProfileState
	var isolationRequired int
	err := b.db.QueryRow(`
		SELECT bridge_id, interaction_mode, default_agent_profile, default_runtime_ref,
			default_model_ref, default_reasoning_effort, isolation_required, state_version
		FROM feishu_bot_profiles
		WHERE bridge_id=?`, bridgeID).Scan(&state.BridgeID, &state.InteractionMode, &state.DefaultAgent,
		&state.DefaultRuntimeRef, &state.DefaultModelRef, &state.DefaultReasoningEffort,
		&isolationRequired, &state.Version)
	state.IsolationRequired = isolationRequired != 0
	if errors.Is(err, sql.ErrNoRows) {
		state = BotProfileState{BridgeID: bridgeID, InteractionMode: fallbackMode}
		err = b.db.QueryRow(`SELECT default_agent FROM feishu_profile_defaults WHERE bridge_id=?`, bridgeID).Scan(&state.DefaultAgent)
		if errors.Is(err, sql.ErrNoRows) {
			return state, nil
		}
	}
	if err != nil {
		return BotProfileState{}, err
	}
	if !validBotInteractionMode(state.InteractionMode) {
		return BotProfileState{}, fmt.Errorf("invalid stored interaction mode %q", state.InteractionMode)
	}
	return state, nil
}

// BotDefaultAgentReferences includes explicit group defaults so a Pack used
// only by a group cannot be removed. Group references include their chat ID.
func (b *bridgeDB) BotDefaultAgentReferences(namespace string) ([]string, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return nil, fmt.Errorf("agent pack namespace is required")
	}
	rows, err := b.db.Query(`
		SELECT bridge_id, default_agent_profile FROM feishu_bot_profiles
		UNION ALL
		SELECT bridge_id || '/' || chat_id, default_agent FROM feishu_group_settings
		WHERE default_agent <> ''
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prefix := namespace + "/"
	var references []string
	for rows.Next() {
		var reference, profile string
		if err := rows.Scan(&reference, &profile); err != nil {
			return nil, err
		}
		if strings.HasPrefix(strings.TrimSpace(profile), prefix) {
			references = append(references, strings.TrimSpace(reference))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return references, nil
}
func (b *bridgeDB) CommitThreadBotSettings(bridgeID string, expectedVersion int64, defaultAgent string) (BotProfileState, error) {
	current, err := b.GetBotProfileState(bridgeID, config.FeishuInteractionThread)
	if err != nil {
		return BotProfileState{}, err
	}
	return b.CommitThreadBotSettingsWithLaunch(bridgeID, expectedVersion, defaultAgent, current.DefaultRuntimeRef, current.DefaultModelRef, current.DefaultReasoningEffort, false)
}
func (b *bridgeDB) CommitThreadBotSettingsWithModel(bridgeID string, expectedVersion int64, defaultAgent, defaultModelRef string, isolationRequired bool) (BotProfileState, error) {
	current, err := b.GetBotProfileState(bridgeID, config.FeishuInteractionThread)
	if err != nil {
		return BotProfileState{}, err
	}
	return b.CommitThreadBotSettingsWithLaunch(bridgeID, expectedVersion, defaultAgent, current.DefaultRuntimeRef, defaultModelRef, current.DefaultReasoningEffort, isolationRequired)
}
func (b *bridgeDB) CommitThreadBotSettingsWithLaunch(bridgeID string, expectedVersion int64, defaultAgent, defaultRuntimeRef, defaultModelRef, defaultReasoningEffort string, isolationRequired bool) (BotProfileState, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	if bridgeID == "" {
		return BotProfileState{}, fmt.Errorf("bot profile bridge_id is required")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return BotProfileState{}, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`
		UPDATE feishu_bot_profiles
		SET interaction_mode=?, default_agent_profile=?, default_runtime_ref=?, default_model_ref=?,
			default_reasoning_effort=?, isolation_required=?, state_version=state_version+1,
			updated_at=strftime('%s','now')
		WHERE bridge_id=? AND state_version=?`, config.FeishuInteractionThread, strings.TrimSpace(defaultAgent),
		strings.TrimSpace(defaultRuntimeRef), strings.TrimSpace(defaultModelRef), strings.TrimSpace(defaultReasoningEffort),
		boolToInt(isolationRequired), bridgeID, expectedVersion)
	if err != nil {
		return BotProfileState{}, err
	}
	if err := requireBotProfileUpdate(result); err != nil {
		return BotProfileState{}, err
	}
	if _, err := tx.Exec(`
		UPDATE feishu_bot_sessions
		SET enabled=0, updated_at=strftime('%s','now')
		WHERE bridge_id=?`, bridgeID); err != nil {
		return BotProfileState{}, err
	}
	state, err := getBotProfileStateTx(tx, bridgeID)
	if err != nil {
		return BotProfileState{}, err
	}
	if err := tx.Commit(); err != nil {
		return BotProfileState{}, err
	}
	return state, nil
}
func (b *bridgeDB) CommitSingletonBotSettings(bridgeID string, expectedVersion int64, defaultAgent string, binding BotSession) (BotProfileState, error) {
	current, err := b.GetBotProfileState(bridgeID, config.FeishuInteractionSingleton)
	if err != nil {
		return BotProfileState{}, err
	}
	return b.CommitSingletonBotSettingsWithLaunch(bridgeID, expectedVersion, defaultAgent, current.DefaultRuntimeRef, current.DefaultModelRef, current.DefaultReasoningEffort, binding)
}
func (b *bridgeDB) CommitSingletonBotSettingsWithModel(bridgeID string, expectedVersion int64, defaultAgent, defaultModelRef string, binding BotSession) (BotProfileState, error) {
	current, err := b.GetBotProfileState(bridgeID, config.FeishuInteractionSingleton)
	if err != nil {
		return BotProfileState{}, err
	}
	return b.CommitSingletonBotSettingsWithLaunch(bridgeID, expectedVersion, defaultAgent, current.DefaultRuntimeRef, defaultModelRef, current.DefaultReasoningEffort, binding)
}
func (b *bridgeDB) CommitSingletonBotSettingsWithLaunch(bridgeID string, expectedVersion int64, defaultAgent, defaultRuntimeRef, defaultModelRef, defaultReasoningEffort string, binding BotSession) (BotProfileState, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	binding.BridgeId = strings.TrimSpace(binding.BridgeId)
	binding.TargetID = strings.TrimSpace(binding.TargetID)
	binding.TargetName = strings.TrimSpace(binding.TargetName)
	binding.SessionName = strings.TrimSpace(binding.SessionName)
	binding.AgentProfile = strings.TrimSpace(binding.AgentProfile)
	binding.AgentType = strings.TrimSpace(binding.AgentType)
	binding.EffectiveModel = strings.TrimSpace(binding.EffectiveModel)
	binding.Cwd = strings.TrimSpace(binding.Cwd)
	if bridgeID == "" {
		return BotProfileState{}, fmt.Errorf("bot profile bridge_id is required")
	}
	if binding.BridgeId != "" && binding.BridgeId != bridgeID {
		return BotProfileState{}, fmt.Errorf("bot session bridge_id %q does not match %q", binding.BridgeId, bridgeID)
	}
	if binding.TargetID == "" || binding.SessionName == "" {
		return BotProfileState{}, fmt.Errorf("singleton binding requires target_id and session_name")
	}
	binding.BridgeId = bridgeID
	binding.Enabled = true
	tx, err := b.db.Begin()
	if err != nil {
		return BotProfileState{}, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`
		UPDATE feishu_bot_profiles
		SET interaction_mode=?, default_agent_profile=?, default_runtime_ref=?, default_model_ref=?,
			default_reasoning_effort=?, isolation_required=0, state_version=state_version+1,
			updated_at=strftime('%s','now')
		WHERE bridge_id=? AND state_version=?`, config.FeishuInteractionSingleton, strings.TrimSpace(defaultAgent),
		strings.TrimSpace(defaultRuntimeRef), strings.TrimSpace(defaultModelRef), strings.TrimSpace(defaultReasoningEffort), bridgeID, expectedVersion)
	if err != nil {
		return BotProfileState{}, err
	}
	if err := requireBotProfileUpdate(result); err != nil {
		return BotProfileState{}, err
	}
	if _, err := tx.Exec(`
		INSERT INTO feishu_bot_sessions (
			bridge_id, enabled, target_id, target_name, session_name, agent_profile, agent_type,
			effective_model, cwd, last_reply_kind, last_reply_chat_id, last_reply_open_id, last_input_message_id
		)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bridge_id) DO UPDATE SET
			enabled=1,
			target_id=excluded.target_id,
			target_name=excluded.target_name,
			session_name=excluded.session_name,
			agent_profile=excluded.agent_profile,
			agent_type=excluded.agent_type,
			effective_model=excluded.effective_model,
			cwd=excluded.cwd,
			last_reply_kind=CASE WHEN excluded.last_reply_kind<>'' THEN excluded.last_reply_kind ELSE feishu_bot_sessions.last_reply_kind END,
			last_reply_chat_id=CASE WHEN excluded.last_reply_chat_id<>'' THEN excluded.last_reply_chat_id ELSE feishu_bot_sessions.last_reply_chat_id END,
			last_reply_open_id=CASE WHEN excluded.last_reply_open_id<>'' THEN excluded.last_reply_open_id ELSE feishu_bot_sessions.last_reply_open_id END,
			last_input_message_id=CASE WHEN excluded.last_input_message_id<>'' THEN excluded.last_input_message_id ELSE feishu_bot_sessions.last_input_message_id END,
			updated_at=strftime('%s','now')`, bridgeID, binding.TargetID, binding.TargetName, binding.SessionName, binding.AgentProfile, binding.AgentType, binding.EffectiveModel, binding.Cwd, strings.TrimSpace(binding.LastReplyKind), strings.TrimSpace(binding.LastReplyChatID), strings.TrimSpace(binding.LastReplyOpenID), strings.TrimSpace(binding.LastInputMessageID)); err != nil {
		return BotProfileState{}, err
	}
	if _, err := tx.Exec(`
		UPDATE lark_chats
		SET status='detached'
		WHERE target_id=? AND session_name=? AND status='active'`, binding.TargetID, binding.SessionName); err != nil {
		return BotProfileState{}, err
	}
	state, err := getBotProfileStateTx(tx, bridgeID)
	if err != nil {
		return BotProfileState{}, err
	}
	if err := tx.Commit(); err != nil {
		return BotProfileState{}, err
	}
	return state, nil
}

func (b *bridgeDB) RefreshSessionIdentity(bridgeID, targetID, sessionName, agentProfile, agentType, effectiveModel string) error {
	bridgeID = strings.TrimSpace(bridgeID)
	targetID = strings.TrimSpace(targetID)
	sessionName = strings.TrimSpace(sessionName)
	if bridgeID == "" || targetID == "" || sessionName == "" {
		return fmt.Errorf("refresh session identity requires bridge_id, target_id, and session_name")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	args := []any{ // RefreshSessionIdentity updates the presentation snapshot for existing
		// bindings after a profile-refresh restart. Routing identity and reply
		// locations remain untouched.
		strings.TrimSpace(agentProfile), strings.TrimSpace(agentType), strings.TrimSpace(effectiveModel), bridgeID, targetID, sessionName}
	if _, err := tx.Exec(`
		UPDATE lark_chats
		SET agent_profile=COALESCE(NULLIF(?, ''), agent_profile),
		    agent_type=COALESCE(NULLIF(?, ''), agent_type),
		    effective_model=COALESCE(NULLIF(?, ''), effective_model)
		WHERE bridge_id=? AND target_id=? AND session_name=? AND status='active'`, args...); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE feishu_bot_sessions
		SET agent_profile=COALESCE(NULLIF(?, ''), agent_profile),
		    agent_type=COALESCE(NULLIF(?, ''), agent_type),
		    effective_model=COALESCE(NULLIF(?, ''), effective_model),
		    updated_at=strftime('%s','now')
		WHERE bridge_id=? AND target_id=? AND session_name=? AND enabled=1`, args...); err != nil {
		return err
	}
	return tx.Commit()
}
func (b *bridgeDB) SetBotSession(s BotSession) error {
	s.BridgeId = strings.TrimSpace(s.BridgeId)
	s.TargetID = strings.TrimSpace(s.TargetID)
	s.TargetName = strings.TrimSpace(s.TargetName)
	s.SessionName = strings.TrimSpace(s.SessionName)
	s.AgentProfile = strings.TrimSpace(s.AgentProfile)
	s.AgentType = strings.TrimSpace(s.AgentType)
	s.EffectiveModel = strings.TrimSpace(s.EffectiveModel)
	s.Cwd = strings.TrimSpace(s.Cwd)
	s.LastReplyKind = strings.TrimSpace(s.LastReplyKind)
	s.LastReplyChatID = strings.TrimSpace(s.LastReplyChatID)
	s.LastReplyOpenID = strings.TrimSpace(s.LastReplyOpenID)
	s.LastInputMessageID = strings.TrimSpace(s.LastInputMessageID)
	if s.BridgeId == "" {
		return fmt.Errorf("bot session bridge_id is required")
	}
	if s.Enabled && (s.TargetID == "" || s.SessionName == "") {
		return fmt.Errorf("enabled bot session requires target_id and session_name")
	}
	enabled := 0
	if s.Enabled {
		enabled = 1
	}
	_, err := b.db.Exec(`
		INSERT INTO feishu_bot_sessions (
			bridge_id, enabled, target_id, target_name, session_name, agent_profile, agent_type, effective_model, cwd, last_reply_kind, last_reply_chat_id, last_reply_open_id, last_input_message_id
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bridge_id) DO UPDATE SET
			enabled=excluded.enabled,
			target_id=excluded.target_id,
			target_name=excluded.target_name,
			session_name=excluded.session_name,
			agent_profile=excluded.agent_profile,
			agent_type=excluded.agent_type,
			effective_model=excluded.effective_model,
			cwd=excluded.cwd,
			last_reply_kind=CASE WHEN excluded.last_reply_kind<>'' THEN excluded.last_reply_kind ELSE feishu_bot_sessions.last_reply_kind END,
			last_reply_chat_id=CASE WHEN excluded.last_reply_chat_id<>'' THEN excluded.last_reply_chat_id ELSE feishu_bot_sessions.last_reply_chat_id END,
			last_reply_open_id=CASE WHEN excluded.last_reply_open_id<>'' THEN excluded.last_reply_open_id ELSE feishu_bot_sessions.last_reply_open_id END,
			last_input_message_id=CASE WHEN excluded.last_input_message_id<>'' THEN excluded.last_input_message_id ELSE feishu_bot_sessions.last_input_message_id END,
			updated_at=strftime('%s','now')`, s.BridgeId, enabled, s.TargetID, s.TargetName, s.SessionName, s.AgentProfile, s.AgentType, s.EffectiveModel, s.Cwd, s.LastReplyKind, s.LastReplyChatID, s.LastReplyOpenID, s.LastInputMessageID)
	if err != nil {
		return err
	}
	if s.Enabled {
		_, err = b.DetachActiveTopicBindingForSession(s.TargetID, s.SessionName)
	}
	return err
}
func (b *bridgeDB) GetBotSession(bridgeID string) (*BotSession, error) {
	bridgeID = strings.TrimSpace(bridgeID)
	if bridgeID == "" {
		return nil, fmt.Errorf("bot session bridge_id is required")
	}
	row := b.db.QueryRow(`
		SELECT bridge_id, enabled, target_id, target_name, session_name, agent_profile, agent_type, effective_model, cwd, last_reply_kind, last_reply_chat_id, last_reply_open_id, last_input_message_id
		FROM feishu_bot_sessions WHERE bridge_id=?`, bridgeID)
	var s BotSession
	var enabled int
	err := row.Scan(&s.BridgeId, &enabled, &s.TargetID, &s.TargetName, &s.SessionName, &s.AgentProfile, &s.AgentType, &s.EffectiveModel, &s.Cwd, &s.LastReplyKind, &s.LastReplyChatID, &s.LastReplyOpenID, &s.LastInputMessageID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
	return &s, nil
}
func (b *bridgeDB) DisableBotSession(bridgeID string) error {
	bridgeID = strings.TrimSpace(bridgeID)
	if bridgeID == "" {
		return fmt.Errorf("bot session bridge_id is required")
	}
	_, err := b.db.Exec(`
		UPDATE feishu_bot_sessions
		SET enabled=0, updated_at=strftime('%s','now')
		WHERE bridge_id=?`, bridgeID)
	return err
}
func (b *bridgeDB) SetBotSessionLastReply(bridgeID, kind, chatID, openID, messageID string) error {
	bridgeID = strings.TrimSpace(bridgeID)
	if bridgeID == "" {
		return fmt.Errorf("bot session bridge_id is required")
	}
	_, err := b.db.Exec(`
		UPDATE feishu_bot_sessions
		SET last_reply_kind=?, last_reply_chat_id=?, last_reply_open_id=?, last_input_message_id=?, updated_at=strftime('%s','now')
		WHERE bridge_id=?`, strings.TrimSpace(kind), strings.TrimSpace(chatID), strings.TrimSpace(openID), strings.TrimSpace(messageID), bridgeID)
	return err
}
func (b *bridgeDB) UpsertChat(c LarkChat) error {
	if strings.TrimSpace(c.BridgeId) == "" {
		return fmt.Errorf("chat bridge_id is required")
	}
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rootAlreadyExists bool
	if err := tx.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM lark_chats WHERE bridge_id=? AND root_msg_id=?
		)`, c.BridgeId, c.RootMsgID).Scan(&rootAlreadyExists); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE lark_chats
		SET status='detached'
		WHERE status='active'
		  AND (
		    (bridge_id = ? AND root_msg_id = ?)
		    OR (target_id = ? AND session_name = ?)
		  )`, c.BridgeId, c.RootMsgID, c.TargetID, c.SessionName); err != nil {
		return err
	}
	_, err = tx.Exec(`
		INSERT INTO lark_chats (bridge_id, chat_id, root_msg_id, thread_id, open_id, target_id, target_name, session_name, agent_profile, agent_type, effective_model, display_title, status, last_activity_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', strftime('%s','now'))
		ON CONFLICT(bridge_id, root_msg_id) DO UPDATE SET
			chat_id=excluded.chat_id,
			thread_id=excluded.thread_id,
			open_id=excluded.open_id,
			target_id=excluded.target_id,
			target_name=excluded.target_name,
			session_name=excluded.session_name,
			agent_profile=excluded.agent_profile,
			agent_type=excluded.agent_type,
			effective_model=excluded.effective_model,
			topic_root_prompt_delivered=CASE
				WHEN lark_chats.target_id=excluded.target_id AND lark_chats.session_name=excluded.session_name
				THEN lark_chats.topic_root_prompt_delivered
				ELSE 0
			END,
			display_title=excluded.display_title,
			last_activity_at=strftime('%s','now'),
			status='active'`, c.BridgeId, c.ChatID, c.RootMsgID, c.ThreadID, c.OpenId, c.TargetID, c.TargetName, c.SessionName, c.AgentProfile, c.AgentType, c.EffectiveModel, c.DisplayTitle)
	if err != nil {
		return err
	}
	entrySource := strings.TrimSpace(c.EntrySource)
	if entrySource == ChatEntrySourceGroupWatch || (!rootAlreadyExists && entrySource == "") {
		if entrySource == "" {
			entrySource = ChatEntrySourceManual
		}
		if _, err := tx.Exec(`
			INSERT INTO feishu_binding_sources (bridge_id, chat_id, root_msg_id, source)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(bridge_id, root_msg_id) DO UPDATE SET
				chat_id=excluded.chat_id,
				source=excluded.source`, c.BridgeId, c.ChatID, c.RootMsgID, entrySource); err != nil {
			return err
		}
	}
	return tx.Commit()
}
