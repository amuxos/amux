package schedule

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	DeliveryTargetNone      = "none"
	DeliveryTargetOrigin    = "origin"
	DeliveryTargetCreatorDM = "creator_dm"
)

const (
	DeliveryPolicyAlways     = "always"
	DeliveryPolicyOnFailure  = "on_failure"
	DeliveryPolicyOnNonEmpty = "on_non_empty"
)

const (
	DeliveryStatusPending   = "pending"
	DeliveryStatusDelivered = "delivered"
	DeliveryStatusSkipped   = "skipped"
	DeliveryStatusFailed    = "failed"
	DeliveryStatusDenied    = "denied"
)

const (
	resultDeliveryMaxAttempts = 5
)

// NormalizeDeliveryTarget validates a schedule result delivery target.
// Empty means "none" (no proactive result posting).
func NormalizeDeliveryTarget(v string) (string, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return DeliveryTargetNone, nil
	}
	switch v {
	case DeliveryTargetNone, DeliveryTargetOrigin, DeliveryTargetCreatorDM:
		return v, nil
	default:
		return "", fmt.Errorf("invalid delivery_target %q (expected none, origin, or creator_dm)", v)
	}
}

// NormalizeDeliveryPolicy validates when a schedule result should be delivered.
// Empty means "always".
func NormalizeDeliveryPolicy(v string) (string, error) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return DeliveryPolicyAlways, nil
	}
	switch v {
	case DeliveryPolicyAlways, DeliveryPolicyOnFailure, DeliveryPolicyOnNonEmpty:
		return v, nil
	default:
		return "", fmt.Errorf("invalid delivery_policy %q (expected always, on_failure, or on_non_empty)", v)
	}
}

// IsSilentResult reports whether a completed run's result text is empty or an
// explicit silence sentinel used by monitoring/patrol tasks to suppress noise.
func IsSilentResult(result string) bool {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return true
	}
	upper := strings.ToUpper(strings.Trim(trimmed, "`*\"'.!。"))
	switch upper {
	case "NO_ALERT", "SILENT", "HEARTBEAT_OK", "NONE":
		return true
	default:
		return false
	}
}

// ShouldDeliverResult evaluates whether a terminal execution should be posted
// according to the schedule's delivery target and policy.
func ShouldDeliverResult(target, policy string, run Execution) bool {
	target = strings.TrimSpace(strings.ToLower(target))
	if target == "" || target == DeliveryTargetNone {
		return false
	}
	if !run.Terminal {
		return false
	}
	policy = strings.TrimSpace(strings.ToLower(policy))
	if policy == "" {
		policy = DeliveryPolicyAlways
	}
	switch policy {
	case DeliveryPolicyAlways:
		return true
	case DeliveryPolicyOnFailure:
		return run.Status != "completed"
	case DeliveryPolicyOnNonEmpty:
		if run.Status != "completed" {
			return true
		}
		return !IsSilentResult(run.Result)
	default:
		return false
	}
}

// ResultDelivery is one queued proactive result delivery for a terminal
// schedule execution, keyed uniquely by RunID for idempotency across restarts.
type ResultDelivery struct {
	RunID           int64  `json:"run_id"`
	ScheduleID      int64  `json:"schedule_id"`
	BotID           string `json:"bot_id"`
	OpenID          string `json:"open_id"`
	Target          string `json:"target"`
	Policy          string `json:"policy"`
	OriginRootMsgID string `json:"origin_root_msg_id,omitempty"`
	OriginChatID    string `json:"origin_chat_id,omitempty"`
	Run             Execution `json:"run"`
	CreatedAt       int64  `json:"created_at"`
	RetryAt         int64  `json:"retry_at"`
	Attempts        int    `json:"attempts"`
	LastError       string `json:"last_error,omitempty"`
}

func (s *Store) migrateResultDeliveries() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schedule_result_deliveries (
			run_id      INTEGER PRIMARY KEY,
			schedule_id INTEGER NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
			bot_id      TEXT    NOT NULL,
			data        BLOB    NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_schedule_result_deliveries_bot
			ON schedule_result_deliveries(bot_id, run_id);
	`)
	return err
}

// PendingResultDeliveries returns queued result deliveries for bot whose
// RetryAt is due at or before now.
func (s *Store) PendingResultDeliveries(bot string, now time.Time) ([]ResultDelivery, error) {
	rows, err := s.db.Query(`SELECT data FROM schedule_result_deliveries WHERE bot_id=? ORDER BY run_id ASC`, bot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResultDelivery
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var d ResultDelivery
		if err := json.Unmarshal(b, &d); err != nil {
			continue
		}
		if d.RetryAt > now.Unix() {
			continue
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkResultDelivery updates the outbox row and mirrors final delivery status
// back onto the execution record in schedule_executions.
func (s *Store) MarkResultDelivery(runID int64, status string, permanent bool, errText string, now time.Time) error {
	s.visMu.Lock()
	defer s.visMu.Unlock()

	var raw []byte
	err := s.db.QueryRow(`SELECT data FROM schedule_result_deliveries WHERE run_id=?`, runID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var d ResultDelivery
	if err := json.Unmarshal(raw, &d); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	finalStatus := ""
	switch status {
	case DeliveryStatusDelivered, DeliveryStatusDenied, DeliveryStatusSkipped:
		finalStatus = status
		if _, err := tx.Exec(`DELETE FROM schedule_result_deliveries WHERE run_id=?`, runID); err != nil {
			return err
		}
	default:
		d.Attempts++
		d.LastError = strings.TrimSpace(errText)
		if permanent || d.Attempts >= resultDeliveryMaxAttempts {
			finalStatus = DeliveryStatusFailed
			if _, err := tx.Exec(`DELETE FROM schedule_result_deliveries WHERE run_id=?`, runID); err != nil {
				return err
			}
		} else {
			backoff := 15 * time.Second << (d.Attempts - 1)
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			d.RetryAt = now.Add(backoff).Unix()
			updated, err := json.Marshal(d)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE schedule_result_deliveries SET data=? WHERE run_id=?`, updated, runID); err != nil {
				return err
			}
		}
	}

	if finalStatus != "" {
		var execRaw []byte
		if err := tx.QueryRow(`SELECT data FROM schedule_executions WHERE id=?`, runID).Scan(&execRaw); err == nil {
			var exec Execution
			if json.Unmarshal(execRaw, &exec) == nil {
				exec.DeliveryStatus = finalStatus
				if updated, err := json.Marshal(exec); err == nil {
					_, _ = tx.Exec(`UPDATE schedule_executions SET data=? WHERE id=?`, updated, runID)
				}
			}
		}
	}
	return tx.Commit()
}
