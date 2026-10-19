package bridge

import (
	"context"
	"html"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"
)

const (
	streamingReplyCardSoftLimit = 8000
	replyStreamCommandBuffer    = 4096
)

// maxTablesPerReplyCard is Feishu CardKit 2.0's cap on the number of GFM tables
// a single card may contain. A card with more tables is rejected wholesale with
// ErrCode 11310 ("card table number over limit"), silently dropping the reply,
// so a table-heavy reply is split across multiple cards each staying at or below
// this many tables.
const maxTablesPerReplyCard = 5

type replyCommandKind uint8

const (
	replyCommandAdd replyCommandKind = iota
	replyCommandStartMessage
	replyCommandReplace
	replyCommandReconcileFinalMessage
	replyCommandActivity
	replyCommandSupersede
	replyCommandFlush
	replyCommandFinalizeForBot
	replyCommandConversationCard
	replyCommandReset
	replyCommandClose
)

type replyCommand struct {
	kind                   replyCommandKind
	text                   string
	activity               turnCardActivity
	activityResult         chan replyActivityResult
	botOpenID              string
	botFinalResult         chan bool
	conversationCardResult chan *ConversationCard
	done                   chan struct{}
}

type replyActivityResult struct {
	handled bool
	err     error
}

type turnActivityCardBackend interface {
	replyCardBackend
	CreateActivity(context.Context, turnCardActivity) (*replyCardHandle, error)
	UpdateActivity(context.Context, *replyCardHandle, turnCardActivity) error
}

type legacyReplyCardBackend interface {
	CreateLegacy(context.Context, string) (*replyCardHandle, error)
}

// streamingReplySender is a small actor. Attach event handling only queues
// text; a single worker owns reply state and serializes CardKit network calls.
type streamingReplySender struct {
	minInterval time.Duration
	backend     replyCardBackend
	onFinal     func(text string)

	commands  chan replyCommand
	done      chan struct{}
	closeOnce sync.Once
}

type replyStreamState struct {
	sender *streamingReplySender

	current                strings.Builder
	full                   strings.Builder
	finalized              []replyCardRecord
	handle                 *replyCardHandle
	primaryHandle          *replyCardHandle
	pendingActivity        *turnCardActivity
	forceLegacy            bool
	lastSent               string
	lastSentAt             time.Time
	dirty                  bool
	timer                  *time.Timer
	finalNotified          bool
	superseded             bool
	messageBoundaryPending bool
}

type replyCardRecord struct {
	handle *replyCardHandle
	text   string
}

func newStreamingReplySender(minInterval time.Duration, backend replyCardBackend, onFinal func(text string)) *streamingReplySender {
	s := &streamingReplySender{
		minInterval: minInterval,
		backend:     backend,
		onFinal:     onFinal,
		commands:    make(chan replyCommand, replyStreamCommandBuffer),
		done:        make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *streamingReplySender) run() {
	state := &replyStreamState{sender: s}
	defer close(s.done)
	for {
		var timerC <-chan time.Time
		if state.timer != nil {
			timerC = state.timer.C
		}
		select {
		case cmd := <-s.commands:
			stop := state.handleCommand(cmd)
			if cmd.done != nil {
				close(cmd.done)
			}
			if stop {
				return
			}
		case <-timerC:
			state.timer = nil
			state.maybeSend()
		}
	}
}

func (s *streamingReplySender) enqueue(kind replyCommandKind, text string) {
	select {
	case s.commands <- replyCommand{kind: kind, text: text}:
	case <-s.done:
	}
}

func (s *streamingReplySender) runSync(kind replyCommandKind) {
	done := make(chan struct{})
	select {
	case s.commands <- replyCommand{kind: kind, done: done}:
	case <-s.done:
		return
	}
	select {
	case <-done:
	case <-s.done:
	}
}

func (s *streamingReplySender) ReplaceCurrent(text string) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	s.enqueue(replyCommandReplace, text)
}

func (s *streamingReplySender) UpdateActivity(activity turnCardActivity) (bool, error) {
	result := make(chan replyActivityResult, 1)
	select {
	case s.commands <- replyCommand{kind: replyCommandActivity, activity: activity, activityResult: result}:
	case <-s.done:
		return false, nil
	}
	select {
	case got := <-result:
		return got.handled, got.err
	case <-s.done:
		return false, nil
	}
}

