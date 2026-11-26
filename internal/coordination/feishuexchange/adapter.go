package feishuexchange

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/coordination"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	CollaboratorPrefix = "external-bot:"
	TargetPrefix       = "feishu-bot-target:"
	ExchangePrefix     = "feishu-exchange:"
	waitPollInterval   = 100 * time.Millisecond
)

// InitiatorBinding identifies the unique active Feishu group thread bound to an
// initiating Session.
type InitiatorBinding struct {
	BridgeID  string
	ChatID    string
	RootMsgID string
}

// Host is the narrow port implemented by a Feishu Bridge so external Bot
// coordination stays decoupled from Bridge internals and Lark SDK transport.
type Host interface {
	BridgeID() string
	ProfileName() string
	ExchangeStore() *Store
	ResolveInitiatorBinding(initiatorRef string) (*InitiatorBinding, error)
	ExternalBotReady(chatID string) (bool, error)
	ValidateExternalBotCreate(ctx context.Context, chatID string) error
	RefreshExternalBotChatBestEffort(ctx context.Context, chatID string)
	SendExternalBotDelegation(ctx context.Context, chatID, threadRootID, botOpenID, input string) (messageID string, err error)
}

// Adapter is both the collaborator catalog and the lifecycle backend for
// outbound Feishu external Bot exchanges.
type Adapter struct {
	hosts map[string]Host
	now   func() time.Time
}

// NewAdapter builds a coordination.Adapter over one or more Feishu Bridge hosts
// and recovers any pending exchanges left behind by a previous process.
func NewAdapter(hosts []Host) (*Adapter, error) {
	adapter := &Adapter{hosts: make(map[string]Host), now: time.Now}
	for _, host := range hosts {
		if host == nil || host.ExchangeStore() == nil {
			continue
		}
		id := strings.TrimSpace(host.BridgeID())
		if id == "" {
			return nil, errors.New("external Bot coordination requires a bridge id")
		}
		if adapter.hosts[id] != nil {
			return nil, fmt.Errorf("duplicate Feishu bridge id %q", id)
		}
		adapter.hosts[id] = host
		if err := host.ExchangeStore().RecoverExchanges(id, adapter.now().UTC()); err != nil {
			return nil, fmt.Errorf("recover external Bot exchanges for %s: %w", host.ProfileName(), err)
		}
	}
	return adapter, nil
}

// SetNow overrides the clock used for timestamps and deadline adjudication.
func (a *Adapter) SetNow(now func() time.Time) {
	if a != nil && now != nil {
		a.now = now
	}
}

func (a *Adapter) BackendKind() string {
	return coordination.BackendKindFeishuBot
}

func (a *Adapter) PrebindRef(route *coordination.Route) (string, error) {
	if route == nil || strings.TrimSpace(route.ID) == "" {
		return "", errors.New("delegation route id is required")
	}
	return ExchangePrefix + route.ID, nil
}

