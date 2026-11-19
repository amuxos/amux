package bridge

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type fakeReplyAPI struct {
	mu         sync.Mutex
	creates    int32
	patches    int32
	lastText   string
	patchTexts []string
	allTexts   []string
	finalByID  map[string]string
	order      []string
	finishes   int32
}

type blockingCreateReplyAPI struct {
	*fakeReplyAPI
	started chan struct{}
	release chan struct{}
}

type failingCreateReplyAPI struct {
	*fakeReplyAPI
	attempts int32
}

func (f *failingCreateReplyAPI) Create(context.Context, string) (*replyCardHandle, error) {
	atomic.AddInt32(&f.attempts, 1)
	return nil, errors.New("create failed")
}

type fakeTurnCardAPI struct {
	*fakeReplyAPI
	activityCreates        int32
	activityUpdates        int32
	legacyCreates          int32
	activityCreateErr      error
	activityUpdateFailures int32
	activityHandles        []string
	activities             []turnCardActivity
	lastActivityHandle     *replyCardHandle
}

func (f *fakeTurnCardAPI) CreateActivity(_ context.Context, activity turnCardActivity) (*replyCardHandle, error) {
	atomic.AddInt32(&f.activityCreates, 1)
	if f.activityCreateErr != nil {
		return nil, f.activityCreateErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "activity-card"
	f.activityHandles = append(f.activityHandles, id)
	f.activities = append(f.activities, activity)
	handle := &replyCardHandle{
		mode:         replyCardModeCardKit,
		cardID:       id,
		messageID:    id,
		hasActivity:  true,
		lastActivity: string(activity.JSON),
	}
	f.lastActivityHandle = handle
	return handle, nil
}

func (f *fakeTurnCardAPI) UpdateActivity(_ context.Context, handle *replyCardHandle, activity turnCardActivity) error {
	if handle == nil || handle.finished {
		return nil
	}
	atomic.AddInt32(&f.activityUpdates, 1)
	if atomic.AddInt32(&f.activityUpdateFailures, -1) >= 0 {
		return errors.New("activity update failed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activityHandles = append(f.activityHandles, handle.messageID)
	f.activities = append(f.activities, activity)
	handle.hasActivity = true
	return nil
}

func (f *fakeTurnCardAPI) CreateLegacy(_ context.Context, text string) (*replyCardHandle, error) {
	n := atomic.AddInt32(&f.legacyCreates, 1)
	id := "legacy-" + strconv.Itoa(int(n))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(id, text)
	return &replyCardHandle{mode: replyCardModeLegacy, messageID: id, lastText: text, hasAnswer: true}, nil
}

func waitForAtomic(t *testing.T, label string, value *int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(value) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s = %d, want %d", label, atomic.LoadInt32(value), want)
}

func (f *blockingCreateReplyAPI) Create(ctx context.Context, text string) (*replyCardHandle, error) {
	close(f.started)
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return f.fakeReplyAPI.Create(ctx, text)
}

func (f *fakeReplyAPI) Create(_ context.Context, text string) (*replyCardHandle, error) {
	n := atomic.AddInt32(&f.creates, 1)
	id := "msg-" + strconv.Itoa(int(n))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastText = text
	f.record(id, text)
	return &replyCardHandle{mode: replyCardModeCardKit, cardID: id, messageID: id, lastText: text}, nil
}

func (f *fakeReplyAPI) Update(_ context.Context, handle *replyCardHandle, text string) error {
	atomic.AddInt32(&f.patches, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastText = text
	f.patchTexts = append(f.patchTexts, text)
	f.record(handle.messageID, text)
	handle.lastText = text
	return nil
}

func (f *fakeReplyAPI) Calibrate(ctx context.Context, handle *replyCardHandle, text string) error {
	return f.Update(ctx, handle, text)
}

func (f *fakeReplyAPI) Finish(_ context.Context, handle *replyCardHandle, text string) error {
	if handle == nil || handle.finished {
		return nil
	}
	atomic.AddInt32(&f.finishes, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastText = text
	f.record(handle.messageID, text)
	handle.lastText = text
	handle.finished = true
	return nil
}

// record tracks the latest text per card id (in first-seen order) and the full
// history of every text sent. Caller must hold f.mu.
func (f *fakeReplyAPI) record(id, text string) {
	f.allTexts = append(f.allTexts, text)
	if f.finalByID == nil {
		f.finalByID = map[string]string{}
	}
	if _, ok := f.finalByID[id]; !ok {
		f.order = append(f.order, id)
	}
	f.finalByID[id] = text
}

// finalsLocked returns the final text of each card in creation order. Caller
// must hold f.mu.
func (f *fakeReplyAPI) finalsLocked() []string {
	out := make([]string, 0, len(f.order))
	for _, id := range f.order {
		out = append(out, f.finalByID[id])
	}
	return out
}

func TestStreamingReplySender_CreatesThenPatches(t *testing.T) {
	t.Parallel()
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(25*time.Millisecond, api, func(t string) { finalText = t })

	s.enqueue(replyCommandAdd, "Hello")
	waitForAtomic(t, "creates", &api.creates, 1)
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 immediately", got)
	}
	// Subsequent deltas within rate window should NOT trigger more calls yet.
	s.enqueue(replyCommandAdd, ", world")
	if got := atomic.LoadInt32(&api.patches); got != 0 {
		t.Fatalf("patches = %d, want 0 during rate window", got)
	}
	// Wait for the rate-limit timer to fire.
	waitForAtomic(t, "patches", &api.patches, 1)
	api.mu.Lock()
	if api.lastText != "Hello, world" {
		t.Fatalf("lastText = %q, want %q", api.lastText, "Hello, world")
	}
	api.mu.Unlock()

	// Flush should bypass rate limit and run onFinal.
	s.enqueue(replyCommandAdd, "!")
	s.Flush()
	api.mu.Lock()
	if api.lastText != "Hello, world!" {
		t.Fatalf("after flush lastText = %q, want %q", api.lastText, "Hello, world!")
	}
	api.mu.Unlock()
	if finalText != "Hello, world!" {
		t.Fatalf("onFinal got %q, want Hello, world!", finalText)
	}

	// Reset → next delta starts a new card (new create).
	s.Reset()
	s.enqueue(replyCommandAdd, "Second turn")
	waitForAtomic(t, "creates after Reset+AddDelta", &api.creates, 2)
}

func TestStreamingReplySender_FinalizeForBotMentionsCallerOnSameCard(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(0, api, func(text string) { finalText = text })
	defer s.Close()

	s.enqueue(replyCommandAdd, "文档检查完成")
	waitForAtomic(t, "creates", &api.creates, 1)
	if delivered := s.FinalizeForBot("ou-calling-bot"); !delivered {
		t.Fatal("FinalizeForBot = false, want same-card Bot mention delivery")
	}

	api.mu.Lock()
	visibleText := api.lastText
	api.mu.Unlock()
	if visibleText != "<at id=\"ou-calling-bot\"></at> 协作结果\n\n文档检查完成" {
		t.Fatalf("visible card text = %q", visibleText)
	}
	if finalText != "文档检查完成" {
		t.Fatalf("logical final text = %q, want undecorated Agent answer", finalText)
	}
}

func TestStreamingReplySender_FinalizeForBotFallsBackForSplitAnswer(t *testing.T) {
	api := &fakeReplyAPI{}
	answer := strings.Repeat("一行足够长的回答\n", 1000)
	s := newStreamingReplySender(0, api, nil)
	defer s.Close()

	s.enqueue(replyCommandAdd, answer)
	if delivered := s.FinalizeForBot("ou-calling-bot"); delivered {
		t.Fatal("FinalizeForBot = true for a split answer, want post fallback")
	}

	api.mu.Lock()
	visibleCards := api.finalsLocked()
	api.mu.Unlock()
	if len(visibleCards) < 2 {
		t.Fatalf("visible cards = %d, want a split answer", len(visibleCards))
	}
	for i, text := range visibleCards {
		if strings.Contains(text, "<at id=") {
			t.Fatalf("card %d unexpectedly contains Bot mention: %q", i, text)
		}
	}
}

func TestStreamingReplySender_SupersedeFreezesOldTurnUntilReset(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)
	defer s.Close()

	s.enqueue(replyCommandAdd, "old")
	waitForAtomic(t, "creates", &api.creates, 1)
	s.Supersede()
	s.enqueue(replyCommandAdd, " stale")
	s.Flush()

	api.mu.Lock()
	oldText := api.lastText
	api.mu.Unlock()
	if oldText != "old" {
		t.Fatalf("old card text = %q, want frozen text %q", oldText, "old")
	}

	s.Reset()
	s.enqueue(replyCommandAdd, "new")
	s.Flush()
	waitForAtomic(t, "creates", &api.creates, 2)
	api.mu.Lock()
	newText := api.lastText
	api.mu.Unlock()
	if newText != "new" {
		t.Fatalf("new card text = %q, want %q", newText, "new")
	}
}

func TestStreamingReplySender_SupersedeAllowsOnlyTerminalActivity(t *testing.T) {
	api := &fakeTurnCardAPI{fakeReplyAPI: &fakeReplyAPI{}}
	s := newStreamingReplySender(0, api, nil)
	defer s.Close()

	if handled, err := s.UpdateActivity(turnCardActivity{JSON: []byte(`{"state":"running"}`)}); !handled || err != nil {
		t.Fatalf("create running activity: handled=%v err=%v", handled, err)
	}
	s.Supersede()
	if handled, err := s.UpdateActivity(turnCardActivity{JSON: []byte(`{"state":"late-running"}`)}); !handled || err != nil {
		t.Fatalf("ignore late running activity: handled=%v err=%v", handled, err)
	}
	if handled, err := s.UpdateActivity(turnCardActivity{JSON: []byte(`{"state":"stopped"}`), Terminal: true}); !handled || err != nil {
		t.Fatalf("patch terminal activity: handled=%v err=%v", handled, err)
	}
	s.Flush()

	if got := atomic.LoadInt32(&api.activityUpdates); got != 1 {
		t.Fatalf("activity updates = %d, want only terminal patch", got)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.activities) != 2 || string(api.activities[1].JSON) != `{"state":"stopped"}` {
		t.Fatalf("activities = %+v, want initial running then stopped", api.activities)
	}
	if api.lastActivityHandle == nil || !api.lastActivityHandle.finished {
		t.Fatalf("terminal activity handle = %+v, want finished after flush", api.lastActivityHandle)
	}
}

func TestStreamingReplySender_ActivityFirstReusesPrimaryCardForAnswer(t *testing.T) {
	api := &fakeTurnCardAPI{fakeReplyAPI: &fakeReplyAPI{}}
	s := newStreamingReplySender(0, api, nil)
	activity := renderTurnActivity(toolActivityCardMeta{Status: "running"})

	handled, err := s.UpdateActivity(activity)
	if err != nil || !handled {
		t.Fatalf("UpdateActivity handled=%v err=%v", handled, err)
	}
	s.enqueue(replyCommandAdd, "answer")
	s.Flush()

	if got := atomic.LoadInt32(&api.activityCreates); got != 1 {
		t.Fatalf("activity creates = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&api.creates); got != 0 {
		t.Fatalf("answer card creates = %d, want 0", got)
	}
	if got := atomic.LoadInt32(&api.patches); got != 1 {
		t.Fatalf("answer patches = %d, want 1 on primary card", got)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.order) != 1 || api.order[0] != "activity-card" || api.finalByID["activity-card"] != "answer" {
		t.Fatalf("cards=%#v final=%#v", api.order, api.finalByID)
	}
}

func TestStreamingReplySender_AnswerFirstAddsActivityToPrimaryCard(t *testing.T) {
	api := &fakeTurnCardAPI{fakeReplyAPI: &fakeReplyAPI{}}
	s := newStreamingReplySender(0, api, nil)
	s.enqueue(replyCommandAdd, "answer")
	waitForAtomic(t, "creates", &api.creates, 1)

	handled, err := s.UpdateActivity(renderTurnActivity(toolActivityCardMeta{Status: "running"}))
	if err != nil || !handled {
		t.Fatalf("UpdateActivity handled=%v err=%v", handled, err)
	}
	s.Flush()

	if got := atomic.LoadInt32(&api.activityCreates); got != 0 {
		t.Fatalf("activity creates = %d, want 0", got)
	}
	if got := atomic.LoadInt32(&api.activityUpdates); got != 1 {
		t.Fatalf("activity updates = %d, want 1", got)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.activityHandles) != 1 || api.activityHandles[0] != "msg-1" {
		t.Fatalf("activity handles = %#v, want primary msg-1", api.activityHandles)
	}
}

func TestStreamingReplySender_LongAnswerKeepsActivityOnFirstCard(t *testing.T) {
	api := &fakeTurnCardAPI{fakeReplyAPI: &fakeReplyAPI{}}
	s := newStreamingReplySender(0, api, nil)
	s.enqueue(replyCommandAdd, strings.Repeat("a", streamingReplyCardSoftLimit+100))
	waitForAtomic(t, "creates", &api.creates, 2)

	if got := atomic.LoadInt32(&api.creates); got != 2 {
		t.Fatalf("creates = %d, want primary plus continuation", got)
	}
	handled, err := s.UpdateActivity(renderTurnActivity(toolActivityCardMeta{Status: "done"}))
	if err != nil || !handled {
		t.Fatalf("UpdateActivity handled=%v err=%v", handled, err)
	}
	s.Flush()
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.activityHandles) != 1 || api.activityHandles[0] != "msg-1" {
		t.Fatalf("activity handles = %#v, want first card msg-1", api.activityHandles)
	}
}

func TestStreamingReplySender_ActivityCreateFailureLocksTurnToLegacy(t *testing.T) {
	api := &fakeTurnCardAPI{
		fakeReplyAPI:      &fakeReplyAPI{},
		activityCreateErr: errors.New("cardkit unavailable"),
	}
	s := newStreamingReplySender(0, api, nil)

	handled, err := s.UpdateActivity(renderTurnActivity(toolActivityCardMeta{Status: "running"}))
	if err == nil || handled {
		t.Fatalf("UpdateActivity handled=%v err=%v, want pre-send failure", handled, err)
	}
	s.enqueue(replyCommandAdd, "legacy answer")
	s.Flush()

	if got := atomic.LoadInt32(&api.creates); got != 0 {
		t.Fatalf("CardKit answer creates = %d, want 0 after activity fallback", got)
	}
	if got := atomic.LoadInt32(&api.legacyCreates); got != 1 {
		t.Fatalf("legacy answer creates = %d, want 1", got)
	}
}

func TestStreamingReplySender_RetriesPendingActivityBeforeFinish(t *testing.T) {
	api := &fakeTurnCardAPI{
		fakeReplyAPI:           &fakeReplyAPI{},
		activityUpdateFailures: 1,
	}
	s := newStreamingReplySender(0, api, nil)
	s.enqueue(replyCommandAdd, "answer")
	waitForAtomic(t, "creates", &api.creates, 1)
	activity := renderTurnActivity(toolActivityCardMeta{Status: "done"})

	handled, err := s.UpdateActivity(activity)
	if err == nil || !handled {
		t.Fatalf("UpdateActivity handled=%v err=%v, want visible-card update failure", handled, err)
	}
	s.Flush()

	if got := atomic.LoadInt32(&api.activityUpdates); got != 2 {
		t.Fatalf("activity updates = %d, want failed update plus finish retry", got)
	}
}

func TestStreamingReplySender_AddDeltaDoesNotBlockOnCardNetwork(t *testing.T) {
	api := &blockingCreateReplyAPI{
		fakeReplyAPI: &fakeReplyAPI{},
		started:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	s := newStreamingReplySender(100*time.Millisecond, api, nil)
	returned := make(chan struct{})
	go func() {
		s.enqueue(replyCommandAdd, "hello")
		close(returned)
	}()

	select {
	case <-api.started:
	case <-time.After(time.Second):
		t.Fatal("CardKit create did not start")
	}
	select {
	case <-returned:
		close(api.release)
	case <-time.After(50 * time.Millisecond):
		close(api.release)
		<-returned
		t.Fatal("AddDelta blocked on CardKit network")
	}
	s.Flush()
	s.Close()
}

func TestStreamingReplySenderThrottlesCreateRetries(t *testing.T) {
	api := &failingCreateReplyAPI{fakeReplyAPI: &fakeReplyAPI{}}
	s := newStreamingReplySender(100*time.Millisecond, api, nil)
	defer s.Close()

	for i := 0; i < 20; i++ {
		s.enqueue(replyCommandAdd, "x")
	}
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&api.attempts); got > 1 {
		t.Fatalf("create attempts within throttle window = %d, want at most 1", got)
	}
}

func TestStreamingReplySender_CoalescesQueuedDeltasAfterSlowCreate(t *testing.T) {
	api := &blockingCreateReplyAPI{
		fakeReplyAPI: &fakeReplyAPI{},
		started:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	s := newStreamingReplySender(100*time.Millisecond, api, nil)
	s.enqueue(replyCommandAdd, "hello")
	select {
	case <-api.started:
	case <-time.After(time.Second):
		t.Fatal("CardKit create did not start")
	}
	s.enqueue(replyCommandAdd, ", ")
	s.enqueue(replyCommandAdd, "world")
	close(api.release)
	s.Flush()

	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&api.patches); got != 1 {
		t.Fatalf("patches = %d, want one coalesced update", got)
	}
	api.mu.Lock()
	lastText := api.lastText
	api.mu.Unlock()
	if lastText != "hello, world" {
		t.Fatalf("lastText = %q, want hello, world", lastText)
	}
	s.Close()
}

func TestStreamingReplySender_DropsEmptyAndDuplicate(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(10*time.Millisecond, api, nil)

	s.enqueue(replyCommandAdd, "")
	if atomic.LoadInt32(&api.creates) != 0 {
		t.Fatalf("empty delta should not trigger create")
	}
	s.enqueue(replyCommandAdd, "same")
	waitForAtomic(t, "creates", &api.creates, 1)
	// Second flush with no new text should not patch.
	s.Flush()
	if atomic.LoadInt32(&api.patches) != 0 {
		t.Fatalf("patches = %d, want 0 (no new text)", atomic.LoadInt32(&api.patches))
	}
}

func TestStreamingReplySender_FinalizesOnceAcrossFlushResetAndClose(t *testing.T) {
	api := &fakeReplyAPI{}
	finalCalls := 0
	s := newStreamingReplySender(time.Hour, api, func(string) { finalCalls++ })

	s.enqueue(replyCommandAdd, "answer")
	s.Flush()
	s.Reset()
	s.Close()

	if got := atomic.LoadInt32(&api.finishes); got != 1 {
		t.Fatalf("finishes = %d, want 1", got)
	}
	if finalCalls != 1 {
		t.Fatalf("onFinal calls = %d, want 1", finalCalls)
	}
}

func TestStreamingReplySender_ResetFinalizesOpenCard(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(time.Hour, api, func(text string) { finalText = text })

	s.enqueue(replyCommandAdd, "answer")
	s.Reset()

	if got := atomic.LoadInt32(&api.finishes); got != 1 {
		t.Fatalf("finishes = %d, want 1", got)
	}
	if finalText != "answer" {
		t.Fatalf("onFinal = %q, want answer", finalText)
	}
}

func TestStreamingReplySender_DropsWhitespaceOnlyDeltaBeforeSegment(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)

	s.enqueue(replyCommandAdd, " \t\r\n")
	s.Segment()
	s.enqueue(replyCommandAdd, "visible answer")
	s.Flush()

	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 (only the visible answer card)", got)
	}
	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "visible answer" {
		t.Fatalf("final cards = %#v, want only visible answer", finals)
	}
}

func TestStreamingReplySender_ReplaceCurrentUsesFinalText(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(0, api, func(t string) { finalText = t })

	s.enqueue(replyCommandAdd, "part")
	s.ReplaceCurrent("complete")
	s.Flush()

	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1", got)
	}
	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "complete" {
		t.Fatalf("final cards = %#v, want [complete]", finals)
	}
	if finalText != "complete" {
		t.Fatalf("onFinal = %q, want complete", finalText)
	}
}

