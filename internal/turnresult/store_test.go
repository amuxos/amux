package turnresult

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T, policy Policy) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "turn_results.db")
	store, err := OpenStore(path, policy)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestTurnResultLifecycleFeishuWithAsyncArtifacts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := openTestStore(t, DefaultPolicy())

	ref := Ref{TargetID: "local", SessionID: "sess-1", SessionName: "chat", LaunchID: "launch-1", TurnID: "turn-1"}
	src := Source{Kind: SourceFeishu, BridgeID: "bot-1", ChatID: "oc_1", RootMessageID: "om_root", MessageIDs: []string{"om_msg_1", "om_msg_2"}}

	if _, err := store.RegisterSubmission(SubmissionInput{Ref: ref, Source: src, DeliveryExpected: true, Now: now}); err != nil {
		t.Fatalf("RegisterSubmission: %v", err)
	}

	got, err := store.LookupByFeishuMessage("bot-1", "om_msg_2", now)
	if err != nil || got.Status != StatusNotReady {
		t.Fatalf("after submission lookup = %+v, err=%v; want not_ready", got, err)
	}

	// Execution finishes, but Feishu delivery hasn't reported yet -> still not_ready.
	if _, err := store.RecordExecutionOutput(ExecutionInput{
		Ref:       ref,
		Source:    src,
		Execution: Execution{Status: ExecutionCompleted, StartedAt: now.Unix(), FinishedAt: now.Unix() + 10},
		Output:    Output{Content: "Final markdown answer"},
		Now:       now.Add(10 * time.Second),
	}); err != nil {
		t.Fatalf("RecordExecutionOutput: %v", err)
	}
	got, err = store.LookupBySessionTurn("local", "sess-1", "chat", "turn-1", now.Add(11*time.Second))
	if err != nil || got.Status != StatusNotReady {
		t.Fatalf("before delivery lookup = %+v, err=%v; want not_ready", got, err)
	}

	// Primary card delivered, async artifact still pending -> still not_ready.
	if _, err := store.RecordDelivery(DeliveryInput{
		Ref:    ref,
		Source: src,
		Delivery: Delivery{
			Channel:       "feishu",
			Status:        DeliveryDelivered,
			BridgeID:      "bot-1",
			ChatID:        "oc_1",
			RootMessageID: "om_root",
			Messages:      []DeliveryMessage{{Kind: MessageKindPrimary, MessageID: "om_reply_1"}},
		},
		ArtifactsPending: true,
		Now:              now.Add(12 * time.Second),
	}); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	got, err = store.LookupBySessionTurn("local", "sess-1", "chat", "turn-1", now.Add(13*time.Second))
	if err != nil || got.Status != StatusNotReady {
		t.Fatalf("while artifacts pending lookup = %+v, err=%v; want not_ready", got, err)
	}

	// Artifact completes -> ready with both primary and attachment message IDs.
	if _, err := store.RecordArtifactDelivery(ArtifactDeliveryInput{
		BridgeID:     "bot-1",
		TurnID:       "turn-1",
		Attachments:  []DeliveryMessage{{Kind: MessageKindAttachment, MessageID: "om_att_1", Name: "report.md"}},
		TotalPlanned: 1,
		Now:          now.Add(15 * time.Second),
	}); err != nil {
		t.Fatalf("RecordArtifactDelivery: %v", err)
	}

	got, err = store.LookupByFeishuMessage("bot-1", "om_msg_1", now.Add(16*time.Second))
	if err != nil || got.Status != StatusReady || got.Result == nil {
		t.Fatalf("final lookup = %+v, err=%v; want ready", got, err)
	}
	if got.Result.Execution.Status != ExecutionCompleted || got.Result.Output.Status != OutputAvailable || got.Result.Output.Content != "Final markdown answer" {
		t.Fatalf("unexpected execution/output: %+v", got.Result)
	}
	if len(got.Result.Deliveries) != 1 || got.Result.Deliveries[0].Status != DeliveryDelivered || len(got.Result.Deliveries[0].Messages) != 2 {
		t.Fatalf("unexpected deliveries: %+v", got.Result.Deliveries)
	}
}

