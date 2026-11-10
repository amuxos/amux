package session

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/replay"
)

type recordingProc struct {
	mu           sync.Mutex
	writes       [][]byte
	controls     []string
	interrupts   int
	interruptErr error
	closeErr     error
	onClose      func()
	writeErr     error
	onWrite      func()
	onWriteData  func([]byte)
	autoTurnDone bool
	eventSink    func(agentstream.ProducerEmission) bool
	turnSink     func(agentstream.ProducerEmission) bool
}

func captureEventTap(s *Session) <-chan agentstream.Event {
	events := make(chan agentstream.Event, 256)
	s.SetEventTap(func(ev agentstream.Event) bool {
		events <- ev
		return false
	})
	return events
}

func testAssistantDelta(text string) agentstream.ProducerEmission {
	return agentstream.ProducerEmission{Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent"}, Turn: agentstream.AssistantDelta{Text: text}}
}

func testResult(text string) agentstream.ProducerEmission {
	return agentstream.ProducerEmission{Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "result"}, Turn: agentstream.ResultObserved{Text: text}}
}

func testTerminal(outcome agentstream.Outcome, code, message string) agentstream.ProducerEmission {
	return agentstream.ProducerEmission{Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent"}, Terminal: &agentstream.Terminal{Outcome: outcome, Code: code, Message: message}}
}

func testSuccess() agentstream.ProducerEmission {
	return testTerminal(agentstream.OutcomeSuccess, "completed", "")
}

func TestKillRecordsIntentionalCanonicalInterruptionOnlyAfterCloseSucceeds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		closeErr  error
		wantCode  string
		wantAlive bool
	}{
		{name: "closed", wantCode: "session_closed"},
		{name: "close failed", closeErr: errors.New("still running"), wantAlive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{ID: "session-1", TimelineEpoch: "epoch-1", Replay: replayBufferForTest()}
			proc := &correlatedRecordingProc{recordingProc: recordingProc{closeErr: tc.closeErr}}
			proc.dispatch = func(context.Context, TurnDispatchRequest, func(agentstream.ProducerEmission) bool) error { return nil }
			s.SetProc(proc)
			generation := s.ProcessGeneration()
			processExitRecorded := make(chan bool, 1)
			if tc.closeErr == nil {
				proc.onClose = func() {
					go func() {
						processExitRecorded <- s.FailActiveTurnForProcess(generation, "process_exit")
					}()
				}
			}
			if _, err := s.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error { return nil }); err != nil {
				t.Fatal(err)
			}
			err := s.Kill()
			if !errors.Is(err, tc.closeErr) {
				t.Fatalf("Kill() error = %v, want %v", err, tc.closeErr)
			}
			active := s.AgentTimeline().ActiveTurn()
			if tc.closeErr == nil && <-processExitRecorded {
				t.Fatal("intentional Close raced into process_exit terminal")
			}
			if tc.wantAlive {
				if active == nil || s.AgentTimeline().Head().Cursor.Seq != 1 {
					t.Fatalf("failed Close advanced canonical turn: active=%v head=%#v", active, s.AgentTimeline().Head())
				}
				return
			}
			if active != nil {
				t.Fatal("successful Close left canonical turn active")
			}
			_, history, readErr := s.AgentTimeline().Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadHistory, After: agentstream.Cursor{Epoch: "epoch-1", Seq: 1}})
			if readErr != nil {
				t.Fatal(readErr)
			}
			terminal, recvErr := history.Recv(context.Background())
			if recvErr != nil {
				t.Fatal(recvErr)
			}
			body := terminal.Body.(agentstream.TurnFinished)
			if body.Terminal.Outcome != agentstream.OutcomeInterrupted || body.Terminal.Code != tc.wantCode {
				t.Fatalf("intentional Close terminal = %#v", body.Terminal)
			}
		})
	}
}

func TestOldProcessExitCannotFinishReplacementTurn(t *testing.T) {
	s := &Session{ID: "session-1", TimelineEpoch: "epoch-1", Replay: replayBufferForTest()}
	first := &correlatedRecordingProc{}
	first.dispatch = func(context.Context, TurnDispatchRequest, func(agentstream.ProducerEmission) bool) error { return nil }
	s.SetProc(first)
	oldGeneration := s.ProcessGeneration()
	second := &correlatedRecordingProc{}
	second.dispatch = func(context.Context, TurnDispatchRequest, func(agentstream.ProducerEmission) bool) error { return nil }
	s.SetProc(second)
	if _, err := s.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-2"}, func(context.Context, *agentstream.Turn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if s.FailActiveTurnForProcess(oldGeneration, "process_exit") {
		t.Fatal("old process exit finished the replacement turn")
	}
	if active := s.AgentTimeline().ActiveTurn(); active == nil || active.Ref().ID != "turn-2" {
		t.Fatalf("replacement active turn = %#v", active)
	}
}

type multipartRecordingProc struct {
	recordingProc
	input UserInput
}

type correlatedRecordingProc struct {
	recordingProc
	dispatch func(context.Context, TurnDispatchRequest, func(agentstream.ProducerEmission) bool) error
	rebind   func(string, uint64, func(agentstream.ProducerEmission) bool) error
}

func (p *correlatedRecordingProc) DispatchTurn(ctx context.Context, req TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
	if p.dispatch == nil {
		return errors.New("missing correlated dispatch")
	}
	return p.dispatch(ctx, req, sink)
}

func (p *correlatedRecordingProc) RebindTurn(turnToken string, afterTransportSeq uint64, sink func(agentstream.ProducerEmission) bool) error {
	if p.rebind == nil {
		return errors.New("missing correlated rebind")
	}
	return p.rebind(turnToken, afterTransportSeq, sink)
}

