package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTurnReplyMentionOpenIDsSelectsDistinctGroupTopicHumans(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.mentionTurnInitiator.Store(true)
	if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), "group-chat", WorkspaceKindTopicGroup); err != nil {
		t.Fatal(err)
	}
	chat := &LarkChat{BridgeId: b.currentBridgeId(), ChatID: "group-chat", RootMsgID: "root"}
	b.recordTurnSenders("root", "turn", []turnInputRef{
		{MessageID: "m1", OpenID: "ou-a"},
		{MessageID: "m2", OpenID: "ou-b"},
		{MessageID: "m3", OpenID: "ou-a"},
		{MessageID: "m4"},
	})
	if got := b.turnReplyMentionOpenIDs(chat, "root", "turn"); strings.Join(got, ",") != "ou-a,ou-b" {
		t.Fatalf("mentions = %#v", got)
	}

	b.mentionTurnInitiator.Store(false)
	if got := b.turnReplyMentionOpenIDs(chat, "root", "turn"); len(got) != 0 {
		t.Fatalf("disabled mentions = %#v", got)
	}
	b.mentionTurnInitiator.Store(true)

	privateChat := *chat
	privateChat.ChatID = "private-chat"
	if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), privateChat.ChatID, WorkspaceKindPrivate); err != nil {
		t.Fatal(err)
	}
	if got := b.turnReplyMentionOpenIDs(&privateChat, "root", "turn"); len(got) != 0 {
		t.Fatalf("private mentions = %#v", got)
	}

	directChat := *chat
	directChat.goalReplyTarget = &directReplyTarget{Kind: botSessionReplyGroup, ChatID: "group-chat"}
	if got := b.turnReplyMentionOpenIDs(&directChat, "root", "turn"); len(got) != 0 {
		t.Fatalf("direct/singleton mentions = %#v", got)
	}

	b.recordTurnSenders("root", "automated", compactTurnInputRefs("", "watch-message"))
	if got := b.turnReplyMentionOpenIDs(chat, "root", "automated"); len(got) != 0 {
		t.Fatalf("automation mentions = %#v", got)
	}
}

func TestReplyMentionRenderersUseNativeMentionsWithoutChangingAnswer(t *testing.T) {
	card := renderAgentReplyCardWithMentions("原始回答", []string{"ou-a", "ou-b", "ou-a"})
	requireCardMentions(t, card, "ou-a", "ou-b")
	if !strings.Contains(card, "原始回答") {
		t.Fatalf("card = %s", card)
	}
	post := renderAgentReplyPostWithMentions("原始回答", []string{"ou-a", "ou-b"})
	var doc postDoc
	if err := json.Unmarshal([]byte(post), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.ZhCN.Content) < 2 || len(doc.ZhCN.Content[0]) != 3 || doc.ZhCN.Content[0][0].Tag != "at" || doc.ZhCN.Content[0][0].UserID != "ou-a" || doc.ZhCN.Content[0][2].UserID != "ou-b" {
		t.Fatalf("post mentions = %+v", doc.ZhCN.Content)
	}
}

func TestCardKitReplyMentionIsInInitialActivityAndSurvivesUpdates(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-mention"}
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-mention", nil
		},
	}
	backend.SetMentionTargets([]string{"ou-a", "ou-b", "ou-a"})
	handle, err := backend.CreateActivity(context.Background(), renderTurnActivity(toolActivityCardMeta{Status: "running", Placeholder: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(api.createdData) != 1 {
		t.Fatalf("initial CardKit data = %v", api.createdData)
	}
	requireCardMentions(t, api.createdData[0], "ou-a", "ou-b")
	if err := backend.UpdateActivity(context.Background(), handle, renderTurnActivity(toolActivityCardMeta{Status: "running"})); err != nil {
		t.Fatal(err)
	}
	if err := backend.Update(context.Background(), handle, "原始回答"); err != nil {
		t.Fatal(err)
	}
	if len(api.inserts) != 1 || strings.Contains(api.inserts[0].Elements, "ou-a") || !strings.Contains(api.inserts[0].Elements, "原始回答") {
		t.Fatalf("answer insert = %+v", api.inserts)
	}
}

func TestCardKitReplyMentionAppearsOnlyOnFirstSplitCardAndLogicalOutputStaysRaw(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-split"}
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-split", nil
		},
	}
	backend.SetMentionTargets([]string{"ou-a"})
	answer := strings.Repeat("这是一行较长的回答。\n", 1000)
	var logical string
	sender := newStreamingReplySender(0, backend, func(text string) { logical = text })
	sender.enqueue(replyCommandAdd, answer)
	sender.Flush()
	sender.Close()
	if len(api.createdData) < 2 {
		t.Fatalf("created cards = %d, want split reply", len(api.createdData))
	}
	requireCardMentions(t, api.createdData[0], "ou-a")
	for i, raw := range api.createdData[1:] {
		if strings.Contains(raw, "ou-a") {
			t.Fatalf("split card %d repeats mention: %s", i+1, raw)
		}
	}
	if logical != strings.TrimRight(answer, "\n") {
		t.Fatalf("logical output changed: len=%d want=%d", len(logical), len(strings.TrimRight(answer, "\n")))
	}
}