func (a *Adapter) List(ctx context.Context, scope coordination.CatalogScope) ([]coordination.CollaboratorDescriptor, error) {
	source, err := a.initiatorSource(scope.InitiatorRef)
	if err != nil {
		return nil, err
	}
	ready, err := sourceReady(source)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}
	h := source.host
	h.RefreshExternalBotChatBestEffort(ctx, source.binding.ChatID)
	peers, err := h.ExchangeStore().ListPeersForChat(source.binding.BridgeID, source.binding.ChatID)
	if err != nil {
		return nil, fmt.Errorf("list external Bot peers for %s current group: %w", h.ProfileName(), err)
	}
	result := make([]coordination.CollaboratorDescriptor, 0, len(peers))
	for _, peer := range peers {
		result = append(result, descriptorForPeer(h.ProfileName(), peer))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (a *Adapter) ResolveTarget(_ context.Context, collaboratorID string, scope coordination.CatalogScope) (coordination.ResolvedCollaborator, error) {
	collaboratorID = strings.TrimSpace(collaboratorID)
	if !strings.HasPrefix(collaboratorID, CollaboratorPrefix) {
		return coordination.ResolvedCollaborator{}, fmt.Errorf("unknown collaborator %q", collaboratorID)
	}
	source, err := a.initiatorSource(scope.InitiatorRef)
	if err != nil {
		return coordination.ResolvedCollaborator{}, err
	}
	ready, err := sourceReady(source)
	if err != nil {
		return coordination.ResolvedCollaborator{}, err
	}
	if !ready {
		return coordination.ResolvedCollaborator{}, fmt.Errorf("unknown or unavailable collaborator %q for the current Feishu group", collaboratorID)
	}
	peer, err := source.host.ExchangeStore().Peer(source.binding.BridgeID, collaboratorID)
	if err != nil {
		return coordination.ResolvedCollaborator{}, err
	}
	if peer == nil || peer.ChatID != strings.TrimSpace(source.binding.ChatID) {
		return coordination.ResolvedCollaborator{}, fmt.Errorf("unknown or stale collaborator %q for the current Feishu group", collaboratorID)
	}
	return coordination.ResolvedCollaborator{
		BackendKind: coordination.BackendKindFeishuBot,
		TargetRef:   EncodeTarget(*peer),
		TrustLevel:  coordination.TrustExternalUntrusted,
	}, nil
}

func (a *Adapter) Create(ctx context.Context, route *coordination.Route) (string, error) {
	if route == nil {
		return "", errors.New("delegation route is required")
	}
	peer, err := DecodeTarget(route.TargetRef)
	if err != nil {
		return "", err
	}
	h := a.hosts[peer.BridgeID]
	if h == nil {
		return "", status.Errorf(codes.Unavailable, "Feishu Bot Profile for collaborator %q is unavailable", route.CollaboratorID)
	}
	backendRef, err := a.PrebindRef(route)
	if err != nil {
		return "", err
	}
	if route.BackendRef != "" && route.BackendRef != backendRef {
		return "", fmt.Errorf("external Bot backend ref %q does not match deterministic ref %q", route.BackendRef, backendRef)
	}
	store := h.ExchangeStore()
	exists, err := store.ExchangeExists(backendRef)
	if err != nil {
		return "", fmt.Errorf("read existing Feishu exchange: %w", err)
	}
	if exists {
		return backendRef, nil
	}
	source, err := h.ResolveInitiatorBinding(route.InitiatorRef)
	if err != nil {
		return "", status.Errorf(codes.FailedPrecondition, "resolve current Feishu group: %v", err)
	}
	if source == nil || strings.TrimSpace(source.ChatID) != peer.ChatID || strings.TrimSpace(source.RootMsgID) == "" {
		return "", status.Error(codes.FailedPrecondition, "external Bot delegation is limited to the initiator session's current Feishu group")
	}
	if err := h.ValidateExternalBotCreate(ctx, peer.ChatID); err != nil {
		return "", err
	}
	currentPeer, err := store.Peer(peer.BridgeID, route.CollaboratorID)
	if err != nil {
		return "", status.Errorf(codes.Unavailable, "verify Feishu external Bot membership: %v", err)
	}
	if currentPeer == nil || currentPeer.ChatID != peer.ChatID || currentPeer.BotOpenID != peer.BotOpenID {
		return "", status.Error(codes.FailedPrecondition, "Feishu external Bot is no longer a member of the target group")
	}
	threadRootID := strings.TrimSpace(source.RootMsgID)
	when := a.now().UTC()
	created, err := store.CreateExchange(Exchange{
		BackendRef: backendRef, BridgeID: peer.BridgeID, CollaboratorID: route.CollaboratorID,
		ChatID: peer.ChatID, BotOpenID: peer.BotOpenID, RootMessageID: threadRootID, Deadline: route.Deadline, CreatedAt: when,
	})
	if err != nil {
		if errors.Is(err, ErrExchangeAlreadyRunning) {
			return "", status.Error(codes.FailedPrecondition, "another external Bot delegation is already running for this thread and Bot")
		}
		return "", fmt.Errorf("create Feishu exchange: %w", err)
	}
	if !created {
		return backendRef, nil
	}
	messageID, sendErr := h.SendExternalBotDelegation(ctx, peer.ChatID, threadRootID, peer.BotOpenID, route.Input)
	correlationRootID := strings.TrimSpace(threadRootID)
	if correlationRootID == "" {
		correlationRootID = strings.TrimSpace(messageID)
	}
	if sendErr != nil || strings.TrimSpace(messageID) == "" {
		message := "platform returned no root message id"
		if sendErr != nil {
			message = sendErr.Error()
		}
		if err := store.FailExchangeWithRoot(backendRef, correlationRootID, coordination.FailureBackendFailed, message, a.now().UTC()); err != nil {
			return "", fmt.Errorf("record Feishu delivery failure: %w", err)
		}
		return backendRef, nil
	}
	if err := store.MarkExchangeRunning(backendRef, correlationRootID, a.now().UTC()); err != nil {
		// The platform already accepted the message. Keep the public route bound
		// to this exchange even if local correlation persistence failed; returning
		// a create error would release the idempotency claim and risk a duplicate
		// outbound message on retry.
		_ = store.FailExchangeWithRoot(backendRef, correlationRootID, coordination.FailureBackendFailed, "message was sent but local reply correlation could not be persisted", a.now().UTC())
		return backendRef, nil
	}
	return backendRef, nil
}

type initiatorSource struct {
	host    Host
	binding *InitiatorBinding
}

func (a *Adapter) initiatorSource(initiatorRef string) (*initiatorSource, error) {
	bridgeIDs := make([]string, 0, len(a.hosts))
	for bridgeID := range a.hosts {
		bridgeIDs = append(bridgeIDs, bridgeID)
	}
	sort.Strings(bridgeIDs)
	var source *initiatorSource
	for _, bridgeID := range bridgeIDs {
		h := a.hosts[bridgeID]
		binding, err := h.ResolveInitiatorBinding(initiatorRef)
		if err != nil {
			return nil, err
		}
		if binding == nil {
			continue
		}
		if source != nil && (source.binding.BridgeID != binding.BridgeID || source.binding.ChatID != binding.ChatID || source.binding.RootMsgID != binding.RootMsgID) {
			return nil, fmt.Errorf("initiator %q resolves to multiple active Feishu bindings", strings.TrimSpace(initiatorRef))
		}
		source = &initiatorSource{host: h, binding: binding}
	}
	return source, nil
}

func sourceReady(source *initiatorSource) (bool, error) {
	if source == nil || source.host == nil || source.binding == nil {
		return false, nil
	}
	return source.host.ExternalBotReady(source.binding.ChatID)
}

func (a *Adapter) Get(_ context.Context, route *coordination.Route) (*coordination.Snapshot, error) {
	return a.read(route, 0, false)
}

func (a *Adapter) Wait(ctx context.Context, route *coordination.Route, fromSeq uint64) (*coordination.Snapshot, error) {
	for {
		snapshot, err := a.read(route, fromSeq, true)
		if err != nil {
			return nil, err
		}
		if len(snapshot.Events) > 0 || snapshot.Delegation.Status.IsTerminal() || ctx.Err() != nil {
			return snapshot, nil
		}
		timer := time.NewTimer(waitPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (a *Adapter) RespondPermission(context.Context, *coordination.Route, coordination.PermissionResponse) error {
	return status.Error(codes.FailedPrecondition, "external Bot collaborator does not support permission responses")
}

func (a *Adapter) Cancel(context.Context, *coordination.Route) error {
	return status.Error(codes.FailedPrecondition, "external Bot collaborator is not cancelable")
}

func (a *Adapter) read(route *coordination.Route, fromSeq uint64, withEvents bool) (*coordination.Snapshot, error) {
	if route == nil || !strings.HasPrefix(route.BackendRef, ExchangePrefix) {
		return nil, fmt.Errorf("invalid external Bot backend ref %q", backendRefOf(route))
	}
	peer, err := DecodeTarget(route.TargetRef)
	if err != nil {
		return nil, err
	}
	h := a.hosts[peer.BridgeID]
	if h == nil {
		return nil, status.Errorf(codes.Unavailable, "Feishu Bot Profile for delegation %q is unavailable", route.ID)
	}
	exchange, events, next, err := h.ExchangeStore().ReadExchange(route.BackendRef, fromSeq, a.now().UTC())
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
		delegation.Result = resultEnvelope(exchange.ResultContent, exchange.ResultMessageID)
	}
	if exchange.FailureCode != "" {
		delegation.Failure = &coordination.Failure{Code: exchange.FailureCode, Message: exchange.FailureMessage}
	}
	return &coordination.Snapshot{Delegation: delegation, Events: events, NextSeq: next}, nil
}

func descriptorForPeer(profileName string, peer Peer) coordination.CollaboratorDescriptor {
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
		chatName = shortIDString(peer.ChatID)
	}
	return coordination.CollaboratorDescriptor{
		ID: peer.CollaboratorID, Name: name,
		Description: fmt.Sprintf("飞书群“%s”中的外部 Bot，由当前 %s 助手最近确认；可用性仅为 best effort", chatName, profileName),
		Kind:        coordination.CollaboratorExternalBot, Availability: coordination.AvailabilityBestEffort,
	}
}

func shortIDString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

// CollaboratorID deterministically derives the opaque external Bot collaborator
// identifier scoped to one bridge, chat, and target Bot open ID.
func CollaboratorID(bridgeID, chatID, botOpenID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(bridgeID) + "\x00" + strings.TrimSpace(chatID) + "\x00" + strings.TrimSpace(botOpenID)))
	return CollaboratorPrefix + fmt.Sprintf("%x", sum[:16])
}

// EncodeTarget encodes the bridge, chat, and target Bot identity into a target ref.
func EncodeTarget(peer Peer) string {
	raw := strings.Join([]string{peer.BridgeID, peer.ChatID, peer.BotOpenID}, "\x00")
	return TargetPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeTarget parses a target ref back into its bridge, chat, and Bot identity.
func DecodeTarget(value string) (Peer, error) {
	if !strings.HasPrefix(value, TargetPrefix) {
		return Peer{}, fmt.Errorf("invalid external Bot target ref %q", value)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, TargetPrefix))
	if err != nil {
		return Peer{}, fmt.Errorf("decode external Bot target: %w", err)
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Peer{}, errors.New("external Bot target requires bridge, chat, and Bot ids")
	}
	return Peer{BridgeID: parts[0], ChatID: parts[1], BotOpenID: parts[2]}, nil
}

func backendRefOf(route *coordination.Route) string {
	if route == nil {
		return ""
	}
	return route.BackendRef
}
