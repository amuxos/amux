package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/amuxos/amux/internal/cleanup"
	"github.com/amuxos/amux/internal/mux"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
)

func (s *server) cleanupReferences() (cleanup.References, map[string]bool, error) {
	var refs cleanup.References
	if s.cleanupDB == nil {
		return refs, nil, fmt.Errorf("runtime database is unavailable")
	}
	rows, err := s.cleanupDB.AllSessions()
	if err != nil {
		return refs, nil, err
	}
	storedStopped := make(map[string]bool)
	for _, row := range rows {
		refs.SessionIDs = append(refs.SessionIDs, row.ID)
		refs.Workspaces = append(refs.Workspaces, row.Cwd)
		refs.AgentSessionIDs = append(refs.AgentSessionIDs, row.AgentSessionID)
		if brokerStopped(session.State(row.State), row.PID, row.BrokerSocket) {
			storedStopped[row.ID] = true
		}
	}
	stoppedBrokers := make(map[string]bool)
	// Include newly-created/in-memory Sessions and their current native thread ID.
	for _, sess := range s.mgr.List() {
		snap := sess.Snapshot()
		refs.SessionIDs = append(refs.SessionIDs, snap.ID)
		refs.Workspaces = append(refs.Workspaces, snap.Cwd)
		refs.AgentSessionIDs = append(refs.AgentSessionIDs, snap.AgentSessionID)
		if storedStopped[snap.ID] && brokerStopped(snap.State, snap.PID, snap.BrokerSocket) {
			stoppedBrokers[snap.ID] = true
		}
	}
	return refs, stoppedBrokers, nil
}

func brokerStopped(state session.State, pid int, socket string) bool {
	return (state == session.StateArchived || state == session.StateSleeping) && pid == 0 && socket == ""
}

// Hold relaunchMu while classifying and removing broker diagnostics, including
// for recovery and model changes that enter through the lower-level re-launch path.
func (s *server) sweepSessionArtifacts(ctx context.Context, protectedPaths []string, dry bool) (cleanup.References, cleanup.Step, error) {
	if err := s.lockRelaunch(ctx); err != nil {
		return cleanup.References{}, cleanup.Step{}, err
	}
	defer s.relaunchMu.Unlock()
	refs, stoppedBrokers, err := s.cleanupReferences()
	if err != nil {
		return refs, cleanup.Step{}, err
	}
	refs.ProtectedPaths = append([]string{}, protectedPaths...)
	if len(stoppedBrokers) > 0 {
		brokers, err := mux.ListBrokerProcesses()
		if err != nil {
			return refs, cleanup.Step{Name: "session_artifacts", Errors: []string{err.Error()}}, nil
		}
		for _, broker := range brokers {
			delete(stoppedBrokers, broker.SessionID)
		}
	}
	return refs, cleanup.SweepSessionArtifacts(ctx, filepath.Join(s.amuxDir, "sessions"), refs, stoppedBrokers, time.Now(), dry), nil
}

