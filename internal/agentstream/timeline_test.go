package agentstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestTurnLifecycleProducesOneOrderedSemanticStream(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 27, 9, 30, 0, 0, time.UTC)
	timeline := agentstream.New(agentstream.Config{
		SessionID:   "session-1",
		Epoch:       "epoch-1",
		MailboxSize: 8,
		Now:         func() time.Time { return now },
		NewTurnID:   func() string { return "turn-1" },
	})
	_, subscription, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	t.Cleanup(subscription.Close)

	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{
		InputSummary: "explain the repository",
		Origin:       agentstream.Origin{Actor: agentstream.ActorUser, Channel: "cli"},
	}, func(ctx context.Context, turn *agentstream.Turn) error {
		if _, err := turn.Emit(ctx, agentstream.AssistantDelta{Text: "working"}, agentstream.Origin{
			Actor:    agentstream.ActorAgent,
			Provider: "codex",
		}); err != nil {
			return err
		}
		_, err := turn.Finish(ctx, agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}, agentstream.Origin{
			Actor:    agentstream.ActorAgent,
			Provider: "codex",
		})
		return err
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if got, want := turn.Ref(), (agentstream.TurnRef{ID: "turn-1", Seq: 1}); got != want {
		t.Fatalf("Turn.Ref() = %#v, want %#v", got, want)
	}

	wantBodies := []agentstream.Kind{
		agentstream.KindTurnStarted,
		agentstream.KindAssistantDelta,
		agentstream.KindTurnFinished,
	}
	for i, wantKind := range wantBodies {
		event, err := subscription.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv(%d) error = %v", i, err)
		}
		if got := event.Body.Kind(); got != wantKind {
			t.Errorf("event %d kind = %q, want %q", i, got, wantKind)
		}
		if got, want := event.Cursor, (agentstream.Cursor{Epoch: "epoch-1", Seq: uint64(i + 1)}); got != want {
			t.Errorf("event %d cursor = %#v, want %#v", i, got, want)
		}
		if event.Turn == nil || *event.Turn != turn.Ref() {
			t.Errorf("event %d turn = %#v, want %#v", i, event.Turn, turn.Ref())
		}
		if event.SessionID != "session-1" || !event.At.Equal(now) {
			t.Errorf("event %d envelope = %#v", i, event)
		}
	}

	select {
	case completion := <-turn.Done():
		if completion.Turn != turn.Ref() || completion.Terminal.Outcome != agentstream.OutcomeSuccess {
			t.Fatalf("completion = %#v", completion)
		}
	case <-time.After(time.Second):
		t.Fatal("Turn.Done() did not complete")
	}
}

func TestLivePermissionKeepsActionableInputWhileHistoryIsRedacted(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{SessionID: "session-1", Epoch: "epoch-1"})
	_, live, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(live.Close)
	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.Recv(context.Background()); err != nil { // turn_started
		t.Fatal(err)
	}
	rawInput := json.RawMessage(`{"command":"git status"}`)
	if _, err := turn.Emit(context.Background(), agentstream.PermissionRequested{Request: agentstream.PermissionRequest{
		ID:       "permission-1",
		ToolCall: &agentstream.PermissionToolCall{Name: "Bash", RawInput: rawInput},
	}}, agentstream.Origin{Actor: agentstream.ActorTool}); err != nil {
		t.Fatal(err)
	}
	liveEvent, err := live.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	livePermission := liveEvent.Body.(agentstream.PermissionRequested).Request
	if livePermission.ToolCall == nil || string(livePermission.ToolCall.RawInput) != string(rawInput) {
		t.Fatalf("live permission input = %#v, want actionable input", livePermission.ToolCall)
	}

	_, history, err := timeline.Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadHistory})
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	if _, err := history.Recv(context.Background()); err != nil { // turn_started
		t.Fatal(err)
	}
	historyEvent, err := history.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	historyPermission := historyEvent.Body.(agentstream.PermissionRequested).Request
	if historyPermission.ToolCall == nil || len(historyPermission.ToolCall.RawInput) != 0 {
		t.Fatalf("history permission input = %#v, want redacted", historyPermission.ToolCall)
	}
}