func (s *streamingReplySender) Flush() {
	s.runSync(replyCommandFlush)
}

// ConversationCard transfers a reference only after all stream mutations have
// finished. The actor keeps ownership of the mutable handle itself.
func (s *streamingReplySender) ConversationCard() *ConversationCard {
	result := make(chan *ConversationCard, 1)
	select {
	case s.commands <- replyCommand{kind: replyCommandConversationCard, conversationCardResult: result}:
	case <-s.done:
		return nil
	}
	select {
	case card := <-result:
		return card
	case <-s.done:
		return nil
	}
}

// FinalizeForBot completes a single reply card with a native CardKit mention
// while keeping the logical Agent answer passed to onFinal undecorated. It
// returns false when the answer spans multiple cards or the final card could
// not be updated, so the caller can use the legacy post + at fallback.
func (s *streamingReplySender) FinalizeForBot(botOpenID string) bool {
	result := make(chan bool, 1)
	select {
	case s.commands <- replyCommand{kind: replyCommandFinalizeForBot, botOpenID: botOpenID, botFinalResult: result}:
	case <-s.done:
		return false
	}
	select {
	case delivered := <-result:
		return delivered
	case <-s.done:
		return false
	}
}

// Supersede finalizes the visible answer and ignores late output from the
// interrupted turn until Reset starts the replacement turn.
func (s *streamingReplySender) Supersede() {
	s.runSync(replyCommandSupersede)
}

// Segment remains a compatibility no-op. Tool activity does not split the
// turn-level answer stream.
func (s *streamingReplySender) Segment() {
}

func (s *streamingReplySender) Reset() {
	s.runSync(replyCommandReset)
}

func (s *streamingReplySender) Close() {
	s.closeOnce.Do(func() {
		s.runSync(replyCommandClose)
	})
}

func (s *replyStreamState) handleCommand(cmd replyCommand) bool {
	switch cmd.kind {
	case replyCommandAdd:
		s.addDelta(cmd.text)
	case replyCommandStartMessage:
		if strings.TrimSpace(s.full.String()) != "" {
			s.messageBoundaryPending = true
		}
	case replyCommandReplace:
		s.replaceCurrent(cmd.text)
	case replyCommandReconcileFinalMessage:
		s.reconcileFinalMessage(cmd.text)
	case replyCommandActivity:
		handled, err := s.updateActivity(cmd.activity)
		if cmd.activityResult != nil {
			cmd.activityResult <- replyActivityResult{handled: handled, err: err}
		}
	case replyCommandSupersede:
		// Freeze the visible answer, but keep the CardKit handle mutable until the
		// old turn's terminal activity patches it to stopped/failed/done. Closing
		// streaming mode here marks the handle finished, making that final patch a
		// no-op in the real CardKit backend.
		s.forceSend()
		s.notifyFinal()
		s.stopTimer()
		s.superseded = true
	case replyCommandFlush:
		s.finishAll()
		s.notifyFinal()
	case replyCommandFinalizeForBot:
		delivered := s.finalizeForBot(cmd.botOpenID)
		if cmd.botFinalResult != nil {
			cmd.botFinalResult <- delivered
		}
	case replyCommandConversationCard:
		var card *ConversationCard
		if h := s.handle; h != nil && h.finished && h.hasAnswer && h.conversationSection && h.mode == replyCardModeCardKit && h.messageID != "" {
			card = &ConversationCard{MessageID: h.messageID, CardID: h.cardID, Sequence: h.sequence, Locale: string(h.delivery.locale)}
		}
		cmd.conversationCardResult <- card
	case replyCommandReset:
		s.finishAll()
		s.notifyFinal()
		s.resetTurn()
	case replyCommandClose:
		s.finishAll()
		s.notifyFinal()
		s.stopTimer()
		return true
	}
	return false
}

