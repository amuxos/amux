package hub

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

const (
	// reconcileInterval bounds how long a finished task's committed admission slot
	// is held before reclamation. Smaller = tighter capacity accounting, more
	// tunnel chatter.
	reconcileInterval = 15 * time.Second
	// reconcileRouteTimeout bounds one TaskGet probe to a runtime.
	reconcileRouteTimeout = 5 * time.Second
)

// taskReconciler keeps amuxd's task-truth (the placement index + the admission
// reservations it holds on runtimes) consistent with each amuxlet's session
// truth. For every indexed task it asks the owning runtime for the task status;
// when the task is terminal (or gone) it releases the committed admission
// reservation and drops the index entry only after release is confirmed.
//
// This is the amuxd half of design "分布式状态归属与对账", scoped to what amuxd
// owns in the runner-stays-on-amuxlet split: reservation + index reclaim. Session
// orphans are handled on the amuxlet side (the runner's child-session teardown on
// terminal, plus RecoverOrphans on amuxlet restart) because amuxd does not track
// child_session_id in this split.
type taskReconciler struct {
	hub      taskRouterHub
	sched    taskScheduler
	index    *taskIndex
	interval time.Duration
}

func newTaskReconciler(hub taskRouterHub, sched taskScheduler, index *taskIndex) *taskReconciler {
	return &taskReconciler{hub: hub, sched: sched, index: index, interval: reconcileInterval}
}

// Run reconciles on a fixed cadence until ctx is done.
func (rc *taskReconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(rc.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rc.reconcileOnce(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// reconcileOnce walks the placement index once, reclaiming terminal/gone tasks.
func (rc *taskReconciler) reconcileOnce(ctx context.Context) {
	for id, p := range rc.index.snapshot() {
		cctx, cancel := context.WithTimeout(ctx, reconcileRouteTimeout)
		resp, err := rc.hub.Route(cctx, p.TargetID, pb.TaskMethodGet, &pb.GetSubtaskRequest{Id: id})
		cancel()
		if err != nil {
			// Gone from its runtime (e.g. amuxlet wiped/pruned it) => reclaim. A
			// "not connected"/transient error keeps the entry for the next tick:
			// never release a slot for a task that might still be running.
			if isTaskGone(err) {
				rc.reclaim(id, p)
			}
			continue
		}
		var gr pb.GetSubtaskResponse
		if json.Unmarshal(resp.Payload, &gr) != nil {
			continue
		}
		var run task.TaskRun
		if json.Unmarshal(gr.TaskJson, &run) != nil {
			continue
		}
		if run.Status.IsTerminal() {
			rc.reclaim(id, p)
		}
	}
}

// reclaim keeps failed releases indexed for the next reconciliation pass.
func (rc *taskReconciler) reclaim(id string, p taskPlacement) {
	rc.index.reclaim(id, p, rc.sched.Release)
}

// isTaskGone reports whether a routed TaskGet error means the task no longer
// exists on its runtime (reclaim), versus a "target not connected"/transient
// error we must retry (keep). The signal is the routed gRPC NotFound surfaced as
// the tunnel response error string.
func isTaskGone(err error) bool {
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "not connected") {
		return false
	}
	return strings.Contains(s, "notfound") || strings.Contains(s, "not found")
}
