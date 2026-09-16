package agentstream_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
)

type memoryJournal struct {
	mu        sync.Mutex
	events    []agentstream.Event
	appendErr error
	failAt    int
	appends   int
}

func (j *memoryJournal) AppendAgentEvent(_ context.Context, event agentstream.Event) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.appends++
	if j.appendErr != nil && (j.failAt == 0 || j.appends == j.failAt) {
		return j.appendErr
	}
	j.events = append(j.events, agentstream.CloneEvent(event))
	return nil
}

func TestDispatchFailureAlsoReportsTerminalPersistenceFailure(t *testing.T) {
	t.Parallel()

	dispatchFailure := errors.New("provider unavailable")
	appendFailure := errors.New("disk full")
	journal := &memoryJournal{appendErr: appendFailure, failAt: 2}
	timeline, err := agentstream.Open(context.Background(), agentstream.Config{SessionID: "session-1", Epoch: "epoch-1", Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		return dispatchFailure
	})
	if turn == nil {
		t.Fatal("dispatch failure did not return accepted turn")
	}
	if !errors.Is(err, dispatchFailure) || !errors.Is(err, appendFailure) || !errors.Is(err, agentstream.ErrJournalFault) {
		t.Fatalf("StartTurn() error = %v, want dispatch and terminal persistence failures", err)
	}
	completion := <-turn.Done()
	if !errors.Is(completion.Err, agentstream.ErrJournalFault) || completion.Event.Body != nil {
		t.Fatalf("completion = %#v, want infrastructure failure without synthetic terminal", completion)
	}
	if head := timeline.Head(); head.Cursor.Seq != 1 {
		t.Fatalf("head seq = %d, want only durable TurnStarted", head.Cursor.Seq)
	}
}

func (j *memoryJournal) LoadAgentEvents(_ context.Context, sessionID string) ([]agentstream.Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var events []agentstream.Event
	for _, event := range j.events {
		if event.SessionID == sessionID {
			events = append(events, agentstream.CloneEvent(event))
		}
	}
	return events, nil
}

func TestDurableTimelineRestoresHistoryAndContinuesCanonicalOrder(t *testing.T) {
	t.Parallel()

	journal := &memoryJournal{}
	timeline, err := agentstream.Open(context.Background(), agentstream.Config{
		SessionID: "session-1",
		Epoch:     "epoch-1",
		Journal:   journal,
	})
	if err != nil {
		t.Fatalf("Open(first) error = %v", err)
	}
	turn, err := timeline.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(ctx context.Context, turn *agentstream.Turn) error {
		if _, err := turn.Emit(ctx, agentstream.AssistantDelta{Text: "hello"}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
			return err
		}
		_, err := turn.Finish(ctx, agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}, agentstream.Origin{Actor: agentstream.ActorAgent})
		return err
	})
	if err != nil {
		t.Fatalf("StartTurn(first) error = %v", err)
	}
	if turn.Ref().Seq != 1 {
		t.Fatalf("first turn seq = %d, want 1", turn.Ref().Seq)
	}

	restored, err := agentstream.Open(context.Background(), agentstream.Config{
		SessionID: "session-1",
		Journal:   journal,
	})
	if err != nil {
		t.Fatalf("Open(restored) error = %v", err)
	}
	head, history, err := restored.Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadHistory})
	if err != nil {
		t.Fatalf("Read(history) error = %v", err)
	}
	t.Cleanup(history.Close)
	if head.Cursor != (agentstream.Cursor{Epoch: "epoch-1", Seq: 3}) {
		t.Fatalf("restored head = %#v, want epoch-1/3", head.Cursor)
	}
	for seq, kind := range []agentstream.Kind{
		agentstream.KindTurnStarted,
		agentstream.KindAssistantDelta,
		agentstream.KindTurnFinished,
	} {
		event, err := history.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv(history %d) error = %v", seq+1, err)
		}
		if event.Cursor.Seq != uint64(seq+1) || event.Body.Kind() != kind {
			t.Fatalf("history event %d = %#v, want kind %s", seq+1, event, kind)
		}
	}
	if _, err := history.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv(history EOF) error = %v, want io.EOF", err)
	}

	second, err := restored.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-2"}, func(context.Context, *agentstream.Turn) error { return nil })
	if err != nil {
		t.Fatalf("StartTurn(second) error = %v", err)
	}
	if second.Ref().Seq != 2 {
		t.Fatalf("second turn seq = %d, want 2", second.Ref().Seq)
	}
	if got := second.StartedEvent().Cursor; got != (agentstream.Cursor{Epoch: "epoch-1", Seq: 4}) {
		t.Fatalf("second start cursor = %#v, want epoch-1/4", got)
	}
}

