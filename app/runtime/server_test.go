package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/provider"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanonicalAgentEventsProduceProviderIndependentPresentationActions(t *testing.T) {
	ref := &agentstream.TurnRef{ID: "turn-contract", Seq: 1}
	bodies := []agentstream.Body{
		agentstream.TurnStarted{InputSummary: "fix it"},
		agentstream.AssistantDelta{Text: "checking"},
		agentstream.ToolChanged{Phase: agentstream.ToolPhaseStarted, Tool: agentstream.Tool{ID: "tool-1", Name: "Read"}},
		agentstream.ToolChanged{Phase: agentstream.ToolPhaseCompleted, Tool: agentstream.Tool{ID: "tool-1", Name: "Read"}},
		agentstream.ResultObserved{Text: "done"},
		agentstream.TurnFinished{Terminal: agentstream.Terminal{Outcome: agentstream.OutcomeSuccess}},
	}
	var baseline []agentui.ActionKind
	for _, providerName := range []string{"acp", "codex", "claude"} {
		presenter := agentui.NewPresenter()
		var got []agentui.ActionKind
		for i, body := range bodies {
			event := agentstream.Event{Cursor: agentstream.Cursor{Epoch: "epoch-1", Seq: uint64(i + 1)}, Turn: ref, Origin: agentstream.Origin{Provider: providerName}, Body: body}
			for _, normalized := range agentui.FromAgentEvent(event) {
				for _, action := range presenter.Consume(normalized) {
					got = append(got, action.Kind)
				}
			}
		}
		if baseline == nil {
			baseline = got
			continue
		}
		if !reflect.DeepEqual(got, baseline) {
			t.Fatalf("%s actions = %v, want %v", providerName, got, baseline)
		}
	}
}

func TestPrepareExecutionRejectsPinnedLinuxDACWithoutIsolationRuntime(t *testing.T) {
	srv := &server{amuxDir: t.TempDir()}
	identity := principal.ExecutionIdentity{
		IdentityMode:         principal.IdentityModeIdentified,
		EffectivePrincipalID: "principal-1",
		IsolationBackend:     principal.BackendLinuxDAC,
	}
	if _, _, _, _, err := srv.prepareExecution(context.Background(), identity, "launch-1", &pb.RunRequest{Name: "session"}); err == nil || !strings.Contains(err.Error(), "isolation runtime is unavailable") {
		t.Fatalf("prepareExecution error = %v", err)
	}
}

type serverTestProc struct {
	writes              [][]byte
	controls            []string
	reconfigurations    []string
	permissionResponses []string
	closed              bool
	writeErr            error
	onWriteData         func([]byte)
	eventSink           func(agentstream.ProducerEmission) bool
	turnSink            func(agentstream.ProducerEmission) bool
	holdTurnOpen        bool
}

func (p *serverTestProc) PID() int { return 4321 }

func (p *serverTestProc) DispatchTurn(_ context.Context, req session.TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
	p.turnSink = sink
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
	if p.holdTurnOpen {
		return nil
	}
	sink(testSuccessEmission())
	return nil
}

func (p *serverTestProc) Write(data []byte) (int, error) {
	p.writes = append(p.writes, append([]byte(nil), data...))
	if p.onWriteData != nil {
		p.onWriteData(data)
	}
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	return len(data), nil
}

func (p *serverTestProc) Interrupt() error { return nil }

func (p *serverTestProc) Control(action string) error {
	p.controls = append(p.controls, action)
	return nil
}

func (p *serverTestProc) ReconfigureExecution(model, reasoningEffort string) error {
	p.reconfigurations = append(p.reconfigurations, model+":"+reasoningEffort)
	return nil
}

func (p *serverTestProc) RespondPermission(permissionID, optionID string) error {
	p.permissionResponses = append(p.permissionResponses, permissionID+":"+optionID)
	return nil
}

func (p *serverTestProc) RespondPermissionMulti(permissionID string, optionIDs []string) error {
	p.permissionResponses = append(p.permissionResponses, permissionID+":"+strings.Join(optionIDs, ","))
	return nil
}

func (p *serverTestProc) SetEventSink(sink func(agentstream.ProducerEmission) bool) {
	p.eventSink = sink
}

func (p *serverTestProc) Close() error {
	p.closed = true
	return nil
}

func TestResolveChangedSessionCwdUsesCurrentSessionForRelativePath(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "project-a")
	want := filepath.Join(root, "project-b")
	if err := os.MkdirAll(current, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(want, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveChangedSessionCwd(current, "../project-b")
	if err != nil {
		t.Fatalf("resolveChangedSessionCwd: %v", err)
	}
	if got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
}

func TestChangeCwdPreservesSessionAndProviderConversation(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	oldCwd := filepath.Join(dir, "project-a")
	newCwd := filepath.Join(dir, "project-b")
	for _, path := range []string{oldCwd, newCwd} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := mgr.Create("work", "codex", oldCwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "local", "", "", launchSpecSnapshot(&pb.RunRequest{
		Name: "work", AgentType: "codex", Cwd: oldCwd, WorkspaceSource: "cli", WorkspaceScope: "local",
	})); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "thread-123"); err != nil {
		t.Fatal(err)
	}
	oldProc := &serverTestProc{}
	sess.SetProc(oldProc)
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}
	sessionID := sess.ID
	var cwdAtStart, providerIDAtStart string
	srv := &server{
		mgr:                  mgr,
		amuxDir:              dir,
		disableMCPAutoInject: true,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _, _ []string) error {
			snap := sess.Snapshot()
			cwdAtStart = snap.Cwd
			providerIDAtStart = snap.AgentSessionID
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	resp, err := srv.ChangeCwd(context.Background(), &pb.ChangeCwdRequest{
		Name: "work", Cwd: "../project-b", Wait: true, ExpectedCwd: oldCwd,
	})
	if err != nil {
		t.Fatalf("ChangeCwd: %v", err)
	}
	if !oldProc.closed {
		t.Fatal("old provider process was not closed")
	}
	if cwdAtStart != newCwd || providerIDAtStart != "thread-123" {
		t.Fatalf("relaunch cwd=%q provider=%q, want %q thread-123", cwdAtStart, providerIDAtStart, newCwd)
	}
	got := sess.Snapshot()
	if got.ID != sessionID || got.Cwd != newCwd || got.AgentSessionID != "thread-123" || got.State != session.StateRunning {
		t.Fatalf("session after switch = %+v", got)
	}
	if resp.PreviousCwd != oldCwd || resp.EffectiveCwd != newCwd || !resp.Changed || resp.Session.CwdSwitch != "resume" {
		t.Fatalf("response = %+v", resp)
	}
	if launchSpecExplicitCwd(got.LaunchSpec) != newCwd {
		t.Fatalf("launch spec cwd = %q, want %q", launchSpecExplicitCwd(got.LaunchSpec), newCwd)
	}
}

func TestChangeCwdRejectsPendingInput(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	oldCwd := filepath.Join(dir, "project-a")
	newCwd := filepath.Join(dir, "project-b")
	for _, path := range []string{oldCwd, newCwd} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := mgr.Create("work", "codex", oldCwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "thread-123"); err != nil {
		t.Fatal(err)
	}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	sess.SetProc(&serverTestProc{onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}})
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}
	if err := sess.SubmitAsync("cli", "first"); err != nil {
		t.Fatal(err)
	}
	<-firstCRSeen
	if err := sess.SubmitAsync("cli", "queued"); err != nil {
		t.Fatal(err)
	}

	_, err = (&server{mgr: mgr}).ChangeCwd(context.Background(), &pb.ChangeCwdRequest{
		Name: "work", Cwd: newCwd, Wait: true,
	})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "queued input") {
		t.Fatalf("ChangeCwd error = %v, want queued-input FailedPrecondition", err)
	}
	if got := sess.Snapshot().Cwd; got != oldCwd {
		t.Fatalf("cwd after rejected switch = %q, want %q", got, oldCwd)
	}
	close(releaseFirst)
}

func TestChangeCwdDeferredRunsBeforePendingInput(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	oldCwd := filepath.Join(dir, "project-a")
	newCwd := filepath.Join(dir, "project-b")
	for _, path := range []string{oldCwd, newCwd} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := mgr.Create("work", "codex", oldCwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "local", "", "", launchSpecSnapshot(&pb.RunRequest{
		Name: "work", AgentType: "codex", Cwd: oldCwd, WorkspaceSource: "cli", WorkspaceScope: "local",
	})); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "thread-123"); err != nil {
		t.Fatal(err)
	}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var firstOnce sync.Once
	oldProc := &serverTestProc{onWriteData: func(data []byte) {
		if string(data) == "\r" {
			firstOnce.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}}
	sess.SetProc(oldProc)
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}
	if err := sess.SubmitAsync("lark", "first"); err != nil {
		t.Fatal(err)
	}
	<-firstCRSeen
	if err := sess.SubmitAsync("lark", "queued"); err != nil {
		t.Fatal(err)
	}

	queuedRan := make(chan struct{})
	var queuedOnce sync.Once
	srv := &server{
		mgr:                  mgr,
		amuxDir:              dir,
		disableMCPAutoInject: true,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _, _ []string) error {
			sess.SetProc(&serverTestProc{onWriteData: func(data []byte) {
				if string(data) == "queued" {
					queuedOnce.Do(func() { close(queuedRan) })
				}
			}})
			return mgr.SetRunning(sess)
		},
	}
	resp, err := srv.ChangeCwd(context.Background(), &pb.ChangeCwdRequest{
		Name: "work", Cwd: newCwd, ExpectedCwd: oldCwd, DeferUntilIdle: true,
	})
	if err != nil {
		t.Fatalf("ChangeCwd deferred: %v", err)
	}
	if !resp.Deferred || resp.Changed || resp.PreviousCwd != oldCwd || resp.EffectiveCwd != newCwd {
		t.Fatalf("deferred response = %+v", resp)
	}
	items := sess.QueueSnapshot()
	if len(items) != 2 || items[0].Control != "cwd" || items[1].Text != "queued" {
		t.Fatalf("queue order = %+v, want cwd lifecycle before queued prompt", items)
	}

	close(releaseFirst)
	select {
	case <-queuedRan:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for deferred switch and queued prompt")
	}
	got := sess.Snapshot()
	if !oldProc.closed || got.Cwd != newCwd || got.AgentSessionID != "thread-123" || got.State != session.StateRunning {
		t.Fatalf("session after deferred switch = %+v, old proc closed=%v", got, oldProc.closed)
	}
}

func TestChangeCwdRestoresPreviousWorkspaceWhenRelaunchFails(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	oldCwd := filepath.Join(dir, "old")
	newCwd := filepath.Join(dir, "new")
	for _, path := range []string{oldCwd, newCwd} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := mgr.Create("work", "codex", oldCwd)
	if err != nil {
		t.Fatal(err)
	}
	oldSpec := launchSpecSnapshot(&pb.RunRequest{Name: "work", AgentType: "codex", Cwd: oldCwd})
	if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "local", "", "", oldSpec); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "thread-123"); err != nil {
		t.Fatal(err)
	}
	sess.SetProc(&serverTestProc{})
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}
	starts := 0
	srv := &server{
		mgr:                  mgr,
		amuxDir:              dir,
		disableMCPAutoInject: true,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _, _ []string) error {
			starts++
			if sess.Snapshot().Cwd == newCwd {
				return errors.New("new workspace rejected")
			}
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	_, err = srv.ChangeCwd(context.Background(), &pb.ChangeCwdRequest{Name: "work", Cwd: newCwd, Wait: true})
	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("ChangeCwd error = %v, want restored Internal error", err)
	}
	got := sess.Snapshot()
	if starts != 2 || got.Cwd != oldCwd || got.LaunchSpec != oldSpec || got.AgentSessionID != "thread-123" || got.State != session.StateRunning {
		t.Fatalf("restored session starts=%d snapshot=%+v", starts, got)
	}
}

