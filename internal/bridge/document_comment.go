package bridge

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	larktypes "github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkdocx "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
	larkdrive "github.com/larksuite/oapi-sdk-go/v3/service/drive/v1"
	larkdrivev2 "github.com/larksuite/oapi-sdk-go/v3/service/drive/v2"
	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// documentCommentController owns document-comment stream state.
type documentCommentController struct {
	documentSourceMu           sync.Mutex
	documentMu                 sync.Mutex
	documentStreams            map[string]context.CancelFunc
	documentStreamReady        map[string]chan struct{}
	documentTurns              map[string]map[string]*documentTurnDelivery
	documentTurnTimeout        time.Duration
	documentCommentSemaphore   chan struct{}
	documentCommentsEnabled    bool
	documentCommentsConfigured bool
}

const (
	documentCommentEventType    = "drive.notice.comment_add_v1"
	documentReplyRuneLimit      = 1200
	documentTextRunRuneLimit    = 1000
	documentHistoryLimit        = 20
	documentContextNeighbors    = 2
	documentContextRuneLimit    = 4000
	documentProcessingReaction  = "OneSecond"
	documentReactionTimeout     = 5 * time.Second
	documentAttachReadyTimeout  = 2 * time.Second
	documentTurnDeliveryTimeout = 35 * time.Minute
)

type documentCommentRef struct {
	FileToken string
	FileType  string
	CommentID string
	ReplyID   string
}

type documentCommentReply struct {
	ReplyID   string
	OpenID    string
	Text      string
	CreatedAt int
}

type documentCommentSnapshot struct {
	Quote           string
	IsWhole         bool
	RequestText     string
	Replies         []documentCommentReply
	AnchorPrecision string
	AnchorBlockID   string
	DocumentContext []documentContextBlock
}

type documentContextBlock struct {
	BlockID  string
	Relation string
	Text     string
}

type documentTurnDelivery struct {
	EventID       string
	Ref           documentCommentRef
	Prompt        string
	TargetID      string
	SessionName   string
	OpenID        string
	ReactionAdded bool
	timeoutCancel context.CancelFunc
}

func (b *Bridge) ensureDocumentCommentSubscription(ctx context.Context) error {
	if b == nil {
		return fmt.Errorf("nil bridge")
	}
	if b.documentSubscribe != nil {
		return b.documentSubscribe(ctx)
	}
	if b.client == nil {
		return fmt.Errorf("nil Lark client")
	}
	req := larkdrive.NewSubscriptionUserReqBuilder().
		Body(larkdrive.NewSubscriptionUserReqBodyBuilder().
			EventType(documentCommentEventType).
			Build()).
		Build()
	resp, err := b.client.Drive.User.Subscription(ctx, req)
	if err != nil {
		return fmt.Errorf("subscribe document comments: %w", err)
	}
	if !resp.Success() {
		appID := ""
		if b.secrets != nil {
			appID = b.secrets.AppId
		}
		return feishuAPIResponseError(
			"subscribe document comments", appID, resp.CodeError, "docs:event:subscribe",
		)
	}
	return nil
}

func (b *Bridge) prepareDocumentCommentSource(ctx context.Context) error {
	if b == nil || !b.documentCommentsActive() {
		return nil
	}
	if err := b.ensureDocumentCommentSubscription(ctx); err != nil {
		b.setDocumentCommentsActive(false)
		b.observeCapabilityPermissionError(
			ctx, capabilityDocumentComments, "云文档评论", err, true,
		)
		return err
	}
	b.resolveCapabilityPermissionScopes(capabilityDocumentComments, "docs:event:subscribe")
	return nil
}

func (c *documentCommentController) documentCommentsActive() bool {
	if c == nil {
		return false
	}
	c.documentMu.Lock()
	defer c.documentMu.Unlock()
	return c.documentCommentsEnabled
}

func (c *documentCommentController) documentCommentsConfiguredForProfile() bool {
	if c == nil {
		return false
	}
	c.documentMu.Lock()
	defer c.documentMu.Unlock()
	return c.documentCommentsConfigured
}

func (c *documentCommentController) setDocumentCommentsActive(enabled bool) {
	if c == nil {
		return
	}
	c.documentMu.Lock()
	c.documentCommentsEnabled = enabled
	c.documentMu.Unlock()
}

func (c *documentCommentController) setDocumentCommentsConfigured(enabled bool) {
	if c == nil {
		return
	}
	c.documentMu.Lock()
	c.documentCommentsConfigured = enabled
	c.documentMu.Unlock()
}

func (b *Bridge) recoverDocumentCommentSource(ctx context.Context, notifyOwner bool) error {
	if b == nil || !b.documentCommentsConfiguredForProfile() || b.documentCommentsActive() {
		return nil
	}
	b.documentSourceMu.Lock()
	defer b.documentSourceMu.Unlock()
	if b.documentCommentsActive() {
		return nil
	}
	if err := b.ensureDocumentCommentSubscription(ctx); err != nil {
		issue, ok := capabilityPermissionIssueFromError(err, capabilityDocumentComments, "云文档评论")
		if ok {
			b.recordCapabilityPermissionIssue(issue)
			if auditErr := b.auditFeishuPermissionsWithObservedIssue(ctx, notifyOwner, &issue); auditErr != nil {
				log.Warn().Err(auditErr).Msg("bridge: reconcile document comment permission issue")
			}
		}
		return err
	}
	b.setDocumentCommentsActive(true)
	b.resolveCapabilityPermissionScopes(capabilityDocumentComments, "docs:event:subscribe")
	return nil
}

