package bridge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/i18n"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ConsoleBotSettings is the closed, credential-free operator edit surface.
// Connection credentials, arbitrary config keys and execution RPCs are absent.
type ConsoleBotSettings struct {
	DefaultAgent           string `json:"default_agent"`
	DefaultRuntimeRef      string `json:"default_runtime_ref"`
	DefaultModelRef        string `json:"default_model_ref"`
	DefaultReasoningEffort string `json:"default_reasoning_effort"`
	InteractionMode        string `json:"interaction_mode"`
	IsolationRequired      bool   `json:"isolation_required"`
}

type ConsoleBot struct {
	Profile  string             `json:"profile"`
	ID       string             `json:"id"`
	Name     string             `json:"name"`
	Status   string             `json:"status"`
	Settings ConsoleBotSettings `json:"settings"`
	Version  int64              `json:"version"`
	Actions  []string           `json:"actions"`
}

type ConsoleBotSave struct {
	Profile           string              `json:"profile"`
	ExpectedVersion   int64               `json:"expected_version"`
	Settings          *ConsoleBotSettings `json:"settings"`
	Confirmed         bool                `json:"confirmed,omitempty"`
	ConfirmationToken string              `json:"confirmation_token,omitempty"`
	RequestID         string              `json:"request_id,omitempty"`
	RecoveryAction    string              `json:"recovery_action,omitempty"`
	RecoveryAttemptID string              `json:"recovery_attempt_id,omitempty"`
}

type ConsoleImpact struct {
	English string `json:"en"`
	Chinese string `json:"zh-CN"`
}

type ConsoleBotResult struct {
	Status            string         `json:"status"`
	Bot               ConsoleBot     `json:"bot"`
	Bots              []ConsoleBot   `json:"bots"`
	ConfirmationToken string         `json:"confirmation_token,omitempty"`
	Impact            *ConsoleImpact `json:"impact,omitempty"`
}

