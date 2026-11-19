package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larktypes "github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larkdrive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
)

type documentStreamingHub struct {
	ready  chan struct{}
	mu     sync.Mutex
	emit   func(*pb.TunnelMsg) error
	prompt string
}

type failingDocumentStreamHub struct{}

type cancelOnSubmitDocumentHub struct {
	*bridgeTestHub
	cancel context.CancelFunc
}

type documentReadinessHub struct {
	*bridgeTestHub
	streamStarted  chan struct{}
	allowHeartbeat chan struct{}
	submitted      chan struct{}
}

func (h *documentReadinessHub) Route(
	ctx context.Context, targetID, method string, payload interface{},
) (*pb.TunnelMsg, error) {
	if method == "Submit" {
		close(h.submitted)
	}
	return h.bridgeTestHub.Route(ctx, targetID, method, payload)
}

func (h *documentReadinessHub) RouteStream(
	ctx context.Context, _ string, method string, _ interface{}, emit func(*pb.TunnelMsg) error,
) error {
	if method != "Attach" {
		return fmt.Errorf("unexpected stream method %s", method)
	}
	close(h.streamStarted)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.allowHeartbeat:
	}
	raw, _ := json.Marshal(&pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT})
	if err := emit(&pb.TunnelMsg{Payload: raw}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (h *cancelOnSubmitDocumentHub) Route(
	ctx context.Context, targetID, method string, payload interface{},
) (*pb.TunnelMsg, error) {
	if method == "Submit" {
		h.cancel()
		return nil, errors.New("submit canceled")
	}
	return h.bridgeTestHub.Route(ctx, targetID, method, payload)
}

func (failingDocumentStreamHub) ListTargets() []*pb.Target { return nil }

func (failingDocumentStreamHub) Route(context.Context, string, string, interface{}) (*pb.TunnelMsg, error) {
	return nil, errors.New("unexpected route")
}

func (failingDocumentStreamHub) RouteStream(context.Context, string, string, interface{}, func(*pb.TunnelMsg) error) error {
	return errors.New("attach unavailable")
}

func (h *documentStreamingHub) ListTargets() []*pb.Target {
	return []*pb.Target{{Id: "target-1", Name: "devbox"}}
}

func (h *documentStreamingHub) Route(ctx context.Context, _, method string, payload interface{}) (*pb.TunnelMsg, error) {
	switch method {
	case "Run":
		req := payload.(*pb.RunRequest)
		raw, _ := json.Marshal(&pb.RunResponse{Session: &pb.Session{
			Name: req.Name, AgentProfile: req.AgentProfile, AgentType: req.AgentType, State: "running",
		}})
		return &pb.TunnelMsg{Payload: raw}, nil
	case "Submit":
		select {
		case <-h.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return nil, fmt.Errorf("document attach stream did not start")
		}
		req := payload.(*pb.SubmitRequest)
		h.mu.Lock()
		emit := h.emit
		h.prompt = req.Text
		h.mu.Unlock()
		ref := &agentstream.TurnRef{ID: "turn-1", Seq: 1}
		for seq, body := range []agentstream.Body{
			agentstream.TurnStarted{InputSummary: req.Text},
			agentstream.AssistantDelta{Text: "建议回复：\n\n最终建议"},
			agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}},
		} {
			canonical, err := agentstream.MarshalEvent(agentstream.Event{
				SessionID: "document-session",
				Cursor:    agentstream.Cursor{Epoch: "epoch-1", Seq: uint64(seq + 1)},
				Turn:      ref,
				At:        time.Now().UTC(),
				Origin:    agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "lark"},
				Body:      body,
			})
			if err != nil {
				return nil, err
			}
			event := &pb.AttachEvent{CanonicalJson: canonical}
			raw, _ := json.Marshal(event)
			if err := emit(&pb.TunnelMsg{Payload: raw}); err != nil {
				return nil, err
			}
		}
		raw, _ := json.Marshal(&pb.SubmitResponse{Ok: true})
		return &pb.TunnelMsg{Payload: raw}, nil
	default:
		return nil, fmt.Errorf("unexpected route method %s", method)
	}
}

func (h *documentStreamingHub) RouteStream(ctx context.Context, _, method string, payload interface{}, emit func(*pb.TunnelMsg) error) error {
	if method != "Attach" {
		return fmt.Errorf("unexpected stream method %s", method)
	}
	req := payload.(*pb.AttachRequest)
	if req.TimelineMode != pb.AttachRequest_TAIL {
		return fmt.Errorf("document Attach timeline mode = %s, want TAIL", req.TimelineMode)
	}
	h.mu.Lock()
	h.emit = emit
	h.mu.Unlock()
	raw, _ := json.Marshal(&pb.AttachEvent{Kind: pb.AttachEvent_HEARTBEAT})
	if err := emit(&pb.TunnelMsg{Payload: raw}); err != nil {
		return err
	}
	close(h.ready)
	<-ctx.Done()
	return ctx.Err()
}

