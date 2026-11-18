package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/sessioncredential"
	"github.com/google/uuid"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

const (
	credentialAdmissionLifetime = 20 * time.Minute
	credentialPollingLifetime   = 8 * time.Minute
)

type CredentialAdmissionOptions struct {
	Render func(CredentialCardView) string
	Notice func(locale, key string) string
}

type CredentialCardView struct {
	Locale   string
	ID       string
	State    sessioncredential.State
	URL      string
	Provider string
}

type credentialAdmissions struct {
	mu     sync.Mutex
	byRoot map[string]*pendingCredentialAdmission
}

type pendingCredentialAdmission struct {
	id, root, message, chat, openID, target string
	identity                                principal.ExecutionIdentity
	actor                                   sessioncredential.FeishuActor
	original                                context.Context
	resume                                  func(context.Context) error
	expires                                 time.Time
	check                                   chan struct{}
	cancel                                  context.CancelFunc
	provider                                string
}

type credentialContinuationKey struct{}
type credentialReplayKey struct{}
type credentialSenderKey struct{}

type credentialSender struct {
	openID string
	actor  sessioncredential.FeishuActor
}

func withCredentialSender(ctx context.Context, openID, tenantKey, userID, unionID string) context.Context {
	return context.WithValue(ctx, credentialSenderKey{}, credentialSender{openID, sessioncredential.FeishuActor{
		TenantKey: strings.TrimSpace(tenantKey), UserID: strings.TrimSpace(userID), UnionID: strings.TrimSpace(unionID),
	}})
}

type credentialContinuation struct {
	context context.Context
	resume  func(context.Context) error
}

func (b *Bridge) withCredentialContinuation(ctx context.Context, chatID, root string, resume func(context.Context) error) context.Context {
	if bridgeExtensions.snapshot().SessionCredential == nil {
		return ctx
	}
	return context.WithValue(ctx, credentialContinuationKey{}, credentialContinuation{
		context: ctx,
		resume: func(ctx context.Context) error {
			chat, err := b.db.FindByRootMsg(b.currentBridgeId(), root)
			if err != nil {
				return err
			}
			if chat != nil {
				return errors.New("conversation binding has changed")
			}
			policy, err := b.storedSessionPolicy(chatID)
			if err != nil || !policy.isolationRequired {
				return errors.New("conversation isolation policy has changed")
			}
			return resume(withGroupLaunchPolicy(ctx, policy.settings, true, false))
		},
	})
}

func (b *Bridge) routeSessionCredential(ctx context.Context, p *pendingCredentialAdmission, action sessioncredential.Action) sessioncredential.Result {
	if !p.actor.Identified() {
		return sessioncredential.Result{State: sessioncredential.AccountError}
	}
	callCtx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	reply, err := b.hub.Route(callCtx, p.target, sessioncredential.Route, sessioncredential.Request{
		Identity: p.identity, Actor: p.actor, Action: action, Provider: p.provider,
	})
	var result sessioncredential.Result
	if err != nil || reply == nil || json.Unmarshal(reply.Payload, &result) != nil || result.State == "" {
		return sessioncredential.Result{State: sessioncredential.Unavailable}
	}
	return result
}

