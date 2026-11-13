package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
	lark "github.com/larksuite/oapi-sdk-go/v3"
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

	privateChat := &LarkChat{BridgeId: b.currentBridgeId(), ChatID: "private-chat", RootMsgID: "private-root"}
	if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), privateChat.ChatID, WorkspaceKindPrivate); err != nil {
		t.Fatal(err)
	}
	b.recordTurnSenders("private-root", "private-turn", compactTurnInputRefs("ou-private", "private-message"))
	if got := b.turnReplyMentionOpenIDs(privateChat, "private-root", "private-turn"); strings.Join(got, ",") != "ou-private" {
		t.Fatalf("private mentions = %#v", got)
	}

	directChat := *chat
	directChat.goalReplyTarget = &directReplyTarget{Kind: botSessionReplyGroup, ChatID: "group-chat"}
	if got := b.turnReplyMentionOpenIDs(&directChat, "root", "turn"); len(got) != 0 {
		t.Fatalf("direct/singleton mentions = %#v", got)
	}
	directDM := *chat
	directDM.goalReplyTarget = &directReplyTarget{Kind: botSessionReplyDM, OpenID: "ou-a"}
	if got := b.turnReplyMentionOpenIDs(&directDM, "root", "turn"); strings.Join(got, ",") != "ou-a" {
		t.Fatalf("direct dm mentions = %#v", got)
	}

	b.recordTurnSenders("root", "automated", compactTurnInputRefs("", "watch-message"))
	if got := b.turnReplyMentionOpenIDs(chat, "root", "automated"); len(got) != 0 {
		t.Fatalf("automation mentions = %#v", got)
	}
}

func TestSubtaskTurnReplyMentionsUseExactParentTurnRecipients(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.mentionTurnInitiator.Store(true)
	if err := b.db.EnsureWorkspaceKind(b.currentBridgeId(), "group-chat", WorkspaceKindTopicGroup); err != nil {
		t.Fatal(err)
	}
	if err := b.db.RecordTurnLinks([]TurnLink{
		{BridgeID: b.currentBridgeId(), RootMsgID: "root", MessageID: "parent-a", TurnID: "parent-turn", OpenID: "ou-origin"},
		{BridgeID: b.currentBridgeId(), RootMsgID: "root", MessageID: "parent-b", TurnID: "parent-turn", OpenID: "ou-origin"},
	}); err != nil {
		t.Fatal(err)
	}
	b.recordTurnSenders("root", "completion-turn", compactTurnInputRefs("ou-recent", "recent-message"))
	b.markSubtaskTurn("root", "completion-turn", "parent-turn")
	chat := &LarkChat{BridgeId: b.currentBridgeId(), ChatID: "group-chat", RootMsgID: "root"}
	if got := b.turnReplyMentionOpenIDs(chat, "root", "completion-turn"); strings.Join(got, ",") != "ou-origin" {
		t.Fatalf("subtask completion mentions = %#v, want exact parent-turn sender", got)
	}
	b.markSubtaskTurn("root", "missing-parent-completion", "missing-parent-turn")
	directDM := &LarkChat{BridgeId: b.currentBridgeId(), RootMsgID: "root", OpenId: "ou-recent",
		goalReplyTarget: &directReplyTarget{Kind: botSessionReplyDM, OpenID: "ou-recent"}}
	if got := b.turnReplyMentionOpenIDs(directDM, "root", "missing-parent-completion"); len(got) != 0 {
		t.Fatalf("missing parent receipts fell back to latest dm recipient: %#v", got)
	}
}

func TestSubtaskNoticeParentTurnID(t *testing.T) {
	text := "[后台任务完成] task_id=task-1 status=succeeded parent_turn_id=turn-parent\n结论: done"
	if got := subtaskNoticeParentTurnID(text); got != "turn-parent" {
		t.Fatalf("parent turn = %q", got)
	}
	if got := subtaskNoticeParentTurnID("[后台任务完成] task_id=task-1 status=succeeded"); got != "" {
		t.Fatalf("missing parent turn = %q", got)
	}
}