func (s *replyStreamState) finalizeForBot(botOpenID string) bool {
	botOpenID = strings.TrimSpace(botOpenID)
	if botOpenID == "" || s.superseded || len(s.finalized) != 0 || strings.TrimSpace(s.full.String()) == "" {
		s.finishAll()
		s.notifyFinal()
		return false
	}
	s.retryPendingActivity()
	s.forceSend()
	if s.handle == nil {
		s.notifyFinal()
		return false
	}
	visibleText := `<at id="` + html.EscapeString(botOpenID) + "\"></at> 协作结果\n\n" + strings.TrimRight(s.current.String(), "\n")
	var err error
	if s.handle.finished {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = s.sender.backend.Calibrate(ctx, s.handle, visibleText)
		cancel()
	} else {
		err = s.finishHandle(s.handle, visibleText)
	}
	s.notifyFinal()
	return err == nil && s.handle.finished
}

func (s *replyStreamState) updateActivity(activity turnCardActivity) (bool, error) {
	if s.superseded && !activity.Terminal {
		return true, nil
	}
	s.pendingActivity = &activity
	backend, ok := s.sender.backend.(turnActivityCardBackend)
	if !ok {
		s.pendingActivity = nil
		return false, nil
	}
	handle := s.primaryHandle
	if handle == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		created, err := backend.CreateActivity(ctx, activity)
		cancel()
		if err != nil {
			s.forceLegacy = true
			s.pendingActivity = nil
			return false, err
		}
		s.primaryHandle = created
		if s.handle == nil && len(s.finalized) == 0 {
			s.handle = created
		}
		s.pendingActivity = nil
		return true, nil
	}
	if handle.mode != replyCardModeCardKit {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := backend.UpdateActivity(ctx, handle, activity)
	cancel()
	if err == nil {
		s.pendingActivity = nil
	}
	return true, err
}

func (s *replyStreamState) addDelta(delta string) {
	if s.superseded {
		return
	}
	if strings.TrimSpace(delta) == "" && strings.TrimSpace(s.current.String()) == "" {
		return
	}
	if s.messageBoundaryPending && strings.TrimSpace(delta) != "" {
		separator := "\n\n"
		full := s.full.String()
		if strings.HasSuffix(full, "\n\n") {
			separator = ""
		} else if strings.HasSuffix(full, "\n") {
			separator = "\n"
		}
		s.full.WriteString(separator)
		s.current.WriteString(separator)
		s.messageBoundaryPending = false
	}
	s.full.WriteString(delta)
	s.current.WriteString(delta)
	s.splitOversized()
	s.dirty = true
	s.maybeSend()
}

func (s *replyStreamState) replaceCurrent(text string) {
	if s.superseded {
		return
	}
	text = normalizeAgentResultText(text)
	if len(s.finalized) > 0 {
		s.calibrateSplitTurn(text)
		return
	}
	oldCurrent := s.current.String()
	full := s.full.String()
	if strings.TrimRight(full, "\n") == text {
		return
	}
	continuation := false
	if oldCurrent != "" && strings.HasSuffix(full, oldCurrent) {
		prefix := full[:len(full)-len(oldCurrent)]
		if prefix != "" && strings.HasPrefix(text, prefix) {
			text = strings.TrimPrefix(text, prefix)
			continuation = true
		}
	} else if oldCurrent == "" && full != "" && strings.HasPrefix(text, full) {
		text = strings.TrimPrefix(text, full)
		continuation = true
	}
	if strings.TrimSpace(text) == "" || strings.TrimRight(oldCurrent, "\n") == text {
		return
	}
	switch {
	case oldCurrent != "" && strings.HasSuffix(full, oldCurrent):
		prefix := full[:len(full)-len(oldCurrent)]
		if continuation {
			full = prefix + text
			break
		}
		if lastReplySegment(prefix) == strings.TrimSpace(text) {
			// The replacement repeats the segment right before it.
			return
		}
		full = joinReplySegments(prefix, text)
	case full == "":
		full = text
	case continuation:
		full += text
	default:
		if lastReplySegment(full) == strings.TrimSpace(text) {
			return
		}
		// A separate assistant message: keep a paragraph break so fenced code
		// blocks and paragraphs from different messages never glue together.
		full = joinReplySegments(full, text)
	}
	s.current.Reset()
	s.current.WriteString(text)
	s.full.Reset()
	s.full.WriteString(full)

	s.splitOversized()
	s.dirty = true
	s.maybeSend()
}