type ConsoleGroup struct {
	Profile string   `json:"profile"`
	ChatID  string   `json:"chat_id"`
	Title   string   `json:"title"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

type ConsoleGroupsResult struct {
	Status         string         `json:"status"`
	Groups         []ConsoleGroup `json:"groups"`
	Profile        string         `json:"profile,omitempty"`
	Source         string         `json:"source"`
	Settings       *GroupSettings `json:"settings,omitempty"`
	SettingsSource string         `json:"settings_source,omitempty"`
	Actions        []string       `json:"actions"`
}

// ConsoleAgentReference is one place a Feishu Bot or group names a default
// expert. A group default carries its chat ID.
type ConsoleAgentReference struct {
	BridgeID string `json:"bridge_id"`
	ChatID   string `json:"chat_id,omitempty"`
	Agent    string `json:"agent"`
}

// ConsoleAgentReferences answers "what would break if this expert's Pack went
// away" from the Bridge database, the same source the Feishu Pack removal checks.
// It reads stored settings only, so it also covers Bots that are not running.
func (b *Bridge) ConsoleAgentReferences(ctx context.Context) ([]ConsoleAgentReference, error) {
	if err := b.consoleAvailable(ctx); err != nil {
		return nil, err
	}
	references, err := b.db.DefaultAgentReferences(ctx)
	if err != nil {
		return nil, consoleDBError(ctx, err)
	}
	return references, nil
}

func (b *Bridge) consoleAvailable(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	if b == nil || b.db == nil || b.currentBridgeId() == "" {
		return status.Error(codes.Unavailable, "console_bridge_unavailable")
	}
	return nil
}

func (b *Bridge) consoleBot(state BotProfileState) ConsoleBot {
	// The first real transition seeds version 1. Expose that prospective
	// version without seeding on GET, so the first displayed draft is saveable.
	if state.Version == 0 {
		state.Version = 1
	}
	name := b.ProfileName()
	if b.secrets != nil && strings.TrimSpace(b.secrets.BotName) != "" {
		name = b.secrets.BotName
	}
	return ConsoleBot{Profile: b.ProfileName(), ID: b.currentBridgeId(), Name: name, Status: "running",
		Version: state.Version, Actions: []string{"get", "save"}, Settings: ConsoleBotSettings{
			DefaultAgent: effectiveBotDefaultAgentRef(state.DefaultAgent), DefaultRuntimeRef: state.DefaultRuntimeRef,
			DefaultModelRef: state.DefaultModelRef, DefaultReasoningEffort: state.DefaultReasoningEffort,
			InteractionMode: state.InteractionMode, IsolationRequired: state.IsolationRequired,
		}}
}

// ConsoleBot queries the running Bridge without seeding state, starting another
// Bridge, or calling Feishu. The live transition remains the only Bot writer.
func (b *Bridge) ConsoleBot(ctx context.Context) (ConsoleBot, error) {
	if err := b.consoleAvailable(ctx); err != nil {
		return ConsoleBot{}, err
	}
	state, err := b.groupSettingsBotProfileStateContext(ctx)
	if err != nil {
		return ConsoleBot{}, consoleDBError(ctx, err)
	}
	return b.consoleBot(state), nil
}

// ConsoleSaveBot is only for the authenticated host-operator RPC adapter. Owner
// provenance comes from the Bridge, never from caller-supplied OpenIDs.
func (b *Bridge) ConsoleSaveBot(ctx context.Context, input ConsoleBotSave) (ConsoleBotResult, error) {
	if err := b.consoleAvailable(ctx); err != nil {
		return ConsoleBotResult{}, err
	}
	if input.Settings == nil || input.Profile != b.ProfileName() || input.ExpectedVersion < 0 {
		return ConsoleBotResult{}, status.Error(codes.InvalidArgument, "console_invalid_bot_settings")
	}
	s := input.Settings
	if !managementValidInteraction(s.InteractionMode) || strings.TrimSpace(s.DefaultAgent) == "" ||
		(s.IsolationRequired && s.InteractionMode != config.FeishuInteractionThread) {
		return ConsoleBotResult{}, status.Error(codes.InvalidArgument, "console_invalid_bot_settings")
	}
	req := BotSettingsRequest{Interaction: s.InteractionMode, DefaultAgent: s.DefaultAgent,
		DefaultRuntimeRef: s.DefaultRuntimeRef, DefaultModelRef: s.DefaultModelRef,
		DefaultReasoningEffort: s.DefaultReasoningEffort, IsolationRequired: s.IsolationRequired,
		ExpectedVersion: input.ExpectedVersion, Confirmed: input.Confirmed, RequestID: input.RequestID,
		RecoveryAction: input.RecoveryAction, RecoveryAttemptID: input.RecoveryAttemptID}
	if b.secrets != nil {
		req.OpenID = b.secrets.OpenId
	}
	if req.RecoveryAction != "" && req.RecoveryAction != BotSettingsRecoveryCreateBlank {
		return ConsoleBotResult{}, status.Error(codes.InvalidArgument, "console_invalid_recovery_action")
	}
	if (input.ConfirmationToken != "" || req.RecoveryAction != "") && !req.Confirmed {
		return ConsoleBotResult{}, status.Error(codes.InvalidArgument, "console_confirmation_required")
	}
	if req.Confirmed {
		// The existing card boundary also consumes a one-use token before calling
		// ApplyBotSettings. Include isolation, which older card tokens omit.
		b.botSettingsMu.Lock()
		pending, ok := b.pendingBotSettings[input.ConfirmationToken]
		valid := ok && pending.ExpiresAt.After(time.Now()) && botSettingsConfirmationMatches(pending.Request, req) &&
			pending.Request.IsolationRequired == req.IsolationRequired
		if valid {
			delete(b.pendingBotSettings, input.ConfirmationToken)
		}
		b.botSettingsMu.Unlock()
		if !valid {
			return ConsoleBotResult{}, status.Error(codes.FailedPrecondition, "console_confirmation_expired")
		}
		if req.RecoveryAction != "" {
			req.RecoveryAttemptID = input.ConfirmationToken
		}
	}
	result, err := b.ApplyBotSettings(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return ConsoleBotResult{}, status.FromContextError(ctx.Err()).Err()
		}
		return ConsoleBotResult{}, status.Error(codes.FailedPrecondition, "console_bot_transition_failed")
	}
	bot := b.consoleBot(result.State)
	out := ConsoleBotResult{Status: result.Status, Bot: bot, Bots: []ConsoleBot{bot}, ConfirmationToken: result.ConfirmationToken}
	if result.ImpactMessage.For(i18n.English) != "" || result.ImpactMessage.For(i18n.Chinese) != "" {
		out.Impact = &ConsoleImpact{English: result.ImpactMessage.For(i18n.English), Chinese: result.ImpactMessage.For(i18n.Chinese)}
	}
	return out, nil
}

// ConsoleGroups lists only locally known bindings, not the Feishu directory.
// Titles are chat IDs: a stored binding's DisplayTitle names a topic, not a group.
func (b *Bridge) ConsoleGroups(ctx context.Context) ([]ConsoleGroup, error) {
	if err := b.consoleAvailable(ctx); err != nil {
		return nil, err
	}
	rows, err := b.db.db.QueryContext(ctx, `SELECT DISTINCT c.chat_id FROM lark_chats c
		LEFT JOIN feishu_workspaces w ON w.bridge_id=c.bridge_id AND w.chat_id=c.chat_id
		WHERE c.bridge_id=? AND c.chat_id<>'' AND COALESCE(w.kind,'')<>'private'
		ORDER BY c.chat_id LIMIT 1001`, b.currentBridgeId())
	if err != nil {
		return nil, consoleDBError(ctx, err)
	}
	defer rows.Close()
	out := make([]ConsoleGroup, 0)
	for rows.Next() {
		var chatID string
		if err := rows.Scan(&chatID); err != nil {
			return nil, consoleDBError(ctx, err)
		}
		out = append(out, ConsoleGroup{Profile: b.ProfileName(), ChatID: chatID, Title: chatID, Name: chatID, Actions: b.consoleGroupActions()})
		if len(out) > 1000 {
			return nil, status.Error(codes.ResourceExhausted, "console_too_many_groups")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, consoleDBError(ctx, err)
	}
	return out, nil
}

func (b *Bridge) consoleGroupActions() []string {
	if !config.GetBridgeFeishuGroupSettingsEnabled() {
		return []string{}
	}
	return []string{"get", "save"}
}

func (b *Bridge) consoleGroup(ctx context.Context, chatID string) (ConsoleGroup, error) {
	groups, err := b.ConsoleGroups(ctx)
	if err != nil {
		return ConsoleGroup{}, err
	}
	idx := sort.Search(len(groups), func(i int) bool { return groups[i].ChatID >= chatID })
	if idx >= len(groups) || groups[idx].ChatID != chatID {
		return ConsoleGroup{}, status.Error(codes.NotFound, "console_group_not_found")
	}
	return groups[idx], nil
}

func (b *Bridge) ConsoleGroupSettings(ctx context.Context, chatID string) (ConsoleGroupsResult, error) {
	group, err := b.consoleGroup(ctx, chatID)
	if err != nil {
		return ConsoleGroupsResult{}, err
	}
	if !config.GetBridgeFeishuGroupSettingsEnabled() {
		return ConsoleGroupsResult{}, status.Error(codes.FailedPrecondition, "console_group_settings_disabled")
	}
	settings, err := b.groupSettingsDraftContext(ctx, chatID)
	if err != nil {
		return ConsoleGroupsResult{}, consoleDBError(ctx, err)
	}
	if settings.DefaultArgs == nil {
		settings.DefaultArgs = []string{}
	}
	source := "bot_defaults"
	if settings.Version > 0 {
		source = "saved"
	}
	return ConsoleGroupsResult{Status: BotSettingsStatusSuccess, Groups: []ConsoleGroup{group}, Profile: b.ProfileName(),
		Source: "known_bindings", Settings: settings, SettingsSource: source, Actions: b.consoleGroupActions()}, nil
}

// ConsoleSaveGroup uses exactly the live card's registry/policy/route locks,
// validation and atomic versioned write. No second DB owner is constructed.
func (b *Bridge) ConsoleSaveGroup(ctx context.Context, chatID string, settings *GroupSettings, version int64) (ConsoleGroupsResult, error) {
	current, err := b.ConsoleGroupSettings(ctx, chatID)
	if err != nil {
		return ConsoleGroupsResult{}, err
	}
	if settings == nil || version < 0 || (settings.BridgeID != "" && settings.BridgeID != b.currentBridgeId()) ||
		(settings.ChatID != "" && settings.ChatID != chatID) {
		return ConsoleGroupsResult{}, status.Error(codes.InvalidArgument, "console_invalid_group_settings")
	}
	copy := *settings
	copy.BridgeID, copy.ChatID = b.currentBridgeId(), chatID
	if err := validateGroupSettingsValues(&copy); err != nil {
		return ConsoleGroupsResult{}, status.Error(codes.InvalidArgument, "console_invalid_group_settings")
	}
	if current.Settings.Version != version {
		current.Status = BotSettingsStatusStateChanged
		return current, nil
	}
	// applyGroupSettings retains the configured-owner check used by Feishu cards;
	// an operator with no configured owner must configure it through onboarding.
	owner := ""
	if b.secrets != nil {
		owner = b.secrets.OpenId
	}
	if !b.isBotOwner(owner) {
		return ConsoleGroupsResult{}, status.Error(codes.FailedPrecondition, "console_bot_owner_required")
	}
	saved, err := b.applyGroupSettings(ctx, &copy, version, owner)
	if errors.Is(err, errGroupSettingsChanged) {
		current, readErr := b.ConsoleGroupSettings(ctx, chatID)
		if readErr != nil {
			return ConsoleGroupsResult{}, readErr
		}
		current.Status = BotSettingsStatusStateChanged
		return current, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return ConsoleGroupsResult{}, status.FromContextError(ctx.Err()).Err()
		}
		return ConsoleGroupsResult{}, status.Error(codes.FailedPrecondition, "console_group_save_failed")
	}
	if saved.DefaultArgs == nil {
		saved.DefaultArgs = []string{}
	}
	current.Settings, current.SettingsSource = saved, "saved"
	return current, nil
}

func consoleDBError(ctx context.Context, _ error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	return status.Error(codes.Internal, "console_bridge_read_failed")
}
