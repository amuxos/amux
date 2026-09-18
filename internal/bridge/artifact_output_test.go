package bridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestArtifactDispatchDoesNotBlockAndFreezesDeliveryIdentity(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	h := &materialTestHub{readData: []byte("result")}
	b.hub = h
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "singleton:" + b.currentBridgeId(), TargetID: "target", SessionName: "original"}
	bs := BotSession{BridgeId: chat.BridgeId, Enabled: true, TargetID: "target", SessionName: "original", LastReplyKind: botSessionReplyGroup, LastReplyChatID: "original-group"}
	if err := b.db.SetBotSession(bs); err != nil {
		t.Fatal(err)
	}
	original := *chat
	canonical, err := agentstream.MarshalEvent(agentstream.Event{
		SessionID: "sid", Origin: agentstream.Origin{LaunchID: "launch"},
		Cursor: agentstream.Cursor{Epoch: "epoch", Seq: 1}, At: time.Now(),
		Turn: &agentstream.TurnRef{ID: "turn", Seq: 1},
		Body: agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	var uploads, sends atomic.Int32
	b.artifactUpload = func(ctx context.Context, _ string, _ bool, _ []byte) (string, string, error) {
		if uploads.Add(1) == 1 {
			context.AfterFunc(ctx, func() { close(finished) })
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 90*time.Second {
			t.Error("worker missing bounded timeout")
		}
		return "file", "key", nil
	}
	b.artifactSend = func(ctx context.Context, got *LarkChat, _, _, _ string) error {
		sends.Add(1)
		if *got != original {
			t.Errorf("delivery followed mutable binding: %+v", got)
		}
		destination, singleton, err := b.artifactDirectTarget(ctx, got)
		if err != nil || !singleton || destination == nil || destination.ChatID != "original-group" {
			t.Errorf("delivery followed mutable singleton destination: %+v, %v", destination, err)
		}
		return nil
	}
	// Block before any identity parsing/read. Dispatch must still return; the
	// stream can then reuse its binding and canonical-event buffer safely.
	unlock := b.lockRoute("artifacts:" + chat.BridgeId + ":turn")
	var once sync.Once
	release := func() { once.Do(unlock) }
	t.Cleanup(func() {
		release()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("artifact worker did not finish")
		}
	})
	returned := make(chan struct{})
	go func() {
		for range 5 {
			b.dispatchArtifactDelivery(chat, "turn", "[report](amux-artifact:report.txt)", canonical)
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("artifact delivery blocked the stream")
	}
	chat.RootMsgID, chat.TargetID, chat.SessionName = "next-root", "next-target", "next-session"
	bs.LastReplyChatID = "next-group"
	if err := b.db.SetBotSession(bs); err != nil {
		t.Fatal(err)
	}
	clear(canonical)
	release()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("artifact delivery did not complete")
	}
	if uploads.Load() != 1 || sends.Load() != 1 || len(h.reads) != 1 {
		t.Fatalf("duplicate delivery: uploads=%d sends=%d reads=%d", uploads.Load(), sends.Load(), len(h.reads))
	}
	if h.reads[0].SessionId != "sid" || h.reads[0].LaunchId != "launch" || h.reads[0].Name != "original" {
		t.Fatalf("execution identity changed: %+v", h.reads[0])
	}
}

func TestArtifactLinksOnlyExplicitOutboxReferences(t *testing.T) {
	refs, err := artifactRefs("[local](/etc/passwd) [report](amux-artifact:报告%20v1.csv) ![plot](amux-artifact:plot.png) [again](amux-artifact:plot.png)")
	if err != nil || len(refs) != 2 || refs[0].name != "报告 v1.csv" || !refs[1].image {
		t.Fatalf("%+v %v", refs, err)
	}
	for _, bad := range []string{"../secret", "%2Fetc%2Fpasswd", "a%5Cb", "a/../../secret"} {
		if _, err := artifactRefs("[x](amux-artifact:" + bad + ")"); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	rendered := renderArtifactLinks("[report](amux-artifact:report.csv)")
	if strings.Contains(rendered, "amux-artifact:") || !strings.Contains(rendered, "report") {
		t.Fatal(rendered)
	}
}

func TestArtifactDeliveryPersistsUploadAndRetriesSameMessage(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	h := &materialTestHub{readData: []byte("result")}
	b.hub = h
	chat := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "root", TargetID: "target", SessionName: "s1"}
	err := b.db.RecordTurnLinks([]TurnLink{{BridgeID: chat.BridgeId, RootMsgID: "root", MessageID: "input", TargetID: "target", SessionName: "s1", SessionID: "sid", LaunchID: "launch", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	var notices []string
	b.threadNotice = func(_ context.Context, _ string, text string) error { notices = append(notices, text); return nil }
	uploads, sends := 0, 0
	var uuids []string
	b.artifactUpload = func(_ context.Context, name string, image bool, data []byte) (string, string, error) {
		uploads++
		if name != "report.txt" || string(data) != "result" {
			t.Fatal("bad upload")
		}
		return "file", "key", nil
	}
	b.artifactSend = func(_ context.Context, got *LarkChat, kind, key, id string) error {
		sends++
		uuids = append(uuids, id)
		if got.RootMsgID != "root" || kind != "file" || key != "key" {
			t.Fatal("wrong destination")
		}
		if sends == 1 {
			return errors.New("ambiguous send")
		}
		return nil
	}
	for i := 0; i < 3; i++ {
		b.deliverSessionArtifacts(context.Background(), chat, "turn", "[report](amux-artifact:report.txt)", nil)
	}
	if uploads != 1 || sends != 2 || len(h.reads) != 1 || len(notices) != 1 || uuids[0] != uuids[1] {
		t.Fatalf("uploads=%d sends=%d reads=%d notices=%v uuids=%v", uploads, sends, len(h.reads), notices, uuids)
	}
	if h.reads[0].SessionId != "sid" || h.reads[0].LaunchId != "launch" {
		t.Fatal("artifact not bound to original execution")
	}
}

func TestArtifactUploadDetectsImageBehindOrdinaryLink(t *testing.T) {
	b := &Bridge{}
	seenImage := false
	b.artifactUpload = func(_ context.Context, _ string, image bool, _ []byte) (string, string, error) {
		seenImage = image
		return "image", "key", nil
	}
	if _, _, err := b.uploadArtifact(context.Background(), artifactRef{name: "chart.png"}, bridgeTestPNG(t)); err != nil || !seenImage {
		t.Fatal("ordinary PNG link was not an image", err)
	}
}

func TestArtifactUploadSendsLargeImageAsFile(t *testing.T) {
	b := &Bridge{}
	seenImage := true
	b.artifactUpload = func(_ context.Context, _ string, image bool, _ []byte) (string, string, error) {
		seenImage = image
		return "file", "key", nil
	}
	data := append(bridgeTestPNG(t), make([]byte, maxFeishuImageBytes+1)...)
	if _, _, err := b.uploadArtifact(context.Background(), artifactRef{name: "chart.png"}, data); err != nil || seenImage {
		t.Fatal("large image was not downgraded to a file", err)
	}
}

func TestArtifactInstructionsStayOutOfQuestionTitle(t *testing.T) {
	if got := toolActivityQuestionTitle("请生成图表" + artifactInstructions); got != "请生成图表" {
		t.Fatal(got)
	}
}