func TestStreamingReplySender_ReconcileFinalMessageKeepsAlreadyStreamedSuffix(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(0, api, func(text string) { finalText = text })
	defer s.Close()

	longAnswer := "完整分析正文\n\n"
	lastMessage := "任务完成。已在本地克隆并分析了项目，总结如上。"
	s.enqueue(replyCommandAdd, longAnswer+lastMessage)
	s.enqueue(replyCommandReconcileFinalMessage, lastMessage)
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	want := longAnswer + lastMessage
	if len(finals) != 1 || finals[0] != want {
		t.Fatalf("final cards = %#v, want unchanged streamed answer %q", finals, want)
	}
	if finalText != want {
		t.Fatalf("onFinal = %q, want %q", finalText, want)
	}
}

func TestStreamingReplySender_AssistantMessageBoundariesCreateParagraphs(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)
	defer s.Close()

	s.enqueue(replyCommandStartMessage, "")
	s.enqueue(replyCommandAdd, "I'll inspect the repository.")
	s.enqueue(replyCommandStartMessage, "")
	s.enqueue(replyCommandAdd, "Now I'll read the core files.")
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	want := "I'll inspect the repository.\n\nNow I'll read the core files."
	if len(finals) != 1 || finals[0] != want {
		t.Fatalf("final cards = %#v, want %q", finals, want)
	}
}