func TestTurnResultScheduledPromptMergesFeishuDeliveryAndSupportsLatest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := openTestStore(t, DefaultPolicy())

	// 1. Schedule triggers session-prompt into a Feishu-bound session.
	ref := Ref{TargetID: "local", SessionID: "sess-sched", SessionName: "ops", TurnID: "turn-sched-1"}
	schedSrc := Source{
		Kind:         SourceSchedule,
		ScheduleID:   8,
		ExecutionID:  42,
		ScheduleType: "session-prompt",
		RunMode:      "reuse-session",
		ScheduledAt:  now.Unix(),
	}
	if _, err := store.RegisterSubmission(SubmissionInput{Ref: ref, Source: schedSrc, DeliveryExpected: true, OwnerPrincipalID: "p-1", Now: now}); err != nil {
		t.Fatalf("RegisterSubmission: %v", err)
	}
	// 2. Bridge observes execution & delivery for the same (session_id, turn_id).
	if _, err := store.RecordExecutionOutput(ExecutionInput{
		Ref:       ref,
		Source:    Source{Kind: SourceFeishu, BridgeID: "bot-1", ChatID: "oc_ops", RootMessageID: "om_root"},
		Execution: Execution{Status: ExecutionCompleted, StartedAt: now.Unix(), FinishedAt: now.Unix() + 5},
		Output:    Output{Content: "Daily report OK"},
		Now:       now.Add(5 * time.Second),
	}); err != nil {
		t.Fatalf("RecordExecutionOutput: %v", err)
	}
	if _, err := store.RecordDelivery(DeliveryInput{
		Ref:    ref,
		Source: Source{BridgeID: "bot-1", ChatID: "oc_ops", RootMessageID: "om_root"},
		Delivery: Delivery{
			Channel:       "feishu",
			Status:        DeliveryDelivered,
			BridgeID:      "bot-1",
			ChatID:        "oc_ops",
			RootMessageID: "om_root",
			Messages:      []DeliveryMessage{{Kind: MessageKindPrimary, MessageID: "om_sched_reply"}},
		},
		Now: now.Add(6 * time.Second),
	}); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}

	got, err := store.LookupByScheduleExecution(8, 0, now.Add(10*time.Second))
	if err != nil || got.Status != StatusReady || got.Result == nil {
		t.Fatalf("LookupByScheduleExecution(latest) = %+v, err=%v", got, err)
	}
	if got.Result.Source.Kind != SourceSchedule || got.Result.Source.ScheduleID != 8 || got.Result.Source.ExecutionID != 42 {
		t.Fatalf("schedule source lost during merge: %+v", got.Result.Source)
	}
	if len(got.Result.Deliveries) != 1 || got.Result.Deliveries[0].Messages[0].MessageID != "om_sched_reply" {
		t.Fatalf("expected merged feishu delivery, got %+v", got.Result.Deliveries)
	}

	// Lookup by session + latest also finds the same turn.
	byLatest, err := store.LookupBySessionTurn("local", "sess-sched", "ops", "latest", now.Add(10*time.Second))
	if err != nil || byLatest.Status != StatusReady || byLatest.Result.Ref.TurnID != "turn-sched-1" {
		t.Fatalf("LookupBySessionTurn(latest) = %+v, err=%v", byLatest, err)
	}
}