func (b *Bridge) setDocumentCommentsForProfile(ctx context.Context, enabled bool) error {
	if b == nil {
		return fmt.Errorf("nil bridge")
	}
	b.documentSourceMu.Lock()
	defer b.documentSourceMu.Unlock()

	if enabled && !b.documentCommentsActive() {
		if err := b.ensureDocumentCommentSubscription(ctx); err != nil {
			b.observeCapabilityPermissionError(
				ctx, capabilityDocumentComments, "云文档评论", err, false,
			)
			return err
		}
		b.resolveCapabilityPermissionScopes(capabilityDocumentComments, "docs:event:subscribe")
	}
	if err := config.SetBridgeFeishuDocumentComments(b.ProfileName(), enabled); err != nil {
		return fmt.Errorf("persist document comment setting: %w", err)
	}
	b.setDocumentCommentsConfigured(enabled)
	b.setDocumentCommentsActive(enabled)
	return nil
}

func (b *Bridge) handleDocumentComment(ctx context.Context, event *larktypes.CommentEvent) error {
	if b == nil || !b.documentCommentsActive() || event == nil {
		return nil
	}
	if !event.MentionedBot {
		return nil
	}
	openID := strings.TrimSpace(event.Operator.OpenID)
	if openID == "" || (b.secrets != nil && openID == strings.TrimSpace(b.secrets.BotOpenId)) {
		return nil
	}
	ref := documentCommentRef{
		FileToken: strings.TrimSpace(event.FileToken),
		FileType:  strings.TrimSpace(event.FileType),
		CommentID: strings.TrimSpace(event.CommentID),
		ReplyID:   strings.TrimSpace(event.ReplyID),
	}
	if ref.FileToken == "" || ref.FileType == "" || ref.CommentID == "" {
		return nil
	}
	eventID := documentCommentEventID(event)

	if !b.ownerConfirmed() || !b.canUseBridge(openID) {
		claimed, err := b.db.ClaimDocumentCommentEvent(DocumentCommentEvent{
			BridgeID: b.currentBridgeId(), EventID: eventID, FileToken: ref.FileToken,
			FileType: ref.FileType, CommentID: ref.CommentID, ReplyID: ref.ReplyID, OpenID: openID,
		})
		if err != nil || !claimed {
			return err
		}
		replyID, replyErr := b.replyDocumentComment(ctx, ref,
			"当前版本只有助手负责人可以使用文档评论功能。\n我没有读取文档内容，也没有启动任务。")
		if replyErr != nil {
			_ = b.db.MarkDocumentCommentEventFailed(b.currentBridgeId(), eventID)
			return replyErr
		}
		return b.db.MarkDocumentCommentEventDone(b.currentBridgeId(), eventID, replyID)
	}

	claimed, err := b.db.ClaimDocumentCommentEvent(DocumentCommentEvent{
		BridgeID: b.currentBridgeId(), EventID: eventID, FileToken: ref.FileToken,
		FileType: ref.FileType, CommentID: ref.CommentID, ReplyID: ref.ReplyID, OpenID: openID,
	})
	if err != nil || !claimed {
		return err
	}
	fail := func(cause error, notice string) error {
		b.observeCapabilityPermissionError(
			ctx, capabilityDocumentComments, "云文档评论", cause, true,
		)
		_ = b.db.MarkDocumentCommentEventFailed(b.currentBridgeId(), eventID)
		if strings.TrimSpace(notice) != "" {
			if _, replyErr := b.replyDocumentComment(ctx, ref, notice); replyErr != nil {
				b.observeCapabilityPermissionError(
					ctx, capabilityDocumentComments, "云文档评论", replyErr, true,
				)
			}
		}
		return cause
	}

	unlock := b.lockRoute(documentBindingKey(b.currentBridgeId(), ref.FileToken, ref.CommentID))
	defer unlock()

	if b.documentCommentSemaphore != nil {
		select {
		case b.documentCommentSemaphore <- struct{}{}:
			defer func() { <-b.documentCommentSemaphore }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	snapshot, err := b.fetchDocumentComment(ctx, ref)
	if err != nil {
		return fail(err, "我暂时无法读取这条评论，因此没有开始分析。\n请稍后重新 @我；如果持续失败，再检查助手的文档访问权限。")
	}
	request := documentCommentRequest(snapshot, ref.ReplyID, openID)
	if request == "" {
		return fail(fmt.Errorf("document comment request is empty"),
			"我无法读取这次 @我的具体请求，因此没有启动任务。\n请在本评论中补充文字后重新 @我。")
	}
	prompt := renderDocumentCommentPrompt(eventID, request, snapshot)

	binding, err := b.db.FindDocumentCommentSession(b.currentBridgeId(), ref.FileToken, ref.CommentID)
	if err != nil {
		return fail(err, "文档评论任务暂时不可用，请稍后重试。")
	}
	if binding == nil {
		binding, err = b.createDocumentCommentSession(ctx, ref, openID)
		if err != nil {
			return fail(err, "无法启动文档评论任务，请稍后重试或在助手私聊中检查运行状态。")
		}
	}

	delivery := &documentTurnDelivery{
		EventID: eventID, Ref: ref, Prompt: prompt, TargetID: binding.TargetID,
		SessionName: binding.SessionName, OpenID: openID,
	}
	delivery.ReactionAdded, err = b.acknowledgeDocumentTurn(ctx, ref)
	if err != nil {
		return fail(err, "")
	}
	b.registerDocumentTurn(delivery)
	ready := b.ensureDocumentStream(binding)
	b.waitForDocumentStreamReady(ctx, binding, ready)

	submitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = b.hub.Route(submitCtx, binding.TargetID, "Submit", &pb.SubmitRequest{
		Name: binding.SessionName, Text: prompt, Source: "lark", Async: true, Preempt: true,
	})
	if err != nil {
		b.removeDocumentTurn(delivery)
		routeErr := fail(err, "任务提交失败，请稍后重新 @我。")
		b.clearDocumentTurnReaction(delivery)
		return routeErr
	}
	return nil
}

func (b *Bridge) createDocumentCommentSession(ctx context.Context, ref documentCommentRef, openID string) (*DocumentCommentSession, error) {
	state, err := b.currentBotProfileState()
	if err != nil {
		return nil, err
	}
	agentRef, runtimeRef, modelRef, reasoningEffort, err := b.documentCommentExecution(state)
	if err != nil {
		return nil, err
	}
	launch, err := resolveBridgeAgentLaunchWithSelection(agentRef, b.db.workspaceDefaults.Args,
		runtimeRef, modelRef, reasoningEffort)
	if err != nil {
		return nil, err
	}
	target, targetErr := b.resolveRunTarget(b.db.workspaceDefaults.Target)
	if targetErr != "" {
		return nil, fmt.Errorf("%s", targetErr)
	}
	sessionName := documentSessionName(b.currentBridgeId(), ref.FileToken, ref.CommentID)
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	idleTimeout := int64(b.db.workspaceDefaults.IdleTimeoutSeconds)
	session, err := b.sessionStarter.Start(runCtx, sessionlaunch.StartRequest{
		TargetID: target.ID,
		Name:     sessionName,
		Launch:   launch,
		Workspace: sessionlaunch.WorkspaceRequest{
			Source: "feishu",
			Scope:  documentWorkspaceScope(b.currentBridgeId(), ref.FileToken),
		},
		IdleTimeoutSeconds: &idleTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("start document session: %w", err)
	}
	binding := &DocumentCommentSession{
		BridgeID: b.currentBridgeId(), FileToken: ref.FileToken, FileType: ref.FileType,
		CommentID: ref.CommentID, TargetID: target.ID, TargetName: target.Name,
		SessionName: session.Name, AgentProfile: session.AgentProfile, AgentType: session.AgentType,
		EffectiveModel: session.EffectiveModel, CreatedBy: openID, Status: DocumentCommentSessionActive,
	}
	if err := b.db.UpsertDocumentCommentSession(*binding); err != nil {
		return nil, err
	}
	return binding, nil
}

func (b *Bridge) documentCommentExecution(state BotProfileState) (string, string, string, string, error) {
	documentComments := config.GetBridgeFeishuDocumentCommentsForProfile(b.ProfileName())
	if documentComments.AgentProfileRef != "" {
		if err := validateDocumentCommentExecution(
			documentComments.AgentProfileRef,
			documentComments.RuntimeRef,
			documentComments.ModelRef,
			documentComments.ReasoningEffort,
		); err != nil {
			return "", "", "", "", err
		}
		return documentComments.AgentProfileRef, documentComments.RuntimeRef, documentComments.ModelRef, documentComments.ReasoningEffort, nil
	}
	return effectiveBotDefaultAgentRef(state.DefaultAgent), strings.TrimSpace(state.DefaultRuntimeRef), b.effectiveBotLaunchModel(state.DefaultRuntimeRef, state.DefaultModelRef), strings.TrimSpace(state.DefaultReasoningEffort), nil
}

func documentSessionName(bridgeID, fileToken, commentID string) string {
	sum := sha1.Sum([]byte(documentBindingKey(bridgeID, fileToken, commentID)))
	return fmt.Sprintf("feishu-doc-%x", sum[:6])
}

func documentWorkspaceScope(bridgeID, fileToken string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(bridgeID) + "\n" + strings.TrimSpace(fileToken)))
	return fmt.Sprintf("document-%x", sum[:8])
}

func documentBindingKey(bridgeID, fileToken, commentID string) string {
	return strings.TrimSpace(bridgeID) + "\n" + strings.TrimSpace(fileToken) + "\n" + strings.TrimSpace(commentID)
}

func documentCommentEventID(event *larktypes.CommentEvent) string {
	if eventID := strings.TrimSpace(event.EventID); eventID != "" {
		return eventID
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s\n%s\n%s\n%s\n%d",
		event.FileToken, event.CommentID, event.ReplyID, event.Operator.OpenID, event.Timestamp)))
	return fmt.Sprintf("derived-%x", sum[:])
}