func (s *server) Cleanup(ctx context.Context, req *pb.CleanupRequest) (*pb.CleanupResponse, error) {
	s.dailyCleanupMu.Lock()
	defer s.dailyCleanupMu.Unlock()
	done, err := s.maintenance.enter(true)
	if err != nil {
		return nil, err
	}
	defer done()
	id := req.GetRunId()
	if id == "" {
		id = uuid.NewString()
	}
	report := cleanup.Report{ID: id, Target: s.cleanupTarget, StartedAt: time.Now(), DryRun: req.GetDryRun()}
	journalPath := filepath.Join(s.amuxDir, "daily-cleanup.json")
	history := map[string]cleanup.Report{}
	if !report.DryRun {
		if err := cleanup.ReadJSON(journalPath, &history); err != nil {
			return nil, err
		}
		if cached, ok := history[id]; ok && !cached.FinishedAt.IsZero() {
			return &pb.CleanupResponse{Reports: []*pb.CleanupReport{cached.Proto()}}, nil
		}
	}
	// Fail closed before lifecycle or filesystem deletion if authoritative state
	// is unavailable. Do not manufacture an empty live set.
	if _, _, err := s.cleanupReferences(); err != nil {
		return nil, err
	}
	if !report.DryRun {
		history[id] = report
		if err := cleanup.WriteJSON(journalPath, history); err != nil {
			return nil, err
		}
	}
	sessions := cleanup.Step{Name: "sessions"}
	if report.DryRun {
		for _, sess := range s.mgr.List() {
			snap := sess.Snapshot()
			if snap.State == session.StateArchived && snap.OwnerTaskId == "" && snap.AgentInstanceID == "" {
				sessions.Candidates++
			}
		}
	} else {
		result, err := s.Prune(ctx, &pb.PruneRequest{})
		sessions.Error(err)
		if result != nil {
			sessions.Candidates = int(result.Count)
			sessions.Deleted = int(result.Count)
		}
	}
	report.Steps = append(report.Steps, sessions)
	tasks := cleanup.Step{Name: "tasks"}
	taskSessions := cleanup.Step{Name: "task_sessions"}
	if s.cleanupTasks == nil {
		tasks.Error(fmt.Errorf("task runner is unavailable"))
	} else {
		result, err := s.cleanupTasks.Prune(ctx, task.PruneRequest{DryRun: report.DryRun, OlderThan: 7 * 24 * time.Hour})
		tasks.Error(err)
		tasks.Candidates = len(result.Tasks)
		tasks.Skipped = len(result.Skipped)
		if !report.DryRun {
			tasks.Deleted = len(result.Tasks)
		}
		for _, item := range result.Tasks {
			if item.DeletedChildSession {
				taskSessions.Candidates++
			}
		}
		if !report.DryRun {
			taskSessions.Deleted = taskSessions.Candidates
		}
	}
	report.Steps = append(report.Steps, tasks, taskSessions)
	s.cleanupLaunchMu.Lock()
	refs, artifacts, err := s.sweepSessionArtifacts(ctx, req.GetProtectedPaths(), report.DryRun)
	if err != nil {
		report.Steps = append(report.Steps, cleanup.Step{Name: "references", Errors: []string{err.Error()}})
	} else {
		report.Steps = append(report.Steps, artifacts)
		report.Steps = append(report.Steps, cleanup.SweepWorkspaces(ctx, filepath.Join(s.amuxDir, "workspaces"), refs, 3, time.Now(), report.DryRun))
		if home, err := os.UserHomeDir(); err != nil {
			report.Steps = append(report.Steps, cleanup.Step{Name: "provider_home", Errors: []string{err.Error()}})
		} else {
			report.Steps = append(report.Steps, cleanup.SweepServiceHomeTrust(ctx, home, report.DryRun)...)
		}
		steps, err := s.isolationRuntime.DailyCleanup(ctx, refs, report.DryRun)
		report.Steps = append(report.Steps, steps...)
		if err != nil {
			report.Steps = append(report.Steps, cleanup.Step{Name: "isolation", Errors: []string{err.Error()}})
		}
	}
	s.cleanupLaunchMu.Unlock()
	report.Steps = append(report.Steps, cleanup.RotateLogs(ctx, s.amuxDir, []string{"hub.log", "runtime.log", "agent-pack-autosync.log"}, report.DryRun))
	dbCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	report.Steps = append(report.Steps, cleanup.Vacuum(dbCtx, s.cleanupDB.DB, report.DryRun))
	cancel()
	report.FinishedAt = time.Now()
	if !report.DryRun {
		history[id] = report
		for key, r := range history {
			if r.StartedAt.Before(time.Now().Add(-30 * 24 * time.Hour)) {
				delete(history, key)
			}
		}
		if err := cleanup.WriteJSON(journalPath, history); err != nil {
			return nil, err
		}
	}
	return &pb.CleanupResponse{Reports: []*pb.CleanupReport{report.Proto()}}, nil
}