func TestStreamingReplySender_ReconcileFinalMessageCreatesFallbackWithoutDeltas(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)
	defer s.Close()

	s.enqueue(replyCommandReconcileFinalMessage, "完整最终回答")
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "完整最终回答" {
		t.Fatalf("final cards = %#v, want result fallback", finals)
	}
}

func TestStreamingReplySender_ReconcileFinalMessageCompletesPartialSuffix(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)
	defer s.Close()

	s.enqueue(replyCommandAdd, "任务完成。已在本地克")
	s.enqueue(replyCommandReconcileFinalMessage, "任务完成。已在本地克隆并分析了项目。")
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "任务完成。已在本地克隆并分析了项目。" {
		t.Fatalf("final cards = %#v, want completed final message", finals)
	}
}

func TestFinalMessageOverlapHandlesLongMismatchAndUTF8(t *testing.T) {
	streamed := strings.Repeat("前", 20000) + "最终答"
	final := "最终答案"
	if got := finalMessageOverlap(streamed, final); got != 3 {
		t.Fatalf("overlap = %d, want 3", got)
	}
}

func TestStreamingReplySender_ReconcileFinalMessageAppendsMissingMessage(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)
	defer s.Close()

	s.enqueue(replyCommandAdd, "完整分析正文")
	s.enqueue(replyCommandReconcileFinalMessage, "任务完成。")
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "完整分析正文\n\n任务完成。" {
		t.Fatalf("final cards = %#v, want final message appended", finals)
	}
}