func TestSendTurnInitiatorMentionFinalCreatesMentionedDM(t *testing.T) {
	var query string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
		case "/open-apis/im/v1/messages":
			query = r.URL.RawQuery
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode message: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": "message-final"}})
		default:
			t.Errorf("unexpected lark API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	b := &Bridge{client: lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))}
	chat := &LarkChat{RootMsgID: "singleton:test", goalReplyTarget: &directReplyTarget{Kind: botSessionReplyDM, OpenID: "ou-user"}}
	messageID, err := b.sendTurnInitiatorMentionFinal(context.Background(), chat, "完整回答", []string{"ou-user"}, "stable-uuid")
	if err != nil || messageID != "message-final" {
		t.Fatalf("send final dm = %q, %v", messageID, err)
	}
	if query != "receive_id_type=open_id" || body["receive_id"] != "ou-user" || body["uuid"] != "stable-uuid" {
		t.Fatalf("dm request query=%q body=%+v", query, body)
	}
	content, _ := body["content"].(string)
	requireCardMentions(t, content, "ou-user")
	if !strings.Contains(content, "完整回答") {
		t.Fatalf("dm final card = %s", content)
	}
}

func TestReplyMentionRenderersUseNativeMentionsWithoutChangingAnswer(t *testing.T) {
	card := renderAgentReplyCardWithMentions("原始回答", []string{"ou-a", "ou-b", "ou-a"})
	requireCardMentions(t, card, "ou-a", "ou-b")
	if !strings.Contains(card, "原始回答") {
		t.Fatalf("card = %s", card)
	}
	decoded := decodeActivityCard(t, card)
	if !decoded.Config.WideScreenMode || decoded.Config.WidthMode != "fill" {
		t.Fatalf("final card config = %+v, want fill-width widescreen", decoded.Config)
	}
	if decoded.Body.Direction != "vertical" || decoded.Body.VerticalSpacing != "12px" || decoded.Body.Padding != "12px 16px 16px 16px" {
		t.Fatalf("final card body = %+v, want original turn-card layout", decoded.Body)
	}
	assertTurnCardChildOrder(t, card, cardKitReplyElementID, amuxBrandDividerID, amuxBrandFooterID)
	if _, ok := findCardElementByID(decoded.Body.Elements, cardKitAnswerHeadingID); ok {
		t.Fatalf("final card retained top status section: %s", card)
	}
	if _, ok := findCardElementByID(decoded.Body.Elements, cardKitAnswerDividerElementID); ok {
		t.Fatalf("final card retained divider above answer: %s", card)
	}
	if strings.Contains(card, `"tag":"collapsible_panel"`) {
		t.Fatalf("final card retained collapsible progress: %s", card)
	}
	footer, ok := findCardElementByID(decoded.Body.Elements, amuxBrandFooterID)
	wantMentions := `<at id="ou-a"></at> <at id="ou-b"></at>`
	wantChineseFooter := amuxBrandFooterMarkdown() + ` <font color='grey'>| 发送给:</font> ` + wantMentions
	wantEnglishFooter := amuxBrandFooterMarkdown() + ` <font color='grey'>| Sent to:</font> ` + wantMentions
	if !ok || footer.Content != wantChineseFooter || footer.I18nContent["zh_cn"] != wantChineseFooter || footer.I18nContent["en_us"] != wantEnglishFooter {
		t.Fatalf("final card footer = %+v, want Powered by before recipients", footer)
	}
	if _, ok := findCardElementByID(decoded.Body.Elements, cardKitActivityContentID); ok {
		t.Fatalf("final card unexpectedly retained progress activity: %s", card)
	}
	if quoted := extractInteractiveMessageText(&card); quoted != "原始回答" {
		t.Fatalf("quoted final card = %q, want answer without heading, mention or brand", quoted)
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

func TestReplyMentionCompletionSummaryIsCompact(t *testing.T) {
	card := renderAgentReplyCompletionCard(nil, false)
	for _, locale := range []i18n.Locale{i18n.Chinese, i18n.English} {
		summary := replyCompletionCardText(t, card, locale)
		if count := utf8.RuneCountInString(summary); count == 0 || count > 10 {
			t.Fatalf("completion summary %q for %s has %d runes, want 1..10", summary, locale, count)
		}
	}
}

func replyCompletionCardText(t *testing.T, raw string, locale i18n.Locale) string {
	t.Helper()
	var card cardDoc
	if err := json.Unmarshal([]byte(selectManagementCardLocale(raw, locale)), &card); err != nil {
		t.Fatalf("decode completion card: %v", err)
	}
	element, ok := findCardElementByID(card.Body.Elements, cardKitReplyElementID)
	if !ok {
		t.Fatalf("completion card lacks %s: %+v", cardKitReplyElementID, card.Body.Elements)
	}
	return strings.TrimSpace(element.Content)
}

func TestReplyMentionCompletionPreservesCollapsedActivity(t *testing.T) {
	activity := renderTurnActivity(toolActivityCardMeta{
		Status: "done",
		Activity: &agentui.ActivityView{
			Status:      "done",
			CurrentStep: "保留处理过程",
			Tools:       []agentui.ToolEvent{{ID: "tool-1", Title: "保留工具调用", Status: "completed"}},
		},
	})
	card := renderAgentReplyCompletionCard(&activity, false)
	if got := replyCompletionCardText(t, card, i18n.Chinese); got != "已完成" {
		t.Fatalf("completion = %q", got)
	}
	decoded := decodeActivityCard(t, card)
	if !decoded.Config.WideScreenMode || decoded.Config.WidthMode != "fill" {
		t.Fatalf("completion card config = %+v, want fill-width widescreen", decoded.Config)
	}
	if decoded.Body.Direction != "vertical" || decoded.Body.VerticalSpacing != "12px" || decoded.Body.Padding != "12px 16px 16px 16px" {
		t.Fatalf("completion card body = %+v, want original turn-card layout", decoded.Body)
	}
	assertTurnCardChildOrder(t, card, cardKitActivityContentID, cardKitAnswerDividerElementID,
		cardKitReplyElementID, amuxBrandDividerID, amuxBrandFooterID)
	if _, ok := findCardElementByID(decoded.Body.Elements, cardKitAnswerHeadingID); ok {
		t.Fatalf("completion card retained final-result heading: %s", card)
	}
	activityElement, ok := findCardElementByID(decoded.Body.Elements, cardKitActivityContentID)
	if !ok {
		t.Fatalf("completion card lost activity: %s", card)
	}
	execution, process := turnActivityLayout(t, activityElement)
	if execution.Expanded || process == nil || process.Expanded {
		t.Fatalf("completion activity execution=%+v process=%+v, want both collapsed", execution, process)
	}
	if !strings.Contains(card, "保留工具调用") || !strings.Contains(card, "保留处理过程") {
		t.Fatalf("completion card lost details: %s", card)
	}
}

func TestReplyMentionCompletionKeepsConversationSection(t *testing.T) {
	card := renderAgentReplyCompletionCard(nil, true)
	assertTurnCardChildOrder(t, card, cardKitReplyElementID, amuxBrandDividerID, conversationSectionID, amuxBrandFooterID)
	if plain := renderAgentReplyCompletionCard(nil, false); strings.Contains(plain, conversationSectionID) {
		t.Fatalf("completion card without extension gained a conversation section: %s", plain)
	}
}

func TestCardKitReplyMentionPublishesFinalCardThenCompactsProgress(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-activity"}
	sendCount := 0
	finalSendCount := 0
	var finalCard string
	var events []string
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			sendCount++
			return "message-activity", nil
		},
		sendMentionedFinal: func(_ context.Context, text string, openIDs []string, requestUUID string) (string, error) {
			finalSendCount++
			if len(api.finishes) != 1 || api.finishes[0].CardID != "card-activity" {
				t.Errorf("final card observed before progress card finish: %+v", api.finishes)
			}
			if requestUUID == "" {
				t.Error("final card request UUID is empty")
			}
			if text != "原始回答" {
				t.Errorf("final card text = %q", text)
			}
			finalCard = renderAgentReplyCardWithMentions(text, openIDs)
			events = append(events, "send-final")
			return "message-final", nil
		},
		compactMessage: func(_ context.Context, messageID, content string) error {
			events = append(events, "compact:"+messageID+":"+replyCompletionCardText(t, content, i18n.Chinese))
			return nil
		},
	}
	var logical string
	sender := newStreamingReplySender(0, backend, func(text string) { logical = text })
	handled, err := sender.UpdateActivity(renderTurnActivity(toolActivityCardMeta{Status: "running", Placeholder: true}))
	if err != nil || !handled {
		t.Fatalf("initial activity handled = %v, err = %v", handled, err)
	}
	sender.enqueue(replyCommandAdd, "原始回答")
	sender.Flush()
	if finalSendCount != 0 {
		t.Fatalf("flush published %d mentioned final cards before the Turn completed", finalSendCount)
	}
	sender.CompleteTurn([]string{"ou-a", "ou-b", "ou-a"})
	sender.Close()

	if len(api.createdData) != 1 || sendCount != 1 || finalSendCount != 1 {
		t.Fatalf("created CardKit data = %d, progress sends = %d, final sends = %d", len(api.createdData), sendCount, finalSendCount)
	}
	if strings.Contains(api.createdData[0], legacyTurnInitiatorMentionElementID) || strings.Contains(api.createdData[0], "ou-a") {
		t.Fatalf("activity card unexpectedly mentions initiator: %s", api.createdData[0])
	}
	if len(api.inserts) != 1 || api.inserts[0].CardID != "card-activity" {
		t.Fatalf("final answer inserts = %+v", api.inserts)
	}
	if strings.Contains(api.inserts[0].Elements, legacyTurnInitiatorMentionElementID) || strings.Contains(api.inserts[0].Elements, "ou-a") {
		t.Fatalf("answer insert unexpectedly mentions initiator: %s", api.inserts[0].Elements)
	}
	if !strings.Contains(api.inserts[0].Elements, "原始回答") {
		t.Fatalf("final answer insert = %s", api.inserts[0].Elements)
	}
	if len(api.finishes) != 1 || api.finishes[0].CardID != "card-activity" {
		t.Fatalf("finished cards = %+v, want shared activity/answer card", api.finishes)
	}
	if logical != "原始回答" {
		t.Fatalf("logical output = %q", logical)
	}
	requireCardMentions(t, finalCard, "ou-a", "ou-b")
	if !strings.Contains(finalCard, "原始回答") {
		t.Fatalf("final card = %s", finalCard)
	}
	if strings.Join(events, ",") != "send-final,compact:message-activity:已完成" {
		t.Fatalf("events = %#v, want final send before progress compression", events)
	}
}

func TestCardKitReplyMentionWaitsForAllSplitCardsAndCompactsEachOldMessage(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-split"}
	progressSendCount := 0
	type finalPart struct {
		text, card, requestUUID string
		mentions                []string
	}
	var finals []finalPart
	var compacted []string
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			progressSendCount++
			return "message-split-" + string(rune('0'+progressSendCount)), nil
		},
		sendMentionedFinal: func(_ context.Context, text string, openIDs []string, requestUUID string) (string, error) {
			if len(api.finishes) != len(api.createdData) {
				t.Errorf("final card observed after %d/%d cards finished", len(api.finishes), len(api.createdData))
			}
			finals = append(finals, finalPart{text: text, card: renderAgentReplyCardWithMentions(text, openIDs), requestUUID: requestUUID, mentions: openIDs})
			return "message-final-" + string(rune('0'+len(finals))), nil
		},
		compactMessage: func(_ context.Context, messageID, content string) error {
			compacted = append(compacted, messageID+":"+replyCompletionCardText(t, content, i18n.Chinese))
			return nil
		},
	}
	line := "这是一行较长的回答。"
	answer := strings.Repeat(line+"\n", 1000)
	var logical string
	sender := newStreamingReplySender(0, backend, func(text string) { logical = text })
	sender.enqueue(replyCommandAdd, answer)
	sender.Flush()
	sender.CompleteTurn([]string{"ou-a"})
	sender.Close()
	if len(api.createdData) < 2 {
		t.Fatalf("created cards = %d, want split reply", len(api.createdData))
	}
	for i, raw := range api.createdData {
		if strings.Contains(raw, "ou-a") {
			t.Fatalf("split answer card %d mentions before completion: %s", i, raw)
		}
	}
	if progressSendCount != len(api.createdData) || len(compacted) != progressSendCount {
		t.Fatalf("progress sends = %d, created cards = %d, compacted = %#v", progressSendCount, len(api.createdData), compacted)
	}
	for _, compact := range compacted {
		if !strings.HasSuffix(compact, ":已完成") {
			t.Fatalf("compacted cards = %#v", compacted)
		}
	}
	if len(finals) < 2 {
		t.Fatalf("final parts = %d, want the long answer split like streamed cards", len(finals))
	}
	lines := 0
	seenUUIDs := map[string]bool{}
	for i, part := range finals {
		if len(part.text) > streamingReplyCardSoftLimit {
			t.Fatalf("final part %d has %d bytes, above the card limit %d", i, len(part.text), streamingReplyCardSoftLimit)
		}
		if part.requestUUID == "" || seenUUIDs[part.requestUUID] {
			t.Fatalf("final part %d request UUID %q is empty or reused", i, part.requestUUID)
		}
		seenUUIDs[part.requestUUID] = true
		last := i == len(finals)-1
		if last {
			requireCardMentions(t, part.card, "ou-a")
		} else if len(part.mentions) != 0 || strings.Contains(part.card, "ou-a") {
			t.Fatalf("non-final part %d mentions initiators: %s", i, part.card)
		}
		lines += strings.Count(part.text, line)
	}
	if lines != 1000 {
		t.Fatalf("final parts carry %d answer lines, want the complete answer", lines)
	}
	if logical != strings.TrimRight(answer, "\n") {
		t.Fatalf("logical output changed: len=%d want=%d", len(logical), len(strings.TrimRight(answer, "\n")))
	}
}

