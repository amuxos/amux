package agentui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestReplyProjectionNarrationAndReconcileFinalAnswer(t *testing.T) {
	var proj ReplyProjection
	proj.BeginTurn("turn-1")

	if got := proj.Append("Step 1: inspect "); got != "Step 1: inspect" {
		t.Fatalf("append = %q", got)
	}
	if got := proj.Append("repo."); got != "Step 1: inspect repo." {
		t.Fatalf("append = %q", got)
	}
	if !proj.Boundary() {
		t.Fatal("expected non-empty boundary")
	}
	if !proj.HasCompletedMessage() {
		t.Fatal("expected HasCompletedMessage true")
	}
	proj.Append("Checked.\n\nFinal answer.")
	before, after, final := proj.ReconcileFinalAnswer("Final answer.")
	if before != "Step 1: inspect repo.\n\nChecked.\n\nFinal answer." {
		t.Fatalf("before = %q", before)
	}
	if after != "Step 1: inspect repo.\n\nChecked." {
		t.Fatalf("after = %q", after)
	}
	if final != "Final answer." {
		t.Fatalf("final = %q", final)
	}
	if !proj.HasAuthoritativeResult() {
		t.Fatal("expected HasAuthoritativeResult true")
	}
	if _, ok := proj.FallbackFinalAnswer(); ok {
		t.Fatal("FallbackFinalAnswer must be suppressed after authoritative result")
	}
}

func TestReplyProjectionSteeredTurnMergeAndFallback(t *testing.T) {
	proj := NewReplyProjection()
	proj.BeginTurn("turn-1")

	// Pre-steer narration is kept out of the steered final answer slice.
	proj.Append("Initial progress before steer")
	proj.Boundary()

	// Mismatched turnID must be ignored.
	proj.MarkSteered("other-turn")
	if proj.steered {
		t.Fatal("MarkSteered accepted mismatched turnID")
	}
	proj.MarkSteered("turn-1")

	proj.Append("DONE-FG")
	proj.Boundary()
	proj.Append("收到甲")
	proj.Boundary()
	proj.Append("收到乙")
	proj.Boundary()

	_, _, final := proj.ReconcileFinalAnswer("收到乙")
	want := "DONE-FG\n\n收到甲\n\n收到乙"
	if final != want {
		t.Fatalf("steered final = %q, want %q", final, want)
	}

	// Fallback path when no authoritative ResultObserved arrives.
	proj.BeginTurn("turn-2")
	proj.MarkSteered("turn-2")
	proj.Append("Part A")
	proj.Boundary()
	proj.Append("Part B")
	proj.Boundary()
	fallback, ok := proj.FallbackFinalAnswer()
	if !ok || fallback != "Part A\n\nPart B" {
		t.Fatalf("fallback = %q, ok=%v; want Part A\\n\\nPart B", fallback, ok)
	}
}

func TestReplyProjectionCollapseRepeatedGluedAnswer(t *testing.T) {
	unit := "这是重复拼接的完整回答内容。"
	if got := MergeSteeredFinalAnswer([]string{unit + unit}, unit); got != unit {
		t.Fatalf("glued repeat = %q, want %q", got, unit)
	}
	if got := MergeSteeredFinalAnswer([]string{"收到甲" + unit}, unit); got != "收到甲\n\n"+unit {
		t.Fatalf("glued prefix+result = %q", got)
	}
	if got := CollapseRepeatedText("88"); got != "88" {
		t.Fatalf("short answer altered: %q", got)
	}
}

func TestReplyProjectionBoundsLongNarration(t *testing.T) {
	var proj ReplyProjection
	long := "START" + strings.Repeat("字", MaxProgressRunes+50) + "END"
	got := proj.Append(long)
	if len([]rune(got)) != MaxProgressRunes || !strings.Contains(got, OmittedProgressMarker) ||
		!strings.HasPrefix(got, "START") || !strings.HasSuffix(got, "END") {
		t.Fatalf("bounded summary len=%d got=%q", len([]rune(got)), got)
	}
}

