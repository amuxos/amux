package task

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/store"
)

// ErrTaskNotFound is returned by Store.Get when no task with the given id
// exists. Callers (e.g. orphan-session reaping) use errors.Is to distinguish a
// genuinely absent task from a transient store failure.
var ErrTaskNotFound = errors.New("task not found")

// Store persists TaskRun records. amuxd/amuxlet own the task truth; the runner
// reads and writes through this interface so it stays testable with an in-memory
// implementation.
type Store interface {
	Save(*TaskRun) error
	Get(id string) (*TaskRun, error)
	List() ([]*TaskRun, error)
	Delete(id string) error
}

// EventStore is the append-only, per-task, monotonic-seq event log backing
// resumable task streams. Seq starts at 1 per task. The stored event is
// expected to be already redacted by the runner.
type EventStore interface {
	Append(taskID string, ev TaskEvent) (seq uint64, err error)
	Events(taskID string, fromSeq uint64) ([]TaskEvent, error)
}

// MemStore is an in-memory Store for tests and ephemeral use.
type MemStore struct {
	mu     sync.RWMutex
	tasks  map[string]*TaskRun
	events map[string][]TaskEvent
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{tasks: map[string]*TaskRun{}, events: map[string][]TaskEvent{}}
}

func (m *MemStore) Save(t *TaskRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *t
	m.tasks[t.ID] = &cp
	return nil
}

func (m *MemStore) Get(id string) (*TaskRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, fmt.Errorf("task %q: %w", id, ErrTaskNotFound)
	}
	cp := *t
	return &cp, nil
}

func (m *MemStore) List() ([]*TaskRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*TaskRun, 0, len(m.tasks))
	for _, t := range m.tasks {
		cp := *t
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out, nil
}

func (m *MemStore) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, id)
	delete(m.events, id)
	return nil
}

func (m *MemStore) Append(taskID string, ev TaskEvent) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seq := uint64(len(m.events[taskID])) + 1
	ev.Seq = seq
	ev.TaskID = taskID
	m.events[taskID] = append(m.events[taskID], ev)
	return seq, nil
}

func (m *MemStore) Events(taskID string, fromSeq uint64) ([]TaskEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := m.events[taskID]
	if fromSeq >= uint64(len(all)) {
		return nil, nil
	}
	out := make([]TaskEvent, len(all)-int(fromSeq))
	copy(out, all[fromSeq:])
	return out, nil
}

// SQLStore persists tasks in the runtime SQLite database.
type SQLStore struct{ db *store.DB }

// NewSQLStore returns a Store backed by the given runtime DB.
func NewSQLStore(db *store.DB) *SQLStore { return &SQLStore{db: db} }

func (s *SQLStore) Save(t *TaskRun) error { return s.db.SaveTask(toTaskRow(t)) }

func (s *SQLStore) Get(id string) (*TaskRun, error) {
	row, err := s.db.GetTask(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("task %q: %w", id, ErrTaskNotFound)
		}
		return nil, err
	}
	return fromTaskRow(row)
}

