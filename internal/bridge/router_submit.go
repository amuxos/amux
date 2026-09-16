package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/rs/zerolog/log"
)

func (b *Bridge) submitToSession(ctx context.Context, chat *LarkChat, text string) error {
	_, err := b.submitToSessionForMessages(ctx, chat, text, nil)
	return err
}
func (b *Bridge) submitSessionControl(ctx context.Context, chat *LarkChat, control string) error {
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := b.hub.Route(sendCtx, chat.TargetID, "Submit", &pb.SubmitRequest{Name: chat.SessionName, Text: "/" + control, Source: "lark", Async: true, Control: control})
	if err == nil {
		if b.db != nil {
			_ = b.db.TouchChat(chat.BridgeId, chat.RootMsgID)
		}
	}
	return err
}
func (b *Bridge) submitToSessionWithPreempt(ctx context.Context, chat *LarkChat, text string, alreadyInterrupted bool) error {
	_, err := b.submitToSessionWithOptions(ctx, chat, text, true, alreadyInterrupted, nil)
	return err
}
func (b *Bridge) submitQueuedToSession(ctx context.Context, chat *LarkChat, text string) error {
	_, err := b.submitQueuedToSessionForMessages(ctx, chat, text, nil)
	return err
}

func (b *Bridge) submitQueuedToSessionForMessages(ctx context.Context, chat *LarkChat, text string, inputs []turnInputRef) (submitReceipt, error) {
	return b.submitToSessionWithOptions(ctx, chat, text, false, false, inputs)
}

type submitReceipt struct {
	SessionID string
	LaunchID  string
	TurnID    string
}

func (b *Bridge) submitToSessionForMessages(ctx context.Context, chat *LarkChat, text string, inputs []turnInputRef) (submitReceipt, error) {
	return b.submitToSessionWithOptions(ctx, chat, text, true, false, inputs)
}

func (b *Bridge) submitToSessionWithOptions(ctx context.Context, chat *LarkChat, text string, preempt, alreadyInterrupted bool, inputs []turnInputRef) (submitReceipt, error) {
	ctx, text, err := b.prepareFileMaterials(ctx, chat, text)
	if err != nil {
		return submitReceipt{}, err
	}
	parts, shouldSubmit, err := b.multipartSubmitParts(ctx, chat, text)
	if err != nil {
		return submitReceipt{}, err
	}
	if !shouldSubmit {
		return submitReceipt{}, nil
	}

	// Delivery guidance belongs to the structured prompt, leaving the recorded
	// user text and the routing/coalescing contract unchanged.
	if len(parts) == 0 {
		parts = []*pb.InputPart{{Value: &pb.InputPart_Text{Text: text + artifactInstructions}}}
	} else if first, ok := parts[0].Value.(*pb.InputPart_Text); ok {
		first.Text += artifactInstructions
	} else {
		parts = append(parts, &pb.InputPart{Value: &pb.InputPart_Text{Text: artifactInstructions}})
	}
	b.waitForStreamReady(ctx, chat.RootMsgID)
	b.setTurnActive(chat.RootMsgID, true)
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	expectedSessionID, expectedLaunchID := "", ""
	if materials := materialsFromContext(ctx); materials != nil {
		expectedSessionID, expectedLaunchID = materials.expectedSessionID, materials.expectedLaunchID
	}
	routed, err := b.hub.Route(sendCtx, chat.TargetID, "Submit", &pb.SubmitRequest{ExpectedSessionId: expectedSessionID, ExpectedLaunchId: expectedLaunchID, Name: chat.SessionName, Text: text, Source: "lark", Async: true, Preempt: preempt, PreemptAlreadyInterrupted: alreadyInterrupted, Parts: parts, LocalExpertAuthoring: chat.localExpertAuthoring})
	if err != nil {
		b.setTurnActive(chat.RootMsgID, false)
		return submitReceipt{}, err
	}
	var response pb.SubmitResponse
	if err := json.Unmarshal(routed.Payload, &response); err != nil {
		b.setTurnActive(chat.RootMsgID, false)
		return submitReceipt{}, fmt.Errorf("decode submit receipt: %w", err)
	}
	receipt := submitReceipt{SessionID: response.GetSessionId(), LaunchID: response.GetLaunchId(), TurnID: response.GetTurnId()}
	if b.db != nil {
		_ = b.db.TouchChat(chat.BridgeId, chat.RootMsgID)
		links := make([]TurnLink, 0, len(inputs))
		for _, input := range inputs {
			if strings.TrimSpace(input.MessageID) == "" || receipt.TurnID == "" {
				continue
			}
			links = append(links, TurnLink{BridgeID: chat.BridgeId, RootMsgID: chat.RootMsgID,
				MessageID: input.MessageID, OpenID: input.OpenID, TargetID: chat.TargetID, SessionID: receipt.SessionID,
				SessionName: chat.SessionName, LaunchID: receipt.LaunchID, TurnID: receipt.TurnID,
				SubmittedAt: time.Now()})
		}
		if err := b.db.RecordTurnLinks(links); err != nil {
			log.Warn().Err(err).Str("turn_id", receipt.TurnID).Msg("bridge: persist submit receipt")
		}
		if chat.topicRootPromptPending {
			marked, markErr := b.db.MarkTopicRootPromptDelivered(chat.BridgeId, chat.RootMsgID, chat.TargetID, chat.SessionName)
			if markErr != nil {
				log.Warn().Err(markErr).Str("root_msg_id", chat.RootMsgID).Msg("bridge: failed to persist topic root prompt delivery")
			} else if marked {
				chat.topicRootPromptPending = false
			} else {
				log.Debug().Str("root_msg_id", chat.RootMsgID).Str("session", chat.SessionName).Msg("bridge: skipped stale topic root prompt delivery mark")
			}
		}
	}
	return receipt, nil
}
func (b *Bridge) addReactionAsync(msgID, emojiType string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		add := b.addReaction
		if b.reactionAdd != nil {
			add = b.reactionAdd
		}
		if err := add(ctx, msgID, emojiType); err != nil {
			log.Debug().Err(err).Str("message_id", msgID).Str("emoji", emojiType).Msg("bridge: add reaction failed")
		}
	}()
}
func (b *Bridge) canAddReaction() bool {
	return b != nil && (b.client != nil || b.reactionAdd != nil)
}

