package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/i18n"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/rs/zerolog/log"
)

const (
	turnBubblePath     = "/open-apis/im/v1/message_cot"
	turnBubbleTimeout  = 1500 * time.Millisecond
	turnBubbleInterval = time.Second
)

type turnBubbleDestination struct {
	ReceiveID       string `json:"receive_id"`
	OriginMessageID string `json:"origin_message_id,omitempty"`
	ReplyInThread   bool   `json:"reply_in_thread,omitempty"`
}

type turnBubbleIDs struct {
	COTID     string `json:"cot_id"`
	MessageID string `json:"message_id"`
}

type turnBubbleEvent struct {
	Type      string `json:"event_type"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

type turnBubbleAPI func(context.Context, *larkcore.ApiReq) (*larkcore.ApiResp, error)

type turnProcessBubble struct {
	api               turnBubbleAPI
	destination       turnBubbleDestination
	locale            i18n.Locale
	sessionID, turnID string

	// The stream processor owns the protocol state. Card rendering reads only
	// confirmed, after Feishu acknowledges the initial write.
	confirmed            atomic.Bool
	ids                  turnBubbleIDs
	stopped              bool
	lastPush             time.Time
	thought, sentThought string
	reasoningID          string
	reasoningCount       int
	tools                map[string]bool
}

func (b *Bridge) newTurnProcessBubble(chat *LarkChat, sessionID, turnID string) *turnProcessBubble {
	if b.client == nil || turnID == "" || sessionID == "" {
		return nil
	}
	target, direct, err := b.directReplyTargetForBinding(chat)
	if err != nil {
		return nil
	}
	var dest turnBubbleDestination
	var locale i18n.Locale
	switch {
	case direct && target != nil && (target.Kind == botSessionReplyDM || target.Kind == botSessionReplyGroup):
		dest.ReceiveID = target.ChatID
		if target.Kind == botSessionReplyDM {
			locale = b.managementLocale(target.OpenID)
		}
	case !direct && strings.HasPrefix(chat.RootMsgID, "om_"):
		dest = turnBubbleDestination{ReceiveID: chat.ChatID, OriginMessageID: chat.RootMsgID, ReplyInThread: true}
		locale = b.managementLocale(b.privateThreadRecipient(chat))
	}
	if dest.ReceiveID == "" {
		return nil
	}
	return &turnProcessBubble{
		api: func(ctx context.Context, req *larkcore.ApiReq) (*larkcore.ApiResp, error) {
			return b.client.Do(ctx, req)
		},
		destination: dest, locale: locale, sessionID: sessionID, turnID: turnID,
		tools: make(map[string]bool),
	}
}

func (b *turnProcessBubble) request(ctx context.Context, method, path string, query larkcore.QueryParams, body any, ids *turnBubbleIDs) error {
	resp, err := b.api(ctx, &larkcore.ApiReq{
		HttpMethod: method, ApiPath: path, QueryParams: query, Body: body,
		SupportedAccessTokenTypes: []larkcore.AccessTokenType{larkcore.AccessTokenTypeTenant},
	})
	if err != nil {
		return err
	}
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp == nil {
			return fmt.Errorf("missing native bubble response")
		}
		return fmt.Errorf("native bubble HTTP status %d", resp.StatusCode)
	}
	var result struct {
		Code *int          `json:"code"`
		Data turnBubbleIDs `json:"data"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return fmt.Errorf("decode native bubble response: %w", err)
	}
	if result.Code == nil {
		return fmt.Errorf("missing native bubble response code")
	}
	if *result.Code != 0 {
		return fmt.Errorf("native bubble API code %d", *result.Code)
	}
	if ids != nil {
		if result.Data.COTID == "" || result.Data.MessageID == "" {
			return fmt.Errorf("missing native bubble identifiers")
		}
		*ids = result.Data
	}
	return nil
}

func (b *turnProcessBubble) start(ctx context.Context) {
	if b != nil && !b.stopped && b.ids.COTID == "" {
		b.publish(ctx, nil, "")
	}
}

func (b *turnProcessBubble) observe(ctx context.Context, view *agentui.ActivityView, thinking, hidden bool) {
	if b == nil || b.stopped || view == nil || view.TurnID != b.turnID {
		return
	}
	if hidden {
		b.thought = ""
	} else if thinking {
		// Use the presenter's bounded, user-visible processing update.
		b.thought = strings.TrimSpace(view.CurrentStep)
	}
	if view.Status == "running" && time.Since(b.lastPush) >= turnBubbleInterval {
		b.publish(ctx, view, "")
	}
}

func (b *turnProcessBubble) finish(ctx context.Context, view *agentui.ActivityView, status string, hidden bool) {
	if b == nil || b.stopped {
		return
	}
	if hidden {
		b.thought = ""
	}
	b.publish(ctx, view, status)
	b.stopped = true
}