func TestRecoveredCanonicalTurnRebindsWithoutRedispatch(t *testing.T) {
	s := &Session{ID: "session-1", TimelineEpoch: "epoch-1", Replay: replayBufferForTest()}
	dispatches := 0
	turn, err := s.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		dispatches++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Emit(context.Background(), agentstream.AssistantDelta{Text: "before restart"}, agentstream.Origin{TransportSeq: 4}); err != nil {
		t.Fatal(err)
	}
	proc := &correlatedRecordingProc{}
	proc.rebind = func(token string, afterTransportSeq uint64, sink func(agentstream.ProducerEmission) bool) error {
		if token != turn.Ref().ID {
			t.Fatalf("rebound token = %q, want %q", token, turn.Ref().ID)
		}
		if afterTransportSeq != 4 {
			t.Fatalf("rebind after sequence = %d, want durable sequence 4", afterTransportSeq)
		}
		continued := testAssistantDelta("continued")
		continued.Origin.TransportSeq = 5
		sink(continued)
		terminal := testSuccess()
		terminal.Origin.TransportSeq = 6
		sink(terminal)
		return nil
	}
	s.SetProc(proc)
	if err := s.RebindRecoveredTurn(); err != nil {
		t.Fatal(err)
	}
	select {
	case completion := <-turn.Done():
		if completion.Terminal.Outcome != agentstream.OutcomeSuccess {
			t.Fatalf("completion = %#v", completion)
		}
	case <-time.After(time.Second):
		t.Fatal("rebound terminal did not finish the recovered turn")
	}
	if dispatches != 1 {
		t.Fatalf("rebind redispatched provider input: %d dispatches", dispatches)
	}
	if head := s.AgentTimeline().Head(); head.Cursor.Seq != 4 {
		t.Fatalf("rebound timeline head = %#v, want start + persisted delta + replay delta + terminal", head.Cursor)
	}
}

func (p *multipartRecordingProc) SubmitInput(input UserInput) error {
	p.input = input
	if p.eventSink != nil {
		p.eventSink(testSuccess())
	}
	return nil
}

func (p *multipartRecordingProc) DispatchTurn(_ context.Context, req TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
	p.input = req.Input
	p.turnSink = sink
	if p.autoTurnDone {
		sink(testSuccess())
	}
	return p.writeErr
}

func (p *multipartRecordingProc) InputCapabilities() ([]string, string) {
	return []string{"text", "image"}, "test"
}

func TestSubmitMultipartIsOneTurnAndReplayContainsOnlySummary(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &multipartRecordingProc{}
	s.SetProc(proc)
	secret := []byte("image-secret-bytes")
	input := UserInput{Parts: []InputPart{
		{Text: "inspect these"},
		{Image: &ImageInput{Data: secret, MIMEType: "image/png", DisplayName: "secret.png"}},
		{Image: &ImageInput{Data: secret, MIMEType: "image/png"}},
	}}
	item, err := s.SubmitInput(context.Background(), "lark", input)
	if err != nil || item.TurnID == "" {
		t.Fatalf("SubmitInput item=%+v err=%v", item, err)
	}
	if proc.input.ImageCount() != 2 || proc.input.Text() != "inspect these" || len(proc.writes) != 0 {
		t.Fatalf("provider input=%+v writes=%q", proc.input, proc.writes)
	}
	replayed := bytes.Join(s.Replay.Snapshot(), nil)
	if !bytes.Contains(replayed, []byte("inspect these\n[2 images]")) || bytes.Contains(replayed, secret) || bytes.Contains(replayed, []byte("secret.png")) {
		t.Fatalf("unsafe replay = %q", replayed)
	}
	snap := s.Snapshot()
	if got := strings.Join(snap.InputModalities, ","); got != "text,image" || snap.InputCapabilitySource != "test" {
		t.Fatalf("snapshot modalities=%q source=%q", got, snap.InputCapabilitySource)
	}
}

func TestSubmitTextUsesAtomicInputWhenProviderSupportsIt(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &multipartRecordingProc{recordingProc: recordingProc{autoTurnDone: true}}
	s.SetProc(proc)

	input := TextUserInput("first line\nsecond line")
	if _, err := s.SubmitInput(context.Background(), "lark", input); err != nil {
		t.Fatalf("SubmitInput: %v", err)
	}
	if got := proc.input.Text(); got != "first line\nsecond line" {
		t.Fatalf("provider input = %q, want one complete submission", got)
	}
	if writes := proc.snapshot(); len(writes) != 0 {
		t.Fatalf("provider byte writes = %q, want none", writes)
	}
}

func TestCorrelatedDispatchFailureEmitsTerminalAndCompletesBarrierBeforeNextTurn(t *testing.T) {
	s := &Session{ID: "session-1", Replay: replayBufferForTest()}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var dispatchMu sync.Mutex
	dispatchCount := 0
	barrierWasClean := false
	proc := &correlatedRecordingProc{}
	proc.dispatch = func(_ context.Context, req TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
		dispatchMu.Lock()
		dispatchCount++
		current := dispatchCount
		dispatchMu.Unlock()
		switch current {
		case 1:
			if req.TurnToken == "" || req.Input.Text() != "first" {
				return errors.New("invalid first dispatch")
			}
			sink(agentstream.ProducerEmission{
				Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Provider: "test", NativeType: "permission_requested"},
				Turn: agentstream.PermissionRequested{Request: agentstream.PermissionRequest{
					ID: "perm-1", Provider: "test",
				}},
			})
			close(firstStarted)
			<-releaseFirst
			return errors.New("provider unavailable")
		case 2:
			s.activityMu.Lock()
			barrierWasClean = len(s.pendingPermissions) == 0
			s.activityMu.Unlock()
			close(secondStarted)
			sink(testSuccess())
			return nil
		default:
			return errors.New("unexpected dispatch")
		}
	}
	s.SetProc(proc)
	sub := captureEventTap(s)

	type submitResult struct {
		item QueueItem
		err  error
	}
	firstResult := make(chan submitResult, 1)
	go func() {
		item, err := s.SubmitInputWithAuthorization(context.Background(), "cli", TextUserInput("first"), TurnAuthorization{LocalExpertAuthoring: true})
		firstResult <- submitResult{item: item, err: err}
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("correlated first turn was not dispatched")
	}
	second, err := s.SubmitInputAsync("cli", TextUserInput("second"))
	if err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	first := <-firstResult
	if first.err == nil || !strings.Contains(first.err.Error(), "provider unavailable") {
		t.Fatalf("first dispatch error = %v", first.err)
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second turn did not advance after failed first turn")
	}
	if !barrierWasClean {
		t.Fatal("next turn started before pending permission was cleared")
	}

	deadline := time.After(time.Second)
	var failedTerminal bool
	for !failedTerminal {
		select {
		case ev := <-sub:
			if ev.Turn == nil || ev.Turn.ID != first.item.TurnID {
				continue
			}
			finished, ok := ev.Body.(agentstream.TurnFinished)
			if !ok {
				continue
			}
			if finished.Terminal.Outcome != agentstream.OutcomeError || !strings.Contains(finished.Terminal.Message, "provider unavailable") {
				t.Fatalf("failed terminal = %+v", ev)
			}
			failedTerminal = true
		case <-deadline:
			t.Fatal("dispatch failure produced no observable terminal")
		}
	}
	if first.item.TurnID == "" || second.TurnID == "" || first.item.TurnID == second.TurnID {
		t.Fatalf("turn identities first=%q second=%q", first.item.TurnID, second.TurnID)
	}
}