func TestSessionMetadataAndGoalEventsDoNotDisruptActiveTurn(t *testing.T) {
	p := NewPresenter()
	startRaw, err := agentstream.MarshalEvent(agentstream.Event{
		SessionID: "sess-1",
		Cursor:    agentstream.Cursor{Epoch: "e1", Seq: 1},
		Turn:      &agentstream.TurnRef{ID: "turn-1", Seq: 1},
		At:        time.Now().UTC(),
		Origin:    agentstream.Origin{Channel: "lark", Actor: agentstream.ActorUser},
		Body:      agentstream.TurnStarted{InputSummary: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var actions []Action
	for _, ev := range FromAttachEvent(&pb.AttachEvent{EffectiveModel: "gpt-6.1-sol", CanonicalJson: startRaw}) {
		actions = append(actions, p.Consume(ev)...)
	}
	assertActionKinds(t, actions, ActionEffectiveModelChanged, ActionTurnStarted)
	if actions[0].Text != "gpt-6.1-sol" || actions[1].Turn.ID != "turn-1" {
		t.Fatalf("actions = %+v", actions)
	}

	// available_commands must not start or reset the turn.
	cmdEvents := FromAttachEvent(&pb.AttachEvent{
		EventType:      "available_commands",
		StructuredJson: json.RawMessage(`[{"name":"help"}]`),
	})
	actions = nil
	for _, ev := range cmdEvents {
		actions = append(actions, p.Consume(ev)...)
	}
	assertActionKinds(t, actions, ActionAvailableCommands)
	if string(actions[0].RawJSON) != `[{"name":"help"}]` {
		t.Fatalf("commands raw = %s", actions[0].RawJSON)
	}

	// Live canonical GoalChanged emits ActionGoalChanged; historical GoalChanged is suppressed.
	goalRaw, err := agentstream.MarshalEvent(agentstream.Event{
		SessionID: "sess-1",
		Cursor:    agentstream.Cursor{Epoch: "e1", Seq: 2},
		At:        time.Now().UTC(),
		Origin:    agentstream.Origin{Channel: "goal", Actor: agentstream.ActorSystem},
		Body:      agentstream.GoalChanged{Goal: &agentstream.GoalState{ThreadID: "th-1", Status: "active", Objective: "ship"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	actions = nil
	for _, ev := range FromAttachEvent(&pb.AttachEvent{CanonicalJson: goalRaw, Historical: true}) {
		actions = append(actions, p.Consume(ev)...)
	}
	if len(actions) != 0 {
		t.Fatalf("historical goal_changed actions = %+v, want none", actions)
	}
	for _, ev := range FromAttachEvent(&pb.AttachEvent{CanonicalJson: goalRaw, Historical: false}) {
		actions = append(actions, p.Consume(ev)...)
	}
	assertActionKinds(t, actions, ActionGoalChanged)
	if actions[0].Goal == nil || actions[0].Goal.ThreadID != "th-1" || actions[0].Goal.Status != "active" {
		t.Fatalf("goal action = %+v", actions[0])
	}

	// Legacy structured goal_changed still maps cleanly.
	actions = nil
	for _, ev := range FromAttachEvent(&pb.AttachEvent{
		EventType:      "goal_changed",
		StructuredJson: json.RawMessage(`{"goal":{"threadId":"th-2","status":"complete"}}`),
	}) {
		actions = append(actions, p.Consume(ev)...)
	}
	assertActionKinds(t, actions, ActionGoalChanged)
	if actions[0].Goal == nil || actions[0].Goal.ThreadID != "th-2" || actions[0].Goal.Status != "complete" {
		t.Fatalf("legacy goal action = %+v", actions[0])
	}

	// Original turn-1 must still be active and finish cleanly.
	doneRaw, err := agentstream.MarshalEvent(agentstream.Event{
		SessionID: "sess-1",
		Cursor:    agentstream.Cursor{Epoch: "e1", Seq: 3},
		Turn:      &agentstream.TurnRef{ID: "turn-1", Seq: 1},
		At:        time.Now().UTC(),
		Origin:    agentstream.Origin{Channel: "system", Actor: agentstream.ActorSystem},
		Body:      agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}},
	})
	if err != nil {
		t.Fatal(err)
	}
	actions = nil
	for _, ev := range FromAttachEvent(&pb.AttachEvent{CanonicalJson: doneRaw}) {
		actions = append(actions, p.Consume(ev)...)
	}
	assertActionKinds(t, actions, ActionTurnFinished)
	if actions[0].Turn.ID != "turn-1" {
		t.Fatalf("finished turn = %+v, want turn-1", actions[0].Turn)
	}
}

func TestGoalContinuationTurnCarriesCanonicalGoalTurnContext(t *testing.T) {
	raw, err := agentstream.MarshalEvent(agentstream.Event{
		SessionID: "sess-goal-1",
		Cursor:    agentstream.Cursor{Epoch: "e1", Seq: 1},
		Turn:      &agentstream.TurnRef{ID: "turn-goal-1", Seq: 1},
		At:        time.Now().UTC(),
		Origin:    agentstream.Origin{Channel: "goal", NativeType: "goal_continuation", Actor: agentstream.ActorSystem},
		Body:      agentstream.TurnStarted{InputSummary: "finish release checklist"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewPresenter()
	var actions []Action
	for _, ev := range FromAttachEvent(&pb.AttachEvent{CanonicalJson: raw}) {
		actions = append(actions, p.Consume(ev)...)
	}
	assertActionKinds(t, actions, ActionTurnStarted)
	if actions[0].Source != SourceGoal {
		t.Fatalf("source = %q, want goal", actions[0].Source)
	}
	if actions[0].GoalTurn == nil || actions[0].GoalTurn.SessionID != "sess-goal-1" || actions[0].GoalTurn.Objective != "finish release checklist" {
		t.Fatalf("GoalTurn = %+v", actions[0].GoalTurn)
	}
}
