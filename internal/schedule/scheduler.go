package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/sessionlaunch"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

// tickInterval is how often the scheduler scans for due tasks. Sub-minute
// precision is intentionally not offered (see minInterval in spec.go).
const tickInterval = 20 * time.Second

// submitTimeout bounds a single async prompt submission or template session launch.
const submitTimeout = 60 * time.Second

const (
	subtaskSubmitTimeout                      = 30 * time.Second
	subtaskSubmitWorkers                      = 8
	promptDeliverWorkers                      = 8
	observeWorkers                            = 4
	observeMaxAge                             = 24 * time.Hour
	observeMaxBackoff                         = 15 * time.Minute
	defaultScheduledSessionIdleTimeoutSeconds = int64(30 * time.Minute / time.Second)
)

// ErrObservationPermanent marks an observation failure that can never recover
// (for example, a replaced session ID or mismatched task identity). Reconcile
// converges such executions to terminal unknown rather than polling forever.
var ErrObservationPermanent = errors.New("schedule observation permanently unavailable")

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

// NewSessionLaunchPreparer enriches a new-session-per-run launch request with
// control-plane execution identity, actor token, and resolved profile spec
// derived from the authorized template session.
type NewSessionLaunchPreparer func(ctx context.Context, task Task, template *pb.Session, req *sessionlaunch.StartRequest) error

// ExecutionListener receives read-only notifications when a scheduled execution
// is persisted or observed. It never alters scheduling or retry behavior.
type ExecutionListener func(task Task, exec Execution)

// Scheduler runs due tasks on a background tick, injecting prompts into
// sessions via the hub.
type Scheduler struct {
	sub               Submitter
	sessionStarter    sessionlaunch.Starter
	store             *Store
	taskSubmit        TaskSubmitter
	authorize         SessionPromptAuthorizer
	prepareNewSession NewSessionLaunchPreparer
	subtaskSem        chan struct{}
	promptSem         chan struct{}
	wakeCh            chan struct{}
	deferred          atomic.Bool
	observer          OutcomeReader
	onExecution       ExecutionListener

	inFlightMu sync.Mutex
	inFlight   map[int64]struct{}
	wg         sync.WaitGroup
}

func NewWithTaskSubmitter(sub Submitter, store *Store, starter sessionlaunch.Starter, taskSubmit TaskSubmitter) *Scheduler {
	return &Scheduler{
		sub:            sub,
		sessionStarter: starter,
		store:          store,
		taskSubmit:     taskSubmit,
		subtaskSem:     make(chan struct{}, subtaskSubmitWorkers),
		promptSem:      make(chan struct{}, promptDeliverWorkers),
		wakeCh:         make(chan struct{}, 1),
		inFlight:       make(map[int64]struct{}),
	}
}

// SetSessionPromptAuthorizer installs the control-plane authorization callback.
// A nil callback preserves legacy installations that do not have identities.
func (s *Scheduler) SetSessionPromptAuthorizer(authorize SessionPromptAuthorizer) {
	s.authorize = authorize
}

// SetNewSessionLaunchPreparer installs the control-plane callback that prepares
// identity, actor tokens, and resolved profile specs for new-session-per-run.
func (s *Scheduler) SetNewSessionLaunchPreparer(prepare NewSessionLaunchPreparer) {
	s.prepareNewSession = prepare
}

// Wait blocks until all in-flight asynchronous schedule deliveries, subtask
// submissions, and observer routines have completed.
func (s *Scheduler) Wait() {
	s.wg.Wait()
}

