package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/coordination/feishuexchange"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const externalBotCollaboratorPrefix = feishuexchange.CollaboratorPrefix

type externalBotPeer = feishuexchange.Peer
type externalBotExchange = feishuexchange.Exchange

func externalBotCollaboratorID(bridgeID, chatID, botOpenID string) string {
	return feishuexchange.CollaboratorID(bridgeID, chatID, botOpenID)
}

func (b *bridgeDB) externalBotStore() *feishuexchange.Store {
	if b == nil || b.db == nil {
		return nil
	}
	return feishuexchange.NewStore(b.db)
}

func (b *bridgeDB) UpsertExternalBotPeer(peer externalBotPeer) error {
	return b.externalBotStore().UpsertPeer(peer)
}

func (b *bridgeDB) SyncExternalBotPeers(bridgeID, chatID string, peers []externalBotPeer, complete bool) error {
	return b.externalBotStore().SyncPeers(bridgeID, chatID, peers, complete)
}

func (b *bridgeDB) ListExternalBotPeers(bridgeID string) ([]externalBotPeer, error) {
	return b.externalBotStore().ListPeers(bridgeID)
}

func (b *bridgeDB) ListExternalBotPeersForChat(bridgeID, chatID string) ([]externalBotPeer, error) {
	return b.externalBotStore().ListPeersForChat(bridgeID, chatID)
}

func (b *bridgeDB) ExternalBotPeer(bridgeID, collaboratorID string) (*externalBotPeer, error) {
	return b.externalBotStore().Peer(bridgeID, collaboratorID)
}

func (b *bridgeDB) CreateExternalBotExchange(exchange externalBotExchange) (bool, error) {
	return b.externalBotStore().CreateExchange(exchange)
}

func (b *bridgeDB) MarkExternalBotExchangeRunning(backendRef, rootMessageID string, when time.Time) error {
	return b.externalBotStore().MarkExchangeRunning(backendRef, rootMessageID, when)
}

func (b *bridgeDB) CompleteExternalBotExchangeReply(bridgeID, rootMessageID, botOpenID, messageID, text string, when time.Time) (bool, error) {
	return b.externalBotStore().CompleteReply(bridgeID, rootMessageID, botOpenID, messageID, text, when)
}

func (b *bridgeDB) RecoverExternalBotExchanges(bridgeID string, when time.Time) error {
	return b.externalBotStore().RecoverExchanges(bridgeID, when)
}

// ExternalBotHost returns the narrow feishuexchange.Host port implemented by
// this Bridge so Hub coordination wires external Bot exchanges without coupling
// coordination internals to Bridge.
func (b *Bridge) ExternalBotHost() feishuexchange.Host {
	if b == nil {
		return nil
	}
	return externalBotHost{bridge: b}
}

type externalBotHost struct {
	bridge *Bridge
}

func (h externalBotHost) BridgeID() string {
	return h.bridge.currentBridgeId()
}

func (h externalBotHost) ProfileName() string {
	return h.bridge.ProfileName()
}

func (h externalBotHost) ExchangeStore() *feishuexchange.Store {
	if h.bridge == nil || h.bridge.db == nil {
		return nil
	}
	return h.bridge.db.externalBotStore()
}

func (h externalBotHost) ResolveInitiatorBinding(initiatorRef string) (*feishuexchange.InitiatorBinding, error) {
	binding, err := externalBotInitiatorBinding(h.bridge, initiatorRef)
	if err != nil || binding == nil {
		return nil, err
	}
	return &feishuexchange.InitiatorBinding{
		BridgeID:  binding.BridgeId,
		ChatID:    binding.ChatID,
		RootMsgID: binding.RootMsgID,
	}, nil
}

func (h externalBotHost) ExternalBotReady(chatID string) (bool, error) {
	b := h.bridge
	if b == nil || !externalBotCatalogEnabled(b) {
		return false, nil
	}
	mode, err := b.interactionModeForChat(chatID)
	if err != nil || mode != config.FeishuInteractionThread {
		return false, nil
	}
	workspace, err := b.currentWorkspace(chatID)
	if err != nil {
		return false, fmt.Errorf("read current Feishu workspace: %w", err)
	}
	return workspace.Mode == WorkspaceModeShared && (workspace.Kind == WorkspaceKindGroup || workspace.Kind == WorkspaceKindTopicGroup), nil
}

func (h externalBotHost) ValidateExternalBotCreate(ctx context.Context, chatID string) error {
	b := h.bridge
	if !externalBotCatalogEnabled(b) {
		return status.Error(codes.FailedPrecondition, "Feishu Bot-to-Bot collaboration is disabled")
	}
	mode, err := b.interactionModeForChat(chatID)
	if err != nil || mode != config.FeishuInteractionThread {
		return status.Error(codes.FailedPrecondition, "Feishu external Bot delegation requires thread mode")
	}
	workspace, err := b.currentWorkspace(chatID)
	if err != nil {
		return status.Errorf(codes.Unavailable, "read Feishu workspace: %v", err)
	}
	if workspace.Mode != WorkspaceModeShared || (workspace.Kind != WorkspaceKindGroup && workspace.Kind != WorkspaceKindTopicGroup) {
		return status.Error(codes.FailedPrecondition, "Feishu external Bot delegation requires a shared group workspace")
	}
	if err := b.refreshExternalBotCatalogChat(ctx, chatID, true); err != nil {
		b.observeCapabilityPermissionError(ctx, "bot_to_bot", "智能体协作", err, true)
		return status.Errorf(codes.Unavailable, "verify Feishu external Bot membership: %v", err)
	}
	return nil
}

func (h externalBotHost) RefreshExternalBotChatBestEffort(ctx context.Context, chatID string) {
	h.bridge.refreshExternalBotCatalogChatBestEffort(ctx, chatID)
}

func (h externalBotHost) SendExternalBotDelegation(ctx context.Context, chatID, threadRootID, botOpenID, input string) (string, error) {
	b := h.bridge
	send := b.sendExternalBotDelegationToChat
	if b.externalBotDelegation != nil {
		send = b.externalBotDelegation
	}
	messageID, _, err := send(ctx, chatID, threadRootID, botOpenID, input)
	return messageID, err
}

func externalBotInitiatorBinding(b *Bridge, initiatorRef string) (*LarkChat, error) {
	if b == nil || b.db == nil || b.hub == nil {
		return nil, nil
	}
	const prefix = "amux-session:"
	initiator := strings.TrimSpace(initiatorRef)
	if !strings.HasPrefix(initiator, prefix) {
		return nil, nil
	}
	sessionID := strings.TrimSpace(strings.TrimPrefix(initiator, prefix))
	if sessionID == "" {
		return nil, nil
	}
	var result *LarkChat
	for _, target := range b.hub.ListTargets() {
		if target == nil {
			continue
		}
		for _, session := range target.Sessions {
			if session == nil || strings.TrimSpace(session.Id) != sessionID {
				continue
			}
			binding, err := b.db.FindByBridgeSession(b.currentBridgeId(), strings.TrimSpace(target.Id), strings.TrimSpace(session.Name))
			if err != nil {
				return nil, err
			}
			if binding == nil || strings.TrimSpace(binding.ChatID) == "" || strings.TrimSpace(binding.RootMsgID) == "" {
				continue
			}
			if result != nil && (result.BridgeId != binding.BridgeId || result.ChatID != binding.ChatID || result.RootMsgID != binding.RootMsgID) {
				return nil, fmt.Errorf("session id %q resolves to multiple active Feishu bindings", sessionID)
			}
			result = binding
		}
	}
	return result, nil
}