func TestStreamingReplySender_ReconcileFinalMessageDoesNotBlankSplitCards(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)
	defer s.Close()

	lastMessage := "任务完成。"
	s.enqueue(replyCommandAdd, strings.Repeat("完整分析正文行\n", 1200)+lastMessage)
	s.Flush()
	api.mu.Lock()
	before := api.finalsLocked()
	api.mu.Unlock()
	if len(before) < 2 {
		t.Fatalf("cards before result = %d, want split answer", len(before))
	}

	s.enqueue(replyCommandReconcileFinalMessage, lastMessage)
	s.Flush()
	api.mu.Lock()
	after := api.finalsLocked()
	api.mu.Unlock()
	if len(after) != len(before) {
		t.Fatalf("cards after result = %d, want %d", len(after), len(before))
	}
	for i := range before {
		if after[i] != before[i] || strings.TrimSpace(after[i]) == "" {
			t.Fatalf("card %d after result = %q, want unchanged nonblank %q", i, after[i], before[i])
		}
	}
}

func TestStreamingReplySender_ReplaceCurrentPatchesExistingCard(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)

	s.enqueue(replyCommandAdd, "partial")
	waitForAtomic(t, "creates before replacement", &api.creates, 1)
	s.ReplaceCurrent("complete")
	s.Flush()

	if got := atomic.LoadInt32(&api.patches); got != 1 {
		t.Fatalf("patches = %d, want 1 replacement patch", got)
	}
	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "complete" {
		t.Fatalf("final cards = %#v, want [complete]", finals)
	}
}

