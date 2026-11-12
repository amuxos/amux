package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/rs/zerolog/log"
)

const (
	cardKitReplyElementID         = "answer_text"
	cardKitAnswerHeadingID        = "answer_heading"
	cardKitActivityElementID      = "activity_section"
	cardKitActivityContentID      = "activity_content"
	cardKitExecutionSummaryID     = "execution_summary"
	cardKitAnswerDividerElementID = "answer_divider"
)

type replyCardMode uint8

const (
	replyCardModeCardKit replyCardMode = iota
	replyCardModeLegacy
)

type replyCardHandle struct {
	delivery            replyCardDelivery
	mode                replyCardMode
	cardID              string
	messageID           string
	sequence            int
	lastText            string
	lastActivity        string
	hasAnswer           bool
	hasActivity         bool
	finished            bool
	messageUUID         string
	deliveryPending     bool
	pendingInsert       *pendingCardKitInsert
	conversationSection bool
}

type pendingCardKitInsertKind uint8

const (
	pendingInsertAnswer pendingCardKitInsertKind = iota
	pendingInsertActivity
)

type pendingCardKitInsert struct {
	kind       pendingCardKitInsertKind
	request    cardKitElementInsert
	answerText string
	activity   turnCardActivity
}

type replyCardBackend interface {
	Create(context.Context, string) (*replyCardHandle, error)
	Update(context.Context, *replyCardHandle, string) error
	Calibrate(context.Context, *replyCardHandle, string) error
	Finish(context.Context, *replyCardHandle, string) error
}

type cardKitReplyUpdate struct {
	CardID    string
	ElementID string
	Text      string
	UUID      string
	Sequence  int
}

type cardKitElementInsert struct {
	CardID          string
	Type            string
	TargetElementID string
	Elements        string
	UUID            string
	Sequence        int
}

type cardKitElementReplace struct {
	CardID    string
	ElementID string
	Element   string
	UUID      string
	Sequence  int
}

type cardKitReplyFinish struct {
	CardID   string
	UUID     string
	Sequence int
}

type cardKitReplyAPI interface {
	CreateCard(context.Context, string) (string, error)
	InsertElements(context.Context, cardKitElementInsert) error
	ReplaceElement(context.Context, cardKitElementReplace) error
	UpdateElement(context.Context, cardKitReplyUpdate) error
	FinishCard(context.Context, cardKitReplyFinish) error
}

type bindingReplyCardBackend struct {
	conversationSection bool
	prepareDelivery     func() (replyCardDelivery, error)
	cardKit             cardKitReplyAPI
	sendCard            func(context.Context, string, string) (string, error)
	createLegacy        func(context.Context, string) (string, error)
	patchLegacy         func(context.Context, string, string) error
}

func newBindingReplyCardBackend(b *Bridge, chat *LarkChat) replyCardBackend {
	return &bindingReplyCardBackend{
		conversationSection: b.getConversationObserver() != nil,
		prepareDelivery:     func() (replyCardDelivery, error) { return b.prepareSessionCardDelivery(chat) },
		cardKit:             &larkCardKitReplyAPI{bridge: b},
		sendCard: func(ctx context.Context, content, requestUUID string) (string, error) {
			msgID, _, err := b.sendCardKitReplyToBinding(ctx, chat, content, requestUUID)
			return msgID, err
		},
		createLegacy: func(ctx context.Context, text string) (string, error) {
			msgID, _, err := b.sendAgentReplyCardToBinding(ctx, chat, text)
			return msgID, err
		},
		patchLegacy: func(ctx context.Context, messageID, text string) error {
			return b.patchAgentReplyBinding(ctx, chat, messageID, text)
		},
	}
}

func (b *Bridge) sendCardKitReplyToBinding(ctx context.Context, chat *LarkChat, content, requestUUID string) (string, string, error) {
	if bs, ok, err := b.directReplyTargetForBinding(chat); ok || err != nil {
		if err != nil {
			return "", "", err
		}
		return b.sendCardKitReplyToDirectTarget(ctx, bs, content, requestUUID)
	}
	if chat == nil {
		return "", "", fmt.Errorf("nil lark chat")
	}
	return b.sendCardKitReplyToThread(ctx, chat.RootMsgID, content, requestUUID)
}