func (b *turnProcessBubble) publish(ctx context.Context, view *agentui.ActivityView, terminal string) {
	now := time.Now()
	var events []turnBubbleEvent
	add := func(kind string, fields map[string]string) {
		content, _ := json.Marshal(fields)
		events = append(events, turnBubbleEvent{Type: kind, Content: string(content), Timestamp: now.UnixMilli()})
	}
	addReasoning := func(text string) {
		b.reasoningCount++
		b.reasoningID = fmt.Sprintf("%s-reasoning-%d", b.turnID, b.reasoningCount)
		add("REASONING_MESSAGE_START", map[string]string{"messageId": b.reasoningID, "role": "reasoning"})
		add("REASONING_MESSAGE_CONTENT", map[string]string{"messageId": b.reasoningID, "delta": text})
		add("REASONING_MESSAGE_END", map[string]string{"messageId": b.reasoningID})
	}

	var tools []agentui.ToolEvent
	if view != nil && view.TurnID == b.turnID {
		tools = view.Tools
	}
	if b.thought != "" && b.thought != b.sentThought &&
		(terminal != "" || len(tools) > len(b.tools) || utf8.RuneCountInString(b.thought) >= 24) {
		addReasoning(b.thought)
		b.sentThought = b.thought
	}
	locale := b.locale
	if locale == "" {
		locale = i18n.Chinese
	}
	for _, tool := range tools {
		if tool.ID == "" {
			continue
		}
		settled, sent := b.tools[tool.ID]
		if settled {
			continue
		}
		if !sent {
			if b.reasoningID == "" {
				addReasoning(managementCatalog.Format(locale, "processing.bubble.tools"))
			}
			title := strings.TrimSpace(tool.Title)
			if title == "" {
				title = strings.TrimSpace(tool.Name)
			}
			if title == "" {
				title = managementCatalog.Format(locale, "processing.bubble.tools")
			}
			add("TOOL_CALL_START", map[string]string{
				"toolCallId": tool.ID, "toolCallName": tool.Name,
				"title": truncateCardText(title, 100), "icon": "default", "parentMessageId": b.reasoningID,
			})
			add("TOOL_CALL_END", map[string]string{"toolCallId": tool.ID})
			b.tools[tool.ID] = false
		}
		var status string
		switch toolDisplayStatusFor(tool.Status) {
		case toolDisplayFailed:
			status = "failed"
		case toolDisplaySucceeded:
			status = "done"
		default:
			if terminal != "" {
				status = "stopped"
			}
		}
		if status != "" {
			marker, _ := json.Marshal(map[string]string{
				"type": "text", "text": managementCatalog.Format(locale, "processing.bubble.tool."+status),
			})
			add("TOOL_CALL_RESULT", map[string]string{
				"toolCallId": tool.ID, "messageId": "result-" + tool.ID,
				"role": "tool", "content": string(marker),
			})
			b.tools[tool.ID] = true
		}
	}
	if len(events) == 0 && (view != nil || b.ids.COTID != "") && terminal == "" {
		return
	}
	prologueID := b.turnID + "-reasoning-0"
	run := map[string]string{"threadId": b.sessionID, "runId": b.turnID}
	if b.ids.COTID == "" {
		nodes := events
		events = nil
		add("RUN_STARTED", run)
		add("REASONING_START", map[string]string{"messageId": prologueID})
		events = append(events, nodes...)
	}
	if terminal != "" {
		reasoningID := b.reasoningID
		if reasoningID == "" {
			reasoningID = prologueID
		}
		add("REASONING_END", map[string]string{"messageId": reasoningID})
		if terminal == "done" {
			run["status"] = "done"
			add("RUN_FINISHED", run)
		}
	}

	// The native path is optional. Bound an entire publication to one deadline
	// so a slow endpoint cannot hold up result delivery for every tool.
	pushCtx, cancel := context.WithTimeout(ctx, turnBubbleTimeout)
	defer cancel()
	b.lastPush = now
	if b.ids.COTID == "" {
		if err := b.request(pushCtx, http.MethodPost, turnBubblePath, larkcore.QueryParams{"receive_id_type": {"chat_id"}}, b.destination, &b.ids); err != nil {
			b.fail(err)
			return
		}
	}
	const maxEventsPerRequest = 50
	for len(events) > 0 {
		n := min(len(events), maxEventsPerRequest)
		body := struct {
			turnBubbleIDs
			Events []turnBubbleEvent `json:"events"`
		}{b.ids, events[:n]}
		if err := b.request(pushCtx, http.MethodPut, turnBubblePath, nil, body, nil); err != nil {
			b.fail(err)
			return
		}
		events = events[n:]
	}
	if terminal != "" && terminal != "done" {
		if err := b.completeError(pushCtx); err != nil {
			b.fail(err)
			return
		}
	}
	b.confirmed.Store(true)
}

func (b *turnProcessBubble) completeError(ctx context.Context) error {
	return b.request(ctx, http.MethodPost, turnBubblePath+"/complete/"+url.PathEscape(b.ids.COTID),
		larkcore.QueryParams{"message_id": {b.ids.MessageID}, "reason": {"error"}}, nil, nil)
}

func (b *turnProcessBubble) fail(err error) {
	b.stopped = true
	b.confirmed.Store(false)
	log.Warn().Err(err).Str("turn_id", b.turnID).Msg("bridge: native processing bubble unavailable; retaining card details")
	b.abandon()
}

func (b *turnProcessBubble) abandon() {
	if b == nil || b.ids.COTID == "" || (b.stopped && b.confirmed.Load()) {
		return
	}
	id := b.ids
	// Mark the handle consumed before best-effort cleanup so a later turn
	// cannot retry the write or accidentally settle a different message.
	b.ids = turnBubbleIDs{}
	api := b.api
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), turnBubbleTimeout)
		defer cancel()
		abandoned := turnProcessBubble{api: api, ids: id}
		if err := abandoned.completeError(ctx); err != nil {
			log.Debug().Err(err).Msg("bridge: could not close native processing bubble")
		}
	}()
}