func TestStreamingReplySender_ReplaceCurrentReplacesOpenTurn(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(0, api, func(t string) { finalText = t })

	s.enqueue(replyCommandAdd, "pre")
	s.Segment()
	s.enqueue(replyCommandAdd, "par")
	s.ReplaceCurrent("post")
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "post" {
		t.Fatalf("final cards = %#v, want [post]", finals)
	}
	if finalText != "post" {
		t.Fatalf("onFinal = %q, want post", finalText)
	}
}

func TestStreamingReplySender_FullTurnResultDoesNotDuplicateCurrentTurn(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(0, api, func(t string) { finalText = t })

	s.enqueue(replyCommandAdd, "pre")
	s.Segment()
	s.enqueue(replyCommandAdd, "post")
	s.ReplaceCurrent("prepost")
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "prepost" {
		t.Fatalf("final cards = %#v, want [prepost]", finals)
	}
	if finalText != "prepost" {
		t.Fatalf("onFinal = %q, want prepost", finalText)
	}
}

func TestStreamingReplySender_FullTurnResultAfterSegmentReplacesCurrentTurn(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(0, api, func(t string) { finalText = t })

	s.enqueue(replyCommandAdd, "pre")
	s.Segment()
	s.ReplaceCurrent("prepost")
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "prepost" {
		t.Fatalf("final cards = %#v, want [prepost]", finals)
	}
	if finalText != "prepost" {
		t.Fatalf("onFinal = %q, want prepost", finalText)
	}
}

func TestStreamingReplySender_RollsLongRepliesIntoMultipleCards(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(time.Hour, api, func(t string) { finalText = t })
	first := strings.Repeat("a", streamingReplyCardSoftLimit-10)
	second := strings.Repeat("b", 20)

	s.enqueue(replyCommandAdd, first)
	s.enqueue(replyCommandAdd, second)
	s.Flush()

	if got := atomic.LoadInt32(&api.creates); got != 2 {
		t.Fatalf("creates = %d, want 2 cards", got)
	}
	if got := atomic.LoadInt32(&api.finishes); got != 2 {
		t.Fatalf("finishes = %d, want 2 cards finalized", got)
	}
	// Every card (created or patched) must stay within the soft limit.
	api.mu.Lock()
	for _, txt := range api.allTexts {
		if len(txt) > streamingReplyCardSoftLimit {
			t.Fatalf("card text length %d exceeds soft limit %d", len(txt), streamingReplyCardSoftLimit)
		}
	}
	api.mu.Unlock()
	if finalText != first+second {
		t.Fatalf("final text length = %d, want %d", len(finalText), len(first+second))
	}
}