func TestPruneRemovesArchivedSessionsAndSocketFiles(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	s := &server{mgr: mgr, amuxDir: dir}

	sess, err := mgr.Create("dead", "claude", filepath.Join(dir, "w"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simulate leftover broker socket + pid files for the dead session, using
	// the production sessions-dir layout: socket <sid>.acp.sock and pid file
	// <sid>.pid (both broker protocols write the pid to <sid>.pid, not
	// <sid>.acp.pid).
	sessFilesDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessFilesDir, 0700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(sessFilesDir, sess.ID+".acp.sock")
	pid := filepath.Join(sessFilesDir, sess.ID+".pid")
	if err := os.WriteFile(sock, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pid, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	sessionDir := profileSessionDir(dir, sess.ID)
	artifact := filepath.Join(sessionDir, "artifact.txt")
	if err := os.MkdirAll(filepath.Dir(artifact), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sess.BrokerSocket = sock
	if err := mgr.SetState(sess, session.StateArchived); err != nil {
		t.Fatalf("archive: %v", err)
	}

	resp, err := s.Prune(context.Background(), &pb.PruneRequest{})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if resp.Count != 1 || len(resp.Names) != 1 || resp.Names[0] != "dead" {
		t.Fatalf("resp = %+v, want count=1 names=[dead]", resp)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("broker socket not removed: %v", err)
	}
	if _, err := os.Stat(pid); !os.IsNotExist(err) {
		t.Fatalf("broker pid file not removed: %v", err)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session profile artifacts not removed: %v", err)
	}
	if list := mgr.List(); len(list) != 0 {
		t.Fatalf("manager still has sessions after prune: %+v", list)
	}
}

// archiveOwned creates an archived task-owned child session for reaping tests.
func archiveOwned(t *testing.T, mgr *session.Manager, name, ownerTaskID, cwd string) *session.Session {
	t.Helper()
	sess, err := mgr.CreateOwned(name, "codex", ownerTaskID, "", cwd)
	if err != nil {
		t.Fatalf("create owned %s: %v", name, err)
	}
	if err := mgr.SetState(sess, session.StateArchived); err != nil {
		t.Fatalf("archive %s: %v", name, err)
	}
	return sess
}

func TestPruneReapsOrphanChildSessionsButKeepsLiveOnes(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)

	live := map[string]bool{"task-live": true} // only this owner task still exists
	s := &server{mgr: mgr, amuxDir: dir, taskExists: func(id string) (bool, error) {
		return live[id], nil
	}}

	// (a) orphan: owner task gone -> should be reaped, incl. its sandbox dir.
	archiveOwned(t, mgr, "subtask-orphan", "task-orphan", filepath.Join(dir, "wo"))
	orphanSandbox := filepath.Join(task.SandboxesDir(dir), "task-orphan")
	if err := os.MkdirAll(orphanSandbox, 0700); err != nil {
		t.Fatal(err)
	}
	// (b) live task child: owner task still exists -> must be preserved.
	archiveOwned(t, mgr, "subtask-live", "task-live", filepath.Join(dir, "wl"))
	// (c) ordinary archived user session -> removed by PruneArchived path.
	user, err := mgr.Create("user-dead", "claude", filepath.Join(dir, "wu"))
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := mgr.SetState(user, session.StateArchived); err != nil {
		t.Fatalf("archive user: %v", err)
	}

	resp, err := s.Prune(context.Background(), &pb.PruneRequest{})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}

	got := map[string]bool{}
	for _, n := range resp.Names {
		got[n] = true
	}
	if !got["user-dead"] || !got["subtask-orphan"] {
		t.Fatalf("prune names = %v, want user-dead and subtask-orphan", resp.Names)
	}
	if got["subtask-live"] {
		t.Fatalf("prune deleted a live task child session: %v", resp.Names)
	}
	if int(resp.Count) != len(resp.Names) {
		t.Fatalf("count %d != len(names) %d", resp.Count, len(resp.Names))
	}
	if _, err := os.Stat(orphanSandbox); !os.IsNotExist(err) {
		t.Fatalf("orphan sandbox dir not removed: %v", err)
	}
	if _, err := mgr.Get("subtask-orphan"); err == nil {
		t.Fatal("orphan child session still present")
	}
	if _, err := mgr.Get("subtask-live"); err != nil {
		t.Fatalf("live task child session should remain: %v", err)
	}
}

func TestReapOrphanChildSessionsSkipsOnCheckErrorAndNilChecker(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	archiveOwned(t, mgr, "subtask-x", "task-x", filepath.Join(dir, "wx"))

	// nil checker: never reap (defensive).
	s := &server{mgr: mgr, amuxDir: dir}
	if names, err := s.reapOrphanChildSessions(); err != nil || len(names) != 0 {
		t.Fatalf("nil checker reaped %v err %v, want none", names, err)
	}
	if _, err := mgr.Get("subtask-x"); err != nil {
		t.Fatalf("session removed under nil checker: %v", err)
	}

	// existence check errors: skip, do not delete.
	s.taskExists = func(string) (bool, error) { return false, context.DeadlineExceeded }
	if names, err := s.reapOrphanChildSessions(); err != nil || len(names) != 0 {
		t.Fatalf("on check error reaped %v err %v, want none", names, err)
	}
	if _, err := mgr.Get("subtask-x"); err != nil {
		t.Fatalf("session removed on transient check error: %v", err)
	}
}

func TestPruneRemovesRuntimeOwnedWorkspace(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	s := &server{mgr: mgr, amuxDir: dir}
	workspaceDir := filepath.Join(dir, "workspaces", "cli", "local", "sre")
	if err := os.MkdirAll(filepath.Join(workspaceDir, "skills", "aweme-push-alarm-process"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "AGENTS.md"), []byte("profile rules\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "skills", "aweme-push-alarm-process", "SKILL.md"), []byte("# skill\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sess, err := mgr.Create("sre", "codex", workspaceDir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "local", "team/push_sre", "", launchSpecSnapshot(&pb.RunRequest{
		Name:            "sre",
		AgentType:       "codex",
		WorkspaceSource: "cli",
		WorkspaceScope:  "local",
	})); err != nil {
		t.Fatalf("record launch params: %v", err)
	}
	if err := mgr.SetState(sess, session.StateArchived); err != nil {
		t.Fatalf("archive: %v", err)
	}

	resp, err := s.Prune(context.Background(), &pb.PruneRequest{})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if resp.Count != 1 || len(resp.Names) != 1 || resp.Names[0] != "sre" {
		t.Fatalf("resp = %+v, want count=1 names=[sre]", resp)
	}
	if _, err := os.Stat(workspaceDir); !os.IsNotExist(err) {
		t.Fatalf("runtime-owned workspace not removed: %v", err)
	}
}

func TestPruneKeepsExplicitCwd(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	s := &server{mgr: mgr, amuxDir: dir}
	explicitCwd := filepath.Join(dir, "workspaces", "cli", "local", "sre")
	if err := os.MkdirAll(explicitCwd, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(explicitCwd, "keep.txt")
	if err := os.WriteFile(marker, []byte("do not prune\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sess, err := mgr.Create("sre", "codex", explicitCwd)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "local", "team/push_sre", "", launchSpecSnapshot(&pb.RunRequest{
		Name:            "sre",
		AgentType:       "codex",
		Cwd:             explicitCwd,
		WorkspaceSource: "cli",
		WorkspaceScope:  "local",
	})); err != nil {
		t.Fatalf("record launch params: %v", err)
	}
	if err := mgr.SetState(sess, session.StateArchived); err != nil {
		t.Fatalf("archive: %v", err)
	}

	if _, err := s.Prune(context.Background(), &pb.PruneRequest{}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("explicit cwd marker should be retained: %v", err)
	}
}

func TestSleepKeepsSessionAndStopsProcess(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "claude", "/tmp/work")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	proc := &serverTestProc{}
	sess.SetProc(proc)
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	srv := &server{mgr: mgr}

	resp, err := srv.Sleep(context.Background(), &pb.SleepRequest{Name: "s1"})
	if err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if !resp.Ok {
		t.Fatal("Sleep ok=false, want true")
	}
	got, err := mgr.Get("s1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !proc.closed || got.State != session.StateSleeping || got.PID != 0 {
		t.Fatalf("closed=%v state=%s pid=%d, want closed sleeping pid 0", proc.closed, got.State, got.PID)
	}
}

func TestRunRejectsDuplicateSessionBeforeMaterializingWorkspace(t *testing.T) {
	amuxDir := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	if _, err := mgr.Create("work", "claude", filepath.Join(amuxDir, "existing")); err != nil {
		t.Fatalf("create existing session: %v", err)
	}
	source := serveServerTestRepo(t, initServerTestRepo(t))
	srv := &server{mgr: mgr, amuxDir: amuxDir}

	_, err = srv.Run(context.Background(), &pb.RunRequest{
		Name:            "work",
		AgentType:       "claude",
		Repo:            source,
		WorkspaceSource: "cli",
		WorkspaceScope:  "local",
	})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("Run error = %v, want AlreadyExists", err)
	}
	repoPath := filepath.Join(amuxDir, "workspaces", "cli", "local", "work", "repo")
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Fatalf("duplicate run materialized repo at %s: %v", repoPath, err)
	}
}