func TestOpenRestoresUnfinishedTurnWithoutRedispatch(t *testing.T) {
	t.Parallel()

	journal := &memoryJournal{}
	first, err := agentstream.Open(context.Background(), agentstream.Config{SessionID: "session-1", Epoch: "epoch-1", Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	dispatches := 0
	if _, err := first.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(ctx context.Context, turn *agentstream.Turn) error {
		dispatches++
		_, err := turn.Emit(ctx, agentstream.AssistantDelta{Text: "durable"}, agentstream.Origin{Actor: agentstream.ActorAgent, TransportSeq: 7})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := agentstream.Open(context.Background(), agentstream.Config{SessionID: "session-1", Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	if dispatches != 1 {
		t.Fatalf("restore redispatched provider: dispatches=%d", dispatches)
	}
	if _, err := restored.StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-2"}, func(context.Context, *agentstream.Turn) error {
		dispatches++
		return nil
	}); !errors.Is(err, agentstream.ErrTurnActive) {
		t.Fatalf("StartTurn while restored turn active error = %v, want ErrTurnActive", err)
	}
	active := restored.ActiveTurn()
	if active == nil || active.Ref() != (agentstream.TurnRef{ID: "turn-1", Seq: 1}) {
		t.Fatalf("ActiveTurn() = %#v, want restored turn-1", active)
	}
	if active.LastTransportSeq() != 7 {
		t.Fatalf("LastTransportSeq() = %d, want durable broker sequence 7", active.LastTransportSeq())
	}
	if _, err := active.Finish(context.Background(), agentstream.Terminal{Outcome: agentstream.OutcomeInterrupted, Code: "runtime_restart"}, agentstream.Origin{Actor: agentstream.ActorSystem}); err != nil {
		t.Fatal(err)
	}
	reopened, err := agentstream.Open(context.Background(), agentstream.Config{SessionID: "session-1", Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ActiveTurn() != nil {
		t.Fatal("finished recovered turn became active again")
	}
	if dispatches != 1 {
		t.Fatalf("reopen redispatched provider: dispatches=%d", dispatches)
	}
}

func TestTimelineReadModesShareOneCursorWithoutGapsOrDuplicates(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{SessionID: "session-1", Epoch: "epoch-1", MailboxSize: 8})
	for _, message := range []string{"one", "two"} {
		if _, err := timeline.Publish(context.Background(), agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Message: message}, agentstream.Origin{}); err != nil {
			t.Fatal(err)
		}
	}
	afterFirst := agentstream.Cursor{Epoch: "epoch-1", Seq: 1}

	_, history, err := timeline.Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadHistory, After: afterFirst})
	if err != nil {
		t.Fatalf("Read(history) error = %v", err)
	}
	event, err := history.Recv(context.Background())
	if err != nil || event.Cursor.Seq != 2 {
		t.Fatalf("history first = (%#v, %v), want seq 2", event, err)
	}
	if _, err := history.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("history EOF error = %v", err)
	}
	history.Close()

	followHead, follow, err := timeline.Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadFollow, After: afterFirst})
	if err != nil {
		t.Fatalf("Read(follow) error = %v", err)
	}
	t.Cleanup(follow.Close)
	if followHead.Cursor.Seq != 2 {
		t.Fatalf("follow head seq = %d, want 2", followHead.Cursor.Seq)
	}
	if _, err := timeline.Publish(context.Background(), agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Message: "three"}, agentstream.Origin{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint64{2, 3} {
		event, err := follow.Recv(context.Background())
		if err != nil || event.Cursor.Seq != want {
			t.Fatalf("follow Recv() = (%#v, %v), want seq %d", event, err, want)
		}
	}

	tailHead, tail, err := timeline.Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadTail})
	if err != nil {
		t.Fatalf("Read(tail) error = %v", err)
	}
	t.Cleanup(tail.Close)
	if tailHead.Cursor.Seq != 3 {
		t.Fatalf("tail head seq = %d, want 3", tailHead.Cursor.Seq)
	}
	if _, err := timeline.Publish(context.Background(), agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Message: "four"}, agentstream.Origin{}); err != nil {
		t.Fatal(err)
	}
	event, err = tail.Recv(context.Background())
	if err != nil || event.Cursor.Seq != 4 {
		t.Fatalf("tail Recv() = (%#v, %v), want seq 4", event, err)
	}
}