func TestStreamingReplySender_ResultCalibratesPreviouslySplitCards(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(time.Hour, api, func(text string) { finalText = text })
	original := strings.Repeat("a", streamingReplyCardSoftLimit-10) + strings.Repeat("b", 20)
	authoritative := "z" + original[1:]

	s.enqueue(replyCommandAdd, original)
	s.ReplaceCurrent(authoritative)
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 2 {
		t.Fatalf("final cards = %d, want 2", len(finals))
	}
	if got := strings.Join(finals, ""); got != authoritative {
		t.Fatalf("joined final cards differ from authoritative result: got prefix %q", got[:20])
	}
	if finalText != authoritative {
		t.Fatalf("onFinal length = %d, want %d", len(finalText), len(authoritative))
	}
	s.Close()
}

func TestStreamingReplySender_ResultCalibratesWhenSplitCardCountChanges(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(time.Hour, api, nil)
	original := strings.Repeat("a", streamingReplyCardSoftLimit+20)

	s.enqueue(replyCommandAdd, original)
	s.ReplaceCurrent("short authoritative result")
	s.Flush()

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 2 {
		t.Fatalf("final cards = %#v, want two existing cards", finals)
	}
	if finals[0] != "short authoritative result" || strings.TrimSpace(finals[1]) != "" {
		t.Fatalf("final cards = %#v, want authoritative text plus cleared continuation", finals)
	}
	s.Close()
}

// TestStreamingReplySender_BalancesCodeFenceAcrossCards ensures that when a long
// reply is split mid code block, the first card closes the fence and the
// continuation card reopens it, so neither card shows broken markdown in Feishu.
func TestStreamingReplySender_BalancesCodeFenceAcrossCards(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(time.Hour, api, nil)

	// A fenced code block large enough to overflow the soft limit, with internal
	// newlines so the splitter has line boundaries to cut on.
	var b strings.Builder
	b.WriteString("```python\n")
	for b.Len() < streamingReplyCardSoftLimit+2000 {
		b.WriteString("x = compute_value(idx)\n")
	}
	b.WriteString("```\n")

	s.enqueue(replyCommandAdd, b.String())
	s.Flush()

	if got := atomic.LoadInt32(&api.creates); got < 2 {
		t.Fatalf("creates = %d, want >= 2 cards for an over-limit code block", got)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	cardFinals := api.finalsLocked()
	if len(cardFinals) < 2 {
		t.Fatalf("cardFinals = %d, want >= 2", len(cardFinals))
	}
	for i, card := range cardFinals {
		if n := strings.Count(card, "```"); n%2 != 0 {
			t.Fatalf("card %d has unbalanced code fences (%d markers):\n%s", i, n, card)
		}
		if len(card) > streamingReplyCardSoftLimit {
			t.Fatalf("card %d length %d exceeds soft limit", i, len(card))
		}
	}
	// The continuation card must reopen the fenced block with its language.
	if !strings.HasPrefix(cardFinals[1], "```python") {
		t.Fatalf("continuation card should reopen ```python, got prefix %q", firstLine(cardFinals[1]))
	}
}

func TestSplitMarkdownCard(t *testing.T) {
	t.Run("cuts on line boundary", func(t *testing.T) {
		text := "line one\nline two\nline three"
		// Limit includes the newline after "line two" so that is the last cut point.
		head, tail, reopen := splitMarkdownCard(text, len("line one\nline two\n"))
		if head != "line one\nline two" {
			t.Fatalf("head = %q", head)
		}
		if tail != "line three" {
			t.Fatalf("tail = %q", tail)
		}
		if reopen != "" {
			t.Fatalf("reopen = %q, want empty (no open fence)", reopen)
		}
	})
	t.Run("balances open fence", func(t *testing.T) {
		text := "intro\n```go\nfmt.Println()\nmore code here"
		head, tail, reopen := splitMarkdownCard(text, len("intro\n```go\nfmt.Println()"))
		if !strings.HasSuffix(head, "```") {
			t.Fatalf("head should be closed with a fence, got %q", head)
		}
		if reopen != "```go\n" {
			t.Fatalf("reopen = %q, want %q", reopen, "```go\n")
		}
		if !strings.Contains(tail, "more code here") {
			t.Fatalf("tail = %q", tail)
		}
	})
	t.Run("keeps utf8 valid without line boundary", func(t *testing.T) {
		text := strings.Repeat("你", 3000)
		head, tail, _ := splitMarkdownCard(text, 8000)
		if !utf8.ValidString(head) || !utf8.ValidString(tail) {
			t.Fatalf("split produced invalid UTF-8: head=%v tail=%v", utf8.ValidString(head), utf8.ValidString(tail))
		}
		if head+tail != text {
			t.Fatal("split text did not round-trip")
		}
	})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Segment is a compatibility no-op. Tool activity should not split ordinary
// assistant narration into multiple Feishu reply cards.
func TestStreamingReplySender_SegmentDoesNotStartNewCard(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(0, api, func(t string) { finalText = t })

	s.enqueue(replyCommandAdd, "let me check the file")
	waitForAtomic(t, "creates", &api.creates, 1)
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 before Segment", got)
	}

	s.Segment()
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 after Segment", got)
	}

	s.enqueue(replyCommandAdd, "the file contains foo")
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 before Flush", got)
	}

	s.Flush()
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 after Flush", got)
	}

	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()

	if len(finals) != 1 {
		t.Fatalf("cards = %d, want 1: %#v", len(finals), finals)
	}
	if finals[0] != "let me check the filethe file contains foo" {
		t.Fatalf("card = %q, want full narration", finals[0])
	}
	if finalText != "let me check the filethe file contains foo" {
		t.Fatalf("onFinal full text = %q, want concatenation of both segments", finalText)
	}
}