func (s *replyStreamState) reconcileFinalMessage(text string) {
	text = normalizeAgentResultText(text)
	if s.superseded || strings.TrimSpace(text) == "" {
		return
	}
	full := s.full.String()
	visible := strings.TrimRight(full, "\n")
	if visible == "" {
		s.addDelta(text)
		return
	}
	if strings.HasSuffix(visible, text) {
		return
	}

	overlapRunes := finalMessageOverlap(visible, text)
	fullRunes := len([]rune(visible))
	if overlapRunes > 0 && (overlapRunes >= 4 || overlapRunes == fullRunes) {
		s.addDelta(string([]rune(text)[overlapRunes:]))
		return
	}

	if lastReplySegment(full) == strings.TrimSpace(text) {
		return
	}
	separator := "\n\n"
	if strings.HasSuffix(full, "\n\n") {
		separator = ""
	} else if strings.HasSuffix(full, "\n") {
		separator = "\n"
	}
	s.addDelta(separator + text)
}

// joinReplySegments joins two assistant message segments with a paragraph
// break unless the left side already ends with one (or is a prefix that
// continues the same message mid-line, i.e. empty).
func joinReplySegments(left, right string) string {
	if left == "" {
		return right
	}
	right = strings.TrimLeft(right, "\n")
	switch {
	case strings.HasSuffix(left, "\n\n"):
		return left + right
	case strings.HasSuffix(left, "\n"):
		return left + "\n" + right
	default:
		return left + "\n\n" + right
	}
}

// lastReplySegment is the last paragraph-separated segment of the visible
// reply, used to drop an identical consecutive repeat.
func lastReplySegment(full string) string {
	full = strings.TrimSpace(full)
	if i := strings.LastIndex(full, "\n\n"); i >= 0 {
		return strings.TrimSpace(full[i+2:])
	}
	return full
}

// finalMessageOverlap returns the longest suffix of streamed that is also a
// prefix of final. Rune-based matching avoids splitting UTF-8 text while
// recovering a final message whose last deltas were dropped.
func finalMessageOverlap(streamed, final string) int {
	streamedRunes := []rune(streamed)
	finalRunes := []rune(final)
	if len(streamedRunes) == 0 || len(finalRunes) == 0 {
		return 0
	}

	prefix := make([]int, len(finalRunes))
	for i, matched := 1, 0; i < len(finalRunes); i++ {
		for matched > 0 && finalRunes[i] != finalRunes[matched] {
			matched = prefix[matched-1]
		}
		if finalRunes[i] == finalRunes[matched] {
			matched++
		}
		prefix[i] = matched
	}

	matched := 0
	for i, current := range streamedRunes {
		for matched > 0 && current != finalRunes[matched] {
			matched = prefix[matched-1]
		}
		if current == finalRunes[matched] {
			matched++
		}
		if matched == len(finalRunes) && i != len(streamedRunes)-1 {
			matched = prefix[matched-1]
		}
	}
	return matched
}

// splitOversized finalizes cards on markdown-safe boundaries whenever the
// pending text exceeds a card limit — either the byte soft limit or the per-card
// table cap (Feishu CardKit 2.0 rejects a card with more than
// maxTablesPerReplyCard tables, ErrCode 11310). If a split occurs inside a fenced
// code block, each card remains independently valid.
func (s *replyStreamState) splitOversized() {
	for {
		text := s.current.String()
		head, tail, reopen, ok := splitCardForLimits(text, streamingReplyCardSoftLimit)
		if !ok {
			return
		}
		next := reopen + tail
		if head == "" || len(next) >= len(text) {
			return
		}
		s.current.Reset()
		s.current.WriteString(head)
		s.finishCurrentForSplit()
		if s.handle != nil {
			s.finalized = append(s.finalized, replyCardRecord{handle: s.handle, text: head})
		}
		s.resetCurrent()
		s.current.WriteString(next)
	}
}

func (s *replyStreamState) finishCurrentForSplit() {
	s.forceSend()
	if s.handle == nil || s.handle == s.primaryHandle {
		return
	}
	s.finishHandle(s.handle, strings.TrimRight(s.current.String(), "\n"))
}

