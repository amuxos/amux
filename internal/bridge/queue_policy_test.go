package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentui"
)

// Bridge ownership follows the active turn. Runtime alone evaluates FIFO.
func TestOwnerMessageUsesRunningTurnDespiteNewerReceipt(t *testing.T) {
	var c turnCoalescer
	own := compactTurnInputRefs("ou-a", "om-1")
	c.recordTurnSenders("root1", "turn-1", own)
	c.observeTurnStarted("root1", "turn-1")
	if _, ok := c.steerTarget("root1", compactTurnInputRefs("ou-a", "om-2")); !ok {
		t.Fatal("owner must steer when nothing is queued")
	}
	// Another member's message queues as turn-2.
	c.recordTurnSenders("root1", "turn-2", compactTurnInputRefs("ou-b", "om-3"))
	if mode := c.busyInputMode("root1", compactTurnInputRefs("ou-a", "om-4")); !mode.steer || mode.steerExpectedTurnID != "turn-1" {
		t.Fatalf("Bridge must pass the known active turn to Runtime: %+v", mode)
	}
	// Once the queue drains (turn-2 runs), steering is about turn-2's owner.
	c.observeTurnFinished("root1", "turn-1")
	c.observeTurnStarted("root1", "turn-2")
	if mode := c.busyInputMode("root1", compactTurnInputRefs("ou-b", "om-5")); !mode.steer || mode.steerExpectedTurnID != "turn-2" {
		t.Fatalf("turn-2 owner mode = %+v", mode)
	}
}

func TestQueueNoticeAnnotatesCardOnceItAppears(t *testing.T) {
	b := &Bridge{}
	b.steerForeground.Store(true)
	cardExists := false
	refreshes := 0
	b.registerQueueNoticeRefresher("root", 1, func() bool {
		if !cardExists {
			return false
		}
		refreshes++
		return true
	})
	b.setQueueNotice("root", 1, true) // queued before the card exists
	if refreshes != 0 {
		t.Fatal("refreshed without a card")
	}
	cardExists = true
	b.retryQueueNoticeAnnotation("root")
	if refreshes != 1 || b.queueNoticeCount("root") != 1 {
		t.Fatalf("refreshes=%d count=%d", refreshes, b.queueNoticeCount("root"))
	}
	b.retryQueueNoticeAnnotation("root") // pending cleared: no extra render
	if refreshes != 1 {
		t.Fatalf("refreshes = %d", refreshes)
	}
}

func TestSubtaskReportCardDropsEmptyDetailsPanel(t *testing.T) {
	meta := toolActivityCardMeta{Status: "done", SubtaskReport: true, Activity: &agentui.ActivityView{Status: "done"}}
	raw, _ := json.Marshal(renderTurnActivity(meta).Element)
	if strings.Contains(string(raw), "暂无更多执行详情") || strings.Contains(string(raw), "collapsible_panel\",\"element_id\":\"execution_summary") {
		t.Fatalf("subtask report card still renders empty details panel: %s", raw)
	}
	if !strings.Contains(string(raw), "execution_summary") {
		t.Fatalf("status line missing: %s", raw)
	}
	meta.SubtaskReport = false
	raw, _ = json.Marshal(renderTurnActivity(meta).Element)
	if !strings.Contains(string(raw), "暂无更多执行详情") {
		t.Fatalf("ordinary turn lost its details fallback: %s", raw)
	}
}

func TestStatusPulseRefreshesQueueNoticeWhileTurnRuns(t *testing.T) {
	// refreshQueueNoticeAsync is what the status ticker calls; this test pins
	// the contract that a changed QueueList depth re-renders the running card
	// even when the bridge itself did not submit the queued item (hub path).
	b := &Bridge{}
	b.steerForeground.Store(true)
	refreshes := 0
	b.registerQueueNoticeRefresher("root", 1, func() bool {
		refreshes++
		return true
	})
	b.setQueueNotice("root", 0, false)
	b.setQueueNotice("root", 1, true) // hub enqueued a completion notify
	if refreshes != 1 || b.queueNoticeCount("root") != 1 {
		t.Fatalf("refreshes=%d count=%d", refreshes, b.queueNoticeCount("root"))
	}
	b.setQueueNotice("root", 1, true) // unchanged
	if refreshes != 1 {
		t.Fatal("unchanged depth re-rendered")
	}
	b.setQueueNotice("root", 2, true)
	if refreshes != 2 {
		t.Fatalf("refreshes=%d", refreshes)
	}
}