// Segment with no accumulated text is a no-op: it must not create an empty card.
func TestStreamingReplySender_SegmentNoopWhenEmpty(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)

	s.Segment()
	if got := atomic.LoadInt32(&api.creates); got != 0 {
		t.Fatalf("creates = %d, want 0 (Segment before any text must not create a card)", got)
	}

	s.enqueue(replyCommandAdd, "answer after a tool that produced no narration")
	waitForAtomic(t, "creates", &api.creates, 1)
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 after first visible delta", got)
	}
	s.Flush()
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 after Flush", got)
	}
}

func TestStreamingReplySender_FirstDeltaCreatesImmediately(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)

	s.enqueue(replyCommandAdd, "hello")
	waitForAtomic(t, "creates", &api.creates, 1)
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 immediately", got)
	}
	api.mu.Lock()
	if api.lastText != "hello" {
		t.Fatalf("lastText = %q, want %q", api.lastText, "hello")
	}
	api.mu.Unlock()
}

func TestStreamingReplySender_SegmentKeepsPreToolText(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)

	s.enqueue(replyCommandAdd, "pre")
	waitForAtomic(t, "creates", &api.creates, 1)
	s.Segment()
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 after Segment", got)
	}

	s.enqueue(replyCommandAdd, "post")
	s.Flush()
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 after Flush", got)
	}
	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "prepost" {
		t.Fatalf("finals = %#v, want [prepost]", finals)
	}
}

func TestStreamingReplySender_MergesAcrossToolSegment(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)

	s.enqueue(replyCommandAdd, "pre")
	s.Segment()
	s.enqueue(replyCommandAdd, "post1 ")
	s.enqueue(replyCommandAdd, "post2 ")
	s.enqueue(replyCommandAdd, "post3")
	time.Sleep(80 * time.Millisecond)

	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1", got)
	}
	api.mu.Lock()
	finals := api.finalsLocked()
	api.mu.Unlock()
	if len(finals) != 1 || finals[0] != "prepost1 post2 post3" {
		t.Fatalf("finals = %#v, want one merged card", finals)
	}
}

// Segment while pending and current is empty is a no-op that stays pending.
func TestStreamingReplySender_SegmentNoopWhenPendingEmpty(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(0, api, nil)

	// No text yet; Segment must not create a card.
	s.Segment()
	if got := atomic.LoadInt32(&api.creates); got != 0 {
		t.Fatalf("creates = %d, want 0 (empty Segment)", got)
	}
	// Text after the empty Segment still goes through pending and Flush.
	s.enqueue(replyCommandAdd, "answer")
	s.Flush()
	if got := atomic.LoadInt32(&api.creates); got != 1 {
		t.Fatalf("creates = %d, want 1 after Flush", got)
	}
}

// tableMD is one minimal GFM table (header + separator + one row).
const tableMD = "| a | b |\n| --- | --- |\n| 1 | 2 |"

func TestCountMarkdownTables(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"none", "just text\n\nmore text", 0},
		{"one", tableMD, 1},
		{"two separated by blank", tableMD + "\n\n" + tableMD, 2},
		{"two separated by heading", tableMD + "\n## h\n" + tableMD, 2},
		{"pipes inside code fence not counted", "```\n" + tableMD + "\n```", 0},
		{"five", strings.Repeat(tableMD+"\n\n", 5), 5},
		{"six", strings.Repeat(tableMD+"\n\n", 6), 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := countMarkdownTables(c.in); got != c.want {
				t.Fatalf("countMarkdownTables() = %d, want %d\ninput:\n%s", got, c.want, c.in)
			}
		})
	}
}

func TestSplitMarkdownCardAtTableLimit(t *testing.T) {
	t.Run("splits after Nth table, next heading travels to tail", func(t *testing.T) {
		// Six tables, each preceded by a heading and followed by a blank line.
		var b strings.Builder
		for i := 0; i < 6; i++ {
			b.WriteString("## Section\n")
			b.WriteString(tableMD)
			b.WriteString("\n\n")
		}
		head, tail, reopen, ok := splitMarkdownCardAtTableLimit(b.String(), 5)
		if !ok {
			t.Fatal("ok = false, want a split for 6 tables at limit 5")
		}
		if n := countMarkdownTables(head); n != 5 {
			t.Fatalf("head has %d tables, want 5:\n%s", n, head)
		}
		if n := countMarkdownTables(reopen + tail); n != 1 {
			t.Fatalf("tail has %d tables, want 1:\n%s", n, reopen+tail)
		}
		// The 6th table's heading must stay with its table in the tail card.
		if got := firstLine(strings.TrimLeft(reopen+tail, "\n")); !strings.HasPrefix(got, "## Section") {
			t.Fatalf("tail should start with the 6th table's heading, got %q", got)
		}
	})
	t.Run("no split at or under the limit", func(t *testing.T) {
		if _, _, _, ok := splitMarkdownCardAtTableLimit(strings.Repeat(tableMD+"\n\n", 5), 5); ok {
			t.Fatal("ok = true, want no split for exactly 5 tables")
		}
	})
}