func (s *replyStreamState) calibrateSplitTurn(text string) {
	desired := splitMarkdownCards(text, streamingReplyCardSoftLimit)
	records := append([]replyCardRecord(nil), s.finalized...)
	if s.handle != nil {
		records = append(records, replyCardRecord{handle: s.handle, text: strings.TrimRight(s.current.String(), "\n")})
	}
	s.full.Reset()
	s.full.WriteString(text)
	for len(desired) < len(records) {
		desired = append(desired, " ")
	}

	for i, desiredText := range desired {
		if i >= len(records) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			handle, err := s.createCard(ctx, desiredText)
			cancel()
			if err != nil {
				log.Warn().Err(err).Msg("bridge: create result calibration reply card error")
				s.adoptCalibratedRecords(records)
				return
			}
			records = append(records, replyCardRecord{handle: handle, text: desiredText})
		} else if desiredText != records[i].text {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := s.sender.backend.Calibrate(ctx, records[i].handle, desiredText)
			cancel()
			if err != nil {
				log.Warn().Err(err).
					Str("card_id", records[i].handle.cardID).
					Msg("bridge: calibrate reply card error")
				// Finish will retry the authoritative text at turn end, even if
				// this card had already left streaming mode.
				records[i].handle.finished = false
			}
			records[i].text = desiredText
		}
		if i < len(desired)-1 && records[i].handle != nil && records[i].handle != s.primaryHandle && !records[i].handle.finished {
			s.finishHandle(records[i].handle, records[i].text)
		}
		s.adoptCalibratedRecords(records)
	}
}

func (s *replyStreamState) adoptCalibratedRecords(records []replyCardRecord) {
	if len(records) == 0 {
		return
	}
	last := records[len(records)-1]
	s.finalized = append([]replyCardRecord(nil), records[:len(records)-1]...)
	s.handle = last.handle
	s.current.Reset()
	s.current.WriteString(last.text)
	s.lastSent = last.text
	s.lastSentAt = time.Now()
	s.dirty = false
}

func (s *replyStreamState) maybeSend() {
	if !s.dirty {
		return
	}
	if !s.lastSentAt.IsZero() {
		remaining := s.sender.minInterval - time.Since(s.lastSentAt)
		if remaining > 0 {
			if s.timer == nil {
				s.timer = time.NewTimer(remaining)
			}
			return
		}
	}
	s.send()
}

func (s *replyStreamState) forceSend() {
	s.stopTimer()
	s.lastSentAt = time.Time{}
	if strings.TrimRight(s.current.String(), "\n") != s.lastSent {
		s.dirty = true
	}
	s.send()
}

