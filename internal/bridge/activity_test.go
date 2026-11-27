package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentui"
)

func TestToolActivityStateOnlyOwnsSubtaskAugmentation(t *testing.T) {
	t.Parallel()
	state := &toolActivityState{}
	state.SetSubtasks([]subtaskProgress{{ID: "task-1", Status: "running", Title: "Review"}})
	got := state.Subtasks()
	if len(got) != 1 || got[0].ID != "task-1" {
		t.Fatalf("subtasks = %+v, want task-1", got)
	}
	got[0].Title = "mutated"
	if state.Subtasks()[0].Title != "Review" {
		t.Fatal("Subtasks returned mutable state")
	}
	state.Reset()
	if len(state.Subtasks()) != 0 {
		t.Fatal("Reset did not clear subtasks")
	}
}

func TestActivityViewFingerprintSurvivesInvalidRawJSON(t *testing.T) {
	t.Parallel()
	view := &agentui.ActivityView{
		TurnID:    "turn-1",
		TurnSeq:   1,
		Status:    "running",
		UsageJSON: json.RawMessage(`{`),
	}
	first := activityViewFingerprint(view)
	if first == "" {
		t.Fatal("fingerprint is empty for invalid raw JSON")
	}
	if second := activityViewFingerprint(view); second != first {
		t.Fatalf("fingerprint is unstable: first=%q second=%q", first, second)
	}
	view.Status = "done"
	if changed := activityViewFingerprint(view); changed == first {
		t.Fatalf("fingerprint did not change with activity state: %q", changed)
	}
}

func TestActivityViewFingerprintIgnoresToolRawPayload(t *testing.T) {
	t.Parallel()
	view := &agentui.ActivityView{
		Status: "running",
		Tools: []agentui.ToolEvent{{
			ID: "tool-1", Title: "Read file", Status: "running",
			RawOutputJSON: json.RawMessage(`{"chunk":"one"}`),
		}},
	}
	first := activityViewFingerprint(view)
	view.Tools[0].RawOutputJSON = json.RawMessage(`{"chunk":"two"}`)
	view.Tools[0].ContentJSON = json.RawMessage(`[{"text":"details"}]`)
	if got := activityViewFingerprint(view); got != first {
		t.Fatalf("raw-only tool update changed fingerprint: first=%q got=%q", first, got)
	}
	view.Tools[0].OutputPreview = "visible result"
	if got := activityViewFingerprint(view); got == first {
		t.Fatal("visible tool update did not change fingerprint")
	}
}

func TestToolActivitySenderCreatesThenPatchesSameMessage(t *testing.T) {
	t.Parallel()
	var sends, patches []string
	sender := newToolActivitySender(0,
		func(text string) (string, error) { sends = append(sends, text); return "msg-1", nil },
		func(msgID, text string) error { patches = append(patches, msgID+":"+text); return nil },
	)
	sender.MaybeSend("one")
	sender.MaybeSend("two")
	sender.MaybeSend("two")
	sender.Flush("three")
	if strings.Join(sends, "|") != "one" || strings.Join(patches, "|") != "msg-1:two|msg-1:three" {
		t.Fatalf("sends=%v patches=%v", sends, patches)
	}
}

func TestToolActivitySenderResetStartsNewMessage(t *testing.T) {
	t.Parallel()
	var sends []string
	sender := newToolActivitySender(0,
		func(text string) (string, error) {
			sends = append(sends, text)
			return fmt.Sprintf("msg-%d", len(sends)), nil
		},
		func(string, string) error { return nil },
	)
	sender.MaybeSend("one")
	sender.Reset()
	sender.MaybeSend("two")
	if strings.Join(sends, "|") != "one|two" {
		t.Fatalf("sends = %v", sends)
	}
}

func TestToolActivitySenderDoesNotCreateNewMessageWhenPatchFails(t *testing.T) {
	t.Parallel()
	var sends, patches []string
	sender := newToolActivitySender(0,
		func(text string) (string, error) { sends = append(sends, text); return "msg-1", nil },
		func(msgID, text string) error {
			patches = append(patches, msgID+":"+text)
			return errors.New("patch failed")
		},
	)
	sender.MaybeSend("one")
	sender.MaybeSend("two")
	if strings.Join(sends, "|") != "one" || strings.Join(patches, "|") != "msg-1:two" {
		t.Fatalf("sends=%v patches=%v", sends, patches)
	}
}

