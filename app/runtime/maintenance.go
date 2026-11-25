package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const maintenanceLease = 90 * time.Second
const maintenanceSleepReason = "upgrade"

type maintenanceSession struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	KeepBroker bool   `json:"keep_broker,omitempty"`
}
type maintenanceRecord struct {
	Token           string               `json:"token"`
	Phase           string               `json:"phase"`
	Expires         time.Time            `json:"expires"`
	Handoff         bool                 `json:"handoff,omitempty"`
	ReplacingHelper bool                 `json:"replacing_helper,omitempty"`
	Sessions        []maintenanceSession `json:"sessions"`
}

// The local administration service is deliberately separate from Amuxlet:
// tunnel dispatch cannot acquire or release an installation maintenance lease.
// The record is written BEFORE sleeping each Session, so recovery also covers
// an updater/runtime crash between process shutdown and persisting Sleep.
type runtimeMaintenance struct {
	pb.UnimplementedRuntimeMaintenanceServer
	mu             sync.Mutex
	srv            *server
	tasks          func() int
	record         *maintenanceRecord
	requests       int
	recoveryActive bool
	handoffArmed   bool
	now            func() time.Time
	resume         func(context.Context, *session.Session) error
}

func newRuntimeMaintenance(srv *server, tasks func() int) (*runtimeMaintenance, error) {
	m := &runtimeMaintenance{srv: srv, tasks: tasks, now: time.Now}
	m.resume = func(ctx context.Context, sess *session.Session) error {
		_, err := srv.relaunchSessionLockedWithEnvContext(ctx, sess, "restarting", nil, nil, nil, false, "", 0)
		return err
	}
	data, err := os.ReadFile(m.path())
	if err == nil {
		var record maintenanceRecord
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		if record.Token == "" || (record.Phase != "draining" && record.Phase != "ready" && record.Phase != "recovering") {
			return nil, errors.New(maintenanceMessage("journal"))
		}
		m.record = &record
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	srv.maintenance = m
	srv.mgr.SetMaintenance(m.blocked())
	return m, nil
}
func (m *runtimeMaintenance) path() string {
	return filepath.Join(m.srv.amuxDir, "upgrade-maintenance.json")
}
func (m *runtimeMaintenance) blocked() bool { return m.record != nil && m.record.Phase != "recovering" }
func (m *runtimeMaintenance) save() error {
	data, err := json.Marshal(m.record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.srv.amuxDir, ".maintenance-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), m.path())
}

// enter serializes admission with begin. Already admitted calls are counted
// until they return; accepted async work is covered by Session queues/TaskRuns.
func (m *runtimeMaintenance) enter(newWork bool) (func(), error) {
	if m == nil {
		return func() {}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blocked() && (newWork || m.record.Phase == "ready") {
		return nil, status.Error(codes.Unavailable, maintenanceMessage("unavailable"))
	}
	m.requests++
	return func() { m.mu.Lock(); m.requests--; m.mu.Unlock() }, nil
}

func (m *runtimeMaintenance) Maintain(ctx context.Context, req *pb.MaintenanceRequest) (*pb.MaintenanceResponse, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil || p.Addr.Network() != "unix" {
		return nil, status.Error(codes.PermissionDenied, maintenanceMessage("local"))
	}
	if len(req.Token) < 16 || len(req.Token) > 128 {
		return nil, status.Error(codes.InvalidArgument, maintenanceMessage("token"))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if (req.Action == "begin" || req.Action == "begin-handoff" || req.Action == "begin-helper-handoff") && m.record == nil {
		m.record = &maintenanceRecord{
			Token: req.Token, Phase: "draining", Expires: m.now().Add(maintenanceLease),
			Handoff: req.Action != "begin", ReplacingHelper: req.Action == "begin-helper-handoff",
		}
		if err := m.save(); err != nil {
			m.record = nil
			return nil, err
		}
		m.srv.mgr.SetMaintenance(true)
	}
	if m.record == nil {
		return &pb.MaintenanceResponse{Phase: "idle"}, nil
	}
	if m.record.Token != req.Token {
		return nil, status.Error(codes.AlreadyExists, maintenanceMessage("held"))
	}
	switch req.Action {
	case "begin", "begin-handoff", "begin-helper-handoff", "renew":
		m.record.Expires = m.now().Add(maintenanceLease)
		if err := m.save(); err != nil {
			return nil, err
		}
	case "status":
		return m.response(), nil
	case "poll":
		return m.drain(ctx)
	case "arm-handoff":
		m.handoffArmed = m.record.Handoff && m.record.Phase == "ready"
		return m.response(), nil
	case "end":
		return m.recover(ctx)
	default:
		return nil, status.Error(codes.InvalidArgument, maintenanceMessage("action"))
	}
	return m.response(), nil
}
func (m *runtimeMaintenance) response() *pb.MaintenanceResponse {
	if m.record == nil {
		return &pb.MaintenanceResponse{Phase: "idle"}
	}
	return &pb.MaintenanceResponse{Phase: m.record.Phase, SleepingSessions: int32(len(m.record.Sessions)), ActiveRequests: int32(m.requests)}
}
func (m *runtimeMaintenance) drain(ctx context.Context) (*pb.MaintenanceResponse, error) {
	out := m.response()
	if m.record.Phase != "draining" {
		return out, nil
	}
	if m.tasks != nil {
		out.ActiveTasks = int32(m.tasks())
	}
	for _, sess := range m.srv.mgr.List() {
		if m.canKeepBroker(sess, sess.Snapshot()) && sess.ReadyForUpgradeHandoff() {
			continue
		}
		busy, pending := sess.QueueState()
		if busy || pending > 0 || sess.AgentTimeline().ActiveTurn() != nil || sess.PendingPermissionCount() > 0 {
			out.BusySessions = append(out.BusySessions, sess.Name)
		}
	}
	// Accepted task cleanup and asynchronous lifecycle operations must finish
	// before the restore set is captured; a terminal Task may have slept its child.
	if m.requests != 0 || out.ActiveTasks != 0 || len(out.BusySessions) != 0 {
		return out, nil
	}
	m.srv.relaunchMu.Lock()
	defer m.srv.relaunchMu.Unlock()
	batchDeadline := time.Now().Add(time.Second)
	var handoff []maintenanceSession
	for _, sess := range m.srv.mgr.List() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snap := sess.Snapshot()
		if snap.State != session.StateRunning && snap.State != session.StateCreated && snap.State != session.StateIdle {
			continue
		}
		if m.canKeepBroker(sess, snap) {
			if sess.ReadyForUpgradeHandoff() {
				handoff = append(handoff, maintenanceSession{ID: snap.ID, Name: snap.Name, KeepBroker: true})
			} else {
				out.BusySessions = append(out.BusySessions, snap.Name)
			}
			continue
		}
		// Native continuations and internal notices also share the queue barrier.
		unlock, busy, pending := sess.TryLockIdleQueue()
		if busy || pending > 0 {
			out.BusySessions = append(out.BusySessions, snap.Name)
			continue
		}
		err := m.sleepLocked(sess, snap)
		unlock()
		if err != nil {
			return nil, err
		}
		if time.Now().After(batchDeadline) {
			return m.response(), nil
		}
	}
	if len(out.BusySessions) != 0 {
		return out, nil
	}
	sleeping := m.record.Sessions
	m.record.Sessions = append(sleeping, handoff...)
	m.record.Phase = "ready"
	if err := m.save(); err != nil {
		m.record.Phase = "draining"
		m.record.Sessions = sleeping
		return nil, err
	}
	return m.response(), nil
}

func (m *runtimeMaintenance) canKeepBroker(sess *session.Session, snap session.Snapshot) bool {
	return m.record.Handoff && snap.State == session.StateRunning && snap.PID > 0 && snap.BrokerSocket != "" && sess.HasProcess() &&
		(!m.record.ReplacingHelper || snap.ExecutionIdentity.IsolationBackend != principal.BackendLinuxDAC)
}

// handoffBrokers is read only by the shutdown path. An expired, aborted or
// incomplete lease must not change the normal stop semantics.
func (m *runtimeMaintenance) handoffBrokers() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.handoffArmed || m.record == nil || m.record.Phase != "ready" || !m.now().Before(m.record.Expires) {
		return nil
	}
	keep := make(map[string]bool)
	for _, entry := range m.record.Sessions {
		if entry.KeepBroker {
			keep[entry.ID] = true
		}
	}
	return keep
}

func (m *runtimeMaintenance) ownsSession(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.record != nil {
		for _, entry := range m.record.Sessions {
			if entry.ID == id {
				return true
			}
		}
	}
	return false
}

func (m *runtimeMaintenance) sleepLocked(sess *session.Session, snap session.Snapshot) error {
	found := false
	for _, entry := range m.record.Sessions {
		if entry.ID == snap.ID {
			found = true
			break
		}
	}
	if !found {
		m.record.Sessions = append(m.record.Sessions, maintenanceSession{ID: snap.ID, Name: snap.Name})
		if err := m.save(); err != nil {
			m.record.Sessions = m.record.Sessions[:len(m.record.Sessions)-1]
			return err
		}
	}
	if err := sess.Kill(); err != nil {
		return err
	}
	return m.srv.mgr.SetSleepingPreservingQueue(sess, maintenanceSleepReason)
}
func (m *runtimeMaintenance) recover(ctx context.Context) (*pb.MaintenanceResponse, error) {
	if m.recoveryActive {
		return m.response(), nil
	}
	m.record.Phase = "recovering"
	m.handoffArmed = false
	// Recovery errors must not leave normal admission disabled indefinitely.
	m.srv.mgr.SetMaintenance(false)
	if err := m.save(); err != nil {
		m.srv.mgr.ResumeMaintenanceGoals()
		return nil, err
	}
	m.recoveryActive = true
	remaining, recoveryErr := m.recoverSessions(ctx, m.record.Sessions)
	m.recoveryActive = false
	m.record.Sessions = remaining
	if len(remaining) > 0 {
		return m.response(), errors.Join(recoveryErr, m.save())
	}
	if err := os.Remove(m.path()); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	m.record = nil
	m.srv.mgr.ResumeMaintenanceGoals()
	return m.response(), nil
}

// The caller holds mu and has marked recovery active. Keep the same durable
// journal until the batch finishes, while letting other local RPCs take mu.
func (m *runtimeMaintenance) recoverSessions(ctx context.Context, entries []maintenanceSession) ([]maintenanceSession, error) {
	m.mu.Unlock()
	defer m.mu.Lock()
	var failures []error
	remaining := make([]maintenanceSession, 0, len(entries))
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			// Try entries that have not had a turn before retrying failures.
			next := append([]maintenanceSession(nil), entries[i:]...)
			return append(next, remaining...), errors.Join(append(failures, err)...)
		}
		if err := m.recoverSession(ctx, entry); err != nil {
			remaining = append(remaining, entry)
			failures = append(failures, fmt.Errorf("%s: %w", entry.Name, err))
		}
	}
	return remaining, errors.Join(failures...)
}

