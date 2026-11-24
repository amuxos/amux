package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newTestScheduler(sub Submitter, store *Store) *Scheduler {
	return NewWithTaskSubmitter(sub, store, sessionlaunch.New(sub), nil)
}

func newTestSchedulerWithTask(sub Submitter, store *Store, taskSubmit TaskSubmitter) *Scheduler {
	return NewWithTaskSubmitter(sub, store, sessionlaunch.New(sub), taskSubmit)
}

func TestStoreCRUD(t *testing.T) {
	s := newTestStore(t)
	next := int64(1000)
	created, err := s.Create(Task{
		Name: "nightly", TargetID: "local", TargetName: "local", SessionName: "c1",
		Prompt: "hello", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 {
		t.Fatal("expected non-zero id")
	}

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "hello" || got.Kind != KindInterval || got.RunMode != RunModeReuseSession || !got.Enabled || got.NextRunAt == nil || *got.NextRunAt != 1000 {
		t.Fatalf("Get mismatch: %+v", got)
	}

	list, err := s.List(Filter{TargetID: "local", SessionName: "c1"})
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v (err %v)", list, err)
	}
	if other, _ := s.List(Filter{SessionName: "nope"}); len(other) != 0 {
		t.Fatalf("filter should exclude, got %v", other)
	}

	ok, err := s.SetEnabled(created.ID, false)
	if err != nil || !ok {
		t.Fatalf("SetEnabled: %v %v", ok, err)
	}
	if g, _ := s.Get(created.ID); g.Enabled {
		t.Fatal("expected disabled")
	}

	ok, err = s.Delete(created.ID)
	if err != nil || !ok {
		t.Fatalf("Delete: %v %v", ok, err)
	}
}

func TestStoreUpdate(t *testing.T) {
	s := newTestStore(t)
	next := int64(1000)
	created, err := s.Create(Task{
		Name: "nightly", TargetID: "local", TargetName: "local", SessionName: "c1",
		Prompt: "hello", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &next,
	})
	if err != nil {
		t.Fatal(err)
	}

	upd := created
	upd.Name = "renamed"
	upd.Prompt = "goodbye"
	upd.Spec = "daily 09:00"
	upd.Kind = KindDaily
	newNext := int64(2000)
	upd.NextRunAt = &newNext
	got, err := s.Update(upd)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != "renamed" || got.Prompt != "goodbye" || got.Spec != "daily 09:00" || got.Kind != KindDaily {
		t.Fatalf("Update mismatch: %+v", got)
	}
	if got.NextRunAt == nil || *got.NextRunAt != 2000 {
		t.Fatalf("next_run_at not updated: %+v", got.NextRunAt)
	}
	if !got.Enabled {
		t.Fatal("Update must not clear enabled")
	}

	missing := upd
	missing.ID = 999999
	if _, err := s.Update(missing); err == nil {
		t.Fatal("expected error updating missing schedule")
	}
}

func TestStoreMigratesRunMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE scheduled_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			target_id TEXT NOT NULL,
			target_name TEXT NOT NULL DEFAULT '',
			session_name TEXT NOT NULL,
			prompt TEXT NOT NULL,
			spec TEXT NOT NULL,
			kind TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			next_run_at INTEGER,
			last_run_at INTEGER,
			last_status TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		);
		INSERT INTO scheduled_tasks (target_id, session_name, prompt, spec, kind, enabled, next_run_at)
		VALUES ('local', 'c1', 'hello', 'every 1m', 'interval', 1, 100);
	`)
	if closeErr := db.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	got, err := s.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunMode != RunModeReuseSession {
		t.Fatalf("RunMode = %q, want %q", got.RunMode, RunModeReuseSession)
	}
	if got.ScheduleType != ScheduleTypeSessionPrompt {
		t.Fatalf("ScheduleType = %q, want %q", got.ScheduleType, ScheduleTypeSessionPrompt)
	}
	if len(got.TaskSpecJson) != 0 {
		t.Fatalf("TaskSpecJson len = %d, want 0", len(got.TaskSpecJson))
	}
	if err := s.RecordScheduleRun(got.ID, 110, "task-legacy", "ok:task:task-legacy"); err != nil {
		t.Fatalf("RecordScheduleRun on migrated store: %v", err)
	}
	runs, err := s.ListRuns(got.ID, 10)
	if err != nil {
		t.Fatalf("ListRuns on migrated store: %v", err)
	}
	if len(runs) != 1 || runs[0].ScheduleID != got.ID || runs[0].TaskID != "task-legacy" {
		t.Fatalf("migrated run history = %+v, want one task-legacy run", runs)
	}
}

func TestDueTasks(t *testing.T) {
	s := newTestStore(t)
	past := int64(100)
	future := int64(1 << 40)
	if _, err := s.Create(Task{TargetID: "t", SessionName: "a", Prompt: "x", Spec: "every 1m", Kind: KindInterval, Enabled: true, NextRunAt: &past}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(Task{TargetID: "t", SessionName: "b", Prompt: "x", Spec: "every 1m", Kind: KindInterval, Enabled: true, NextRunAt: &future}); err != nil {
		t.Fatal(err)
	}
	// Disabled, due -> excluded.
	if _, err := s.Create(Task{TargetID: "t", SessionName: "c", Prompt: "x", Spec: "every 1m", Kind: KindInterval, Enabled: false, NextRunAt: &past}); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueTasks(500)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].SessionName != "a" {
		t.Fatalf("DueTasks = %+v, want only session a", due)
	}
}

func TestStoreSubtaskAndRuns(t *testing.T) {
	s := newTestStore(t)
	next := int64(2000)
	created, err := s.Create(Task{
		Name:         "nightly-review",
		ScheduleType: ScheduleTypeSubtask,
		Spec:         "every 30m",
		Kind:         KindInterval,
		TaskSpecJson: []byte(`{"agent":"builtin/codex","task":"review","permission_policy":"deny-all"}`),
		Enabled:      true,
		NextRunAt:    &next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ScheduleType != ScheduleTypeSubtask {
		t.Fatalf("type = %q, want %q", created.ScheduleType, ScheduleTypeSubtask)
	}
	if string(created.TaskSpecJson) == "" {
		t.Fatal("expected task spec json to round-trip")
	}

	if err := s.RecordScheduleRun(created.ID, 2100, "task-1", "ok:task:task-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordScheduleRun(created.ID, 2200, "", "error: no runtime"); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRuns(created.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs len = %d, want 2", len(runs))
	}
	if runs[0].FiredAt != 2200 || runs[0].Status != "error: no runtime" {
		t.Fatalf("newest run mismatch: %+v", runs[0])
	}
	if runs[1].TaskID != "task-1" {
		t.Fatalf("oldest task id mismatch: %+v", runs[1])
	}
}

func TestStoreDeleteCascadesRuns(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(Task{
		TargetID:    "local",
		SessionName: "c1",
		Prompt:      "tick",
		Spec:        "every 30m",
		Kind:        KindInterval,
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordScheduleRun(created.ID, 2100, "task-1", "ok:task:task-1"); err != nil {
		t.Fatal(err)
	}
	ok, err := s.Delete(created.ID)
	if err != nil || !ok {
		t.Fatalf("Delete: %v %v", ok, err)
	}
	runs, err := s.ListRuns(created.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs after schedule delete = %+v, want none", runs)
	}
}

// fakeSub implements Submitter for scheduler tests.
type fakeRoute struct {
	method  string
	payload interface{}
}

type fakeSub struct {
	mu             sync.Mutex
	targets        []*pb.Target
	routes         []fakeRoute
	failures       map[string]error
	runSessionName string
	routeFn        func(ctx context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error)
}

func (f *fakeSub) ListTargets() []*pb.Target { return f.targets }
func (f *fakeSub) Route(ctx context.Context, targetID string, method string, payload interface{}) (*pb.TunnelMsg, error) {
	if f.routeFn != nil {
		return f.routeFn(ctx, targetID, method, payload)
	}
	f.mu.Lock()
	err := f.failures[method]
	runSessionName := f.runSessionName
	if err == nil {
		f.routes = append(f.routes, fakeRoute{method: method, payload: payload})
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if method == "Run" {
		req := payload.(*pb.RunRequest)
		name := req.Name
		if runSessionName != "" {
			name = runSessionName
		}
		raw, _ := json.Marshal(&pb.RunResponse{Session: &pb.Session{
			Name: name, AgentType: req.AgentType, Cwd: req.Cwd, State: "running",
		}})
		return &pb.TunnelMsg{Payload: raw}, nil
	}
	return &pb.TunnelMsg{}, nil
}

func (f *fakeSub) submitted() []*pb.SubmitRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*pb.SubmitRequest
	for _, route := range f.routes {
		if req, ok := route.payload.(*pb.SubmitRequest); ok {
			out = append(out, req)
		}
	}
	return out
}

func runDueAndWait(sched *Scheduler, ctx context.Context, now time.Time) {
	sched.runDue(ctx, now)
	sched.Wait()
}

type fakeTaskSubmitter struct {
	mu       sync.Mutex
	delay    time.Duration
	err      error
	ids      []string
	seen     [][]byte
	calls    int
	started  chan struct{}
	finished chan struct{}
	release  <-chan struct{}
}

func (f *fakeTaskSubmitter) SubmitScheduledTask(ctx context.Context, specJSON []byte) (string, error) {
	f.mu.Lock()
	f.calls++
	f.seen = append(f.seen, append([]byte(nil), specJSON...))
	f.mu.Unlock()

	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	defer func() {
		if f.finished != nil {
			select {
			case f.finished <- struct{}{}:
			default:
			}
		}
	}()

	if f.release != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-f.release:
		}
	}
	if f.delay > 0 {
		timer := time.NewTimer(f.delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return "", ctx.Err()
		case <-timer.C:
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	if len(f.ids) == 0 {
		return "", nil
	}
	id := f.ids[0]
	f.ids = f.ids[1:]
	return id, nil
}

func (f *fakeTaskSubmitter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func waitForRuns(t *testing.T, s *Store, scheduleID int64, want int) []Run {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := s.ListRuns(scheduleID, DefaultRunHistoryLimit)
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(runs) >= want {
			return runs
		}
		time.Sleep(10 * time.Millisecond)
	}
	runs, err := s.ListRuns(scheduleID, DefaultRunHistoryLimit)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	t.Fatalf("timed out waiting for %d runs, got %+v", want, runs)
	return nil
}

func onlineTarget() []*pb.Target {
	return []*pb.Target{{Id: "local", Name: "local", Sessions: []*pb.Session{{
		Name:            "c1",
		AgentType:       "demo-agent",
		State:           "running",
		Cwd:             "/repo",
		ExtraArgs:       []string{"--debug"},
		Env:             []string{"A=B"},
		WorkspaceSource: "cli",
		WorkspaceScope:  "local",
	}}}}
}

func TestSchedulerSubtaskClaimThenAsyncSubmit(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	specJSON := []byte(`{"agent":"builtin/codex","task":"review","permission_policy":"deny-all"}`)
	task, err := s.Create(Task{
		Name:         "nightly-review",
		ScheduleType: ScheduleTypeSubtask,
		Spec:         "every 30m",
		Kind:         KindInterval,
		TaskSpecJson: specJSON,
		Enabled:      true,
		NextRunAt:    &due,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	finished := make(chan struct{}, 1)
	release := make(chan struct{})
	releaseOnce := sync.Once{}
	releaseSubmitter := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseSubmitter()
	fake := &fakeTaskSubmitter{ids: []string{"task-42"}, started: started, finished: finished, release: release}
	sched := newTestSchedulerWithTask(&fakeSub{}, s, fake)
	runDone := make(chan struct{})
	go func() {
		sched.runDue(context.Background(), now)
		close(runDone)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("submitter did not start")
	}
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("runDue did not return before scheduled subtask submitter was released")
	}
	select {
	case <-finished:
		t.Fatal("submitter finished before release")
	default:
	}

	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != statusSubmitting {
		t.Fatalf("immediate status = %q, want %q", got.LastStatus, statusSubmitting)
	}
	if got.NextRunAt == nil || *got.NextRunAt != now.Add(30*time.Minute).Unix() {
		t.Fatalf("next_run_at = %v, want %d", got.NextRunAt, now.Add(30*time.Minute).Unix())
	}

	releaseSubmitter()
	runs := waitForRuns(t, s, task.ID, 1)
	if len(runs) != 1 || runs[0].TaskID != "task-42" || runs[0].Status != "ok:task:task-42" {
		t.Fatalf("runs = %+v, want one ok task-42 run", runs)
	}
	got, err = s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != "ok:task:task-42" {
		t.Fatalf("final status = %q, want ok:task:task-42", got.LastStatus)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.seen) != 1 || string(fake.seen[0]) != string(specJSON) {
		t.Fatalf("submitter saw = %q, want %q", fake.seen, specJSON)
	}
}

func TestSchedulerSubtaskConcurrentRunDueClaimsOnce(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, err := s.Create(Task{
		Name:         "nightly-review",
		ScheduleType: ScheduleTypeSubtask,
		Spec:         "every 30m",
		Kind:         KindInterval,
		TaskSpecJson: []byte(`{"agent":"builtin/codex","task":"review"}`),
		Enabled:      true,
		NextRunAt:    &due,
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTaskSubmitter{ids: []string{"task-42", "task-43"}}
	sched := newTestSchedulerWithTask(&fakeSub{}, s, fake)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			sched.runDue(context.Background(), now)
		}()
	}
	wg.Wait()

	runs := waitForRuns(t, s, task.ID, 1)
	if len(runs) != 1 || runs[0].TaskID != "task-42" || runs[0].Status != "ok:task:task-42" {
		t.Fatalf("runs = %+v, want exactly one ok task-42 run", runs)
	}
	if got := fake.callCount(); got != 1 {
		t.Fatalf("submitter calls = %d, want 1", got)
	}
}

func TestSchedulerSubtaskStaleCompletionDoesNotOverwriteNewerStatus(t *testing.T) {
	s := newTestStore(t)
	due := int64(1000)
	task, err := s.Create(Task{
		Name:         "nightly-review",
		ScheduleType: ScheduleTypeSubtask,
		Spec:         "every 30m",
		Kind:         KindInterval,
		TaskSpecJson: []byte(`{"agent":"builtin/codex","task":"review"}`),
		Enabled:      true,
		NextRunAt:    &due,
	})
	if err != nil {
		t.Fatal(err)
	}
	olderRanAt := int64(1100)
	newerRanAt := int64(1200)
	nextAfterOlder := int64(1800)
	nextAfterNewer := int64(2400)

	claimed, err := s.ClaimRun(task.ID, task.NextRunAt, olderRanAt, &nextAfterOlder, statusSubmitting)
	if err != nil {
		t.Fatalf("claim older run: %v", err)
	}
	if !claimed {
		t.Fatal("older run was not claimed")
	}
	claimed, err = s.ClaimRun(task.ID, &nextAfterOlder, newerRanAt, &nextAfterNewer, statusSubmitting)
	if err != nil {
		t.Fatalf("claim newer run: %v", err)
	}
	if !claimed {
		t.Fatal("newer run was not claimed")
	}

	sched := newTestSchedulerWithTask(&fakeSub{}, s, nil)
	sched.recordSubtaskFinal(task.ID, newerRanAt, "task-new", "ok:task:task-new")
	sched.recordSubtaskFinal(task.ID, olderRanAt, "task-old", "ok:task:task-old")

	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastRunAt == nil || *got.LastRunAt != newerRanAt {
		t.Fatalf("last_run_at = %v, want %d", got.LastRunAt, newerRanAt)
	}
	if got.LastStatus != "ok:task:task-new" {
		t.Fatalf("last_status = %q, want ok:task:task-new", got.LastStatus)
	}
	runs, err := s.ListRuns(task.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs len = %d, want 2 (%+v)", len(runs), runs)
	}
	seen := map[string]bool{}
	for _, run := range runs {
		seen[run.TaskID] = true
	}
	if !seen["task-new"] || !seen["task-old"] {
		t.Fatalf("runs = %+v, want both new and old completions", runs)
	}
}

func TestSchedulerSubtaskSubmitErrorAdvancesRecurring(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, err := s.Create(Task{
		Name:         "nightly-review",
		ScheduleType: ScheduleTypeSubtask,
		Spec:         "every 30m",
		Kind:         KindInterval,
		TaskSpecJson: []byte(`{"agent":"builtin/codex","task":"review"}`),
		Enabled:      true,
		NextRunAt:    &due,
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeTaskSubmitter{err: errors.New("runtime unavailable")}

	newTestSchedulerWithTask(&fakeSub{}, s, fake).runDue(context.Background(), now)

	runs := waitForRuns(t, s, task.ID, 1)
	if len(runs) != 1 || !strings.HasPrefix(runs[0].Status, "error:") {
		t.Fatalf("runs = %+v, want one error run", runs)
	}
	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextRunAt == nil || *got.NextRunAt != now.Add(30*time.Minute).Unix() {
		t.Fatalf("next_run_at = %v, want %d", got.NextRunAt, now.Add(30*time.Minute).Unix())
	}
	if !strings.HasPrefix(got.LastStatus, "error:") {
		t.Fatalf("last status = %q, want error", got.LastStatus)
	}
}

func TestSchedulerSubtaskOneShotNoSubmitterMissed(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, err := s.Create(Task{
		Name:         "one-shot-review",
		ScheduleType: ScheduleTypeSubtask,
		Spec:         "at 2026-06-01 09:59",
		Kind:         KindOnce,
		TaskSpecJson: []byte(`{"agent":"builtin/codex","task":"review"}`),
		Enabled:      true,
		NextRunAt:    &due,
	})
	if err != nil {
		t.Fatal(err)
	}

	newTestSchedulerWithTask(&fakeSub{}, s, nil).runDue(context.Background(), now)

	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != statusMissed {
		t.Fatalf("status = %q, want %q", got.LastStatus, statusMissed)
	}
	if got.NextRunAt != nil {
		t.Fatalf("next_run_at = %v, want nil", got.NextRunAt)
	}
	runs, err := s.ListRuns(task.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].TaskID != "" || runs[0].Status != statusMissed {
		t.Fatalf("runs = %+v, want one missed run", runs)
	}
}

func TestSchedulerFireOnlineRecurring(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, _ := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &due})

	sub := &fakeSub{targets: onlineTarget()}
	runDueAndWait(newTestScheduler(sub, s), context.Background(), now)

	if len(sub.submitted()) != 1 || sub.submitted()[0].Text != "tick" || sub.submitted()[0].Source != "scheduler" || !sub.submitted()[0].Async {
		t.Fatalf("submitted = %+v", sub.submitted())
	}
	got, _ := s.Get(task.ID)
	if got.LastStatus != statusOK {
		t.Errorf("status = %q, want ok", got.LastStatus)
	}
	if got.NextRunAt == nil || *got.NextRunAt != now.Add(30*time.Minute).Unix() {
		t.Errorf("next_run_at not advanced: %v", got.NextRunAt)
	}
}

func TestSchedulerAuthorizationDenialRoutesNothingAndRecordsError(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, err := s.Create(Task{
		TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m",
		Kind: KindInterval, Enabled: true, NextRunAt: &due, OwnerPrincipalID: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	sub := &fakeSub{targets: onlineTarget()}
	sched := NewWithTaskSubmitter(sub, s, sessionlaunch.New(sub), nil)
	sched.SetSessionPromptAuthorizer(func(context.Context, Task, *pb.Session) error {
		return errors.New("session authorization: owner no longer authorized")
	})

	runDueAndWait(sched, context.Background(), now)

	if len(sub.routes) != 0 {
		t.Fatalf("authorization denial routed calls: %+v", sub.routes)
	}
	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != "error: session authorization: owner no longer authorized" {
		t.Fatalf("last status = %q", got.LastStatus)
	}
	if got.NextRunAt == nil || *got.NextRunAt != now.Add(30*time.Minute).Unix() {
		t.Fatalf("recurring denial did not advance next run: %v", got.NextRunAt)
	}
}

func TestSchedulerAuthorizationRunsBeforeNewSessionPerRun(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, err := s.Create(Task{
		TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m",
		Kind: KindInterval, RunMode: RunModeNewSessionPerRun, Enabled: true,
		NextRunAt: &due, OwnerPrincipalID: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	sub := &fakeSub{targets: onlineTarget()}
	sched := NewWithTaskSubmitter(sub, s, sessionlaunch.New(sub), nil)
	sched.SetSessionPromptAuthorizer(func(context.Context, Task, *pb.Session) error {
		return errors.New("session authorization: template owner changed")
	})

	runDueAndWait(sched, context.Background(), now)

	if len(sub.routes) != 0 {
		t.Fatalf("authorization denial routed calls: %+v", sub.routes)
	}
	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != "error: session authorization: template owner changed" {
		t.Fatalf("last status = %q", got.LastStatus)
	}
}

func TestSchedulerAuthorizationAllowsDelivery(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	_, err := s.Create(Task{
		TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m",
		Kind: KindInterval, Enabled: true, NextRunAt: &due, OwnerPrincipalID: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	sub := &fakeSub{targets: onlineTarget()}
	called := 0
	sched := NewWithTaskSubmitter(sub, s, sessionlaunch.New(sub), nil)
	sched.SetSessionPromptAuthorizer(func(_ context.Context, task Task, session *pb.Session) error {
		called++
		if task.OwnerPrincipalID != "alice" || session.Name != "c1" {
			t.Fatalf("authorizer got task=%+v session=%+v", task, session)
		}
		return nil
	})

	runDueAndWait(sched, context.Background(), now)

	if called != 1 {
		t.Fatalf("authorizer calls = %d, want 1", called)
	}
	if len(sub.routes) != 1 || sub.routes[0].method != "Submit" {
		t.Fatalf("routes = %+v, want one Submit", sub.routes)
	}
}

func TestSchedulerFireOfflineRecurring(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, _ := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &due})

	sub := &fakeSub{targets: nil} // nothing online
	runDueAndWait(newTestScheduler(sub, s), context.Background(), now)

	if len(sub.submitted()) != 0 {
		t.Fatalf("should not submit when offline: %+v", sub.submitted())
	}
	got, _ := s.Get(task.ID)
	if got.LastStatus != statusSkippedOffline {
		t.Errorf("status = %q, want %q", got.LastStatus, statusSkippedOffline)
	}
	if got.NextRunAt == nil {
		t.Error("recurring task should still advance when offline")
	}
}

func TestSchedulerFireOnceConsumed(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, _ := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "once", Spec: "at 2026-06-01 09:59", Kind: KindOnce, Enabled: true, NextRunAt: &due})

	sub := &fakeSub{targets: onlineTarget()}
	runDueAndWait(newTestScheduler(sub, s), context.Background(), now)

	got, _ := s.Get(task.ID)
	if got.NextRunAt != nil {
		t.Errorf("one-shot should clear next_run_at, got %v", *got.NextRunAt)
	}
	if got.LastStatus != statusOK {
		t.Errorf("status = %q, want ok", got.LastStatus)
	}
}

func TestSchedulerOnceOfflineMissed(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, _ := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "once", Spec: "at 2026-06-01 09:59", Kind: KindOnce, Enabled: true, NextRunAt: &due})

	sub := &fakeSub{targets: nil}
	runDueAndWait(newTestScheduler(sub, s), context.Background(), now)

	got, _ := s.Get(task.ID)
	if got.NextRunAt != nil {
		t.Error("missed one-shot should clear next_run_at")
	}
	if got.LastStatus != statusMissed {
		t.Errorf("status = %q, want %q", got.LastStatus, statusMissed)
	}
}

// TestSchedulerArchivedSessionSkipped verifies a killed session that lingers in
// the hub's target list as "archived" is treated as offline.
func TestSchedulerNewSessionPerRun(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, _ := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m", Kind: KindInterval, RunMode: RunModeNewSessionPerRun, Enabled: true, NextRunAt: &due})

	sub := &fakeSub{targets: onlineTarget(), runSessionName: "resolved-session"}
	runDueAndWait(newTestScheduler(sub, s), context.Background(), now)

	if len(sub.routes) != 2 {
		t.Fatalf("routes = %+v, want Run then Submit", sub.routes)
	}
	run, ok := sub.routes[0].payload.(*pb.RunRequest)
	if sub.routes[0].method != "Run" || !ok {
		t.Fatalf("first route = %s %T, want Run *RunRequest", sub.routes[0].method, sub.routes[0].payload)
	}
	submit, ok := sub.routes[1].payload.(*pb.SubmitRequest)
	if sub.routes[1].method != "Submit" || !ok {
		t.Fatalf("second route = %s %T, want Submit *SubmitRequest", sub.routes[1].method, sub.routes[1].payload)
	}
	if run.Name == "" || submit.Name != "resolved-session" {
		t.Fatalf("run name = %q submit name = %q, want resolved response name", run.Name, submit.Name)
	}
	if !strings.HasPrefix(run.Name, "sched-") || !strings.HasSuffix(run.Name, "-1") {
		t.Fatalf("run name = %q, want sched-<id>-<ts>-<execID>", run.Name)
	}
	if !run.IdleTimeoutSecondsSet || run.IdleTimeoutSeconds != defaultScheduledSessionIdleTimeoutSeconds {
		t.Fatalf("idle timeout = (%v, %d), want (true, %d)", run.IdleTimeoutSecondsSet, run.IdleTimeoutSeconds, defaultScheduledSessionIdleTimeoutSeconds)
	}
	if run.AgentType != "demo-agent" || run.Cwd != "/repo" || run.WorkspaceSource != "cli" || run.WorkspaceScope != "local" {
		t.Fatalf("run request did not clone template: %+v", run)
	}
	if len(run.ExtraArgs) != 1 || run.ExtraArgs[0] != "--debug" || len(run.Env) != 1 || run.Env[0] != "A=B" {
		t.Fatalf("run args/env did not clone template: %+v", run)
	}
	if submit.Text != "tick" || submit.Source != "scheduler" || !submit.Async {
		t.Fatalf("submit = %+v", submit)
	}
	got, _ := s.Get(task.ID)
	if got.LastStatus != "ok:new-session:resolved-session" {
		t.Fatalf("status = %q, want resolved session status", got.LastStatus)
	}
}

func TestSchedulerNewSessionPerRunRunFailure(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, _ := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m", Kind: KindInterval, RunMode: RunModeNewSessionPerRun, Enabled: true, NextRunAt: &due})

	sub := &fakeSub{targets: onlineTarget(), failures: map[string]error{"Run": context.DeadlineExceeded}}
	runDueAndWait(newTestScheduler(sub, s), context.Background(), now)

	if len(sub.routes) != 0 {
		t.Fatalf("should not record successful routes after run failure: %+v", sub.routes)
	}
	got, _ := s.Get(task.ID)
	if !strings.HasPrefix(got.LastStatus, "error: run:") {
		t.Fatalf("status = %q, want run error", got.LastStatus)
	}
}

func TestSchedulerNewSessionPerRunSubmitFailure(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, _ := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m", Kind: KindInterval, RunMode: RunModeNewSessionPerRun, Enabled: true, NextRunAt: &due})

	sub := &fakeSub{targets: onlineTarget(), failures: map[string]error{"Submit": context.DeadlineExceeded}}
	runDueAndWait(newTestScheduler(sub, s), context.Background(), now)

	if len(sub.routes) != 1 || sub.routes[0].method != "Run" {
		t.Fatalf("routes = %+v, want only Run", sub.routes)
	}
	got, _ := s.Get(task.ID)
	if !strings.HasPrefix(got.LastStatus, "error: submit:") {
		t.Fatalf("status = %q, want submit error", got.LastStatus)
	}
}

func TestSchedulerArchivedSessionSkipped(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	task, _ := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &due})

	sub := &fakeSub{targets: []*pb.Target{{Id: "local", Sessions: []*pb.Session{{Name: "c1", State: "archived"}}}}}
	runDueAndWait(newTestScheduler(sub, s), context.Background(), now)

	if len(sub.submitted()) != 0 {
		t.Fatalf("should not submit to archived session: %+v", sub.submitted())
	}
	got, _ := s.Get(task.ID)
	if got.LastStatus != statusSkippedOffline {
		t.Errorf("status = %q, want %q", got.LastStatus, statusSkippedOffline)
	}
	if got.NextRunAt == nil {
		t.Error("recurring task should still advance")
	}
}

func TestStoreUsesWALBusyTimeoutAndForeignKeys(t *testing.T) {
	s := newTestStore(t)
	var journalMode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var busyTimeout int
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != scheduleDBBusyTimeoutMS {
		t.Fatalf("busy_timeout = %d, want %d", busyTimeout, scheduleDBBusyTimeoutMS)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}
}

func TestSchedulerSessionPromptAsyncDoesNotBlockOtherDueTasks(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Add(-time.Second).Unix()
	slowTask, err := s.Create(Task{TargetID: "local", SessionName: "slow", Prompt: "slow-prompt", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	fastTask, err := s.Create(Task{TargetID: "local", SessionName: "fast", Prompt: "fast-prompt", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &due})
	if err != nil {
		t.Fatal(err)
	}

	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	fastDone := make(chan struct{})
	sub := &fakeSub{
		targets: []*pb.Target{{Id: "local", Name: "local", Sessions: []*pb.Session{
			{Name: "slow", State: "running"},
			{Name: "fast", State: "running"},
		}}},
		routeFn: func(ctx context.Context, _ string, method string, payload interface{}) (*pb.TunnelMsg, error) {
			req := payload.(*pb.SubmitRequest)
			if !req.Async {
				t.Errorf("SubmitRequest.Async = false for %s, want true", req.Name)
			}
			if req.Name == "slow" {
				close(slowStarted)
				select {
				case <-releaseSlow:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			} else if req.Name == "fast" {
				defer close(fastDone)
			}
			raw, _ := json.Marshal(&pb.SubmitResponse{Ok: true, TurnId: "turn-" + req.Name, SessionId: "sess-" + req.Name})
			return &pb.TunnelMsg{Payload: raw}, nil
		},
	}

	sched := newTestScheduler(sub, s)
	runDone := make(chan struct{})
	go func() {
		sched.runDue(context.Background(), now)
		close(runDone)
	}()

	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow task did not start")
	}
	select {
	case <-fastDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fast task was blocked by slow session-prompt delivery")
	}
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("runDue blocked waiting for slow session-prompt delivery")
	}

	close(releaseSlow)
	sched.Wait()

	for _, id := range []int64{slowTask.ID, fastTask.ID} {
		got, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastStatus != statusOK {
			t.Fatalf("task %d status = %q, want %q", id, got.LastStatus, statusOK)
		}
	}
}

func TestSchedulerBurstSubtasksDeferClaimInsteadOfDropping(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.Local)
	due := now.Unix()
	total := subtaskSubmitWorkers + 4
	ids := make([]int64, 0, total)
	taskIDs := make([]string, 0, total)
	for i := 0; i < total; i++ {
		row, err := s.Create(Task{
			Name:         "morning-burst",
			ScheduleType: ScheduleTypeSubtask,
			Spec:         "daily 09:00",
			Kind:         KindDaily,
			TaskSpecJson: []byte(`{"agent":"builtin/codex","task":"report"}`),
			Enabled:      true,
			NextRunAt:    &due,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, row.ID)
		taskIDs = append(taskIDs, "task-burst-"+string(rune('a'+i)))
	}

	release := make(chan struct{})
	fake := &fakeTaskSubmitter{ids: taskIDs, release: release}
	sched := newTestSchedulerWithTask(&fakeSub{}, s, fake)

	// First pass claims up to subtaskSubmitWorkers and leaves overflow unclaimed.
	sched.runDue(context.Background(), now)
	dueAfterFirst, err := s.DueTasks(now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(dueAfterFirst) != total-subtaskSubmitWorkers {
		t.Fatalf("remaining unclaimed due tasks = %d, want %d", len(dueAfterFirst), total-subtaskSubmitWorkers)
	}

	// Release workers; second pass claims and submits the deferred tasks cleanly.
	close(release)
	sched.Wait()
	sched.runDue(context.Background(), now)
	sched.Wait()

	if got := fake.callCount(); got != total {
		t.Fatalf("total submitted subtasks = %d, want %d (none dropped)", got, total)
	}
	for _, id := range ids {
		runs, err := s.ListRuns(id, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 1 || !strings.HasPrefix(runs[0].Status, "ok:task:") {
			t.Fatalf("schedule %d runs = %+v, want one ok:task run", id, runs)
		}
	}
}

type fakeOutcomeReader struct {
	mu    sync.Mutex
	fn    func(ctx context.Context, r Execution) (Execution, error)
	calls int
}

func (f *fakeOutcomeReader) ObserveScheduleExecution(ctx context.Context, r Execution) (Execution, error) {
	f.mu.Lock()
	f.calls++
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, r)
	}
	return r, nil
}

func (f *fakeOutcomeReader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestSchedulerReconcileSkipsInFlightAndObservePreservesReceipts(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Unix()
	row, err := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "tick", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &due})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	sub := &fakeSub{
		targets: onlineTarget(),
		routeFn: func(ctx context.Context, _ string, _ string, _ interface{}) (*pb.TunnelMsg, error) {
			close(started)
			<-release
			raw, _ := json.Marshal(&pb.SubmitResponse{Ok: true, TurnId: "turn-live", SessionId: "sess-live", LaunchId: "launch-live"})
			return &pb.TunnelMsg{Payload: raw}, nil
		},
	}
	obs := &fakeOutcomeReader{}
	sched := newTestScheduler(sub, s)
	sched.SetOutcomeReader(obs)
	sched.runDue(context.Background(), now)
	<-started

	// Reconcile running >60s after claim must skip the in-flight execution.
	sched.reconcile(context.Background(), now.Add(90*time.Second))
	pending, err := s.PendingExecutions()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v %v, want 1 still-pending execution", pending, err)
	}
	if pending[0].Status != "submitting" {
		t.Fatalf("in-flight execution status = %q, want submitting", pending[0].Status)
	}
	staleSnapshot := pending[0]

	// Finish delivery so TurnID is recorded, then attempt a stale Observe write
	// with empty TurnID; Store.Observe must refuse to clobber the receipt.
	close(release)
	sched.Wait()

	staleSnapshot.Status = "unknown"
	staleSnapshot.Reason = "admission_unconfirmed"
	if err := s.Observe(staleSnapshot, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	history, err := s.ExecutionHistory(row.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v %v", history, err)
	}
	if history[0].TurnID != "turn-live" || history[0].Status != "submitted" {
		t.Fatalf("stale Observe clobbered receipt: %+v", history[0])
	}
}

func TestSchedulerReconcileConvergesPermanentExpiredAndBacksOff(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	due := now.Unix()

	// 1) Crashed receipt without TaskID/TurnID converges after 60s.
	crashedRow, err := s.Create(Task{TargetID: "local", SessionName: "c1", Prompt: "a", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	next := now.Add(30 * time.Minute).Unix()
	if _, ok, err := s.ClaimTracked(crashedRow, now, &next); err != nil || !ok {
		t.Fatalf("claim crashed: %v %v", ok, err)
	}

	// 2) Permanent error (e.g. session identity mismatch) converges immediately.
	permRow, err := s.Create(Task{TargetID: "local", SessionName: "c2", Prompt: "b", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	permRun, ok, err := s.ClaimTracked(permRow, now, &next)
	if err != nil || !ok {
		t.Fatalf("claim perm: %v %v", ok, err)
	}
	permRun.TurnID = "turn-perm"
	permRun.SessionID = "sess-old"
	permRun.Status = "submitted"
	if err := s.Observe(permRun, now); err != nil {
		t.Fatal(err)
	}

	// 3) Transient error backs off, then converges after observeMaxAge.
	transRow, err := s.Create(Task{TargetID: "local", SessionName: "c3", Prompt: "c", Spec: "every 30m", Kind: KindInterval, Enabled: true, NextRunAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	transRun, ok, err := s.ClaimTracked(transRow, now, &next)
	if err != nil || !ok {
		t.Fatalf("claim trans: %v %v", ok, err)
	}
	transRun.TaskID = "task-transient"
	transRun.Status = "submitted"
	if err := s.Observe(transRun, now); err != nil {
		t.Fatal(err)
	}

	obs := &fakeOutcomeReader{
		fn: func(_ context.Context, r Execution) (Execution, error) {
			if r.TurnID == "turn-perm" {
				return r, ErrObservationPermanent
			}
			return r, errors.New("temporary network error")
		},
	}
	sched := newTestScheduler(&fakeSub{}, s)
	sched.SetOutcomeReader(obs)

	// At now+90s: crashed converges, perm converges, trans fails once and sets backoff.
	sched.reconcile(context.Background(), now.Add(90*time.Second))
	if obs.callCount() != 2 {
		t.Fatalf("first reconcile observer calls = %d, want 2", obs.callCount())
	}
	pending, err := s.PendingExecutions()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].TaskID != "task-transient" {
		t.Fatalf("pending after first reconcile = %+v, want only task-transient", pending)
	}
	if pending[0].ObserveFailures != 1 || pending[0].NextObserveAt <= now.Add(90*time.Second).Unix() {
		t.Fatalf("transient backoff not recorded: %+v", pending[0])
	}

	// Calling reconcile again within backoff window must NOT hit observer.
	sched.reconcile(context.Background(), now.Add(95*time.Second))
	if obs.callCount() != 2 {
		t.Fatalf("reconcile ignored backoff: calls = %d, want 2", obs.callCount())
	}

	// Past observeMaxAge: transient converges to terminal unknown.
	sched.reconcile(context.Background(), now.Add(observeMaxAge+time.Minute))
	pending, err = s.PendingExecutions()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after observeMaxAge = %+v, want 0", pending)
	}
}

type blockingTaskSubmitter struct {
	started chan struct{}
	release chan struct{}
	id      string
}

func (b *blockingTaskSubmitter) SubmitScheduledTask(_ context.Context, _ []byte) (string, error) {
	close(b.started)
	<-b.release
	return b.id, nil
}

func TestSchedulerStartDrainsWorkersOnCancel(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Add(-time.Second)
	due := now.Unix()
	row, err := s.Create(Task{
		Name:         "drain-test",
		ScheduleType: ScheduleTypeSubtask,
		Spec:         "every 30m",
		Kind:         KindInterval,
		TaskSpecJson: []byte(`{"agent":"builtin/codex","task":"review"}`),
		Enabled:      true,
		NextRunAt:    &due,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	fake := &blockingTaskSubmitter{started: started, release: release, id: "task-drain"}
	sched := newTestSchedulerWithTask(&fakeSub{}, s, fake)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sched.Start(ctx)
	}()

	// Trigger an immediate run and wait for worker to start.
	sched.runDue(ctx, time.Now())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}

	// Cancel Start context; Start must not return while worker is still finishing.
	cancel()
	select {
	case <-done:
		t.Fatal("Start returned before in-flight worker finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after worker finished")
	}
	runs, err := s.ListRuns(row.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].TaskID != "task-drain" {
		t.Fatalf("worker receipt lost during drain: %+v %v", runs, err)
	}
}
