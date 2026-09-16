package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/coordination"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	externalBotCollaboratorPrefix = "external-bot:"
	externalBotTargetPrefix       = "feishu-bot-target:"
	externalBotExchangePrefix     = "feishu-exchange:"
	externalBotWaitPollInterval   = 100 * time.Millisecond
)

// ExternalBotCoordinationAdapter is both the collaborator catalog and the
// lifecycle adapter for outbound Feishu Bot exchanges. Platform behavior and
// exchange truth intentionally stay in the Bridge package.
type ExternalBotCoordinationAdapter struct {
	bridges map[string]*Bridge
	now     func() time.Time
}

func NewExternalBotCoordinationAdapter(bridges []*Bridge) (*ExternalBotCoordinationAdapter, error) {
	adapter := &ExternalBotCoordinationAdapter{bridges: make(map[string]*Bridge), now: time.Now}
	for _, item := range bridges {
		if item == nil || item.db == nil {
			continue
		}
		id := item.currentBridgeId()
		if id == "" {
			return nil, errors.New("external Bot coordination requires a bridge id")
		}
		if adapter.bridges[id] != nil {
			return nil, fmt.Errorf("duplicate Feishu bridge id %q", id)
		}
		adapter.bridges[id] = item
		if err := item.db.RecoverExternalBotExchanges(id, adapter.now().UTC()); err != nil {
			return nil, fmt.Errorf("recover external Bot exchanges for %s: %w", item.ProfileName(), err)
		}
	}
	return adapter, nil
}

func (a *ExternalBotCoordinationAdapter) BackendKind() string {
	return coordination.BackendKindFeishuBot
}

func (a *ExternalBotCoordinationAdapter) PrebindRef(route *coordination.Route) (string, error) {
	if route == nil || strings.TrimSpace(route.ID) == "" {
		return "", errors.New("delegation route id is required")
	}
	return externalBotExchangePrefix + route.ID, nil
}