func TestCorrelatedTerminalIsExactlyOnceAndLateEmissionCannotLeak(t *testing.T) {
	s := &Session{ID: "session-1", Replay: replayBufferForTest()}
	proc := &correlatedRecordingProc{}
	proc.dispatch = func(_ context.Context, _ TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
		sink(testAssistantDelta("answer"))
		sink(testSuccess())
		sink(testSuccess())
		sink(testAssistantDelta("late"))
		return nil
	}
	s.SetProc(proc)
	sub := captureEventTap(s)
	item, err := s.SubmitInput(context.Background(), "cli", TextUserInput("hello"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	terminals := 0
	late := false
	for {
		select {
		case ev := <-sub:
			if ev.Turn == nil || ev.Turn.ID != item.TurnID {
				continue
			}
			if _, ok := ev.Body.(agentstream.TurnFinished); ok {
				terminals++
			}
			if delta, ok := ev.Body.(agentstream.AssistantDelta); ok && delta.Text == "late" {
				late = true
			}
		case <-time.After(30 * time.Millisecond):
			if terminals != 1 || late {
				t.Fatalf("terminals=%d late=%v", terminals, late)
			}
			return
		case <-deadline:
			t.Fatal("timed out collecting correlated events")
		}
	}
}

func TestCapturedTurnSinkCannotMisbindLatePreviousTurnEventToNextTurn(t *testing.T) {
	s := &Session{ID: "session-1", Replay: replayBufferForTest()}
	var sinkA, sinkB func(agentstream.ProducerEmission) bool
	bStarted := make(chan struct{})
	proc := &correlatedRecordingProc{}
	dispatches := 0
	proc.dispatch = func(_ context.Context, _ TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
		dispatches++
		if dispatches == 1 {
			sinkA = sink
			sink(testSuccess())
			return nil
		}
		sinkB = sink
		close(bStarted)
		return nil
	}
	s.SetProc(proc)
	sub := captureEventTap(s)

	first, err := s.SubmitInput(context.Background(), "cli", TextUserInput("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SubmitInputAsync("cli", TextUserInput("second"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-bStarted:
	case <-time.After(time.Second):
		t.Fatal("second turn did not start")
	}
	if sinkA == nil || sinkB == nil {
		t.Fatal("turn sinks were not captured")
	}

	// A provider/broker that captured A's capability may still call it late.
	// Timeline must reject it as belonging to the finished A, never reinterpret
	// it as an event for the currently-active B.
	sinkA(testAssistantDelta("late-a"))
	sinkB(testAssistantDelta("answer-b"))
	sinkB(testSuccess())

	deadline := time.After(time.Second)
	sawB := false
	for {
		select {
		case ev := <-sub:
			delta, isDelta := ev.Body.(agentstream.AssistantDelta)
			if isDelta && delta.Text == "late-a" {
				t.Fatalf("late A event leaked as turn %+v (A=%q B=%q)", ev.Turn, first.TurnID, second.TurnID)
			}
			if isDelta && delta.Text == "answer-b" {
				if ev.Turn == nil || ev.Turn.ID != second.TurnID {
					t.Fatalf("B output turn = %+v, want %q", ev.Turn, second.TurnID)
				}
				sawB = true
			}
			_, finished := ev.Body.(agentstream.TurnFinished)
			if sawB && ev.Turn != nil && ev.Turn.ID == second.TurnID && finished {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for second turn completion")
		}
	}
}

func TestProviderFixturesConvergeOnCanonicalTurnSequence(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		provider string
		terminal agentstream.ProducerEmission
	}{
		{name: "claude", provider: "claude", terminal: testSuccess()},
		{name: "codex", provider: "codex", terminal: testSuccess()},
		{name: "acp", provider: "demo-agent", terminal: testSuccess()},
		{name: "pi", provider: "pi", terminal: testSuccess()},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := &Session{ID: "session-" + fixture.name, Replay: replayBufferForTest()}
			_, stream, err := s.AgentTimeline().Subscribe(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			proc := &correlatedRecordingProc{}
			proc.dispatch = func(_ context.Context, _ TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
				delta := testAssistantDelta("answer")
				delta.Origin.Provider = fixture.provider
				result := testResult("answer")
				result.Origin.Provider = fixture.provider
				sink(delta)
				sink(result)
				sink(fixture.terminal)
				return nil
			}
			s.SetProc(proc)
			item, err := s.SubmitInput(context.Background(), "cli", TextUserInput("question"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			want := []agentstream.Kind{
				agentstream.KindTurnStarted,
				agentstream.KindAssistantDelta,
				agentstream.KindResultObserved,
				agentstream.KindTurnFinished,
			}
			for i, kind := range want {
				ev, err := stream.Recv(ctx)
				if err != nil {
					t.Fatalf("event %d: %v", i, err)
				}
				if ev.Body.Kind() != kind || ev.Turn == nil || ev.Turn.ID != item.TurnID {
					t.Fatalf("event %d = kind %q turn %+v, want %q/%q", i, ev.Body.Kind(), ev.Turn, kind, item.TurnID)
				}
			}
		})
	}
}

func TestInvalidProducerPayloadIsRejectedBeforeCanonicalJournal(t *testing.T) {
	s := &Session{ID: "session-1", Replay: replayBufferForTest()}
	_, stream, err := s.AgentTimeline().Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	proc := &correlatedRecordingProc{}
	proc.dispatch = func(_ context.Context, _ TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
		sink(agentstream.ProducerEmission{Turn: agentstream.PermissionRequested{}})
		sink(testSuccess())
		return nil
	}
	s.SetProc(proc)
	if _, err := s.SubmitInput(context.Background(), "cli", TextUserInput("question")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i, want := range []agentstream.Kind{agentstream.KindTurnStarted, agentstream.KindTurnFinished} {
		ev, err := stream.Recv(ctx)
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if ev.Body.Kind() != want {
			t.Fatalf("event %d kind = %q, want %q", i, ev.Body.Kind(), want)
		}
	}
}

func TestCorrelatedMetadataPublishesCompleteStickySnapshot(t *testing.T) {
	s := &Session{ID: "session-1", Replay: replayBufferForTest()}
	proc := &correlatedRecordingProc{}
	s.SetProc(proc)
	proc.eventSink(agentstream.ProducerEmission{Session: agentstream.SessionMetadataChanged{AgentSessionID: "native-session", EffectiveModel: "model-v2"}})

	head, sub, err := s.AgentTimeline().Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if head.Metadata == nil || head.Metadata.AgentSessionID != "native-session" || head.Metadata.EffectiveModel != "model-v2" {
		t.Fatalf("sticky metadata = %+v", head.Metadata)
	}
}

func TestLocalExpertAuthoringGrantBindsOnlyToActiveSubmittedTurn(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &recordingProc{}
	s.SetProc(proc)
	item, err := s.SubmitInputAsyncWithAuthorization("lark", TextUserInput("edit local expert"), TurnAuthorization{LocalExpertAuthoring: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		turnID, authorization, active := s.ActiveTurnAuthorization()
		if active {
			if turnID != item.TurnID || !authorization.LocalExpertAuthoring {
				t.Fatalf("active turn = %q %+v, want owner grant on %q", turnID, authorization, item.TurnID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authorized turn did not become active")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	turnID, authorization, active := s.ActiveTurnAuthorization()
	if !active || turnID != item.TurnID || authorization.LocalExpertAuthoring {
		t.Fatalf("interrupt did not revoke active grant: active=%v turn=%q auth=%+v", active, turnID, authorization)
	}
	if !proc.finishTurn() {
		t.Fatal("authorized turn did not accept terminal")
	}
	deadline = time.Now().Add(time.Second)
	for {
		turnID, authorization, active = s.ActiveTurnAuthorization()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal turn retained authorization: active=%v turn=%q auth=%+v", active, turnID, authorization)
		}
		time.Sleep(time.Millisecond)
	}

	plain, err := s.SubmitInputAsync("cli", TextUserInput("ordinary"))
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		turnID, authorization, active := s.ActiveTurnAuthorization()
		if active {
			if turnID != plain.TurnID || authorization.LocalExpertAuthoring {
				t.Fatalf("plain turn inherited grant: turn=%q auth=%+v", turnID, authorization)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("plain turn did not become active")
		}
		time.Sleep(time.Millisecond)
	}
	if !proc.finishTurn() {
		t.Fatal("plain turn did not accept terminal")
	}
}

func (p *recordingProc) PID() int { return 1234 }

func (p *recordingProc) Write(data []byte) (int, error) {
	p.mu.Lock()
	p.writes = append(p.writes, append([]byte(nil), data...))
	p.mu.Unlock()
	if p.onWrite != nil {
		p.onWrite()
	}
	if p.onWriteData != nil {
		p.onWriteData(data)
	}
	return len(data), p.writeErr
}

func TestConversationHandoffIsInjectedOnceAndRestoredAfterWriteFailure(t *testing.T) {
	var persisted []string
	s := &Session{
		Replay:         replayBufferForTest(),
		HandoffContext: "[history]\n",
		persistHandoff: func(value string) error {
			persisted = append(persisted, value)
			return nil
		},
	}
	proc := &recordingProc{writeErr: errors.New("provider unavailable")}
	s.SetProc(proc)
	if _, err := s.SubmitInput(context.Background(), "lark", TextUserInput("continue")); err == nil {
		t.Fatal("first write error = nil")
	}
	if got := s.Snapshot().HandoffContext; got != "[history]\n" {
		t.Fatalf("handoff after failed write = %q", got)
	}
	proc.writeErr = nil
	proc.autoTurnDone = true
	proc.writes = nil
	if _, err := s.SubmitInput(context.Background(), "lark", TextUserInput("continue")); err != nil {
		t.Fatalf("retry write: %v", err)
	}
	writes := proc.snapshot()
	if len(writes) != 2 || string(writes[0]) != "[history]\n\ncontinue" || string(writes[1]) != "\r" {
		t.Fatalf("provider writes = %q", writes)
	}
	if got := s.Snapshot().HandoffContext; got != "" {
		t.Fatalf("handoff after successful write = %q", got)
	}
	proc.writes = nil
	if _, err := s.SubmitInput(context.Background(), "lark", TextUserInput("next")); err != nil {
		t.Fatalf("next write: %v", err)
	}
	if writes := proc.snapshot(); len(writes) != 2 || string(writes[0]) != "next" {
		t.Fatalf("second provider writes = %q, handoff was injected again", writes)
	}
	replayed := string(bytes.Join(s.Replay.Snapshot(), nil))
	if strings.Contains(replayed, "[history]") || strings.Count(replayed, "continue") != 2 || !strings.Contains(replayed, "next") {
		t.Fatalf("display replay = %q", replayed)
	}
	if got := strings.Join(persisted, ","); got != ",[history]\n," {
		t.Fatalf("persisted handoff transitions = %q", got)
	}
}

func (p *recordingProc) Control(action string) error {
	p.mu.Lock()
	p.controls = append(p.controls, action)
	p.mu.Unlock()
	return nil
}

func (p *recordingProc) DispatchTurn(_ context.Context, req TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
	p.mu.Lock()
	p.turnSink = sink
	p.mu.Unlock()
	if req.Control != "" {
		if err := p.Control(req.Control); err != nil {
			return err
		}
	} else {
		if text := req.Input.Text(); text != "" {
			if _, err := p.Write([]byte(text)); err != nil {
				return err
			}
		}
		if _, err := p.Write([]byte("\r")); err != nil {
			return err
		}
	}
	if p.autoTurnDone {
		sink(testSuccess())
	}
	return nil
}

func (p *recordingProc) finishTurn() bool {
	p.mu.Lock()
	sink := p.turnSink
	p.mu.Unlock()
	if sink == nil {
		return false
	}
	sink(testSuccess())
	return true
}

func (p *recordingProc) Close() error {
	if p.onClose != nil {
		p.onClose()
	}
	return p.closeErr
}
func (p *recordingProc) RespondPermission(string, string) error        { return nil }
func (p *recordingProc) RespondPermissionMulti(string, []string) error { return nil }
func (p *recordingProc) SetEventSink(sink func(agentstream.ProducerEmission) bool) {
	p.eventSink = sink
}
func (p *recordingProc) Interrupt() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.interrupts++
	return p.interruptErr
}

func (p *recordingProc) snapshot() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.writes))
	for i := range p.writes {
		out[i] = append([]byte(nil), p.writes[i]...)
	}
	return out
}

func TestSubmitControlAsyncUsesStructuredControlAndPreservesDisplayReplay(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &recordingProc{autoTurnDone: true}
	s.SetProc(proc)
	if err := s.SubmitControlAsync("lark", "/clear", "clear"); err != nil {
		t.Fatalf("SubmitControlAsync: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		proc.mu.Lock()
		controls := append([]string(nil), proc.controls...)
		writes := len(proc.writes)
		proc.mu.Unlock()
		if len(controls) == 1 {
			if controls[0] != "clear" || writes != 0 {
				t.Fatalf("controls=%v writes=%d, want structured clear and no prompt writes", controls, writes)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for structured control")
		}
		time.Sleep(time.Millisecond)
	}
	replayed := s.Replay.Snapshot()
	if len(replayed) != 1 || !bytes.Contains(replayed[0], []byte("/clear")) {
		t.Fatalf("replay = %q, want visible /clear input", replayed)
	}
}

func TestSubmitLifecycleAsyncRunsBeforeOrdinaryPendingInput(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &recordingProc{}
	s.SetProc(proc)

	if err := s.SubmitAsync("lark", "first"); err != nil {
		t.Fatalf("SubmitAsync first: %v", err)
	}
	waitForJoinedWrites(t, proc, "first\r")
	if err := s.SubmitAsync("lark", "queued"); err != nil {
		t.Fatalf("SubmitAsync queued: %v", err)
	}
	lifecycleRan := make(chan struct{})
	item, err := s.SubmitLifecycleAsync("system", "switch workspace", "cwd", func() error {
		if got := joinedWrites(proc.snapshot()); got != "first\r" {
			t.Errorf("writes before lifecycle = %q, want only active turn", got)
		}
		close(lifecycleRan)
		return nil
	})
	if err != nil {
		t.Fatalf("SubmitLifecycleAsync: %v", err)
	}
	items := s.QueueSnapshot()
	if len(items) != 2 || items[0].ID != item.ID || items[0].Control != "cwd" || items[1].Text != "queued" {
		t.Fatalf("queue order = %+v, want cwd lifecycle before queued prompt", items)
	}

	if !proc.finishTurn() {
		t.Fatal("active turn did not accept terminal")
	}
	select {
	case <-lifecycleRan:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lifecycle action")
	}
	waitForJoinedWrites(t, proc, "first\rqueued\r")
	if !proc.finishTurn() {
		t.Fatal("queued turn did not accept terminal")
	}
}

func TestSubmitPreservesMultilineInputAsSingleFrame(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &recordingProc{autoTurnDone: true}
	s.SetProc(proc)
	ch := captureEventTap(s)

	prompt := "[Amux Feishu Thread Mode]\nUse it only as background context.\n[User request]\n修复这个问题"
	if err := s.Submit(context.Background(), "lark", prompt); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	var inputs []agentstream.Event
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if _, ok := ev.Body.(agentstream.TurnStarted); ok {
				inputs = append(inputs, ev)
			}
			if _, ok := ev.Body.(agentstream.TurnFinished); ok {
				goto done
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn_done")
		}
	}

done:
	if len(inputs) != 1 {
		t.Fatalf("input events = %d, want exactly one: %+v", len(inputs), inputs)
	}
	started := inputs[0].Body.(agentstream.TurnStarted)
	if started.InputSummary != prompt {
		t.Fatalf("input summary = %q, want %q", started.InputSummary, prompt)
	}
	if inputs[0].Origin.Channel != "lark" || inputs[0].Turn == nil || inputs[0].Turn.ID == "" || inputs[0].Turn.Seq != 1 {
		t.Fatalf("input metadata = %+v, want lark turn 1", inputs[0])
	}
	wantFrame := "\r\n📱 来自飞书: " + prompt + "\r\n"
	if replayed := string(bytes.Join(s.Replay.Snapshot(), nil)); !strings.Contains(replayed, wantFrame) {
		t.Fatalf("replay = %q, want frame %q", replayed, wantFrame)
	}
	writes := proc.snapshot()
	if len(writes) != 2 || string(writes[0]) != prompt || string(writes[1]) != "\r" {
		t.Fatalf("proc writes = %q, want [%q \\r]", writes, prompt)
	}
}

func TestSubmitAsyncTrackedReturnsExecutedTurnIdentity(t *testing.T) {
	p := &recordingProc{}
	s := &Session{Replay: replayBufferForTest()}
	s.SetProc(p)
	sub := captureEventTap(s)

	item, err := s.SubmitAsyncTracked("task", "inspect")
	if err != nil {
		t.Fatalf("SubmitAsyncTracked: %v", err)
	}
	if item.ID == "" || item.TurnID == "" {
		t.Fatalf("tracked item = %+v, want queue and turn identity", item)
	}
	select {
	case input := <-sub:
		if input.Turn == nil || input.Turn.ID != item.TurnID {
			t.Fatalf("input turn = %+v, want submitted %q", input.Turn, item.TurnID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tracked input")
	}
}

func TestSubmitRejectsMissingProcess(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}

	if err := s.Submit(context.Background(), "lark", "hello"); err == nil {
		t.Fatal("Submit succeeded without a process")
	}
	if err := s.SubmitAsync("lark", "hello"); err == nil {
		t.Fatal("SubmitAsync succeeded without a process")
	}
	if _, err := s.SubmitAsyncTracked("task", "hello"); err == nil {
		t.Fatal("SubmitAsyncTracked succeeded without a process")
	}
}

func TestSubmitAssignsStableCanonicalTurnIdentity(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &recordingProc{autoTurnDone: true}
	s.SetProc(proc)

	first, err := s.SubmitInput(context.Background(), "lark", TextUserInput("first"))
	if err != nil {
		t.Fatalf("first SubmitInput: %v", err)
	}
	second, err := s.SubmitInput(context.Background(), "lark", TextUserInput("second"))
	if err != nil {
		t.Fatalf("second SubmitInput: %v", err)
	}
	if first.TurnID == "" || second.TurnID == "" || first.TurnID == second.TurnID {
		t.Fatalf("turn IDs = %q then %q, want distinct non-empty IDs", first.TurnID, second.TurnID)
	}
}

func TestInterruptCallsProc(t *testing.T) {
	s := &Session{}
	proc := &recordingProc{}
	s.SetProc(proc)

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	proc.mu.Lock()
	defer proc.mu.Unlock()
	if got, want := proc.interrupts, 1; got != want {
		t.Fatalf("interrupts = %d, want %d", got, want)
	}
}

func TestInterruptDoesNotWriteDerivedReplayNotice(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &recordingProc{}
	s.SetProc(proc)

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if replay := s.Replay.Snapshot(); len(replay) != 0 {
		t.Fatalf("interrupt wrote derived replay frames: %q", replay)
	}
}

func TestInterruptPropagatesProcError(t *testing.T) {
	want := errors.New("cannot interrupt")
	s := &Session{}
	proc := &recordingProc{interruptErr: want}
	s.SetProc(proc)

	if got := s.Interrupt(); !errors.Is(got, want) {
		t.Fatalf("Interrupt error = %v, want %v", got, want)
	}
}

func TestSubmitQueuesPendingMessagesWhileTurnIsActive(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	proc := &recordingProc{
		autoTurnDone: true,
		onWriteData: func(data []byte) {
			if string(data) != "\r" {
				return
			}
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		},
	}
	s.SetProc(proc)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- s.Submit(context.Background(), "lark", "first")
	}()
	<-firstCRSeen

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- s.Submit(context.Background(), "cli", "second")
	}()
	waitForQueueLen(t, s, 1)

	items := s.QueueSnapshot()
	if len(items) != 1 || items[0].Text != "second" || items[0].Source != "cli" {
		t.Fatalf("pending items = %+v, want second from cli", items)
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Submit: %v", err)
	}
	if got := len(s.QueueSnapshot()); got != 0 {
		t.Fatalf("queue len after drain = %d, want 0", got)
	}

	writes := proc.snapshot()
	if got, want := joinedWrites(writes), "first\rsecond\r"; got != want {
		t.Fatalf("writes = %q, want %q", got, want)
	}
}

func TestSubmitAsyncReturnsAfterEnqueueWhileTurnIsActive(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	proc := &recordingProc{autoTurnDone: true, onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}}
	s.SetProc(proc)

	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Submit(context.Background(), "cli", "first") }()
	<-firstCRSeen

	if err := s.SubmitAsync("cli", "second"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	items := s.QueueSnapshot()
	if len(items) != 1 || items[0].Text != "second" {
		t.Fatalf("pending items = %+v, want second enqueued", items)
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	waitForQueueLen(t, s, 0)
	waitForJoinedWrites(t, proc, "first\rsecond\r")
}

func TestQueuedInputWaitsForPreviousTurnTerminal(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &recordingProc{}
	s.SetProc(proc)

	if err := s.SubmitAsync("cli", "first"); err != nil {
		t.Fatalf("SubmitAsync first: %v", err)
	}
	waitForJoinedWrites(t, proc, "first\r")
	if err := s.SubmitAsync("cli", "second"); err != nil {
		t.Fatalf("SubmitAsync second: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := joinedWrites(proc.snapshot()); got != "first\r" {
		t.Fatalf("writes before terminal = %q, want only first turn", got)
	}

	if !proc.finishTurn() {
		t.Fatal("first queued turn did not accept terminal")
	}
	waitForJoinedWrites(t, proc, "first\rsecond\r")
	if !proc.finishTurn() {
		t.Fatal("second queued turn did not accept terminal")
	}
}

func TestFailActiveTurnUnblocksQueuedInput(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &recordingProc{}
	s.SetProc(proc)

	if err := s.SubmitAsync("cli", "first"); err != nil {
		t.Fatalf("SubmitAsync first: %v", err)
	}
	waitForJoinedWrites(t, proc, "first\r")
	if err := s.SubmitAsync("cli", "second"); err != nil {
		t.Fatalf("SubmitAsync second: %v", err)
	}

	if !s.FailActiveTurn("process_exit") {
		t.Fatal("FailActiveTurn returned false for an active turn")
	}
	waitForJoinedWrites(t, proc, "first\rsecond\r")
	s.FailActiveTurn("process_exit")
}

func TestQueueClearCancelsPendingMessages(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	s.SetProc(&recordingProc{autoTurnDone: true, onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}})

	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Submit(context.Background(), "cli", "first") }()
	<-firstCRSeen

	secondDone := make(chan error, 1)
	go func() { secondDone <- s.Submit(context.Background(), "cli", "second") }()
	waitForQueueLen(t, s, 1)

	if cleared := s.ClearQueue(); cleared != 1 {
		t.Fatalf("ClearQueue() = %d, want 1", cleared)
	}
	if err := <-secondDone; !errors.Is(err, ErrQueueItemCancelled) {
		t.Fatalf("second Submit error = %v, want ErrQueueItemCancelled", err)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Submit: %v", err)
	}
}

func TestResetForRestartClearsPendingMessages(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	s.SetProc(&recordingProc{autoTurnDone: true, onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}})

	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Submit(context.Background(), "cli", "first") }()
	<-firstCRSeen

	secondDone := make(chan error, 1)
	go func() { secondDone <- s.Submit(context.Background(), "cli", "second") }()
	waitForQueueLen(t, s, 1)

	m := &Manager{}
	m.ResetForRestart(s)

	if got := len(s.QueueSnapshot()); got != 0 {
		t.Fatalf("queue len after restart reset = %d, want 0", got)
	}
	if err := <-secondDone; !errors.Is(err, ErrQueueItemCancelled) {
		t.Fatalf("second Submit error = %v, want ErrQueueItemCancelled", err)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Submit: %v", err)
	}
}

func TestQueueCancelRemovesOnePendingMessage(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	s.SetProc(&recordingProc{autoTurnDone: true, onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}})

	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Submit(context.Background(), "cli", "first") }()
	<-firstCRSeen

	secondDone := make(chan error, 1)
	go func() { secondDone <- s.Submit(context.Background(), "cli", "second") }()
	waitForQueueLen(t, s, 1)
	thirdDone := make(chan error, 1)
	go func() { thirdDone <- s.Submit(context.Background(), "cli", "third") }()
	waitForQueueLen(t, s, 2)

	items := s.QueueSnapshot()
	if ok := s.CancelQueueItem(items[0].ID); !ok {
		t.Fatalf("CancelQueueItem(%q) = false, want true", items[0].ID)
	}
	if err := <-secondDone; !errors.Is(err, ErrQueueItemCancelled) {
		t.Fatalf("second Submit error = %v, want ErrQueueItemCancelled", err)
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Submit: %v", err)
	}
	if err := <-thirdDone; err != nil {
		t.Fatalf("third Submit: %v", err)
	}
}