func TestLegacyReplyMentionPersistsAcrossPatch(t *testing.T) {
	api := &fakeCardKitReplyAPI{createErr: errors.New("cardkit unavailable")}
	var created, patched string
	backend := &bindingReplyCardBackend{
		cardKit: api,
		createLegacyWithMentions: func(_ context.Context, text string, openIDs []string) (string, error) {
			created = renderAgentReplyCardWithMentions(text, openIDs)
			return "legacy-message", nil
		},
		patchLegacyWithMentions: func(_ context.Context, _ string, text string, openIDs []string) error {
			patched = renderAgentReplyCardWithMentions(text, openIDs)
			return nil
		},
	}
	backend.SetMentionTargets([]string{"ou-a"})
	handle, err := backend.Create(context.Background(), "部分回答")
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Finish(context.Background(), handle, "完整回答"); err != nil {
		t.Fatal(err)
	}
	for label, raw := range map[string]string{"created": created, "patched": patched} {
		t.Run(label, func(t *testing.T) { requireCardMentions(t, raw, "ou-a") })
	}
}

func TestToolActivityMentionSurvivesCompactFallback(t *testing.T) {
	raw := renderToolActivityCardWithMentions(
		toolActivityCardMeta{Status: "running"},
		strings.Repeat("进度。", maxFeishuCardBytes),
		[]string{"ou-a"},
	)
	if len(raw) > maxFeishuCardBytes {
		t.Fatalf("compact card size = %d", len(raw))
	}
	requireCardMentions(t, raw, "ou-a")
}

func TestReplyMentionStateCanBeReadByAggregatorTimer(t *testing.T) {
	state := &replyMentionState{}
	if !state.set("turn", []string{"ou-a"}) {
		t.Fatal("first turn was not accepted")
	}
	done := make(chan []string, 1)
	time.AfterFunc(time.Millisecond, func() { done <- state.current() })
	if got := <-done; len(got) != 1 || got[0] != "ou-a" {
		t.Fatalf("timer read = %#v", got)
	}
	turnID, claimed := state.claim()
	if turnID != "turn" || len(claimed) != 1 || claimed[0] != "ou-a" {
		t.Fatalf("first claim = %q %#v", turnID, claimed)
	}
	if _, duplicate := state.claim(); len(duplicate) != 0 {
		t.Fatalf("duplicate claim = %#v", duplicate)
	}
	state.release(turnID)
	if _, retried := state.claim(); len(retried) != 1 || retried[0] != "ou-a" {
		t.Fatalf("released claim = %#v", retried)
	}
}

func requireCardMentions(t *testing.T, raw string, openIDs ...string) {
	t.Helper()
	var doc cardDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode card: %v: %s", err, raw)
	}
	element, ok := findCardElementByID(doc.Body.Elements, turnInitiatorMentionElementID)
	if !ok {
		t.Fatalf("card lacks mention element: %s", raw)
	}
	for _, openID := range openIDs {
		mention := `<at id="` + openID + `"></at>`
		if strings.Count(element.Content, mention) != 1 {
			t.Fatalf("mention %q count = %d in %q", openID, strings.Count(element.Content, mention), element.Content)
		}
	}
}