func TestFollowBacklogLargerThanMailboxDoesNotLagOrGap(t *testing.T) {
	t.Parallel()

	const half = 300
	timeline := agentstream.New(agentstream.Config{SessionID: "session-1", Epoch: "epoch-1", MailboxSize: 1})
	for i := 0; i < half; i++ {
		if _, err := timeline.Publish(context.Background(), agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Message: "before"}, agentstream.Origin{}); err != nil {
			t.Fatal(err)
		}
	}
	_, follow, err := timeline.Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadFollow})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(follow.Close)
	for i := 0; i < half; i++ {
		if _, err := timeline.Publish(context.Background(), agentstream.SessionDiagnostic{Severity: agentstream.SeverityInfo, Message: "after"}, agentstream.Origin{}); err != nil {
			t.Fatal(err)
		}
	}
	for want := uint64(1); want <= 2*half; want++ {
		event, err := follow.Recv(context.Background())
		if err != nil || event.Cursor.Seq != want {
			t.Fatalf("Recv(%d) = (%#v, %v)", want, event, err)
		}
	}
}

func TestTimelineRejectsInvalidCursor(t *testing.T) {
	t.Parallel()

	timeline := agentstream.New(agentstream.Config{SessionID: "session-1", Epoch: "epoch-1"})
	if _, err := timeline.Publish(context.Background(), agentstream.CommandsReplaced{}, agentstream.Origin{}); err != nil {
		t.Fatal(err)
	}
	for _, after := range []agentstream.Cursor{
		{Epoch: "other-epoch", Seq: 1},
		{Epoch: "epoch-1", Seq: 2},
		{Seq: 1},
	} {
		if _, _, err := timeline.Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadHistory, After: after}); !errors.Is(err, agentstream.ErrInvalidCursor) {
			t.Fatalf("Read(after=%#v) error = %v, want ErrInvalidCursor", after, err)
		}
	}
}

func TestTimelineAppendFailureDoesNotPublishOrConsumeSequence(t *testing.T) {
	t.Parallel()

	appendFailure := errors.New("disk full")
	journal := &memoryJournal{appendErr: appendFailure}
	timeline, err := agentstream.Open(context.Background(), agentstream.Config{SessionID: "session-1", Epoch: "epoch-1", Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	_, tail, err := timeline.Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadTail})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tail.Close)
	if _, err := timeline.Publish(context.Background(), agentstream.CommandsReplaced{}, agentstream.Origin{}); !errors.Is(err, appendFailure) {
		t.Fatalf("Publish(failing journal) error = %v, want disk full", err)
	}
	head, probe, err := timeline.Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadHistory})
	if err != nil {
		t.Fatal(err)
	}
	if head.Cursor.Seq != 0 {
		t.Fatalf("head seq after failed append = %d, want 0", head.Cursor.Seq)
	}
	if _, err := probe.Recv(context.Background()); !errors.Is(err, agentstream.ErrJournalFault) {
		t.Fatalf("history terminal error = %v, want ErrJournalFault", err)
	}
	probe.Close()
	if _, err := tail.Recv(context.Background()); !errors.Is(err, agentstream.ErrJournalFault) || !errors.Is(err, appendFailure) {
		t.Fatalf("tail Recv() error = %v, want durable fault with no visible event", err)
	}

	journal.mu.Lock()
	journal.appendErr = nil
	journal.mu.Unlock()
	if _, err := timeline.Publish(context.Background(), agentstream.CommandsReplaced{}, agentstream.Origin{}); !errors.Is(err, agentstream.ErrJournalFault) {
		t.Fatalf("Publish(after fault) error = %v, want ErrJournalFault", err)
	}
}