// TriggerNow executes one immediate manual run for schedule `id` without
// modifying its enabled state or next_run_at cadence.
func (s *Scheduler) TriggerNow(ctx context.Context, id int64) (Execution, string, error) {
	t, err := s.store.Get(id)
	if err != nil {
		return Execution{}, "", err
	}
	scheduleType, err := NormalizeScheduleType(t.ScheduleType)
	if err != nil {
		return Execution{}, "", err
	}
	now := time.Now()
	run, err := s.store.JournalManualTrigger(t, now)
	if err != nil {
		return Execution{}, "", err
	}
	s.beginInFlight(run.ID)
	defer s.endInFlight(run.ID)

	if scheduleType == ScheduleTypeSubtask {
		if s.taskSubmit == nil {
			status := "error: scheduled subtask submitter unavailable"
			run.Status = "missed"
			run.Reason = "not_dispatched"
			run.Terminal = true
			s.recordTrackedSubtask(run, "", status)
			return run, status, nil
		}
		submitCtx, cancel := context.WithTimeout(ctx, subtaskSubmitTimeout)
		defer cancel()
		taskID, submitErr := s.taskSubmit.SubmitScheduledTask(submitCtx, t.TaskSpecJson)
		status := ""
		if submitErr != nil {
			markAdmissionError(&run, submitErr)
			status = "error: " + submitErr.Error()
			taskID = ""
		} else if taskID == "" {
			status = "error: empty task id"
		} else {
			status = "ok:task:" + taskID
		}
		s.recordTrackedSubtask(run, taskID, status)
		run.TaskID = taskID
		finishAdmission(&run, status, time.Now())
		return run, status, nil
	}

	status := s.deliver(context.WithValue(ctx, executionKey{}, &run), t, now)
	_, _ = s.store.UpdateLastStatusIfRun(t.ID, now.Unix(), status)
	_ = s.store.RecordScheduleRun(t.ID, now.Unix(), "", status)
	finishAdmission(&run, status, now)
	if err := s.store.Observe(run, now); err != nil {
		log.Warn().Err(err).Msg("scheduler: persist manual prompt observation")
	} else {
		s.notifyExecution(t, run)
	}
	return run, status, nil
}

func (s *Scheduler) beginInFlight(execID int64) {
	s.inFlightMu.Lock()
	if s.inFlight == nil {
		s.inFlight = make(map[int64]struct{})
	}
	s.inFlight[execID] = struct{}{}
	s.inFlightMu.Unlock()
}

func (s *Scheduler) endInFlight(execID int64) {
	s.inFlightMu.Lock()
	delete(s.inFlight, execID)
	s.inFlightMu.Unlock()
}

func (s *Scheduler) isInFlight(execID int64) bool {
	s.inFlightMu.Lock()
	_, ok := s.inFlight[execID]
	s.inFlightMu.Unlock()
	return ok
}

// Notify wakes the scheduler loop immediately to re-evaluate the earliest due
// schedule after a create, update, or enable mutation.
func (s *Scheduler) Notify() {
	if s == nil || s.wakeCh == nil {
		return
	}
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *Scheduler) notifyIfDeferred() {
	if !s.deferred.Swap(false) {
		return
	}
	s.Notify()
}

func (s *Scheduler) nextWait(now time.Time) time.Duration {
	if s.store == nil {
		return tickInterval
	}
	earliest, err := s.store.EarliestNextRun()
	if err != nil || earliest == nil {
		return tickInterval
	}
	wait := time.Until(time.Unix(*earliest, 0))
	if !now.IsZero() {
		wait = time.Unix(*earliest, 0).Sub(now)
	}
	if wait <= 0 {
		// Guard against tight spin when due tasks are deferred waiting for busy workers.
		if s.deferred.Load() {
			return 250 * time.Millisecond
		}
		return 0
	}
	if wait > tickInterval {
		return tickInterval
	}
	return wait
}

// Start runs the event-driven schedule loop until ctx is cancelled, then waits
// for all in-flight workers and the observer loop to finish before returning.
func (s *Scheduler) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runObserver(ctx)
	}()
	timer := time.NewTimer(s.nextWait(time.Now()))
	defer timer.Stop()
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(s.nextWait(time.Now()))
	}
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-timer.C:
			s.runDue(ctx, time.Now())
			resetTimer()
		case <-s.wakeCh:
			s.runDue(ctx, time.Now())
			resetTimer()
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
	loc, _, err := ParseLocation(t.Timezone)
	if err != nil {
		log.Error().Err(err).Int64("id", t.ID).Str("timezone", t.Timezone).Msg("scheduler: invalid timezone, disabling")
		_, _ = s.store.SetEnabled(t.ID, false)
		return
	}
	spec, err := ParseSpecInLocation(t.Spec, loc, now)
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

	// Acquire a worker slot before claiming so busy workers defer the claim
	// instead of silently advancing next_run_at and dropping the occurrence.
	select {
	case s.promptSem <- struct{}{}:
	default:
		s.deferred.Store(true)
		log.Info().Int64("id", t.ID).Msg("scheduler: session-prompt workers busy, deferring claim")
		return
	}

	var next *int64
	if spec.Recurring() {
		var scheduledAt time.Time
		if t.NextRunAt != nil {
			scheduledAt = time.Unix(*t.NextRunAt, 0)
		}
		if at, ok := spec.NextAfterScheduled(scheduledAt, now); ok {
			next = unixPtr(at)
		}
	}
	run, claimed, err := s.store.ClaimTracked(t, now, next)
	if err != nil || !claimed {
		<-s.promptSem
		s.notifyIfDeferred()
		return
	}
	s.beginInFlight(run.ID)
	s.wg.Add(1)
	go s.deliverSessionPrompt(ctx, t, spec, run, now)
}