func (b *Bridge) fetchDocumentComment(ctx context.Context, ref documentCommentRef) (documentCommentSnapshot, error) {
	if b.documentCommentFetch != nil {
		return b.documentCommentFetch(ctx, ref)
	}
	if b.client == nil {
		return documentCommentSnapshot{}, fmt.Errorf("nil Lark client")
	}
	req := larkdrive.NewBatchQueryFileCommentReqBuilder().
		FileToken(ref.FileToken).
		FileType(ref.FileType).
		UserIdType("open_id").
		Body(larkdrive.NewBatchQueryFileCommentReqBodyBuilder().
			CommentIds([]string{ref.CommentID}).
			Build()).
		Build()
	var resp *larkdrive.BatchQueryFileCommentResp
	if err := retryFeishuRateLimit(ctx, "query document comment", func() error {
		var apiErr error
		resp, apiErr = b.client.Drive.FileComment.BatchQuery(ctx, req)
		if apiErr != nil {
			return apiErr
		}
		if !resp.Success() {
			appID := ""
			if b.secrets != nil {
				appID = b.secrets.AppId
			}
			return feishuAPIResponseError(
				"query document comment", appID, resp.CodeError, "docs:document.comment:read",
			)
		}
		return nil
	}); err != nil {
		return documentCommentSnapshot{}, fmt.Errorf("query document comment: %w", err)
	}
	b.resolveCapabilityPermissionScopes(capabilityDocumentComments, "docs:document.comment:read")
	if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
		return documentCommentSnapshot{}, fmt.Errorf("document comment not found")
	}
	item := resp.Data.Items[0]
	snapshot := documentCommentSnapshot{
		Quote:   stringPtr(item.Quote),
		IsWhole: item.IsWhole != nil && *item.IsWhole,
	}
	seenReplies := make(map[string]struct{})
	if item.ReplyList != nil {
		for _, reply := range item.ReplyList.Replies {
			appendDocumentCommentReply(&snapshot, reply, ref.ReplyID, seenReplies)
		}
	}
	if item.HasMore == nil || !*item.HasMore {
		finalizeDocumentCommentSnapshot(&snapshot, ref.ReplyID, b.secrets)
		b.enrichDocumentCommentContext(ctx, ref, &snapshot)
		return snapshot, nil
	}

	replyReqBuilder := larkdrive.NewListFileCommentReplyReqBuilder().
		FileToken(ref.FileToken).
		CommentId(ref.CommentID).
		FileType(ref.FileType).
		UserIdType("open_id").
		PageSize(50)
	if pageToken := stringPtr(item.PageToken); pageToken != "" {
		replyReqBuilder.PageToken(pageToken)
	}
	replyReq := replyReqBuilder.Build()
	iterator, err := b.client.Drive.FileCommentReply.ListByIterator(ctx, replyReq)
	if err != nil {
		return documentCommentSnapshot{}, fmt.Errorf("list document comment replies: %w", err)
	}
	for {
		var ok bool
		var reply *larkdrive.FileCommentReply
		if err := retryFeishuRateLimit(ctx, "list document comment replies", func() error {
			var iterErr error
			ok, reply, iterErr = iterator.Next()
			return iterErr
		}); err != nil {
			return documentCommentSnapshot{}, fmt.Errorf("list document comment replies: %w", err)
		}
		if !ok {
			break
		}
		if reply == nil {
			continue
		}
		appendDocumentCommentReply(&snapshot, reply, ref.ReplyID, seenReplies)
	}
	finalizeDocumentCommentSnapshot(&snapshot, ref.ReplyID, b.secrets)
	b.enrichDocumentCommentContext(ctx, ref, &snapshot)
	return snapshot, nil
}