func (b *bindingReplyCardBackend) Create(ctx context.Context, text string) (*replyCardHandle, error) {
	delivery, err := b.delivery()
	if err != nil {
		return nil, err
	}
	raw := renderCardKitData(wrapTurnCardElements(renderCardKitAnswerElements(text, b.conversationSection)))
	cardID, err := b.cardKit.CreateCard(ctx, selectManagementCardLocale(raw, delivery.locale))
	if err == nil {
		var messageID string
		requestUUID := uuid.NewString()
		messageID, err = delivery.send(ctx, renderCardKitMessageContent(cardID), requestUUID)
		ambiguousSend := isAmbiguousCardKitSendError(err)
		if ambiguousSend {
			retryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			messageID, err = delivery.send(retryCtx, renderCardKitMessageContent(cardID), requestUUID)
			cancel()
			ambiguousSend = isAmbiguousCardKitSendError(err)
		}
		if err == nil {
			return &replyCardHandle{
				conversationSection: b.conversationSection,
				delivery:            delivery,
				mode:                replyCardModeCardKit,
				cardID:              cardID,
				messageID:           messageID,
				lastText:            text,
				hasAnswer:           true,
				messageUUID:         requestUUID,
			}, nil
		}
		if ambiguousSend {
			log.Warn().Err(err).Str("card_id", cardID).Msg("bridge: CardKit message delivery remains uncertain; preserving send identity")
			return &replyCardHandle{
				conversationSection: b.conversationSection,
				delivery:            delivery,
				mode:                replyCardModeCardKit,
				cardID:              cardID,
				lastText:            text,
				hasAnswer:           true,
				messageUUID:         requestUUID,
				deliveryPending:     true,
			}, nil
		}
	}
	log.Warn().Err(err).Str("card_id", cardID).Msg("bridge: CardKit reply unavailable before send; using legacy reply card")
	fallbackCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	handle, legacyErr := b.CreateLegacy(fallbackCtx, text)
	if legacyErr != nil {
		return nil, errors.Join(fmt.Errorf("create CardKit reply: %w", err), fmt.Errorf("create legacy reply: %w", legacyErr))
	}
	return handle, nil
}

func (b *bindingReplyCardBackend) CreateLegacy(ctx context.Context, text string) (*replyCardHandle, error) {
	if b.createLegacy == nil {
		return nil, fmt.Errorf("legacy reply create is unavailable")
	}
	messageID, err := b.createLegacy(ctx, text)
	if err != nil {
		return nil, err
	}
	return &replyCardHandle{
		mode: replyCardModeLegacy, messageID: messageID, lastText: text, hasAnswer: true,
	}, nil
}

func (b *bindingReplyCardBackend) CreateActivity(ctx context.Context, activity turnCardActivity) (*replyCardHandle, error) {
	if len(activity.JSON) == 0 {
		return nil, fmt.Errorf("empty turn activity")
	}
	delivery, err := b.delivery()
	if err != nil {
		return nil, err
	}
	raw := renderCardKitData(activity.elements())
	cardID, err := b.cardKit.CreateCard(ctx, selectManagementCardLocale(raw, delivery.locale))
	if err != nil {
		return nil, fmt.Errorf("create CardKit activity: %w", err)
	}
	requestUUID := uuid.NewString()
	messageID, err := delivery.send(ctx, renderCardKitMessageContent(cardID), requestUUID)
	ambiguousSend := isAmbiguousCardKitSendError(err)
	if ambiguousSend {
		retryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		messageID, err = delivery.send(retryCtx, renderCardKitMessageContent(cardID), requestUUID)
		cancel()
		ambiguousSend = isAmbiguousCardKitSendError(err)
	}
	if err == nil {
		return &replyCardHandle{
			conversationSection: b.conversationSection,
			delivery:            delivery,
			mode:                replyCardModeCardKit,
			cardID:              cardID,
			messageID:           messageID,
			lastActivity:        selectManagementCardLocale(string(activity.JSON), delivery.locale),
			hasActivity:         true,
			messageUUID:         requestUUID,
		}, nil
	}
	if ambiguousSend {
		log.Warn().Err(err).Str("card_id", cardID).Msg("bridge: CardKit activity delivery remains uncertain; preserving send identity")
		return &replyCardHandle{
			conversationSection: b.conversationSection,
			delivery:            delivery,
			mode:                replyCardModeCardKit,
			cardID:              cardID,
			lastActivity:        selectManagementCardLocale(string(activity.JSON), delivery.locale),
			hasActivity:         true,
			messageUUID:         requestUUID,
			deliveryPending:     true,
		}, nil
	}
	return nil, fmt.Errorf("send CardKit activity: %w", err)
}