func (s *Scheduler) deliverSessionPrompt(ctx context.Context, t Task, spec Spec, run Execution, now time.Time) {
	defer s.wg.Done()
	defer func() {
		<-s.promptSem
		s.notifyIfDeferred()
	}()
	defer s.endInFlight(run.ID)

	status := s.deliver(context.WithValue(ctx, executionKey{}, &run), t, now)
	if !spec.Recurring() && status == statusSkippedOffline {
		status = statusMissed
	}
	_, _ = s.store.UpdateLastStatusIfRun(t.ID, now.Unix(), status)
	_ = s.store.RecordScheduleRun(t.ID, now.Unix(), "", status)
	finishAdmission(&run, status, now)
	if err := s.store.Observe(run, now); err != nil {
		log.Warn().Err(err).Msg("scheduler: persist prompt observation")
	} else {
		s.notifyExecution(t, run)
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
		var scheduledAt time.Time
		if t.NextRunAt != nil {
			scheduledAt = time.Unix(*t.NextRunAt, 0)
		}
		if nextAt, ok := spec.NextAfterScheduled(scheduledAt, now); ok {
			next = unixPtr(nextAt)
		}
	}
	if s.taskSubmit == nil {
		run, claimed, err := s.store.ClaimTracked(t, now, next)
		if err != nil {
			log.Warn().Err(err).Int64("id", t.ID).Msg("scheduler: claim subtask run")
			return
		}
		if !claimed {
			return
		}
		status := statusMissed
		if spec.Recurring() {
			status = "error: scheduled subtask submitter unavailable"
		}
		run.Status = "missed"
		run.Reason = "not_dispatched"
		run.Terminal = true
		s.recordTrackedSubtask(run, "", status)
		return
	}

	// Acquire a worker slot before claiming so a burst of concurrent schedules
	// (e.g. multiple daily 09:00 rules) defers instead of silently dropping.
	select {
	case s.subtaskSem <- struct{}{}:
	default:
		s.deferred.Store(true)
		log.Info().Int64("id", t.ID).Msg("scheduler: subtask workers busy, deferring claim")
		return
	}

	run, claimed, err := s.store.ClaimTracked(t, now, next)
	if err != nil {
		<-s.subtaskSem
		s.notifyIfDeferred()
		log.Warn().Err(err).Int64("id", t.ID).Msg("scheduler: claim subtask run")
		return
	}
	if !claimed {
		<-s.subtaskSem
		s.notifyIfDeferred()
		return
	}

	s.beginInFlight(run.ID)
	s.wg.Add(1)
	go s.submitSubtask(ctx, t, run)
}

