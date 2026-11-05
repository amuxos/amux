package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/i18n"
	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

// ConversationBinding is an immutable snapshot, scoped to a Bot and conversation.
type ConversationBinding struct {
	BridgeID, RootID, ChatID, ThreadID, Initiator, TargetID, Session, Agent, Model, Title string
}

// ConversationSubmission describes an accepted, correlated user submission.
// HumanIDs is empty for automation and ordinary bot-to-bot input.
type ConversationSubmission struct {
	Binding              ConversationBinding
	TurnID, Text         string
	HumanIDs, MessageIDs []string
}

// ConversationCompletion reports canonical completion, never inferred text.
// Status uses the Bridge completion states: done, failed, or interrupted.
type ConversationCompletion struct {
	Binding                            ConversationBinding
	Card                               *ConversationCard
	TurnID, Status, StopReason, Answer string
	Superseded, Automation             bool
}

// ConversationCard identifies a delivered, finalized reply. Extensions persist
// Sequence before each mutation and serialize updates to their reserved section.
type ConversationCard struct {
	BridgeID, MessageID, CardID, Locale string
	Sequence                            int
}

const conversationSectionID = "conversation_extra"

func conversationSection(elements []json.RawMessage) json.RawMessage {
	if elements == nil {
		elements = []json.RawMessage{}
	}
	raw, _ := json.Marshal(map[string]any{"tag": "column_set", "element_id": conversationSectionID,
		"flex_mode": "none", "columns": []any{map[string]any{"tag": "column", "width": "weighted", "weight": 1, "elements": elements}}})
	return raw
}

func emptyConversationSection() cardElement {
	return cardElement{Tag: "column_set", ElementID: conversationSectionID, FlexMode: "none",
		Columns: []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, Elements: []cardElement{}}}}
}

// ConversationObserver is installed per Bridge. Implementations must serialize
// their own state; callbacks can race. Close must join any background workers.
type ConversationObserver interface {
	Submitted(context.Context, ConversationSubmission)
	Completed(context.Context, ConversationCompletion)
	CardAction(context.Context, *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, bool, error)
	Close() error
}

// ConversationServices exposes host capabilities without exposing Bridge storage.
// SubmitTask is a trusted in-process automation surface, not a remote endpoint.
type ConversationServices struct {
	BridgeID, StateDir, ConfigPath string
	Client                         *lark.Client
	IsOwner                        func(string) bool
	Locale                         func(string) string
	SendCard                       func(context.Context, ConversationBinding, string) (string, error)
	PatchCard                      func(context.Context, string, string) error
	UpdateCardSection              func(context.Context, ConversationCard, []json.RawMessage) error
	SubmitTask                     func(context.Context, []byte) (string, error)
	ReadTask                       func(context.Context, string) ([]byte, error)
	// Use the scoped capabilities for work originating from a conversation.
	// They retain its execution identity and validate result access on every read.
	SubmitConversationTask func(context.Context, ConversationBinding, []byte) (string, error)
	ReadConversationTask   func(context.Context, ConversationBinding, string) ([]byte, error)
	ConversationURL        func(context.Context, ConversationBinding) (string, error)
}

type BackgroundTaskSubmitter interface {
	SubmitScheduledTask(context.Context, []byte) (string, error)
}

type ConversationTaskRunner interface {
	SubmitConversationTask(context.Context, string, string, []byte) (string, error)
	ReadConversationTask(context.Context, string, string, string) ([]byte, error)
}

func (b *Bridge) validateConversationTaskBinding(binding ConversationBinding) error {
	if binding.BridgeID != b.currentBridgeId() || binding.RootID == "" || binding.TargetID == "" || binding.Session == "" || b.db == nil {
		return fmt.Errorf("invalid conversation task binding")
	}
	chat, err := b.db.FindByRootMsg(binding.BridgeID, binding.RootID)
	if err != nil {
		return err
	}
	if chat == nil || chat.TargetID != binding.TargetID || chat.SessionName != binding.Session || chat.ChatID != binding.ChatID || chat.OpenId != binding.Initiator {
		return fmt.Errorf("conversation task binding has changed")
	}
	return nil
}

func (b *Bridge) conversationTaskServices(s *ConversationServices) {
	runner, ok := b.subtasks.(ConversationTaskRunner)
	if !ok {
		return
	}
	s.SubmitConversationTask = func(ctx context.Context, binding ConversationBinding, spec []byte) (string, error) {
		if err := b.validateConversationTaskBinding(binding); err != nil {
			return "", err
		}
		return runner.SubmitConversationTask(ctx, binding.TargetID, binding.Session, spec)
	}
	s.ReadConversationTask = func(ctx context.Context, binding ConversationBinding, id string) ([]byte, error) {
		if err := b.validateConversationTaskBinding(binding); err != nil {
			return nil, err
		}
		return runner.ReadConversationTask(ctx, binding.TargetID, binding.Session, id)
	}
}

func (b *Bridge) conversationBinding(chat *LarkChat) ConversationBinding {
	agent := chat.AgentProfile
	if agent == "" {
		agent = chat.AgentType
	}
	return ConversationBinding{BridgeID: b.currentBridgeId(), RootID: chat.RootMsgID,
		ChatID: chat.ChatID, ThreadID: chat.ThreadID, Initiator: chat.OpenId,
		TargetID: chat.TargetID, Session: chat.SessionName, Agent: agent, Model: chat.EffectiveModel, Title: chat.DisplayTitle}
}