func TestLegacyReplyMentionPublishesFinalCardThenCompactsPatchedAnswer(t *testing.T) {
	api := &fakeCardKitReplyAPI{createErr: errors.New("cardkit unavailable")}
	var created, patched, finalCard string
	var compacted []string
	backend := &bindingReplyCardBackend{
		cardKit: api,
		createLegacy: func(_ context.Context, text string) (string, error) {
			created = renderAgentReplyCard(text)
			return "legacy-message", nil
		},
		patchLegacy: func(_ context.Context, _ string, text string) error {
			patched = renderAgentReplyCard(text)
			return nil
		},
		sendMentionedFinal: func(_ context.Context, text string, openIDs []string, _ string) (string, error) {
			finalCard = renderAgentReplyCardWithMentions(text, openIDs)
			return "legacy-final", nil
		},
		compactMessage: func(_ context.Context, messageID, content string) error {
			compacted = append(compacted, messageID+":"+replyCompletionCardText(t, content, i18n.Chinese))
			return nil
		},
	}
	sender := newStreamingReplySender(0, backend, nil)
	sender.enqueue(replyCommandAdd, "部分回答")
	sender.ReplaceCurrent("完整回答")
	sender.Flush()
	sender.CompleteTurn([]string{"ou-a"})
	sender.Close()
	for label, raw := range map[string]string{"created": created, "patched": patched} {
		if strings.Contains(raw, "ou-a") {
			t.Fatalf("%s answer card mentions before completion: %s", label, raw)
		}
	}
	requireCardMentions(t, finalCard, "ou-a")
	if !strings.Contains(finalCard, "完整回答") || strings.Join(compacted, ",") != "legacy-message:已完成" {
		t.Fatalf("final card = %s, compacted = %#v", finalCard, compacted)
	}
}

