package schedule

import (
	"encoding/json"
	"errors"
	"sort"
)

// UnboundLegacy returns only schedules that predate this upgrade. The durable
// cutoff prevents recovery from racing CreateSchedule's authoritative binding,
// or later attributing new schedules whose creator could not be established.
func (s *Store) UnboundLegacy() ([]Task, error) {
	rows, err := s.db.Query(`SELECT ` + selectColumns + ` FROM scheduled_tasks
		WHERE id <= (SELECT max_schedule_id FROM schedule_visibility_upgrade WHERE id=1)
		AND NOT EXISTS (SELECT 1 FROM schedule_visibility WHERE schedule_id=scheduled_tasks.id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RecoverCreator installs verified historical provenance without changing any
// firing or notification state. Concurrent observations use the same lock;
// concurrent definition edits invalidate the inspected snapshot in SQL.
func (s *Store) RecoverCreator(t Task, c Creator) error {
	if c.PrincipalID == "" || c.BotID == "" || c.OpenID == "" || c.Source == "" || c.AccessIdentityJSON == "" {
		return errors.New("unverified historical schedule creator")
	}
	s.visMu.Lock()
	defer s.visMu.Unlock()
	runs, err := s.VisibleHistory(t.ID)
	if err != nil {
		return err
	}
	v := Visibility{Creator: c}
	if len(runs) > 0 {
		v.Latest = runs[0]
	}
	for _, r := range runs {
		if r.Status == "completed" {
			v.LastCompletedAt = max(v.LastCompletedAt, r.FinishedAt)
			v.LastCompletedScheduledAt = max(v.LastCompletedScheduledAt, r.ScheduledAt)
		}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO schedule_visibility(schedule_id,bot_id,open_id,data)
		SELECT id,?,?,? FROM scheduled_tasks WHERE id=? AND schedule_type=?
		AND owner_principal_id=? AND task_spec_json=?
		AND id <= (SELECT max_schedule_id FROM schedule_visibility_upgrade WHERE id=1)
		ON CONFLICT(schedule_id) DO NOTHING`, c.BotID, c.OpenID, raw, t.ID, t.ScheduleType, t.OwnerPrincipalID, t.TaskSpecJson)
	return err
}

// VisibleHistory preserves pre-journal submission records without turning an
// admission receipt into a successful execution or an alertable failure.
// These projections never enter the execution journal or notification counters.
func (s *Store) VisibleHistory(id int64) ([]Execution, error) {
	runs, err := s.ExecutionHistory(id)
	if err != nil {
		return nil, err
	}
	legacy, err := s.ListRuns(id, DefaultRunHistoryLimit)
	if err != nil {
		return nil, err
	}
	for _, old := range legacy {
		matched := false
		for _, r := range runs {
			if r.SubmittedAt == old.FiredAt || r.ScheduledAt == old.FiredAt || (old.TaskID != "" && r.TaskID == old.TaskID) {
				matched = true
				break
			}
		}
		if !matched {
			runs = append(runs, Execution{ScheduleID: id, ScheduledAt: old.FiredAt, SubmittedAt: old.FiredAt, TaskID: old.TaskID, Status: "legacy_unconfirmed", Terminal: true})
		}
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].ScheduledAt > runs[j].ScheduledAt })
	return runs[:min(len(runs), DefaultRunHistoryLimit)], nil
}