// TestStreamingReplySender_SplitsOnTableLimit reproduces the ErrCode 11310 drop:
// a reply with more than maxTablesPerReplyCard tables, but well under the byte
// soft limit, must still be split into multiple cards so no single card exceeds
// Feishu's per-card table cap.
func TestStreamingReplySender_SplitsOnTableLimit(t *testing.T) {
	api := &fakeReplyAPI{}
	var finalText string
	s := newStreamingReplySender(time.Hour, api, func(t string) { finalText = t })

	// Seven small tables (well under the byte soft limit), each with a heading
	// and trailing blank line, so only the table cap can force a split.
	var b strings.Builder
	for i := 0; i < 7; i++ {
		b.WriteString("## Section\n")
		b.WriteString(tableMD)
		b.WriteString("\n\n")
	}
	full := b.String()
	if len(full) >= streamingReplyCardSoftLimit {
		t.Fatalf("test setup: %d bytes is not under the byte soft limit; adjust the fixture", len(full))
	}

	s.enqueue(replyCommandAdd, full)
	s.Flush()

	if got := atomic.LoadInt32(&api.creates); got < 2 {
		t.Fatalf("creates = %d, want >= 2 cards for a %d-table reply", got, countMarkdownTables(full))
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	for i, card := range api.finalsLocked() {
		if n := countMarkdownTables(card); n > maxTablesPerReplyCard {
			t.Fatalf("card %d has %d tables, exceeds per-card limit %d:\n%s", i, n, maxTablesPerReplyCard, card)
		}
	}
	// The split must not lose any tables.
	if n := countMarkdownTables(finalText); n != 7 {
		t.Fatalf("final text has %d tables, want 7", n)
	}
}

// TestStreamingReplySender_SplitsWhenBothCapsExceeded exercises the branch where
// the byte soft limit binds before 5 tables accumulate (large tables): both caps
// are active and the earlier boundary must win, so no card exceeds either cap.
func TestStreamingReplySender_SplitsWhenBothCapsExceeded(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(time.Hour, api, nil)

	// Each table is large enough that only a couple fit under the byte soft
	// limit, so the byte cap forces cuts before the 5-table cap would.
	bigRow := "| " + strings.Repeat("x", 300) + " | " + strings.Repeat("y", 300) + " |\n"
	var oneTable strings.Builder
	oneTable.WriteString("## Section\n| h1 | h2 |\n| --- | --- |\n")
	for r := 0; r < 6; r++ {
		oneTable.WriteString(bigRow)
	}
	oneTable.WriteString("\n")
	var b strings.Builder
	for i := 0; i < 8; i++ {
		b.WriteString(oneTable.String())
	}

	s.enqueue(replyCommandAdd, b.String())
	s.Flush()

	api.mu.Lock()
	defer api.mu.Unlock()
	cards := api.finalsLocked()
	if len(cards) < 2 {
		t.Fatalf("cards = %d, want >= 2 when both caps are exceeded", len(cards))
	}
	for i, card := range cards {
		if n := countMarkdownTables(card); n > maxTablesPerReplyCard {
			t.Fatalf("card %d has %d tables, exceeds per-card limit %d", i, n, maxTablesPerReplyCard)
		}
	}
}

// TestStreamingReplySender_CalibrationRespectsTableLimit guards the turn-end
// calibration path (replaceCurrent -> calibrateSplitTurn -> splitMarkdownCards):
// once a table-heavy turn has been split into finalized cards, the authoritative
// re-split at turn end must also keep every card within the per-card table cap,
// or calibration would re-merge cards past the limit and re-trigger 11310.
func TestStreamingReplySender_CalibrationRespectsTableLimit(t *testing.T) {
	api := &fakeReplyAPI{}
	s := newStreamingReplySender(time.Hour, api, nil)

	var b strings.Builder
	for i := 0; i < 7; i++ {
		b.WriteString("## Section\n")
		b.WriteString(tableMD)
		b.WriteString("\n\n")
	}
	full := b.String()

	// Streaming produces finalized cards; ReplaceCurrent then routes the
	// authoritative text through calibrateSplitTurn (len(finalized) > 0).
	s.enqueue(replyCommandAdd, full)
	s.ReplaceCurrent(strings.TrimRight(full, "\n"))
	s.Flush()

	api.mu.Lock()
	defer api.mu.Unlock()
	cards := api.finalsLocked()
	if len(cards) < 2 {
		t.Fatalf("calibrated cards = %d, want >= 2 for a 7-table turn", len(cards))
	}
	total := 0
	for i, card := range cards {
		n := countMarkdownTables(card)
		if n > maxTablesPerReplyCard {
			t.Fatalf("calibrated card %d has %d tables, exceeds per-card limit %d:\n%s", i, n, maxTablesPerReplyCard, card)
		}
		total += n
	}
	if total != 7 {
		t.Fatalf("calibrated cards hold %d tables total, want 7", total)
	}
}
