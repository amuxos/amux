package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/sourceframe"
	"github.com/amuxos/amux/internal/store"
)

func TestManagerRestoresDurableTimelineAndStableEmptyEpoch(t *testing.T) {
	t.Parallel()

	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := NewManager(db)
	sessionWithHistory, err := mgr.Create("history", "codex")
	if err != nil {
		t.Fatal(err)
	}
	emptySession, err := mgr.Create("empty", "codex")
	if err != nil {
		t.Fatal(err)
	}
	emptyHead, emptyProbe, err := emptySession.AgentTimeline().Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	emptyProbe.Close()
	if _, err := sessionWithHistory.AgentTimeline().Publish(context.Background(), agentstream.SessionMetadataChanged{AgentSessionID: "native-1", EffectiveModel: "gpt-5"}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}
	turn, err := sessionWithHistory.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(ctx context.Context, turn *agentstream.Turn) error {
		_, err := turn.Finish(ctx, agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}, agentstream.Origin{Actor: agentstream.ActorAgent})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Ref().Seq != 1 {
		t.Fatalf("first turn seq = %d, want 1", turn.Ref().Seq)
	}

	reloaded := NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatalf("LoadFromDB() error = %v", err)
	}
	restored, err := reloaded.Get("history")
	if err != nil {
		t.Fatal(err)
	}
	head, history, err := restored.AgentTimeline().Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadHistory})
	if err != nil {
		t.Fatal(err)
	}
	if head.Cursor.Seq != 3 || head.Metadata == nil || head.Metadata.AgentSessionID != "native-1" {
		t.Fatalf("restored head = %#v, want seq 3 with metadata", head)
	}
	for want := uint64(1); want <= 3; want++ {
		event, err := history.Recv(context.Background())
		if err != nil || event.Cursor.Seq != want {
			t.Fatalf("history Recv() = (%#v, %v), want seq %d", event, err, want)
		}
	}
	if _, err := history.Recv(context.Background()); err != io.EOF {
		t.Fatalf("history EOF = %v", err)
	}
	history.Close()

	restoredEmpty, err := reloaded.Get("empty")
	if err != nil {
		t.Fatal(err)
	}
	restoredEmptyHead, restoredEmptyProbe, err := restoredEmpty.AgentTimeline().Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restoredEmptyProbe.Close()
	if restoredEmptyHead.Cursor != emptyHead.Cursor {
		t.Fatalf("empty session cursor changed across restart: got %#v want %#v", restoredEmptyHead.Cursor, emptyHead.Cursor)
	}
}

func TestManagerClosesRecoveredTurnOnceWithoutReplayingSideEffects(t *testing.T) {
	t.Parallel()

	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := NewManager(db)
	sess, err := mgr.Create("crashed-turn", "codex")
	if err != nil {
		t.Fatal(err)
	}
	dispatches := 0
	if _, err := sess.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error {
		dispatches++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reloaded := NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatal(err)
	}
	restored, err := reloaded.Get("crashed-turn")
	if err != nil {
		t.Fatal(err)
	}
	if restored.AgentTimeline().ActiveTurn() != nil {
		t.Fatal("dead recovered session retained an active turn")
	}
	if dispatches != 1 {
		t.Fatalf("restore replayed provider dispatch: %d", dispatches)
	}
	_, history, err := restored.AgentTimeline().Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadHistory})
	if err != nil {
		t.Fatal(err)
	}
	start, err := history.Recv(context.Background())
	if err != nil || start.Body.Kind() != agentstream.KindTurnStarted {
		t.Fatalf("first history event = (%#v, %v)", start, err)
	}
	finished, err := history.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, ok := finished.Body.(agentstream.TurnFinished)
	if !ok || body.Terminal.Outcome != agentstream.OutcomeInterrupted || body.Terminal.Code != "runtime_restart" {
		t.Fatalf("recovery terminal = %#v", finished.Body)
	}
	if _, err := history.Recv(context.Background()); err != io.EOF {
		t.Fatalf("history EOF = %v", err)
	}
	history.Close()

	reloadedAgain := NewManager(db)
	if err := reloadedAgain.LoadFromDB(); err != nil {
		t.Fatal(err)
	}
	again, _ := reloadedAgain.Get("crashed-turn")
	head, probe, err := again.AgentTimeline().Read(context.Background(), agentstream.ReadRequest{Mode: agentstream.ReadHistory})
	if err != nil {
		t.Fatal(err)
	}
	probe.Close()
	if head.Cursor.Seq != 2 {
		t.Fatalf("recovery terminal duplicated on second restart: head=%#v", head.Cursor)
	}
}