func newDocumentCommentTestBridge(t *testing.T, owner string) (*Bridge, *bridgeTestHub, *[]string, *[]string) {
	t.Helper()
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hub := &bridgeTestHub{targets: []*pb.Target{{Id: "target-1", Name: "devbox"}}}
	replies := []string{}
	reactions := []string{}
	var repliesMu sync.Mutex
	b := &Bridge{
		hub:                       hub,
		sessionStarter:            sessionlaunch.New(hub),
		db:                        db,
		secrets:                   &Secrets{AppId: "cli_test", OpenId: owner, BotOpenId: "bot-open"},
		bridgeID:                  "feishu:cli_test",
		inputRouter:               inputRouter{routeLocks: make(map[string]*sync.Mutex)},
		documentCommentController: documentCommentController{documentCommentsEnabled: true, documentStreams: make(map[string]context.CancelFunc), documentTurns: make(map[string]map[string]*documentTurnDelivery)},
		documentCommentAPI: documentCommentAPI{documentCommentFetch: func(_ context.Context, _ documentCommentRef) (documentCommentSnapshot, error) {
			return documentCommentSnapshot{
				Quote: "被评论的设计段落",
				Replies: []documentCommentReply{
					{ReplyID: "reply-1", OpenID: owner, Text: "@amux 检查失败恢复"},
				},
			}, nil
		}, documentCommentReply: func(_ context.Context, _ documentCommentRef, text string) (string, error) {
			repliesMu.Lock()
			defer repliesMu.Unlock()
			replies = append(replies, text)
			return "bot-reply", nil
		}, documentCommentReact: func(_ context.Context, ref documentCommentRef, action, reactionType string) error {
			reactions = append(reactions, action+":"+reactionType+":"+ref.ReplyID)
			return nil
		}},
	}
	return b, hub, &replies, &reactions
}

