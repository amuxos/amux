package agentui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPresenterStartsAChangedTurnBeforeAdoptingItsIdentity(t *testing.T) {
	p := NewPresenter()
	assertActionKinds(t, p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-1", Seq: 1}}), ActionTurnStarted)
	p.Consume(Event{Kind: KindThinkingDelta, Text: "old turn", Turn: &TurnEvent{ID: "turn-1", Seq: 1}})

	actions := p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-2", Seq: 2}})
	assertActionKinds(t, actions, ActionTurnStarted)
	if actions[0].Turn.ID != "turn-2" || actions[0].Turn.Seq != 2 {
		t.Fatalf("new turn = %+v, want turn-2/2", actions[0].Turn)
	}

	actions = p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-2", Seq: 2, State: "turn_done"}})
	assertActionKinds(t, actions, ActionTurnFinished)
}

func TestPresenterIgnoresLateEventsFromOlderTurn(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-2", Seq: 2}})
	if actions := p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-1", Seq: 1, State: "turn_done"}}); len(actions) != 0 {
		t.Fatalf("late old turn actions = %+v, want ignored", actions)
	}
	actions := p.Consume(Event{Kind: KindAssistantTextDelta, Text: "new turn", Turn: &TurnEvent{ID: "turn-2", Seq: 2}})
	assertActionKinds(t, actions, ActionReplyAppend)
}

func TestPresenterIgnoresDuplicateTerminalForCurrentTurn(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-1", Seq: 1}})
	first := p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-1", Seq: 1, State: "turn_done"}})
	assertActionKinds(t, first, ActionTurnFinished)

	if actions := p.Consume(Event{Kind: KindTurnDone}); len(actions) != 0 {
		t.Fatalf("duplicate terminal actions = %+v, want ignored", actions)
	}
}

func TestPresenterPreservesReplySource(t *testing.T) {
	p := NewPresenter()
	actions := p.Consume(Event{Kind: KindAssistantTextDelta, Source: SourceSystem, Text: "[system] provider error"})
	assertActionKinds(t, actions, ActionReplyAppend)
	if actions[0].Source != SourceSystem {
		t.Fatalf("reply source = %q, want %q", actions[0].Source, SourceSystem)
	}
}

func TestPresenterMarksToolBoundariesImmediateAndTracksStartTime(t *testing.T) {
	now := time.Unix(100, 0)
	p := NewPresenterWithClock(func() time.Time { return now })
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-1", Seq: 1}})

	started := p.Consume(Event{Kind: KindToolStarted, Tool: &ToolEvent{ID: "tool-1", Title: "Read"}, Turn: &TurnEvent{ID: "turn-1", Seq: 1}})
	if !started[len(started)-1].Immediate {
		t.Fatal("tool_started activity must be immediate")
	}
	if got := started[len(started)-1].Activity; !got.StartedAt.Equal(now) || !got.Tools[0].StartedAt.Equal(now) {
		t.Fatalf("activity timestamps = %+v, want %v", got, now)
	}

	now = now.Add(3 * time.Second)
	updated := p.Consume(Event{Kind: KindToolUpdated, Tool: &ToolEvent{ID: "tool-1", OutputPreview: "working"}, Turn: &TurnEvent{ID: "turn-1", Seq: 1}})
	if updated[0].Immediate {
		t.Fatal("tool_updated activity should remain throttled")
	}
	completed := p.Consume(Event{Kind: KindToolCompleted, Tool: &ToolEvent{ID: "tool-1"}, Turn: &TurnEvent{ID: "turn-1", Seq: 1}})
	if !completed[0].Immediate {
		t.Fatal("tool_completed activity must be immediate")
	}
}