func (b *Bridge) admitIsolatedFeishuSession(ctx context.Context, root, chat, openID, target string, identity principal.ExecutionIdentity) (bool, error) {
	options := bridgeExtensions.snapshot().SessionCredential
	if options == nil {
		return true, nil
	}
	if replay, ok := ctx.Value(credentialReplayKey{}).(*pendingCredentialAdmission); ok {
		if replay.target != target || replay.openID != openID || replay.identity.EffectivePrincipalID != identity.EffectivePrincipalID {
			return false, errors.New("conversation execution identity has changed")
		}
		// The completing operation just verified the exact Runtime and principal.
		return true, nil
	}
	pending := &b.credentialAdmissions
	pending.mu.Lock()
	previous := pending.byRoot[root]
	if previous != nil && time.Now().Before(previous.expires) {
		pending.mu.Unlock()
		return false, nil
	}
	pending.mu.Unlock()
	continuation, ok := ctx.Value(credentialContinuationKey{}).(credentialContinuation)
	if !ok || openID == "" || options.Render == nil {
		return false, errors.New("isolated session credential continuation unavailable")
	}
	deadline := time.Now().Add(credentialAdmissionLifetime)
	pendingContext, stop := context.WithDeadline(context.WithoutCancel(continuation.context), deadline)
	p := &pendingCredentialAdmission{
		id: uuid.NewString(), root: root, chat: chat, openID: openID, target: target, identity: identity,
		original: pendingContext, resume: continuation.resume, expires: deadline, cancel: stop,
	}
	if sender, ok := ctx.Value(credentialSenderKey{}).(credentialSender); ok && sender.openID == openID {
		p.actor = sender.actor
	}
	result := b.routeSessionCredential(ctx, p, sessioncredential.Probe)
	if result.State == sessioncredential.Ready {
		stop()
		return true, nil
	}
	p.provider = result.Provider
	content := options.Render(CredentialCardView{Locale: string(b.managementLocale(openID)), ID: p.id, State: result.State, Provider: p.provider})
	message, _, err := b.sendInteractiveToBinding(ctx, &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: root, ChatID: chat, OpenId: openID}, content, "session credential card")
	if err != nil || message == "" {
		stop()
		return false, errors.New("could not deliver isolated session credential card")
	}
	p.message = message
	pending.mu.Lock()
	if pending.byRoot == nil {
		pending.byRoot = make(map[string]*pendingCredentialAdmission)
	}
	pending.byRoot[root] = p
	pending.mu.Unlock()
	context.AfterFunc(pendingContext, func() { b.removeCredentialAdmission(p) })
	return false, nil
}

func (b *Bridge) removeCredentialAdmission(p *pendingCredentialAdmission) bool {
	admissions := &b.credentialAdmissions
	admissions.mu.Lock()
	defer admissions.mu.Unlock()
	if admissions.byRoot[p.root] != p {
		return false
	}
	delete(admissions.byRoot, p.root)
	return true
}

func (b *Bridge) stopCredentialAdmissions() {
	admissions := &b.credentialAdmissions
	admissions.mu.Lock()
	defer admissions.mu.Unlock()
	for _, p := range admissions.byRoot {
		p.cancel()
	}
	admissions.byRoot = nil
}

func (b *Bridge) handleCredentialAdmissionAction(event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool) {
	request := event.Event
	id, _ := request.Action.Value["session_credential_id"].(string)
	options := bridgeExtensions.snapshot().SessionCredential
	if id == "" || options == nil || options.Notice == nil {
		return nil, false
	}
	openID := ""
	if request.Operator != nil {
		openID = request.Operator.OpenID
	}
	locale := string(b.managementLocale(openID))
	notice := func(level, key string) (*callback.CardActionTriggerResponse, bool) {
		return cardToast(level, options.Notice(locale, key)), true
	}
	action, _ := request.Action.Value["session_credential_action"].(string)
	var operation sessioncredential.Action
	switch action {
	case "sign_in":
		operation = sessioncredential.Begin
	case "check":
		operation = sessioncredential.Probe
	case "reauth":
		operation = sessioncredential.Reauth
	default:
		return notice("warning", "expired")
	}
	if request.Context == nil || request.Context.OpenMessageID == "" || request.Token == "" {
		return notice("warning", "expired")
	}
	admissions := &b.credentialAdmissions
	admissions.mu.Lock()
	var pending *pendingCredentialAdmission
	for _, p := range admissions.byRoot {
		if p.id == id && p.message == request.Context.OpenMessageID {
			pending = p
			break
		}
	}
	if pending == nil || !time.Now().Before(pending.expires) {
		admissions.mu.Unlock()
		return notice("warning", "expired")
	}
	// A direct route can use a synthetic chat key. The exact card message ID
	// plus original operator gates delivery for both native and direct routes.
	if openID == "" || openID != pending.openID {
		admissions.mu.Unlock()
		return notice("error", "unauthorized")
	}
	if pending.check != nil {
		if operation == sessioncredential.Probe {
			select {
			case pending.check <- struct{}{}:
			default:
			}
		}
		admissions.mu.Unlock()
		return notice("info", "processing")
	}
	check := make(chan struct{}, 1)
	pending.check = check
	admissions.mu.Unlock()
	go b.workCredentialAdmission(options, pending, operation, request.Token, locale, check)
	if operation == sessioncredential.Probe {
		return notice("info", "checking")
	}
	return notice("info", "starting")
}