func (a *ExternalBotCoordinationAdapter) List(ctx context.Context, scope coordination.CatalogScope) ([]coordination.CollaboratorDescriptor, error) {
	source, err := a.initiatorSource(scope.InitiatorRef)
	if err != nil {
		return nil, err
	}
	ready, err := externalBotSourceReady(source)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}
	b := source.bridge
	b.refreshExternalBotCatalogChatBestEffort(ctx, source.binding.ChatID)
	peers, err := b.db.ListExternalBotPeersForChat(source.binding.BridgeId, source.binding.ChatID)
	if err != nil {
		return nil, fmt.Errorf("list external Bot peers for %s current group: %w", b.ProfileName(), err)
	}
	result := make([]coordination.CollaboratorDescriptor, 0, len(peers))
	for _, peer := range peers {
		result = append(result, descriptorForExternalBot(b, peer))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (a *ExternalBotCoordinationAdapter) ResolveTarget(_ context.Context, collaboratorID string, scope coordination.CatalogScope) (coordination.ResolvedCollaborator, error) {
	collaboratorID = strings.TrimSpace(collaboratorID)
	if !strings.HasPrefix(collaboratorID, externalBotCollaboratorPrefix) {
		return coordination.ResolvedCollaborator{}, fmt.Errorf("unknown collaborator %q", collaboratorID)
	}
	source, err := a.initiatorSource(scope.InitiatorRef)
	if err != nil {
		return coordination.ResolvedCollaborator{}, err
	}
	ready, err := externalBotSourceReady(source)
	if err != nil {
		return coordination.ResolvedCollaborator{}, err
	}
	if !ready {
		return coordination.ResolvedCollaborator{}, fmt.Errorf("unknown or unavailable collaborator %q for the current Feishu group", collaboratorID)
	}
	peer, err := source.bridge.db.ExternalBotPeer(source.binding.BridgeId, collaboratorID)
	if err != nil {
		return coordination.ResolvedCollaborator{}, err
	}
	if peer == nil || peer.ChatID != strings.TrimSpace(source.binding.ChatID) {
		return coordination.ResolvedCollaborator{}, fmt.Errorf("unknown or stale collaborator %q for the current Feishu group", collaboratorID)
	}
	return coordination.ResolvedCollaborator{
		BackendKind: coordination.BackendKindFeishuBot,
		TargetRef:   encodeExternalBotTarget(*peer),
		TrustLevel:  coordination.TrustExternalUntrusted,
	}, nil
}

func (a *ExternalBotCoordinationAdapter) Create(ctx context.Context, route *coordination.Route) (string, error) {
	if route == nil {
		return "", errors.New("delegation route is required")
	}
	peer, err := decodeExternalBotTarget(route.TargetRef)
	if err != nil {
		return "", err
	}
	b := a.bridges[peer.BridgeID]
	if b == nil {
		return "", status.Errorf(codes.Unavailable, "Feishu Bot Profile for collaborator %q is unavailable", route.CollaboratorID)
	}
	backendRef, err := a.PrebindRef(route)
	if err != nil {
		return "", err
	}
	if route.BackendRef != "" && route.BackendRef != backendRef {
		return "", fmt.Errorf("external Bot backend ref %q does not match deterministic ref %q", route.BackendRef, backendRef)
	}
	exists, err := b.db.ExternalBotExchangeExists(backendRef)
	if err != nil {
		return "", fmt.Errorf("read existing Feishu exchange: %w", err)
	}
	if exists {
		return backendRef, nil
	}
	source, err := externalBotInitiatorBinding(b, route.InitiatorRef)
	if err != nil {
		return "", status.Errorf(codes.FailedPrecondition, "resolve current Feishu group: %v", err)
	}
	if source == nil || strings.TrimSpace(source.ChatID) != peer.ChatID || strings.TrimSpace(source.RootMsgID) == "" {
		return "", status.Error(codes.FailedPrecondition, "external Bot delegation is limited to the initiator session's current Feishu group")
	}
	if !externalBotCatalogEnabled(b) {
		return "", status.Error(codes.FailedPrecondition, "Feishu Bot-to-Bot collaboration is disabled")
	}
	mode, err := b.interactionMode()
	if err != nil || mode != config.FeishuInteractionThread {
		return "", status.Error(codes.FailedPrecondition, "Feishu external Bot delegation requires thread mode")
	}
	workspace, err := b.currentWorkspace(peer.ChatID)
	if err != nil {
		return "", status.Errorf(codes.Unavailable, "read Feishu workspace: %v", err)
	}
	if workspace.Mode != WorkspaceModeShared || (workspace.Kind != WorkspaceKindGroup && workspace.Kind != WorkspaceKindTopicGroup) {
		return "", status.Error(codes.FailedPrecondition, "Feishu external Bot delegation requires a shared group workspace")
	}
	if err := b.refreshExternalBotCatalogChat(ctx, peer.ChatID, true); err != nil {
		b.observeCapabilityPermissionError(ctx, "bot_to_bot", "智能体协作", err, true)
		return "", status.Errorf(codes.Unavailable, "verify Feishu external Bot membership: %v", err)
	}
	currentPeer, err := b.db.ExternalBotPeer(peer.BridgeID, route.CollaboratorID)
	if err != nil {
		return "", status.Errorf(codes.Unavailable, "verify Feishu external Bot membership: %v", err)
	}
	if currentPeer == nil || currentPeer.ChatID != peer.ChatID || currentPeer.BotOpenID != peer.BotOpenID {
		return "", status.Error(codes.FailedPrecondition, "Feishu external Bot is no longer a member of the target group")
	}
	threadRootID := strings.TrimSpace(source.RootMsgID)
	when := a.now().UTC()
	created, err := b.db.CreateExternalBotExchange(externalBotExchange{
		BackendRef: backendRef, BridgeID: peer.BridgeID, CollaboratorID: route.CollaboratorID,
		ChatID: peer.ChatID, BotOpenID: peer.BotOpenID, RootMessageID: threadRootID, Deadline: route.Deadline, CreatedAt: when,
	})
	if err != nil {
		if errors.Is(err, errExternalBotExchangeAlreadyRunning) {
			return "", status.Error(codes.FailedPrecondition, "another external Bot delegation is already running for this thread and Bot")
		}
		return "", fmt.Errorf("create Feishu exchange: %w", err)
	}
	if !created {
		return backendRef, nil
	}
	send := b.sendExternalBotDelegationToChat
	if b.externalBotDelegation != nil {
		send = b.externalBotDelegation
	}
	messageID, _, sendErr := send(ctx, peer.ChatID, threadRootID, peer.BotOpenID, route.Input)
	correlationRootID := strings.TrimSpace(threadRootID)
	if correlationRootID == "" {
		correlationRootID = strings.TrimSpace(messageID)
	}
	if sendErr != nil || strings.TrimSpace(messageID) == "" {
		message := "platform returned no root message id"
		if sendErr != nil {
			message = sendErr.Error()
		}
		if err := b.db.FailExternalBotExchangeWithRoot(backendRef, correlationRootID, coordination.FailureBackendFailed, message, a.now().UTC()); err != nil {
			return "", fmt.Errorf("record Feishu delivery failure: %w", err)
		}
		return backendRef, nil
	}
	if err := b.db.MarkExternalBotExchangeRunning(backendRef, correlationRootID, a.now().UTC()); err != nil {
		// The platform already accepted the message. Keep the public route bound
		// to this exchange even if local correlation persistence failed; returning
		// a create error would release the idempotency claim and risk a duplicate
		// outbound message on retry.
		_ = b.db.FailExternalBotExchangeWithRoot(backendRef, correlationRootID, coordination.FailureBackendFailed, "message was sent but local reply correlation could not be persisted", a.now().UTC())
		return backendRef, nil
	}
	return backendRef, nil
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

type externalBotInitiatorSource struct {
	bridge  *Bridge
	binding *LarkChat
}

func (a *ExternalBotCoordinationAdapter) initiatorSource(initiatorRef string) (*externalBotInitiatorSource, error) {
	bridgeIDs := make([]string, 0, len(a.bridges))
	for bridgeID := range a.bridges {
		bridgeIDs = append(bridgeIDs, bridgeID)
	}
	sort.Strings(bridgeIDs)
	var source *externalBotInitiatorSource
	for _, bridgeID := range bridgeIDs {
		b := a.bridges[bridgeID]
		binding, err := externalBotInitiatorBinding(b, initiatorRef)
		if err != nil {
			return nil, err
		}
		if binding == nil {
			continue
		}
		if source != nil && (source.binding.BridgeId != binding.BridgeId || source.binding.ChatID != binding.ChatID || source.binding.RootMsgID != binding.RootMsgID) {
			return nil, fmt.Errorf("initiator %q resolves to multiple active Feishu bindings", strings.TrimSpace(initiatorRef))
		}
		source = &externalBotInitiatorSource{bridge: b, binding: binding}
	}
	return source, nil
}

func externalBotSourceReady(source *externalBotInitiatorSource) (bool, error) {
	if source == nil || source.bridge == nil || source.binding == nil || !externalBotCatalogEnabled(source.bridge) {
		return false, nil
	}
	mode, err := source.bridge.interactionMode()
	if err != nil || mode != config.FeishuInteractionThread {
		return false, nil
	}
	workspace, err := source.bridge.currentWorkspace(source.binding.ChatID)
	if err != nil {
		return false, fmt.Errorf("read current Feishu workspace: %w", err)
	}
	return workspace.Mode == WorkspaceModeShared && (workspace.Kind == WorkspaceKindGroup || workspace.Kind == WorkspaceKindTopicGroup), nil
}

func (a *ExternalBotCoordinationAdapter) Get(_ context.Context, route *coordination.Route) (*coordination.Snapshot, error) {
	return a.read(route, 0, false)
}

func (a *ExternalBotCoordinationAdapter) Wait(ctx context.Context, route *coordination.Route, fromSeq uint64) (*coordination.Snapshot, error) {
	for {
		snapshot, err := a.read(route, fromSeq, true)
		if err != nil {
			return nil, err
		}
		if len(snapshot.Events) > 0 || snapshot.Delegation.Status.IsTerminal() || ctx.Err() != nil {
			return snapshot, nil
		}
		timer := time.NewTimer(externalBotWaitPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (a *ExternalBotCoordinationAdapter) RespondPermission(context.Context, *coordination.Route, coordination.PermissionResponse) error {
	return status.Error(codes.FailedPrecondition, "external Bot collaborator does not support permission responses")
}

func (a *ExternalBotCoordinationAdapter) Cancel(context.Context, *coordination.Route) error {
	return status.Error(codes.FailedPrecondition, "external Bot collaborator is not cancelable")
}

func (a *ExternalBotCoordinationAdapter) read(route *coordination.Route, fromSeq uint64, withEvents bool) (*coordination.Snapshot, error) {
	if route == nil || !strings.HasPrefix(route.BackendRef, externalBotExchangePrefix) {
		return nil, fmt.Errorf("invalid external Bot backend ref %q", backendRef(route))
	}
	peer, err := decodeExternalBotTarget(route.TargetRef)
	if err != nil {
		return nil, err
	}
	b := a.bridges[peer.BridgeID]
	if b == nil {
		return nil, status.Errorf(codes.Unavailable, "Feishu Bot Profile for delegation %q is unavailable", route.ID)
	}
	exchange, events, next, err := b.db.ReadExternalBotExchange(route.BackendRef, fromSeq, a.now().UTC())
	if err != nil {
		return nil, err
	}
	if !withEvents {
		events = nil
		next = 0
	}
	delegation := &coordination.Delegation{
		ID: route.ID, Direction: coordination.DirectionOutbound, InitiatorRef: route.InitiatorRef,
		TargetRef: route.TargetRef, ReplyTo: exchange.RootMessageID, ScopeRef: route.ScopeRef,
		ParentDelegationID: route.ParentDelegationID, HopCount: route.HopCount, HopBudget: route.HopBudget,
		Input: route.Input, Status: exchange.Status, Deadline: exchange.Deadline, BackendRef: route.BackendRef,
		ResultTrustLevel: coordination.TrustExternalUntrusted, IdempotencyKey: route.IdempotencyKey,
		CreatedAt: exchange.CreatedAt, UpdatedAt: exchange.UpdatedAt, FinishedAt: exchange.FinishedAt,
	}
	if exchange.ResultContent != "" {
		delegation.Result = externalBotResult(exchange.ResultContent, exchange.ResultMessageID)
	}
	if exchange.FailureCode != "" {
		delegation.Failure = &coordination.Failure{Code: exchange.FailureCode, Message: exchange.FailureMessage}
	}
	return &coordination.Snapshot{Delegation: delegation, Events: events, NextSeq: next}, nil
}

func descriptorForExternalBot(b *Bridge, peer externalBotPeer) coordination.CollaboratorDescriptor {
	shortID := peer.BotOpenID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	name := strings.TrimSpace(peer.BotDisplayName)
	if name == "" {
		name = "Feishu Bot " + shortID
	}
	chatName := strings.TrimSpace(peer.ChatDisplayName)
	if chatName == "" {
		chatName = shortExternalBotID(peer.ChatID)
	}
	return coordination.CollaboratorDescriptor{
		ID: peer.CollaboratorID, Name: name,
		Description: fmt.Sprintf("飞书群“%s”中的外部 Bot，由当前 %s 助手最近确认；可用性仅为 best effort", chatName, b.ProfileName()),
		Kind:        coordination.CollaboratorExternalBot, Availability: coordination.AvailabilityBestEffort,
	}
}

func shortExternalBotID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func externalBotCollaboratorID(bridgeID, chatID, botOpenID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(bridgeID) + "\x00" + strings.TrimSpace(chatID) + "\x00" + strings.TrimSpace(botOpenID)))
	return externalBotCollaboratorPrefix + fmt.Sprintf("%x", sum[:16])
}

func encodeExternalBotTarget(peer externalBotPeer) string {
	raw := strings.Join([]string{peer.BridgeID, peer.ChatID, peer.BotOpenID}, "\x00")
	return externalBotTargetPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeExternalBotTarget(value string) (externalBotPeer, error) {
	if !strings.HasPrefix(value, externalBotTargetPrefix) {
		return externalBotPeer{}, fmt.Errorf("invalid external Bot target ref %q", value)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, externalBotTargetPrefix))
	if err != nil {
		return externalBotPeer{}, fmt.Errorf("decode external Bot target: %w", err)
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return externalBotPeer{}, errors.New("external Bot target requires bridge, chat, and Bot ids")
	}
	return externalBotPeer{BridgeID: parts[0], ChatID: parts[1], BotOpenID: parts[2]}, nil
}

func backendRef(route *coordination.Route) string {
	if route == nil {
		return ""
	}
	return route.BackendRef
}