func (b *bindingReplyCardBackend) Update(ctx context.Context, handle *replyCardHandle, text string) error {
	return b.update(ctx, handle, text, false)
}

func (b *bindingReplyCardBackend) Calibrate(ctx context.Context, handle *replyCardHandle, text string) error {
	return b.update(ctx, handle, text, true)
}

func (b *bindingReplyCardBackend) update(ctx context.Context, handle *replyCardHandle, text string, allowFinished bool) error {
	if handle == nil || (!allowFinished && handle.finished) {
		return nil
	}
	if handle.mode == replyCardModeLegacy {
		if text == handle.lastText {
			return nil
		}
		if b.patchLegacy == nil {
			return fmt.Errorf("legacy reply patch is unavailable")
		}
		if err := b.patchLegacy(ctx, handle.messageID, text); err != nil {
			return err
		}
		handle.lastText = text
		return nil
	}
	if err := b.ensureDelivered(ctx, handle); err != nil {
		return err
	}
	if err := b.retryPendingInsert(ctx, handle); err != nil {
		return err
	}
	if text == handle.lastText && handle.hasAnswer {
		return nil
	}

	handle.sequence++
	mutationUUID := uuid.NewString()
	var err error
	if handle.hasAnswer {
		err = b.cardKit.UpdateElement(ctx, cardKitReplyUpdate{
			CardID: handle.cardID, ElementID: cardKitReplyElementID, Text: renderAgentResultBody(text),
			UUID: mutationUUID, Sequence: handle.sequence,
		})
	} else {
		answerElements := []cardElement{{Tag: "hr", ElementID: cardKitAnswerDividerElementID}}
		answerElements = append(answerElements, renderCardKitAnswerElements(text, handle.conversationSection)...)
		elements, marshalErr := json.Marshal(answerElements)
		if marshalErr != nil {
			return marshalErr
		}
		insert := cardKitElementInsert{
			CardID: handle.cardID, Type: "insert_after", TargetElementID: cardKitActivityContentID,
			Elements: selectManagementCardLocale(string(elements), handle.delivery.locale), UUID: mutationUUID, Sequence: handle.sequence,
		}
		err = b.cardKit.InsertElements(ctx, insert)
		if err != nil && isAmbiguousCardKitMutationError(err) {
			handle.pendingInsert = &pendingCardKitInsert{kind: pendingInsertAnswer, request: insert, answerText: text}
		}
	}
	if err != nil {
		return err
	}
	handle.lastText = text
	handle.hasAnswer = true
	return nil
}

func (b *bindingReplyCardBackend) UpdateActivity(ctx context.Context, handle *replyCardHandle, activity turnCardActivity) error {
	if handle == nil || handle.finished {
		return nil
	}
	if handle.mode != replyCardModeCardKit {
		return fmt.Errorf("CardKit turn activity unavailable for legacy reply")
	}
	if err := b.ensureDelivered(ctx, handle); err != nil {
		return err
	}
	if err := b.retryPendingInsert(ctx, handle); err != nil {
		return err
	}
	raw := selectManagementCardLocale(string(activity.JSON), handle.delivery.locale)
	if raw == "" {
		return fmt.Errorf("empty turn activity")
	}
	if handle.hasActivity && raw == handle.lastActivity {
		return nil
	}
	handle.sequence++
	mutationUUID := uuid.NewString()
	var err error
	if handle.hasActivity {
		err = b.cardKit.ReplaceElement(ctx, cardKitElementReplace{
			CardID: handle.cardID, ElementID: cardKitActivityContentID, Element: raw,
			UUID: mutationUUID, Sequence: handle.sequence,
		})
	} else {
		activityElements := append(activity.contentElements(), cardElement{Tag: "hr", ElementID: cardKitAnswerDividerElementID})
		elements, marshalErr := json.Marshal(activityElements)
		if marshalErr != nil {
			return marshalErr
		}
		insert := cardKitElementInsert{
			CardID: handle.cardID, Type: "insert_before", TargetElementID: cardKitAnswerHeadingID,
			Elements: selectManagementCardLocale(string(elements), handle.delivery.locale), UUID: mutationUUID, Sequence: handle.sequence,
		}
		err = b.cardKit.InsertElements(ctx, insert)
		if err != nil && isAmbiguousCardKitMutationError(err) {
			handle.pendingInsert = &pendingCardKitInsert{kind: pendingInsertActivity, request: insert, activity: activity}
		}
	}
	if err != nil {
		return err
	}
	handle.lastActivity = raw
	handle.hasActivity = true
	return nil
}