func (b *Bridge) boundSessionAlive(ctx context.Context, chat *LarkChat) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := b.hub.Route(probeCtx, chat.TargetID, "List", &pb.ListRequest{})
	if err != nil {
		return false, err
	}
	var out pb.ListResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return false, err
	}
	for _, s := range // boundSessionAlive reports whether the session bound to chat still exists and is
	// not archived on its target. A reclaimed session (archived or pruned/absent)
	// returns false. On a probe RPC/parse failure it returns a non-nil error so the
	// caller leaves the binding untouched (avoids terminating on transient hub errors).
	out.Sessions {
		if s.Name == chat.SessionName {
			return s.State != string(session.StateArchived), nil
		}
	}
	return false, nil
}
func (b *Bridge) notifyQueue(ctx context.Context, rootMsgID string, chat *LarkChat) error {
	queueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := b.hub.Route(queueCtx, chat.TargetID, "QueueList", &pb.QueueListRequest{Name: chat.SessionName})
	if err != nil {
		return b.notifyThread(ctx, rootMsgID, "⚠️ 查看队列失败："+err.Error())
	}
	var out pb.QueueListResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return b.notifyThread(ctx, rootMsgID, "⚠️ 解析队列失败："+err.Error())
	}
	if len(out.Items) == 0 {
		return b.notifyThread(ctx, rootMsgID, "当前没有 pending 输入")
	}
	lines := make([]string, 0, len(out.Items)+1)
	lines = append(lines, fmt.Sprintf("Pending 输入（%d 条）：", len(out.Items)))
	for _, item := range out.Items {
		lines = append(lines, fmt.Sprintf("- %s [%s] %s", item.Id, item.Source, truncateForNotice(item.Text, 80)))
	}
	return b.notifyThread(ctx, rootMsgID, strings.Join(lines, "\n"))
}
func (b *Bridge) notifyThread(ctx context.Context, rootMsgID, text string) error {
	if b.threadNotice != nil {
		return b.threadNotice(ctx, rootMsgID, text)
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if target, ok, err := b.directReplyTargetForRoot(ctx, rootMsgID); ok || err != nil {
		if err != nil {
			return err
		}
		_, _, err := b.sendTextToDirectTarget(notifyCtx, target, text)
		return err
	}
	_, _, err := b.sendTextToThread(notifyCtx, rootMsgID, text)
	return err
}
func truncateForNotice(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if max > 0 && len(runes) > max {
		return string(runes[:max-3]) + "..."
	}
	return text
}