func TestPresenterBuildsProviderIndependentTurnActions(t *testing.T) {
	p := NewPresenter()

	actions := p.Consume(Event{
		Kind:   KindInputSubmitted,
		Source: SourceFeishu,
		Text:   "fix the tests",
		Turn:   &TurnEvent{ID: "turn-1", Seq: 3},
	})
	assertActionKinds(t, actions, ActionTurnStarted)

	actions = p.Consume(Event{Kind: KindAssistantTextDelta, Text: "I will inspect it.", Turn: &TurnEvent{ID: "turn-1", Seq: 3}})
	assertActionKinds(t, actions, ActionReplyAppend)

	actions = p.Consume(Event{
		Kind: KindToolStarted,
		Tool: &ToolEvent{ID: "tool-1", Name: "Read", Title: "Read proc.go", Status: "running"},
		Turn: &TurnEvent{ID: "turn-1", Seq: 3},
	})
	assertActionKinds(t, actions, ActionActivityChanged)
	if got := actions[0].Activity.Tools[0]; got.ID != "tool-1" || got.Status != "running" {
		t.Fatalf("running tool = %+v, want tool-1/running", got)
	}
	actions = p.Consume(Event{
		Kind: KindToolUpdated,
		Tool: &ToolEvent{ID: "tool-1", Status: "running", OutputPreview: "still running"},
		Turn: &TurnEvent{ID: "turn-1", Seq: 3},
	})
	assertActionKinds(t, actions, ActionActivityChanged)

	actions = p.Consume(Event{
		Kind: KindToolCompleted,
		Tool: &ToolEvent{ID: "tool-1", Name: "Read", Title: "Read proc.go", Status: "completed", OutputPreview: "done"},
		Turn: &TurnEvent{ID: "turn-1", Seq: 3},
	})
	assertActionKinds(t, actions, ActionActivityChanged)
	if got := actions[0].Activity.Tools; len(got) != 1 || got[0].Status != "completed" || got[0].OutputPreview != "done" {
		t.Fatalf("completed tools = %+v, want one updated tool", got)
	}

	usage := json.RawMessage(`{"used":1200,"size":8000,"cost":{"amount":0.12,"currency":"USD"}}`)
	if actions := p.Consume(Event{Kind: KindUsage, UsageJSON: usage, Turn: &TurnEvent{ID: "turn-1", Seq: 3}}); len(actions) != 0 {
		t.Fatalf("usage actions before turn end = %+v, want deferred", actions)
	}

	actions = p.Consume(Event{Kind: KindResult, Text: "All tests pass.", Turn: &TurnEvent{ID: "turn-1", Seq: 3}})
	assertActionKinds(t, actions, ActionReplyReconcileFinalMessage)

	actions = p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-1", Seq: 3, State: "turn_done"}})
	assertActionKinds(t, actions, ActionActivityChanged, ActionTurnFinished)
	if got := actions[0].Activity.UsageJSON; string(got) != string(usage) {
		t.Fatalf("final usage = %s, want %s", got, usage)
	}
	if actions[0].Activity.Status != "done" {
		t.Fatalf("final activity status = %q, want done", actions[0].Activity.Status)
	}
}

func TestPresenterEmitsAssistantMessageBoundary(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-1", Seq: 1}})

	actions := p.Consume(Event{Kind: KindAssistantText, Turn: &TurnEvent{ID: "turn-1", Seq: 1}})
	assertActionKinds(t, actions, ActionReplyMessageBoundary)
}

func TestPresenterKeepsThinkingPlanAndToolsSeparate(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-2", Seq: 4}})

	actions := p.Consume(Event{Kind: KindThinkingDelta, Text: "Checking\ncurrent file", Turn: &TurnEvent{ID: "turn-2", Seq: 4}})
	assertActionKinds(t, actions, ActionActivityChanged)
	if got := actions[0].Activity.CurrentStep; got != "current file" {
		t.Fatalf("current step = %q, want current file", got)
	}

	plan := &PlanEvent{Entries: []PlanEntry{
		{Content: "Inspect", Priority: "high", Status: "completed"},
		{Content: "Fix", Priority: "high", Status: "in_progress"},
	}}
	actions = p.Consume(Event{Kind: KindPlan, Plan: plan, Turn: &TurnEvent{ID: "turn-2", Seq: 4}})
	assertActionKinds(t, actions, ActionActivityChanged)
	if got := actions[0].Activity.Plan; len(got) != 2 || got[1].Content != "Fix" || got[1].Status != "in_progress" {
		t.Fatalf("plan = %+v, want complete replacement", got)
	}

	p.Consume(Event{Kind: KindToolStarted, Tool: &ToolEvent{ID: "a", Name: "Read", Status: "running"}, Turn: &TurnEvent{ID: "turn-2", Seq: 4}})
	actions = p.Consume(Event{Kind: KindToolStarted, Tool: &ToolEvent{ID: "b", Name: "Read", Status: "running"}, Turn: &TurnEvent{ID: "turn-2", Seq: 4}})
	if got := actions[len(actions)-1].Activity.Tools; len(got) != 2 {
		t.Fatalf("same-name tools = %+v, want two rows keyed by ID", got)
	}
}

