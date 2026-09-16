package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// tickInterval is how often the scheduler scans for due tasks. Sub-minute
// precision is intentionally not offered (see minInterval in spec.go).
const tickInterval = 20 * time.Second

// submitTimeout bounds a single prompt injection.
const submitTimeout = 10 * time.Minute

const (
	subtaskSubmitTimeout = 30 * time.Second
	subtaskSubmitWorkers = 4
)

// Submitter is the subset of *tunnel.Hub the scheduler needs. It is an
// interface so the tick loop can be unit-tested without a real hub.
type Submitter interface {
	ListTargets() []*pb.Target
	Route(ctx context.Context, targetID, method string, payload interface{}) (*pb.TunnelMsg, error)
}

// TaskSubmitter submits scheduled subtask specs without waiting for task terminal state.
type TaskSubmitter interface {
	SubmitScheduledTask(ctx context.Context, specJSON []byte) (taskID string, err error)
}

// SessionPromptAuthorizer revalidates a session-prompt schedule against the
// current target session immediately before the scheduler routes any action.
// Principal policy stays in the control plane; the schedule package only
// enforces that the injected decision runs on every delivery path.
type SessionPromptAuthorizer func(ctx context.Context, task Task, session *pb.Session) error

// Scheduler runs due tasks on a background tick, injecting prompts into
// sessions via the hub.
type Scheduler struct {
	sub            Submitter
	sessionStarter sessionlaunch.Starter
	store          *Store
	taskSubmit     TaskSubmitter
	authorize      SessionPromptAuthorizer
	subtaskSem     chan struct{}
}

func NewWithTaskSubmitter(sub Submitter, store *Store, starter sessionlaunch.Starter, taskSubmit TaskSubmitter) *Scheduler {
	return &Scheduler{
		sub:            sub,
		sessionStarter: starter,
		store:          store,
		taskSubmit:     taskSubmit,
		subtaskSem:     make(chan struct{}, subtaskSubmitWorkers),
	}
}

// SetSessionPromptAuthorizer installs the control-plane authorization callback.
// A nil callback preserves legacy installations that do not have identities.
func (s *Scheduler) SetSessionPromptAuthorizer(authorize SessionPromptAuthorizer) {
	s.authorize = authorize
}

// Start runs the tick loop until ctx is cancelled. Intended to run in its own
// goroutine.
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDue(ctx, time.Now())
		}
	}
}

// runDue processes every task due at `now`. Exported-for-test via the package
// boundary; callers outside tests should use Start.
func (s *Scheduler) runDue(ctx context.Context, now time.Time) {
	due, err := s.store.DueTasks(now.Unix())
	if err != nil {
		log.Warn().Err(err).Msg("scheduler: list due tasks")
		return
	}
	for _, t := range due {
		s.fire(ctx, t, now)
	}
}

// fire attempts to deliver one task and records the outcome, advancing or
// clearing its next run time.
func (s *Scheduler) fire(ctx context.Context, t Task, now time.Time) {
	spec, err := ParseSpec(t.Spec)
	if err != nil {
		// A spec that no longer parses (e.g. corrupted row): disable it so we
		// do not spin on it every tick.
		log.Error().Err(err).Int64("id", t.ID).Str("spec", t.Spec).Msg("scheduler: unparseable spec, disabling")
		_, _ = s.store.SetEnabled(t.ID, false)
		return
	}
	scheduleType, err := NormalizeScheduleType(t.ScheduleType)
	if err != nil {
		log.Error().Err(err).Int64("id", t.ID).Str("type", t.ScheduleType).Msg("scheduler: invalid schedule type, disabling")
		_, _ = s.store.SetEnabled(t.ID, false)
		return
	}
	if scheduleType == ScheduleTypeSubtask {
		s.fireSubtask(ctx, t, spec, now)
		return
	}

	status := s.deliver(ctx, t, now)

	// Compute the next run time. Recurring tasks always advance regardless of
	// delivery outcome; one-shot tasks are consumed (next = nil).
	var next *int64
	if spec.Recurring() {
		if nextAt, ok := spec.Next(now); ok {
			next = unixPtr(nextAt)
		}
	} else if status == statusSkippedOffline {
		// One-shot whose target was offline: it will never fire usefully.
		status = statusMissed
	}

	if err := s.store.RecordRun(t.ID, now.Unix(), next, status); err != nil {
		log.Warn().Err(err).Int64("id", t.ID).Msg("scheduler: record run")
	}
}