func TestReplyMentionFinalSendRetriesWithStableUUIDAndDoesNotCompactOnFailure(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-retry"}
	var requestUUIDs []string
	var compacted []string
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-retry", nil
		},
		sendMentionedFinal: func(_ context.Context, _ string, _ []string, requestUUID string) (string, error) {
			requestUUIDs = append(requestUUIDs, requestUUID)
			if len(requestUUIDs) == 1 {
				return "", context.DeadlineExceeded
			}
			return "message-final", nil
		},
		compactMessage: func(_ context.Context, messageID, content string) error {
			compacted = append(compacted, messageID+":"+replyCompletionCardText(t, content, i18n.Chinese))
			return nil
		},
	}
	sender := newStreamingReplySender(0, backend, nil)
	sender.enqueue(replyCommandAdd, "answer")
	sender.CompleteTurn([]string{"ou-a"})
	sender.Close()
	if len(requestUUIDs) != 2 || requestUUIDs[0] == "" || requestUUIDs[0] != requestUUIDs[1] {
		t.Fatalf("final request UUIDs = %#v, want one stable non-empty identity", requestUUIDs)
	}
	if strings.Join(compacted, ",") != "message-retry:已完成" {
		t.Fatalf("compacted = %#v, want compression only after successful final send", compacted)
	}
}