func (b *Bridge) enrichDocumentCommentContext(ctx context.Context, ref documentCommentRef, snapshot *documentCommentSnapshot) {
	if b == nil || b.client == nil || snapshot == nil || !strings.EqualFold(ref.FileType, "docx") {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req := larkdocx.NewListDocumentBlockReqBuilder().
		DocumentId(ref.FileToken).
		DocumentRevisionId(-1).
		UserIdType("open_id").
		PageSize(500).
		Build()
	iterator, err := b.client.Docx.DocumentBlock.ListByIterator(readCtx, req)
	if err != nil {
		log.Warn().Err(err).Str("comment_id", ref.CommentID).
			Msg("bridge: document comment context unavailable; using quote only")
		return
	}
	var blocks []*larkdocx.Block
	for {
		var ok bool
		var block *larkdocx.Block
		if err := retryFeishuRateLimit(readCtx, "list document blocks", func() error {
			var iterErr error
			ok, block, iterErr = iterator.Next()
			return iterErr
		}); err != nil {
			log.Warn().Err(err).Str("comment_id", ref.CommentID).
				Msg("bridge: document comment context unavailable; using quote only")
			return
		}
		if !ok {
			break
		}
		if block != nil {
			blocks = append(blocks, block)
		}
	}
	b.resolveCapabilityPermissionScopes(capabilityDocumentComments, "docx:document:readonly")
	if snapshot.IsWhole {
		snapshot.AnchorPrecision = "whole_document_comment"
		snapshot.DocumentContext = documentContextExcerpt(blocks, 0, minInt(len(blocks)-1, 8), nil)
		return
	}
	snapshot.AnchorPrecision, snapshot.AnchorBlockID, snapshot.DocumentContext =
		documentCommentContextFromBlocks(blocks, ref.CommentID, snapshot.Quote)
}

func documentCommentContextFromBlocks(blocks []*larkdocx.Block, commentID, quote string) (string, string, []documentContextBlock) {
	commentID = strings.TrimSpace(commentID)
	quote = strings.TrimSpace(quote)
	if commentID == "" {
		return "quote_only", "", nil
	}
	anchors := make(map[int]bool)
	first, last := -1, -1
	elementMatched := false
	anchorID := ""
	for i, block := range blocks {
		if block == nil {
			continue
		}
		blockMatched := containsString(block.CommentIds, commentID)
		elementMatch := documentBlockElementHasComment(block, commentID)
		if !blockMatched && !elementMatch {
			continue
		}
		anchors[i] = true
		if first < 0 {
			first = i
			anchorID = stringPtr(block.BlockId)
		}
		last = i
		if elementMatch {
			text := strings.TrimSpace(documentBlockText(block))
			if quote == "" || strings.Contains(text, quote) || strings.Contains(quote, text) {
				elementMatched = true
			}
		}
	}
	if first < 0 {
		return "quote_only", "", nil
	}
	precision := "block"
	if elementMatched {
		precision = "element"
	}
	return precision, anchorID, documentContextExcerpt(
		blocks,
		maxInt(0, first-documentContextNeighbors),
		minInt(len(blocks)-1, last+documentContextNeighbors),
		anchors,
	)
}

func documentContextExcerpt(blocks []*larkdocx.Block, start, end int, anchors map[int]bool) []documentContextBlock {
	if start < 0 || end < start || start >= len(blocks) {
		return nil
	}
	end = minInt(end, len(blocks)-1)
	firstAnchor, lastAnchor := -1, -1
	for i := start; i <= end; i++ {
		if anchors[i] {
			if firstAnchor < 0 {
				firstAnchor = i
			}
			lastAnchor = i
		}
	}
	type contextCandidate struct {
		index int
		block documentContextBlock
	}
	var candidates []contextCandidate
	var anchorCandidates []int
	used := 0
	for i := start; i <= end; i++ {
		text := strings.TrimSpace(documentBlockText(blocks[i]))
		if text == "" {
			continue
		}
		relation := "document"
		switch {
		case anchors[i]:
			relation = "anchor"
		case firstAnchor >= 0 && i < firstAnchor:
			relation = "before"
		case lastAnchor >= 0 && i > lastAnchor:
			relation = "after"
		}
		candidates = append(candidates, contextCandidate{
			index: i,
			block: documentContextBlock{
				BlockID:  stringPtr(blocks[i].BlockId),
				Relation: relation,
				Text:     text,
			},
		})
		if anchors[i] {
			anchorCandidates = append(anchorCandidates, len(candidates)-1)
		}
	}

	selected := make(map[int]string, len(candidates))
	addCandidate := func(candidateIndex, reserved int) {
		if used >= documentContextRuneLimit {
			return
		}
		runes := []rune(candidates[candidateIndex].block.Text)
		remaining := documentContextRuneLimit - used - reserved
		if remaining <= 0 {
			return
		}
		if len(runes) > remaining {
			if remaining == 1 {
				runes = []rune("…")
			} else {
				runes = append(runes[:remaining-1], '…')
			}
		}
		selected[candidateIndex] = strings.TrimSpace(string(runes))
		used += len(runes)
	}
	if len(anchorCandidates) == 0 {
		for i := range candidates {
			addCandidate(i, 0)
		}
	} else {
		// Anchor text is the semantic center of a document comment. Reserve at
		// least one rune for every remaining anchor before spending the budget
		// on neighboring blocks.
		for i, candidateIndex := range anchorCandidates {
			addCandidate(candidateIndex, len(anchorCandidates)-i-1)
		}
		for distance := 1; used < documentContextRuneLimit; distance++ {
			added := false
			for i, candidate := range candidates {
				if _, ok := selected[i]; ok || anchors[candidate.index] {
					continue
				}
				if candidate.index >= firstAnchor-distance && candidate.index <= lastAnchor+distance {
					addCandidate(i, 0)
					added = true
				}
			}
			if !added {
				break
			}
		}
	}

	var out []documentContextBlock
	for i, candidate := range candidates {
		text, ok := selected[i]
		if !ok || text == "" {
			continue
		}
		candidate.block.Text = text
		out = append(out, candidate.block)
	}
	return out
}

func documentBlockText(block *larkdocx.Block) string {
	if block == nil {
		return ""
	}
	texts := []*larkdocx.Text{
		block.Page, block.Text, block.Heading1, block.Heading2, block.Heading3,
		block.Heading4, block.Heading5, block.Heading6, block.Heading7,
		block.Heading8, block.Heading9, block.Bullet, block.Ordered, block.Code,
		block.Quote, block.Equation, block.Todo,
	}
	for _, text := range texts {
		if text == nil {
			continue
		}
		var out strings.Builder
		for _, element := range text.Elements {
			if element == nil {
				continue
			}
			switch {
			case element.TextRun != nil:
				out.WriteString(stringPtr(element.TextRun.Content))
			case element.MentionDoc != nil:
				out.WriteString(stringPtr(element.MentionDoc.Title))
			case element.MentionUser != nil:
				out.WriteString("@")
				out.WriteString(stringPtr(element.MentionUser.UserId))
			case element.Equation != nil:
				out.WriteString(stringPtr(element.Equation.Content))
			}
		}
		if result := strings.TrimSpace(out.String()); result != "" {
			return result
		}
	}
	return ""
}

func documentBlockElementHasComment(block *larkdocx.Block, commentID string) bool {
	if block == nil {
		return false
	}
	texts := []*larkdocx.Text{
		block.Page, block.Text, block.Heading1, block.Heading2, block.Heading3,
		block.Heading4, block.Heading5, block.Heading6, block.Heading7,
		block.Heading8, block.Heading9, block.Bullet, block.Ordered, block.Code,
		block.Quote, block.Equation, block.Todo,
	}
	for _, text := range texts {
		if text == nil {
			continue
		}
		for _, element := range text.Elements {
			if element == nil {
				continue
			}
			var style *larkdocx.TextElementStyle
			switch {
			case element.TextRun != nil:
				style = element.TextRun.TextElementStyle
			case element.MentionDoc != nil:
				style = element.MentionDoc.TextElementStyle
			case element.MentionUser != nil:
				style = element.MentionUser.TextElementStyle
			case element.Equation != nil:
				style = element.Equation.TextElementStyle
			}
			if style != nil && containsString(style.CommentIds, commentID) {
				return true
			}
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func appendDocumentCommentReply(snapshot *documentCommentSnapshot, reply *larkdrive.FileCommentReply, requestReplyID string, seen map[string]struct{}) {
	if snapshot == nil || reply == nil {
		return
	}
	replyID := stringPtr(reply.ReplyId)
	if replyID != "" {
		if _, ok := seen[replyID]; ok {
			return
		}
		seen[replyID] = struct{}{}
	}
	text := documentReplyText(reply.Content)
	snapshot.Replies = append(snapshot.Replies, documentCommentReply{
		ReplyID:   replyID,
		OpenID:    stringPtr(reply.UserId),
		Text:      text,
		CreatedAt: intPtrValue(reply.CreateTime),
	})
	if requestReplyID != "" && replyID == requestReplyID {
		snapshot.RequestText = text
	}
}

func finalizeDocumentCommentSnapshot(snapshot *documentCommentSnapshot, requestReplyID string, secrets *Secrets) {
	if snapshot == nil {
		return
	}
	sort.SliceStable(snapshot.Replies, func(i, j int) bool {
		return snapshot.Replies[i].CreatedAt < snapshot.Replies[j].CreatedAt
	})
	if requestReplyID == "" {
		ownerOpenID := ""
		if secrets != nil {
			ownerOpenID = strings.TrimSpace(secrets.OpenId)
		}
		for i := len(snapshot.Replies) - 1; i >= 0; i-- {
			if snapshot.Replies[i].OpenID == ownerOpenID &&
				strings.TrimSpace(snapshot.Replies[i].Text) != "" {
				snapshot.RequestText = snapshot.Replies[i].Text
				break
			}
		}
	}
	if len(snapshot.Replies) > documentHistoryLimit {
		snapshot.Replies = snapshot.Replies[len(snapshot.Replies)-documentHistoryLimit:]
	}
}

func intPtrValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func documentReplyText(content *larkdrive.ReplyContent) string {
	if content == nil {
		return ""
	}
	var out strings.Builder
	for _, element := range content.Elements {
		if element == nil || element.TextRun == nil || element.TextRun.Text == nil {
			continue
		}
		out.WriteString(*element.TextRun.Text)
	}
	return strings.TrimSpace(out.String())
}

func documentCommentRequest(snapshot documentCommentSnapshot, replyID, openID string) string {
	if request := strings.TrimSpace(snapshot.RequestText); request != "" {
		return request
	}
	replyID = strings.TrimSpace(replyID)
	if replyID != "" {
		for _, reply := range snapshot.Replies {
			if reply.ReplyID == replyID {
				return strings.TrimSpace(reply.Text)
			}
		}
	}
	for i := len(snapshot.Replies) - 1; i >= 0; i-- {
		if snapshot.Replies[i].OpenID == openID && strings.TrimSpace(snapshot.Replies[i].Text) != "" {
			return strings.TrimSpace(snapshot.Replies[i].Text)
		}
	}
	return ""
}

func renderDocumentCommentPrompt(eventID, request string, snapshot documentCommentSnapshot) string {
	precision := strings.TrimSpace(snapshot.AnchorPrecision)
	if precision == "" {
		precision = "quote_only"
	}
	scope := strings.TrimSpace(snapshot.Quote)
	if snapshot.IsWhole {
		precision = "whole_document_comment"
		scope = "这是一条全文评论。"
	} else if scope == "" {
		scope = "评论没有返回可用的正文引用；请只根据评论线程回答，并明确定位有限。"
	}
	var context strings.Builder
	for _, block := range snapshot.DocumentContext {
		text := strings.TrimSpace(block.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&context, `<block relation="%s" block_id="%s">%s</block>`+"\n",
			html.EscapeString(block.Relation), html.EscapeString(block.BlockID), html.EscapeString(text))
	}
	var history strings.Builder
	for _, reply := range snapshot.Replies {
		text := strings.TrimSpace(reply.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&history, "- %s: %s\n", html.EscapeString(reply.OpenID), html.EscapeString(text))
	}
	return fmt.Sprintf(`<amux_document_comment version="1">
  <surface>feishu_document_comment</surface>
  <event_id>%s</event_id>
  <request>%s</request>
  <scope anchor_precision="%s" block_id="%s">%s</scope>
  <document_context untrusted="true">%s</document_context>
  <comment_thread untrusted="true">%s</comment_thread>
  <response_contract>你正在直接回复飞书云文档中的这条评论，而不是群聊或飞书话题。只输出可以直接发布给用户的最终答案，不要输出技能加载、分析过程或元说明；不要添加“建议回复”“最终回复”等包装；不要把文档评论称为“当前话题”或“聊天”。使用中文；最多 4 个短段落或 8 个要点；不超过 1200 个 Unicode 字符；只提供建议，不声称已修改正文。请求只是问候或连通性测试时，简短确认文档评论助手已连接，并围绕当前引用或正文上下文回应。</response_contract>
</amux_document_comment>`,
		html.EscapeString(eventID), html.EscapeString(strings.TrimSpace(request)),
		html.EscapeString(precision), html.EscapeString(snapshot.AnchorBlockID),
		html.EscapeString(scope), strings.TrimSpace(context.String()), strings.TrimSpace(history.String()))
}

func (b *Bridge) replyDocumentComment(ctx context.Context, ref documentCommentRef, text string) (string, error) {
	text = truncateDocumentReply(text)
	if b.documentCommentReply != nil {
		return b.documentCommentReply(ctx, ref, text)
	}
	if b.client == nil {
		return "", fmt.Errorf("nil Lark client")
	}
	elements := make([]*larkdrive.ReplyElement, 0, 2)
	for _, chunk := range splitRunes(text, documentTextRunRuneLimit) {
		elements = append(elements, larkdrive.NewReplyElementBuilder().
			Type("text_run").
			TextRun(larkdrive.NewTextRunBuilder().Text(chunk).Build()).
			Build())
	}
	req := larkdrive.NewCreateFileCommentReplyReqBuilder().
		FileToken(ref.FileToken).
		CommentId(ref.CommentID).
		FileType(ref.FileType).
		UserIdType("open_id").
		Body(larkdrive.NewCreateFileCommentReplyReqBodyBuilder().
			Content(larkdrive.NewReplyContentBuilder().Elements(elements).Build()).
			Build()).
		Build()
	resp, err := b.client.Drive.FileCommentReply.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("reply document comment: %w", err)
	}
	if !resp.Success() {
		appID := ""
		if b.secrets != nil {
			appID = b.secrets.AppId
		}
		return "", feishuAPIResponseError(
			"reply document comment", appID, resp.CodeError, "docs:document.comment:create",
		)
	}
	b.resolveCapabilityPermissionScopes(capabilityDocumentComments, "docs:document.comment:create")
	if resp.Data == nil {
		return "", fmt.Errorf("reply document comment: empty response data")
	}
	replyID := strings.TrimSpace(stringPtr(resp.Data.ReplyId))
	if replyID == "" {
		return "", fmt.Errorf("reply document comment: empty reply_id")
	}
	return replyID, nil
}

func (b *Bridge) acknowledgeDocumentTurn(ctx context.Context, ref documentCommentRef) (bool, error) {
	if ref.ReplyID != "" {
		if err := b.updateDocumentCommentReaction(ctx, ref, "add", documentProcessingReaction); err == nil {
			return true, nil
		} else {
			log.Warn().Err(err).Str("comment_id", ref.CommentID).Str("reply_id", ref.ReplyID).
				Msg("bridge: document comment processing reaction unavailable; using text acknowledgement")
		}
	}
	_, err := b.replyDocumentComment(ctx, ref,
		"收到，正在分析当前评论关联的内容及必要上下文。\n本次只提供建议，不会通过评论功能修改正文。")
	return false, err
}

func (b *Bridge) updateDocumentCommentReaction(
	ctx context.Context, ref documentCommentRef, action, reactionType string,
) error {
	if b.documentCommentReact != nil {
		return b.documentCommentReact(ctx, ref, action, reactionType)
	}
	if b.client == nil {
		return fmt.Errorf("nil Lark client")
	}
	req := larkdrivev2.NewUpdateReactionCommentReactionReqBuilder().
		FileToken(ref.FileToken).
		FileType(ref.FileType).
		Body(larkdrivev2.NewUpdateReactionCommentReactionReqBodyBuilder().
			Action(action).
			ReplyId(ref.ReplyID).
			ReactionType(reactionType).
			Build()).
		Build()
	resp, err := b.client.Drive.V2.CommentReaction.UpdateReaction(ctx, req)
	if err != nil {
		return fmt.Errorf("update document comment reaction: %w", err)
	}
	if !resp.Success() {
		appID := ""
		if b.secrets != nil {
			appID = b.secrets.AppId
		}
		return feishuAPIResponseError(
			"update document comment reaction", appID, resp.CodeError, "docs:document.comment:create",
		)
	}
	b.resolveCapabilityPermissionScopes(capabilityDocumentComments, "docs:document.comment:create")
	return nil
}

func truncateDocumentReply(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= documentReplyRuneLimit {
		return text
	}
	return strings.TrimSpace(string(runes[:documentReplyRuneLimit-1])) + "…"
}

func splitRunes(text string, limit int) []string {
	if text == "" || limit <= 0 {
		return nil
	}
	var out []string
	for len(text) > 0 {
		if utf8.RuneCountInString(text) <= limit {
			out = append(out, text)
			break
		}
		runes := []rune(text)
		out = append(out, string(runes[:limit]))
		text = string(runes[limit:])
	}
	return out
}

func documentStreamKey(targetID, sessionName string) string {
	return strings.TrimSpace(targetID) + "\x00" + strings.TrimSpace(sessionName)
}

func (b *Bridge) registerDocumentTurn(delivery *documentTurnDelivery) {
	if delivery == nil {
		return
	}
	key := documentStreamKey(delivery.TargetID, delivery.SessionName)
	b.documentMu.Lock()
	defer b.documentMu.Unlock()
	if b.documentTurns == nil {
		b.documentTurns = make(map[string]map[string]*documentTurnDelivery)
	}
	if b.documentTurns[key] == nil {
		b.documentTurns[key] = make(map[string]*documentTurnDelivery)
	}
	timeout := b.documentTurnTimeout
	if timeout <= 0 {
		timeout = documentTurnDeliveryTimeout
	}
	timeoutCtx, cancel := context.WithCancel(context.Background())
	delivery.timeoutCancel = cancel
	b.documentTurns[key][delivery.EventID] = delivery
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timeoutCtx.Done():
			return
		case <-timer.C:
			b.finishDocumentTurn(delivery,
				"⚠️ 当前文档评论任务处理超时，请稍后在本评论中重新 @我。")
		}
	}()
}

func (c *documentCommentController) removeDocumentTurn(delivery *documentTurnDelivery) {
	if delivery == nil {
		return
	}
	key := documentStreamKey(delivery.TargetID, delivery.SessionName)
	c.documentMu.Lock()
	defer c.documentMu.Unlock()
	if delivery.timeoutCancel != nil {
		delivery.timeoutCancel()
		delivery.timeoutCancel = nil
	}
	delete(c.documentTurns[key], delivery.EventID)
	if len(c.documentTurns[key]) == 0 {
		delete(c.documentTurns, key)
	}
}

func (c *documentCommentController) matchingDocumentTurn(key, prompt string) *documentTurnDelivery {
	c.documentMu.Lock()
	defer c.documentMu.Unlock()
	for _, delivery := range c.documentTurns[key] {
		if delivery != nil && delivery.Prompt == prompt {
			return delivery
		}
	}
	return nil
}

func (c *documentCommentController) pendingDocumentTurns(key string) []*documentTurnDelivery {
	c.documentMu.Lock()
	defer c.documentMu.Unlock()
	out := make([]*documentTurnDelivery, 0, len(c.documentTurns[key]))
	for _, delivery := range c.documentTurns[key] {
		if delivery == nil {
			continue
		}
		out = append(out, delivery)
	}
	return out
}

func (b *Bridge) waitForDocumentStreamReady(
	ctx context.Context, binding *DocumentCommentSession, ready <-chan struct{},
) {
	if b == nil || binding == nil || ready == nil {
		return
	}
	timer := time.NewTimer(documentAttachReadyTimeout)
	defer timer.Stop()
	select {
	case <-ready:
		return
	case <-ctx.Done():
		return
	case <-timer.C:
		log.Warn().Str("session", binding.SessionName).Str("target_id", binding.TargetID).
			Msg("bridge: timed out waiting for document Attach readiness; submitting with compatibility fallback")
	}
}

func (b *Bridge) ensureDocumentStream(binding *DocumentCommentSession) <-chan struct{} {
	if b == nil || binding == nil {
		return nil
	}
	key := documentStreamKey(binding.TargetID, binding.SessionName)
	b.documentMu.Lock()
	if _, ok := b.documentStreams[key]; ok {
		ready := b.documentStreamReady[key]
		b.documentMu.Unlock()
		return ready
	}
	streamCtx, cancel := context.WithCancel(context.Background())
	if b.documentStreams == nil {
		b.documentStreams = make(map[string]context.CancelFunc)
	}
	if b.documentStreamReady == nil {
		b.documentStreamReady = make(map[string]chan struct{})
	}
	ready := make(chan struct{})
	b.documentStreams[key] = cancel
	b.documentStreamReady[key] = ready
	b.documentMu.Unlock()

	go func() {
		var readyOnce sync.Once
		markReady := func() { readyOnce.Do(func() { close(ready) }) }
		defer markReady()
		defer func() {
			b.documentMu.Lock()
			if b.documentStreamReady[key] == ready {
				delete(b.documentStreams, key)
				delete(b.documentStreamReady, key)
			}
			b.documentMu.Unlock()
		}()
		presenter := agentui.NewPresenter()
		var active *documentTurnDelivery
		var output strings.Builder
		var finalText string
		var failure bool
		err := b.hub.RouteStream(streamCtx, binding.TargetID, "Attach",
			&pb.AttachRequest{Name: binding.SessionName, TimelineMode: pb.AttachRequest_TAIL},
			func(msg *pb.TunnelMsg) error {
				var attach pb.AttachEvent
				if json.Unmarshal(msg.Payload, &attach) != nil {
					return nil
				}
				markReady()
				for _, uiEvent := range agentui.FromAttachEvent(&attach) {
					for _, action := range presenter.Consume(uiEvent) {
						switch action.Kind {
						case agentui.ActionTurnStarted:
							if action.Source != agentui.SourceFeishu {
								active = nil
								continue
							}
							active = b.matchingDocumentTurn(key, action.Text)
							if active != nil {
								output.Reset()
								finalText = ""
								failure = false
							}
						case agentui.ActionReplyAppend:
							if active != nil {
								output.WriteString(action.Text)
							}
						case agentui.ActionReplyReplace, agentui.ActionReplyReconcileFinalMessage:
							if active != nil && strings.TrimSpace(action.Text) != "" {
								finalText = action.Text
							}
						case agentui.ActionPermissionRequest:
							if active != nil {
								b.handleDocumentPermission(active, action.Permission)
							}
						case agentui.ActionError:
							if active != nil {
								failure = true
							}
						case agentui.ActionTurnFinished:
							if active == nil {
								continue
							}
							text := strings.TrimSpace(finalText)
							if text == "" {
								text = strings.TrimSpace(output.String())
							}
							if failure || action.Turn.State == "failed" {
								text = "⚠️ Agent 执行出错，请稍后在本评论中重新 @我。"
							} else if text == "" {
								text = "任务已结束，但没有产生可展示的结果。请在本评论中补充要求后重新 @我。"
							}
							b.finishDocumentTurn(active, text)
							active = nil
						}
					}
				}
				return nil
			})
		if err != nil && streamCtx.Err() == nil {
			log.Warn().Err(err).Str("session", binding.SessionName).
				Msg("bridge: document comment stream ended")
		}
		if err != nil && streamCtx.Err() == nil {
			for _, delivery := range b.pendingDocumentTurns(key) {
				b.finishDocumentTurn(delivery,
					"⚠️ 当前文档评论任务的输出连接已中断，请稍后在本评论中重新 @我。")
			}
		}
	}()
	return ready
}

func (b *Bridge) finishDocumentTurn(delivery *documentTurnDelivery, text string) {
	if delivery == nil {
		return
	}
	claimed, err := b.db.TryBeginDocumentCommentEventFinalization(
		b.currentBridgeId(), delivery.EventID,
	)
	if err != nil {
		log.Warn().Err(err).Str("event_id", delivery.EventID).
			Msg("bridge: claim document comment finalization failed")
		return
	}
	if !claimed {
		b.removeDocumentTurn(delivery)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	replyID, replyErr := b.replyDocumentComment(
		ctx, delivery.Ref, normalizeDocumentFinalReply(text),
	)
	if replyErr != nil {
		_ = b.db.MarkDocumentCommentEventFailed(b.currentBridgeId(), delivery.EventID)
	} else {
		_ = b.db.MarkDocumentCommentEventDone(b.currentBridgeId(), delivery.EventID, replyID)
	}
	b.clearDocumentTurnReaction(delivery)
	b.removeDocumentTurn(delivery)
}

func (b *Bridge) clearDocumentTurnReaction(delivery *documentTurnDelivery) {
	if delivery == nil || !delivery.ReactionAdded {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), documentReactionTimeout)
	defer cancel()
	if err := b.updateDocumentCommentReaction(
		ctx, delivery.Ref, "delete", documentProcessingReaction,
	); err != nil {
		log.Debug().Err(err).Str("comment_id", delivery.Ref.CommentID).
			Str("reply_id", delivery.Ref.ReplyID).
			Msg("bridge: remove document comment processing reaction failed")
	}
}

func normalizeDocumentFinalReply(text string) string {
	text = strings.TrimSpace(text)
	firstLine, rest, hasRest := strings.Cut(text, "\n")
	labelPart := firstLine
	inline := ""
	before, after, hasColon := strings.Cut(labelPart, "：")
	if !hasColon {
		before, after, hasColon = strings.Cut(labelPart, ":")
	}
	if hasColon {
		inline = strings.Trim(strings.TrimSpace(after), "*_`")
		labelPart = before
	}
	label := strings.Trim(strings.TrimSpace(labelPart), "#*_` ")
	if label == "建议回复" || label == "最终回复" {
		if hasRest {
			if inline != "" {
				return strings.TrimSpace(inline + "\n" + rest)
			}
			return strings.TrimSpace(rest)
		}
		if inline != "" {
			return strings.TrimSpace(inline)
		}
	}
	return text
}

func (b *Bridge) handleDocumentPermission(delivery *documentTurnDelivery, permission *agentui.PermissionEvent) {
	if delivery == nil || permission == nil || permission.RequestID == "" {
		return
	}
	key, shouldSend := b.beginPermissionRequest(&pendingPermission{
		PermissionId: permission.RequestID, RootMsgID: documentBindingKey(
			b.currentBridgeId(), delivery.Ref.FileToken, delivery.Ref.CommentID),
		TargetID: delivery.TargetID, SessionName: delivery.SessionName, OpenId: delivery.OpenID,
	})
	if !shouldSend {
		return
	}
	content := renderPermissionRequestCard(delivery.TargetID, delivery.SessionName,
		permission.RequestID, permissionPayloadFromEvent(permission), delivery.OpenID)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var messageID string
	var err error
	if b.dmInteractive != nil {
		messageID, _, err = b.dmInteractive(ctx, delivery.OpenID, content)
	} else {
		messageID, _, err = b.sendInteractiveToDM(ctx, delivery.OpenID, content, "document permission card")
	}
	if err != nil {
		b.forgetPermissionRequest(key)
		return
	}
	b.completePermissionRequest(key, messageID)
	_, _ = b.replyDocumentComment(ctx, delivery.Ref, "任务需要你的授权，请到助手私聊完成后继续。")
}