func (b *bindingReplyCardBackend) Finish(ctx context.Context, handle *replyCardHandle, text string) error {
	if handle == nil || handle.finished {
		return nil
	}
	var updateErr error
	if text != "" && text != handle.lastText {
		updateErr = b.Update(ctx, handle, text)
	}
	if handle.mode == replyCardModeLegacy {
		handle.finished = updateErr == nil
		return updateErr
	}
	if err := b.retryPendingInsert(ctx, handle); err != nil {
		return errors.Join(updateErr, err)
	}
	deliveryErr := b.ensureDelivered(ctx, handle)
	handle.sequence++
	finishErr := b.cardKit.FinishCard(ctx, cardKitReplyFinish{
		CardID:   handle.cardID,
		UUID:     uuid.NewString(),
		Sequence: handle.sequence,
	})
	handle.finished = deliveryErr == nil && updateErr == nil && finishErr == nil
	return errors.Join(deliveryErr, updateErr, finishErr)
}

func (b *bindingReplyCardBackend) retryPendingInsert(ctx context.Context, handle *replyCardHandle) error {
	if handle == nil || handle.pendingInsert == nil {
		return nil
	}
	pending := handle.pendingInsert
	if err := b.cardKit.InsertElements(ctx, pending.request); err != nil {
		if !isAmbiguousCardKitMutationError(err) {
			handle.pendingInsert = nil
		}
		return err
	}
	handle.pendingInsert = nil
	switch pending.kind {
	case pendingInsertAnswer:
		handle.lastText = pending.answerText
		handle.hasAnswer = true
	case pendingInsertActivity:
		handle.lastActivity = selectManagementCardLocale(string(pending.activity.JSON), handle.delivery.locale)
		handle.hasActivity = true
	}
	return nil
}

func (b *bindingReplyCardBackend) ensureDelivered(ctx context.Context, handle *replyCardHandle) error {
	if handle == nil || !handle.deliveryPending {
		return nil
	}
	send := handle.delivery.send
	if send == nil {
		send = b.sendCard
	}
	messageID, err := send(ctx, renderCardKitMessageContent(handle.cardID), handle.messageUUID)
	if err != nil {
		return err
	}
	handle.messageID = messageID
	handle.deliveryPending = false
	return nil
}

type ambiguousCardKitSendError struct {
	err error
}

type ambiguousCardKitMutationError struct {
	err error
}

func (e *ambiguousCardKitMutationError) Error() string { return e.err.Error() }
func (e *ambiguousCardKitMutationError) Unwrap() error { return e.err }

func isAmbiguousCardKitMutationError(err error) bool {
	var target *ambiguousCardKitMutationError
	return errors.As(err, &target)
}

func (e *ambiguousCardKitSendError) Error() string { return e.err.Error() }
func (e *ambiguousCardKitSendError) Unwrap() error { return e.err }

func isAmbiguousCardKitSendError(err error) bool {
	var target *ambiguousCardKitSendError
	return errors.As(err, &target)
}