const (
	statusOK             = "ok"
	statusSkippedOffline = "skipped:offline"
	statusMissed         = "missed"
	statusSubmitting     = "submitting"
)

func (s *Scheduler) fireSubtask(ctx context.Context, t Task, spec Spec, now time.Time) {
	var next *int64
	if spec.Recurring() {
		if nextAt, ok := spec.Next(now); ok {
			next = unixPtr(nextAt)
		}
	}
	ranAt := now.Unix()
	claimed, err := s.store.ClaimRun(t.ID, t.NextRunAt, ranAt, next, statusSubmitting)
	if err != nil {
		log.Warn().Err(err).Int64("id", t.ID).Msg("scheduler: claim subtask run")
		return
	}
	if !claimed {
		return
	}

	if s.taskSubmit == nil {
		status := statusMissed
		if spec.Recurring() {
			status = "error: scheduled subtask submitter unavailable"
		}
		s.recordSubtaskFinal(t.ID, ranAt, "", status)
		return
	}

	select {
	case s.subtaskSem <- struct{}{}:
		go s.submitSubtask(ctx, t, ranAt)
	default:
		status := statusMissed
		if spec.Recurring() {
			status = "error: scheduled subtask submission queue full"
		}
		s.recordSubtaskFinal(t.ID, ranAt, "", status)
	}
}

func (s *Scheduler) submitSubtask(ctx context.Context, t Task, ranAt int64) {
	defer func() { <-s.subtaskSem }()
	submitCtx, cancel := context.WithTimeout(ctx, subtaskSubmitTimeout)
	defer cancel()
	taskID, err := s.taskSubmit.SubmitScheduledTask(submitCtx, t.TaskSpecJson)
	status := ""
	if err != nil {
		status = "error: " + err.Error()
		taskID = ""
	} else if taskID == "" {
		status = "error: empty task id"
	} else {
		status = "ok:task:" + taskID
	}
	s.recordSubtaskFinal(t.ID, ranAt, taskID, status)
}

func (s *Scheduler) recordSubtaskFinal(scheduleID int64, ranAt int64, taskID string, status string) {
	if _, err := s.store.UpdateLastStatusIfRun(scheduleID, ranAt, status); err != nil {
		log.Warn().Err(err).Int64("id", scheduleID).Msg("scheduler: update subtask status")
	}
	if err := s.store.RecordScheduleRun(scheduleID, ranAt, taskID, status); err != nil {
		log.Warn().Err(err).Int64("id", scheduleID).Msg("scheduler: record subtask run history")
	}
}

// deliver runs a scheduled prompt according to its execution mode, returning a
// status string.
func (s *Scheduler) deliver(ctx context.Context, t Task, now time.Time) string {
	runMode, err := NormalizeRunMode(t.RunMode)
	if err != nil {
		return "error: " + err.Error()
	}
	switch runMode {
	case RunModeNewSessionPerRun:
		return s.deliverNewSessionPerRun(ctx, t, now)
	default:
		return s.deliverReuseSession(ctx, t)
	}
}

func (s *Scheduler) deliverReuseSession(ctx context.Context, t Task) string {
	sess := s.findSession(t.TargetID, t.SessionName)
	if sess == nil || !liveState(sess.State) {
		log.Info().Int64("id", t.ID).Str("target", t.TargetID).Str("session", t.SessionName).
			Msg("scheduler: target/session offline, skipping")
		return statusSkippedOffline
	}
	if s.authorize != nil {
		if err := s.authorize(ctx, t, sess); err != nil {
			log.Warn().Err(err).Int64("id", t.ID).Str("target", t.TargetID).Str("session", t.SessionName).
				Msg("scheduler: session prompt authorization failed")
			return "error: " + err.Error()
		}
	}
	sendCtx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	_, err := s.sub.Route(sendCtx, t.TargetID, "Submit", &pb.SubmitRequest{
		Name:   t.SessionName,
		Text:   t.Prompt,
		Source: "scheduler",
	})
	if err != nil {
		log.Warn().Err(err).Int64("id", t.ID).Msg("scheduler: submit failed")
		return "error: " + err.Error()
	}
	log.Info().Int64("id", t.ID).Str("target", t.TargetID).Str("session", t.SessionName).
		Msg("scheduler: prompt injected")
	return statusOK
}

