package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const browserLinkAction = "bot.browser.link"
const browserDisconnectAction = "bot.browser.disconnect"

type ConsoleWorkspaceRequest struct {
	Profile  string            `json:"profile"`
	Browser  string            `json:"browser"`
	Expires  int64             `json:"expires"`
	Revision uint64            `json:"revision,omitempty"`
	ChatID   string            `json:"chat_id,omitempty"`
	Control  string            `json:"control,omitempty"`
	Option   string            `json:"option,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
}
type ConsoleWorkspaceResult struct {
	Status       string          `json:"status"`
	State        string          `json:"state"`
	Code         string          `json:"code,omitempty"`
	Revision     uint64          `json:"revision,omitempty"`
	CanOpenGroup bool            `json:"can_open_group,omitempty"`
	Card         json.RawMessage `json:"card,omitempty"`
	Toast        *callback.Toast `json:"toast,omitempty"`
}
type browserWorkspace struct {
	code, openID, messageID, chatID string
	expiry, codeExpiry              time.Time
	owner, busy                     bool
	revision                        uint64
	frame                           browserCardFrame
}
type browserWorkspaces struct {
	mu           sync.Mutex
	sessions     map[string]*browserWorkspace
	lastRevision uint64
}

func (w *browserWorkspaces) prune(now time.Time) {
	for key, session := range w.sessions {
		if !session.expiry.After(now) || session.openID == "" && !session.codeExpiry.After(now) {
			delete(w.sessions, key)
		}
	}
}

// Every linked frame has a distinct revision, including after reconnect or a
// change of Feishu account in another tab of the same browser.
func (w *browserWorkspaces) advance(s *browserWorkspace) {
	next := uint64(time.Now().UnixMicro())
	if next <= w.lastRevision {
		next = w.lastRevision + 1
	}
	w.lastRevision = next
	s.revision = next
}

func workspaceResult(s *browserWorkspace) ConsoleWorkspaceResult {
	out := ConsoleWorkspaceResult{Status: "success", State: "disconnected"}
	if s == nil {
		return out
	}
	if s.openID == "" {
		out.State = "pending"
		out.Code = s.code
		return out
	}
	out.State = "connected"
	out.CanOpenGroup = s.owner
	out.Card = s.frame.card
	out.Revision = s.revision
	return out
}
func (w *browserWorkspaces) patch(messageID, raw string) bool {
	if messageID == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prune(time.Now())
	for _, s := range w.sessions {
		if s.messageID == messageID {
			s.frame = browserFrame(raw)
			w.advance(s)
			return true
		}
	}
	return false
}

// The browser obtains a challenge. Only an actual Feishu callback on a
// server-recorded private /amux card can give that browser an acting identity.
func (b *Bridge) ConsoleWorkspace(ctx context.Context, action string, in ConsoleWorkspaceRequest) (ConsoleWorkspaceResult, error) {
	var zero ConsoleWorkspaceResult
	if err := b.consoleAvailable(ctx); err != nil {
		return zero, err
	}
	now := time.Now()
	if _, err := hex.DecodeString(in.Browser); err != nil || len(in.Browser) != 64 || in.Profile != b.ProfileName() || in.Expires <= now.Unix() {
		return zero, status.Error(codes.PermissionDenied, "console_operator_required")
	}
	w := &b.browserCards
	w.mu.Lock()
	w.prune(now)
	s := w.sessions[in.Browser]
	if s != nil && s.openID != "" && (s.owner != b.isBotOwner(s.openID) || !b.db.isPrivateManagementCardContext(ctx, b.currentBridgeId(), s.openID, s.messageID)) {
		delete(w.sessions, in.Browser)
		s = nil
	}
	switch action {
	case "get":
		result := workspaceResult(s)
		w.mu.Unlock()
		return result, nil
	case "disconnect":
		delete(w.sessions, in.Browser)
		w.mu.Unlock()
		return workspaceResult(nil), nil
	case "connect":
		if s == nil {
			if len(w.sessions) >= 128 {
				w.mu.Unlock()
				return zero, status.Error(codes.ResourceExhausted, "console_too_many_browsers")
			}
			if w.sessions == nil {
				w.sessions = make(map[string]*browserWorkspace)
			}
			expiry := time.Unix(in.Expires, 0)
			if limit := now.Add(30 * time.Minute); expiry.After(limit) {
				expiry = limit
			}
			s = &browserWorkspace{code: rand.Text()[:16], expiry: expiry, codeExpiry: now.Add(5 * time.Minute)}
			w.sessions[in.Browser] = s
		}
		result := workspaceResult(s)
		w.mu.Unlock()
		return result, nil
	case "invoke", "home", "group":
	default:
		w.mu.Unlock()
		return zero, status.Error(codes.InvalidArgument, "console_unknown_action")
	}
	if s == nil || s.openID == "" {
		w.mu.Unlock()
		return zero, status.Error(codes.PermissionDenied, "console_private_card_required")
	}
	if action == "group" && !s.owner {
		w.mu.Unlock()
		return zero, status.Error(codes.PermissionDenied, "console_bot_owner_required")
	}
	// Returning home only rereads the linked identity. A background editor may
	// finish between a browser click and this request.
	if s.busy || (action != "home" && s.revision != in.Revision) {
		w.mu.Unlock()
		return zero, status.Error(codes.Aborted, "console_stale_card")
	}
	var cb *callback.CallBackAction
	if action == "invoke" {
		var err error
		cb, err = s.frame.action(in)
		if err != nil {
			w.mu.Unlock()
			return zero, err
		}
	}
	s.busy = true
	w.advance(s)
	revision := s.revision
	w.mu.Unlock()

	var raw string
	var toast *callback.Toast
	var err error
	if action == "home" || action == "group" || cb != nil && cb.Value[managementActionKey] == browserTopicAction {
		var unlock func()
		unlock, err = b.lockGroupSettingsRouteContext(ctx, "management-card\x00"+s.messageID)
		if err == nil {
			defer unlock()
			b.advanceModelServiceEditor(s.messageID)
			switch action {
			case "home":
				raw, err = b.amuxManagementHome(ctx, s.openID)
			case "group":
				raw, err = b.browserGroupCard(ctx, s.openID, in.ChatID)
			default:
				raw, err = b.browserTopicCard(ctx, s.openID, managementCallbackString(cb, "chat_id"), cb.Option)
			}
			if err == nil {
				raw = b.managementCardForRecipient(s.openID, raw)
			}
		}
	} else {
		event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: s.openID}, Context: &callback.Context{OpenMessageID: s.messageID, OpenChatID: s.chatID}, Action: cb}}
		var response *callback.CardActionTriggerResponse
		response, err = b.handleCardAction(ctx, event)
		if response != nil {
			toast = response.Toast
			if response.Card != nil {
				data, marshalErr := json.Marshal(response.Card.Data)
				if marshalErr == nil {
					raw = string(data)
				}
			}
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s.busy = false
	if w.sessions[in.Browser] != s || !s.expiry.After(time.Now()) {
		return workspaceResult(nil), nil
	}
	if err != nil {
		return zero, err
	}
	// A newer async completion takes precedence over its synchronous loading card.
	if raw != "" && (action != "invoke" || cb.Value[managementActionKey] == browserTopicAction || s.revision == revision) {
		s.frame = browserFrame(raw)
		w.advance(s)
	}
	result := workspaceResult(s)
	result.Toast = toast
	return result, nil
}

func (b *Bridge) withBrowserLinkForm(raw string) string {
	w := &b.browserCards
	w.mu.Lock()
	w.prune(time.Now())
	pending := false
	for _, s := range w.sessions {
		if s.code != "" {
			pending = true
			break
		}
	}
	w.mu.Unlock()
	if !pending {
		return raw
	}
	var doc cardDoc
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return raw
	}
	submit := managementFormSubmitButton(browserLinkAction, managementCopy("workspace.link"))
	submit.Confirm = managementConfirm(managementCopy("workspace.link"), managementCopy("workspace.consent"))
	doc.Body.Elements = append(doc.Body.Elements, cardElement{Tag: "form", Name: "browser_pairing", Elements: []cardElement{
		managementHintElement(managementCopy("workspace.consent")),
		{Tag: "input", Name: "browser_code", Required: true, Placeholder: managementPlainTextPtr(managementCopy("workspace.code"))},
		managementFormButtonRowElement(submit),
	}})
	data, _ := json.Marshal(doc)
	return string(data)
}
func (b *Bridge) browserLinkCallback(ctx context.Context, event *callback.CardActionTriggerEvent, action string) *callback.CardActionTriggerResponse {
	denied := func() *callback.CardActionTriggerResponse {
		return cardToast("error", managementCopy("workspace.invalid"))
	}
	input := event.Event
	if input.Operator == nil || input.Context == nil || b.db == nil || !b.ownerConfirmed() {
		return denied()
	}
	open, msg := input.Operator.OpenID, input.Context.OpenMessageID
	if (!b.isBotOwner(open) && b.schedules == nil) || open == "" || msg == "" || !b.db.isPrivateManagementCardContext(ctx, b.currentBridgeId(), open, msg) {
		return denied()
	}
	w := &b.browserCards
	if action == browserDisconnectAction {
		w.mu.Lock()
		for key, s := range w.sessions {
			if s.openID == open && s.messageID == msg {
				delete(w.sessions, key)
			}
		}
		w.mu.Unlock()
		return cardToast("success", managementCopy("workspace.disconnected"))
	}
	code, _ := input.Action.FormValue["browser_code"].(string)
	code = strings.TrimSpace(code)
	if code == "" {
		return denied()
	}
	w.mu.Lock()
	w.prune(time.Now())
	var pending *browserWorkspace
	for _, s := range w.sessions {
		if s.code == code {
			pending = s
			break
		}
	}
	w.mu.Unlock()
	if pending == nil {
		return denied()
	}
	raw, err := b.amuxManagementHome(ctx, open)
	if err != nil {
		return denied()
	}
	frame := browserFrame(b.managementCardForRecipient(open, raw))
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prune(time.Now())
	for _, s := range w.sessions {
		if s.messageID == msg {
			return denied()
		}
	}
	for _, s := range w.sessions {
		if s != pending || s.code != code {
			continue
		}
		s.code = ""
		s.openID = open
		s.messageID = msg
		s.chatID = input.Context.OpenChatID
		s.owner = b.isBotOwner(open)
		w.advance(s)
		s.frame = frame
		receipt := marshalNativeManagementCard(managementCardView{Title: managementCopy("workspace.connected"), Elements: []cardElement{managementHintElement(managementCopy("workspace.consent"))}, Buttons: []managementButton{{Text: managementCopy("workspace.disconnect"), Action: browserDisconnectAction}}})
		return &callback.CardActionTriggerResponse{Card: &callback.Card{Type: "raw", Data: json.RawMessage(b.managementCardForRecipient(open, receipt))}}
	}
	return denied()
}