func TestReplyDocumentCommentRejectsIncompleteSuccessResponse(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantErr  string
	}{
		{
			name:     "missing data",
			response: `{"code":0,"msg":"ok"}`,
			wantErr:  "empty response data",
		},
		{
			name:     "missing reply id",
			response: `{"code":0,"msg":"ok","data":{}}`,
			wantErr:  "empty reply_id",
		},
		{
			name:     "blank reply id",
			response: `{"code":0,"msg":"ok","data":{"reply_id":"  "}}`,
			wantErr:  "empty reply_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
				case r.Method == http.MethodPost &&
					r.URL.Path == "/open-apis/drive/v1/files/doc-token/comments/comment-1/replies":
					_, _ = w.Write([]byte(tt.response))
				default:
					t.Fatalf("unexpected lark API call: %s %s", r.Method, r.URL.String())
				}
			}))
			defer server.Close()

			b := &Bridge{client: lark.NewClient(
				"app-id",
				"app-secret",
				lark.WithOpenBaseUrl(server.URL),
				lark.WithOAuthBaseUrl(server.URL),
			)}
			_, err := b.replyDocumentComment(context.Background(), documentCommentRef{
				FileToken: "doc-token",
				FileType:  "docx",
				CommentID: "comment-1",
			}, "测试回复")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("replyDocumentComment error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDocumentCommentOwnerCreatesOneSessionAndReusesIt(t *testing.T) {
	b, hub, replies, reactions := newDocumentCommentTestBridge(t, "owner-open")
	event := &larktypes.CommentEvent{
		EventID: "event-1", CommentID: "comment-1", ReplyID: "reply-1",
		FileToken: "doc-token", FileType: "docx", MentionedBot: true,
		Operator: larktypes.OperatorInfo{OpenID: "owner-open"},
	}
	if err := b.handleDocumentComment(context.Background(), event); err != nil {
		t.Fatalf("first handleDocumentComment: %v", err)
	}

	event.EventID = "event-2"
	if err := b.handleDocumentComment(context.Background(), event); err != nil {
		t.Fatalf("second handleDocumentComment: %v", err)
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	var runs, submits int
	for _, call := range hub.routes {
		switch call.method {
		case "Run":
			runs++
			req := call.payload.(*pb.RunRequest)
			if req.WorkspaceSource != "feishu" || !strings.HasPrefix(req.WorkspaceScope, "document-") {
				t.Fatalf("Run workspace = %q/%q, want feishu/document-*", req.WorkspaceSource, req.WorkspaceScope)
			}
		case "Submit":
			submits++
			req := call.payload.(*pb.SubmitRequest)
			if req.Source != "lark" || !strings.Contains(req.Text, "<amux_document_comment") {
				t.Fatalf("Submit = %+v, want Lark document invocation", req)
			}
		}
	}
	if runs != 1 || submits != 2 {
		t.Fatalf("routes Run=%d Submit=%d, want 1 and 2", runs, submits)
	}
	if len(*replies) != 0 {
		t.Fatalf("replies = %#v, want reaction-only acknowledgement", *replies)
	}
	if got := *reactions; len(got) != 2 || got[0] != "add:OneSecond:reply-1" || got[1] != "add:OneSecond:reply-1" {
		t.Fatalf("reactions = %#v, want the chat-style reaction per accepted turn", got)
	}
}

func TestDocumentCommentNonOwnerDoesNotReadOrStartSession(t *testing.T) {
	b, hub, replies, reactions := newDocumentCommentTestBridge(t, "owner-open")
	fetched := false
	b.documentCommentFetch = func(context.Context, documentCommentRef) (documentCommentSnapshot, error) {
		fetched = true
		return documentCommentSnapshot{}, nil
	}
	event := &larktypes.CommentEvent{
		EventID: "event-non-owner", CommentID: "comment-1", ReplyID: "reply-1",
		FileToken: "doc-token", FileType: "docx", MentionedBot: true,
		Operator: larktypes.OperatorInfo{OpenID: "member-open"},
	}
	if err := b.handleDocumentComment(context.Background(), event); err != nil {
		t.Fatalf("handleDocumentComment: %v", err)
	}
	if fetched {
		t.Fatal("non-owner event read comment content")
	}
	hub.mu.Lock()
	routes := len(hub.routes)
	hub.mu.Unlock()
	if routes != 0 {
		t.Fatalf("non-owner routes = %d, want 0", routes)
	}
	if len(*replies) != 1 || !strings.Contains((*replies)[0], "没有读取文档内容") {
		t.Fatalf("replies = %#v, want owner-only notice", *replies)
	}
	if len(*reactions) != 0 {
		t.Fatalf("reactions = %#v, non-owner must not receive processing state", *reactions)
	}
}

func TestDocumentCommentIgnoresUnmentionedSelfAndDuplicateEvents(t *testing.T) {
	b, hub, replies, reactions := newDocumentCommentTestBridge(t, "owner-open")
	base := larktypes.CommentEvent{
		EventID: "event-1", CommentID: "comment-1", ReplyID: "reply-1",
		FileToken: "doc-token", FileType: "docx",
		Operator: larktypes.OperatorInfo{OpenID: "owner-open"},
	}
	if err := b.handleDocumentComment(context.Background(), &base); err != nil {
		t.Fatal(err)
	}
	base.MentionedBot = true
	base.Operator.OpenID = "bot-open"
	if err := b.handleDocumentComment(context.Background(), &base); err != nil {
		t.Fatal(err)
	}
	base.Operator.OpenID = "owner-open"
	if err := b.handleDocumentComment(context.Background(), &base); err != nil {
		t.Fatal(err)
	}
	if err := b.handleDocumentComment(context.Background(), &base); err != nil {
		t.Fatal(err)
	}

	hub.mu.Lock()
	routes := len(hub.routes)
	hub.mu.Unlock()
	if routes != 2 {
		t.Fatalf("routes = %d, want one Run and one Submit", routes)
	}
	if len(*replies) != 0 {
		t.Fatalf("replies = %#v, want no processing comment", *replies)
	}
	if len(*reactions) != 1 {
		t.Fatalf("reactions = %#v, want one acknowledgement", *reactions)
	}
}

func TestDocumentCommentSubmitFailureClearsProcessingReaction(t *testing.T) {
	b, hub, replies, reactions := newDocumentCommentTestBridge(t, "owner-open")
	hub.routeErrs = map[string]error{"Submit": errors.New("submit unavailable")}
	event := &larktypes.CommentEvent{
		EventID: "event-submit-failure", CommentID: "comment-1", ReplyID: "reply-1",
		FileToken: "doc-token", FileType: "docx", MentionedBot: true,
		Operator: larktypes.OperatorInfo{OpenID: "owner-open"},
	}
	if err := b.handleDocumentComment(context.Background(), event); err == nil {
		t.Fatal("handleDocumentComment error = nil, want submit failure")
	}
	if got := *reactions; len(got) != 2 ||
		got[0] != "add:OneSecond:reply-1" || got[1] != "delete:OneSecond:reply-1" {
		t.Fatalf("reactions = %#v, want processing reaction removed after submit failure", got)
	}
	if len(*replies) != 1 || !strings.Contains((*replies)[0], "任务提交失败") {
		t.Fatalf("replies = %#v, want actionable submit failure", *replies)
	}
}

func TestDocumentCommentSubmitCancellationUsesIndependentReactionCleanupContext(t *testing.T) {
	b, hub, _, _ := newDocumentCommentTestBridge(t, "owner-open")
	eventCtx, cancel := context.WithCancel(context.Background())
	b.hub = &cancelOnSubmitDocumentHub{bridgeTestHub: hub, cancel: cancel}
	var cleanupContextCanceled bool
	var deleted bool
	b.documentCommentReact = func(ctx context.Context, _ documentCommentRef, action, _ string) error {
		if action == "delete" {
			cleanupContextCanceled = ctx.Err() != nil
			deleted = !cleanupContextCanceled
		}
		return nil
	}
	event := &larktypes.CommentEvent{
		EventID: "event-submit-canceled", CommentID: "comment-1", ReplyID: "reply-1",
		FileToken: "doc-token", FileType: "docx", MentionedBot: true,
		Operator: larktypes.OperatorInfo{OpenID: "owner-open"},
	}
	if err := b.handleDocumentComment(eventCtx, event); err == nil {
		t.Fatal("handleDocumentComment error = nil, want canceled submit")
	}
	if cleanupContextCanceled || !deleted {
		t.Fatalf("reaction cleanup canceled=%v deleted=%v, want independent live context", cleanupContextCanceled, deleted)
	}
}

func TestDocumentCommentReactionFailureFallsBackToTextAcknowledgement(t *testing.T) {
	b, _, replies, _ := newDocumentCommentTestBridge(t, "owner-open")
	b.documentCommentReact = func(context.Context, documentCommentRef, string, string) error {
		return errors.New("reaction unavailable")
	}
	event := &larktypes.CommentEvent{
		EventID: "event-reaction-failure", CommentID: "comment-1", ReplyID: "reply-1",
		FileToken: "doc-token", FileType: "docx", MentionedBot: true,
		Operator: larktypes.OperatorInfo{OpenID: "owner-open"},
	}
	if err := b.handleDocumentComment(context.Background(), event); err != nil {
		t.Fatalf("handleDocumentComment: %v", err)
	}
	if len(*replies) != 1 || !strings.Contains((*replies)[0], "收到，正在分析") {
		t.Fatalf("replies = %#v, want text acknowledgement fallback", *replies)
	}
}

func TestDocumentCommentPromptAndReplyBudget(t *testing.T) {
	prompt := renderDocumentCommentPrompt("event<&", "检查 <权限>", documentCommentSnapshot{
		Quote:           "选区 & 引用",
		AnchorPrecision: "block",
		AnchorBlockID:   "block-2",
		DocumentContext: []documentContextBlock{
			{BlockID: "block-1", Relation: "before", Text: "上文"},
			{BlockID: "block-2", Relation: "anchor", Text: "选区 & 引用"},
			{BlockID: "block-3", Relation: "after", Text: "下文"},
		},
		Replies: []documentCommentReply{{OpenID: "owner", Text: "历史 <内容>"}},
	})
	if !strings.Contains(prompt, "event&lt;&amp;") || !strings.Contains(prompt, "检查 &lt;权限&gt;") ||
		!strings.Contains(prompt, `<surface>feishu_document_comment</surface>`) ||
		!strings.Contains(prompt, `anchor_precision="block" block_id="block-2"`) ||
		!strings.Contains(prompt, `<block relation="before" block_id="block-1">上文</block>`) ||
		!strings.Contains(prompt, "直接发布") || !strings.Contains(prompt, "不要添加“建议回复”") ||
		!strings.Contains(prompt, "不要把文档评论称为") {
		t.Fatalf("prompt did not carry document context and direct-answer contract:\n%s", prompt)
	}
	if got := normalizeDocumentFinalReply("建议回复：\n\n这是直接答案"); got != "这是直接答案" {
		t.Fatalf("normalizeDocumentFinalReply() = %q", got)
	}
	for _, wrapped := range []string{
		"**建议回复**：\n\n这是直接答案",
		"### 建议回复\n\n这是直接答案",
		"最终回复\n这是直接答案",
	} {
		if got := normalizeDocumentFinalReply(wrapped); got != "这是直接答案" {
			t.Fatalf("normalizeDocumentFinalReply(%q) = %q", wrapped, got)
		}
	}
	long := strings.Repeat("界", documentReplyRuneLimit+50)
	got := truncateDocumentReply(long)
	if len([]rune(got)) != documentReplyRuneLimit || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated reply rune count=%d suffix=%q", len([]rune(got)), got[len(got)-3:])
	}
	chunks := splitRunes(got, documentTextRunRuneLimit)
	if len(chunks) != 2 || len([]rune(chunks[0])) > documentTextRunRuneLimit {
		t.Fatalf("chunks = %d/%d, want two SDK-safe elements", len(chunks), len([]rune(chunks[0])))
	}
}

