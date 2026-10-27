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

	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const maintenanceLease = 90 * time.Second
const maintenanceSleepReason = "upgrade"

type maintenanceSession struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type maintenanceRecord struct {
	Token    string               `json:"token"`
	Phase    string               `json:"phase"`
	Expires  time.Time            `json:"expires"`
	Sessions []maintenanceSession `json:"sessions"`
}

// The local administration service is deliberately separate from Amuxlet:
// tunnel dispatch cannot acquire or release an installation maintenance lease.
// The record is written BEFORE sleeping each Session, so recovery also covers
// an updater/runtime crash between process shutdown and persisting Sleep.
type runtimeMaintenance struct {
	pb.UnimplementedRuntimeMaintenanceServer
	mu       sync.Mutex
	srv      *server
	tasks    func() int
	record   *maintenanceRecord
	requests int
	now      func() time.Time
	resume   func(context.Context, *session.Session) error
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
	if req.Action == "begin" && m.record == nil {
		m.record = &maintenanceRecord{Token: req.Token, Phase: "draining", Expires: m.now().Add(maintenanceLease)}
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
	case "begin", "renew":
		m.record.Expires = m.now().Add(maintenanceLease)
		if err := m.save(); err != nil {
			return nil, err
		}
	case "poll":
		return m.drain(ctx)
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
	for _, sess := range m.srv.mgr.List() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snap := sess.Snapshot()
		if snap.State != session.StateRunning && snap.State != session.StateCreated && snap.State != session.StateIdle {
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
	m.record.Phase = "ready"
	if err := m.save(); err != nil {
		return nil, err
	}
	return m.response(), nil
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
			return err
		}
	}
	if err := sess.Kill(); err != nil {
		return err
	}
	return m.srv.mgr.SetSleepingPreservingQueue(sess, maintenanceSleepReason)
}
func (m *runtimeMaintenance) recover(ctx context.Context) (*pb.MaintenanceResponse, error) {
	m.record.Phase = "recovering"
	// Recovery errors must not leave normal admission disabled indefinitely.
	m.srv.mgr.SetMaintenance(false)
	if err := m.save(); err != nil {
		m.srv.mgr.ResumeMaintenanceGoals()
		return nil, err
	}
	m.srv.relaunchMu.Lock()
	defer m.srv.relaunchMu.Unlock()
	var failures []error
	remaining := make([]maintenanceSession, 0, len(m.record.Sessions))
	for _, entry := range m.record.Sessions {
		sess, err := m.srv.mgr.Get(entry.Name)
		if err != nil || sess.Snapshot().ID != entry.ID {
			continue
		}
		if sess.StateSnapshot() == session.StateRunning && sess.HasProcess() {
			// A prior resume may have launched the process but failed to persist
			// Running. Retry that write without interrupting any newly accepted turn.
			if err := m.srv.mgr.SetRunning(sess); err != nil {
				remaining = append(remaining, entry)
				failures = append(failures, fmt.Errorf("%s: %w", entry.Name, err))
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			remaining = append(remaining, entry)
			failures = append(failures, err)
			continue
		}
		if err := m.resume(ctx, sess); err != nil {
			remaining = append(remaining, entry)
			failures = append(failures, fmt.Errorf("%s: %w", entry.Name, err))
		}
	}
	m.record.Sessions = remaining
	if len(remaining) > 0 {
		if err := m.save(); err != nil {
			failures = append(failures, err)
		}
		return m.response(), errors.Join(failures...)
	}
	if err := os.Remove(m.path()); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	m.record = nil
	m.srv.mgr.ResumeMaintenanceGoals()
	return m.response(), nil
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
		if m.record != nil && (m.record.Phase == "recovering" || !m.now().Before(m.record.Expires)) {
			recoveryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if _, err := m.recover(recoveryCtx); err != nil {
				fmt.Fprintf(os.Stderr, "runtime: maintenance recovery: %v\n", err)
				progressed = false
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