// TestToolActivitySenderKeepsFailedSendResendable asserts a failed activity
// create does not mark the fingerprint delivered: a later Flush of the same
// activity must retry the send instead of deduping against a card Feishu never
// saw. This is the path that froze the collapsed title without the final
// usage/speed summary when CardKit intermittently failed.
func TestToolActivitySenderKeepsFailedSendResendable(t *testing.T) {
	t.Parallel()
	var sends []string
	failFirst := true
	sender := newToolActivitySender(0,
		func(text string) (string, error) {
			sends = append(sends, text)
			if failFirst {
				failFirst = false
				return "", errors.New("cardkit create failed")
			}
			return "msg-1", nil
		},
		func(msgID, text string) error { return nil },
	)
	sender.MaybeSend("final-usage")
	if len(sends) != 1 {
		t.Fatalf("sends = %v, want one attempt", sends)
	}
	sender.Flush("final-usage")
	if len(sends) != 2 || sends[1] != "final-usage" {
		t.Fatalf("Flush after failed send did not retry: %v", sends)
	}
	// Once delivered, the same text is deduped again.
	sender.MaybeSend("final-usage")
	if len(sends) != 2 {
		t.Fatalf("duplicate send after success: %v", sends)
	}
}

func TestToolActivitySenderFlushForcesPatchForSameText(t *testing.T) {
	t.Parallel()
	var patches []string
	sender := newToolActivitySender(0,
		func(string) (string, error) { return "msg-1", nil },
		func(msgID, text string) error { patches = append(patches, msgID+":"+text); return nil },
	)
	sender.MaybeSend("same")
	sender.Flush("same")
	if strings.Join(patches, "|") != "msg-1:same" {
		t.Fatalf("patches = %v", patches)
	}
}