func TestInterruptPreservesPendingQueue(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	proc := &recordingProc{autoTurnDone: true, onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}}
	s.SetProc(proc)

	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Submit(context.Background(), "cli", "first") }()
	<-firstCRSeen
	secondDone := make(chan error, 1)
	go func() { secondDone <- s.Submit(context.Background(), "cli", "second") }()
	waitForQueueLen(t, s, 1)

	if err := s.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if got := len(s.QueueSnapshot()); got != 1 {
		t.Fatalf("queue len after interrupt = %d, want 1", got)
	}

	close(releaseFirst)
	<-firstDone
	<-secondDone
	if proc.interrupts != 1 {
		t.Fatalf("interrupts = %d, want 1", proc.interrupts)
	}
}

func TestAutoSleepEligibleRequiresResumableSessionIdleAndNoQueue(t *testing.T) {
	now := time.Unix(2000, 0)
	s := &Session{
		AgentType:          "claude",
		State:              StateRunning,
		IdleTimeoutSeconds: 60,
		AgentSessionID:     "native-session",
		LastActivityAt:     now.Add(-61 * time.Second),
	}
	if !s.AutoSleepEligible(now) {
		t.Fatalf("AutoSleepEligible = false, want true for idle resumable claude session")
	}

	s.AgentSessionID = ""
	if s.AutoSleepEligible(now) {
		t.Fatal("AutoSleepEligible = true without native agent session id, want false")
	}
	s.AgentSessionID = "native-session"
	s.IdleTimeoutSeconds = 59
	if s.AutoSleepEligible(now) {
		t.Fatal("AutoSleepEligible = true for idle timeout below minimum, want false")
	}
}

