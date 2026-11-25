package bridge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/turnresult"
)

func TestReplyDeliveryReceiptTracksPrimaryContinuationAndFailure(t *testing.T) {
	chat := &LarkChat{BridgeId: "bot-1", ChatID: "oc_1", RootMsgID: "om_root", TargetID: "local", SessionName: "s1"}

	// Single primary card succeeded.
	d := buildFeishuDelivery("bot-1", chat, turnresult.ExecutionCompleted, "ok", false, false, replyDeliveryReceipt{
		Cards: []replyDeliveryCard{
			{Kind: "primary", MessageID: "om_1", Finished: true},
		},
	})
	if d.Status != turnresult.DeliveryDelivered || len(d.Messages) != 1 || d.Messages[0].Kind != turnresult.MessageKindPrimary || d.Messages[0].MessageID != "om_1" {
		t.Fatalf("primary delivery = %+v", d)
	}

	// Split cards where second card did not finish -> partial, only confirmed card listed.
	d = buildFeishuDelivery("bot-1", chat, turnresult.ExecutionCompleted, "long", false, false, replyDeliveryReceipt{
		Cards: []replyDeliveryCard{
			{Kind: "primary", MessageID: "om_1", Finished: true},
			{Kind: "continuation", MessageID: "om_2", Finished: false},
		},
		FailedAttempts: 1,
	})
	if d.Status != turnresult.DeliveryPartial || len(d.Messages) != 1 || d.Messages[0].MessageID != "om_1" {
		t.Fatalf("partial delivery = %+v", d)
	}

	// Suppressed by visibility gate or superseded.
	d = buildFeishuDelivery("bot-1", chat, turnresult.ExecutionCompleted, "ok", false, true, replyDeliveryReceipt{})
	if d.Status != turnresult.DeliverySuppressed {
		t.Fatalf("suppressed delivery = %+v", d)
	}
}

func TestBridgeTurnResultRecorderEndToEndWithStore(t *testing.T) {
	store, err := turnresult.OpenStore(filepath.Join(t.TempDir(), "tr.db"), turnresult.DefaultPolicy())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	b := &Bridge{bridgeID: "bot-1"}
	b.SetTurnResultRecorder(store)

	chat := &LarkChat{BridgeId: "bot-1", ChatID: "oc_chat", RootMsgID: "om_root", TargetID: "local", SessionName: "s1"}
	b.recordTurnResultSubmission(chat, submitReceipt{SessionID: "sess-1", LaunchID: "l-1", TurnID: "turn-1"}, []turnInputRef{{MessageID: "om_in_1", OpenID: "ou_1"}})
	b.recordTurnResultCompletion(chat, "turn-1", agentui.SourceFeishu, agentui.TurnEvent{ID: "turn-1", State: "turn_done"}, "Hello world", false, false, replyDeliveryReceipt{
		Cards: []replyDeliveryCard{{Kind: "primary", MessageID: "om_reply_1", Finished: true}},
	}, true, time.Now().Unix(), nil)

	// Still not_ready while artifacts are pending.
	mid, err := store.LookupByFeishuMessage("bot-1", "om_in_1", time.Now())
	if err != nil || mid.Status != turnresult.StatusNotReady {
		t.Fatalf("mid lookup = %+v, err=%v; want not_ready", mid, err)
	}

	// Artifact finishes -> ready with both primary and attachment message IDs.
	b.recordTurnResultArtifacts("bot-1", "turn-1", []turnresult.DeliveryMessage{
		{Kind: turnresult.MessageKindAttachment, MessageID: "om_file_1", Name: "out.md"},
	}, 0, 1)

	got, err := store.LookupByFeishuMessage("bot-1", "om_in_1", time.Now())
	if err != nil || got.Status != turnresult.StatusReady || got.Result == nil {
		t.Fatalf("final lookup = %+v, err=%v; want ready", got, err)
	}
	if got.Result.Output.Content != "Hello world" || len(got.Result.Deliveries) != 1 || len(got.Result.Deliveries[0].Messages) != 2 {
		t.Fatalf("unexpected result: %+v", got.Result)
	}
	_ = context.Background()
}