func TestToolActivitySenderSendsTrailingThrottledUpdate(t *testing.T) {
	t.Parallel()
	patched := make(chan string, 1)
	sender := newToolActivitySender(40*time.Millisecond,
		func(string) (string, error) { return "msg-1", nil },
		func(_ string, text string) error { patched <- text; return nil },
	)
	defer sender.Close()

	sender.MaybeSend("one")
	sender.MaybeSend("two")
	select {
	case got := <-patched:
		if got != "two" {
			t.Fatalf("trailing patch = %q, want two", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for trailing activity update")
	}
}

func TestMainTurnActivitySenderDoesNotBlockOnCardNetwork(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	reply := newStreamingReplySender(0, &orderedReplyBackend{
		create: func(string) (*replyCardHandle, error) {
			return &replyCardHandle{mode: replyCardModeCardKit}, nil
		},
		createActivity: func(turnCardActivity) (*replyCardHandle, error) {
			close(started)
			<-release
			return &replyCardHandle{mode: replyCardModeCardKit}, nil
		},
		update:         func(*replyCardHandle, string) error { return nil },
		updateActivity: func(*replyCardHandle, turnCardActivity) error { return nil },
		finish:         func(*replyCardHandle, string) error { return nil },
	}, nil)
	defer reply.Close()
	sender := newMainTurnActivitySender(0, reply,
		func() turnCardActivity { return renderTurnActivity(toolActivityCardMeta{Status: "running"}) },
		nil, nil, nil,
	)
	defer sender.Close()

	returned := make(chan struct{})
	go func() {
		sender.ForceSend("running")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("ForceSend blocked on CardKit network")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("CardKit activity update did not start")
	}
	close(release)
	sender.Flush("running")
}

func TestMainTurnActivitySenderFallsBackBeforeCardKitMessageIsVisible(t *testing.T) {
	t.Parallel()
	backend := &bindingReplyCardBackend{
		cardKit: &fakeCardKitReplyAPI{createErr: errors.New("cardkit unavailable")},
		sendCard: func(context.Context, string, string) (string, error) {
			t.Fatal("sendCard called after CardKit create failure")
			return "", nil
		},
	}
	reply := newStreamingReplySender(0, backend, nil)
	defer reply.Close()
	var sends, patches []string
	var fallbackErr error
	sender := newMainTurnActivitySender(0, reply,
		func() turnCardActivity { return renderTurnActivity(toolActivityCardMeta{Status: "running"}) },
		func(text string) (string, error) {
			sends = append(sends, text)
			return "legacy-activity", nil
		},
		func(messageID, text string) error {
			patches = append(patches, messageID+":"+text)
			return nil
		},
		func(err error) { fallbackErr = err },
	)

	sender.MaybeSend("one")
	sender.ForceSend("two")
	sender.Reset()

	if strings.Join(sends, "|") != "two" || len(patches) != 0 {
		t.Fatalf("sends=%v patches=%v", sends, patches)
	}
	if fallbackErr == nil || !strings.Contains(fallbackErr.Error(), "cardkit unavailable") {
		t.Fatalf("fallback error = %v, want original CardKit failure", fallbackErr)
	}
}

func TestToolActivityTurnUsesExplicitAmuxSequence(t *testing.T) {
	t.Parallel()
	var turn toolActivityTurn
	meta := turn.Start(7, "分析当前仓库")
	if meta.TurnSeq != 7 || meta.Question != "分析当前仓库" || meta.Status != "running" {
		t.Fatalf("first meta = %+v", meta)
	}
	meta = turn.Start(7, "选方案 A")
	if meta.TurnSeq != 7 || meta.Followup != "选方案 A" {
		t.Fatalf("followup meta = %+v", meta)
	}
	turn.Complete(false)
	meta = turn.Start(8, "下一轮")
	if meta.TurnSeq != 8 || meta.Question != "下一轮" || meta.Followup != "" {
		t.Fatalf("next meta = %+v", meta)
	}
}

func TestToolActivityTurnCompletePreservesWaitingStatus(t *testing.T) {
	t.Parallel()
	var turn toolActivityTurn
	turn.Start(1, "分析当前仓库")
	turn.MarkWaiting()

	meta := turn.Complete(false)
	if meta.Status != "waiting" {
		t.Fatalf("completed waiting turn status = %q, want waiting", meta.Status)
	}

	meta = turn.Complete(true)
	if meta.Status != "failed" {
		t.Fatalf("failed waiting turn status = %q, want failed", meta.Status)
	}
}

func TestToolActivityTurnRunningOnlyWhileProviderTurnIsActive(t *testing.T) {
	t.Parallel()
	var turn toolActivityTurn
	if turn.Running() {
		t.Fatal("zero-value turn is running")
	}
	turn.Start(1, "分析当前仓库")
	if !turn.Running() {
		t.Fatal("started turn is not running")
	}
	turn.MarkWaiting()
	if turn.Running() {
		t.Fatal("waiting turn is still running")
	}
	turn.Complete(false)
	if turn.Running() {
		t.Fatal("completed turn is still running")
	}
}

func TestIsFollowupPrompt(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"你希望保留 post 吗？", "Please choose one?", "请确认是否继续"} {
		if !isFollowupPrompt(text) {
			t.Fatalf("isFollowupPrompt(%q) = false", text)
		}
	}
	if isFollowupPrompt("已完成分析，下面是结论。") {
		t.Fatal("plain conclusion should not be a followup")
	}
}

func TestForceSendBypassesThrottleAndUnchangedText(t *testing.T) {
	t.Parallel()
	var patched []string
	s := newToolActivitySender(time.Hour,
		func(string) (string, error) { return "msg1", nil },
		func(_, text string) error { patched = append(patched, text); return nil },
	)
	s.MaybeSend("v1")
	s.MaybeSend("v1")
	s.ForceSend("v1")
	if len(patched) != 1 || patched[0] != "v1" {
		t.Fatalf("patched = %v", patched)
	}
}

func TestTurnStatusRefreshPolicyUsesIndependentSilenceCadence(t *testing.T) {
	t.Parallel()
	var policy turnStatusRefreshPolicy
	view := &agentui.ActivityView{Status: "running", LastActivityAt: time.Unix(100, 0)}
	if policy.Due(time.Unix(109, 0), view) {
		t.Fatal("status refreshed before ten seconds of silence")
	}
	if !policy.Due(time.Unix(110, 0), view) {
		t.Fatal("first silent status refresh was not due at ten seconds")
	}
	if policy.Due(time.Unix(124, 0), view) {
		t.Fatal("status refreshed before fifteen-second cadence")
	}
	if !policy.Due(time.Unix(125, 0), view) {
		t.Fatal("continued silent status refresh was not due")
	}

	view.LastActivityAt = time.Unix(130, 0)
	if policy.Due(time.Unix(139, 0), view) {
		t.Fatal("new provider activity did not reset silence window")
	}
	if !policy.Due(time.Unix(140, 0), view) {
		t.Fatal("status did not refresh ten seconds after new activity")
	}
	view.Status = "done"
	if policy.Due(time.Unix(160, 0), view) {
		t.Fatal("terminal activity kept running heartbeat refreshes")
	}
}

func TestToolActivitySenderConcurrentForceAndMaybeSend(t *testing.T) {
	t.Parallel()
	s := newToolActivitySender(0,
		func(string) (string, error) { return "msg1", nil },
		func(string, string) error { return nil },
	)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.MaybeSend(fmt.Sprintf("maybe-%d-%d", n, j))
			}
		}(i)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.ForceSend(fmt.Sprintf("force-%d-%d", n, j))
				if j%10 == 0 {
					s.Reset()
				}
			}
		}(i)
	}
	wg.Wait()
}
