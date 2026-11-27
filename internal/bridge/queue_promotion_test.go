package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/i18n"
)

func TestQueuePromotionCallbackRejectsDifferentUserOrCard(t *testing.T) {
	b := &Bridge{}
	b.steerForeground.Store(true)
	entry := &queuePromotion{
		chat: LarkChat{ChatID: "oc-group"}, messageID: "om-receipt",
		sender: "ou-member", submittedAt: time.Now(),
	}
	b.queuePromotions.offers = map[string]*queuePromotion{"offer": entry}
	for _, tt := range []struct{ sender, chat, message string }{
		{"ou-other", "oc-group", "om-receipt"},
		{"ou-member", "oc-other", "om-receipt"},
		{"ou-member", "oc-group", "om-other"},
	} {
		event := settingsCallback(queuePromotionAction, map[string]interface{}{"token": "offer"})
		event.Event.Operator.OpenID = tt.sender
		event.Event.Context.OpenChatID = tt.chat
		event.Event.Context.OpenMessageID = tt.message
		resp, err := b.handleCardAction(context.Background(), event)
		if err != nil || resp == nil || resp.Toast == nil || resp.Toast.Type != "error" || entry.busy {
			t.Fatalf("invalid callback %+v: response=%+v err=%v busy=%v", tt, resp, err, entry.busy)
		}
	}
	event := settingsCallback(queuePromotionAction, map[string]interface{}{"token": "offer"})
	event.Event.Operator.OpenID = entry.sender
	event.Event.Context.OpenChatID = entry.chat.ChatID
	event.Event.Context.OpenMessageID = entry.messageID
	entry.busy = true
	resp, _ := b.handleCardAction(context.Background(), event)
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "info" {
		t.Fatalf("in-flight callback response = %+v", resp)
	}
}

func TestQueuePromotionCardLocales(t *testing.T) {
	for _, tt := range []struct {
		locale i18n.Locale
		action string
	}{
		{i18n.Chinese, "转入当前任务"},
		{i18n.English, "Move into current task"},
		{i18n.Locale("unknown"), "Move into current task"},
	} {
		card := renderQueuePromotion(tt.locale, "queue.promotion.pending", "offer")
		if !strings.Contains(card, tt.action) || !strings.Contains(card, queuePromotionAction) {
			t.Fatalf("locale %q receipt should have the localized button: %s", tt.locale, card)
		}
		card = renderQueuePromotion(tt.locale, "queue.promotion.unknown", "")
		if strings.Contains(card, queuePromotionAction) || strings.Contains(card, "queue.promotion.unknown") {
			t.Fatalf("locale %q unknown result should remove the button: %s", tt.locale, card)
		}
	}
}

func TestCoalescedForegroundPreservesSenderAndOriginalTurn(t *testing.T) {
	hub := &fakeSubmitHub{}
	b := &Bridge{hub: hub, turnCoalescer: turnCoalescer{coalesceDelay: time.Hour}}
	b.steerForeground.Store(true)
	chat := &LarkChat{RootMsgID: "om-root", TargetID: "t", SessionName: "s"}
	ownTurn(b, chat.RootMsgID, "turn-a", "ou-a")
	ctx := context.Background()
	b.enqueueCoalescedInputForMessage(ctx, chat, "first user", "om-a", "ou-a", nil)
	b.enqueueCoalescedInputForMessage(ctx, chat, "other user", "om-b", "ou-b", nil)
	b.observeTurnFinished(chat.RootMsgID, "turn-a")
	ownTurn(b, chat.RootMsgID, "turn-b", "ou-a")
	b.enqueueCoalescedInputForMessage(ctx, chat, "on the next turn", "om-c", "ou-a", nil)

	pending := b.coalesced[chat.RootMsgID]
	t.Cleanup(func() { pending.timer.Stop() })
	b.flushCoalescedInput(chat.RootMsgID, pending)
	got := hub.snapshot()
	if len(got) != 3 || got[0].GetText() != "first user" || got[0].GetSteer() ||
		got[1].GetText() != "other user" || got[1].GetSteer() ||
		got[2].GetText() != "on the next turn" || got[2].GetSteerExpectedTurnId() != "turn-b" {
		t.Fatalf("submissions: %v", got)
	}
}