func (s *Scheduler) submitSubtask(ctx context.Context, t Task, run Execution) {
	defer s.wg.Done()
	defer func() {
		<-s.subtaskSem
		s.notifyIfDeferred()
	}()
	defer s.endInFlight(run.ID)

	submitCtx, cancel := context.WithTimeout(ctx, subtaskSubmitTimeout)
	defer cancel()
	taskID, err := s.taskSubmit.SubmitScheduledTask(submitCtx, t.TaskSpecJson)
	status := ""
	if err != nil {
		markAdmissionError(&run, err)
		status = "error: " + err.Error()
		taskID = ""
	} else if taskID == "" {
		status = "error: empty task id"
	} else {
		status = "ok:task:" + taskID
	}
	s.recordTrackedSubtask(run, taskID, status)
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
			if r, _ := ctx.Value(executionKey{}).(*Execution); r != nil {
				r.Reason = "admission_rejected"
			}
			return "error: " + err.Error()
		}
	}
	sendCtx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	resp, err := s.sub.Route(sendCtx, t.TargetID, "Submit", &pb.SubmitRequest{
		Name:   t.SessionName,
		Text:   t.Prompt,
		Source: "scheduler",
		Async:  true,
	})
	if err != nil {
		log.Warn().Err(err).Int64("id", t.ID).Msg("scheduler: submit failed")
		if r, _ := ctx.Value(executionKey{}).(*Execution); r != nil {
			markAdmissionError(r, err)
		}
		return "error: " + err.Error()
	}
	captureSubmission(ctx, resp, t.SessionName)
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
			if r, _ := ctx.Value(executionKey{}).(*Execution); r != nil {
				r.Reason = "admission_rejected"
			}
			return "error: " + err.Error()
		}
	}
	var execID int64
	if r, _ := ctx.Value(executionKey{}).(*Execution); r != nil {
		execID = r.ID
	}
	name := scheduledSessionName(t.ID, execID, now)
	runCtx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	idleTimeout := template.IdleTimeoutSeconds
	if idleTimeout <= 0 {
		idleTimeout = defaultScheduledSessionIdleTimeoutSeconds
	}
	startReq := sessionlaunch.StartRequest{
		TargetID: t.TargetID,
		Name:     name,
		Launch: agentprofile.LaunchProjection{
			AgentProfile: template.AgentProfile,
			AgentType:    template.AgentType,
			ExtraArgs:    append([]string(nil), template.ExtraArgs...),
			Env:          append([]string(nil), template.Env...),
		},
		Workspace: sessionlaunch.WorkspaceRequest{
			Cwd:    template.Cwd,
			Source: template.WorkspaceSource,
			Scope:  template.WorkspaceScope,
		},
		IdleTimeoutSeconds: &idleTimeout,
		ExecutionIdentity:  template.GetExecutionIdentity(),
	}
	if s.prepareNewSession != nil {
		if err := s.prepareNewSession(runCtx, t, template, &startReq); err != nil {
			log.Warn().Err(err).Int64("id", t.ID).Str("session", name).Msg("scheduler: prepare new session failed")
			if r, _ := ctx.Value(executionKey{}).(*Execution); r != nil {
				r.Reason = "admission_rejected"
			}
			return "error: prepare: " + err.Error()
		}
	}
	session, err := s.sessionStarter.Start(runCtx, startReq)
	if err != nil {
		log.Warn().Err(err).Int64("id", t.ID).Str("session", name).Msg("scheduler: run failed")
		if r, _ := ctx.Value(executionKey{}).(*Execution); r != nil {
			markAdmissionError(r, err)
		}
		return "error: run: " + err.Error()
	}
	resp, err := s.sub.Route(runCtx, t.TargetID, "Submit", &pb.SubmitRequest{
		Name:   session.Name,
		Text:   t.Prompt,
		Source: "scheduler",
		Async:  true,
	})
	if err != nil {
		log.Warn().Err(err).Int64("id", t.ID).Str("session", session.Name).Msg("scheduler: submit failed")
		if r, _ := ctx.Value(executionKey{}).(*Execution); r != nil {
			markAdmissionError(r, err)
		}
		return "error: submit: " + err.Error()
	}
	captureSubmission(ctx, resp, session.Name)
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