func TestFinishIsSingleIdempotentTerminalAndRejectsLateEvents(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{Epoch: "epoch-1"})
	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		return nil
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	waiter1 := turn.Done()
	waiter2 := turn.Done()
	terminal := agentstream.Terminal{Outcome: agentstream.OutcomeError, Code: "provider_stream_closed", Message: "stream closed"}
	first, err := turn.Finish(context.Background(), terminal, agentstream.Origin{Actor: agentstream.ActorSystem})
	if err != nil {
		t.Fatalf("Finish(first) error = %v", err)
	}
	duplicate, err := turn.Finish(context.Background(), terminal, agentstream.Origin{Actor: agentstream.ActorAgent})
	if err != nil {
		t.Fatalf("Finish(duplicate) error = %v", err)
	}
	if duplicate.Cursor != first.Cursor || duplicate.Origin != first.Origin {
		t.Fatalf("duplicate terminal = %#v, want original %#v", duplicate, first)
	}
	if _, err := turn.Finish(context.Background(), agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}, agentstream.Origin{}); !errors.Is(err, agentstream.ErrTerminalConflict) {
		t.Fatalf("Finish(conflict) error = %v, want ErrTerminalConflict", err)
	}
	if _, err := turn.Emit(context.Background(), agentstream.AssistantDelta{Text: "late"}, agentstream.Origin{}); !errors.Is(err, agentstream.ErrTurnFinished) {
		t.Fatalf("Emit(late) error = %v, want ErrTurnFinished", err)
	}

	for i, waiter := range []<-chan agentstream.Completion{waiter1, waiter2, turn.Done()} {
		select {
		case completion := <-waiter:
			if completion.Terminal != terminal || completion.Event.Cursor != first.Cursor {
				t.Errorf("waiter %d completion = %#v", i, completion)
			}
		case <-time.After(time.Second):
			t.Errorf("waiter %d did not complete", i)
		}
	}

	head, subscription, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	subscription.Close()
	if head.Cursor.Seq != 2 { // TurnStarted + the first TurnFinished only.
		t.Fatalf("Head.Cursor.Seq = %d, want 2", head.Cursor.Seq)
	}
}

func TestDispatchFailureProducesObservableSubmitFailedTerminal(t *testing.T) {
	t.Parallel()

	dispatchFailure := errors.New("provider unavailable")
	timeline := agentstream.New(agentstream.Config{Epoch: "epoch-1", MailboxSize: 4})
	_, subscription, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	t.Cleanup(subscription.Close)

	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{
		TurnID: "turn-1",
		Origin: agentstream.Origin{Actor: agentstream.ActorUser, Channel: "feishu", Provider: "claude", LaunchID: "launch-1"},
	}, func(context.Context, *agentstream.Turn) error {
		return dispatchFailure
	})
	var gotDispatchError *agentstream.DispatchError
	if !errors.As(err, &gotDispatchError) || !errors.Is(err, dispatchFailure) {
		t.Fatalf("StartTurn() error = %v, want wrapped DispatchError", err)
	}
	if turn == nil || gotDispatchError.Turn != turn.Ref() {
		t.Fatalf("turn = %#v, dispatch error = %#v", turn, gotDispatchError)
	}

	if _, err := subscription.Recv(context.Background()); err != nil { // TurnStarted
		t.Fatalf("Recv(start) error = %v", err)
	}
	finished, err := subscription.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv(finished) error = %v", err)
	}
	body, ok := finished.Body.(agentstream.TurnFinished)
	if !ok || body.Terminal.Outcome != agentstream.OutcomeError || body.Terminal.Code != "submit_failed" || body.Terminal.Message != dispatchFailure.Error() {
		t.Fatalf("terminal body = %#v", finished.Body)
	}
	if finished.Origin.Actor != agentstream.ActorSystem || finished.Origin.Provider != "claude" || finished.Origin.LaunchID != "launch-1" {
		t.Fatalf("terminal origin = %#v", finished.Origin)
	}
	completion := <-turn.Done()
	if completion.Event.Cursor != finished.Cursor {
		t.Fatalf("completion cursor = %#v, want %#v", completion.Event.Cursor, finished.Cursor)
	}
}