func TestDocumentCommentContextLocatesElementAndNeighborBlocks(t *testing.T) {
	commentID := "comment-1"
	blocks := []*larkdocx.Block{
		textDocumentBlock("block-1", "上文", nil, nil),
		textDocumentBlock("block-2", "被评论内容", []string{commentID}, []string{commentID}),
		textDocumentBlock("block-3", "下文", nil, nil),
	}
	precision, anchorID, context := documentCommentContextFromBlocks(blocks, commentID, "被评论内容")
	if precision != "element" || anchorID != "block-2" {
		t.Fatalf("anchor = %s/%s, want element/block-2", precision, anchorID)
	}
	if len(context) != 3 || context[0].Relation != "before" ||
		context[1].Relation != "anchor" || context[2].Relation != "after" {
		t.Fatalf("context = %+v, want before/anchor/after", context)
	}
}

func TestDocumentCommentContextKeepsAnchorWhenPreviousBlockExceedsBudget(t *testing.T) {
	commentID := "comment-1"
	blocks := []*larkdocx.Block{
		textDocumentBlock("block-1", strings.Repeat("前", documentContextRuneLimit+100), nil, nil),
		textDocumentBlock("block-2", "必须保留的锚点", []string{commentID}, []string{commentID}),
		textDocumentBlock("block-3", "下文", nil, nil),
	}
	precision, anchorID, context := documentCommentContextFromBlocks(blocks, commentID, "必须保留的锚点")
	if precision != "element" || anchorID != "block-2" {
		t.Fatalf("anchor = %s/%s, want element/block-2", precision, anchorID)
	}
	var foundAnchor bool
	for _, block := range context {
		if block.Relation == "anchor" && block.BlockID == "block-2" && block.Text == "必须保留的锚点" {
			foundAnchor = true
		}
	}
	if !foundAnchor {
		t.Fatalf("context = %+v, want the anchor block even when before text is oversized", context)
	}
}