func TestRunRejectsUnknownProfilePermissionPolicy(t *testing.T) {
	amuxDir := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	srv := &server{mgr: mgr, amuxDir: amuxDir}

	_, err = srv.Run(context.Background(), &pb.RunRequest{
		Name:            "work",
		AgentType:       "claude",
		WorkspaceSource: "cli",
		WorkspaceScope:  "local",
		ProfileSpec: &pb.AgentProfileSpec{
			Type:             "claude",
			PermissionPolicy: "ask",
		},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Run error = %v, want InvalidArgument", err)
	}
	if _, err := mgr.Get("work"); err == nil {
		t.Fatal("invalid permission policy left a session row behind")
	}
}

func TestRunPersistsExplicitIdleTimeoutSetBit(t *testing.T) {
	amuxDir := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	srv := &server{
		mgr:                  mgr,
		amuxDir:              amuxDir,
		disableMCPAutoInject: true,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, _ []string) error {
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	if _, err := srv.Run(context.Background(), &pb.RunRequest{
		Name:                  "feishu-zero-idle",
		AgentType:             "claude",
		IdleTimeoutSecondsSet: true,
		WorkspaceSource:       "feishu",
		WorkspaceScope:        "oc_test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := mgr.Get("feishu-zero-idle")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if snap := got.Snapshot(); snap.IdleTimeoutSeconds != 0 || !snap.IdleTimeoutSet {
		t.Fatalf("idle timeout = %d set:%v, want 0 set:true", snap.IdleTimeoutSeconds, snap.IdleTimeoutSet)
	}

	reloaded := session.NewManager(db)
	if err := reloaded.LoadFromDB(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, err = reloaded.Get("feishu-zero-idle")
	if err != nil {
		t.Fatalf("get reloaded session: %v", err)
	}
	if snap := got.Snapshot(); snap.IdleTimeoutSeconds != 0 || !snap.IdleTimeoutSet {
		t.Fatalf("reloaded idle timeout = %d set:%v, want 0 set:true", snap.IdleTimeoutSeconds, snap.IdleTimeoutSet)
	}
}

func TestRunPersistsAndReturnsEffectiveModel(t *testing.T) {
	tests := []struct {
		name string
		req  *pb.RunRequest
		want string
	}{
		{
			name: "profile model remains user facing",
			req: &pb.RunRequest{
				Name:         "profile-model",
				AgentType:    "codex",
				AgentProfile: "team/reviewer@abc123",
				ExtraArgs:    []string{"--model", "gpt-5.5-2026-04-24"},
				ProfileSpec: &pb.AgentProfileSpec{
					Ref:       "team/reviewer",
					PinnedRef: "team/reviewer@abc123",
					Type:      "codex",
					Model:     "custom-model",
				},
			},
			want: "custom-model",
		},
		{
			name: "legacy agent model comes from native args",
			req: &pb.RunRequest{
				Name:      "legacy-model",
				AgentType: "codex",
				ExtraArgs: []string{"--model", "gpt-5.4"},
			},
			want: "gpt-5.4",
		},
		{
			name: "unknown adapter default stays empty",
			req: &pb.RunRequest{
				Name:      "adapter-default",
				AgentType: "claude",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer db.Close()
			mgr := session.NewManager(db)
			srv := &server{
				mgr:                  mgr,
				amuxDir:              t.TempDir(),
				disableMCPAutoInject: true,
				startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, _ []string) error {
					sess.SetProc(&serverTestProc{})
					return mgr.SetRunning(sess)
				},
			}

			resp, err := srv.Run(context.Background(), tt.req)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := resp.Session.EffectiveModel; got != tt.want {
				t.Fatalf("response effective_model = %q, want %q", got, tt.want)
			}
			stored, err := mgr.Get(tt.req.Name)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if got := stored.Snapshot().EffectiveModel; got != tt.want {
				t.Fatalf("stored effective_model = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRestartRefreshProfileRejectsLegacyAndTypeChangeBeforeStoppingProcess(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mgr := session.NewManager(db)
		sess, err := mgr.Create("legacy", "claude", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		proc := &serverTestProc{}
		sess.SetProc(proc)
		if err := mgr.SetRunning(sess); err != nil {
			t.Fatal(err)
		}
		srv := &server{mgr: mgr, amuxDir: t.TempDir()}
		_, err = srv.Restart(context.Background(), &pb.RestartRequest{Name: "legacy", RefreshProfile: true})
		if status.Code(err) != codes.FailedPrecondition || proc.closed {
			t.Fatalf("error=%v closed=%v, want FailedPrecondition with live old proc", err, proc.closed)
		}
	})

	t.Run("type change", func(t *testing.T) {
		amuxDir := t.TempDir()
		packRoot := filepath.Join(amuxDir, "agent-packs", "team", "repo")
		writeRefreshProfilePack(t, packRoot, "codex", "new-model")
		if err := agentprofile.SavePackRegistry(filepath.Join(amuxDir, "agent-packs", "registry.json"), agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{
			"team": {Namespace: "team", SourceKind: agentprofile.PackSourceGit, MaterializedPath: packRoot, CurrentRevision: "newrev"},
		}}); err != nil {
			t.Fatal(err)
		}
		db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mgr := session.NewManager(db)
		sess, err := mgr.Create("work", "claude", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "local", "team/coder@oldrev", "old", ""); err != nil {
			t.Fatal(err)
		}
		proc := &serverTestProc{}
		sess.SetProc(proc)
		if err := mgr.SetRunning(sess); err != nil {
			t.Fatal(err)
		}
		srv := &server{mgr: mgr, amuxDir: amuxDir}
		_, err = srv.Restart(context.Background(), &pb.RestartRequest{Name: "work", RefreshProfile: true})
		if status.Code(err) != codes.FailedPrecondition || proc.closed {
			t.Fatalf("error=%v closed=%v, want FailedPrecondition with live old proc", err, proc.closed)
		}
	})
}

func writeRefreshProfilePack(t *testing.T, root, agentType, model string) {
	t.Helper()
	manifest := fmt.Sprintf(`{
  "schema_version": "v1.1",
  "pack_namespace": "team",
  "profiles": {
    "coder": {"namespace":"common","type":%q,"model":%q,"providers":[],"instructions":["instructions/coder/AGENTS.md"],"skills":[],"subagents":[],"extends":null}
  },
  "providers": {},
  "namespaces": {"common": {"profiles": ["coder"]}}
}`, agentType, model)
	path := filepath.Join(root, "dist", "agent-pack.manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir pack: %v", err)
	}
	if err := os.WriteFile(path, []byte(manifest), 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	instructionsPath := filepath.Join(root, "instructions", "coder", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(instructionsPath), 0700); err != nil {
		t.Fatalf("mkdir instructions: %v", err)
	}
	if err := os.WriteFile(instructionsPath, []byte("# Refreshed profile\n"), 0600); err != nil {
		t.Fatalf("write instructions: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRunHonorsCanceledContextBeforeMaterializingRepo(t *testing.T) {
	amuxDir := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	binDir := t.TempDir()
	gitMarker := filepath.Join(binDir, "git-called")
	fakeGit := filepath.Join(binDir, "git")
	if err := os.WriteFile(fakeGit, []byte("#!/bin/sh\necho \"$@\" > "+gitMarker+"\nif [ \"$1\" = \"clone\" ]; then mkdir -p \"$3\"; fi\nexit 0\n"), 0700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	srv := &server{
		mgr:     mgr,
		amuxDir: amuxDir,
		startSessionHook: func(adapter.Adapter, *session.Session, []string, []string) error {
			return status.Error(codes.Internal, "start should not run after context cancellation")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = srv.Run(ctx, &pb.RunRequest{
		Name:            "cancelled",
		AgentType:       "claude",
		Repo:            "https://example.com/org/repo.git",
		WorkspaceSource: "cli",
		WorkspaceScope:  "local",
	})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("Run error = %v, want Canceled", err)
	}
	if _, err := os.Stat(gitMarker); !os.IsNotExist(err) {
		t.Fatalf("git was invoked despite canceled context: %v", err)
	}
	if _, err := mgr.Get("cancelled"); err == nil {
		t.Fatal("canceled run left a session row behind")
	}
	workspace := filepath.Join(amuxDir, "workspaces", "cli", "local", "cancelled")
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("canceled run left workspace at %s: %v", workspace, err)
	}
}

func TestLaunchSpecSnapshotRedactsEnvAndInlineProfileContent(t *testing.T) {
	snapshot := launchSpecSnapshot(&pb.RunRequest{
		AgentProfile: "team/coder@abc123",
		AgentType:    "codex",
		Name:         "work",
		ExtraArgs:    []string{"--model", "gpt-5-codex"},
		Env:          []string{"SECRET_TOKEN=should-not-persist"},
		Repo:         "git@example.com:org/repo.git",
		RepoRef:      "main",
		ProfileSpec: &pb.AgentProfileSpec{
			Ref:             "team/coder",
			PinnedRef:       "team/coder@abc123",
			Revision:        "abc123",
			Type:            "codex",
			Model:           "gpt-5-codex",
			RuntimeRef:      "codex",
			ReasoningEffort: "high",
			Agents:          "AGENTS.md",
			AgentsContent:   "private AGENTS.md content",
			DefaultEnv:      []string{"PACK_SECRET=also-private"},
		},
	})

	for _, secret := range []string{"SECRET_TOKEN", "should-not-persist", "private AGENTS.md content", "PACK_SECRET"} {
		if strings.Contains(snapshot, secret) {
			t.Fatalf("launch snapshot leaked %q: %s", secret, snapshot)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(snapshot), &decoded); err != nil {
		t.Fatalf("snapshot is not JSON: %v\n%s", err, snapshot)
	}
	if decoded["agent_profile"] != "team/coder@abc123" || decoded["agent_type"] != "codex" || decoded["repo_ref"] != "main" {
		t.Fatalf("snapshot = %#v, want core refs preserved", decoded)
	}
	profile, ok := decoded["profile_spec"].(map[string]any)
	if !ok || profile["runtime_ref"] != "codex" || profile["reasoning_effort"] != "high" {
		t.Fatalf("profile snapshot = %#v, want frozen runtime and reasoning selection", decoded["profile_spec"])
	}
	if got := launchSpecReasoningEffort(snapshot); got != "high" {
		t.Fatalf("launchSpecReasoningEffort = %q, want high", got)
	}
}

func TestLaunchSpecReasoningEffortToleratesLegacyAndInvalidSnapshots(t *testing.T) {
	for _, spec := range []string{"", `{}`, `{not-json}`, `{"profile_spec":null}`} {
		if got := launchSpecReasoningEffort(spec); got != "" {
			t.Fatalf("launchSpecReasoningEffort(%q) = %q", spec, got)
		}
	}
}

func TestProviderNativeResumeCompatibleRequiresSameProviderIdentity(t *testing.T) {
	provider := func(ref, wireAPI, baseURL, upstreamModel string) *pb.AgentProfileSpec {
		return &pb.AgentProfileSpec{
			ProviderRef: ref,
			Provider: &pb.AgentProfileProvider{
				Ref: ref, Platform: "openai-compat", WireApi: wireAPI,
				BaseUrl: baseURL, UpstreamModel: upstreamModel,
			},
		}
	}
	previous := launchSpecSnapshot(&pb.RunRequest{
		AgentType:   "codex",
		ProfileSpec: provider("shared-provider", "responses", "http://models.example/v1", "old-model"),
	})
	tests := []struct {
		name        string
		replacement *pb.AgentProfileSpec
		want        bool
	}{
		{name: "same provider and upstream model", replacement: provider("shared-provider", "responses", "http://models.example/v1", "old-model"), want: true},
		{name: "different upstream model", replacement: provider("shared-provider", "responses", "http://models.example/v1", "new-model")},
		{name: "different provider ref", replacement: provider("other-provider", "responses", "http://models.example/v1", "new-model")},
		{name: "different wire API", replacement: provider("shared-provider", "chat-completions", "http://models.example/v1", "new-model")},
		{name: "different endpoint", replacement: provider("shared-provider", "responses", "http://other.example/v1", "new-model")},
		{name: "native replacement", replacement: &pb.AgentProfileSpec{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := providerNativeResumeCompatible(previous, &pb.RunRequest{ProfileSpec: tt.replacement})
			if got != tt.want {
				t.Fatalf("providerNativeResumeCompatible = %v, want %v", got, tt.want)
			}
		})
	}
	if !providerNativeResumeCompatible(`{}`, &pb.RunRequest{}) {
		t.Fatal("legacy native launch should retain same-runtime resume compatibility")
	}
}

func TestTryHotReconfigureExecutionKeepsProcessAndProviderConversation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("hot-model", "codex", "/tmp/work")
	if err != nil {
		t.Fatal(err)
	}
	oldReq := &pb.RunRequest{
		AgentProfile: "builtin/codex", AgentType: "codex", ExtraArgs: []string{"--model", "gpt-5.5"},
		ProfileSpec: &pb.AgentProfileSpec{Ref: "builtin/codex", Type: "codex", Model: "gpt-5.5", ReasoningEffort: "medium"},
	}
	if err := mgr.RecordLaunchParams(sess, oldReq.ExtraArgs, nil, "feishu", "singleton", oldReq.AgentProfile, "gpt-5.5", launchSpecSnapshot(oldReq)); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "native-thread"); err != nil {
		t.Fatal(err)
	}
	proc := &serverTestProc{}
	sess.SetProc(proc)
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}
	replacement := &pb.RunRequest{
		AgentProfile: "builtin/codex", AgentType: "codex", ExtraArgs: []string{"--model", "gpt-5.4"},
		ProfileSpec: &pb.AgentProfileSpec{Ref: "builtin/codex", Type: "codex", Model: "gpt-5.4", ReasoningEffort: "xhigh"},
	}
	launchSpec := launchSpecSnapshot(replacement)
	hot, err := (&server{mgr: mgr}).tryHotReconfigureExecution(sess, sess.Snapshot(), replacement,
		replacement.ExtraArgs, nil, "gpt-5.4", launchSpec)
	if err != nil {
		t.Fatalf("tryHotReconfigureExecution: %v", err)
	}
	if !hot || proc.closed || !reflect.DeepEqual(proc.reconfigurations, []string{"gpt-5.4:xhigh"}) {
		t.Fatalf("hot=%v closed=%v reconfigurations=%v", hot, proc.closed, proc.reconfigurations)
	}
	got := sess.Snapshot()
	if got.AgentSessionID != "native-thread" || got.EffectiveModel != "gpt-5.4" || got.State != session.StateRunning {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestReconfigureExecutionRestoresPreviousSnapshotWhenReplacementLaunchFails(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("singleton", "codex", dir)
	if err != nil {
		t.Fatal(err)
	}
	oldSpec := `{}`
	if err := mgr.RecordLaunchParams(sess, []string{"--model", "old"}, []string{"KEEP=old"},
		"feishu", "singleton", "team/reviewer", "old", oldSpec); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordAgentSessionID(sess, "old-thread"); err != nil {
		t.Fatal(err)
	}
	sess.SetProc(&serverTestProc{})
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatal(err)
	}
	original := sess.Snapshot()

	var starts []string
	srv := &server{
		mgr:                  mgr,
		amuxDir:              dir,
		disableMCPAutoInject: true,
		startSessionContextHook: func(_ context.Context, _ adapter.Adapter, current *session.Session, _, _ []string) error {
			snap := current.Snapshot()
			starts = append(starts, snap.AgentType+":"+snap.AgentSessionID)
			if snap.AgentType == "claude" {
				return errors.New("replacement runtime rejected")
			}
			current.SetProc(&serverTestProc{})
			return mgr.SetRunning(current)
		},
	}
	replacement := &pb.RunRequest{
		AgentProfile: "team/reviewer", AgentType: "claude", ExtraArgs: []string{"--model", "new"}, Cwd: dir,
	}
	_, err = srv.reconfigureExecutionAndRelaunch(context.Background(), sess, replacement, true)
	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "previous execution restored") {
		t.Fatalf("reconfigure error = %v, want restored Internal error", err)
	}
	if !reflect.DeepEqual(starts, []string{"claude:", "codex:old-thread"}) {
		t.Fatalf("launch attempts = %v", starts)
	}
	got := sess.Snapshot()
	if got.State != session.StateRunning || got.AgentType != original.AgentType ||
		got.AgentSessionID != original.AgentSessionID || got.EffectiveModel != original.EffectiveModel ||
		got.LaunchSpec != original.LaunchSpec || !reflect.DeepEqual(got.ExtraArgs, original.ExtraArgs) ||
		!reflect.DeepEqual(got.Env, original.Env) {
		t.Fatalf("restored snapshot = %+v, want launch fields from %+v", got, original)
	}
}

func TestResumeFailureKeepsSessionSleeping(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	missingCwd := filepath.Join(t.TempDir(), "missing")
	sess, err := mgr.Create("s1", "claude", missingCwd)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetSleeping(sess); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	srv := &server{mgr: mgr, amuxDir: t.TempDir()}

	if _, err := srv.Resume(context.Background(), &pb.ResumeRequest{Name: "s1"}); err == nil {
		t.Fatal("Resume succeeded with missing cwd, want launch error")
	}
	got, err := mgr.Get("s1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.State != session.StateSleeping {
		t.Fatalf("state after failed resume = %s, want sleeping", got.State)
	}
}

func TestEnsureSessionActiveWaitsForConcurrentResume(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "claude", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetSleeping(sess); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var launches int32
	srv := &server{
		mgr: mgr,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _, _ []string) error {
			if atomic.AddInt32(&launches, 1) == 1 {
				close(started)
				<-release
			}
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- srv.ensureSessionActive(sess)
	}()
	<-started

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- srv.ensureSessionActive(sess)
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second resume returned before first completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first ensureSessionActive: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second ensureSessionActive: %v", err)
	}
	if atomic.LoadInt32(&launches) != 1 {
		t.Fatalf("launches = %d, want 1", launches)
	}
	if got := sess.StateSnapshot(); got != session.StateRunning {
		t.Fatalf("state = %s, want running", got)
	}
}

func TestSubmitRestartsCreatedSessionBeforeWriting(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("interrupted-restart", "claude", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
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
		t.Fatalf("record launch params: %v", err)
	}

	writes := make(chan string, 2)
	var launches int32
	srv := &server{
		mgr: mgr,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _, _ []string) error {
			atomic.AddInt32(&launches, 1)
			sess.SetProc(&serverTestProc{onWriteData: func(data []byte) {
				writes <- string(data)
			}})
			return mgr.SetRunning(sess)
		},
	}

	if _, err := srv.Submit(context.Background(), &pb.SubmitRequest{
		Name:   sess.Name,
		Source: "lark",
		Text:   "hello",
		Async:  true,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := atomic.LoadInt32(&launches); got != 1 {
		t.Fatalf("launches = %d, want 1", got)
	}
	if got := sess.StateSnapshot(); got != session.StateRunning {
		t.Fatalf("state = %s, want running", got)
	}
	for _, want := range []string{"hello", "\r"} {
		select {
		case got := <-writes:
			if got != want {
				t.Fatalf("provider write = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for provider write %q", want)
		}
	}
}

func TestSubmitRejectsRunningSessionWithoutProcess(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("missing-proc", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetRunning(sess); err != nil {
		t.Fatalf("set running: %v", err)
	}
	before := sess.Snapshot().LastActivityAt
	time.Sleep(5 * time.Millisecond)

	srv := &server{mgr: mgr}
	_, err = srv.Submit(context.Background(), &pb.SubmitRequest{
		Name:   sess.Name,
		Source: "lark",
		Text:   "hello",
		Async:  true,
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("Submit error = %v, want Unavailable", err)
	}
	if after := sess.Snapshot().LastActivityAt; !after.Equal(before) {
		t.Fatalf("last activity changed from %s to %s for rejected input", before, after)
	}
}

func TestSubmitRejectsForgedLocalExpertAuthoringGrant(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("forged-authoring", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.SetProc(&serverTestProc{})

	srv := &server{mgr: mgr}
	_, err = srv.Submit(context.Background(), &pb.SubmitRequest{
		Name: sess.Name, Source: "lark", Text: "edit local expert", Async: true,
		LocalExpertAuthoring: true,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Submit error = %v, want PermissionDenied", err)
	}
	if _, _, active := sess.ActiveTurnAuthorization(); active {
		t.Fatal("forged authoring submit started a turn")
	}
}

func TestSubmitReportsProviderWriteFailure(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("write-failure", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.SetProc(&serverTestProc{writeErr: errors.New("provider write failed")})

	srv := &server{mgr: mgr}
	_, err = srv.Submit(context.Background(), &pb.SubmitRequest{
		Name:    sess.Name,
		Source:  "lark",
		Text:    "hello",
		Async:   true,
		Preempt: true,
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("Submit error = %v, want Internal", err)
	}
}

func TestRespondPermissionResumesSleepingSessionBeforeResponding(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "claude", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.SetSleeping(sess); err != nil {
		t.Fatalf("set sleeping: %v", err)
	}
	var proc *serverTestProc
	var launches int32
	srv := &server{
		mgr: mgr,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _, _ []string) error {
			atomic.AddInt32(&launches, 1)
			proc = &serverTestProc{}
			sess.SetProc(proc)
			return mgr.SetRunning(sess)
		},
	}

	resp, err := srv.RespondPermission(context.Background(), &pb.RespondPermissionRequest{Name: "s1", PermissionId: "perm-1", OptionId: "allow"})
	if err != nil {
		t.Fatalf("RespondPermission: %v", err)
	}
	if !resp.Ok {
		t.Fatal("RespondPermission ok=false, want true")
	}
	if launches != 1 {
		t.Fatalf("launches = %d, want 1 resume before permission response", launches)
	}
	if proc == nil || len(proc.permissionResponses) != 1 || proc.permissionResponses[0] != "perm-1:allow" {
		t.Fatalf("permission responses = %#v, want perm-1:allow", proc)
	}
	if got := sess.StateSnapshot(); got != session.StateRunning {
		t.Fatalf("state = %s, want running", got)
	}
}

func TestRunPermissionPolicyAllowRespondsAndConsumesPermission(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	cwd := t.TempDir()
	sess, err := mgr.Create("s1", "codex", cwd)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	req := &pb.RunRequest{
		Name:      "s1",
		AgentType: "codex",
		ProfileSpec: &pb.AgentProfileSpec{
			Ref:              "team/push_sre",
			Type:             "codex",
			PermissionPolicy: "policy-allow",
		},
	}
	if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "local", "team/push_sre", "", launchSpecSnapshot(req)); err != nil {
		t.Fatalf("record launch params: %v", err)
	}
	proc := &serverTestProc{holdTurnOpen: true}
	srv := &server{mgr: mgr}
	srv.wireSessionEventTap(sess)
	sess.SetProc(proc)
	if err := sess.Submit(context.Background(), "cli", "test permission"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	consumed := proc.turnSink(testPermissionEmission("perm-1"))

	if !consumed {
		t.Fatal("permission event was not consumed by run permission policy")
	}
	if len(proc.permissionResponses) != 1 || proc.permissionResponses[0] != "perm-1:allow" {
		t.Fatalf("permission responses = %#v, want perm-1:allow", proc.permissionResponses)
	}
	if count := sess.PendingPermissionCount(); count != 0 {
		t.Fatalf("pending permissions = %d, want 0", count)
	}
	proc.turnSink(testSuccessEmission())
}

func TestRunPermissionPolicyDenyAllRespondsWithDeny(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	req := &pb.RunRequest{
		Name:      "s1",
		AgentType: "codex",
		ProfileSpec: &pb.AgentProfileSpec{
			Ref:              "team/locked",
			Type:             "codex",
			PermissionPolicy: "deny-all",
		},
	}
	if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "local", "team/locked", "", launchSpecSnapshot(req)); err != nil {
		t.Fatalf("record launch params: %v", err)
	}
	proc := &serverTestProc{holdTurnOpen: true}
	srv := &server{mgr: mgr}
	srv.wireSessionEventTap(sess)
	sess.SetProc(proc)
	if err := sess.Submit(context.Background(), "cli", "test permission"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	consumed := proc.turnSink(testPermissionEmission("perm-1"))

	if !consumed {
		t.Fatal("permission event was not consumed by run permission policy")
	}
	if len(proc.permissionResponses) != 1 || proc.permissionResponses[0] != "perm-1:deny" {
		t.Fatalf("permission responses = %#v, want perm-1:deny", proc.permissionResponses)
	}
	proc.turnSink(testSuccessEmission())
}

func TestRunWithoutPermissionPolicyKeepsPermissionInteractive(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	proc := &serverTestProc{holdTurnOpen: true}
	srv := &server{mgr: mgr}
	srv.wireSessionEventTap(sess)
	sess.SetProc(proc)
	if err := sess.Submit(context.Background(), "cli", "test permission"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	consumed := proc.turnSink(testPermissionEmission("perm-1"))

	if consumed {
		t.Fatal("permission event was consumed without a run permission policy")
	}
	if len(proc.permissionResponses) != 0 {
		t.Fatalf("permission responses = %#v, want none", proc.permissionResponses)
	}
	if count := sess.PendingPermissionCount(); count != 1 {
		t.Fatalf("pending permissions = %d, want 1", count)
	}
	proc.turnSink(testSuccessEmission())
}

func testPermissionEmission(id string) agentstream.ProducerEmission {
	return agentstream.ProducerEmission{Turn: agentstream.PermissionRequested{Request: agentstream.PermissionRequest{
		ID:       id,
		Provider: "test",
		Options: []agentstream.PermissionOption{
			{ID: "allow", Name: "Allow"},
			{ID: "deny", Name: "Deny"},
		},
	}}}
}

func testSuccessEmission() agentstream.ProducerEmission {
	return agentstream.ProducerEmission{Terminal: &agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}}
}

func TestMaterializeRepoWorkspaceClonesIntoFixedRepoDir(t *testing.T) {
	source := serveServerTestRepo(t, initServerTestRepo(t))
	root := t.TempDir()
	req := &pb.RunRequest{Name: "work", Repo: source}

	repoPath, err := materializeRepoWorkspace(context.Background(), req, root)
	if err != nil {
		t.Fatalf("materializeRepoWorkspace: %v", err)
	}
	if repoPath != "repo" {
		t.Fatalf("repo path = %q, want repo", repoPath)
	}
	if _, err := os.Stat(filepath.Join(root, "repo", "README.md")); err != nil {
		t.Fatalf("repo README not cloned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.Base(source))); !os.IsNotExist(err) {
		t.Fatalf("materialized repo used source directory name: %v", err)
	}
}

func TestMaterializeRepoWorkspaceRejectsRefWithoutRepo(t *testing.T) {
	root := t.TempDir()
	req := &pb.RunRequest{Name: "work", RepoRef: "stable"}

	_, err := materializeRepoWorkspace(context.Background(), req, root)
	if err == nil || !strings.Contains(err.Error(), "repo_ref requires repo") {
		t.Fatalf("materializeRepoWorkspace error = %v, want repo_ref requires repo", err)
	}
}

func TestMaterializeRepoWorkspaceRejectsLocalRepoPath(t *testing.T) {
	source := initServerTestRepo(t)
	root := t.TempDir()
	req := &pb.RunRequest{Name: "work", Repo: source}

	_, err := materializeRepoWorkspace(context.Background(), req, root)
	if err == nil || !strings.Contains(err.Error(), "remote Git URLs") {
		t.Fatalf("materializeRepoWorkspace error = %v, want local repo path rejected", err)
	}
}

func TestMaterializeRepoWorkspaceChecksOutRepoRef(t *testing.T) {
	worktree := initServerTestRepo(t)
	base := gitBranch(t, worktree)
	runServerTestGit(t, worktree, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("feature\n"), 0600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runServerTestGit(t, worktree, "add", ".")
	runServerTestGit(t, worktree, "commit", "-m", "feature")
	runServerTestGit(t, worktree, "checkout", base)
	source := serveServerTestRepo(t, worktree)

	root := t.TempDir()
	req := &pb.RunRequest{Name: "work", Repo: source, RepoRef: "feature"}
	if _, err := materializeRepoWorkspace(context.Background(), req, root); err != nil {
		t.Fatalf("materializeRepoWorkspace: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "repo", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if strings.TrimSpace(string(data)) != "feature" {
		t.Fatalf("README = %q, want feature branch", string(data))
	}
}

func TestMaterializeProfileWorkspaceWritesProviderProjection(t *testing.T) {
	root := t.TempDir()
	req := &pb.RunRequest{
		Name:         "work",
		AgentType:    "codex",
		AgentProfile: "team/coder@abc123",
		ProfileSpec: &pb.AgentProfileSpec{
			Ref:           "team/coder",
			PinnedRef:     "team/coder@abc123",
			Namespace:     "team",
			Name:          "coder",
			Revision:      "abc123",
			Type:          "codex",
			Model:         "GPT-5.5",
			Source:        "pack",
			Description:   "Team coding profile",
			Agents:        "AGENTS.md",
			AgentsContent: "# Instructions\n\nYou are the team coder.\n",
			Skills:        []string{"amux-dev"},
			Placement: &pb.AgentProfilePlacement{
				Requires: &pb.AgentProfileRequirements{
					AgentTypes: []string{"codex"},
				},
			},
		},
	}

	if err := materializeProfileWorkspace(req, root, "repo", t.TempDir()); err != nil {
		t.Fatalf("materializeProfileWorkspace: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "You are the team coder.") {
		t.Fatalf("AGENTS.md missing source instructions:\n%s", text)
	}
	if !strings.Contains(text, `The model selected for this session launch is "GPT-5.5".`) ||
		!strings.Contains(text, "Do not infer model identity from memory or prior conversations.") {
		t.Fatalf("AGENTS.md missing authoritative runtime model identity:\n%s", text)
	}
	for _, unwanted := range []string{"team/coder", "Team coding profile", "amux-dev"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("AGENTS.md contains profile metadata %q:\n%s", unwanted, text)
		}
	}
}

func TestMaterializeProfileWorkspaceProjectsMultipleLocalPackSkills(t *testing.T) {
	root := t.TempDir()
	amuxDir := t.TempDir()
	packRoot := filepath.Join(amuxDir, "agent-packs", "team", "repo")
	writeServerPackSkill(t, packRoot, "alpha", "Alpha skill")
	writeServerPackSkill(t, packRoot, "beta", "Beta skill")
	if err := agentprofile.SavePackRegistry(filepath.Join(amuxDir, "agent-packs", "registry.json"), agentprofile.PackRegistry{Packs: map[string]agentprofile.Pack{
		"team": {
			Namespace:        "team",
			SourceKind:       agentprofile.PackSourceGit,
			MaterializedPath: packRoot,
			CurrentRevision:  "abc123",
		},
	}}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	req := &pb.RunRequest{
		Name:         "work",
		AgentType:    "codex",
		AgentProfile: "team/coder@abc123",
		ProfileSpec: &pb.AgentProfileSpec{
			Ref:       "team/coder",
			PinnedRef: "team/coder@abc123",
			Namespace: "team",
			Name:      "coder",
			Revision:  "abc123",
			Type:      "codex",
			Source:    "pack",
			Skills:    []string{"alpha", "beta"},
			SkillDirs: []string{"skills"},
		},
	}

	if err := materializeProfileWorkspace(req, root, "", amuxDir); err != nil {
		t.Fatalf("materializeProfileWorkspace: %v", err)
	}

	assertFileContains(t, filepath.Join(root, ".agents", "skills", "alpha", "SKILL.md"), "Alpha skill")
	assertFileContains(t, filepath.Join(root, ".agents", "skills", "beta", "SKILL.md"), "Beta skill")
}

func TestRunPersistsEffectivePATH(t *testing.T) {
	amuxDir := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	customBin := filepath.Join(t.TempDir(), "bin")
	t.Setenv("PATH", customBin+string(os.PathListSeparator)+"/usr/bin:/bin")
	var capturedEnv []string
	srv := &server{
		mgr:                  mgr,
		amuxDir:              amuxDir,
		disableMCPAutoInject: true,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			capturedEnv = append([]string(nil), env...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	if _, err := srv.Run(context.Background(), &pb.RunRequest{
		Name:      "path-snapshot",
		AgentType: "codex",
		Env:       []string{"CUSTOM=1"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := envValueForTest(capturedEnv, "PATH"); !strings.Contains(got, customBin) {
		t.Fatalf("launch PATH = %q, want %s", got, customBin)
	}
	sess, err := mgr.Get("path-snapshot")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got := envValueForTest(sess.Snapshot().Env, "PATH"); !strings.Contains(got, customBin) {
		t.Fatalf("persisted PATH = %q, want %s", got, customBin)
	}
}

func TestRunPreservesExplicitPATH(t *testing.T) {
	explicit := "/restricted/bin"
	if got := envValueForTest(ensureLaunchEnvPATH([]string{"PATH=" + explicit}), "PATH"); got != explicit {
		t.Fatalf("explicit PATH = %q, want unchanged %q", got, explicit)
	}
}

func TestResolveACPCommandPathUsesLastLaunchEnvPATH(t *testing.T) {
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "example-acp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake example-acp: %v", err)
	}
	cmd := exec.Command("example-acp", "acp")
	env := []string{"PATH=/service/bin", "PATH=" + binDir}

	if err := resolveACPCommandPath(cmd, env); err != nil {
		t.Fatalf("resolveACPCommandPath: %v", err)
	}
	if cmd.Path != bin {
		t.Fatalf("cmd.Path = %q, want %q", cmd.Path, bin)
	}
}

func TestResolveACPCommandPathRejectsServicePATHFallback(t *testing.T) {
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "example-acp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake example-acp: %v", err)
	}
	t.Setenv("PATH", binDir)
	cmd := exec.Command("example-acp", "acp")

	err := resolveACPCommandPath(cmd, []string{"PATH=/restricted/bin"})
	if err == nil || !strings.Contains(err.Error(), `agent binary "example-acp" not found in PATH`) {
		t.Fatalf("resolveACPCommandPath error = %v, want strict launch PATH failure", err)
	}
}

func TestRelaunchMigratesMissingPATH(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("path-migration", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.RecordLaunchParams(
		sess,
		nil,
		nil,
		"feishu",
		"bot-profile",
		"builtin/codex",
		"",
		`{"agent_profile":"builtin/codex","agent_type":"codex"}`,
	); err != nil {
		t.Fatalf("record params: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	var capturedEnv []string
	srv := &server{
		mgr: mgr,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			capturedEnv = append([]string(nil), env...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	if _, err := srv.relaunchSession(sess, "restarting"); err != nil {
		t.Fatalf("relaunchSession: %v", err)
	}
	wantBin := filepath.Join(home, ".local", "bin")
	if got := envValueForTest(capturedEnv, "PATH"); !strings.Contains(got, wantBin) {
		t.Fatalf("relaunch PATH = %q, want legacy fallback %s", got, wantBin)
	}
	if got := envValueForTest(sess.Snapshot().Env, "PATH"); !strings.Contains(got, wantBin) {
		t.Fatalf("persisted migrated PATH = %q, want %s", got, wantBin)
	}
}

func TestRelaunchMigratesPATHMissingStandardUserBins(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("path-migration-existing", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := mgr.RecordLaunchParams(
		sess,
		nil,
		[]string{"PATH=/usr/local/bin:/usr/bin:/bin"},
		"feishu",
		"bot-profile",
		"builtin/codex",
		"",
		`{"agent_profile":"builtin/codex","agent_type":"codex"}`,
	); err != nil {
		t.Fatalf("record params: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	var capturedEnv []string
	srv := &server{
		mgr: mgr,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			capturedEnv = append([]string(nil), env...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	if _, err := srv.relaunchSession(sess, "restarting"); err != nil {
		t.Fatalf("relaunchSession: %v", err)
	}
	wantBin := filepath.Join(home, ".local", "bin")
	if got := envValueForTest(capturedEnv, "PATH"); !strings.Contains(got, wantBin) {
		t.Fatalf("relaunch PATH = %q, want migrated user bin %s", got, wantBin)
	}
	if got := envValueForTest(sess.Snapshot().Env, "PATH"); !strings.Contains(got, wantBin) {
		t.Fatalf("persisted migrated PATH = %q, want %s", got, wantBin)
	}
}

func TestRelaunchMigratesLastEffectivePATH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := []string{
		"PATH=/profile/bin",
		"CUSTOM=1",
		"PATH=/explicit/bin",
	}

	got := ensureLegacyLaunchEnvPATH(env)
	if first := strings.TrimPrefix(got[0], "PATH="); first != "/profile/bin" {
		t.Fatalf("first PATH = %q, want unchanged profile PATH", first)
	}
	wantBin := filepath.Join(home, ".local", "bin")
	if last := envValueForTest(got, "PATH"); !strings.HasPrefix(last, "/explicit/bin") || !strings.Contains(last, wantBin) {
		t.Fatalf("effective PATH = %q, want explicit path followed by %s", last, wantBin)
	}
}

func TestRunProfileUsesIsolationGlobalEnvWithoutPersistingIt(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	var capturedEnv []string
	srv := &server{
		mgr:                  mgr,
		amuxDir:              t.TempDir(),
		creds:                profileCreds{}, // Any credential lookup would fail the launch.
		disableMCPAutoInject: true,
		isolationGlobalEnv:   []string{"GLM_API_KEY=shared-secret", "HTTP_PROXY=http://shared-proxy"},
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			capturedEnv = append([]string(nil), env...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	req := &pb.RunRequest{
		Name:      "shared-glm",
		AgentType: "codex",
		Env:       []string{"HTTP_PROXY=http://explicit-proxy"},
		ExecutionIdentity: principal.ToProtoIdentity(principal.ExecutionIdentity{
			IdentityMode:           principal.IdentityModeIdentified,
			OriginActorPrincipalID: "01234567-89ab-4cde-8fab-0123456789ab",
			EffectivePrincipalID:   "01234567-89ab-4cde-8fab-0123456789ab",
			Source:                 principal.SourceFeishu,
		}),
		ProfileSpec: &pb.AgentProfileSpec{
			Ref:  "team/shared-glm",
			Type: "codex",
			Envs: []string{"GLM_API_KEY"},
		},
	}

	if _, err := srv.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := envValueForTest(capturedEnv, "GLM_API_KEY"); got != "shared-secret" {
		t.Fatalf("GLM_API_KEY = %q, want shared value", got)
	}
	if got := envValueForTest(capturedEnv, "HTTP_PROXY"); got != "http://explicit-proxy" {
		t.Fatalf("HTTP_PROXY = %q, want explicit launch value", got)
	}
	sess, err := mgr.Get("shared-glm")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got := envValueForTest(sess.Snapshot().Env, "GLM_API_KEY"); got != "" {
		t.Fatalf("persisted GLM_API_KEY = %q, want empty", got)
	}
	capturedEnv = nil
	if _, err := srv.relaunchSession(sess, "restarting"); err != nil {
		t.Fatalf("relaunchSession: %v", err)
	}
	if got := envValueForTest(capturedEnv, "GLM_API_KEY"); got != "shared-secret" {
		t.Fatalf("relaunch GLM_API_KEY = %q, want shared value", got)
	}
}

func TestRunProfileInjectsCredentialEnvRefs(t *testing.T) {
	amuxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(amuxDir, "secrets"), 0700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(amuxDir, "secrets", "github.json"), []byte(`{
		"kind": "long-lived",
		"env": {
			"GITHUB_TOKEN": "gh-secret",
			"GITHUB_OTHER": "ignored"
		}
	}`), 0600); err != nil {
		t.Fatalf("write github secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(amuxDir, "secrets", "slack.json"), []byte(`{
		"kind": "long-lived",
		"env": {
			"SLACK_TOKEN": "sl-secret",
			"SLACK_OTHER": "ignored"
		}
	}`), 0600); err != nil {
		t.Fatalf("write slack secrets: %v", err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	var capturedEnv []string
	srv := &server{
		mgr:     mgr,
		amuxDir: amuxDir,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			capturedEnv = append([]string(nil), env...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	req := &pb.RunRequest{
		Name:         "repo-review",
		AgentType:    "codex",
		AgentProfile: "team/repo-review@abc123",
		ProfileSpec: &pb.AgentProfileSpec{
			Ref:       "team/repo-review",
			PinnedRef: "team/repo-review@abc123",
			Namespace: "team",
			Name:      "repo-review",
			Revision:  "abc123",
			Type:      "codex",
			Source:    "pack",
			EnvRefs: []string{
				"credential:github:GITHUB_TOKEN",
				"credential:slack:SLACK_TOKEN",
			},
		},
	}

	if _, err := srv.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := envValueForTest(capturedEnv, "GITHUB_TOKEN"); got != "gh-secret" {
		t.Fatalf("GITHUB_TOKEN = %q, want injected github secret", got)
	}
	if got := envValueForTest(capturedEnv, "SLACK_TOKEN"); got != "sl-secret" {
		t.Fatalf("SLACK_TOKEN = %q, want injected slack secret", got)
	}
	if got := envValueForTest(capturedEnv, "GITHUB_OTHER"); got != "" {
		t.Fatalf("GITHUB_OTHER = %q, want filtered env", got)
	}
	if got := envValueForTest(capturedEnv, "SLACK_OTHER"); got != "" {
		t.Fatalf("SLACK_OTHER = %q, want filtered env", got)
	}
	sess, err := mgr.Get("repo-review")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	for _, key := range []string{"GITHUB_TOKEN", "SLACK_TOKEN", "GITHUB_OTHER", "SLACK_OTHER"} {
		if stored := envValueForTest(sess.Snapshot().Env, key); stored != "" {
			t.Fatalf("persisted %s = %q, want not persisted", key, stored)
		}
	}

	capturedEnv = nil
	if _, err := srv.relaunchSession(sess, "restarting"); err != nil {
		t.Fatalf("relaunchSession: %v", err)
	}
	if got := envValueForTest(capturedEnv, "GITHUB_TOKEN"); got != "gh-secret" {
		t.Fatalf("relaunch GITHUB_TOKEN = %q, want injected github secret", got)
	}
	if got := envValueForTest(capturedEnv, "SLACK_TOKEN"); got != "sl-secret" {
		t.Fatalf("relaunch SLACK_TOKEN = %q, want injected slack secret", got)
	}
}

func TestRunProfileInjectsProfileEnvs(t *testing.T) {
	amuxDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(amuxDir, "secrets"), 0700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(amuxDir, "secrets", "operator.json"), []byte(`{
		"kind": "long-lived",
		"env": {
			"SERVICE_TOKEN": "demo-secret",
			"OTHER": "ignored"
		}
	}`), 0600); err != nil {
		t.Fatalf("write operator secrets: %v", err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	var capturedEnv []string
	srv := &server{
		mgr:     mgr,
		amuxDir: amuxDir,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
			capturedEnv = append([]string(nil), env...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	req := &pb.RunRequest{
		Name:         "operator",
		AgentType:    "codex",
		AgentProfile: "team/operator@abc123",
		ProfileSpec: &pb.AgentProfileSpec{
			Ref:       "team/operator",
			PinnedRef: "team/operator@abc123",
			Namespace: "push",
			Name:      "operator",
			Revision:  "abc123",
			Type:      "codex",
			Source:    "pack",
			Envs:      []string{"SERVICE_TOKEN"},
		},
	}

	if _, err := srv.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := envValueForTest(capturedEnv, "SERVICE_TOKEN"); got != "demo-secret" {
		t.Fatalf("SERVICE_TOKEN = %q, want injected secret", got)
	}
	if got := envValueForTest(capturedEnv, "OTHER"); got != "" {
		t.Fatalf("OTHER = %q, want filtered env", got)
	}
	sess, err := mgr.Get("operator")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored := envValueForTest(sess.Snapshot().Env, "SERVICE_TOKEN"); stored != "" {
		t.Fatalf("persisted SERVICE_TOKEN = %q, want not persisted", stored)
	}
	capturedEnv = nil
	if _, err := srv.relaunchSession(sess, "restarting"); err != nil {
		t.Fatalf("relaunchSession: %v", err)
	}
	if got := envValueForTest(capturedEnv, "SERVICE_TOKEN"); got != "demo-secret" {
		t.Fatalf("relaunch SERVICE_TOKEN = %q, want injected secret", got)
	}
}

type profileCreds map[string]task.CredentialMaterial

func (p profileCreds) Materialize(_ context.Context, profile, _ string) (task.CredentialMaterial, error) {
	mat, ok := p[profile]
	if !ok {
		return task.CredentialMaterial{}, fmt.Errorf("credential profile %q not found", profile)
	}
	return mat, nil
}

func (p profileCreds) MaterializeEnvByName(_ context.Context, _ string, envNames []string) (task.CredentialMaterial, error) {
	var out task.CredentialMaterial
	for _, envName := range envNames {
		var foundProfile string
		var foundValue string
		for profile, material := range p {
			for _, entry := range material.Env {
				key, value, ok := strings.Cut(entry, "=")
				if !ok || key != envName {
					continue
				}
				if foundProfile != "" && foundProfile != profile {
					return task.CredentialMaterial{}, &task.CredentialEnvAmbiguousError{
						EnvName:       envName,
						FirstProfile:  foundProfile,
						SecondProfile: profile,
					}
				}
				foundProfile = profile
				foundValue = value
				out.Kind = material.Kind
			}
		}
		if foundProfile == "" {
			return task.CredentialMaterial{}, &task.CredentialEnvNotFoundError{EnvName: envName}
		}
		out.Env = append(out.Env, envName+"="+foundValue)
		out.Profiles = append(out.Profiles, foundProfile)
	}
	return out, nil
}

func TestRunCredentialScopeFollowsExecutionIdentity(t *testing.T) {
	const (
		actorToken  = "principal-token"
		principalID = "01234567-89ab-4cde-8fab-0123456789ab"
	)
	for _, tc := range []struct {
		name              string
		executionIdentity *pb.ExecutionIdentity
		wantCredentialTok string
	}{
		{
			name:              "legacy host keeps installation credentials",
			wantCredentialTok: "",
		},
		{
			name: "identified launch keeps principal credentials",
			executionIdentity: principal.ToProtoIdentity(principal.ExecutionIdentity{
				IdentityMode:           principal.IdentityModeIdentified,
				OriginActorPrincipalID: principalID,
				EffectivePrincipalID:   principalID,
				Source:                 principal.SourceFeishu,
			}),
			wantCredentialTok: actorToken,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer db.Close()
			mgr := session.NewManager(db)
			creds := &credentialActorTokenRecorder{}
			var capturedEnv []string
			srv := &server{
				mgr:                  mgr,
				amuxDir:              t.TempDir(),
				creds:                creds,
				disableMCPAutoInject: true,
				startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, env []string) error {
					capturedEnv = append([]string(nil), env...)
					sess.SetProc(&serverTestProc{})
					return mgr.SetRunning(sess)
				},
			}
			inheritedCredentialCtx := task.WithCredentialActorToken(context.Background(), "inherited-token")
			_, err = srv.Run(inheritedCredentialCtx, &pb.RunRequest{
				Name:              "credential-scope",
				AgentType:         "codex",
				AgentActorToken:   actorToken,
				ExecutionIdentity: tc.executionIdentity,
				ProfileSpec: &pb.AgentProfileSpec{
					Ref:  "team/credential-scope",
					Type: "codex",
					Provider: &pb.AgentProfileProvider{
						Platform:      "openai-compat",
						ApiKeyEnv:     "GLM_API_KEY",
						BaseUrl:       "https://provider.example/v1",
						UpstreamModel: "test-model",
					},
				},
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := envValueForTest(capturedEnv, "GLM_API_KEY"); got != "scoped-secret" {
				t.Fatalf("GLM_API_KEY = %q, want injected credential", got)
			}
			if got := envValueForTest(capturedEnv, agentActorTokenEnv); got != actorToken {
				t.Fatalf("%s = %q, want control-plane actor token", agentActorTokenEnv, got)
			}
			sess, err := mgr.Get("credential-scope")
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			capturedEnv = nil
			if _, err := srv.relaunchSession(sess, "restarting"); err != nil {
				t.Fatalf("relaunchSession: %v", err)
			}
			if got := creds.actorTokens; !reflect.DeepEqual(got, []string{tc.wantCredentialTok, tc.wantCredentialTok}) {
				t.Fatalf("credential actor tokens = %q, want two %q scopes", got, tc.wantCredentialTok)
			}
			if got := envValueForTest(capturedEnv, agentActorTokenEnv); got != actorToken {
				t.Fatalf("relaunch %s = %q, want control-plane actor token", agentActorTokenEnv, got)
			}
		})
	}
}

type credentialActorTokenRecorder struct {
	actorTokens []string
}

func (r *credentialActorTokenRecorder) Materialize(ctx context.Context, _, _ string) (task.CredentialMaterial, error) {
	r.actorTokens = append(r.actorTokens, task.CredentialActorToken(ctx))
	return task.CredentialMaterial{Env: []string{"GLM_API_KEY=scoped-secret"}}, nil
}

func TestRunSkipsAmuxMCPWhenAutoInjectDisabled(t *testing.T) {
	amuxDir := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	var capturedArgs []string
	srv := &server{
		mgr:                  mgr,
		amuxDir:              amuxDir,
		disableMCPAutoInject: true,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, args []string, _ []string) error {
			capturedArgs = append([]string(nil), args...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	if _, err := srv.Run(context.Background(), &pb.RunRequest{Name: "codex-no-mcp", AgentType: "codex"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, arg := range capturedArgs {
		if strings.Contains(arg, "mcp_servers.amux.") {
			t.Fatalf("args include amux MCP despite disabled auto inject: %v", capturedArgs)
		}
	}
}

func TestRunDoesNotDuplicateExplicitAmuxMCPArgs(t *testing.T) {
	amuxDir := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	var capturedArgs []string
	srv := &server{
		mgr:     mgr,
		amuxDir: amuxDir,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, args []string, _ []string) error {
			capturedArgs = append([]string(nil), args...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	req := &pb.RunRequest{
		Name:      "codex-explicit-mcp",
		AgentType: "codex",
		ExtraArgs: []string{
			"-c", "mcp_servers.amux.command=\"/custom/amux\"",
			"-c", "mcp_servers.amux.args=[\"mcp\"]",
		},
	}
	if _, err := srv.Run(context.Background(), req); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := countArgsPrefixForTest(capturedArgs, "mcp_servers.amux.command="); got != 1 {
		t.Fatalf("mcp command arg count = %d in %v, want 1", got, capturedArgs)
	}
}

func envValueForTest(env []string, key string) string {
	prefix := key + "="
	value := ""
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			value = strings.TrimPrefix(item, prefix)
		}
	}
	return value
}

func argsContainPrefixForTest(args []string, want string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, want) {
			return true
		}
	}
	return false
}

func countArgsPrefixForTest(args []string, want string) int {
	count := 0
	for _, arg := range args {
		if strings.HasPrefix(arg, want) {
			count++
		}
	}
	return count
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s missing %q:\n%s", path, want, string(data))
	}
}

func writeServerPackSkill(t *testing.T, packRoot, name, description string) {
	t.Helper()
	path := filepath.Join(packRoot, "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nUse this skill.\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func initServerTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runServerTestGit(t, repo, "init")
	runServerTestGit(t, repo, "config", "user.name", "amux test")
	runServerTestGit(t, repo, "config", "user.email", "amux@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runServerTestGit(t, repo, "add", ".")
	runServerTestGit(t, repo, "commit", "-m", "init")
	return repo
}

func serveServerTestRepo(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "source.git")
	cmd := exec.Command("git", "clone", "--bare", source, bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, string(out))
	}
	runServerTestGit(t, bare, "update-server-info")
	srv := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(srv.Close)
	return srv.URL + "/source.git"
}

func runServerTestGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func gitBranch(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--abbrev-ref", "HEAD")
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --abbrev-ref HEAD: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func TestSubmitAsyncReturnsAfterEnqueueWhileTurnIsActive(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "claude")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	launchID, err := mgr.RotateLaunchID(sess)
	if err != nil {
		t.Fatalf("rotate launch id: %v", err)
	}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	sess.SetProc(&serverTestProc{onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}})
	srv := &server{mgr: mgr}

	firstDone := make(chan error, 1)
	go func() {
		_, err := srv.Submit(context.Background(), &pb.SubmitRequest{Name: "s1", Source: "cli", Text: "first"})
		firstDone <- err
	}()
	<-firstCRSeen

	accepted, err := srv.Submit(context.Background(), &pb.SubmitRequest{Name: "s1", Source: "cli", Text: "second", Async: true})
	if err != nil {
		t.Fatalf("async Submit: %v", err)
	}
	if accepted.GetTurnId() == "" || accepted.GetSessionId() != sess.ID || accepted.GetLaunchId() != launchID {
		t.Fatalf("submit receipt = %+v, want turn/session/launch identity", accepted)
	}
	items := sess.QueueSnapshot()
	if len(items) != 1 || items[0].Text != "second" {
		t.Fatalf("queue = %+v, want second pending", items)
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Submit: %v", err)
	}
}

func TestSubmitPreemptJumpsAheadOfPendingInput(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "claude")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	releaseFirst := make(chan struct{})
	firstCRSeen := make(chan struct{})
	var once sync.Once
	sess.SetProc(&serverTestProc{onWriteData: func(data []byte) {
		if string(data) == "\r" {
			once.Do(func() {
				close(firstCRSeen)
				<-releaseFirst
			})
		}
	}})
	srv := &server{mgr: mgr}

	firstDone := make(chan error, 1)
	go func() {
		_, err := srv.Submit(context.Background(), &pb.SubmitRequest{Name: "s1", Source: "cli", Text: "first"})
		firstDone <- err
	}()
	<-firstCRSeen

	if _, err := srv.Submit(context.Background(), &pb.SubmitRequest{Name: "s1", Source: "cli", Text: "queued", Async: true}); err != nil {
		t.Fatalf("async Submit: %v", err)
	}
	preemptDone := make(chan error, 1)
	go func() {
		_, err := srv.Submit(context.Background(), &pb.SubmitRequest{Name: "s1", Source: "lark", Text: "urgent", Async: true, Preempt: true})
		preemptDone <- err
	}()
	var items []session.QueueItem
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		items = sess.QueueSnapshot()
		if len(items) == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(items) != 2 || items[0].Text != "urgent" || items[1].Text != "queued" {
		t.Fatalf("queue = %+v, want [urgent queued]", items)
	}

	close(releaseFirst)
	if err := <-preemptDone; err != nil {
		t.Fatalf("preempt Submit: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first Submit: %v", err)
	}
}

func TestAttachRequiresExplicitTimelineMode(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	_, err = mgr.Create("s1", "claude")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	srv := &server{mgr: mgr}

	stream := &attachTestStream{ctx: context.Background()}
	err = srv.Attach(&pb.AttachRequest{Name: "s1"}, stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Attach() error = %v, want InvalidArgument", err)
	}
	if len(stream.events) != 0 {
		t.Fatalf("events = %+v, want none", stream.events)
	}
}

func TestAttachHistoryReadsDurableCanonicalTimelineAndEndsAtHead(t *testing.T) {
	t.Parallel()

	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("history", "codex")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := sess.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1", InputSummary: "hello"}, func(ctx context.Context, turn *agentstream.Turn) error {
		if _, err := turn.Emit(ctx, agentstream.AssistantDelta{Text: "world"}, agentstream.Origin{Actor: agentstream.ActorAgent, Provider: "codex"}); err != nil {
			return err
		}
		_, err := turn.Finish(ctx, agentstream.Terminal{Outcome: agentstream.OutcomeSuccess, Code: "completed"}, agentstream.Origin{Actor: agentstream.ActorAgent, Provider: "codex"})
		return err
	})
	if err != nil || turn.Ref().Seq != 1 {
		t.Fatalf("create turn = (%#v, %v)", turn, err)
	}

	stream := &attachTestStream{ctx: context.Background()}
	err = (&server{mgr: mgr}).Attach(&pb.AttachRequest{
		Name:         "history",
		TimelineMode: pb.AttachRequest_HISTORY,
	}, stream)
	if err != nil {
		t.Fatalf("Attach(history) error = %v", err)
	}
	if len(stream.events) != 4 {
		t.Fatalf("history events = %d, want 3 canonical events plus caught-up marker", len(stream.events))
	}
	for i, event := range stream.events[:3] {
		if !event.Historical || event.Cursor == "" || len(event.CanonicalJson) == 0 {
			t.Fatalf("history event %d = %+v", i, event)
		}
		decoded, err := agentstream.UnmarshalEvent(event.CanonicalJson)
		if err != nil || decoded.Cursor.Seq != uint64(i+1) {
			t.Fatalf("history canonical event %d = (%#v, %v)", i, decoded, err)
		}
	}
	marker := stream.events[3]
	if !marker.CaughtUp || marker.Kind != pb.AttachEvent_HEARTBEAT || marker.Cursor != stream.events[2].Cursor {
		t.Fatalf("caught-up marker = %+v", marker)
	}

	resumed := &attachTestStream{ctx: context.Background()}
	if err := (&server{mgr: mgr}).Attach(&pb.AttachRequest{
		Name:         "history",
		TimelineMode: pb.AttachRequest_HISTORY,
		AfterCursor:  stream.events[1].Cursor,
	}, resumed); err != nil {
		t.Fatalf("Attach(resume history) error = %v", err)
	}
	if len(resumed.events) != 2 || !resumed.events[0].Historical || !resumed.events[1].CaughtUp {
		t.Fatalf("resumed history = %+v", resumed.events)
	}
	decoded, err := agentstream.UnmarshalEvent(resumed.events[0].CanonicalJson)
	if err != nil || decoded.Cursor.Seq != 3 {
		t.Fatalf("resumed event = (%#v, %v), want seq 3", decoded, err)
	}
}

func TestAttachTailIncludesLatestAvailableCommandsOnly(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "codex")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.Replay.Append([]byte("old output"))
	if _, err := sess.AgentTimeline().Publish(context.Background(), agentstream.CommandsReplaced{Commands: []agentstream.Command{{Name: "old-skill", Kind: "skill"}}}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AgentTimeline().Publish(context.Background(), agentstream.CommandsReplaced{Commands: []agentstream.Command{{Name: "current-skill", Kind: "skill"}}}, agentstream.Origin{Actor: agentstream.ActorAgent}); err != nil {
		t.Fatal(err)
	}

	srv := &server{mgr: mgr}
	ctx, cancel := context.WithCancel(context.Background())
	stream := &attachTestStream{ctx: ctx, cancel: cancel, cancelAfter: 2}
	if err := srv.Attach(&pb.AttachRequest{Name: "s1", TimelineMode: pb.AttachRequest_TAIL}, stream); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if len(stream.events) != 2 || stream.events[0].Kind != pb.AttachEvent_HEARTBEAT {
		t.Fatalf("events = %+v, want heartbeat plus current available_commands", stream.events)
	}
	got := stream.events[1]
	if got.EventType != string(provider.EventAvailableCommands) || strings.Contains(string(got.StructuredJson), "old-skill") || !strings.Contains(string(got.StructuredJson), "current-skill") {
		t.Fatalf("capability snapshot = %+v, want only latest available_commands", got)
	}
}

func TestAttachMarksInputSourceEvents(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "claude")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := sess.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1", InputSummary: "hello", Origin: agentstream.Origin{Actor: agentstream.ActorUser, Channel: "lark"}}, func(context.Context, *agentstream.Turn) error { return nil }); err != nil {
		t.Fatal(err)
	}

	srv := &server{mgr: mgr}
	ctx, cancel := context.WithCancel(context.Background())
	stream := &attachTestStream{ctx: ctx, cancel: cancel, cancelAfter: 1}
	if err := srv.Attach(&pb.AttachRequest{Name: "s1", TimelineMode: pb.AttachRequest_FOLLOW}, stream); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if len(stream.events) < 1 {
		t.Fatalf("events len = %d, want canonical input", len(stream.events))
	}
	ev := stream.events[0]
	if ev.Source != "lark" || !ev.Input {
		t.Fatalf("event source/input = %q/%v, want lark/true", ev.Source, ev.Input)
	}
}

func TestAttachHistoryIncludesStructuredProviderEvents(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	sess, err := mgr.Create("s1", "codex")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	turn, err := sess.AgentTimeline().StartTurn(context.Background(), agentstream.StartRequest{TurnID: "turn-1"}, func(context.Context, *agentstream.Turn) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	startedCursor, err := agentstream.EncodeCursor(turn.StartedEvent().Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Emit(context.Background(), agentstream.ToolChanged{Phase: agentstream.ToolPhaseUpdated, Tool: agentstream.Tool{ID: "tool-1", Content: json.RawMessage(`[{"text":"diff"}]`)}}, agentstream.Origin{Actor: agentstream.ActorTool}); err != nil {
		t.Fatal(err)
	}

	srv := &server{mgr: mgr}
	stream := &attachTestStream{ctx: context.Background()}
	if err := srv.Attach(&pb.AttachRequest{Name: "s1", TimelineMode: pb.AttachRequest_HISTORY, AfterCursor: startedCursor}, stream); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if len(stream.events) != 2 {
		t.Fatalf("events = %d, want one canonical tool event and caught-up marker", len(stream.events))
	}
	got := stream.events[0]
	if got.EventType != "tool_updated" || got.ToolId != "tool-1" || len(got.CanonicalJson) == 0 {
		t.Fatalf("history event = %+v, want rich tool_updated", got)
	}
}

type attachTestStream struct {
	ctx         context.Context
	cancel      context.CancelFunc
	cancelAfter int
	events      []*pb.AttachEvent
}

func (s *attachTestStream) Send(ev *pb.AttachEvent) error {
	s.events = append(s.events, ev)
	if s.cancel != nil && (s.cancelAfter == 0 || len(s.events) >= s.cancelAfter) {
		s.cancel()
	}
	return nil
}

func (s *attachTestStream) Context() context.Context { return s.ctx }

func (s *attachTestStream) SetHeader(metadata.MD) error { return nil }

func (s *attachTestStream) SendHeader(metadata.MD) error { return nil }

func (s *attachTestStream) SetTrailer(metadata.MD) {}

func (s *attachTestStream) RecvMsg(interface{}) error { return nil }

func (s *attachTestStream) SendMsg(interface{}) error { return nil }

// TestCreateChildPersistentReusesSession verifies that a persistent agent
// instance reuses one child session across CreateChild calls (named
// instance-<id>), while ephemeral tasks each get their own subtask-<taskID>
// session.
func TestCreateChildPersistentReusesSession(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "tc.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	srv := &server{
		mgr:     mgr,
		amuxDir: t.TempDir(),
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, _ []string, _ []string) error {
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}
	ctrl := newTaskController(srv, "t-1")

	// Two persistent CreateChild calls for the same instance reuse one session.
	c1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-a", AgentType: "claude", OwnerTaskId: "task-a", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 1: %v", err)
	}
	if c1.Name() != "instance-inst-1" {
		t.Fatalf("name 1 = %q, want instance-inst-1", c1.Name())
	}
	c2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-b", AgentType: "claude", OwnerTaskId: "task-b", InstanceID: "inst-1"})
	if err != nil {
		t.Fatalf("CreateChild 2: %v", err)
	}
	if c2.ID() != c1.ID() {
		t.Fatalf("persistent same instance should reuse session: c1=%s c2=%s", c1.ID(), c2.ID())
	}
	if got, _ := mgr.Get("instance-inst-1"); got == nil || got.AgentInstanceID != "inst-1" {
		t.Fatalf("persisted session = %+v, want AgentInstanceID inst-1", got)
	}

	// Ephemeral tasks each build their own session.
	e1, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-e1", AgentType: "claude", OwnerTaskId: "task-e1"})
	if err != nil {
		t.Fatalf("CreateChild ephemeral 1: %v", err)
	}
	e2, err := ctrl.CreateChild(context.Background(), task.ChildSpec{TaskID: "task-e2", AgentType: "claude", OwnerTaskId: "task-e2"})
	if err != nil {
		t.Fatalf("CreateChild ephemeral 2: %v", err)
	}
	if e1.ID() == e2.ID() {
		t.Fatal("ephemeral tasks must not share a session")
	}
	if e1.Name() != "subtask-task-e1" || e2.Name() != "subtask-task-e2" {
		t.Fatalf("ephemeral names = %q / %q, want subtask-task-e1 / subtask-task-e2", e1.Name(), e2.Name())
	}
}

// TestRunWritesActorTokenIntoMCPServerEnv pins that a session launch carrying an
// actor credential also hands it to the amux MCP server the agent spawns.
// Codex pass only a curated whitelist to MCP subprocesses, so the token
// must be written into the MCP config itself — otherwise the MCP server calls
// the hub anonymously and, with owner-scoped schedules and tasks, sees nothing.
func TestRunWritesActorTokenIntoMCPServerEnv(t *testing.T) {
	amuxDir := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	var gotArgs []string
	srv := &server{
		mgr:     mgr,
		amuxDir: amuxDir,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, args, _ []string) error {
			gotArgs = append([]string(nil), args...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	if _, err := srv.Run(context.Background(), &pb.RunRequest{
		Name:            "feishu-mcp-token",
		AgentType:       "codex",
		AgentActorToken: "tok-secret",
		WorkspaceSource: "feishu",
		WorkspaceScope:  "oc_test",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "mcp_servers.amux.env=") {
		t.Fatalf("launch args carry no mcp_servers.amux.env entry: %q", joined)
	}
	if !strings.Contains(joined, `AMUX_ACTOR_TOKEN="tok-secret"`) {
		t.Fatalf("mcp env lacks AMUX_ACTOR_TOKEN: %q", joined)
	}
	if !strings.Contains(joined, `AMUX_ACTOR_TOKEN_FILE="`+filepath.Join(amuxDir, "sessions")) {
		t.Fatalf("mcp env lacks AMUX_ACTOR_TOKEN_FILE: %q", joined)
	}
}

// TestRunWithoutActorTokenLeavesMCPEnvWithoutOne is the other half: a launch
// with no credential must not write an AMUX_ACTOR_TOKEN value at all, so the
// MCP server behaves exactly as before for legacy launches. The token-file
// path is still written: the file simply does not exist until the hub mints a
// credential for this session, and the MCP server falls back to the env var
// and enrollment token as before.
func TestRunWithoutActorTokenLeavesMCPEnvWithoutOne(t *testing.T) {
	amuxDir := t.TempDir()
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	var gotArgs []string
	srv := &server{
		mgr:     mgr,
		amuxDir: amuxDir,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, args, _ []string) error {
			gotArgs = append([]string(nil), args...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	if _, err := srv.Run(context.Background(), &pb.RunRequest{
		Name:            "cli-mcp-no-token",
		AgentType:       "codex",
		WorkspaceSource: "cli",
		WorkspaceScope:  "local",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "mcp_servers.amux.env=") {
		t.Fatalf("launch args carry no mcp_servers.amux.env entry: %q", joined)
	}
	if strings.Contains(joined, `AMUX_ACTOR_TOKEN="`) {
		t.Fatalf("mcp env unexpectedly carries an AMUX_ACTOR_TOKEN value: %q", joined)
	}
	if !strings.Contains(joined, `AMUX_ACTOR_TOKEN_FILE="`) {
		t.Fatalf("mcp env lacks AMUX_ACTOR_TOKEN_FILE: %q", joined)
	}
}

// TestRelaunchRefreshesMCPServerToken pins that a relaunch replaces the
// creation-time MCP server config with one carrying the relaunch credential.
// Sessions created before the credential was written into mcp_servers.amux.env
// (or with a stale one) would otherwise replay it forever: Codex do not
// inherit the agent env into MCP subprocesses, so an anonymous MCP server is
// permanent until the args are refreshed.
func TestRelaunchRefreshesMCPServerToken(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	mgr := session.NewManager(db)
	amuxDir := t.TempDir()
	sess, err := mgr.Create("feishu-mcp-relaunch", "codex", t.TempDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	oldArgs := []string{
		"-c", `mcp_servers.amux.command="/usr/bin/amux"`,
		"-c", `mcp_servers.amux.args=["mcp"]`,
		"-c", `mcp_servers.amux.env={AMUX_HOME="/home/u/.amux", AMUX_SESSION_ID="` + sess.ID + `"}`,
	}
	if err := mgr.RecordLaunchParams(sess, oldArgs, nil, "feishu", "oc_test", "builtin/codex", "", `{"agent_profile":"builtin/codex","agent_type":"codex"}`); err != nil {
		t.Fatalf("record params: %v", err)
	}
	if err := writeSessionActorToken(amuxDir, sess.ID, "tok-fresh", time.Now().Add(24*time.Hour).Unix()); err != nil {
		t.Fatalf("write actor token: %v", err)
	}
	var gotArgs []string
	srv := &server{
		mgr:     mgr,
		amuxDir: amuxDir,
		startSessionHook: func(_ adapter.Adapter, sess *session.Session, args, _ []string) error {
			gotArgs = append([]string(nil), args...)
			sess.SetProc(&serverTestProc{})
			return mgr.SetRunning(sess)
		},
	}

	if _, err := srv.relaunchSession(sess, "restarting"); err != nil {
		t.Fatalf("relaunchSession: %v", err)
	}

	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "mcp_servers.amux.env=") {
		t.Fatalf("relaunch args lost the mcp env entry: %q", joined)
	}
	if !strings.Contains(joined, `AMUX_ACTOR_TOKEN="tok-fresh"`) {
		t.Fatalf("relaunch mcp env lacks the relaunch credential: %q", joined)
	}
	if strings.Contains(joined, "AMUX_SESSION_ID=\"\"") {
		t.Fatalf("relaunch mcp env lost the session id: %q", joined)
	}
	if !strings.Contains(joined, `AMUX_SESSION_ID="`+sess.ID+`"`) {
		t.Fatalf("relaunch mcp env must keep the session id: %q", joined)
	}
	if strings.Count(joined, "mcp_servers.amux.tool_timeout_sec=7200") != 1 {
		t.Fatalf("relaunch args must refresh one long amux MCP tool timeout: %q", joined)
	}
}

// mockAutoModelSelector is a test adapter that implements AutoModelSelector.
type mockAutoModelSelector struct {
	adapter.Adapter
	model string
	err   error
}

func (m *mockAutoModelSelector) SelectAutoModel(cwd string, env []string, cfg adapter.AutoModelSelectionConfig) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.model, nil
}

func enabledAutoModelConfig(string) (adapter.AutoModelSelectionConfig, bool) {
	return adapter.AutoModelSelectionConfig{Priority: []string{"example-model"}, LoadThresholdPercent: 80}, true
}

func (m *mockAutoModelSelector) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{}
}

func (m *mockAutoModelSelector) Cmd(extraArgs []string) *exec.Cmd {
	return exec.Command("true")
}

// mockNonAutoSelector is a plain adapter without AutoModelSelector.
type mockNonAutoSelector struct{}

func (m *mockNonAutoSelector) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{}
}

func (m *mockNonAutoSelector) Cmd(extraArgs []string) *exec.Cmd {
	return exec.Command("true")
}