func TestReplyMentionFinalCompressionFailureRetriesWithoutResendingFinal(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-recall-retry"}
	finalSendCount := 0
	compactCount := 0
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-progress", nil
		},
		sendMentionedFinal: func(context.Context, string, []string, string) (string, error) {
			finalSendCount++
			return "message-final", nil
		},
		compactMessage: func(context.Context, string, string) error {
			compactCount++
			if compactCount == 1 {
				return errors.New("temporary compression failure")
			}
			return nil
		},
	}
	sender := newStreamingReplySender(0, backend, nil)
	sender.enqueue(replyCommandAdd, "answer")
	sender.CompleteTurn([]string{"ou-a"})
	sender.Close()
	if finalSendCount != 1 || compactCount != 2 {
		t.Fatalf("after retry final sends = %d, compressions = %d; final card must not duplicate", finalSendCount, compactCount)
	}
}

func TestReplyMentionFinalWaitsForSuccessfulAnswerFinish(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-finish-error", finishErr: errors.New("finish failed")}
	finalSendCount := 0
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-finish-error", nil
		},
		sendMentionedFinal: func(context.Context, string, []string, string) (string, error) {
			finalSendCount++
			return "message-final", nil
		},
	}
	sender := newStreamingReplySender(0, backend, nil)
	sender.enqueue(replyCommandAdd, "answer")
	sender.CompleteTurn([]string{"ou-a"})
	sender.Close()
	if finalSendCount != 0 {
		t.Fatalf("final sends = %d, want none before answer finishes successfully", finalSendCount)
	}
}

