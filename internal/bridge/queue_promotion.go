package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

const (
	queuePromotionAction = "queue_promote"
	queuePromotionTTL    = 2 * time.Hour
)

type queuePromotion struct {
	chat         LarkChat
	receipt      submitReceipt
	targetTurnID string
	sender       string
	inputs       []turnInputRef
	messageID    string
	submittedAt  time.Time
	busy         bool
}

type queuePromotionState struct {
	mu     sync.Mutex
	offers map[string]*queuePromotion
}

func (b *Bridge) offerQueuePromotion(chat *LarkChat, receipt submitReceipt, targetTurnID string, inputs []turnInputRef) {
	// Group topic replies need a stable source to keep the receipt beside the
	// original message and to validate the callback's chat and active binding.
	if b.client == nil || b.db == nil || chat.ChatID == "" || !strings.HasPrefix(chat.RootMsgID, "om_") ||
		receipt.SessionID == "" || receipt.LaunchID == "" || receipt.TurnID == "" || len(inputs) == 0 {
		return
	}
	sender := inputs[0].OpenID
	for _, input := range inputs {
		if sender == "" || input.OpenID != sender || input.MessageID == "" {
			return
		}
	}
	token := uuid.NewString()
	entry := queuePromotion{
		chat: *chat, receipt: receipt, targetTurnID: targetTurnID, sender: sender,
		inputs: append([]turnInputRef(nil), inputs...), submittedAt: time.Now(),
	}
	locale := b.managementLocale(sender)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		card := renderQueuePromotion(locale, "queue.promotion.pending", token)
		messageID, _, err := b.sendInteractiveToThreadWithUUID(ctx, inputs[0].MessageID, card, "queue receipt", receipt.TurnID)
		if err != nil {
			log.Warn().Err(err).Str("turn_id", receipt.TurnID).Msg("bridge: send queue receipt")
			return
		}
		if messageID == "" {
			return
		}
		entry.messageID = messageID
		b.queuePromotions.mu.Lock()
		defer b.queuePromotions.mu.Unlock()
		if b.queuePromotions.offers == nil {
			b.queuePromotions.offers = make(map[string]*queuePromotion)
		}
		for key, offer := range b.queuePromotions.offers {
			if time.Since(offer.submittedAt) >= queuePromotionTTL {
				delete(b.queuePromotions.offers, key)
			}
		}
		b.queuePromotions.offers[token] = &entry
	}()
}

func renderQueuePromotion(locale i18n.Locale, key, token string) string {
	locales := []i18n.Locale{locale}
	elements := []cardElement{managementContentElement(sessionCopy(locales, key), cardElement{Tag: "markdown"})}
	if token != "" {
		elements = append(elements, managementButtonRowElement([]managementButton{{
			Action: queuePromotionAction, Text: sessionCopy(locales, "queue.promotion.action"),
			Type: "primary", Values: map[string]string{"token": token},
		}}))
	}
	raw, _ := json.Marshal(newCardDoc(elements))
	return string(raw)
}

func (b *Bridge) handleQueuePromotion(event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool) {
	action := event.Event.Action
	if managementActionFromCallback(action) != queuePromotionAction {
		return nil, false
	}
	operator := ""
	if event.Event.Operator != nil {
		operator = event.Event.Operator.OpenID
	}
	locale := b.managementLocale(operator)
	toast := func(kind, key string) (*callback.CardActionTriggerResponse, bool) {
		return cardToast(kind, sessionCopy([]i18n.Locale{locale}, key)), true
	}
	if !b.busyInputSteers() {
		return toast("warning", "queue.promotion.unavailable")
	}
	token, _ := action.Value["token"].(string)
	b.queuePromotions.mu.Lock()
	entry := b.queuePromotions.offers[token]
	if entry == nil || time.Since(entry.submittedAt) >= queuePromotionTTL {
		b.queuePromotions.mu.Unlock()
		return toast("warning", "queue.promotion.unavailable")
	}
	if operator == "" || entry.sender != operator || event.Event.Context == nil ||
		event.Event.Context.OpenMessageID != entry.messageID || event.Event.Context.OpenChatID != entry.chat.ChatID {
		b.queuePromotions.mu.Unlock()
		return toast("error", "queue.promotion.unauthorized")
	}
	if entry.busy {
		b.queuePromotions.mu.Unlock()
		return toast("info", "queue.promotion.processing")
	}
	entry.busy = true
	b.queuePromotions.mu.Unlock()
	go b.finishQueuePromotion(token, *entry, locale)
	return toast("info", "queue.promotion.processing")
}

