package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/config"
)

func (b *Bridge) resolveTarget(ref string) (resolvedBridgeTarget, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return resolvedBridgeTarget{}, false
	}
	var found resolvedBridgeTarget
	ok := false
	for _, target := range b.hub.ListTargets() {
		if target.Id != ref && target.Name != ref {
			continue
		}
		if ok {
			return resolvedBridgeTarget{}, false
		}
		found = resolvedBridgeTarget{ID: target.Id, Name: target.Name}
		ok = true
	}
	return found, ok
}
func (b *Bridge) resolveSessionRef(ref string) (resolvedBridgeSession, bool) {
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "@"))
	if ref == "" {
		return resolvedBridgeSession{}, false
	}
	if strings.Contains(ref, "/") {
		parts := strings.SplitN(ref, "/", 2)
		if parts[0] == "" || parts[1] == "" {
			return resolvedBridgeSession{}, false
		}
		targetRef, sessionRef := parts[0], parts[1]
		var found resolvedBridgeSession
		ok := false
		for _, target := range b.hub.ListTargets() {
			if target.Name != targetRef && target.Id != targetRef {
				continue
			}
			for _, s := range target.Sessions {
				if s.Name != sessionRef && s.Id != sessionRef {
					continue
				}
				if ok {
					return resolvedBridgeSession{}, false
				}
				found = resolvedBridgeSession{TargetID: target.Id, TargetName: target.Name, SessionName: s.Name, AgentProfile: s.AgentProfile, AgentType: s.AgentType, EffectiveModel: s.EffectiveModel}
				ok = true
			}
		}
		return found, ok
	}
	var found resolvedBridgeSession
	ok := false
	for _, sb := range b.hub.ListTargets() {
		for _, s := range sb.Sessions {
			if s.Name != ref && s.Id != ref {
				continue
			}
			if ok {
				return resolvedBridgeSession{}, false
			}
			found = resolvedBridgeSession{TargetID: sb.Id, TargetName: sb.Name, SessionName: s.Name, AgentProfile: s.AgentProfile, AgentType: s.AgentType, EffectiveModel: s.EffectiveModel}
			ok = true
		}
	}
	return found, ok
}
func (b *Bridge) handleCommand(ctx context.Context, openID, text string) error {
	switch command.Resolve(command.ScopePrivate, text) {
	case command.CommandAmux:
		return b.cmdAmuxManagement(ctx, openID)
	case command.CommandHelp:
		return b.cmdHelp(ctx, openID)
	}
	if agentRef, ok := parseNaturalProfileDefaultAgent(text); ok {
		return b.cmdSetProfileDefaultAgent(ctx, openID, agentRef)
	}
	text = strings.TrimPrefix(text, "/")
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(parts[0])
	switch cmd {
	case "amux":
		if len(parts) > 1 {
			switch strings.ToLower(parts[1]) {
			case "status", "settings", "setting", "onboarding", "help", "h", "?":
				return nil
			}
		}
		_, _, err := b.notifyDM(ctx, openID, "用法：/amux")
		return err
	case "amux1":
		return nil
	case "help", "h", "?":
		return b.cmdHelp(ctx, openID)
	}
	return b.cmdHelp(ctx, openID)
}
func (b *Bridge) cmdAmuxManagement(ctx context.Context, openID string) error {
	return b.sendOwnerManagementCard(ctx, openID, renderManagementOverviewCard(b.managementState()))
}
func isAmuxManagementCommand(text string) bool {
	text, ok := slashCommandText(text)
	if !ok {
		return false
	}
	parts := strings.Fields(strings.ToLower(text))
	return len(parts) == 1 && parts[0] == "amux"
}
func (b *Bridge) cmdSetProfileDefaultAgent(ctx context.Context, openID, agentRef string) error {
	if b != nil && b.managementPackActions != nil {
		return b.managementPackActions.WithRegistryStable(func() error {
			return b.setProfileDefaultAgentRegistryStable(ctx, openID, agentRef)
		})
	}
	return b.setProfileDefaultAgentRegistryStable(ctx, openID, agentRef)
}
func (b *Bridge) setProfileDefaultAgentRegistryStable(ctx context.Context, openID, agentRef string) error {
	if !b.ownerConfirmed() || !b.canUseBridge(openID) {
		_, _, err := b.notifyDM(ctx, openID, ownerConfirmationRequiredText())
		return err
	}
	resolved, err := resolveBridgeAgentRef(agentRef)
	if err != nil {
		_, _, notifyErr := b.notifyDM(ctx, openID, err.Error())
		return notifyErr
	}
	state, err := b.currentBotProfileState()
	if err != nil {
		return err
	}
	if state.InteractionMode == config.FeishuInteractionSingleton {
		if state.DefaultAgent != resolved {
			_, _, notifyErr := b.notifyDM(ctx, openID, "更换当前专家会开始新的连续对话，需要确认。请发送 /amux，在“助手”中选择新的默认专家。")
			return notifyErr
		}
		_, _, notifyErr := b.notifyDM(ctx, openID, fmt.Sprintf("默认专家已经是 %s。", resolved))
		return notifyErr
	}
	state, err = b.db.CommitThreadBotSettings(b.currentBridgeId(), state.Version, resolved)
	if err != nil {
		return err
	}
	b.projectLegacyBotSettings(state)
	_, _, err = b.notifyDM(ctx, openID, fmt.Sprintf("默认专家已设置为 %s。之后新群和未单独设置的群会使用这个专家。", resolved))
	return err
}
func (b *Bridge) cmdHelp(ctx context.Context, openID string) error {
	return b.sendOwnerManagementCard(ctx, openID, renderManagementHelpCard(b.managementState()))
}