func (b *Bridge) sendCardKitReplyToThread(ctx context.Context, rootMsgID, content, requestUUID string) (string, string, error) {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(rootMsgID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(content).
			ReplyInThread(true).
			Uuid(requestUUID).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", "", &ambiguousCardKitSendError{err: fmt.Errorf("lark reply CardKit card: %w", err)}
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark reply CardKit card: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil {
		return "", "", nil
	}
	return larkMessageResponseIDs(resp.Data.MessageId, resp.Data.ThreadId)
}

func (b *Bridge) sendCardKitReplyToDirectTarget(ctx context.Context, target *directReplyTarget, content, requestUUID string) (string, string, error) {
	if target == nil {
		return "", "", fmt.Errorf("nil direct reply target")
	}
	var receiveIDType, receiveID string
	switch strings.TrimSpace(target.Kind) {
	case botSessionReplyDM:
		receiveIDType, receiveID = "open_id", strings.TrimSpace(target.OpenID)
	case botSessionReplyGroup:
		receiveIDType, receiveID = "chat_id", strings.TrimSpace(target.ChatID)
	default:
		return "", "", fmt.Errorf("direct output target is unknown")
	}
	if receiveID == "" {
		return "", "", fmt.Errorf("direct %s output target is empty", target.Kind)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(receiveID).
			MsgType("interactive").
			Content(content).
			Uuid(requestUUID).
			Build()).
		Build()
	resp, err := b.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", "", &ambiguousCardKitSendError{err: fmt.Errorf("lark create CardKit message: %w", err)}
	}
	if !resp.Success() {
		return "", "", fmt.Errorf("lark create CardKit message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil {
		return "", "", nil
	}
	msgID, threadID, err := larkMessageResponseIDs(resp.Data.MessageId, resp.Data.ThreadId)
	return b.recordDirectReply(target, msgID, threadID, err)
}

func larkMessageResponseIDs(messageID, threadID *string) (string, string, error) {
	if messageID == nil {
		return "", "", nil
	}
	thread := ""
	if threadID != nil {
		thread = *threadID
	}
	return *messageID, thread, nil
}

type larkCardKitReplyAPI struct {
	bridge *Bridge
}

func (a *larkCardKitReplyAPI) CreateCard(ctx context.Context, data string) (string, error) {
	if a == nil || a.bridge == nil || a.bridge.client == nil {
		return "", fmt.Errorf("nil lark client")
	}
	req := larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().Type("card_json").Data(data).Build()).
		Build()
	resp, err := a.bridge.client.Cardkit.V1.Card.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("lark create CardKit reply: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("lark create CardKit reply: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.CardId == nil || *resp.Data.CardId == "" {
		return "", fmt.Errorf("lark create CardKit reply: no card_id in response")
	}
	return *resp.Data.CardId, nil
}

func (a *larkCardKitReplyAPI) UpdateElement(ctx context.Context, update cardKitReplyUpdate) error {
	req := larkcardkit.NewContentCardElementReqBuilder().
		CardId(update.CardID).
		ElementId(update.ElementID).
		Body(larkcardkit.NewContentCardElementReqBodyBuilder().
			Uuid(update.UUID).
			Content(update.Text).
			Sequence(update.Sequence).
			Build()).
		Build()
	resp, err := a.bridge.client.Cardkit.V1.CardElement.Content(ctx, req)
	if err != nil {
		return fmt.Errorf("lark update CardKit reply: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark update CardKit reply: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (a *larkCardKitReplyAPI) InsertElements(ctx context.Context, insert cardKitElementInsert) error {
	body := larkcardkit.NewCreateCardElementReqBodyBuilder().
		Type(insert.Type).
		TargetElementId(insert.TargetElementID).
		Uuid(insert.UUID).
		Sequence(insert.Sequence).
		Elements(insert.Elements).
		Build()
	req := larkcardkit.NewCreateCardElementReqBuilder().
		CardId(insert.CardID).
		Body(body).
		Build()
	resp, err := a.bridge.client.Cardkit.V1.CardElement.Create(ctx, req)
	if err != nil {
		return &ambiguousCardKitMutationError{err: fmt.Errorf("lark insert CardKit turn element: %w", err)}
	}
	if !resp.Success() {
		return fmt.Errorf("lark insert CardKit turn element: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (a *larkCardKitReplyAPI) ReplaceElement(ctx context.Context, replace cardKitElementReplace) error {
	body := larkcardkit.NewUpdateCardElementReqBodyBuilder().
		Uuid(replace.UUID).
		Element(replace.Element).
		Sequence(replace.Sequence).
		Build()
	req := larkcardkit.NewUpdateCardElementReqBuilder().
		CardId(replace.CardID).
		ElementId(replace.ElementID).
		Body(body).
		Build()
	resp, err := a.bridge.client.Cardkit.V1.CardElement.Update(ctx, req)
	if err != nil {
		return fmt.Errorf("lark replace CardKit turn element: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark replace CardKit turn element: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (a *larkCardKitReplyAPI) FinishCard(ctx context.Context, finish cardKitReplyFinish) error {
	settings := `{"config":{"streaming_mode":false}}`
	req := larkcardkit.NewSettingsCardReqBuilder().
		CardId(finish.CardID).
		Body(larkcardkit.NewSettingsCardReqBodyBuilder().
			Settings(settings).
			Uuid(finish.UUID).
			Sequence(finish.Sequence).
			Build()).
		Build()
	resp, err := a.bridge.client.Cardkit.V1.Card.Settings(ctx, req)
	if err != nil {
		return fmt.Errorf("lark finish CardKit reply: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("lark finish CardKit reply: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func renderCardKitReplyData(text string) string {
	return renderCardKitData(wrapTurnCardElements(renderCardKitAnswerElements(text, false)))
}

func renderCardKitAnswerElements(text string, withConversationSection bool) []cardElement {
	elements := renderFinalResultElements(text)
	footer := renderAmuxBrandFooterElements()
	elements = append(elements, footer[0])
	if withConversationSection {
		// Build the section alongside the answer, before wrapping the turn card.
		// This also applies when an activity card receives its answer later.
		elements = append(elements, emptyConversationSection())
	}
	return append(elements, footer[1:]...)
}

// Product headings never share a content-only streaming target with Agent text.
func renderFinalResultElements(text string) []cardElement {
	return []cardElement{
		managementContentElement(sessionMessage("result"), cardElement{Tag: "markdown", ElementID: cardKitAnswerHeadingID}),
		{Tag: "markdown", Content: renderAgentResultBody(text), ElementID: cardKitReplyElementID},
	}
}

func renderAgentResultBody(text string) string {
	return strings.TrimSpace(renderArtifactLinks(normalizeAgentResultText(text)))
}

func renderCardKitData(elements []cardElement) string {
	card := struct {
		Schema string `json:"schema"`
		Config struct {
			StreamingMode  bool `json:"streaming_mode"`
			UpdateMulti    bool `json:"update_multi"`
			WideScreenMode bool `json:"wide_screen_mode"`
			Summary        struct {
				Content string `json:"content"`
			} `json:"summary"`
			StreamingConfig struct {
				PrintFrequencyMS map[string]int `json:"print_frequency_ms"`
				PrintStep        map[string]int `json:"print_step"`
				PrintStrategy    string         `json:"print_strategy"`
			} `json:"streaming_config"`
			WidthMode string `json:"width_mode"`
		} `json:"config"`
		Body struct {
			Elements        []cardElement `json:"elements"`
			Direction       string        `json:"direction"`
			VerticalSpacing string        `json:"vertical_spacing"`
			Padding         string        `json:"padding"`
		} `json:"body"`
	}{Schema: "2.0"}
	card.Config.StreamingMode = true
	card.Config.UpdateMulti = true
	card.Config.WideScreenMode = true
	card.Config.StreamingConfig.PrintFrequencyMS = map[string]int{
		"default": 70,
		"android": 70,
		"ios":     70,
		"pc":      70,
	}
	card.Config.StreamingConfig.PrintStep = map[string]int{
		"default": 1,
		"android": 1,
		"ios":     1,
		"pc":      1,
	}
	card.Config.StreamingConfig.PrintStrategy = "fast"
	card.Config.WidthMode = "fill"
	card.Body.Elements = elements
	card.Body.Direction = "vertical"
	card.Body.VerticalSpacing = "12px"
	card.Body.Padding = "12px 16px 16px 16px"
	raw, _ := json.Marshal(card)
	return string(raw)
}

func renderCardKitMessageContent(cardID string) string {
	content := struct {
		Type string `json:"type"`
		Data struct {
			CardID string `json:"card_id"`
		} `json:"data"`
	}{Type: "card"}
	content.Data.CardID = cardID
	raw, _ := json.Marshal(content)
	return string(raw)
}

func (b *bindingReplyCardBackend) delivery() (replyCardDelivery, error) {
	if b.prepareDelivery != nil {
		return b.prepareDelivery()
	}
	return replyCardDelivery{send: b.sendCard}, nil
}
