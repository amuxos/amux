package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// parentNotifySource is the Submit source of background-task completion
// messages. The Feishu bridge renders turns it starts like any other input.
const parentNotifySource = "subtask"

const (
	parentNotifySummaryRunes = 800
	parentNotifyMaxWatch     = 72 * time.Hour
	parentNotifyRetryDelay   = 5 * time.Second
	// parentNotifySubmitAttempts bounds delivery retries when the parent
	// session is not reachable yet (for example right after an amuxd restart,
	// before its runtime reconnects).
	parentNotifySubmitAttempts = 24
	parentNotifyDeliveredTTL   = 7 * 24 * time.Hour
)

// parentNotifier delivers one "[后台任务完成]" message into a task's parent
// session when the task reaches a terminal state. It is gated by the
// experimental steer_foreground switch, read at delivery time.
//
// Pending watches and delivered keys are persisted (storePath) so a task that
// ends while amuxd is down, or is failed by a runtime restart, is still
// reported once after amuxd comes back.
type parentNotifier struct {
	mu        sync.Mutex
	watching  map[string]struct{}
	delivered map[string]time.Time
	pending   map[string]parentNotifyPending
	// storePath is the JSON state file; empty keeps state in memory only.
	storePath string
	// enabled overrides the live steer_foreground switch in tests.
	enabled    func() bool
	retryDelay time.Duration
}

type parentNotifyPending struct {
	ParentSessionID string    `json:"parent_session_id"`
	RegisteredAt    time.Time `json:"registered_at"`
}

type parentNotifyState struct {
	Pending   map[string]parentNotifyPending `json:"pending,omitempty"`
	Delivered map[string]time.Time           `json:"delivered,omitempty"`
}

func newPersistentParentNotifier(path string) *parentNotifier {
	n := &parentNotifier{storePath: path}
	n.load()
	return n
}

func (n *parentNotifier) load() {
	if n == nil || n.storePath == "" {
		return
	}
	data, err := os.ReadFile(n.storePath)
	if err != nil {
		return
	}
	var st parentNotifyState
	if err := json.Unmarshal(data, &st); err != nil {
		log.Warn().Err(err).Msg("hub: parent notify state unreadable; starting empty")
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pending = st.Pending
	n.delivered = st.Delivered
	cutoff := time.Now().Add(-parentNotifyDeliveredTTL)
	for k, at := range n.delivered {
		if at.Before(cutoff) {
			delete(n.delivered, k)
		}
	}
}

// saveLocked writes the state file atomically. Caller holds n.mu.
func (n *parentNotifier) saveLocked() {
	if n.storePath == "" {
		return
	}
	data, err := json.Marshal(parentNotifyState{Pending: n.pending, Delivered: n.delivered})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(n.storePath), 0o700)
	tmp := n.storePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Warn().Err(err).Msg("hub: persist parent notify state")
		return
	}
	_ = os.Rename(tmp, n.storePath)
}

func (n *parentNotifier) addPending(taskID, parentSessionID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pending == nil {
		n.pending = make(map[string]parentNotifyPending)
	}
	if _, ok := n.pending[taskID]; ok {
		return
	}
	n.pending[taskID] = parentNotifyPending{ParentSessionID: parentSessionID, RegisteredAt: time.Now()}
	n.saveLocked()
}

func (n *parentNotifier) removePending(taskID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.pending[taskID]; !ok {
		return
	}
	delete(n.pending, taskID)
	n.saveLocked()
}

func (n *parentNotifier) pendingTasks() map[string]parentNotifyPending {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make(map[string]parentNotifyPending, len(n.pending))
	for k, v := range n.pending {
		out[k] = v
	}
	return out
}

func (n *parentNotifier) isEnabled() bool {
	if n != nil && n.enabled != nil {
		return n.enabled()
	}
	return config.LiveSteerForeground()
}

// shouldNotifyParent reports whether a submitted task wants a completion
// message: it has a parent, is not blocking (the caller already gets the
// result) and did not opt out.
func shouldNotifyParent(spec task.Spec) bool {
	if strings.TrimSpace(spec.ParentSessionID) == "" || spec.Blocking {
		return false
	}
	return spec.NotifyParent == nil || *spec.NotifyParent
}

func (n *parentNotifier) claimWatch(taskID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.watching == nil {
		n.watching = make(map[string]struct{})
	}
	if _, ok := n.watching[taskID]; ok {
		return false
	}
	n.watching[taskID] = struct{}{}
	return true
}

func (n *parentNotifier) releaseWatch(taskID string) {
	n.mu.Lock()
	delete(n.watching, taskID)
	n.mu.Unlock()
}

func parentNotifyKey(run *task.TaskRun) string {
	return run.ID + "@" + run.FinishedAt.UTC().Format(time.RFC3339Nano)
}

