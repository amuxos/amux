package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
)

const questionLifetime = 15 * time.Minute

// A question must be displayed intact: trimming choices changes its meaning.
func questionCardFits(payload permissionPayload) bool {
	if len(payload.Options) > maxPermissionOptions || len(payload.Tool.Question) > 8000 || len(payload.Tool.Title) > 300 {
		return false
	}
	seen := make(map[string]bool)
	for _, option := range payload.Options {
		if strings.TrimSpace(option.OptionId) == "" || seen[option.OptionId] || utf8.RuneCountInString(option.OptionId) > 100 || utf8.RuneCountInString(option.Name) > 100 {
			return false
		}
		seen[option.OptionId] = true
	}
	encoded, _ := json.Marshal(payload)
	return len(encoded) < maxFeishuCardBytes/2 // reserve room for form labels, descriptions and identity
}

func renderQuestionCard(targetID, sessionName, permissionID string, payload permissionPayload) string {
	if !questionCardFits(payload) || payload.Tool.Secret {
		return renderPermissionResolvedCard(managementCopy("question.unavailable"))
	}
	metadata := map[string]string{"permission_id": permissionID, "target_id": targetID, "session_name": sessionName, "turn_id": payload.TurnID}
	value, _ := json.Marshal(metadata)
	content := payload.Tool.Question
	if payload.Tool.Title != "" {
		content = "**" + escapeCardMarkdown(payload.Tool.Title) + "**\n" + content
	}
	for _, option := range payload.Options {
		if option.Description != "" {
			label := option.Name
			if label == "" {
				label = option.OptionId
			}
			content += "\n\n**" + escapeCardMarkdown(label) + "**: " + escapeCardMarkdown(option.Description)
		}
	}
	metadata["cancel"] = "true"
	doc := newCardDoc([]cardElement{
		{Tag: "markdown", Content: content},
		renderPermissionQuestionForm(payload, value),
		managementFormButtonRowElement(cardAction{Tag: "button", Name: "question_cancel", Type: "default",
			Text: managementPlainTextPtr(managementCopy("question.cancel")), Behaviors: []cardBehavior{{Type: "callback", Value: metadata}}}),
	})
	doc.Header = &cardHeader{Title: managementPlainText(managementCopy("question.title")), Template: "orange"}
	encoded, _ := json.Marshal(doc)
	return string(encoded)
}

func questionAnswers(payload permissionPayload, action *callback.CallBackAction) ([]string, bool) {
	answers := permissionOptionIDs(action)
	custom := false
	if answer, ok := action.FormValue["answer"].(string); ok && strings.TrimSpace(answer) != "" {
		if len(payload.Options) > 0 && !payload.Tool.AllowOther {
			return nil, false
		}
		answers, custom = []string{answer}, true
	}
	selected := make([]string, 0, len(answers))
	seen := make(map[string]bool)
	for _, answer := range answers {
		answer = strings.TrimSpace(answer)
		if answer == "" || len(answer) > 8000 {
			return nil, false
		}
		if seen[answer] {
			continue
		}
		seen[answer] = true
		if len(payload.Options) > 0 && !custom {
			valid := false
			for _, option := range payload.Options {
				if option.OptionId == answer {
					valid = true
					break
				}
			}
			if !valid {
				return nil, false
			}
		}
		selected = append(selected, answer)
	}
	return selected, len(selected) > 0 && (payload.Tool.MultiSelect || len(selected) == 1)
}

func (b *Bridge) cancelQuestion(pending *pendingPermission) {
	if pending == nil || pending.Question == nil || pending.Question.Tool.Delivery == "async" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	_, err := b.hub.Route(ctx, pending.TargetID, "RespondPermission", &pb.RespondPermissionRequest{
		Name: pending.SessionName, PermissionId: pending.PermissionId, TurnId: pending.TurnID, Cancel: true,
	})
	if err != nil {
		log.Warn().Err(err).Str("permission_id", pending.PermissionId).Msg("bridge: cancel question failed")
	}
}

func (b *Bridge) sendQuestionUnavailable(ctx context.Context, chat *LarkChat, secret bool) error {
	key := "question.unavailable"
	if secret {
		key = "question.secret"
	}
	_, _, err := b.sendInteractiveToBinding(ctx, chat, renderPermissionResolvedCard(managementCopy(key)), "question unavailable")
	return err
}

func (b *Bridge) patchQuestionAsync(messageID string, text any) {
	if messageID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := b.patchPermissionMessage(ctx, messageID, text); err != nil {
			log.Warn().Err(err).Msg("bridge: retire question card failed")
		}
	}()
}

func (b *Bridge) armQuestionExpiry(key string) {
	b.permissionMu.Lock()
	defer b.permissionMu.Unlock()
	if pending := b.permissions[key]; pending != nil {
		pending.Expiry = time.AfterFunc(questionLifetime, func() { b.expireQuestion(key) })
	}
}

func (b *Bridge) expireQuestion(key string) {
	pending := b.claimPermission(key)
	if pending == nil {
		// A bounded callback RPC may be in progress at the deadline. Recheck
		// after it finishes, in case it releases the claim on a transport error.
		b.permissionMu.Lock()
		if p := b.permissions[key]; p != nil {
			p.Expiry = time.AfterFunc(3*time.Second, func() { b.expireQuestion(key) })
		}
		b.permissionMu.Unlock()
		return
	}
	b.cancelQuestion(pending)
	b.resolvePermissionRequest(key)
	b.patchQuestionAsync(pending.MessageID, managementCopy("question.expired"))
}

// Resolution and terminal events retire cards without inventing a business
// answer. A callback already in progress owns its success/failure presentation.
func (b *Bridge) retireQuestions(targetID, sessionName, rootMsgID, turnID, permissionID string, cancelNative bool, keepAsync ...bool) {
	b.permissionMu.Lock()
	var keys []string
	for key, pending := range b.permissions {
		if len(keepAsync) > 0 && keepAsync[0] && pending.Question != nil && pending.Question.Tool.Delivery == "async" {
			continue
		}
		if pending.Question != nil && !pending.Resolving && pending.TargetID == targetID && pending.SessionName == sessionName &&
			(rootMsgID == "" || pending.RootMsgID == rootMsgID) && (turnID == "" || pending.TurnID == turnID) &&
			(permissionID == "" || pending.PermissionId == permissionID) {
			keys = append(keys, key)
		}
	}
	b.permissionMu.Unlock()
	for _, key := range keys {
		pending := b.claimPermission(key)
		if pending == nil {
			continue
		}
		if cancelNative {
			b.cancelQuestion(pending)
		}
		b.resolvePermissionRequest(key)
		b.patchQuestionAsync(pending.MessageID, managementCopy("permission.expired"))
	}
}

func (b *Bridge) submitAsyncQuestionAnswer(ctx context.Context, pending *pendingPermission, answers []string) error {
	if pending.Chat == nil || pending.SessionID == "" || pending.LaunchID == "" || pending.Question.Tool.AgentSessionID == "" {
		return fmt.Errorf("async question has no bound execution")
	}
	chat := *pending.Chat
	chat.OpenId = pending.OpenId
	chat.localExpertAuthoring = false
	text := pending.Question.Tool.Question + "\n\n" + strings.Join(answers, "\n")
	_, err := b.submitToSessionMode(ctx, &chat, text, submitMode{
		expectedSessionID: pending.SessionID, expectedLaunchID: pending.LaunchID,
		expectedAgentSessionID: pending.Question.Tool.AgentSessionID,
	}, []turnInputRef{{OpenID: pending.OpenId}})
	return err
}