func TestInvalidPayloadsAreRejectedWithoutChangingTheStream(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{Epoch: "epoch-1"})
	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		return nil
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	var nilDelta *agentstream.AssistantDelta
	invalidTurnPayloads := []agentstream.TurnPayload{
		nilDelta,
		agentstream.ToolChanged{Phase: "future_phase"},
		agentstream.PermissionRequested{},
		agentstream.PermissionResolved{},
		agentstream.UsageObserved{Usage: json.RawMessage(`not-json`)},
		agentstream.Diagnostic{Severity: "fatal"},
	}
	for i, payload := range invalidTurnPayloads {
		if _, err := turn.Emit(context.Background(), payload, agentstream.Origin{}); !errors.Is(err, agentstream.ErrInvalidPayload) {
			t.Errorf("Emit(invalid %d) error = %v, want ErrInvalidPayload", i, err)
		}
	}

	tooLarge := make(json.RawMessage, agentstream.MaxExtensionPayloadBytes+1)
	for i, payload := range []agentstream.SessionPayload{
		(*agentstream.CommandsReplaced)(nil),
		agentstream.SessionDiagnostic{Severity: "fatal"},
		agentstream.ExtensionObserved{},
		agentstream.ExtensionObserved{Namespace: "codex", Schema: "v1", Payload: json.RawMessage(`not-json`)},
		agentstream.ExtensionObserved{Namespace: "codex", Schema: "v1", Payload: tooLarge},
	} {
		if _, err := timeline.Publish(context.Background(), payload, agentstream.Origin{}); !errors.Is(err, agentstream.ErrInvalidPayload) {
			t.Errorf("Publish(invalid %d) error = %v, want ErrInvalidPayload", i, err)
		}
	}

	head, subscription, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	subscription.Close()
	if head.Cursor.Seq != 1 { // Only TurnStarted was accepted.
		t.Fatalf("Head.Cursor.Seq = %d, want 1", head.Cursor.Seq)
	}
	if _, err := turn.Finish(context.Background(), agentstream.Terminal{Outcome: "unknown"}, agentstream.Origin{}); !errors.Is(err, agentstream.ErrInvalidPayload) {
		t.Fatalf("Finish(unknown) error = %v, want ErrInvalidPayload", err)
	}
}

func TestEventsAndStickyHeadDefensivelyCopyMutablePayloads(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{Epoch: "epoch-1", MailboxSize: 8})
	_, subscription, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	t.Cleanup(subscription.Close)

	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		return nil
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if _, err := subscription.Recv(context.Background()); err != nil { // TurnStarted
		t.Fatalf("Recv(start) error = %v", err)
	}

	originalInput := json.RawMessage(`{"path":"README.md"}`)
	original := agentstream.ToolChanged{
		Phase: agentstream.ToolPhaseStarted,
		Tool:  agentstream.Tool{ID: "tool-1", Input: originalInput},
	}
	returned, err := turn.Emit(context.Background(), original, agentstream.Origin{})
	if err != nil {
		t.Fatalf("Emit() error = %v", err)
	}
	originalInput[2] = 'X'
	returnedBody := returned.Body.(agentstream.ToolChanged)
	returnedBody.Tool.Input[2] = 'Y'

	received, err := subscription.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv(tool) error = %v", err)
	}
	receivedBody := received.Body.(agentstream.ToolChanged)
	if got, want := string(receivedBody.Tool.Input), `{"path":"README.md"}`; got != want {
		t.Fatalf("received input = %q, want %q", got, want)
	}

	commands := []agentstream.Command{{Name: "review"}}
	published, err := timeline.Publish(context.Background(), agentstream.CommandsReplaced{Commands: commands}, agentstream.Origin{})
	if err != nil {
		t.Fatalf("Publish(commands) error = %v", err)
	}
	commands[0].Name = "mutated-original"
	publishedBody := published.Body.(agentstream.CommandsReplaced)
	publishedBody.Commands[0].Name = "mutated-return"

	head1, headSub1, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe(head1) error = %v", err)
	}
	headSub1.Close()
	if head1.Commands == nil || head1.Commands.Commands[0].Name != "review" {
		t.Fatalf("head1 commands = %#v", head1.Commands)
	}
	head1.Commands.Commands[0].Name = "mutated-head"
	head2, headSub2, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe(head2) error = %v", err)
	}
	headSub2.Close()
	if head2.Commands == nil || head2.Commands.Commands[0].Name != "review" {
		t.Fatalf("head2 commands = %#v", head2.Commands)
	}
}