func TestAutoSleepEligibleIsFalseWhileQueueBusy(t *testing.T) {
	now := time.Unix(2000, 0)
	s := &Session{
		AgentType:          "demo-agent",
		State:              StateRunning,
		IdleTimeoutSeconds: 60,
		AgentSessionID:     "native-session",
		LastActivityAt:     now.Add(-61 * time.Second),
		Replay:             replayBufferForTest(),
	}
	release := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	s.SetProc(&recordingProc{autoTurnDone: true, onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-release
			})
		}
	}})

	done := make(chan error, 1)
	go func() { done <- s.Submit(context.Background(), "cli", "first") }()
	<-firstCRSeen

	if s.AutoSleepEligible(now) {
		t.Fatal("AutoSleepEligible = true while queue is busy, want false")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

func replayBufferForTest() *replay.Buffer {
	return replay.New(10)
}

func waitForQueueLen(t *testing.T, s *Session, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if got := len(s.QueueSnapshot()); got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for queue len %d; got %d", want, len(s.QueueSnapshot()))
		case <-tick.C:
		}
	}
}

func waitForJoinedWrites(t *testing.T, proc *recordingProc, want string) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if got := joinedWrites(proc.snapshot()); got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for writes %q; got %q", want, joinedWrites(proc.snapshot()))
		case <-tick.C:
		}
	}
}

