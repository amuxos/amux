package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

type personalScheduleBinding struct {
	OpenID   string
	TargetID string
	Identity feishumsg.PersonalScheduleIdentity
}

func (b *Bridge) rememberPersonalScheduleLogin(p *pendingCredentialAdmission) {
	if b.db == nil || b.schedules == nil {
		return
	}
	binding := personalScheduleBinding{OpenID: p.openID, TargetID: p.target,
		Identity: feishumsg.PersonalScheduleIdentity{Execution: p.identity, Actor: p.actor, SourceOpenID: p.openID}}
	if !b.canManagePersonalSchedules(binding) {
		return
	}
	owner := binding.Identity.Execution.OriginActorPrincipalID
	// A new topic changes execution provenance but does not change the login.
	binding.Identity.Execution = principal.ExecutionIdentity{
		IdentityMode: principal.IdentityModeIdentified, Source: principal.SourceFeishu,
		OriginActorPrincipalID: owner, EffectivePrincipalID: owner, IsolationBackend: principal.BackendLinuxDAC,
	}
	payload, err := json.Marshal(binding.Identity)
	if err != nil {
		return
	}
	// A principal uses one originating Bridge and Runtime for the current
	// personal Bot subscription. The most recent verified login can move it.
	if _, err := b.db.db.Exec(`INSERT INTO feishu_personal_schedule_bindings
		(principal_id, bridge_id, open_id, target_id, identity) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(principal_id) DO UPDATE SET bridge_id=excluded.bridge_id,
		open_id=excluded.open_id, target_id=excluded.target_id, identity=excluded.identity`,
		owner, b.currentBridgeId(), p.openID, p.target, payload); err != nil {
		log.Warn().Msg("bridge: could not save personal schedule login")
	}
}

func (b *Bridge) canManagePersonalSchedules(binding personalScheduleBinding) bool {
	if b.resolveFeishuPrincipal == nil || binding.OpenID == "" || binding.TargetID == "" || !binding.Identity.Valid() {
		return false
	}
	resolved, err := b.resolveFeishuPrincipal(binding.OpenID)
	return err == nil && resolved == binding.Identity.Execution.OriginActorPrincipalID
}

func (b *Bridge) personalScheduleBindings(ctx context.Context) ([]personalScheduleBinding, error) {
	rows, err := b.db.db.QueryContext(ctx, `SELECT open_id,target_id,identity
		FROM feishu_personal_schedule_bindings WHERE bridge_id=?`, b.currentBridgeId())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []personalScheduleBinding
	for rows.Next() {
		var binding personalScheduleBinding
		var payload []byte
		if err := rows.Scan(&binding.OpenID, &binding.TargetID, &payload); err != nil {
			return nil, err
		}
		if json.Unmarshal(payload, &binding.Identity) == nil {
			bindings = append(bindings, binding)
		}
	}
	return bindings, rows.Err()
}