// claimDelivery dedups per task id + run (finish time): one message per run.
// The claim is persisted; releaseDelivery undoes it after a failed submit.
func (n *parentNotifier) claimDelivery(run *task.TaskRun) bool {
	key := parentNotifyKey(run)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.delivered == nil {
		n.delivered = make(map[string]time.Time)
	}
	if _, ok := n.delivered[key]; ok {
		return false
	}
	n.delivered[key] = time.Now()
	n.saveLocked()
	return true
}

func (n *parentNotifier) releaseDelivery(run *task.TaskRun) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.delivered, parentNotifyKey(run))
	n.saveLocked()
}

// watchForParentNotify starts the completion watcher for one submitted task.
func (r *taskRouter) watchForParentNotify(spec task.Spec, taskID string) {
	if r == nil || r.parentNotify == nil || taskID == "" || !shouldNotifyParent(spec) || !r.parentNotify.isEnabled() {
		return
	}
	r.parentNotify.addPending(taskID, spec.ParentSessionID)
	r.startParentNotifyWatch(taskID, parentNotifyMaxWatch)
}

// resumeParentNotify restarts watches persisted before an amuxd restart. A
// task that ended meanwhile (including one failed as "interrupted by runtime
// restart") is detected on the first read and reported once.
func (r *taskRouter) resumeParentNotify() {
	if r == nil || r.parentNotify == nil {
		return
	}
	for taskID, p := range r.parentNotify.pendingTasks() {
		remaining := parentNotifyMaxWatch - time.Since(p.RegisteredAt)
		if remaining <= 0 {
			r.parentNotify.removePending(taskID)
			continue
		}
		log.Info().Str("task_id", taskID).Msg("hub: resuming parent completion watch after restart")
		r.startParentNotifyWatch(taskID, remaining)
	}
}

func (r *taskRouter) startParentNotifyWatch(taskID string, maxWatch time.Duration) {
	if !r.parentNotify.claimWatch(taskID) {
		return
	}
	go func() {
		defer r.parentNotify.releaseWatch(taskID)
		ctx, cancel := context.WithTimeout(context.Background(), maxWatch)
		defer cancel()
		run, ok := r.awaitTerminalTask(ctx, taskID)
		if !ok {
			if ctx.Err() != nil {
				r.parentNotify.removePending(taskID)
			}
			return
		}
		r.deliverParentNotify(ctx, run)
		r.parentNotify.removePending(taskID)
	}()
}

// awaitTerminalTask tails the task stream until a terminal state, falling
// back to a snapshot read when the stream ends early (runtime reconnect).
func (r *taskRouter) awaitTerminalTask(ctx context.Context, taskID string) (*task.TaskRun, bool) {
	delay := r.parentNotify.retryDelay
	if delay <= 0 {
		delay = parentNotifyRetryDelay
	}
	var fromSeq uint64
	for {
		if run, ok := r.readTaskRun(ctx, taskID); ok && run.Status.IsTerminal() {
			return run, true
		}
		if target, err := r.resolve(ctx, taskID); err == nil {
			_ = r.hub.RouteStream(ctx, target, pb.TaskMethodStream, &pb.StreamSubtaskRequest{Id: taskID, FromSeq: fromSeq}, func(msg *pb.TunnelMsg) error {
				var wire pb.SubtaskEvent
				if err := json.Unmarshal(msg.Payload, &wire); err != nil {
					return err
				}
				var ev task.TaskEvent
				if err := json.Unmarshal(wire.EventJson, &ev); err == nil && ev.Seq > fromSeq {
					fromSeq = ev.Seq
				}
				return nil
			})
		}
		if run, ok := r.readTaskRun(ctx, taskID); ok && run.Status.IsTerminal() {
			return run, true
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(delay):
		}
	}
}

func (r *taskRouter) readTaskRun(ctx context.Context, taskID string) (*task.TaskRun, bool) {
	target, err := r.resolve(ctx, taskID)
	if err != nil {
		return nil, false
	}
	getCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := r.hub.Route(getCtx, target, pb.TaskMethodGet, &pb.GetSubtaskRequest{Id: taskID})
	if err != nil {
		return nil, false
	}
	var out pb.GetSubtaskResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, false
	}
	var run task.TaskRun
	if err := json.Unmarshal(out.TaskJson, &run); err != nil || run.ID == "" {
		return nil, false
	}
	return &run, true
}