func joinedWrites(writes [][]byte) string {
	var b bytes.Buffer
	for _, w := range writes {
		b.Write(w)
	}
	return b.String()
}

func TestSubmitPreemptInterruptsAndJumpsQueue(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	proc := &recordingProc{autoTurnDone: true, onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}}
	s.SetProc(proc)

	// 占住当前 turn。
	if err := s.SubmitAsync("cli", "first"); err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	<-firstCRSeen

	// 排一条普通输入，再 preempt 两条。
	if err := s.SubmitAsync("cli", "queued"); err != nil {
		t.Fatalf("SubmitAsync queued: %v", err)
	}
	if err := s.SubmitPreempt("lark", "pre1"); err != nil {
		t.Fatalf("SubmitPreempt pre1: %v", err)
	}
	if err := s.SubmitPreempt("lark", "pre2"); err != nil {
		t.Fatalf("SubmitPreempt pre2: %v", err)
	}

	items := s.QueueSnapshot()
	if len(items) != 3 || items[0].Text != "pre1" || items[1].Text != "pre2" || items[2].Text != "queued" {
		t.Fatalf("queue order = %+v, want [pre1 pre2 queued]", items)
	}
	proc.mu.Lock()
	interrupts := proc.interrupts
	proc.mu.Unlock()
	if interrupts != 2 {
		t.Fatalf("interrupts = %d, want 2", interrupts)
	}

	close(releaseFirst)

	// 等队列清空，校验执行顺序：first, pre1, pre2, queued（每条 text + \r 各一次 Write）。
	deadline := time.After(2 * time.Second)
	for {
		s.queueMu.Lock()
		busy := s.queueBusy
		s.queueMu.Unlock()
		if !busy {
			break
		}
		select {
		case <-deadline:
			t.Fatal("queue did not drain")
		case <-time.After(10 * time.Millisecond):
		}
	}
	var texts []string
	for _, w := range proc.snapshot() {
		if string(w) != "\r" {
			texts = append(texts, string(w))
		}
	}
	want := []string{"first", "pre1", "pre2", "queued"}
	if len(texts) != len(want) {
		t.Fatalf("writes = %v, want %v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("writes = %v, want %v", texts, want)
		}
	}
}