func (b *Bridge) workCredentialAdmission(options *CredentialAdmissionOptions, p *pendingCredentialAdmission, action sessioncredential.Action, token, locale string, check <-chan struct{}) {
	defer func() {
		b.credentialAdmissions.mu.Lock()
		p.check = nil
		b.credentialAdmissions.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(p.original, credentialPollingLifetime)
	defer cancel()
	// Feishu processes a delayed update after accepting the callback response.
	select {
	case <-time.After(350 * time.Millisecond):
	case <-ctx.Done():
		return
	}
	update := func(result sessioncredential.Result) bool {
		if result.Provider != "" {
			p.provider = result.Provider
		}
		if err := b.updateCredentialAdmissionCard(ctx, options, p, token, locale, result); err != nil {
			log.Warn().Msg("bridge: could not update exclusive session credential card")
			return false
		}
		return true
	}
	result := b.routeSessionCredential(ctx, p, action)
	if result.State == sessioncredential.Pending && result.URL != "" {
		if !update(result) {
			return
		}
		for result.State == sessioncredential.Pending {
			timer := time.NewTimer(3 * time.Second)
			select {
			case <-timer.C:
			case <-check:
			case <-ctx.Done():
				timer.Stop()
				return
			}
			timer.Stop()
			result = b.routeSessionCredential(ctx, p, sessioncredential.Poll)
			// Retriable transport failures during polling leave the same challenge active.
			if result.State == sessioncredential.Unavailable && ctx.Err() == nil {
				result.State = sessioncredential.Pending
			}
		}
	}
	if result.State != sessioncredential.Ready {
		update(result)
		return
	}
	claimed, resumeErr := b.resumeCredentialAdmission(p)
	if !claimed {
		return
	}
	defer p.cancel()
	state := sessioncredential.Continuing
	binding, err := b.db.FindByRootMsg(b.currentBridgeId(), p.root)
	if resumeErr != nil || err != nil || binding == nil || binding.TargetID != p.target {
		state = sessioncredential.DeliveryError
		log.Warn().Msg("bridge: isolated session credential verified but original input did not resume")
	}
	update(sessioncredential.Result{State: state})
}

func (b *Bridge) resumeCredentialAdmission(p *pendingCredentialAdmission) (bool, error) {
	unlockGroup := b.lockRoute(groupSettingsRouteKey(b.currentBridgeId(), p.chat))
	defer unlockGroup()
	unlock := b.lockRoute(p.root)
	defer unlock()
	if p.original.Err() != nil || !b.removeCredentialAdmission(p) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.WithValue(p.original, credentialReplayKey{}, p), time.Minute)
	defer cancel()
	return true, p.resume(ctx)
}

func (b *Bridge) updateCredentialAdmissionCard(ctx context.Context, options *CredentialAdmissionOptions, p *pendingCredentialAdmission, token, locale string, result sessioncredential.Result) error {
	rendered := options.Render(CredentialCardView{Locale: locale, ID: p.id, State: result.State, URL: result.URL, Provider: p.provider})
	var card map[string]any
	if err := json.Unmarshal([]byte(rendered), &card); err != nil || card == nil {
		return errors.New("invalid session credential card")
	}
	card["open_ids"] = []string{p.openID}
	updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := b.client.Post(updateCtx, "/open-apis/interactive/v1/card/update", map[string]any{
		"token": token, "card": card,
	}, larkcore.AccessTokenTypeTenant)
	if err != nil || response == nil {
		return errors.New("exclusive card update unavailable")
	}
	var resultCode struct {
		Code *int `json:"code"`
	}
	if response.StatusCode != 200 || json.Unmarshal(response.RawBody, &resultCode) != nil || resultCode.Code == nil || *resultCode.Code != 0 {
		return fmt.Errorf("exclusive card update was rejected")
	}
	return nil
}
