package task

import (
	"context"
	"fmt"
	"time"
)

// ArtifactPruner removes task-owned runtime artifacts that live outside the task
// store (child session metadata, sandbox directories, etc.). Implemented by
// amuxlet wiring so internal/task stays independent of session/fs details.
type ArtifactPruner interface {
	PruneTaskArtifacts(ctx context.Context, run *TaskRun, dryRun bool) (PruneArtifactReport, error)
}

type PruneArtifactReport struct {
	ChildSessionDeleted bool
	ChildSessionName    string
	SandboxDeleted      bool
	// ChildSessionPending is set when the task's child session still belongs to
	// this task but is not yet archived (e.g. a broker subprocess outlived the
	// runtime). Such a task must not be pruned: deleting its record would strand
	// the live child as an orphan no lifecycle path can reap. Prune defers it.
	ChildSessionPending bool
	Skipped             []SkippedTask
}

type PruneRequest struct {
	DryRun    bool
	OlderThan time.Duration
	Statuses  []Status
	Now       time.Time
}

type PrunedTask struct {
	ID                  string `json:"id"`
	Status              Status `json:"status,omitempty"`
	TargetID            string `json:"target_id,omitempty"`
	ChildSessionId      string `json:"child_session_id,omitempty"`
	ChildSessionName    string `json:"child_session_name,omitempty"`
	DeletedChildSession bool   `json:"deleted_child_session,omitempty"`
	DeletedSandbox      bool   `json:"deleted_sandbox,omitempty"`
}

type SkippedTask struct {
	ID       string `json:"id,omitempty"`
	TargetID string `json:"target_id,omitempty"`
	Reason   string `json:"reason"`
}

type PruneResult struct {
	Tasks   []PrunedTask  `json:"tasks,omitempty"`
	Skipped []SkippedTask `json:"skipped,omitempty"`
}

func (r *Runner) Prune(ctx context.Context, req PruneRequest) (PruneResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	statusSet := map[Status]bool{}
	for _, st := range req.Statuses {
		if !st.IsTerminal() {
			return PruneResult{}, fmt.Errorf("status %q is not terminal", st)
		}
		statusSet[st] = true
	}
	now := req.Now
	if now.IsZero() {
		now = r.now()
	}
	var cutoff time.Time
	if req.OlderThan > 0 {
		cutoff = now.Add(-req.OlderThan)
	}

	runs, err := r.opts.Store.List()
	if err != nil {
		return PruneResult{}, err
	}
	out := PruneResult{}
	for _, run := range runs {
		if run == nil || !run.Status.IsTerminal() {
			continue
		}
		if len(statusSet) > 0 && !statusSet[run.Status] {
			continue
		}
		if !cutoff.IsZero() {
			if run.FinishedAt.IsZero() || run.FinishedAt.After(cutoff) {
				continue
			}
		}

		pruned := PrunedTask{
			ID:               run.ID,
			Status:           run.Status,
			TargetID:         run.TargetID,
			ChildSessionId:   run.ChildSessionId,
			ChildSessionName: run.ChildSessionName,
		}
		if r.opts.Artifacts != nil {
			report, perr := r.opts.Artifacts.PruneTaskArtifacts(ctx, run, req.DryRun)
			if report.ChildSessionName != "" {
				pruned.ChildSessionName = report.ChildSessionName
			}
			pruned.DeletedChildSession = report.ChildSessionDeleted
			pruned.DeletedSandbox = report.SandboxDeleted
			// Defer (do not delete) a terminal task whose child session is still
			// live: pruning the record now would orphan the child. A later prune
			// finishes the job once the child archives.
			if report.ChildSessionPending {
				out.Skipped = append(out.Skipped, SkippedTask{ID: run.ID, TargetID: run.TargetID, Reason: "child session still live; deferred until archived"})
				continue
			}
			out.Skipped = append(out.Skipped, report.Skipped...)
			if perr != nil {
				out.Skipped = append(out.Skipped, SkippedTask{ID: run.ID, TargetID: run.TargetID, Reason: perr.Error()})
			}
		}
		if !req.DryRun {
			if err := r.opts.Store.Delete(run.ID); err != nil {
				return out, err
			}
		}
		out.Tasks = append(out.Tasks, pruned)
	}
	return out, nil
}