func TestSubmitPreemptIdleRunsImmediately(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	proc := &recordingProc{autoTurnDone: true}
	s.SetProc(proc)

	if err := s.SubmitPreempt("lark", "hello"); err != nil {
		t.Fatalf("SubmitPreempt: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for {
		if writes := proc.snapshot(); len(writes) == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("writes = %q, want hello+CR", proc.snapshot())
		case <-time.After(10 * time.Millisecond):
		}
	}
	proc.mu.Lock()
	interrupts := proc.interrupts
	proc.mu.Unlock()
	if interrupts != 0 {
		t.Fatalf("interrupts = %d, want 0 (idle preempt must not interrupt)", interrupts)
	}
}

func TestSubmitPreemptAfterInterruptJumpsQueueWithoutInterruptingAgain(t *testing.T) {
	s := &Session{Replay: replayBufferForTest()}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	proc := &recordingProc{autoTurnDone: true, onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}}
	s.SetProc(proc)

	if err := s.SubmitAsync("cli", "first"); err != nil {
		t.Fatalf("SubmitAsync first: %v", err)
	}
	<-firstCRSeen
	if err := s.SubmitAsync("cli", "queued"); err != nil {
		t.Fatalf("SubmitAsync queued: %v", err)
	}
	if err := s.SubmitPreemptAfterInterrupt("lark", "preempted"); err != nil {
		t.Fatalf("SubmitPreemptAfterInterrupt: %v", err)
	}
	items := s.QueueSnapshot()
	if len(items) != 2 || items[0].Text != "preempted" || items[1].Text != "queued" {
		t.Fatalf("queue order = %+v, want [preempted queued]", items)
	}
	proc.mu.Lock()
	interrupts := proc.interrupts
	proc.mu.Unlock()
	if interrupts != 0 {
		t.Fatalf("interrupts = %d, want 0 after caller already interrupted", interrupts)
	}
	close(releaseFirst)
}