func (s *Scheduler) deliverNewSessionPerRun(ctx context.Context, t Task, now time.Time) string {
	template := s.findSession(t.TargetID, t.SessionName)
	if template == nil || !liveState(template.State) {
		log.Info().Int64("id", t.ID).Str("target", t.TargetID).Str("session", t.SessionName).
			Msg("scheduler: template session offline, skipping")
		return statusSkippedOffline
	}
	if s.authorize != nil {
		if err := s.authorize(ctx, t, template); err != nil {
			log.Warn().Err(err).Int64("id", t.ID).Str("target", t.TargetID).Str("session", t.SessionName).
				Msg("scheduler: session prompt authorization failed")
			return "error: " + err.Error()
		}
	}
	name := scheduledSessionName(t.ID, now)
	runCtx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	session, err := s.sessionStarter.Start(runCtx, sessionlaunch.StartRequest{
		TargetID: t.TargetID,
		Name:     name,
		Launch: agentprofile.LaunchProjection{
			AgentType: template.AgentType,
			ExtraArgs: append([]string(nil), template.ExtraArgs...),
			Env:       append([]string(nil), template.Env...),
		},
		Workspace: sessionlaunch.WorkspaceRequest{
			Cwd:    template.Cwd,
			Source: template.WorkspaceSource,
			Scope:  template.WorkspaceScope,
		},
	})
	if err != nil {
		log.Warn().Err(err).Int64("id", t.ID).Str("session", name).Msg("scheduler: run failed")
		return "error: run: " + err.Error()
	}
	_, err = s.sub.Route(runCtx, t.TargetID, "Submit", &pb.SubmitRequest{
		Name:   session.Name,
		Text:   t.Prompt,
		Source: "scheduler",
	})
	if err != nil {
		log.Warn().Err(err).Int64("id", t.ID).Str("session", session.Name).Msg("scheduler: submit failed")
		return "error: submit: " + err.Error()
	}
	log.Info().Int64("id", t.ID).Str("target", t.TargetID).Str("template", t.SessionName).Str("session", session.Name).
		Msg("scheduler: prompt injected into new session")
	return "ok:new-session:" + session.Name
}

func (s *Scheduler) sessionOnline(targetID, sessionName string) bool {
	sess := s.findSession(targetID, sessionName)
	return sess != nil && liveState(sess.State)
}

func (s *Scheduler) findSession(targetID, sessionName string) *pb.Session {
	for _, tg := range s.sub.ListTargets() {
		if tg.Id != targetID {
			continue
		}
		for _, sess := range tg.Sessions {
			if sess.Name == sessionName {
				return sess
			}
		}
	}
	return nil
}

func scheduledSessionName(taskID int64, now time.Time) string {
	return fmt.Sprintf("sched-%d-%s", taskID, strings.ToLower(now.UTC().Format("20060102-150405")))
}

// liveState reports whether a session state can accept input. Killed sessions
// linger in the hub's target list as "archived"; those are treated as offline.
func liveState(state string) bool {
	switch state {
	case "archived":
		return false
	default:
		return true
	}
}

// FirstRun computes the initial next_run_at for a freshly created spec, used by
// the RPC handler when persisting a task. It returns an error if the spec has
// no future occurrence.
func FirstRun(spec Spec, from time.Time) (*int64, error) {
	next, ok := spec.Next(from)
	if !ok {
		return nil, fmt.Errorf("schedule %q has no future run time", spec.Raw)
	}
	return unixPtr(next), nil
}
