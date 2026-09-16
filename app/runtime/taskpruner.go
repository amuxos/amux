package runtime

import (
	"context"
	"os"
	"path/filepath"

	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/task"
)

type taskArtifactPruner struct {
	srv *server
}

func (p taskArtifactPruner) PruneTaskArtifacts(_ context.Context, run *task.TaskRun, dryRun bool) (task.PruneArtifactReport, error) {
	var report task.PruneArtifactReport
	if run == nil {
		return report, nil
	}
	if run.ChildSessionName != "" {
		report.ChildSessionName = run.ChildSessionName
	}
	if p.srv != nil && run.ChildSessionId != "" {
		if dryRun {
			sess, err := p.srv.mgr.Get(run.ChildSessionId)
			if err == nil && sess != nil {
				report.ChildSessionName = sess.Name
				ownerMatch := sess.OwnerTaskId == run.ID
				archived := sess.StateSnapshot() == session.StateArchived
				report.ChildSessionDeleted = ownerMatch && archived
				if ownerMatch && !archived {
					report.ChildSessionPending = true
				}
				if !report.ChildSessionDeleted {
					report.Skipped = append(report.Skipped, task.SkippedTask{ID: run.ID, TargetID: run.TargetID, Reason: "child session is not archived or owner mismatched"})
				}
			}
		} else {
			sess, deleted, err := p.srv.mgr.DeleteArchivedOwned(run.ID, run.ChildSessionId)
			if err != nil {
				return report, err
			}
			if sess != nil {
				report.ChildSessionName = sess.Name
			}
			if deleted {
				report.ChildSessionDeleted = true
				p.srv.cleanupSessionArtifacts(sess)
			} else if sess != nil {
				if sess.OwnerTaskId == run.ID && sess.StateSnapshot() != session.StateArchived {
					report.ChildSessionPending = true
				}
				report.Skipped = append(report.Skipped, task.SkippedTask{ID: run.ID, TargetID: run.TargetID, Reason: "child session is not archived or owner mismatched"})
			}
		}
	}

	if p.srv != nil && p.srv.amuxDir != "" {
		sandboxRoot := filepath.Join(task.SandboxesDir(p.srv.amuxDir), run.ID)
		if _, err := os.Stat(sandboxRoot); err == nil {
			report.SandboxDeleted = true
			if !dryRun {
				if err := os.RemoveAll(sandboxRoot); err != nil {
					return report, err
				}
			}
		} else if err != nil && !os.IsNotExist(err) {
			return report, err
		}
	}
	return report, nil
}