func TestLastInteractionAtOnlyRealIO(t *testing.T) {
	s := &Session{}
	base := time.Unix(1000, 0)

	// real agent output → updates
	s.ApplyEventActivity(Event{Kind: EventOutput, Source: "agent", Data: []byte("hi")}, base)
	if got := s.InteractionAt(); !got.Equal(base) {
		t.Fatalf("after agent output InteractionAt=%v want %v", got, base)
	}

	// user input → updates
	in := base.Add(time.Minute)
	s.ApplyEventActivity(Event{Kind: EventOutput, Source: "cli", Input: true, Data: []byte("x")}, in)
	if got := s.InteractionAt(); !got.Equal(in) {
		t.Fatalf("after user input InteractionAt=%v want %v", got, in)
	}

	// system notice (sleep/archive text) → does NOT update
	sys := in.Add(time.Hour)
	s.ApplyEventActivity(Event{Kind: EventOutput, Source: "system", Data: []byte("[system] sleeping session")}, sys)
	if got := s.InteractionAt(); !got.Equal(in) {
		t.Fatalf("system notice must NOT bump InteractionAt: got=%v want %v", got, in)
	}

	// internal state write (MarkActivity) → does NOT update
	s.MarkActivity(sys)
	if got := s.InteractionAt(); !got.Equal(in) {
		t.Fatalf("MarkActivity must NOT bump InteractionAt: got=%v want %v", got, in)
	}
}

func TestPendingPermissionsClearedOnTerminalTurnStates(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
	}{
		{"turn_done", Event{Kind: EventStateChange, State: "turn_done"}},
		{"failed without ProviderEvent", Event{Kind: EventStateChange, State: "failed", StopReason: "turn idle timeout"}},
		{"failed terminal", Event{Kind: EventStateChange, State: "failed", StopReason: "turn hard cap exceeded"}},
		{"interrupted", Event{Kind: EventStateChange, State: "interrupted"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{}
			s.ApplyEventActivity(Event{Kind: EventPermissionRequest, PermissionId: "perm-1"}, time.Unix(1000, 0))
			if got := s.PendingPermissionCount(); got != 1 {
				t.Fatalf("pending permissions = %d, want 1", got)
			}
			s.ApplyEventActivity(tc.ev, time.Unix(1001, 0))
			if got := s.PendingPermissionCount(); got != 0 {
				t.Fatalf("pending permissions after terminal %q = %d, want 0 (leak blocks session reclaim)", tc.name, got)
			}
		})
	}

	// A non-terminal state change must not clear pending permissions.
	s := &Session{}
	s.ApplyEventActivity(Event{Kind: EventPermissionRequest, PermissionId: "perm-1"}, time.Unix(1000, 0))
	s.ApplyEventActivity(Event{Kind: EventStateChange, State: "running"}, time.Unix(1001, 0))
	if got := s.PendingPermissionCount(); got != 1 {
		t.Fatalf("non-terminal state cleared pending permissions: got %d, want 1", got)
	}
}

func TestIsTerminalTurnEventAcceptsBareFailed(t *testing.T) {
	// ACP, watchdog, and process-owner terminal events do not carry
	// ProviderEvent, but must still finish the canonical turn.
	cases := []struct {
		name string
		ev   Event
		want bool
	}{
		{"turn_done bare", Event{Kind: EventStateChange, State: "turn_done"}, true},
		{"failed bare non-provider event", Event{Kind: EventStateChange, State: "failed", StopReason: "turn idle timeout"}, true},
		{"interrupted bare", Event{Kind: EventStateChange, State: "interrupted"}, true},
		{"failed", Event{Kind: EventStateChange, State: "failed"}, true},
		{"running not terminal", Event{Kind: EventStateChange, State: "running"}, false},
		{"output not terminal", Event{Kind: EventOutput, Data: []byte("hi")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTerminalTurnEvent(tc.ev); got != tc.want {
				t.Fatalf("isTerminalTurnEvent(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestTerminalFailedDoesNotBumpInteraction(t *testing.T) {
	s := &Session{}
	base := time.Unix(1000, 0)
	// Real agent output bumps interaction.
	s.ApplyEventActivity(Event{Kind: EventOutput, Source: "agent", Data: []byte("hi")}, base)
	if got := s.InteractionAt(); !got.Equal(base) {
		t.Fatalf("after output InteractionAt=%v want %v", got, base)
	}
	// An autonomous watchdog failure 300s later (terminal turn StateChange) must
	// NOT bump the interaction clock — it is lifecycle bookkeeping, not a
	// genuine interaction, and would otherwise postpone idle reclaim.
	later := base.Add(300 * time.Second)
	s.noteCanonicalActivity(agentstream.Event{
		At:     later,
		Origin: agentstream.Origin{Actor: agentstream.ActorSystem, Channel: "system"},
		Body: agentstream.TurnFinished{Terminal: agentstream.Terminal{
			Outcome: agentstream.OutcomeError,
			Code:    "turn_idle_timeout",
			Message: "turn idle timeout",
		}},
	}, later)
	if got := s.InteractionAt(); !got.Equal(base) {
		t.Fatalf("terminal failed bumped InteractionAt to %v, want unchanged %v", got, base)
	}
}

func TestAsyncQuestionDoesNotBlockIdleReclaim(t *testing.T) {
	s := &Session{}
	for _, delivery := range []string{"async", ""} {
		s.noteCanonicalActivity(agentstream.Event{Body: agentstream.PermissionRequested{Request: agentstream.PermissionRequest{ID: delivery + "question", ToolCall: &agentstream.PermissionToolCall{Question: "Choose", Delivery: delivery}}}}, time.Now())
		want := 0
		if delivery == "" {
			want = 1
		}
		if got := s.PendingPermissionCount(); got != want {
			t.Fatalf("delivery=%q pending=%d", delivery, got)
		}
	}
}

func TestQueuedAsyncAnswerRejectsChangedNativeConversation(t *testing.T) {
	s := &Session{Replay: replayBufferForTest(), AgentSessionID: "native-old"}
	release, started := make(chan struct{}), make(chan struct{})
	var once sync.Once
	proc := &recordingProc{autoTurnDone: true, onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() { close(started); <-release })
		}
	}}
	s.SetProc(proc)
	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Submit(context.Background(), "cli", "first") }()
	<-started
	if _, err := s.SubmitInputAsyncPinned("lark", TextUserInput("old answer"), TurnAuthorization{}, "native-old"); err != nil {
		t.Fatal(err)
	}
	s.runtimeMu.Lock()
	s.AgentSessionID = "native-new"
	s.runtimeMu.Unlock()
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	// A later synchronous submit also proves the rejected answer drained.
	if err := s.Submit(context.Background(), "cli", "next"); err != nil {
		t.Fatal(err)
	}
	if got := joinedWrites(proc.snapshot()); got != "first\rnext\r" {
		t.Fatalf("stale answer reached provider: %q", got)
	}
}