func (s *replyStreamState) send() {
	text := strings.TrimRight(s.current.String(), "\n")
	if strings.TrimSpace(text) == "" {
		s.current.Reset()
		s.dirty = false
		return
	}
	if text == s.lastSent {
		s.dirty = false
		return
	}
	if s.sender.backend == nil {
		log.Warn().Msg("bridge: streaming reply backend is nil")
		s.dirty = false
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if s.handle == nil {
		handle, err := s.createCard(ctx, text)
		s.lastSentAt = time.Now()
		if err != nil {
			log.Warn().Err(err).Msg("bridge: create streaming reply error")
			return
		}
		s.handle = handle
		if handle != nil && handle.mode == replyCardModeLegacy {
			s.forceLegacy = true
		}
		if s.primaryHandle == nil {
			s.primaryHandle = handle
		}
	} else {
		if err := s.sender.backend.Update(ctx, s.handle, text); err != nil {
			s.lastSentAt = time.Now()
			log.Warn().Err(err).
				Str("card_id", s.handle.cardID).
				Str("msg_id", s.handle.messageID).
				Msg("bridge: update streaming reply error")
			return
		}
		s.lastSentAt = time.Now()
	}
	s.lastSent = text
	s.dirty = false
}

func (s *replyStreamState) finishCurrent() {
	s.forceSend()
	if s.handle == nil {
		return
	}
	s.finishHandle(s.handle, strings.TrimRight(s.current.String(), "\n"))
}

func (s *replyStreamState) finishAll() {
	s.retryPendingActivity()
	for i := range s.finalized {
		if s.finalized[i].handle == nil || s.finalized[i].handle.finished {
			continue
		}
		s.finishHandle(s.finalized[i].handle, s.finalized[i].text)
	}
	s.finishCurrent()
}

func (s *replyStreamState) createCard(ctx context.Context, text string) (*replyCardHandle, error) {
	if s.forceLegacy {
		if backend, ok := s.sender.backend.(legacyReplyCardBackend); ok {
			return backend.CreateLegacy(ctx, text)
		}
	}
	return s.sender.backend.Create(ctx, text)
}

func (s *replyStreamState) retryPendingActivity() {
	if s.pendingActivity == nil || s.primaryHandle == nil || s.forceLegacy {
		return
	}
	backend, ok := s.sender.backend.(turnActivityCardBackend)
	if !ok {
		return
	}
	activity := *s.pendingActivity
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := backend.UpdateActivity(ctx, s.primaryHandle, activity)
	cancel()
	if err != nil {
		log.Warn().Err(err).
			Str("card_id", s.primaryHandle.cardID).
			Msg("bridge: retry final turn activity error")
		return
	}
	s.pendingActivity = nil
}

func (s *replyStreamState) finishHandle(handle *replyCardHandle, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.sender.backend.Finish(ctx, handle, text); err != nil {
		log.Warn().Err(err).
			Str("card_id", handle.cardID).
			Str("msg_id", handle.messageID).
			Msg("bridge: finish streaming reply error")
		return err
	}
	return nil
}

func (s *replyStreamState) notifyFinal() {
	text := strings.TrimRight(s.full.String(), "\n")
	if s.finalNotified || text == "" {
		return
	}
	s.finalNotified = true
	if s.sender.onFinal != nil {
		s.sender.onFinal(text)
	}
}

func (s *replyStreamState) resetTurn() {
	s.stopTimer()
	s.current.Reset()
	s.full.Reset()
	s.finalized = nil
	s.handle = nil
	s.primaryHandle = nil
	s.pendingActivity = nil
	s.forceLegacy = false
	s.lastSent = ""
	s.lastSentAt = time.Time{}
	s.dirty = false
	s.finalNotified = false
	s.superseded = false
	s.messageBoundaryPending = false
}

// splitMarkdownCards splits text into cards each within both the byte soft limit
// and the per-card table cap (maxTablesPerReplyCard; Feishu rejects a card
// exceeding it with ErrCode 11310). It is used by the turn-end calibration path,
// so a recomputed multi-card turn stays table-safe just like the streaming path.
func splitMarkdownCards(text string, limit int) []string {
	remaining := text
	cards := make([]string, 0, 1)
	for {
		head, tail, reopen, ok := splitCardForLimits(remaining, limit)
		if !ok {
			break
		}
		next := reopen + tail
		if head == "" || len(next) >= len(remaining) {
			break
		}
		cards = append(cards, head)
		remaining = next
	}
	if remaining != "" {
		cards = append(cards, remaining)
	}
	return cards
}

func (s *replyStreamState) resetCurrent() {
	s.current.Reset()
	s.handle = nil
	s.lastSent = ""
	s.lastSentAt = time.Time{}
	s.dirty = false
}

func (s *replyStreamState) stopTimer() {
	if s.timer == nil {
		return
	}
	if !s.timer.Stop() {
		select {
		case <-s.timer.C:
		default:
		}
	}
	s.timer = nil
}

// splitMarkdownCard splits text at a line boundary where possible. Open code
// fences are closed on the first card and reopened on the continuation card.
func splitMarkdownCard(text string, limit int) (head, tail, reopen string) {
	if cut := strings.LastIndexByte(text[:limit], '\n'); cut > 0 {
		head, tail = text[:cut], text[cut+1:]
	} else {
		cut := limit
		for cut > 0 && cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut--
		}
		head, tail = text[:cut], text[cut:]
	}
	if openLine, open := openFenceAt(head); open {
		if !strings.HasSuffix(head, "\n") {
			head += "\n"
		}
		head += "```"
		reopen = openLine + "\n"
	}
	return head, tail, reopen
}

func openFenceAt(text string) (openLine string, open bool) {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "```") {
			if open {
				open, openLine = false, ""
			} else {
				open, openLine = true, strings.TrimRight(line, "\r")
			}
		}
	}
	return openLine, open
}