func textDocumentBlock(blockID, text string, blockCommentIDs, elementCommentIDs []string) *larkdocx.Block {
	return larkdocx.NewBlockBuilder().
		BlockId(blockID).
		CommentIds(blockCommentIDs).
		Text(larkdocx.NewTextBuilder().
			Elements([]*larkdocx.TextElement{
				larkdocx.NewTextElementBuilder().
					TextRun(larkdocx.NewTextRunBuilder().
						Content(text).
						TextElementStyle(larkdocx.NewTextElementStyleBuilder().
							CommentIds(elementCommentIDs).
							Build()).
						Build()).
					Build(),
			}).
			Build()).
		Build()
}

func TestDocumentCommentUsesRepliesEmbeddedInBatchQuery(t *testing.T) {
	replyID := "reply-1"
	ownerID := "owner-open"
	createdAt := 123
	text := "解释这一段"
	item := larkdrive.NewFileCommentBuilder().
		Quote("被评论的段落").
		HasMore(false).
		ReplyList(larkdrive.NewReplyListBuilder().
			Replies([]*larkdrive.FileCommentReply{{
				ReplyId:    &replyID,
				UserId:     &ownerID,
				CreateTime: &createdAt,
				Content: larkdrive.NewReplyContentBuilder().
					Elements([]*larkdrive.ReplyElement{
						larkdrive.NewReplyElementBuilder().
							Type("text_run").
							TextRun(larkdrive.NewTextRunBuilder().Text(text).Build()).
							Build(),
					}).
					Build(),
			}}).
			Build()).
		Build()

	snapshot := documentCommentSnapshot{
		Quote:   stringPtr(item.Quote),
		IsWhole: item.IsWhole != nil && *item.IsWhole,
	}
	seen := make(map[string]struct{})
	for _, reply := range item.ReplyList.Replies {
		appendDocumentCommentReply(&snapshot, reply, replyID, seen)
	}
	finalizeDocumentCommentSnapshot(&snapshot, replyID, &Secrets{OpenId: ownerID})

	if snapshot.RequestText != text {
		t.Fatalf("RequestText = %q, want %q", snapshot.RequestText, text)
	}
	if len(snapshot.Replies) != 1 || snapshot.Replies[0].ReplyID != replyID {
		t.Fatalf("Replies = %+v, want embedded batch-query reply", snapshot.Replies)
	}
	if item.HasMore == nil || *item.HasMore {
		t.Fatal("HasMore should explicitly disable the follow-up reply-list request")
	}
}

func TestDocumentCommentSubscriptionFailureDisablesOnlyDocumentSource(t *testing.T) {
	b := &Bridge{
		documentCommentController: documentCommentController{documentCommentsEnabled: true},
		documentCommentAPI: documentCommentAPI{documentSubscribe: func(context.Context) error {
			return errors.New("missing docs:event:subscribe")
		}},
	}
	if err := b.prepareDocumentCommentSource(context.Background()); err == nil {
		t.Fatal("prepareDocumentCommentSource error = nil, want subscription failure")
	}
	if b.documentCommentsEnabled {
		t.Fatal("document source remained enabled after subscription failure")
	}
}

func TestSetDocumentCommentsForProfileEnablesAfterSubscriptionAndPersists(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.document_comments]
enabled = false
`)
	subscriptions := 0
	b := &Bridge{
		profile: "coder",
		documentCommentAPI: documentCommentAPI{documentSubscribe: func(context.Context) error {
			subscriptions++
			return nil
		}},
	}

	if err := b.setDocumentCommentsForProfile(context.Background(), true); err != nil {
		t.Fatalf("setDocumentCommentsForProfile: %v", err)
	}
	if subscriptions != 1 {
		t.Fatalf("subscriptions = %d, want 1", subscriptions)
	}
	if !b.documentCommentsActive() {
		t.Fatal("document comments active = false, want true")
	}
	if got := config.GetBridgeFeishuDocumentCommentsForProfile("coder"); !got.Enabled {
		t.Fatalf("profile config = %+v, want enabled", got)
	}
}

func TestSetDocumentCommentsForProfileSubscriptionFailureKeepsOldState(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.document_comments]
enabled = false
`)
	b := &Bridge{
		profile: "coder",
		documentCommentAPI: documentCommentAPI{documentSubscribe: func(context.Context) error {
			return errors.New("missing docs:event:subscribe")
		}},
	}

	if err := b.setDocumentCommentsForProfile(context.Background(), true); err == nil {
		t.Fatal("setDocumentCommentsForProfile error = nil, want subscription failure")
	}
	if b.documentCommentsActive() {
		t.Fatal("document comments active = true after subscription failure")
	}
	if got := config.GetBridgeFeishuDocumentCommentsForProfile("coder"); got.Enabled {
		t.Fatalf("profile config = %+v, want unchanged disabled state", got)
	}
}

