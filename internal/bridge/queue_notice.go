package bridge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/rs/zerolog/log"
)

// queueNoticeState tracks, per Feishu chat root, how many inputs are queued
// behind the running turn, and the running main-turn card's refresher. The
// count is shown as one line on that card (never as a new card) while the
// steer_foreground experiment is on.
type queueNoticeState struct {
	mu         sync.Mutex
	counts     map[string]int
	refreshers map[string]queueNoticeRefresher
	// pending marks roots whose queue count changed while no running card
	// could be annotated; the card annotates as soon as it appears.
	pending map[string]bool
}

type queueNoticeRefresher struct {
	token uint64
	fn    func() bool
}

// queueNoticeCount is the number shown on the running card for rootMsgID.
func (b *Bridge) queueNoticeCount(rootMsgID string) int {
	if b == nil || !b.steerForeground.Load() {
		return 0
	}
	b.queueNotice.mu.Lock()
	defer b.queueNotice.mu.Unlock()
	return b.queueNotice.counts[rootMsgID]
}

// registerQueueNoticeRefresher binds the running stream's card refresh. fn
// reports whether a running card existed and was re-rendered.
func (b *Bridge) registerQueueNoticeRefresher(rootMsgID string, token uint64, fn func() bool) {
	b.queueNotice.mu.Lock()
	defer b.queueNotice.mu.Unlock()
	if b.queueNotice.refreshers == nil {
		b.queueNotice.refreshers = make(map[string]queueNoticeRefresher)
	}
	b.queueNotice.refreshers[rootMsgID] = queueNoticeRefresher{token: token, fn: fn}
}

func (b *Bridge) unregisterQueueNoticeRefresher(rootMsgID string, token uint64) {
	b.queueNotice.mu.Lock()
	defer b.queueNotice.mu.Unlock()
	if cur, ok := b.queueNotice.refreshers[rootMsgID]; ok && cur.token == token {
		delete(b.queueNotice.refreshers, rootMsgID)
		delete(b.queueNotice.counts, rootMsgID)
		delete(b.queueNotice.pending, rootMsgID)
	}
}

// setQueueNotice records the queued count and, when it changed, re-renders
// the running card. Without a running card it only logs.
func (b *Bridge) setQueueNotice(rootMsgID string, n int, refresh bool) {
	if b == nil || rootMsgID == "" {
		return
	}
	if n < 0 {
		n = 0
	}
	b.queueNotice.mu.Lock()
	if b.queueNotice.counts == nil {
		b.queueNotice.counts = make(map[string]int)
	}
	prev := b.queueNotice.counts[rootMsgID]
	if n == 0 {
		delete(b.queueNotice.counts, rootMsgID)
	} else {
		b.queueNotice.counts[rootMsgID] = n
	}
	refresher, hasRefresher := b.queueNotice.refreshers[rootMsgID]
	b.queueNotice.mu.Unlock()
	if !refresh || prev == n {
		return
	}
	log.Info().Str("root_msg_id", rootMsgID).Int("queued", n).Int("prev", prev).Bool("has_card_refresher", hasRefresher).
		Msg("bridge: queue notice changed")
	if hasRefresher && refresher.fn != nil && refresher.fn() {
		b.setQueueNoticePending(rootMsgID, false)
		return
	}
	if n > 0 {
		b.setQueueNoticePending(rootMsgID, true)
		log.Debug().Str("root_msg_id", rootMsgID).Int("queued", n).
			Msg("bridge: input queued before the running card exists; annotating when it appears")
	}
}

func (b *Bridge) setQueueNoticePending(rootMsgID string, pending bool) {
	b.queueNotice.mu.Lock()
	defer b.queueNotice.mu.Unlock()
	if !pending {
		delete(b.queueNotice.pending, rootMsgID)
		return
	}
	if b.queueNotice.pending == nil {
		b.queueNotice.pending = make(map[string]bool)
	}
	b.queueNotice.pending[rootMsgID] = true
}

// retryQueueNoticeAnnotation annotates a card that just appeared with a
// queue count recorded before it existed.
func (b *Bridge) retryQueueNoticeAnnotation(rootMsgID string) {
	if b == nil || rootMsgID == "" {
		return
	}
	b.queueNotice.mu.Lock()
	pending := b.queueNotice.pending[rootMsgID] && b.queueNotice.counts[rootMsgID] > 0
	refresher, ok := b.queueNotice.refreshers[rootMsgID]
	b.queueNotice.mu.Unlock()
	if !pending || !ok || refresher.fn == nil {
		return
	}
	if refresher.fn() {
		b.setQueueNoticePending(rootMsgID, false)
	}
}

// queuedInputCount asks the runtime how many inputs wait in the session queue.
func (b *Bridge) queuedInputCount(ctx context.Context, chat *LarkChat) (int, error) {
	queueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := b.hub.Route(queueCtx, chat.TargetID, "QueueList", &pb.QueueListRequest{Name: chat.SessionName})
	if err != nil {
		return 0, err
	}
	var out pb.QueueListResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return 0, err
	}
	return len(out.Items), nil
}

// refreshQueueNoticeAsync re-reads the session queue and updates the running
// card's queue line. No-op unless the steer_foreground experiment is on.
func (b *Bridge) refreshQueueNoticeAsync(targetID, sessionName, rootMsgID string) {
	if b == nil || b.hub == nil || !b.steerForeground.Load() || strings.TrimSpace(rootMsgID) == "" {
		return
	}
	chat := &LarkChat{TargetID: targetID, SessionName: sessionName, RootMsgID: rootMsgID}
	go func() {
		n, err := b.queuedInputCount(context.Background(), chat)
		if err != nil {
			log.Warn().Err(err).Str("session", sessionName).Msg("bridge: read session queue for queue notice")
			return
		}
		b.setQueueNotice(rootMsgID, n, true)
	}()
}