func TestSlowSubscriberFailsExplicitlyWithoutBlockingTimeline(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{Epoch: "epoch-1", MailboxSize: 1})
	_, slow, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	t.Cleanup(slow.Close)
	if _, err := timeline.Publish(context.Background(), agentstream.CommandsReplaced{}, agentstream.Origin{}); err != nil {
		t.Fatalf("Publish(first) error = %v", err)
	}
	if _, err := timeline.Publish(context.Background(), agentstream.CommandsReplaced{}, agentstream.Origin{}); err != nil {
		t.Fatalf("Publish(second) error = %v", err)
	}
	if _, err := slow.Recv(context.Background()); !errors.Is(err, agentstream.ErrLagged) {
		t.Fatalf("slow.Recv() error = %v, want ErrLagged", err)
	}

	head, healthy, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe(healthy) error = %v", err)
	}
	t.Cleanup(healthy.Close)
	if head.Cursor.Seq != 2 {
		t.Fatalf("Head.Cursor.Seq = %d, want 2", head.Cursor.Seq)
	}
	if _, err := timeline.Publish(context.Background(), agentstream.CommandsReplaced{}, agentstream.Origin{}); err != nil {
		t.Fatalf("Publish(third) error = %v", err)
	}
	if event, err := healthy.Recv(context.Background()); err != nil || event.Cursor.Seq != 3 {
		t.Fatalf("healthy.Recv() = (%#v, %v), want seq 3", event, err)
	}
}

func TestConcurrentEmitsReceiveOneCanonicalOrder(t *testing.T) {
	t.Parallel()

	const emissionCount = 64
	timeline := agentstream.New(agentstream.Config{Epoch: "epoch-1", MailboxSize: emissionCount + 2})
	_, subscription, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	t.Cleanup(subscription.Close)
	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		return nil
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	var wg sync.WaitGroup
	errorsCh := make(chan error, emissionCount)
	for i := 0; i < emissionCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := turn.Emit(context.Background(), agentstream.AssistantDelta{Text: fmt.Sprintf("delta-%d", i)}, agentstream.Origin{})
			errorsCh <- err
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("Emit() error = %v", err)
		}
	}
	if _, err := turn.Finish(context.Background(), agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}, agentstream.Origin{}); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	seenText := make(map[string]struct{}, emissionCount)
	for wantSeq := uint64(1); wantSeq <= emissionCount+2; wantSeq++ {
		event, err := subscription.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv(seq %d) error = %v", wantSeq, err)
		}
		if event.Cursor.Seq != wantSeq {
			t.Fatalf("cursor seq = %d, want %d", event.Cursor.Seq, wantSeq)
		}
		if delta, ok := event.Body.(agentstream.AssistantDelta); ok {
			seenText[delta.Text] = struct{}{}
		}
	}
	if len(seenText) != emissionCount {
		t.Fatalf("unique deltas = %d, want %d", len(seenText), emissionCount)
	}
}