func scheduledSessionName(taskID, execID int64, now time.Time) string {
	stamp := strings.ToLower(now.UTC().Format("20060102-150405"))
	if execID > 0 {
		return fmt.Sprintf("sched-%d-%s-%d", taskID, stamp, execID)
	}
	return fmt.Sprintf("sched-%d-%s", taskID, stamp)
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

// OutcomeReader reads authoritative execution state through the control plane.
type OutcomeReader interface {
	ObserveScheduleExecution(context.Context, Execution) (Execution, error)
}

func (s *Scheduler) SetOutcomeReader(r OutcomeReader) { s.observer = r }

// SetExecutionListener installs a read-only listener invoked after scheduled
// execution admission and observation updates.
func (s *Scheduler) SetExecutionListener(fn ExecutionListener) { s.onExecution = fn }

func (s *Scheduler) notifyExecution(t Task, exec Execution) {
	if s.onExecution != nil {
		s.onExecution(t, exec)
	}
}

type executionKey struct{}

func captureSubmission(ctx context.Context, msg *pb.TunnelMsg, name string) {
	r, _ := ctx.Value(executionKey{}).(*Execution)
	if r == nil || msg == nil {
		return
	}
	var resp pb.SubmitResponse
	if json.Unmarshal(msg.Payload, &resp) != nil {
		return
	}
	r.SessionName = name
	r.SessionID = resp.SessionId
	r.LaunchID = resp.LaunchId
	r.TurnID = resp.TurnId
}
func finishAdmission(r *Execution, status string, now time.Time) {
	if r.Terminal {
		r.FinishedAt = now.Unix()
		return
	}
	r.Status = "submitted"
	switch {
	case status == statusMissed || status == statusSkippedOffline:
		r.Status = "missed"
		if r.Reason == "" {
			r.Reason = "not_dispatched"
		}
		r.Terminal = true
	case strings.HasPrefix(status, "error:"):
		r.Status = "unknown"
		if r.Reason == "admission_rejected" {
			r.Status = "missed"
			r.Terminal = true
		} else {
			r.Reason = "admission_unconfirmed"
			if r.TaskID == "" && r.TurnID == "" {
				// Admission finished without a trackable receipt; converge to
				// terminal unknown immediately so reconcile does not poll forever.
				r.Terminal = true
			}
		}
	}
	if r.Terminal {
		r.FinishedAt = now.Unix()
	}
}
func (s *Scheduler) recordTrackedSubtask(r Execution, taskID, status string) {
	s.recordSubtaskFinal(r.ScheduleID, r.SubmittedAt, taskID, status)
	r.TaskID = taskID
	finishAdmission(&r, status, time.Now())
	if err := s.store.Observe(r, time.Now()); err != nil {
		log.Warn().Err(err).Msg("scheduler: persist task observation")
	} else if t, getErr := s.store.Get(r.ScheduleID); getErr == nil {
		s.notifyExecution(t, r)
	}
}

func observeBackoff(failures int) time.Duration {
	if failures <= 1 {
		return tickInterval
	}
	shift := failures - 1
	if shift > 6 {
		shift = 6
	}
	d := tickInterval << shift
	if d > observeMaxBackoff {
		return observeMaxBackoff
	}
	return d
}

func (s *Scheduler) reconcile(ctx context.Context, now time.Time) {
	if s.observer == nil {
		return
	}
	runs, err := s.store.PendingExecutions()
	if err != nil {
		return
	}
	var ready []Execution
	for _, r := range runs {
		if s.isInFlight(r.ID) {
			continue
		}
		if r.NextObserveAt > now.Unix() {
			continue
		}
		ready = append(ready, r)
	}
	if len(ready) == 0 {
		return
	}
	sem := make(chan struct{}, observeWorkers)
	var wg sync.WaitGroup
	for _, r := range ready {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(r Execution) {
			defer wg.Done()
			defer func() { <-sem }()
			s.reconcileOne(ctx, r, now)
		}(r)
	}
	wg.Wait()
}

func (s *Scheduler) reconcileOne(ctx context.Context, r Execution, now time.Time) {
	if ctx.Err() != nil {
		return
	}
	if r.TaskID == "" && r.TurnID == "" {
		if now.Unix()-r.SubmittedAt < 60 {
			return
		}
		r.Status = "unknown"
		r.Reason = "admission_unconfirmed"
		r.Terminal = true
		r.FinishedAt = now.Unix()
	} else {
		readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		observed, e := s.observer.ObserveScheduleExecution(readCtx, r)
		cancel()
		if e != nil {
			if errors.Is(e, context.Canceled) && ctx.Err() != nil {
				return
			}
			r.Status = "unknown"
			r.Reason = "observation_unavailable"
			r.ObserveFailures++
			if errors.Is(e, ErrObservationPermanent) || now.Unix()-r.SubmittedAt >= int64(observeMaxAge.Seconds()) {
				r.Terminal = true
				r.FinishedAt = now.Unix()
				r.NextObserveAt = 0
			} else {
				r.NextObserveAt = now.Add(observeBackoff(r.ObserveFailures)).Unix()
			}
		} else {
			r = observed
			r.ObserveFailures = 0
			r.NextObserveAt = 0
		}
	}
	if err := s.store.Observe(r, now); err != nil {
		log.Warn().Err(err).Msg("scheduler: reconcile observation")
	} else if t, getErr := s.store.Get(r.ScheduleID); getErr == nil {
		s.notifyExecution(t, r)
	}
}

func markAdmissionError(r *Execution, err error) {
	if err == nil {
		return
	}
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		switch grpcstatus.Code(cur) {
		case codes.InvalidArgument, codes.PermissionDenied, codes.Unauthenticated, codes.FailedPrecondition, codes.NotFound, codes.ResourceExhausted:
			r.Reason = "admission_rejected"
			return
		}
	}
	msg := err.Error()
	for _, marker := range []string{
		"code = InvalidArgument",
		"code = PermissionDenied",
		"code = Unauthenticated",
		"code = FailedPrecondition",
		"code = NotFound",
		"code = ResourceExhausted",
	} {
		if strings.Contains(msg, marker) {
			r.Reason = "admission_rejected"
			return
		}
	}
}

func (s *Scheduler) runObserver(ctx context.Context) {
	if s.observer == nil {
		return
	}
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		s.reconcile(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