// splitCardForLimits picks the earliest card boundary forced by either the byte
// soft limit (byteLimit) or the per-card table limit (maxTablesPerReplyCard;
// Feishu rejects a card exceeding it with ErrCode 11310). When both caps are
// exceeded it takes whichever yields the shorter head, so every sealed card stays
// under both caps (in particular ≤ maxTablesPerReplyCard tables, the hard Feishu
// constraint). ok is false when neither cap is exceeded, or no safe cut exists.
func splitCardForLimits(text string, byteLimit int) (head, tail, reopen string, ok bool) {
	overBytes := len(text) > byteLimit
	overTables := countMarkdownTables(text) > maxTablesPerReplyCard
	if !overBytes && !overTables {
		return "", "", "", false
	}
	if overTables {
		if h, t, r, tok := splitMarkdownCardAtTableLimit(text, maxTablesPerReplyCard); tok {
			head, tail, reopen, ok = h, t, r, true
		}
	}
	if overBytes {
		h, t, r := splitMarkdownCard(text, byteLimit)
		if h != "" && (!ok || len(h) < len(head)) {
			head, tail, reopen, ok = h, t, r, true
		}
	}
	return head, tail, reopen, ok
}

// countMarkdownTables returns the number of GFM tables in text, matching how a
// Feishu markdown card renders them: a table is a maximal run of contiguous
// pipe-rows (isMarkdownTableLine) that occurs outside a fenced code block. Pipe
// rows inside a ``` fence are code, not tables, so they are not counted.
func countMarkdownTables(text string) int {
	count := 0
	inCode := false
	inTable := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "```") {
			inCode = !inCode
			inTable = false
			continue
		}
		if inCode {
			continue
		}
		if isMarkdownTableLine(strings.TrimSpace(line)) {
			if !inTable {
				count++
				inTable = true
			}
			continue
		}
		inTable = false
	}
	return count
}

// splitMarkdownCardAtTableLimit splits text (which contains more than maxTables
// GFM tables) into a head holding exactly maxTables tables and a tail holding
// the rest, so each resulting Feishu card stays under the per-card table limit.
// The cut is taken at the first blank line after the maxTables-th table block
// ends (falling back to immediately after that block when the next section is
// contiguous), so the next table's heading and lead-in prose travel into the
// tail card alongside their table. Like splitMarkdownCard, a dangling ``` fence
// left open in the head is closed and carried to the tail via reopen. ok is
// false when text has at most maxTables tables or no usable boundary exists.
func splitMarkdownCardAtTableLimit(text string, maxTables int) (head, tail, reopen string, ok bool) {
	if maxTables < 1 {
		return "", "", "", false
	}
	lines := strings.Split(text, "\n")
	inCode := false
	inTable := false
	tables := 0
	nthTableEnd := -1 // index of the last line of the maxTables-th table block
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "```") {
			inCode = !inCode
			inTable = false
			continue
		}
		if inCode {
			continue
		}
		if isMarkdownTableLine(strings.TrimSpace(line)) {
			if !inTable {
				tables++
				inTable = true
			}
			if tables == maxTables {
				nthTableEnd = i
			}
			continue
		}
		inTable = false
	}
	if tables <= maxTables || nthTableEnd < 0 {
		return "", "", "", false
	}
	// Prefer the first blank line after the Nth table block; stop if the next
	// table begins first, keeping the cut right after the Nth block so that
	// table's heading leads the tail card.
	cut := nthTableEnd
	for j := nthTableEnd + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			cut = j
			break
		}
		if isMarkdownTableLine(strings.TrimSpace(lines[j])) {
			break
		}
	}
	head = strings.Join(lines[:cut+1], "\n")
	tail = strings.Join(lines[cut+1:], "\n")
	if strings.TrimSpace(head) == "" || strings.TrimSpace(tail) == "" {
		return "", "", "", false
	}
	if openLine, open := openFenceAt(head); open {
		if !strings.HasSuffix(head, "\n") {
			head += "\n"
		}
		head += "```"
		reopen = openLine + "\n"
	}
	return head, tail, reopen, true
}