func TestTurnResultSizeLimitFailureOutputPendingTimeoutTTLEvictionAndClear(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	policy := Policy{
		TTL:            10 * time.Minute,
		PendingTimeout: 2 * time.Minute,
		MaxEntries:     3,
		MaxTotalBytes:  32 << 20,
		MaxContentSize: 32,
	}
	store := openTestStore(t, policy)

	// 1. Oversized content is marked result_too_large and not truncated.
	if _, err := store.RecordExecutionOutput(ExecutionInput{
		Ref:       Ref{TargetID: "local", SessionID: "s1", TurnID: "t-large"},
		Source:    Source{Kind: SourceCLI},
		Execution: Execution{Status: ExecutionCompleted, FinishedAt: now.Unix()},
		Output:    Output{Content: strings.Repeat("a", 64)},
		Now:       now,
	}); err != nil {
		t.Fatalf("RecordExecutionOutput large: %v", err)
	}
	got, _ := store.LookupBySessionTurn("local", "s1", "", "t-large", now)
	if got.Status != StatusReady || got.Result.Output.Status != OutputUnavailable || got.Result.Output.Reason != ReasonResultTooLarge || got.Result.Output.Content != "" {
		t.Fatalf("oversized output = %+v", got.Result)
	}

	// 2. Failed execution never leaks partial content.
	if _, err := store.RecordExecutionOutput(ExecutionInput{
		Ref:       Ref{TargetID: "local", SessionID: "s1", TurnID: "t-fail"},
		Source:    Source{Kind: SourceCLI},
		Execution: Execution{Status: ExecutionFailed, Code: "provider_error", FinishedAt: now.Unix() + 1},
		Output:    Output{Content: "half answer"},
		Now:       now.Add(time.Second),
	}); err != nil {
		t.Fatalf("RecordExecutionOutput fail: %v", err)
	}
	got, _ = store.LookupBySessionTurn("local", "s1", "", "t-fail", now.Add(time.Second))
	if got.Status != StatusReady || got.Result.Output.Status != OutputUnavailable || got.Result.Output.Content != "" {
		t.Fatalf("failed output = %+v", got.Result)
	}

	// 3. Pending delivery times out after PendingTimeout -> unavailable/result_not_settled.
	if _, err := store.RegisterSubmission(SubmissionInput{
		Ref: Ref{TargetID: "local", SessionID: "s1", TurnID: "t-stuck"}, Source: Source{Kind: SourceFeishu}, DeliveryExpected: true, Now: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("RegisterSubmission stuck: %v", err)
	}
	if _, err := store.RecordExecutionOutput(ExecutionInput{
		Ref:       Ref{TargetID: "local", SessionID: "s1", TurnID: "t-stuck"},
		Execution: Execution{Status: ExecutionCompleted, FinishedAt: now.Unix() + 2},
		Output:    Output{Content: "ok"},
		Now:       now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("RecordExecutionOutput stuck: %v", err)
	}
	got, _ = store.LookupBySessionTurn("local", "s1", "", "t-stuck", now.Add(3*time.Minute))
	if got.Status != StatusUnavailable || got.Reason != ReasonResultNotSettled {
		t.Fatalf("timed-out pending = %+v; want unavailable/result_not_settled", got)
	}

	// 4. MaxEntries=3 evicts oldest (t-large) when adding 4th record.
	if _, err := store.RecordExecutionOutput(ExecutionInput{
		Ref:       Ref{TargetID: "local", SessionID: "s1", TurnID: "t-fourth"},
		Source:    Source{Kind: SourceCLI},
		Execution: Execution{Status: ExecutionCompleted, FinishedAt: now.Unix() + 3},
		Output:    Output{Content: "fourth"},
		Now:       now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("RecordExecutionOutput fourth: %v", err)
	}
	evicted, _ := store.LookupBySessionTurn("local", "s1", "", "t-large", now.Add(3*time.Minute))
	if evicted.Status != StatusNotFound {
		t.Fatalf("oldest entry should be evicted, got %+v", evicted)
	}

	// 5. TTL expiry clears settled records without reads extending TTL.
	expired, _ := store.LookupBySessionTurn("local", "s1", "", "t-fail", now.Add(11*time.Minute))
	if expired.Status != StatusNotFound {
		t.Fatalf("expired entry should be not_found, got %+v", expired)
	}

	// 6. ClearAll empties remaining records.
	if err := store.ClearAll(); err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	afterClear, _ := store.LookupBySessionTurn("local", "s1", "", "t-fourth", now.Add(3*time.Minute))
	if afterClear.Status != StatusNotFound {
		t.Fatalf("after ClearAll got %+v, want not_found", afterClear)
	}
}