func TestReplyMentionFinalOnlyPublishesForCompletedTurn(t *testing.T) {
	for name, finish := range map[string]func(*streamingReplySender){
		"flush and close":          func(s *streamingReplySender) { s.Flush(); s.Close() },
		"reset without completion": func(s *streamingReplySender) { s.Flush(); s.Reset() },
		"superseded":               func(s *streamingReplySender) { s.Supersede(); s.CompleteTurn([]string{"ou-a"}) },
		"no initiators":            func(s *streamingReplySender) { s.Flush(); s.CompleteTurn(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			api := &fakeCardKitReplyAPI{cardID: "card-incomplete"}
			finalSendCount, compactCount := 0, 0
			backend := &bindingReplyCardBackend{
				cardKit: api,
				sendCard: func(context.Context, string, string) (string, error) {
					return "message-incomplete", nil
				},
				sendMentionedFinal: func(context.Context, string, []string, string) (string, error) {
					finalSendCount++
					return "message-final", nil
				},
				compactMessage: func(context.Context, string, string) error {
					compactCount++
					return nil
				},
			}
			// Targets left over from an earlier configuration must not publish on
			// their own: only CompleteTurn with initiators may.
			backend.SetMentionTargets([]string{"ou-a"})
			sender := newStreamingReplySender(0, backend, nil)
			sender.enqueue(replyCommandAdd, "partial answer")
			finish(sender)
			sender.Close()
			if finalSendCount != 0 || compactCount != 0 {
				t.Fatalf("final sends = %d, compactions = %d; incomplete Turns must keep their cards", finalSendCount, compactCount)
			}
		})
	}
}