func (b *Bridge) finishQueuePromotion(token string, entry queuePromotion, locale i18n.Locale) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	key, retry := b.promoteQueueEntry(ctx, entry)
	cancel()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	button := ""
	if retry {
		button = token
	}
	if err := b.patchInteractiveMessage(ctx, entry.messageID, renderQueuePromotion(locale, key, button), "queue receipt"); err != nil {
		log.Warn().Err(err).Str("turn_id", entry.receipt.TurnID).Msg("bridge: update queue receipt")
	}
	b.queuePromotions.mu.Lock()
	if retry {
		if offer := b.queuePromotions.offers[token]; offer != nil {
			offer.busy = false
		}
	} else {
		delete(b.queuePromotions.offers, token)
	}
	b.queuePromotions.mu.Unlock()
}

func (b *Bridge) promoteQueueEntry(ctx context.Context, entry queuePromotion) (key string, retry bool) {
	chat := &entry.chat
	current, err := b.db.FindByRootMsgContext(ctx, chat.BridgeId, chat.RootMsgID)
	if err != nil {
		return "queue.promotion.queued", true
	}
	if current == nil || current.Status != ChatStatusActive || current.ChatID != chat.ChatID ||
		current.TargetID != chat.TargetID || current.SessionName != chat.SessionName {
		return "queue.promotion.unavailable", false
	}
	allowed, err := b.canUseBotInGroup(chat.ChatID, entry.sender)
	if err != nil {
		return "queue.promotion.queued", true
	}
	if !allowed || !b.busyInputSteers() {
		return "queue.promotion.unauthorized", false
	}
	routed, err := b.hub.Route(ctx, chat.TargetID, "SteerQueued", &pb.SteerQueuedRequest{
		Name: chat.SessionName, SessionId: entry.receipt.SessionID, LaunchId: entry.receipt.LaunchID,
		QueuedTurnId: entry.receipt.TurnID, ExpectedTurnId: entry.targetTurnID,
		LocalExpertAuthoring: b.isBotOwner(entry.sender),
	})
	var response pb.SubmitResponse
	if err != nil || routed == nil || json.Unmarshal(routed.Payload, &response) != nil {
		return "queue.promotion.unknown", false
	}
	switch response.GetDelivery() {
	case "steered":
		turnID := response.GetTurnId()
		b.recordTurnSenders(chat.RootMsgID, turnID, entry.inputs)
		links := make([]TurnLink, 0, len(entry.inputs))
		for _, input := range entry.inputs {
			links = append(links, TurnLink{
				BridgeID: chat.BridgeId, RootMsgID: chat.RootMsgID, TargetID: chat.TargetID,
				SessionName: chat.SessionName, SessionID: entry.receipt.SessionID, LaunchID: entry.receipt.LaunchID,
				MessageID: input.MessageID, OpenID: input.OpenID, TurnID: turnID, SubmittedAt: entry.submittedAt,
			})
		}
		if err := b.db.RecordTurnLinks(links); err != nil {
			log.Warn().Err(err).Str("turn_id", turnID).Msg("bridge: persist queue promotion receipt")
		}
		b.noteTurnSteered(chat.RootMsgID, turnID)
		b.refreshQueueNoticeAsync(chat.TargetID, chat.SessionName, chat.RootMsgID)
		return "queue.promotion.steered", false
	case "queued":
		if response.GetSteerFallbackReason() == "queue_order" {
			return "queue.promotion.order", true
		}
		return "queue.promotion.queued", true
	case "unavailable":
		return "queue.promotion.unavailable", false
	default:
		b.refreshQueueNoticeAsync(chat.TargetID, chat.SessionName, chat.RootMsgID)
		return "queue.promotion.unknown", false
	}
}