func (s *SQLStore) List() ([]*TaskRun, error) {
	rows, err := s.db.AllTasks()
	if err != nil {
		return nil, err
	}
	out := make([]*TaskRun, 0, len(rows))
	for _, r := range rows {
		t, err := fromTaskRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *SQLStore) Delete(id string) error { return s.db.DeleteTask(id) }

func (s *SQLStore) Append(taskID string, ev TaskEvent) (uint64, error) {
	ev.TaskID = taskID
	b, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	return s.db.AppendTaskEvent(taskID, string(ev.Kind), string(b), ev.At.Unix())
}

func (s *SQLStore) Events(taskID string, fromSeq uint64) ([]TaskEvent, error) {
	rows, err := s.db.TaskEvents(taskID, fromSeq)
	if err != nil {
		return nil, err
	}
	out := make([]TaskEvent, 0, len(rows))
	for _, r := range rows {
		var ev TaskEvent
		if err := json.Unmarshal([]byte(r.EventJson), &ev); err != nil {
			return nil, fmt.Errorf("task %s seq %d: decode event_json: %w", taskID, r.Seq, err)
		}
		ev.Seq = r.Seq
		ev.TaskID = taskID
		out = append(out, ev)
	}
	return out, nil
}

func toTaskRow(t *TaskRun) store.TaskRow {
	resultJSON := ""
	if t.Result != nil {
		if b, err := json.Marshal(t.Result); err == nil {
			resultJSON = string(b)
		}
	}
	credentialProfilesJSON := ""
	if len(t.CredentialProfiles) > 0 {
		if b, err := json.Marshal(t.CredentialProfiles); err == nil {
			credentialProfilesJSON = string(b)
		}
	}
	executionIdentityJSON := ""
	if b, err := json.Marshal(t.ExecutionIdentity); err == nil {
		executionIdentityJSON = string(b)
	}
	return store.TaskRow{
		ID:                 t.ID,
		ParentSessionID:    t.ParentSessionID,
		ParentTaskID:       t.ParentTaskID,
		ParentTurnID:       t.ParentTurnID,
		TurnID:             t.TurnID,
		Mode:               string(t.Mode),
		AgentType:          t.AgentType,
		AgentProfile:       t.AgentProfile,
		TargetID:           t.TargetID,
		ChildSessionId:     t.ChildSessionId,
		ChildSessionName:   t.ChildSessionName,
		IsolationMode:      string(t.IsolationMode),
		CredentialProfile:  t.CredentialProfile,
		CredentialProfiles: credentialProfilesJSON,
		ExecutionIdentity:  executionIdentityJSON,
		LaunchID:           t.LaunchID,
		IsolationBackend:   t.IsolationBackend,
		PermissionPolicy:   string(t.PermissionPolicy),
		WorkspacePolicy:    string(t.WorkspacePolicy),
		ResultGranularity:  string(t.ResultGranularity),
		SecurityLevel:      t.SecurityLevel,
		Status:             string(t.Status),
		Task:               t.Task,
		ResultJSON:         resultJSON,
		Error:              t.Error,
		TimeoutSeconds:     t.TimeoutSeconds,
		RequestedAt:        unix(t.RequestedAt),
		StartedAt:          unix(t.StartedAt),
		FinishedAt:         unix(t.FinishedAt),
	}
}

func fromTaskRow(r store.TaskRow) (*TaskRun, error) {
	t := &TaskRun{
		ID:                r.ID,
		ParentSessionID:   r.ParentSessionID,
		ParentTaskID:      r.ParentTaskID,
		ParentTurnID:      r.ParentTurnID,
		TurnID:            r.TurnID,
		Mode:              Mode(r.Mode),
		AgentType:         r.AgentType,
		AgentProfile:      r.AgentProfile,
		TargetID:          r.TargetID,
		ChildSessionId:    r.ChildSessionId,
		ChildSessionName:  r.ChildSessionName,
		IsolationMode:     IsolationMode(r.IsolationMode),
		CredentialProfile: r.CredentialProfile,
		LaunchID:          r.LaunchID,
		IsolationBackend:  r.IsolationBackend,
		PermissionPolicy:  PermissionPolicy(r.PermissionPolicy),
		WorkspacePolicy:   WorkspacePolicy(r.WorkspacePolicy),
		ResultGranularity: ResultGranularity(r.ResultGranularity),
		SecurityLevel:     r.SecurityLevel,
		Status:            Status(r.Status),
		Task:              r.Task,
		Error:             r.Error,
		TimeoutSeconds:    r.TimeoutSeconds,
		RequestedAt:       fromUnix(r.RequestedAt),
		StartedAt:         fromUnix(r.StartedAt),
		FinishedAt:        fromUnix(r.FinishedAt),
	}
	if r.ExecutionIdentity != "" {
		if err := json.Unmarshal([]byte(r.ExecutionIdentity), &t.ExecutionIdentity); err != nil {
			return nil, fmt.Errorf("task %s: decode execution_identity_json: %w", r.ID, err)
		}
	}
	if t.ExecutionIdentity.IdentityMode == "" {
		t.ExecutionIdentity = principal.LegacyHostIdentity(principal.SourceSystem)
	}
	if t.ExecutionIdentity.IsolationBackend == "" {
		t.ExecutionIdentity.IsolationBackend = t.IsolationBackend
	}
	if r.ResultJSON != "" {
		var res TaskResult
		if err := json.Unmarshal([]byte(r.ResultJSON), &res); err != nil {
			return nil, fmt.Errorf("task %s: decode result_json: %w", r.ID, err)
		}
		t.Result = &res
	}
	if r.CredentialProfiles != "" {
		if err := json.Unmarshal([]byte(r.CredentialProfiles), &t.CredentialProfiles); err != nil {
			return nil, fmt.Errorf("task %s: decode credential_profiles: %w", r.ID, err)
		}
	}
	if len(t.CredentialProfiles) == 0 && t.CredentialProfile != "" {
		t.CredentialProfiles = []string{t.CredentialProfile}
	}
	return t, nil
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}