func TestPresenterDoesNotCreateActivityForSimpleReply(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-3", Seq: 5}})
	p.Consume(Event{Kind: KindAssistantTextDelta, Text: "hello", Turn: &TurnEvent{ID: "turn-3", Seq: 5}})
	actions := p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-3", Seq: 5, State: "turn_done"}})
	assertActionKinds(t, actions, ActionTurnFinished)
}

func TestPresenterCreatesActivityForLongRunningTurn(t *testing.T) {
	now := time.Unix(100, 0)
	p := NewPresenterWithClock(func() time.Time { return now })
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-long", Seq: 12}})

	now = now.Add(9 * time.Second)
	if actions := p.Consume(Event{Kind: KindHeartbeat, Turn: &TurnEvent{ID: "turn-long", Seq: 12}}); len(actions) != 0 {
		t.Fatalf("early heartbeat actions = %+v, want none", actions)
	}
	now = now.Add(time.Second)
	actions := p.Consume(Event{Kind: KindHeartbeat, Turn: &TurnEvent{ID: "turn-long", Seq: 12}})
	assertActionKinds(t, actions, ActionActivityChanged)
	if !actions[0].Immediate {
		t.Fatal("first long-running activity must be sent immediately")
	}
}

func TestPresenterTracksRealActivityAndFreezesTerminalTime(t *testing.T) {
	now := time.Unix(100, 0)
	p := NewPresenterWithClock(func() time.Time { return now })
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-clock", Seq: 15}})

	now = time.Unix(105, 0)
	actions := p.Consume(Event{Kind: KindThinkingDelta, Text: "working", Turn: &TurnEvent{ID: "turn-clock", Seq: 15}})
	view := actions[0].Activity
	if !view.StartedAt.Equal(time.Unix(100, 0)) || !view.LastActivityAt.Equal(now) {
		t.Fatalf("activity clock = %+v, want start=100 last=105", view)
	}

	now = time.Unix(115, 0)
	p.Consume(Event{Kind: KindHeartbeat, Turn: &TurnEvent{ID: "turn-clock", Seq: 15}})
	if got := p.EnsureActivity().LastActivityAt; !got.Equal(time.Unix(105, 0)) {
		t.Fatalf("heartbeat advanced LastActivityAt to %v", got)
	}

	now = time.Unix(130, 0)
	actions = p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-clock", Seq: 15, State: "turn_done"}})
	view = actions[0].Activity
	if !view.FinishedAt.Equal(now) || !view.LastActivityAt.Equal(time.Unix(105, 0)) {
		t.Fatalf("terminal clock = %+v, want finish=130 and last=105", view)
	}
}

func TestPresenterShowsUsageAtTurnEndWithoutOtherActivity(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-usage", Seq: 13}})
	usage := json.RawMessage(`{"total_tokens":42}`)
	if actions := p.Consume(Event{Kind: KindUsage, UsageJSON: usage, Turn: &TurnEvent{ID: "turn-usage", Seq: 13}}); len(actions) != 0 {
		t.Fatalf("usage update actions = %+v, want deferred", actions)
	}
	actions := p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-usage", Seq: 13, State: "turn_done"}})
	assertActionKinds(t, actions, ActionActivityChanged, ActionTurnFinished)
	if string(actions[0].Activity.UsageJSON) != string(usage) {
		t.Fatalf("usage = %s, want %s", actions[0].Activity.UsageJSON, usage)
	}
}

func TestPresenterCanEnsureActivityForBridgeSubtasks(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-subtask", Seq: 14}})
	view := p.EnsureActivity()
	if view == nil || view.TurnID != "turn-subtask" || view.Status != "running" {
		t.Fatalf("ensured activity = %+v", view)
	}
}

func TestPresenterForwardsNonFeishuInputAndPermission(t *testing.T) {
	p := NewPresenter()
	actions := p.Consume(Event{Kind: KindInputSubmitted, Source: SourceCLI, Text: "continue", Turn: &TurnEvent{ID: "turn-4", Seq: 6}})
	assertActionKinds(t, actions, ActionTurnStarted, ActionInputEcho)

	permission := &PermissionEvent{RequestID: "perm-1"}
	actions = p.Consume(Event{Kind: KindPermissionRequested, Permission: permission, Turn: &TurnEvent{ID: "turn-4", Seq: 6}})
	assertActionKinds(t, actions, ActionPermissionRequest)
	if actions[0].Permission != permission {
		t.Fatalf("permission = %+v, want original semantic permission", actions[0].Permission)
	}
}