func (b *Bridge) startConversationObserver(ctx context.Context) error {
	factory := bridgeExtensions.snapshot().ObserveConversations
	if factory == nil {
		return nil
	}
	path := viper.ConfigFileUsed()
	if path == "" {
		path = amuxhome.ConfigPath()
	}
	s := ConversationServices{BridgeID: b.currentBridgeId(), StateDir: filepath.Join(amuxhome.Dir(), "extensions"),
		ConfigPath: path, Client: b.client, IsOwner: b.isBotOwner,
		Locale: func(id string) string { return string(b.managementLocale(id)) },
		SendCard: func(ctx context.Context, binding ConversationBinding, card string) (string, error) {
			if binding.BridgeID != b.currentBridgeId() {
				return "", fmt.Errorf("conversation belongs to another bridge")
			}
			chat := &LarkChat{BridgeId: binding.BridgeID, RootMsgID: binding.RootID, ChatID: binding.ChatID, ThreadID: binding.ThreadID, OpenId: binding.Initiator, TargetID: binding.TargetID, SessionName: binding.Session}
			id, _, err := b.sendInteractiveToBinding(ctx, chat, card, "extension card")
			return id, err
		},
		ConversationURL: func(ctx context.Context, binding ConversationBinding) (string, error) {
			return b.completionNotificationURL(ctx, &LarkChat{RootMsgID: binding.RootID, ChatID: binding.ChatID, ThreadID: binding.ThreadID})
		},
		PatchCard: func(ctx context.Context, id, card string) error {
			_, err := b.patchManagementCard(ctx, id, card)
			return err
		},
		UpdateCardSection: func(ctx context.Context, card ConversationCard, elements []json.RawMessage) error {
			if card.BridgeID != b.currentBridgeId() || card.MessageID == "" || card.CardID == "" || card.Sequence < 1 {
				return fmt.Errorf("invalid completed conversation card")
			}
			api := &larkCardKitReplyAPI{bridge: b}
			return api.ReplaceElement(ctx, cardKitElementReplace{CardID: card.CardID, ElementID: conversationSectionID,
				Element: selectManagementCardLocale(string(conversationSection(elements)), i18n.Locale(card.Locale)),
				UUID:    uuid.NewString(), Sequence: card.Sequence})
		},
	}
	if b.subtasks != nil {
		s.ReadTask = b.subtasks.GetSubtaskJSON
		if submitter, ok := b.subtasks.(BackgroundTaskSubmitter); ok {
			s.SubmitTask = submitter.SubmitScheduledTask
		}
	}
	b.conversationTaskServices(&s)
	observer, err := factory(ctx, s)
	if err != nil {
		return err
	}
	b.conversationMu.Lock()
	b.conversationObserver = observer
	b.conversationMu.Unlock()
	return nil
}

func (b *Bridge) observeSubmission(ctx context.Context, chat *LarkChat, turnID, text string, inputs []turnInputRef) {
	b.observeGuideSubmission(chat, turnID, inputs)
	observer := b.getConversationObserver()
	if observer == nil || chat == nil {
		return
	}
	event := ConversationSubmission{Binding: b.conversationBinding(chat), TurnID: turnID, Text: stripAmuxGuidance(text)}
	if source, err := b.db.FindChatEntrySource(b.currentBridgeId(), chat.RootMsgID); err != nil || source == ChatEntrySourceGroupWatch {
		return
	}
	for _, input := range inputs {
		if strings.TrimSpace(input.OpenID) != "" {
			event.HumanIDs = append(event.HumanIDs, input.OpenID)
			event.MessageIDs = append(event.MessageIDs, input.MessageID)
		}
	}
	observer.Submitted(ctx, event)
}

func (b *Bridge) observeCompletion(ctx context.Context, chat *LarkChat, turnID, status, stopReason, answer string, superseded bool, cards ...*ConversationCard) {
	b.observeGuideCompletion(chat, turnID, status)
	observer := b.getConversationObserver()
	if observer == nil || chat == nil {
		return
	}
	automation := b.isSubtaskTurn(chat.RootMsgID, turnID) || chat.goalGeneration != ""
	if source, err := b.db.FindChatEntrySource(b.currentBridgeId(), chat.RootMsgID); err != nil || source == ChatEntrySourceGroupWatch {
		automation = true
	}
	var card *ConversationCard
	if len(cards) > 0 && cards[0] != nil {
		copy := *cards[0]
		copy.BridgeID = b.currentBridgeId()
		card = &copy
	}
	observer.Completed(ctx, ConversationCompletion{Binding: b.conversationBinding(chat), Card: card, TurnID: turnID, Status: status, StopReason: stopReason, Answer: stripAmuxGuidance(answer), Superseded: superseded, Automation: automation})
}

// Extension experiment entries are independent controls with their own callback
// namespace; they are not part of the public experiment save/restart form.
func conversationExperimentElements() []cardElement {
	fn := bridgeExtensions.snapshot().ExperimentEntries
	if fn == nil {
		return nil
	}
	var out []cardElement
	for _, raw := range fn() {
		var el cardElement
		if err := json.Unmarshal(raw, &el); err != nil {
			log.Warn().Err(err).Msg("bridge: invalid extension experiment entry")
			continue
		}
		out = append(out, el)
	}
	return out
}

func (b *Bridge) getConversationObserver() ConversationObserver {
	b.conversationMu.RLock()
	defer b.conversationMu.RUnlock()
	return b.conversationObserver
}