func TestManagerPersistsSessionCwd(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db)
	sess, err := mgr.Create("c1", "codex", "/workspace/amux")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}

	reloaded := NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatalf("load from db: %v", err)
	}
	got, err := reloaded.Get("c1")
	if err != nil {
		t.Fatalf("get reloaded session: %v", err)
	}
	if got.Cwd != "/workspace/amux" {
		t.Fatalf("cwd = %q, want /workspace/amux", got.Cwd)
	}
}

func TestManagerPersistsLaunchParamsAndPendingRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	mgr := NewManager(db)
	sess, err := mgr.Create("rs", "claude", "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	extraArgs := []string{"--model", "sonnet"}
	env := []string{"FOO=bar"}
	launchSpec := `{"agent_profile":"builtin/claude","agent_type":"claude"}`
	if err := mgr.RecordLaunchParams(sess, extraArgs, env, "git", "user", "builtin/claude", "sonnet", launchSpec); err != nil {
		t.Fatalf("record params: %v", err)
	}
	// Pretend the agent was running but its process is now dead. We mark
	// PID to a value that cannot be alive (0 PID would be skipped by
	// LoadFromDB's check), so use a high one and rely on the
	// "not alive" path.
	sess.PID = 999999
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	db.Close()

	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	mgr2 := NewManager(db2)
	if err := mgr2.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	got, err := mgr2.Get("rs")
	if err != nil {
		t.Fatalf("get reloaded: %v", err)
	}
	if !reflect.DeepEqual(got.ExtraArgs, extraArgs) {
		t.Fatalf("extra_args = %v, want %v", got.ExtraArgs, extraArgs)
	}
	if !reflect.DeepEqual(got.Env, env) {
		t.Fatalf("env = %v, want %v", got.Env, env)
	}
	if got.State != StateCreated {
		t.Fatalf("dead Running session should be reset to Created, got %s", got.State)
	}
	if got.AgentProfile != "builtin/claude" {
		t.Fatalf("agent_profile = %q, want builtin/claude", got.AgentProfile)
	}
	if got.EffectiveModel != "sonnet" {
		t.Fatalf("effective_model = %q, want sonnet", got.EffectiveModel)
	}
	if got.LaunchSpec != launchSpec {
		t.Fatalf("launch_spec = %q, want %q", got.LaunchSpec, launchSpec)
	}

	select {
	case s := <-mgr2.PendingRestart():
		if s.Name != "rs" {
			t.Fatalf("pending restart name = %s, want rs", s.Name)
		}
	default:
		t.Fatalf("expected session on PendingRestart channel")
	}
}

func TestManagerReloadsCreatedSessionForPendingRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	mgr := NewManager(db)
	sess, err := mgr.Create("interrupted-restart", "claude", "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RecordLaunchParams(
		sess,
		[]string{"-c", `approval_policy="never"`},
		[]string{"TOKEN=redacted"},
		"feishu",
		"bot-profile",
		"builtin/claude",
		"",
		`{"agent_profile":"builtin/claude","agent_type":"claude"}`,
	); err != nil {
		t.Fatalf("record params: %v", err)
	}
	if got := sess.StateSnapshot(); got != StateCreated {
		t.Fatalf("state before reload = %s, want created", got)
	}
	if _, err := mgr.Create("incomplete-create", "claude", "/tmp/work"); err != nil {
		t.Fatalf("create incomplete session: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	mgr2 := NewManager(db2)
	if err := mgr2.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	select {
	case got := <-mgr2.PendingRestart():
		if got.Name != "interrupted-restart" {
			t.Fatalf("pending restart name = %s, want interrupted-restart", got.Name)
		}
	default:
		t.Fatal("created session with durable launch parameters was not queued for restart")
	}
	select {
	case got := <-mgr2.PendingRestart():
		t.Fatalf("unexpected extra pending restart for %s", got.Name)
	default:
	}
}

func TestManagerQueuesEveryRecoverableCreatedSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	mgr := NewManager(db)
	const sessionCount = 20
	for i := 0; i < sessionCount; i++ {
		sess, err := mgr.Create(fmt.Sprintf("recover-%02d", i), "claude", "/tmp/work")
		if err != nil {
			t.Fatalf("create session %d: %v", i, err)
		}
		if err := mgr.RecordLaunchParams(
			sess,
			nil,
			nil,
			"feishu",
			"bot-profile",
			"builtin/claude",
			"",
			`{"agent_profile":"builtin/claude","agent_type":"claude"}`,
		); err != nil {
			t.Fatalf("record params %d: %v", i, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	mgr2 := NewManager(db2)
	if err := mgr2.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	seen := make(map[string]bool, sessionCount)
	for i := 0; i < sessionCount; i++ {
		select {
		case got := <-mgr2.PendingRestart():
			seen[got.Name] = true
		default:
			t.Fatalf("pending restart count = %d, want %d", len(seen), sessionCount)
		}
	}
	if len(seen) != sessionCount {
		t.Fatalf("unique pending restarts = %d, want %d", len(seen), sessionCount)
	}
}

func TestRecordRefreshedLaunchParamsClearsProviderSessionID(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := NewManager(db)
	sess, err := mgr.Create("refresh", "claude", "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RecordAgentSessionID(sess, "old-thread"); err != nil {
		t.Fatalf("record provider session: %v", err)
	}
	if err := mgr.RecordRefreshedLaunchParams(sess, []string{"--model", "new"}, []string{"KEEP=1"}, "cli", "local", "team/coder@new", "new", `{"agent_profile":"team/coder@new"}`); err != nil {
		t.Fatalf("record refreshed launch: %v", err)
	}
	got := sess.Snapshot()
	if got.AgentSessionID != "" {
		t.Fatalf("AgentSessionID = %q, want cleared", got.AgentSessionID)
	}
	if got.AgentProfile != "team/coder@new" || got.EffectiveModel != "new" {
		t.Fatalf("refreshed snapshot = %+v", got)
	}
}

func TestRecordReconfiguredLaunchParamsPreservesOnlySameRuntimeProviderSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := NewManager(db)
	sess, err := mgr.Create("reconfigure", "claude", "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RecordAgentSessionID(sess, "claude-thread"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordReconfiguredLaunchParams(sess, "claude", []string{"--model", "Example-Model"}, nil,
		"feishu", "singleton", "team/assistant", "Example-Model", `{}`, "", true); err != nil {
		t.Fatal(err)
	}
	if got := sess.Snapshot(); got.AgentSessionID != "claude-thread" || got.AgentType != "claude" {
		t.Fatalf("same-runtime snapshot = %+v", got)
	}
	if err := mgr.RecordReconfiguredLaunchParams(sess, "codex", []string{"--model", "example-model"}, nil,
		"feishu", "singleton", "team/assistant", "example-model", `{}`, "handoff", false); err != nil {
		t.Fatal(err)
	}
	if got := sess.Snapshot(); got.AgentSessionID != "" || got.AgentType != "codex" || got.HandoffContext != "handoff" {
		t.Fatalf("cross-runtime snapshot = %+v", got)
	}
}

func TestRestoreLaunchSnapshotRestoresProviderIdentityAndExecutionFields(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := NewManager(db)
	sess, err := mgr.Create("restore", "claude", "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RecordLaunchParams(sess, []string{"--model", "old"}, []string{"KEEP=old"},
		"feishu", "singleton", "team/assistant", "old", `{"old":true}`); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "old-thread"); err != nil {
		t.Fatal(err)
	}
	original := sess.Snapshot()
	if err := mgr.RecordReconfiguredLaunchParams(sess, "codex", []string{"--model", "new"}, []string{"KEEP=new"},
		"feishu", "singleton", "team/assistant", "new", `{"new":true}`, "handoff", false); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RestoreLaunchSnapshot(sess, original); err != nil {
		t.Fatalf("restore launch snapshot: %v", err)
	}
	got := sess.Snapshot()
	if got.AgentType != original.AgentType || got.AgentSessionID != original.AgentSessionID ||
		got.AgentProfile != original.AgentProfile || got.EffectiveModel != original.EffectiveModel ||
		got.LaunchSpec != original.LaunchSpec || got.HandoffContext != original.HandoffContext ||
		!reflect.DeepEqual(got.ExtraArgs, original.ExtraArgs) || !reflect.DeepEqual(got.Env, original.Env) {
		t.Fatalf("restored snapshot = %+v, want launch fields from %+v", got, original)
	}
}

func TestConversationHandoffUsesReplayThenDurableMessages(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mgr := NewManager(db)
	sess, err := mgr.Create("handoff", "claude", "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sess.AppendReplay(sourceframe.FormatInput(sourceframe.SourceLark, "live question"))
	sess.AppendReplay([]byte("live answer"))
	live, err := mgr.ConversationHandoff(sess)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"User: live question", "Assistant: live answer"} {
		if !strings.Contains(live, want) {
			t.Fatalf("live handoff = %q, want %q", live, want)
		}
	}
	if err := mgr.RecordAgentEvent(sess, agentstream.Event{Body: agentstream.TurnStarted{InputSummary: "durable question"}, Origin: agentstream.Origin{Channel: sourceframe.SourceLark}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentEvent(sess, agentstream.Event{Body: agentstream.ResultObserved{Text: "durable answer"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	mgr2 := NewManager(db2)
	if err := mgr2.LoadFromDB(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := mgr2.Get("handoff")
	if err != nil {
		t.Fatal(err)
	}
	durable, err := mgr2.ConversationHandoff(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"User: durable question", "Assistant: durable answer"} {
		if !strings.Contains(durable, want) {
			t.Fatalf("durable handoff = %q, want %q", durable, want)
		}
	}
}

func TestManagerPersistsSleepingSessionWithoutPendingRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	mgr := NewManager(db)
	sess, err := mgr.Create("nap", "codex", "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sess.PID = 1234
	sess.BrokerSocket = "/tmp/amux.sock"
	if err := mgr.SetSleeping(sess); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	db.Close()

	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	mgr2 := NewManager(db2)
	if err := mgr2.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := mgr2.Get("nap")
	if err != nil {
		t.Fatalf("get reloaded: %v", err)
	}
	if got.State != StateSleeping || got.PID != 0 || got.BrokerSocket != "" {
		t.Fatalf("sleeping session = state %s pid %d socket %q, want sleeping/0/empty", got.State, got.PID, got.BrokerSocket)
	}
	select {
	case s := <-mgr2.PendingRestart():
		t.Fatalf("sleeping session should not auto restart, got %s", s.Name)
	default:
	}
}

func TestManagerPersistsIdlePolicyActivityAndSleepReason(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	mgr := NewManager(db)
	sess, err := mgr.CreateWithOptions("nap", "codex", CreateOptions{
		Cwd:                "/tmp/work",
		IdleTimeoutSeconds: 300,
		IdleTimeoutSet:     true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	activity := time.Unix(2000, 0)
	if err := mgr.RecordActivity(sess, activity, true); err != nil {
		t.Fatalf("record activity: %v", err)
	}
	if err := mgr.SetSleeping(sess, SleepReasonIdleTimeout); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	db.Close()

	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	mgr2 := NewManager(db2)
	if err := mgr2.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := mgr2.Get("nap")
	if err != nil {
		t.Fatalf("get reloaded: %v", err)
	}
	if got.IdleTimeoutSeconds != 300 {
		t.Fatalf("IdleTimeoutSeconds = %d, want 300", got.IdleTimeoutSeconds)
	}
	if !got.IdleTimeoutSet {
		t.Fatal("IdleTimeoutSet = false, want true")
	}
	if got.LastActivityAt.Before(activity) {
		t.Fatalf("LastActivityAt = %s, want not before %s", got.LastActivityAt, activity)
	}
	if got.LastSleepReason != SleepReasonIdleTimeout {
		t.Fatalf("LastSleepReason = %q, want %q", got.LastSleepReason, SleepReasonIdleTimeout)
	}
}

func TestLoadFromDBSleepsUnrecoverableDeadRunningSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db)
	sess, err := mgr.Create("unrecoverable", "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	sess.SetProc(deadTestProc{pid: os.Getpid() + 100000})
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("persist dead running pid: %v", err)
	}

	mgr2 := NewManager(db)
	if err := mgr2.LoadFromDB(); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	got, err := mgr2.Get("unrecoverable")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.State != StateSleeping {
		t.Fatalf("state = %s, want %s", got.State, StateSleeping)
	}
	if got.LastSleepReason != SleepReasonRestartFailed {
		t.Fatalf("LastSleepReason = %q, want %q", got.LastSleepReason, SleepReasonRestartFailed)
	}
	if got.PID != 0 {
		t.Fatalf("PID = %d, want 0", got.PID)
	}
	if got.BrokerSocket != "" {
		t.Fatalf("BrokerSocket = %q, want empty", got.BrokerSocket)
	}
}

type deadTestProc struct{ pid int }

func (deadTestProc) Close() error                                  { return nil }
func (deadTestProc) Interrupt() error                              { return nil }
func (deadTestProc) Write(_ []byte) (int, error)                   { return 0, nil }
func (deadTestProc) RespondPermission(string, string) error        { return nil }
func (deadTestProc) RespondPermissionMulti(string, []string) error { return nil }
func (deadTestProc) SetEventSink(func(agentstream.ProducerEmission) bool) {
}
func (deadTestProc) DispatchTurn(context.Context, TurnDispatchRequest, func(agentstream.ProducerEmission) bool) error {
	return nil
}
func (p deadTestProc) PID() int { return p.pid }

func TestSetArchivedAfterProcessExitPreservesSleepingSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db)
	sess, err := mgr.Create("nap", "claude", "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if err := mgr.SetSleeping(sess); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}

	if err := mgr.SetArchivedAfterProcessExit(sess); err != nil {
		t.Fatalf("archive after process exit: %v", err)
	}
	if sess.State != StateSleeping {
		t.Fatalf("state = %s, want sleeping", sess.State)
	}

	reloaded := NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := reloaded.Get("nap")
	if err != nil {
		t.Fatalf("get reloaded: %v", err)
	}
	if got.State != StateSleeping {
		t.Fatalf("persisted state = %s, want sleeping", got.State)
	}
}

func TestRecordAgentSessionIDPersistsAndEventTap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	mgr := NewManager(db)
	sess, err := mgr.Create("a1", "claude", "/tmp/work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Tap forwards the exact accepted canonical metadata event to the manager.
	sess.SetEventTap(func(ev agentstream.Event) bool {
		_ = mgr.RecordAgentEvent(sess, ev)
		return false
	})
	sess.recordProducerEmission(agentstream.ProducerEmission{
		Session: agentstream.SessionMetadataChanged{AgentSessionID: "claude-uuid-1"},
	})
	if sess.AgentSessionID != "claude-uuid-1" {
		t.Fatalf("in-memory id = %q, want claude-uuid-1", sess.AgentSessionID)
	}
	db.Close()

	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	mgr2 := NewManager(db2)
	if err := mgr2.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := mgr2.Get("a1")
	if err != nil {
		t.Fatalf("get reloaded: %v", err)
	}
	if got.AgentSessionID != "claude-uuid-1" {
		t.Fatalf("persisted id = %q, want claude-uuid-1", got.AgentSessionID)
	}
}

func TestRecordAgentEventFillsOnlyMissingEffectiveModel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	mgr := NewManager(db)
	unknown, err := mgr.Create("unknown-model", "claude", "/tmp/unknown-model")
	if err != nil {
		t.Fatalf("create unknown-model: %v", err)
	}
	if err := mgr.RecordAgentEvent(unknown, agentstream.Event{Body: agentstream.SessionMetadataChanged{
		AgentSessionID: "thread-1",
		EffectiveModel: " Observed-Model ",
	}}); err != nil {
		t.Fatalf("record provider metadata: %v", err)
	}
	if got := unknown.Snapshot(); got.AgentSessionID != "thread-1" || got.EffectiveModel != "Observed-Model" {
		t.Fatalf("unknown-model snapshot = %+v, want provider metadata persisted", got)
	}

	explicit, err := mgr.Create("explicit-model", "claude", "/tmp/explicit-model")
	if err != nil {
		t.Fatalf("create explicit-model: %v", err)
	}
	if err := mgr.RecordLaunchParams(explicit, nil, nil, "", "", "builtin/claude", "Configured-Model", ""); err != nil {
		t.Fatalf("record explicit launch model: %v", err)
	}
	if err := mgr.RecordAgentEvent(explicit, agentstream.Event{Body: agentstream.SessionMetadataChanged{EffectiveModel: "Observed-Model"}}); err != nil {
		t.Fatalf("record provider metadata for explicit model: %v", err)
	}
	if got := explicit.Snapshot().EffectiveModel; got != "Configured-Model" {
		t.Fatalf("explicit effective model = %q, want Configured-Model", got)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer db.Close()
	reloaded := NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err := reloaded.Get("unknown-model")
	if err != nil {
		t.Fatalf("get reloaded unknown-model: %v", err)
	}
	if got.Snapshot().EffectiveModel != "Observed-Model" {
		t.Fatalf("reloaded effective model = %q, want Observed-Model", got.Snapshot().EffectiveModel)
	}
}

func TestPruneArchivedRemovesOnlyArchivedSessions(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db)
	// One running session (keep) and two archived (prune).
	live, _ := mgr.Create("live", "codex", "/w/live")
	if err := mgr.SetRunning(live); err != nil {
		t.Fatalf("set running: %v", err)
	}
	for _, name := range []string{"dead1", "dead2"} {
		s, err := mgr.Create(name, "claude", "/w/"+name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := mgr.SetState(s, StateArchived); err != nil {
			t.Fatalf("archive %s: %v", name, err)
		}
	}
	// An archived task-owned child session must be preserved: it is reaped by
	// the task lifecycle, never by user-facing session prune.
	child, err := mgr.CreateOwned("subtask", "claude", "task-1", "", "/w/subtask")
	if err != nil {
		t.Fatalf("create owned: %v", err)
	}
	if err := mgr.SetState(child, StateArchived); err != nil {
		t.Fatalf("archive owned child: %v", err)
	}

	pruned, err := mgr.PruneArchived()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 2 {
		t.Fatalf("pruned %d sessions, want 2", len(pruned))
	}
	for _, p := range pruned {
		if p.OwnerTaskId != "" {
			t.Fatalf("prune removed task-owned child %q, want it preserved", p.Name)
		}
	}

	// In-memory: the running session and the archived owned child remain.
	if _, err := mgr.Get("subtask"); err != nil {
		t.Fatalf("archived task-owned child should survive session prune: %v", err)
	}
	if _, err := mgr.Get("dead1"); err == nil {
		t.Fatalf("dead1 should be gone from manager")
	}

	// DB: a fresh manager loading from disk sees the running session and the
	// preserved owned child, but not the pruned user sessions.
	reloaded := NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := reloaded.Get("live"); err != nil {
		t.Fatalf("reloaded live missing: %v", err)
	}
	if _, err := reloaded.Get("subtask"); err != nil {
		t.Fatalf("reloaded owned child missing: %v", err)
	}
	if _, err := reloaded.Get("dead1"); err == nil {
		t.Fatalf("reloaded dead1 should be gone")
	}

	// Pruning again is a no-op.
	again, err := mgr.PruneArchived()
	if err != nil {
		t.Fatalf("prune again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second prune removed %d, want 0", len(again))
	}
}

func TestDeleteArchivedOwnedOnlyDeletesMatchingArchivedChild(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	m := NewManager(db)
	owned, err := m.CreateOwned("child", "claude", "task-1", "")
	if err != nil {
		t.Fatalf("CreateOwned: %v", err)
	}
	user, err := m.Create("user", "claude")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.SetState(owned, StateArchived); err != nil {
		t.Fatalf("archive owned: %v", err)
	}
	if err := m.SetState(user, StateArchived); err != nil {
		t.Fatalf("archive user: %v", err)
	}
	if _, deleted, err := m.DeleteArchivedOwned("task-2", owned.ID); err != nil || deleted {
		t.Fatalf("owner mismatch deleted=%v err=%v", deleted, err)
	}
	if _, deleted, err := m.DeleteArchivedOwned("task-1", user.ID); err != nil || deleted {
		t.Fatalf("user session deleted=%v err=%v", deleted, err)
	}
	if sess, deleted, err := m.DeleteArchivedOwned("task-1", owned.ID); err != nil || !deleted || sess.Name != "child" {
		t.Fatalf("delete owned sess=%+v deleted=%v err=%v", sess, deleted, err)
	}
	if _, err := m.Get(owned.ID); err == nil {
		t.Fatal("owned session still exists")
	}
	if _, err := m.Get(user.ID); err != nil {
		t.Fatalf("user session removed: %v", err)
	}
}

func TestCreateOwnedFailureDoesNotPublishSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Reject persistence of owned sessions regardless of whether ownership is
	// written on INSERT or UPDATE. A failed creation must leave no visible state.
	for _, operation := range []string{"INSERT", "UPDATE"} {
		_, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_owned_%s BEFORE %s ON sessions
			WHEN NEW.owner_task_id <> '' BEGIN SELECT RAISE(ABORT, 'injected owner write failure'); END`, operation, operation))
		if err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(db)
	changed := m.WatchSessions()
	if sess, err := m.CreateOwned("owned", "codex", "task-1", "instance-1"); err == nil || sess != nil {
		t.Fatalf("CreateOwned() = (%v, %v), want nil and persistence error", sess, err)
	}
	if _, err := m.Get("owned"); err == nil {
		t.Error("failed creation remains queryable")
	}
	if got := m.List(); len(got) != 0 {
		t.Errorf("List() contains %d sessions after failed creation", len(got))
	}
	select {
	case <-changed:
		t.Error("failed creation notified session watchers")
	default:
	}
	reloaded := NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.List(); len(got) != 0 {
		t.Errorf("restored %d sessions after failed creation", len(got))
	}
}

// TestCreateOwnedPersistsAgentInstanceID verifies the agent_instance_id is
// persisted atomically with creation and survives a manager reload, so a
// follow-up task for the same instance can find the session.
func TestCreateOwnedPersistsAgentInstanceID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// The durable record must have its ownership from the first write, even if
	// the runtime stops immediately after creation becomes visible.
	if _, err := db.Exec(`CREATE TRIGGER require_initial_owner BEFORE INSERT ON sessions
		WHEN NEW.owner_task_id <> 'task-1' OR NEW.agent_instance_id <> 'i1'
		BEGIN SELECT RAISE(ABORT, 'initial ownership missing'); END`); err != nil {
		t.Fatal(err)
	}
	m := NewManager(db)
	changed := m.WatchSessions()
	sess, err := m.CreateOwned("instance-i1", "claude", "task-1", "i1", "/w")
	if err != nil {
		t.Fatalf("CreateOwned: %v", err)
	}
	if sess.AgentInstanceID != "i1" {
		t.Fatalf("in-memory AgentInstanceID = %q, want i1", sess.AgentInstanceID)
	}
	// Snapshot reflects it.
	if got := sess.Snapshot().AgentInstanceID; got != "i1" {
		t.Fatalf("snapshot AgentInstanceID = %q, want i1", got)
	}
	select {
	case <-changed:
	default:
		t.Fatal("successful creation did not notify session watchers")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reload from disk.
	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	m2 := NewManager(db2)
	if err := m2.LoadFromDB(); err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	reloaded, err := m2.Get("instance-i1")
	if err != nil {
		t.Fatalf("reload Get: %v", err)
	}
	if reloaded.AgentInstanceID != "i1" {
		t.Fatalf("reloaded AgentInstanceID = %q, want i1", reloaded.AgentInstanceID)
	}
	if reloaded.OwnerTaskId != "task-1" {
		t.Fatalf("reloaded OwnerTaskId = %q, want task-1", reloaded.OwnerTaskId)
	}
}

// TestFindOwnedByName verifies lookup of an existing non-archived session by
// name, used by the persistent-agent path to reuse a sleeping child session.
func TestFindOwnedByName(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	m := NewManager(db)

	if _, ok := m.FindOwnedByName("absent"); ok {
		t.Fatal("FindOwnedByName(absent) should be false")
	}

	sess, err := m.CreateOwned("instance-i2", "claude", "task-1", "i2")
	if err != nil {
		t.Fatalf("CreateOwned: %v", err)
	}
	got, ok := m.FindOwnedByName("instance-i2")
	if !ok || got.ID != sess.ID {
		t.Fatalf("FindOwnedByName(instance-i2) = %+v ok=%v, want %s", got, ok, sess.ID)
	}

	// An archived session is not returned (it is not reusable).
	if err := m.SetState(sess, StateArchived); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, ok := m.FindOwnedByName("instance-i2"); ok {
		t.Fatal("FindOwnedByName must not return an archived session")
	}
}

func TestRecordAgentEventPersistsInteractionClockThroughActivityThrottle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db)
	sess, err := mgr.Create("active", "claude")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	oldInteraction := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	sess.activityMu.Lock()
	sess.LastActivityAt = oldInteraction
	sess.LastInteractionAt = oldInteraction
	sess.lastActivityPersist = oldInteraction
	sess.activityMu.Unlock()
	if err := db.UpdateSessionActivity(sess.ID, oldInteraction.Unix(), oldInteraction.Unix()); err != nil {
		t.Fatalf("seed old activity: %v", err)
	}

	// Drive the canonical observer path that persists last_interaction_at.
	event := agentstream.Event{At: time.Now(), Body: agentstream.AssistantDelta{Text: "x"}, Origin: agentstream.Origin{Actor: agentstream.ActorAgent, Channel: "agent"}}
	sess.noteCanonicalActivity(event, event.At)
	if err := mgr.RecordAgentEvent(sess, event); err != nil {
		t.Fatalf("record agent event: %v", err)
	}

	// Reload from disk via fromRow.
	reloaded := NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatalf("load from db: %v", err)
	}
	got, err := reloaded.Get("active")
	if err != nil {
		t.Fatalf("get reloaded session: %v", err)
	}
	if reloadedAt := got.InteractionAt().Unix(); reloadedAt <= oldInteraction.Unix() {
		t.Fatalf("reloaded InteractionAt = %d, want > %d; event activity path did not persist last_interaction_at", reloadedAt, oldInteraction.Unix())
	}
}

func TestRecordAgentEventDoesNotBumpInteractionClockForAgentSessionIDMetadata(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := NewManager(db)
	sess, err := mgr.Create("active", "codex")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	oldInteraction := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	sess.activityMu.Lock()
	sess.LastActivityAt = oldInteraction
	sess.LastInteractionAt = oldInteraction
	sess.lastActivityPersist = oldInteraction
	sess.activityMu.Unlock()
	if err := db.UpdateSessionActivity(sess.ID, oldInteraction.Unix(), oldInteraction.Unix()); err != nil {
		t.Fatalf("seed old activity: %v", err)
	}

	if err := mgr.RecordAgentEvent(sess, agentstream.Event{Body: agentstream.SessionMetadataChanged{AgentSessionID: "native-1"}}); err != nil {
		t.Fatalf("record agent event: %v", err)
	}

	reloaded := NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatalf("load from db: %v", err)
	}
	got, err := reloaded.Get("active")
	if err != nil {
		t.Fatalf("get reloaded session: %v", err)
	}
	if got.AgentSessionID != "native-1" {
		t.Fatalf("AgentSessionID = %q, want native-1", got.AgentSessionID)
	}
	if reloadedAt := got.InteractionAt().Unix(); reloadedAt != oldInteraction.Unix() {
		t.Fatalf("reloaded InteractionAt = %d, want unchanged %d", reloadedAt, oldInteraction.Unix())
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return NewManager(db)
}

func TestSetArchivedFromSleeping(t *testing.T) {
	mgr := newTestManager(t)
	sess, err := mgr.Create("arch-1", "claude")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.SetArchived(sess, "idle"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got := sess.StateSnapshot(); got != StateArchived {
		t.Fatalf("state = %v, want archived", got)
	}
}

func TestPruneArchivedOlderThanRespectsThresholdAndOwner(t *testing.T) {
	mgr := newTestManager(t)
	now := time.Unix(2_000_000, 0)

	old, _ := mgr.Create("old", "claude")
	_ = mgr.SetArchived(old, "idle")
	old.LastInteractionAt = now.Add(-200 * time.Hour)

	fresh, _ := mgr.Create("fresh", "claude")
	_ = mgr.SetArchived(fresh, "idle")
	fresh.LastInteractionAt = now.Add(-1 * time.Hour)

	owned, _ := mgr.CreateOwned("owned", "claude", "task-9", "")
	_ = mgr.SetArchived(owned, "idle")
	owned.LastInteractionAt = now.Add(-300 * time.Hour)

	pruned, err := mgr.PruneArchivedOlderThan(now, 120*time.Hour, nil)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0].Name != "old" {
		t.Fatalf("pruned = %v, want [old]", pruned)
	}
}