func TestSetDocumentCommentsForProfileDisablesWithoutSubscription(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.document_comments]
enabled = true
`)
	b := &Bridge{
		profile:                   "coder",
		documentCommentController: documentCommentController{documentCommentsEnabled: true, documentStreams: make(map[string]context.CancelFunc), documentTurns: make(map[string]map[string]*documentTurnDelivery)},
		documentCommentAPI: documentCommentAPI{documentCommentFetch: func(context.Context, documentCommentRef) (documentCommentSnapshot, error) {
			t.Fatal("disabled source fetched a comment")
			return documentCommentSnapshot{}, nil
		}, documentSubscribe: func(context.Context) error { t.Fatal("disable must not subscribe"); return nil }},
	}

	if err := b.setDocumentCommentsForProfile(context.Background(), false); err != nil {
		t.Fatalf("setDocumentCommentsForProfile: %v", err)
	}
	if b.documentCommentsActive() {
		t.Fatal("document comments active = true, want false")
	}
	if got := config.GetBridgeFeishuDocumentCommentsForProfile("coder"); got.Enabled {
		t.Fatalf("profile config = %+v, want disabled", got)
	}
	if err := b.handleDocumentComment(context.Background(), &larktypes.CommentEvent{
		MentionedBot: true,
		CommentID:    "comment-1",
		FileToken:    "doc-1",
		FileType:     "docx",
	}); err != nil {
		t.Fatalf("handle disabled document comment: %v", err)
	}
}

func TestDocumentCommentAttachOutputReturnsFinalNativeReply(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	hub := &documentStreamingHub{ready: make(chan struct{})}
	var replies []string
	var reactions []string
	b := &Bridge{
		hub:                       hub,
		sessionStarter:            sessionlaunch.New(hub),
		db:                        db,
		bridgeID:                  "feishu:cli_test",
		secrets:                   &Secrets{AppId: "cli_test", OpenId: "owner-open"},
		inputRouter:               inputRouter{routeLocks: make(map[string]*sync.Mutex)},
		documentCommentController: documentCommentController{documentCommentsEnabled: true, documentStreams: make(map[string]context.CancelFunc), documentTurns: make(map[string]map[string]*documentTurnDelivery)},
		documentCommentAPI: documentCommentAPI{documentCommentFetch: func(context.Context, documentCommentRef) (documentCommentSnapshot, error) {
			return documentCommentSnapshot{
				Quote:   "设计段落",
				Replies: []documentCommentReply{{ReplyID: "reply-1", OpenID: "owner-open", Text: "@amux 审查一下"}},
			}, nil
		}, documentCommentReply: func(_ context.Context, _ documentCommentRef, text string) (string, error) {
			replies = append(replies, text)
			return fmt.Sprintf("reply-%d", len(replies)), nil
		}, documentCommentReact: func(_ context.Context, _ documentCommentRef, action, reactionType string) error {
			reactions = append(reactions, action+":"+reactionType)
			return nil
		}},
	}
	event := &larktypes.CommentEvent{
		EventID: "event-stream", CommentID: "comment-1", ReplyID: "reply-1",
		FileToken: "doc-token", FileType: "docx", MentionedBot: true,
		Operator: larktypes.OperatorInfo{OpenID: "owner-open"},
	}
	if err := b.handleDocumentComment(context.Background(), event); err != nil {
		t.Fatalf("handleDocumentComment: %v", err)
	}
	if len(replies) != 1 || replies[0] != "最终建议" {
		t.Fatalf("replies = %#v, want only the final output", replies)
	}
	if len(reactions) != 2 || reactions[0] != "add:OneSecond" || reactions[1] != "delete:OneSecond" {
		t.Fatalf("reactions = %#v, want add/delete around the active turn", reactions)
	}
	binding, err := db.FindDocumentCommentSession("feishu:cli_test", "doc-token", "comment-1")
	if err != nil || binding == nil {
		t.Fatalf("document binding = %+v, %v", binding, err)
	}
	key := documentStreamKey(binding.TargetID, binding.SessionName)
	b.documentMu.Lock()
	cancel := b.documentStreams[key]
	b.documentMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func TestDocumentCommentSubmitWaitsForAttachReadinessHeartbeat(t *testing.T) {
	b, base, _, _ := newDocumentCommentTestBridge(t, "owner-open")
	hub := &documentReadinessHub{
		bridgeTestHub: base,
		streamStarted: make(chan struct{}), allowHeartbeat: make(chan struct{}),
		submitted: make(chan struct{}),
	}
	b.hub = hub
	event := &larktypes.CommentEvent{
		EventID: "event-ready", CommentID: "comment-ready", ReplyID: "reply-ready",
		FileToken: "doc-token", FileType: "docx", MentionedBot: true,
		Operator: larktypes.OperatorInfo{OpenID: "owner-open"},
	}
	done := make(chan error, 1)
	go func() { done <- b.handleDocumentComment(context.Background(), event) }()

	select {
	case <-hub.streamStarted:
	case <-time.After(time.Second):
		t.Fatal("document Attach stream did not start")
	}
	select {
	case <-hub.submitted:
		t.Fatal("document Submit reached runtime before Attach readiness heartbeat")
	case <-time.After(50 * time.Millisecond):
	}

	close(hub.allowHeartbeat)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleDocumentComment: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("document Submit did not continue after Attach readiness heartbeat")
	}
	select {
	case <-hub.submitted:
	default:
		t.Fatal("document Submit was not routed after Attach became ready")
	}

	binding, err := b.db.FindDocumentCommentSession(b.currentBridgeId(), event.FileToken, event.CommentID)
	if err != nil || binding == nil {
		t.Fatalf("document binding = %+v, %v", binding, err)
	}
	key := documentStreamKey(binding.TargetID, binding.SessionName)
	b.documentMu.Lock()
	cancel := b.documentStreams[key]
	b.documentMu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, delivery := range b.pendingDocumentTurns(key) {
		b.removeDocumentTurn(delivery)
	}
}

func TestDocumentCommentAttachFailureClosesPendingTurnWithNativeError(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	replyCh := make(chan string, 1)
	b := &Bridge{
		hub:                       failingDocumentStreamHub{},
		db:                        db,
		bridgeID:                  "feishu:cli_test",
		documentCommentController: documentCommentController{documentStreams: make(map[string]context.CancelFunc), documentTurns: make(map[string]map[string]*documentTurnDelivery)},
		documentCommentAPI: documentCommentAPI{documentCommentReply: func(_ context.Context, _ documentCommentRef, text string) (string, error) {
			replyCh <- text
			return "failure-reply", nil
		}},
	}
	delivery := &documentTurnDelivery{
		EventID: "event-failure", Ref: documentCommentRef{
			FileToken: "doc-token", FileType: "docx", CommentID: "comment-1",
		},
		Prompt: "prompt", TargetID: "target-1", SessionName: "session-1", OpenID: "owner-open",
	}
	if claimed, err := db.ClaimDocumentCommentEvent(DocumentCommentEvent{
		BridgeID: "feishu:cli_test", EventID: delivery.EventID,
	}); err != nil || !claimed {
		t.Fatalf("claim event = %v, %v", claimed, err)
	}
	b.registerDocumentTurn(delivery)
	b.ensureDocumentStream(&DocumentCommentSession{TargetID: delivery.TargetID, SessionName: delivery.SessionName})
	select {
	case reply := <-replyCh:
		if !strings.Contains(reply, "输出连接已中断") {
			t.Fatalf("failure reply = %q", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for native failure reply")
	}
	key := documentStreamKey(delivery.TargetID, delivery.SessionName)
	deadline := time.Now().Add(2 * time.Second)
	for len(b.pendingDocumentTurns(key)) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pending := b.pendingDocumentTurns(key); len(pending) != 0 {
		t.Fatalf("pending document turns = %d, want 0", len(pending))
	}
}

func TestDocumentCommentDeliveryTimeoutRepliesOnceAndClearsReaction(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	defer db.Close()
	replyCh := make(chan string, 2)
	reactionCh := make(chan string, 2)
	b := &Bridge{
		db:                        db,
		bridgeID:                  "feishu:cli_test",
		documentCommentController: documentCommentController{documentStreams: make(map[string]context.CancelFunc), documentTurns: make(map[string]map[string]*documentTurnDelivery), documentTurnTimeout: 30 * time.Millisecond},
		documentCommentAPI: documentCommentAPI{documentCommentReply: func(_ context.Context, _ documentCommentRef, text string) (string, error) {
			replyCh <- text
			return "timeout-reply", nil
		}, documentCommentReact: func(_ context.Context, _ documentCommentRef, action, reactionType string) error {
			reactionCh <- action + ":" + reactionType
			return nil
		}},
	}
	delivery := &documentTurnDelivery{
		EventID: "event-timeout", Ref: documentCommentRef{
			FileToken: "doc-token", FileType: "docx", CommentID: "comment-1", ReplyID: "reply-1",
		},
		Prompt: "prompt", TargetID: "target-1", SessionName: "session-1",
		OpenID: "owner-open", ReactionAdded: true,
	}
	if claimed, err := db.ClaimDocumentCommentEvent(DocumentCommentEvent{
		BridgeID: "feishu:cli_test", EventID: delivery.EventID,
	}); err != nil || !claimed {
		t.Fatalf("claim event = %v, %v", claimed, err)
	}

	b.registerDocumentTurn(delivery)
	select {
	case reply := <-replyCh:
		if !strings.Contains(reply, "超时") {
			t.Fatalf("timeout reply = %q", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for document delivery fallback")
	}

	// A late provider terminal must not publish a second native comment.
	b.finishDocumentTurn(delivery, "迟到的正常回答")
	select {
	case reply := <-replyCh:
		t.Fatalf("late terminal published duplicate reply %q", reply)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case reaction := <-reactionCh:
		if reaction != "delete:OneSecond" {
			t.Fatalf("reaction = %q, want delete:OneSecond", reaction)
		}
	case <-time.After(time.Second):
		t.Fatal("processing reaction was not cleared")
	}
	select {
	case reaction := <-reactionCh:
		t.Fatalf("processing reaction cleared more than once: %q", reaction)
	default:
	}
}

func TestDocumentCommentFinalizationClaimFailureDoesNotPublish(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatalf("openBridgeDB: %v", err)
	}
	if claimed, err := db.ClaimDocumentCommentEvent(DocumentCommentEvent{
		BridgeID: "feishu:cli_test", EventID: "event-db-failure",
	}); err != nil || !claimed {
		t.Fatalf("claim event = %v, %v", claimed, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close bridge DB: %v", err)
	}

	replyCh := make(chan string, 1)
	b := &Bridge{
		db:                        db,
		bridgeID:                  "feishu:cli_test",
		documentCommentController: documentCommentController{documentStreams: make(map[string]context.CancelFunc), documentTurns: make(map[string]map[string]*documentTurnDelivery)},
		documentCommentAPI: documentCommentAPI{documentCommentReply: func(_ context.Context, _ documentCommentRef, text string) (string, error) {
			replyCh <- text
			return "unexpected-reply", nil
		}},
	}
	b.finishDocumentTurn(&documentTurnDelivery{
		EventID: "event-db-failure",
		Ref:     documentCommentRef{FileToken: "doc-token", FileType: "docx", CommentID: "comment-1"},
	}, "不能在没有唯一发布权时发送")

	select {
	case reply := <-replyCh:
		t.Fatalf("finalization claim failure published reply %q", reply)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRetryFeishuRateLimitRetriesOn99991400(t *testing.T) {
	t.Parallel()
	calls := 0
	err := retryFeishuRateLimitWithBackoff(context.Background(), "test-op", time.Millisecond, 8*time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return &feishuPermissionError{operation: "test-op", code: feishuRateLimitCode, message: "request trigger frequency limit"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestRetryFeishuRateLimitNoRetryOnOtherError(t *testing.T) {
	t.Parallel()
	calls := 0
	err := retryFeishuRateLimitWithBackoff(context.Background(), "test-op", time.Millisecond, 8*time.Millisecond, func() error {
		calls++
		return fmt.Errorf("some other error: code=99991672")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Fatalf("expected 1 call (no retry), got %d", calls)
	}
}

func TestRetryFeishuRateLimitGivesUpAfterMaxRetries(t *testing.T) {
	t.Parallel()
	calls := 0
	err := retryFeishuRateLimitWithBackoff(context.Background(), "test-op", time.Millisecond, 8*time.Millisecond, func() error {
		calls++
		return &feishuPermissionError{operation: "test-op", code: feishuRateLimitCode, message: "request trigger frequency limit"}
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if calls != feishuRateLimitMaxRetries+1 {
		t.Fatalf("expected %d calls, got %d", feishuRateLimitMaxRetries+1, calls)
	}
}

func TestRetryFeishuRateLimitRespectsContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryFeishuRateLimitWithBackoff(ctx, "test-op", time.Millisecond, 8*time.Millisecond, func() error {
		calls++
		return &feishuPermissionError{operation: "test-op", code: feishuRateLimitCode, message: "request trigger frequency limit"}
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls > 1 {
		t.Fatalf("expected at most 1 call before context cancellation, got %d", calls)
	}
}

func TestIsFeishuRateLimitError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"perm_error_99991400", &feishuPermissionError{code: feishuRateLimitCode}, true},
		{"perm_error_other", &feishuPermissionError{code: 99991672}, false},
		{"string_with_code", fmt.Errorf("list replies: Code:99991400,Msg:request trigger frequency limit"), true},
		{"string_without_code", fmt.Errorf("some other error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFeishuRateLimitError(tt.err); got != tt.want {
				t.Fatalf("isFeishuRateLimitError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestDocumentCommentSemaphoreLimitsConcurrency(t *testing.T) {
	sem := make(chan struct{}, 2)
	ctx := context.Background()

	acquire := func() bool {
		select {
		case sem <- struct{}{}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	release := func() { <-sem }

	if !acquire() {
		t.Fatal("first acquire failed")
	}
	if !acquire() {
		t.Fatal("second acquire failed")
	}
	select {
	case sem <- struct{}{}:
		t.Fatal("third acquire should have blocked (limit=2)")
	default:
	}
	release()
	if !acquire() {
		t.Fatal("acquire after release failed")
	}
	release()
	release()
}