func TestPresenterDoesNotRestartSameTurnForMultilineInputFrames(t *testing.T) {
	p := NewPresenter()
	turn := &TurnEvent{ID: "turn-lines", Seq: 8}
	assertActionKinds(t, p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Text: "line 1", Turn: turn}), ActionTurnStarted)
	if actions := p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Text: "line 2", Turn: turn}); len(actions) != 0 {
		t.Fatalf("second Feishu frame actions = %+v, want no turn restart or echo", actions)
	}
}

func TestPresenterKeepsFailedTerminalState(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-fail", Seq: 9}})
	p.Consume(Event{Kind: KindThinkingDelta, Text: "running", Turn: &TurnEvent{ID: "turn-fail", Seq: 9}})
	actions := p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-fail", Seq: 9, State: "failed", StopReason: "provider error"}})
	assertActionKinds(t, actions, ActionActivityChanged, ActionTurnFinished)
	if actions[0].Activity.Status != "failed" || actions[1].Turn.State != "failed" {
		t.Fatalf("failed terminal actions = %+v", actions)
	}
}

func TestPresenterDoesNotFailTurnForRecoverableToolFailure(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-recover", Seq: 10}})
	p.Consume(Event{Kind: KindToolFailed, Tool: &ToolEvent{ID: "tool-1", Error: "retryable"}, Turn: &TurnEvent{ID: "turn-recover", Seq: 10}})
	actions := p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-recover", Seq: 10, State: "turn_done"}})
	assertActionKinds(t, actions, ActionActivityChanged, ActionTurnFinished)
	if actions[0].Activity.Status != "done" {
		t.Fatalf("activity status = %q, want done after recovered tool failure", actions[0].Activity.Status)
	}
}

func TestPresenterCollectsUsageCarriedByResult(t *testing.T) {
	p := NewPresenter()
	p.Consume(Event{Kind: KindInputSubmitted, Source: SourceFeishu, Turn: &TurnEvent{ID: "turn-result-usage", Seq: 11}})
	p.Consume(Event{Kind: KindThinkingDelta, Text: "working", Turn: &TurnEvent{ID: "turn-result-usage", Seq: 11}})
	usage := json.RawMessage(`{"input_tokens":10,"output_tokens":5}`)
	p.Consume(Event{Kind: KindResult, Text: "done", UsageJSON: usage, Turn: &TurnEvent{ID: "turn-result-usage", Seq: 11}})
	actions := p.Consume(Event{Kind: KindTurnDone, Turn: &TurnEvent{ID: "turn-result-usage", Seq: 11, State: "turn_done"}})
	if got := actions[0].Activity.UsageJSON; string(got) != string(usage) {
		t.Fatalf("result usage = %s, want %s", got, usage)
	}
}

func assertActionKinds(t *testing.T, actions []Action, want ...ActionKind) {
	t.Helper()
	if len(actions) != len(want) {
		t.Fatalf("action count = %d, want %d: %+v", len(actions), len(want), actions)
	}
	for i := range want {
		if actions[i].Kind != want[i] {
			t.Fatalf("action[%d].Kind = %q, want %q", i, actions[i].Kind, want[i])
		}
	}
}

func TestSubtaskInputIsSilent(t *testing.T) {
	p := NewPresenter()
	ev := Event{Kind: KindInputSubmitted, Source: SourceSubtask, Turn: &TurnEvent{ID: "t1", Seq: 1}, Text: "[后台任务完成] task_id=task-1 status=succeeded\n摘要: long"}
	for _, a := range p.Consume(ev) {
		if a.Kind == ActionInputEcho {
			t.Fatalf("subtask notice echoed: %+v", a)
		}
		if strings.Contains(a.Text, "后台任务完成") || strings.Contains(a.Text, "task_id") {
			t.Fatalf("subtask notice leaked in %s: %q", a.Kind, a.Text)
		}
	}
	if normalizeSource("subtask") != SourceSubtask {
		t.Fatal("subtask source not mapped")
	}
}