func (m *runtimeMaintenance) recoverSession(ctx context.Context, entry maintenanceSession) error {
	if err := m.srv.lockRelaunch(ctx); err != nil {
		return err
	}
	defer m.srv.relaunchMu.Unlock()
	sess, err := m.srv.mgr.Get(entry.Name)
	if err != nil {
		return nil
	}
	snap := sess.Snapshot()
	if snap.ID != entry.ID || snap.State == session.StateArchived {
		return nil
	}
	if snap.State == session.StateRunning && sess.HasProcess() {
		if entry.KeepBroker {
			return nil
		}
		// A prior resume may have failed to persist Running. Retry the write
		// without interrupting any work accepted since the process started.
		return m.srv.mgr.SetRunning(sess)
	}
	return m.resume(ctx, sess)
}

// maintenanceWatchInterval is how soon recovery is retried once a recovery
// need appears, and maintenanceWatchMaxInterval caps the backoff while
// attempts keep failing.
const (
	maintenanceWatchInterval    = 5 * time.Second
	maintenanceWatchMaxInterval = 5 * time.Minute
)

// watch retries recovery for the sessions the maintenance lease still owns.
// A resume that keeps failing must not become a hot loop: every attempt
// materializes a fresh run tree and starts a broker, so repeated failures back
// off to maintenanceWatchMaxInterval until one attempt makes progress.
func (m *runtimeMaintenance) watch(ctx context.Context) {
	timer := time.NewTimer(maintenanceWatchInterval)
	defer timer.Stop()
	interval := maintenanceWatchInterval
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		progressed := true
		m.mu.Lock()
		if m.record != nil && !m.recoveryActive && (m.record.Phase == "recovering" || !m.now().Before(m.record.Expires)) {
			pending := len(m.record.Sessions)
			recoveryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if _, err := m.recover(recoveryCtx); err != nil {
				fmt.Fprintf(os.Stderr, "runtime: maintenance recovery: %v\n", err)
				progressed = len(m.record.Sessions) < pending
			}
			cancel()
		}
		m.mu.Unlock()
		interval = nextMaintenanceWatchInterval(interval, progressed)
		timer.Reset(interval)
	}
}

// nextMaintenanceWatchInterval doubles the wait after a failed attempt and
// snaps back to the base interval as soon as an attempt makes progress.
func nextMaintenanceWatchInterval(current time.Duration, progressed bool) time.Duration {
	if progressed || current <= 0 {
		return maintenanceWatchInterval
	}
	if next := current * 2; next < maintenanceWatchMaxInterval {
		return next
	}
	return maintenanceWatchMaxInterval
}