func TestOnlyOneTurnCanBeActiveAndTurnIDsCannotBeReused(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{Epoch: "epoch-1"})
	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		return nil
	})
	if err != nil {
		t.Fatalf("StartTurn(first) error = %v", err)
	}
	dispatched := false
	if _, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-2"}, func(context.Context, *agentstream.Turn) error {
		dispatched = true
		return nil
	}); !errors.Is(err, agentstream.ErrTurnActive) {
		t.Fatalf("StartTurn(active) error = %v, want ErrTurnActive", err)
	}
	if dispatched {
		t.Fatal("dispatch ran for rejected active turn")
	}
	if _, err := turn.Finish(context.Background(), agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}, agentstream.Origin{}); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if _, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		return nil
	}); !errors.Is(err, agentstream.ErrDuplicateTurnID) {
		t.Fatalf("StartTurn(reused ID) error = %v, want ErrDuplicateTurnID", err)
	}
	second, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-2"}, func(context.Context, *agentstream.Turn) error {
		return nil
	})
	if err != nil {
		t.Fatalf("StartTurn(second) error = %v", err)
	}
	if second.Ref().Seq != 2 {
		t.Fatalf("second TurnRef.Seq = %d, want 2", second.Ref().Seq)
	}
}

func TestTypedPayloadsKeepTurnAndSessionScopesDistinct(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{Epoch: "epoch-1", MailboxSize: 32})
	_, subscription, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	t.Cleanup(subscription.Close)

	turnPayloads := []struct {
		body agentstream.TurnPayload
		kind agentstream.Kind
	}{
		{agentstream.AssistantDelta{Text: "delta"}, agentstream.KindAssistantDelta},
		{agentstream.AssistantMessage{Text: "message"}, agentstream.KindAssistantMessage},
		{agentstream.ThinkingDelta{Text: "thought"}, agentstream.KindThinkingDelta},
		{agentstream.PlanReplaced{Entries: []agentstream.PlanEntry{{Content: "inspect", Status: "in_progress"}}}, agentstream.KindPlanReplaced},
		{agentstream.ToolChanged{Phase: agentstream.ToolPhaseStarted, Tool: agentstream.Tool{ID: "tool-1", Name: "shell"}}, agentstream.KindToolChanged},
		{agentstream.PermissionRequested{Request: agentstream.PermissionRequest{ID: "permission-1"}}, agentstream.KindPermissionRequested},
		{agentstream.PermissionResolved{RequestID: "permission-1", OptionIDs: []string{"allow"}}, agentstream.KindPermissionResolved},
		{agentstream.UsageObserved{Usage: json.RawMessage(`{"input_tokens":12}`)}, agentstream.KindUsageObserved},
		{agentstream.ResultObserved{Text: "done", Usage: json.RawMessage(`{"output_tokens":3}`), CostUSD: 0.01}, agentstream.KindResultObserved},
		{agentstream.Diagnostic{Severity: agentstream.SeverityWarning, Code: "retry", Message: "retrying", Retryable: true}, agentstream.KindDiagnostic},
	}

	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(ctx context.Context, turn *agentstream.Turn) error {
		for _, test := range turnPayloads {
			if _, err := turn.Emit(ctx, test.body, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
				return err
			}
		}
		_, err := turn.Finish(ctx, agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}, agentstream.Origin{Actor: agentstream.ActorAgent})
		return err
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	// TurnStarted, each typed payload, then TurnFinished all retain the exact
	// capability-bound TurnRef.
	wantTurnKinds := []agentstream.Kind{agentstream.KindTurnStarted}
	for _, payload := range turnPayloads {
		wantTurnKinds = append(wantTurnKinds, payload.kind)
	}
	wantTurnKinds = append(wantTurnKinds, agentstream.KindTurnFinished)
	for i, wantKind := range wantTurnKinds {
		event, err := subscription.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv(turn %d) error = %v", i, err)
		}
		if event.Body.Kind() != wantKind || event.Turn == nil || *event.Turn != turn.Ref() {
			t.Errorf("turn event %d = %#v, want kind %q and turn %#v", i, event, wantKind, turn.Ref())
		}
	}

	sessionPayloads := []struct {
		body agentstream.SessionPayload
		kind agentstream.Kind
	}{
		{agentstream.SessionMetadataChanged{AgentSessionID: "native-1"}, agentstream.KindSessionMetadataChanged},
		{agentstream.CommandsReplaced{Commands: []agentstream.Command{{Name: "review"}}}, agentstream.KindCommandsReplaced},
		{agentstream.SessionDiagnostic{Severity: agentstream.SeverityError, Code: "process_exit", Message: "exited"}, agentstream.KindSessionDiagnostic},
		{agentstream.ExtensionObserved{Namespace: "codex", Schema: "v1", Payload: json.RawMessage(`{"type":"future"}`)}, agentstream.KindExtensionObserved},
	}
	for _, test := range sessionPayloads {
		if _, err := timeline.Publish(context.Background(), test.body, agentstream.Origin{Actor: agentstream.ActorSystem}); err != nil {
			t.Fatalf("Publish(%s) error = %v", test.kind, err)
		}
	}
	for i, test := range sessionPayloads {
		event, err := subscription.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv(session %d) error = %v", i, err)
		}
		if event.Body.Kind() != test.kind || event.Turn != nil {
			t.Errorf("session event %d = %#v, want kind %q and nil turn", i, event, test.kind)
		}
	}
}