func (b *Bridge) runPersonalSchedules(ctx context.Context) {
	if b.db == nil || b.schedules == nil || b.hub == nil || bridgeExtensions.snapshot().SessionCredential == nil {
		return
	}
	type subscription struct {
		binding personalScheduleBinding
		cancel  context.CancelFunc
	}
	active := map[string]subscription{}
	defer func() {
		for _, sub := range active {
			sub.cancel()
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		bindings, err := b.personalScheduleBindings(ctx)
		if err == nil {
			next := make(map[string]personalScheduleBinding, len(bindings))
			for _, binding := range bindings {
				if b.canManagePersonalSchedules(binding) {
					next[binding.Identity.Execution.OriginActorPrincipalID] = binding
				}
			}
			for id, sub := range active {
				if next[id] != sub.binding {
					sub.cancel()
					delete(active, id)
				}
			}
			for id, binding := range next {
				if _, ok := active[id]; !ok {
					subCtx, cancel := context.WithCancel(ctx)
					active[id] = subscription{binding: binding, cancel: cancel}
					go b.observePersonalSchedules(subCtx, binding)
				}
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (b *Bridge) observePersonalSchedules(ctx context.Context, binding personalScheduleBinding) {
	for ctx.Err() == nil {
		err := b.hub.RouteStream(ctx, binding.TargetID, feishumsg.PersonalScheduleEventsRoute, binding.Identity, func(frame *pb.TunnelMsg) error {
			var event feishumsg.PersonalScheduleEvent
			if json.Unmarshal(frame.Payload, &event) == nil {
				if err := b.handlePersonalScheduleEvent(ctx, binding, event); err != nil {
					log.Debug().Msg("bridge: personal schedule card action could not be completed")
				}
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Debug().Str("target", binding.TargetID).Msg("bridge: personal schedule subscription unavailable")
		}
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

func (b *Bridge) personalScheduleBindingCurrent(ctx context.Context, binding personalScheduleBinding) bool {
	if !b.canManagePersonalSchedules(binding) {
		return false
	}
	var bridgeID, openID, target string
	var payload []byte
	err := b.db.db.QueryRowContext(ctx, `SELECT bridge_id,open_id,target_id,identity
		FROM feishu_personal_schedule_bindings WHERE principal_id=?`, binding.Identity.Execution.OriginActorPrincipalID).
		Scan(&bridgeID, &openID, &target, &payload)
	var identity feishumsg.PersonalScheduleIdentity
	return err == nil && bridgeID == b.currentBridgeId() && openID == binding.OpenID && target == binding.TargetID &&
		json.Unmarshal(payload, &identity) == nil && identity == binding.Identity
}

func (b *Bridge) handlePersonalScheduleEvent(ctx context.Context, binding personalScheduleBinding, event feishumsg.PersonalScheduleEvent) error {
	if event.EventID == "" || event.MessageID == "" || event.ChatID == "" ||
		event.Account.Profile == "" || event.Account.OpenID == "" || !b.personalScheduleBindingCurrent(ctx, binding) {
		return nil
	}
	scope := "personal-schedules:" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join([]string{
		b.currentBridgeId(), binding.Identity.Execution.OriginActorPrincipalID, event.Account.OpenID,
	}, "\x00"))).String()
	if event.Token != "" && !b.db.isPrivateManagementCardContext(ctx, scope, event.ChatID, event.MessageID) {
		return nil
	}
	claim := "personal-schedules:" + binding.Identity.Execution.OriginActorPrincipalID + ":" + event.EventID
	if claimed, err := b.db.ConsumeManagementAction(claim); err != nil || !claimed {
		return err
	}
	locale := b.managementLocale(binding.OpenID)
	write := func(card, text, token string) error {
		callCtx, cancel := context.WithTimeout(ctx, 22*time.Second)
		defer cancel()
		msg, err := b.hub.Route(callCtx, binding.TargetID, feishumsg.PersonalScheduleCardRoute, feishumsg.PersonalScheduleCard{
			Binding: binding.Identity, Account: event.Account, Card: json.RawMessage(b.managementCardForRecipient(binding.OpenID, card)),
			Text: text, Token: token, UUID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(claim)).String(),
		})
		if err != nil || token != "" || text != "" {
			return err
		}
		var receipt feishumsg.PersonalResult
		if msg == nil || json.Unmarshal(msg.Payload, &receipt) != nil || receipt.MessageID == "" || receipt.ChatID != event.ChatID {
			return errors.New("personal schedule card delivery receipt unavailable")
		}
		return b.db.recordPrivateManagementCard(scope, receipt.ChatID, receipt.MessageID)
	}
	notice := func() error {
		return write("", managementText(scheduleText("unavailable")).For(locale), "")
	}
	if event.Token == "" {
		entries, err := b.schedules.List(ctx, b.currentBridgeId(), binding.OpenID)
		if err != nil {
			return notice()
		}
		return write(renderSchedules(entries, "all", 0), "", "")
	}
	action := &callback.CallBackAction{Value: event.Action}
	op := managementCallbackString(action, managementActionKey)
	switch op {
	case "schedule.home", "schedule.list", "schedule.prune_completed", "schedule.detail", "schedule.result",
		"schedule.run_detail", "schedule.trigger", "schedule.pause", "schedule.resume", "schedule.delete",
		"schedule.delivery_target", "schedule.delivery_policy", "schedule.off", "schedule.on", "schedule.ack", "schedule.snooze":
	default:
		return nil
	}
	response, _, err := b.scheduleCardActionWithDelivery(ctx, &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: binding.OpenID}, Action: action},
	}, op, func(card string) error { return write(card, "", event.Token) })
	if err == nil && response != nil && response.Toast != nil && response.Toast.Type == "error" {
		return notice()
	}
	return err
}