func (r *taskRouter) deliverParentNotify(ctx context.Context, run *task.TaskRun) {
	if run == nil {
		return
	}
	logger := log.With().Str("task_id", run.ID).Str("agent_instance_id", run.AgentInstanceID).
		Str("parent_session_id", run.ParentSessionID).Str("status", string(run.Status)).Logger()
	if !r.parentNotify.isEnabled() {
		logger.Debug().Msg("hub: parent completion notify skipped (steer_foreground off)")
		return
	}
	delay := r.parentNotify.retryDelay
	if delay <= 0 {
		delay = parentNotifyRetryDelay
	}
	for attempt := 1; ; attempt++ {
		retry, err := r.submitParentNotify(ctx, run, logger)
		if !retry {
			return
		}
		if attempt >= parentNotifySubmitAttempts {
			logger.Warn().Err(err).Int("attempts", attempt).Msg("hub: parent completion notify gave up")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// submitParentNotify makes one delivery attempt. retry reports a transient
// failure (parent not connected yet, submit error) worth another attempt.
func (r *taskRouter) submitParentNotify(ctx context.Context, run *task.TaskRun, logger zerolog.Logger) (bool, error) {
	parent, ok := r.parentSession(run.ParentSessionID)
	if !ok {
		// After an amuxd restart the parent's runtime may not have
		// reconnected yet; a truly deleted parent exhausts the retries.
		logger.Debug().Msg("hub: parent completion notify waiting (parent session not visible)")
		return true, fmt.Errorf("parent session not visible")
	}
	if strings.TrimSpace(parent.OwnerTaskID) != "" {
		// The parent is itself a task's child session: its run is bound to
		// one turn, so an extra queued turn there would outlive the task.
		logger.Info().Msg("hub: parent completion notify skipped (parent is a task child session)")
		return false, nil
	}
	if !r.parentNotify.claimDelivery(run) {
		return false, nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := r.hub.Route(sendCtx, parent.TargetID, "Submit", &pb.SubmitRequest{
		Name:   parent.SessionName,
		Text:   parentNotifyText(run),
		Source: parentNotifySource,
		Async:  true,
	})
	if err != nil {
		r.parentNotify.releaseDelivery(run)
		logger.Warn().Err(err).Msg("hub: parent completion notify submit failed; will retry")
		return true, err
	}
	logger.Info().Str("parent_session", parent.SessionName).Msg("hub: parent completion notify queued")
	return false, nil
}

// parentNotifyText is the completion message for the model: a header line
// with the task handles (for follow-up tool calls only), the result
// conclusion, and a tail matching the foreground protocol.
func parentNotifyText(run *task.TaskRun) string {
	header := "[后台任务完成]"
	if run.AgentInstanceID != "" {
		header += " agent_instance_id=" + run.AgentInstanceID
	}
	header += fmt.Sprintf(" task_id=%s status=%s", run.ID, run.Status)
	summary := ""
	if run.Result != nil {
		summary = noticeConclusion(run.Result.Summary)
	}
	if summary == "" {
		summary = strings.TrimSpace(run.Error)
	} else if e := strings.TrimSpace(run.Error); e != "" {
		summary += "\n错误: " + e
	}
	if summary == "" {
		summary = "（无摘要，可用 amux.task_get 查看详情）"
	}
	if runes := []rune(summary); len(runes) > parentNotifySummaryRunes {
		summary = string(runes[:parentNotifySummaryRunes]) + "…（已截断，完整结果可用 amux.task_get 查看）"
	}
	return header + "\n结论: " + summary +
		"\n（系统自动送达的后台任务通知，不是用户消息，用户看不到；无需轮询。请用自然语言向用户汇报结论，不要复述上面的 id 或状态字段；任务失败时由你判断是重试、汇报还是询问用户。）"
}

// conclusionHeading matches a closing section the background agent was asked
// to end with (结论/总结/Summary/Conclusion, optionally as a markdown heading
// or bold label).
var conclusionHeading = regexp.MustCompile(`(?im)^\s*(?:#{1,6}\s*|\*\*)?(?:最终)?(?:结论|总结|小结|结果|summary|conclusion|tl;dr)(?:\*\*)?\s*[:：]?`)

// noticeConclusion prefers the user-facing conclusion at the end of a
// background agent's final message over its working narration: the last
// conclusion section when present, otherwise the trailing paragraphs that fit
// the notice budget.
func noticeConclusion(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	if locs := conclusionHeading.FindAllStringIndex(summary, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		if tail := strings.TrimSpace(summary[last[0]:]); tail != "" && last[0] > 0 {
			return tail
		}
	}
	if len([]rune(summary)) <= parentNotifySummaryRunes {
		return summary
	}
	paras := regexp.MustCompile(`\n\s*\n`).Split(summary, -1)
	var kept []string
	total := 0
	for i := len(paras) - 1; i >= 0; i-- {
		p := strings.TrimSpace(paras[i])
		if p == "" {
			continue
		}
		n := len([]rune(p))
		if total+n > parentNotifySummaryRunes && len(kept) > 0 {
			break
		}
		kept = append([]string{p}, kept...)
		total += n
	}
	return "…" + strings.Join(kept, "\n\n")
}
