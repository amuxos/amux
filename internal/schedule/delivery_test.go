package schedule

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeDeliveryTargetAndPolicy(t *testing.T) {
	targetCases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", DeliveryTargetNone, false},
		{"  ", DeliveryTargetNone, false},
		{"none", DeliveryTargetNone, false},
		{"ORIGIN", DeliveryTargetOrigin, false},
		{" creator_dm ", DeliveryTargetCreatorDM, false},
		{"chat", "", true},
	}
	for _, tc := range targetCases {
		got, err := NormalizeDeliveryTarget(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("NormalizeDeliveryTarget(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if got != tc.want {
			t.Fatalf("NormalizeDeliveryTarget(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	policyCases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", DeliveryPolicyAlways, false},
		{"always", DeliveryPolicyAlways, false},
		{"ON_FAILURE", DeliveryPolicyOnFailure, false},
		{" on_non_empty ", DeliveryPolicyOnNonEmpty, false},
		{"never", "", true},
	}
	for _, tc := range policyCases {
		got, err := NormalizeDeliveryPolicy(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("NormalizeDeliveryPolicy(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if got != tc.want {
			t.Fatalf("NormalizeDeliveryPolicy(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsSilentResultAndShouldDeliverResult(t *testing.T) {
	for _, silent := range []string{"", "   ", "NO_ALERT", "no_alert", "SILENT", "HEARTBEAT_OK", "NONE", "none\n"} {
		if !IsSilentResult(silent) {
			t.Fatalf("IsSilentResult(%q) = false, want true", silent)
		}
	}
	for _, loud := range []string{"All checks passed", "CPU 95%", "NO_ALERT: wait actually alert"} {
		if IsSilentResult(loud) {
			t.Fatalf("IsSilentResult(%q) = true, want false", loud)
		}
	}

	cases := []struct {
		name   string
		target string
		policy string
		run    Execution
		want   bool
	}{
		{"none target never delivers", DeliveryTargetNone, DeliveryPolicyAlways, Execution{Status: "completed", Terminal: true, Result: "hi"}, false},
		{"non-terminal never delivers", DeliveryTargetOrigin, DeliveryPolicyAlways, Execution{Status: "running", Terminal: false, Result: "hi"}, false},
		{"always delivers empty completed", DeliveryTargetOrigin, DeliveryPolicyAlways, Execution{Status: "completed", Terminal: true}, true},
		{"on_failure skips completed", DeliveryTargetOrigin, DeliveryPolicyOnFailure, Execution{Status: "completed", Terminal: true, Result: "ok"}, false},
		{"on_failure delivers failed", DeliveryTargetOrigin, DeliveryPolicyOnFailure, Execution{Status: "failed", Terminal: true, Reason: "boom"}, true},
		{"on_failure delivers timed_out", DeliveryTargetOrigin, DeliveryPolicyOnFailure, Execution{Status: "timed_out", Terminal: true}, true},
		{"on_non_empty skips empty completed", DeliveryTargetOrigin, DeliveryPolicyOnNonEmpty, Execution{Status: "completed", Terminal: true, Result: "   "}, false},
		{"on_non_empty skips NO_ALERT completed", DeliveryTargetOrigin, DeliveryPolicyOnNonEmpty, Execution{Status: "completed", Terminal: true, Result: "NO_ALERT"}, false},
		{"on_non_empty delivers real output", DeliveryTargetOrigin, DeliveryPolicyOnNonEmpty, Execution{Status: "completed", Terminal: true, Result: "3 errors found"}, true},
		{"on_non_empty delivers empty failure", DeliveryTargetCreatorDM, DeliveryPolicyOnNonEmpty, Execution{Status: "failed", Terminal: true}, true},
	}
	for _, tc := range cases {
		if got := ShouldDeliverResult(tc.target, tc.policy, tc.run); got != tc.want {
			t.Fatalf("%s: ShouldDeliverResult = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStoreObserveEnqueuesResultDeliveryAndTracksStatus(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Unix(1700000000, 0)
	nextRun := now.Unix()
	task, err := store.Create(Task{
		Name:           "hourly-check",
		Spec:           "every 1h",
		ScheduleType:   ScheduleTypeSubtask,
		DeliveryTarget: DeliveryTargetOrigin,
		DeliveryPolicy: DeliveryPolicyOnNonEmpty,
		Enabled:        true,
		NextRunAt:      &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindCreator(task.ID, Creator{
		BotID:           "bot-1",
		OpenID:          "ou_alice",
		PrincipalID:     "p_alice",
		OriginRootMsgID: "om_root_1",
		OriginChatID:    "oc_chat_1",
		Source:          "turn:ou_alice",
	}); err != nil {
		t.Fatal(err)
	}

	// Run 1: silent completed output under on_non_empty -> skipped, no queue row.
	next1 := now.Add(time.Hour).Unix()
	run1, claimed, err := store.ClaimTracked(task, now, &next1)
	if err != nil || !claimed {
		t.Fatalf("ClaimTracked: claimed=%v err=%v", claimed, err)
	}
	run1.Status = "completed"
	run1.Terminal = true
	run1.FinishedAt = now.Add(5 * time.Second).Unix()
	run1.Result = "NO_ALERT"
	if err := store.Observe(run1, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingResultDeliveries("bot-1", now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending deliveries for silent output, got %d", len(pending))
	}
	hist, err := store.ExecutionHistory(task.ID)
	if err != nil || len(hist) != 1 {
		t.Fatalf("ExecutionHistory: %v len=%d", err, len(hist))
	}
	if hist[0].DeliveryStatus != DeliveryStatusSkipped {
		t.Fatalf("run1 DeliveryStatus = %q, want skipped", hist[0].DeliveryStatus)
	}

	// Run 2: manual trigger with non-empty completed output -> pending + enqueued atomically.
	run2, err := store.JournalManualTrigger(task, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Non-terminal observe should not enqueue.
	run2.Status = "running"
	if err := store.Observe(run2, now.Add(time.Hour+time.Second)); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingResultDeliveries("bot-1", now.Add(time.Hour+time.Second))
	if err != nil || len(pending) != 0 {
		t.Fatalf("non-terminal observe should not enqueue: err=%v len=%d", err, len(pending))
	}

	run2.Status = "completed"
	run2.Terminal = true
	run2.FinishedAt = now.Add(time.Hour + 10*time.Second).Unix()
	run2.Result = "Disk usage at 91%"
	run2.TriggerSource = "" // Verify Observe preserves existing TriggerSource.
	if err := store.Observe(run2, now.Add(time.Hour+10*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Duplicate terminal observe must be a no-op.
	if err := store.Observe(run2, now.Add(time.Hour+11*time.Second)); err != nil {
		t.Fatal(err)
	}

	pending, err = store.PendingResultDeliveries("bot-1", now.Add(time.Hour+10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending delivery, got %d", len(pending))
	}
	d := pending[0]
	if d.RunID != run2.ID || d.OriginRootMsgID != "om_root_1" || d.OriginChatID != "oc_chat_1" || d.OpenID != "ou_alice" {
		t.Fatalf("unexpected delivery row: %+v", d)
	}
	if d.Run.TriggerSource != TriggerSourceManual {
		t.Fatalf("preserved TriggerSource = %q, want manual", d.Run.TriggerSource)
	}

	// Transient failure schedules exponential backoff and keeps row pending.
	retryTime := now.Add(time.Hour + 15*time.Second)
	if err := store.MarkResultDelivery(run2.ID, DeliveryStatusFailed, false, "http 503", retryTime); err != nil {
		t.Fatal(err)
	}
	pendingNow, err := store.PendingResultDeliveries("bot-1", retryTime.Add(5*time.Second))
	if err != nil || len(pendingNow) != 0 {
		t.Fatalf("should be backed off 5s after transient failure, got %d", len(pendingNow))
	}
	pendingLater, err := store.PendingResultDeliveries("bot-1", retryTime.Add(30*time.Second))
	if err != nil || len(pendingLater) != 1 {
		t.Fatalf("should be ready after backoff, got %d", len(pendingLater))
	}

	// Marking delivered clears pending queue and mirrors status onto schedule_executions.
	if err := store.MarkResultDelivery(run2.ID, DeliveryStatusDelivered, true, "", retryTime.Add(35*time.Second)); err != nil {
		t.Fatal(err)
	}
	pendingDone, err := store.PendingResultDeliveries("bot-1", retryTime.Add(time.Hour))
	if err != nil || len(pendingDone) != 0 {
		t.Fatalf("delivered row should leave pending queue, got %d", len(pendingDone))
	}
	hist, err = store.ExecutionHistory(task.ID)
	if err != nil || len(hist) != 2 {
		t.Fatalf("ExecutionHistory: %v len=%d", err, len(hist))
	}
	if hist[0].ID != run2.ID || hist[0].DeliveryStatus != DeliveryStatusDelivered {
		t.Fatalf("run2 DeliveryStatus = %q, want delivered", hist[0].DeliveryStatus)
	}
}

func TestOneShotCompletedSuppressesAdminDigestWhenProactiveDeliveryEnabled(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Unix(1700000000, 0)
	nextRun := now.Unix()

	// 1) One-shot with delivery_target=origin + completed -> NO admin PendingAt digest.
	withDelivery, err := store.Create(Task{
		Name:           "once-with-delivery",
		Spec:           "at 2026-11-25T15:00:00Z",
		Kind:           KindOnce,
		ScheduleType:   ScheduleTypeSubtask,
		DeliveryTarget: DeliveryTargetOrigin,
		DeliveryPolicy: DeliveryPolicyOnNonEmpty,
		Enabled:        true,
		NextRunAt:      &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindCreator(withDelivery.ID, Creator{BotID: "bot-1", OpenID: "ou_alice", PrincipalID: "p_alice", Source: "turn:1"}); err != nil {
		t.Fatal(err)
	}
	run1, ok, err := store.ClaimTracked(withDelivery, now, nil)
	if err != nil || !ok {
		t.Fatalf("ClaimTracked: %v %v", ok, err)
	}
	run1.Status = "completed"
	run1.Terminal = true
	run1.FinishedAt = now.Add(5 * time.Second).Unix()
	run1.Result = "NO_ALERT"
	if err := store.Observe(run1, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	v1, err := store.Visibility(withDelivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v1.PendingAt != 0 {
		t.Fatalf("one-shot completed with proactive delivery should not schedule admin digest, got PendingAt=%d", v1.PendingAt)
	}

	// 2) One-shot with delivery_target=origin + timed_out -> STILL alerts admin.
	failedOnce, err := store.Create(Task{
		Name:           "once-timed-out",
		Spec:           "at 2026-11-25T15:01:00Z",
		Kind:           KindOnce,
		ScheduleType:   ScheduleTypeSubtask,
		DeliveryTarget: DeliveryTargetOrigin,
		DeliveryPolicy: DeliveryPolicyOnFailure,
		Enabled:        true,
		NextRunAt:      &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindCreator(failedOnce.ID, Creator{BotID: "bot-1", OpenID: "ou_alice", PrincipalID: "p_alice", Source: "turn:2"}); err != nil {
		t.Fatal(err)
	}
	run2, ok, err := store.ClaimTracked(failedOnce, now, nil)
	if err != nil || !ok {
		t.Fatalf("ClaimTracked: %v %v", ok, err)
	}
	run2.Status = "timed_out"
	run2.Terminal = true
	run2.FinishedAt = now.Add(5 * time.Second).Unix()
	if err := store.Observe(run2, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	v2, err := store.Visibility(failedOnce.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v2.PendingAt == 0 {
		t.Fatalf("one-shot timed_out must still schedule admin alert")
	}

	// 3) One-shot with delivery_target=none + completed -> keeps legacy admin digest.
	legacyOnce, err := store.Create(Task{
		Name:           "once-legacy",
		Spec:           "at 2026-11-25T15:02:00Z",
		Kind:           KindOnce,
		ScheduleType:   ScheduleTypeSubtask,
		DeliveryTarget: DeliveryTargetNone,
		DeliveryPolicy: DeliveryPolicyAlways,
		Enabled:        true,
		NextRunAt:      &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindCreator(legacyOnce.ID, Creator{BotID: "bot-1", OpenID: "ou_alice", PrincipalID: "p_alice", Source: "turn:3"}); err != nil {
		t.Fatal(err)
	}
	run3, ok, err := store.ClaimTracked(legacyOnce, now, nil)
	if err != nil || !ok {
		t.Fatalf("ClaimTracked: %v %v", ok, err)
	}
	run3.Status = "completed"
	run3.Terminal = true
	run3.FinishedAt = now.Add(5 * time.Second).Unix()
	if err := store.Observe(run3, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	v3, err := store.Visibility(legacyOnce.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v3.PendingAt == 0 {
		t.Fatalf("one-shot completed with delivery_target=none should still schedule legacy admin digest")
	}
}

func TestAutoPauseCircuitBreakerAfterFiveConsecutiveFailuresAndResetOnResume(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Unix(1700000000, 0)
	nextRun := now.Unix()
	tsk, err := store.Create(Task{
		Name:           "flaky-monitor",
		Spec:           "every 10m",
		Kind:           KindInterval,
		ScheduleType:   ScheduleTypeSubtask,
		DeliveryTarget: DeliveryTargetOrigin,
		DeliveryPolicy: DeliveryPolicyAlways,
		Enabled:        true,
		NextRunAt:      &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindCreator(tsk.ID, Creator{
		BotID:       "bot-1",
		OpenID:      "ou_alice",
		PrincipalID: "p_alice",
		Source:      "turn:1",
	}); err != nil {
		t.Fatal(err)
	}

	// 2 failures followed by 1 success must reset the consecutive failure counter.
	for i := 0; i < 2; i++ {
		at := now.Add(time.Duration(i+1) * time.Minute)
		next := at.Add(10 * time.Minute).Unix()
		run, ok, err := store.ClaimTracked(tsk, at, &next)
		if err != nil || !ok {
			t.Fatalf("claim %d: ok=%v err=%v", i, ok, err)
		}
		run.Status = "failed"
		run.Terminal = true
		run.FinishedAt = at.Unix() + 5
		if err := store.Observe(run, at.Add(5*time.Second)); err != nil {
			t.Fatal(err)
		}
		tsk, _ = store.Get(tsk.ID)
	}
	if v, _ := store.Visibility(tsk.ID); v.ConsecutiveFailures != 2 || v.AutoPausedAt != 0 {
		t.Fatalf("after 2 failures: %+v", v)
	}
	okAt := now.Add(3 * time.Minute)
	okNext := okAt.Add(10 * time.Minute).Unix()
	okRun, ok, err := store.ClaimTracked(tsk, okAt, &okNext)
	if err != nil || !ok {
		t.Fatalf("claim success: ok=%v err=%v", ok, err)
	}
	okRun.Status = "completed"
	okRun.Terminal = true
	okRun.FinishedAt = okAt.Unix() + 5
	if err := store.Observe(okRun, okAt.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if v, _ := store.Visibility(tsk.ID); v.ConsecutiveFailures != 0 || v.AutoPausedAt != 0 {
		t.Fatalf("success did not reset consecutive failures: %+v", v)
	}

	// Now 4 consecutive failures keep the schedule enabled; the 5th auto-pauses it.
	for i := 1; i <= DefaultAutoPauseFailureThreshold; i++ {
		tsk, _ = store.Get(tsk.ID)
		at := now.Add(time.Duration(10+i) * time.Minute)
		next := at.Add(10 * time.Minute).Unix()
		run, claimed, err := store.ClaimTracked(tsk, at, &next)
		if err != nil || !claimed {
			t.Fatalf("claim fail %d: claimed=%v err=%v", i, claimed, err)
		}
		run.Status = "timed_out"
		run.Terminal = true
		run.FinishedAt = at.Unix() + 5
		if err := store.Observe(run, at.Add(5*time.Second)); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(tsk.ID)
		if err != nil {
			t.Fatal(err)
		}
		v, err := store.Visibility(tsk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i < DefaultAutoPauseFailureThreshold {
			if !got.Enabled || v.AutoPausedAt != 0 || v.ConsecutiveFailures != i {
				t.Fatalf("at failure %d: enabled=%v vis=%+v", i, got.Enabled, v)
			}
		} else {
			if got.Enabled || v.AutoPausedAt == 0 || v.ConsecutiveFailures != DefaultAutoPauseFailureThreshold || v.AutoPausedReason == "" {
				t.Fatalf("at failure %d expected auto-pause: enabled=%v vis=%+v", i, got.Enabled, v)
			}
		}
	}

	// ResetAutoPause clears counter and auto-paused marker on resume and advances
	// the watermark so the first post-resume failure starts from 1 (not 6).
	if _, err := store.SetEnabled(tsk.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetAutoPause(tsk.ID); err != nil {
		t.Fatal(err)
	}
	v, err := store.Visibility(tsk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.ConsecutiveFailures != 0 || v.AutoPausedAt != 0 || v.AutoPausedReason != "" || v.AutoPauseResetScheduledAt == 0 {
		t.Fatalf("ResetAutoPause did not clear fields or advance watermark: %+v", v)
	}
	tsk, _ = store.Get(tsk.ID)
	afterAt := now.Add(30 * time.Minute)
	afterNext := afterAt.Add(10 * time.Minute).Unix()
	postResumeRun, claimed, err := store.ClaimTracked(tsk, afterAt, &afterNext)
	if err != nil || !claimed {
		t.Fatalf("claim post-resume: claimed=%v err=%v", claimed, err)
	}
	postResumeRun.Status = "failed"
	postResumeRun.Terminal = true
	postResumeRun.FinishedAt = afterAt.Unix() + 5
	if err := store.Observe(postResumeRun, afterAt.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(tsk.ID); !got.Enabled {
		t.Fatalf("first failure after resume must not immediately re-pause")
	}
	if v, _ := store.Visibility(tsk.ID); v.ConsecutiveFailures != 1 || v.AutoPausedAt != 0 {
		t.Fatalf("expected streak=1 after first post-resume failure, got %+v", v)
	}
}

func TestAutoPauseCircuitBreakerOutOfOrderTerminalObservations(t *testing.T) {
	t.Run("newer success observed before older failures does not falsely pause", func(t *testing.T) {
		store, err := OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		base := time.Unix(1700000000, 0)
		nextRun := base.Unix()
		tsk, err := store.Create(Task{
			Name:         "concurrent-job-a",
			Spec:         "every 1m",
			Kind:         KindInterval,
			ScheduleType: ScheduleTypeSubtask,
			Enabled:      true,
			NextRunAt:    &nextRun,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.BindCreator(tsk.ID, Creator{BotID: "bot-1", OpenID: "ou_alice", PrincipalID: "p_alice", Source: "turn:1"}); err != nil {
			t.Fatal(err)
		}

		// Claim 6 overlapping occurrences (ScheduledAt = base + 0..5m).
		runs := make([]Execution, 6)
		for i := 0; i < 6; i++ {
			tsk, _ = store.Get(tsk.ID)
			at := base.Add(time.Duration(i) * time.Minute)
			next := at.Add(time.Minute).Unix()
			r, ok, err := store.ClaimTracked(tsk, at, &next)
			if err != nil || !ok {
				t.Fatalf("claim %d: ok=%v err=%v", i, ok, err)
			}
			runs[i] = r
		}

		// Run 6 (newest ScheduledAt) completes FIRST.
		runs[5].Status = "completed"
		runs[5].Terminal = true
		runs[5].FinishedAt = base.Add(6 * time.Minute).Unix()
		if err := store.Observe(runs[5], base.Add(6*time.Minute)); err != nil {
			t.Fatal(err)
		}

		// Runs 1..5 (older ScheduledAt) fail AFTER run 6 completed.
		for i := 0; i < 5; i++ {
			runs[i].Status = "failed"
			runs[i].Terminal = true
			runs[i].FinishedAt = base.Add(time.Duration(7+i) * time.Minute).Unix()
			if err := store.Observe(runs[i], base.Add(time.Duration(7+i)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}

		got, err := store.Get(tsk.ID)
		if err != nil {
			t.Fatal(err)
		}
		v, err := store.Visibility(tsk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Enabled || v.ConsecutiveFailures != 0 || v.AutoPausedAt != 0 {
			t.Fatalf("older failures arriving after newer success falsely paused/counted: enabled=%v vis=%+v", got.Enabled, v)
		}
	})

	t.Run("older success observed after newer failures does not clear consecutive failure streak", func(t *testing.T) {
		store, err := OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()

		base := time.Unix(1700000000, 0)
		nextRun := base.Unix()
		tsk, err := store.Create(Task{
			Name:         "concurrent-job-b",
			Spec:         "every 1m",
			Kind:         KindInterval,
			ScheduleType: ScheduleTypeSubtask,
			Enabled:      true,
			NextRunAt:    &nextRun,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.BindCreator(tsk.ID, Creator{BotID: "bot-1", OpenID: "ou_alice", PrincipalID: "p_alice", Source: "turn:1"}); err != nil {
			t.Fatal(err)
		}

		// Claim 6 overlapping occurrences (run 0 will succeed late; runs 1..5 will fail).
		runs := make([]Execution, 6)
		for i := 0; i < 6; i++ {
			tsk, _ = store.Get(tsk.ID)
			at := base.Add(time.Duration(i) * time.Minute)
			next := at.Add(time.Minute).Unix()
			r, ok, err := store.ClaimTracked(tsk, at, &next)
			if err != nil || !ok {
				t.Fatalf("claim %d: ok=%v err=%v", i, ok, err)
			}
			runs[i] = r
		}

		// Runs 1..4 (newer than run 0) fail first -> streak=4.
		for i := 1; i <= 4; i++ {
			runs[i].Status = "failed"
			runs[i].Terminal = true
			runs[i].FinishedAt = base.Add(time.Duration(10+i) * time.Minute).Unix()
			if err := store.Observe(runs[i], base.Add(time.Duration(10+i)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		if v, _ := store.Visibility(tsk.ID); v.ConsecutiveFailures != 4 {
			t.Fatalf("expected streak=4 before late success, got %+v", v)
		}

		// Older run 0 completes LATE -> must NOT wipe out the 4 newer consecutive failures!
		runs[0].Status = "completed"
		runs[0].Terminal = true
		runs[0].FinishedAt = base.Add(15 * time.Minute).Unix()
		if err := store.Observe(runs[0], base.Add(15*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if v, _ := store.Visibility(tsk.ID); v.ConsecutiveFailures != 4 || v.AutoPausedAt != 0 {
			t.Fatalf("older success cleared newer failure streak: %+v", v)
		}

		// Run 5 (newest) fails -> streak reaches 5 and auto-pauses.
		runs[5].Status = "timed_out"
		runs[5].Terminal = true
		runs[5].FinishedAt = base.Add(16 * time.Minute).Unix()
		if err := store.Observe(runs[5], base.Add(16*time.Minute)); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(tsk.ID)
		if err != nil {
			t.Fatal(err)
		}
		v, err := store.Visibility(tsk.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Enabled || v.ConsecutiveFailures != 5 || v.AutoPausedAt == 0 {
			t.Fatalf("expected auto-pause on 5th consecutive newest failure, got enabled=%v vis=%+v", got.Enabled, v)
		}
	})
}

func TestEnqueueCreatedCardIdempotencyAndMarkDelivery(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Unix(1700000000, 0)
	nextRun := now.Add(time.Hour).Unix()
	tsk, err := store.Create(Task{
		Name:           "created-card-job",
		Spec:           "every 1h",
		Kind:           KindInterval,
		ScheduleType:   ScheduleTypeSubtask,
		DeliveryTarget: DeliveryTargetNone,
		Enabled:        true,
		NextRunAt:      &nextRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	creator := Creator{
		BotID:           "bot-1",
		OpenID:          "ou_alice",
		PrincipalID:     "p_alice",
		OriginRootMsgID: "om_root_1",
		OriginChatID:    "oc_chat_1",
		Source:          "turn:1",
	}
	if err := store.EnqueueCreatedCard(tsk, creator, now); err != nil {
		t.Fatal(err)
	}
	// Duplicate enqueue is idempotent.
	if err := store.EnqueueCreatedCard(tsk, creator, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingResultDeliveries("bot-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 created card delivery, got %d", len(pending))
	}
	if pending[0].Kind != DeliveryKindCreated || pending[0].RunID != -tsk.ID || pending[0].Target != DeliveryTargetOrigin {
		t.Fatalf("unexpected created card row: %+v", pending[0])
	}
	if err := store.MarkResultDelivery(pending[0].RunID, DeliveryStatusDelivered, true, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pendingAfter, err := store.PendingResultDeliveries("bot-1", now.Add(time.Minute))
	if err != nil || len(pendingAfter) != 0 {
		t.Fatalf("expected empty queue after mark delivered, got err=%v len=%d", err, len(pendingAfter))
	}
}