func TestSessionPublishReturnsStickyHeadAndOnlyFutureLiveEvents(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{
		SessionID: "session-1",
		Epoch:     "epoch-1",
	})
	if _, err := timeline.Publish(context.Background(), agentstream.SessionMetadataChanged{
		AgentSessionID: "native-session-1",
		EffectiveModel: "gpt-5",
	}, agentstream.Origin{Actor: agentstream.ActorAgent, Provider: "codex"}); err != nil {
		t.Fatalf("Publish(metadata) error = %v", err)
	}
	if _, err := timeline.Publish(context.Background(), agentstream.CommandsReplaced{
		Commands: []agentstream.Command{{Name: "review", Description: "Review code"}},
	}, agentstream.Origin{Actor: agentstream.ActorAgent, Provider: "codex"}); err != nil {
		t.Fatalf("Publish(commands) error = %v", err)
	}

	head, subscription, err := timeline.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	t.Cleanup(subscription.Close)
	if got, want := head.Cursor, (agentstream.Cursor{Epoch: "epoch-1", Seq: 2}); got != want {
		t.Fatalf("Head.Cursor = %#v, want %#v", got, want)
	}
	if head.Metadata == nil || head.Metadata.AgentSessionID != "native-session-1" || head.Metadata.EffectiveModel != "gpt-5" {
		t.Fatalf("Head.Metadata = %#v", head.Metadata)
	}
	if head.Commands == nil || len(head.Commands.Commands) != 1 || head.Commands.Commands[0].Name != "review" {
		t.Fatalf("Head.Commands = %#v", head.Commands)
	}

	published, err := timeline.Publish(context.Background(), agentstream.CommandsReplaced{
		Commands: []agentstream.Command{{Name: "plan"}},
	}, agentstream.Origin{Actor: agentstream.ActorAgent, Provider: "codex"})
	if err != nil {
		t.Fatalf("Publish(live commands) error = %v", err)
	}
	if published.Turn != nil {
		t.Fatalf("session-scoped event Turn = %#v, want nil", published.Turn)
	}
	live, err := subscription.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if live.Cursor.Seq != 3 || live.Body.Kind() != agentstream.KindCommandsReplaced {
		t.Fatalf("live event = %#v", live)
	}
}