func TestReplyMentionStateCanBeClaimedByAggregatorTimer(t *testing.T) {
	state := &replyMentionState{}
	state.observe("turn")
	resolves := 0
	resolve := func(turnID string) []string {
		resolves++
		if turnID != "turn" {
			t.Errorf("resolved turn = %q", turnID)
		}
		return []string{"ou-a", "ou-a"}
	}
	type claimResult struct {
		turnID  string
		openIDs []string
	}
	done := make(chan claimResult, 1)
	time.AfterFunc(time.Millisecond, func() {
		turnID, openIDs := state.claim(resolve)
		done <- claimResult{turnID: turnID, openIDs: openIDs}
	})
	result := <-done
	turnID, claimed := result.turnID, result.openIDs
	if turnID != "turn" || len(claimed) != 1 || claimed[0] != "ou-a" {
		t.Fatalf("first claim = %q %#v", turnID, claimed)
	}
	if _, duplicate := state.claim(resolve); len(duplicate) != 0 {
		t.Fatalf("duplicate claim = %#v", duplicate)
	}
	state.release(turnID)
	if _, retried := state.claim(resolve); len(retried) != 1 || retried[0] != "ou-a" {
		t.Fatalf("released claim = %#v", retried)
	}
	state.observe("turn")
	if _, again := state.claim(resolve); len(again) != 0 {
		t.Fatalf("observing the same turn reset its delivery: %#v", again)
	}
	state.observe("next-turn")
	if gotTurn, next := state.claim(func(string) []string { return []string{"ou-b"} }); gotTurn != "next-turn" || len(next) != 1 || next[0] != "ou-b" {
		t.Fatalf("next turn claim = %q %#v", gotTurn, next)
	}
	if resolves != 2 {
		t.Fatalf("resolves = %d, want lookups only for attempted posts", resolves)
	}
}

func TestReplyMentionStateResolvesLateSubmitSender(t *testing.T) {
	state := &replyMentionState{}
	state.observe("turn")
	var senders []string
	resolve := func(string) []string { return senders }
	if turnID, openIDs := state.claim(resolve); turnID != "" || len(openIDs) != 0 {
		t.Fatalf("claim before Submit receipt = %q %#v", turnID, openIDs)
	}
	senders = []string{"ou-a"}
	turnID, openIDs := state.claim(resolve)
	if turnID != "turn" || len(openIDs) != 1 || openIDs[0] != "ou-a" {
		t.Fatalf("late sender claim = %q %#v", turnID, openIDs)
	}
	senders = []string{"ou-b"}
	if _, replaced := state.claim(resolve); len(replaced) != 0 {
		t.Fatalf("delivered turn accepted a later target replacement: %#v", replaced)
	}
}

func requireCardMentions(t *testing.T, raw string, openIDs ...string) {
	t.Helper()
	var doc cardDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode card: %v: %s", err, raw)
	}
	element, ok := findCardElementByID(doc.Body.Elements, amuxBrandFooterID)
	if !ok {
		t.Fatalf("card lacks brand footer mention element: %s", raw)
	}
	for _, openID := range openIDs {
		mention := `<at id="` + openID + `"></at>`
		if strings.Count(element.Content, mention) != 1 {
			t.Fatalf("mention %q count = %d in %q", openID, strings.Count(element.Content, mention), element.Content)
		}
	}
}
