// Package agentinstance persists durable agent instance state on the amuxd
// (hub) side. An AgentInstance is the cross-task persistent identity of a
// long-lived sub-agent (codex v2 style): it carries the sticky target, durable
// environment references, the provider-side session/thread id, and lifecycle
// status. A single instance hosts multiple TaskRuns over its lifetime.
//
// Addressing is flat: agent_instance_id is the primary key. The optional path
// column is reserved for a future hierarchical AgentPath (codex-style agent
// tree) but is not consumed yet; instance_list does not implement path_prefix.
//
// The store uses modernc.org/sqlite (CGO-free), mirroring internal/schedule.
package agentinstance

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/principal"
)

const (
	StatusActive   = "active"
	StatusIdle     = "idle"
	StatusArchived = "archived"
)

// Instance is a persisted agent instance record.
type Instance struct {
	ID                 string // agent_instance_id, flat primary key (caller-assigned)
	Name               string
	AgentType          string
	AgentProfile       string
	ModelRef           string
	PermissionPolicy   string
	OwnerSessionID     string // parent main session
	TargetID           string // sticky target
	TargetName         string
	CredentialProfile  string
	CredentialProfiles []string
	IsolationMode      string
	HomeRef            string // durable HOME reference
	WorkspaceRef       string // durable workspace reference
	ProviderSessionID  string // provider-side session/thread id (B2 resume handle)
	ExecutionIdentity  principal.ExecutionIdentity
	LaunchID           string
	IsolationBackend   string
	Path               string // optional, reserved for future hierarchical addressing
	Status             string // active | idle | archived
	CreatedAt          int64
	UpdatedAt          int64
	LastUsedAt         int64
}

type InstanceIdentity struct {
	AgentType        string
	AgentProfile     string
	ModelRef         string
	PermissionPolicy string
}

type IdentityClaim struct {
	ID       string
	Previous InstanceIdentity
	Claimed  InstanceIdentity
	Changed  bool
}

type IdentityConflictError struct {
	Field     string
	Existing  string
	Requested string
}

func (e *IdentityConflictError) Error() string {
	return fmt.Sprintf("agent instance is fixed to %s %q; requested %q", e.Field, e.Existing, e.Requested)
}

// Filter narrows List/GetByOwner queries. Empty fields are ignored.
type Filter struct {
	OwnerSessionID string
	TargetID       string
	Status         string
}

// Store persists agent instances in SQLite (modernc, CGO-free).
type Store struct {
	db *sql.DB
}

// DefaultPath returns ~/.amux/agent_instances.db.
func DefaultPath() string {
	return filepath.Join(amuxhome.Dir(), "agent_instances.db")
}

// OpenStore opens (creating if needed) the agent instance database and migrates it.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("agentinstance db mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("agentinstance db open: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agentinstance db foreign keys: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_instances (
			id                 TEXT PRIMARY KEY,
			name               TEXT NOT NULL DEFAULT '',
			agent_type         TEXT NOT NULL DEFAULT '',
			agent_profile      TEXT NOT NULL DEFAULT '',
			model_ref          TEXT NOT NULL DEFAULT '',
			permission_policy  TEXT NOT NULL DEFAULT '',
			owner_session_id   TEXT NOT NULL DEFAULT '',
			target_id          TEXT NOT NULL DEFAULT '',
			target_name        TEXT NOT NULL DEFAULT '',
			credential_profile TEXT NOT NULL DEFAULT '',
			credential_profiles TEXT NOT NULL DEFAULT '',
			isolation_mode     TEXT NOT NULL DEFAULT '',
			home_ref           TEXT NOT NULL DEFAULT '',
			workspace_ref      TEXT NOT NULL DEFAULT '',
			provider_session_id TEXT NOT NULL DEFAULT '',
			execution_identity_json TEXT NOT NULL DEFAULT '',
			launch_id TEXT NOT NULL DEFAULT '',
			isolation_backend TEXT NOT NULL DEFAULT '',
			path               TEXT NOT NULL DEFAULT '',
			status             TEXT NOT NULL DEFAULT 'active',
			created_at         INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at         INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			last_used_at       INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)
	`)
	if err != nil {
		return fmt.Errorf("agentinstance migrate: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE agent_instances ADD COLUMN agent_profile TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(`ALTER TABLE agent_instances ADD COLUMN permission_policy TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(`ALTER TABLE agent_instances ADD COLUMN model_ref TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(`ALTER TABLE agent_instances ADD COLUMN path TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("agentinstance migrate path: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE agent_instances ADD COLUMN credential_profiles TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("agentinstance migrate credential_profiles: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE agent_instances ADD COLUMN execution_identity_json TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("agentinstance migrate execution_identity_json: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE agent_instances ADD COLUMN launch_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("agentinstance migrate launch_id: %w", err)
	}
	if err := s.addColumnIfMissing(`ALTER TABLE agent_instances ADD COLUMN isolation_backend TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("agentinstance migrate isolation_backend: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_agent_instances_owner
		ON agent_instances(owner_session_id)
	`); err != nil {
		return fmt.Errorf("agentinstance owner index: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_agent_instances_target
		ON agent_instances(target_id, status)
	`); err != nil {
		return fmt.Errorf("agentinstance target index: %w", err)
	}
	return nil
}

// NormalizeStatus validates a status. Empty means the default (active on create).
func NormalizeStatus(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return StatusActive, nil
	}
	switch v {
	case StatusActive, StatusIdle, StatusArchived:
		return v, nil
	default:
		return "", fmt.Errorf("invalid agent instance status %q", v)
	}
}

// Put inserts or replaces an instance by id. CreatedAt is preserved on replace;
// UpdatedAt is refreshed. LastUsedAt is set to now unless the caller provided a
// non-zero value (in which case it is honored).
func (s *Store) Put(in Instance) (Instance, error) {
	status, err := NormalizeStatus(in.Status)
	if err != nil {
		return Instance{}, err
	}
	now := nowUnix()
	// Honor a caller-provided LastUsedAt; default to now when zero.
	lastUsed := in.LastUsedAt
	if lastUsed == 0 {
		lastUsed = now
	}
	credentialProfilesJSON := ""
	if len(in.CredentialProfiles) > 0 {
		b, err := json.Marshal(in.CredentialProfiles)
		if err != nil {
			return Instance{}, fmt.Errorf("agentinstance encode credential_profiles: %w", err)
		}
		credentialProfilesJSON = string(b)
	}
	identityJSON, err := json.Marshal(in.ExecutionIdentity)
	if err != nil {
		return Instance{}, fmt.Errorf("agentinstance encode execution identity: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO agent_instances
			(id, name, agent_type, agent_profile, model_ref, owner_session_id, target_id, target_name,
			 credential_profile, credential_profiles, isolation_mode, home_ref, workspace_ref,
			 provider_session_id, execution_identity_json, launch_id, isolation_backend,
			 path, status, created_at, updated_at, last_used_at, permission_policy)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			agent_type = excluded.agent_type,
			agent_profile = excluded.agent_profile,
			model_ref = excluded.model_ref,
			permission_policy = CASE WHEN agent_instances.permission_policy = '' THEN excluded.permission_policy ELSE agent_instances.permission_policy END,
			owner_session_id = excluded.owner_session_id,
			target_id = excluded.target_id,
			target_name = excluded.target_name,
			credential_profile = excluded.credential_profile,
			credential_profiles = excluded.credential_profiles,
			isolation_mode = excluded.isolation_mode,
			home_ref = excluded.home_ref,
			workspace_ref = excluded.workspace_ref,
			provider_session_id = excluded.provider_session_id,
			execution_identity_json = CASE
			  WHEN agent_instances.execution_identity_json = '' THEN excluded.execution_identity_json
			  ELSE agent_instances.execution_identity_json END,
			launch_id = excluded.launch_id,
			isolation_backend = excluded.isolation_backend,
			path = excluded.path,
			status = excluded.status,
			updated_at = excluded.updated_at,
			last_used_at = excluded.last_used_at
	`, in.ID, in.Name, in.AgentType, in.AgentProfile, in.ModelRef, in.OwnerSessionID, in.TargetID, in.TargetName,
		in.CredentialProfile, credentialProfilesJSON, in.IsolationMode, in.HomeRef, in.WorkspaceRef,
		in.ProviderSessionID, string(identityJSON), in.LaunchID, in.IsolationBackend,
		in.Path, status, now, now, lastUsed, in.PermissionPolicy)
	if err != nil {
		return Instance{}, fmt.Errorf("agentinstance put: %w", err)
	}
	return s.Get(in.ID)
}

const selectColumns = `id, name, agent_type, agent_profile, model_ref, owner_session_id, target_id, target_name,
	credential_profile, credential_profiles, isolation_mode, home_ref, workspace_ref,
	provider_session_id, execution_identity_json, launch_id, isolation_backend,
	path, status, created_at, updated_at, last_used_at, permission_policy`

// Get returns an instance by id, or sql.ErrNoRows if absent.
func (s *Store) Get(id string) (Instance, error) {
	row := s.db.QueryRow(`SELECT `+selectColumns+` FROM agent_instances WHERE id = ?`, id)
	return scanInstance(row)
}

func (s *Store) ClaimIdentity(id string, requested InstanceIdentity) (IdentityClaim, error) {
	id = strings.TrimSpace(id)
	requested.AgentType = strings.TrimSpace(requested.AgentType)
	requested.AgentProfile = strings.TrimSpace(requested.AgentProfile)
	requested.ModelRef = strings.TrimSpace(requested.ModelRef)
	requested.PermissionPolicy = strings.TrimSpace(requested.PermissionPolicy)
	for range 8 {
		existing, err := s.Get(id)
		if err != nil {
			return IdentityClaim{}, err
		}
		previous := InstanceIdentity{
			AgentType: existing.AgentType, AgentProfile: existing.AgentProfile, ModelRef: existing.ModelRef, PermissionPolicy: existing.PermissionPolicy,
		}
		claimed := previous
		for _, field := range []struct {
			name      string
			existing  string
			requested string
			set       func(string)
		}{
			{name: "agent type", existing: previous.AgentType, requested: requested.AgentType, set: func(v string) { claimed.AgentType = v }},
			{name: "profile", existing: previous.AgentProfile, requested: requested.AgentProfile, set: func(v string) { claimed.AgentProfile = v }},
			{name: "permission policy", existing: previous.PermissionPolicy, requested: requested.PermissionPolicy, set: func(v string) { claimed.PermissionPolicy = v }},
			{name: "model", existing: previous.ModelRef, requested: requested.ModelRef, set: func(v string) { claimed.ModelRef = v }},
		} {
			current := strings.TrimSpace(field.existing)
			if current != "" && field.requested != "" && current != field.requested {
				return IdentityClaim{}, &IdentityConflictError{Field: field.name, Existing: current, Requested: field.requested}
			}
			if current == "" && field.requested != "" {
				field.set(field.requested)
			}
		}
		claim := IdentityClaim{ID: id, Previous: previous, Claimed: claimed, Changed: claimed != previous}
		if !claim.Changed {
			return claim, nil
		}
		res, err := s.db.Exec(`UPDATE agent_instances
			SET agent_type = ?, agent_profile = ?, model_ref = ?, permission_policy = ?, updated_at = ?
			WHERE id = ? AND agent_type = ? AND agent_profile = ? AND model_ref = ? AND permission_policy = ?`,
			claimed.AgentType, claimed.AgentProfile, claimed.ModelRef, claimed.PermissionPolicy, nowUnix(), id,
			previous.AgentType, previous.AgentProfile, previous.ModelRef, previous.PermissionPolicy)
		if err != nil {
			return IdentityClaim{}, fmt.Errorf("agentinstance claim identity: %w", err)
		}
		updated, err := res.RowsAffected()
		if err != nil {
			return IdentityClaim{}, fmt.Errorf("agentinstance claim identity rows affected: %w", err)
		}
		if updated == 1 {
			return claim, nil
		}
	}
	return IdentityClaim{}, fmt.Errorf("agentinstance claim identity %q did not converge", id)
}

func (s *Store) RollbackIdentityClaim(claim IdentityClaim) error {
	if !claim.Changed {
		return nil
	}
	_, err := s.db.Exec(`UPDATE agent_instances
		SET agent_type = ?, agent_profile = ?, model_ref = ?, permission_policy = ?, updated_at = ?
		WHERE id = ? AND agent_type = ? AND agent_profile = ? AND model_ref = ? AND permission_policy = ?`,
		claim.Previous.AgentType, claim.Previous.AgentProfile, claim.Previous.ModelRef, claim.Previous.PermissionPolicy, nowUnix(), claim.ID,
		claim.Claimed.AgentType, claim.Claimed.AgentProfile, claim.Claimed.ModelRef, claim.Claimed.PermissionPolicy)
	if err != nil {
		return fmt.Errorf("agentinstance rollback identity claim: %w", err)
	}
	return nil
}

// GetByOwner returns instances owned by a parent session, newest first.
func (s *Store) GetByOwner(ownerSessionID string) ([]Instance, error) {
	rows, err := s.db.Query(`SELECT `+selectColumns+`
		FROM agent_instances WHERE owner_session_id = ?
		ORDER BY created_at DESC, id DESC`, ownerSessionID)
	if err != nil {
		return nil, fmt.Errorf("agentinstance get by owner: %w", err)
	}
	defer rows.Close()
	return scanInstances(rows)
}

// List returns instances matching the filter, newest first.
func (s *Store) List(f Filter) ([]Instance, error) {
	q := `SELECT ` + selectColumns + ` FROM agent_instances WHERE 1=1`
	var args []any
	if f.OwnerSessionID != "" {
		q += ` AND owner_session_id = ?`
		args = append(args, f.OwnerSessionID)
	}
	if f.TargetID != "" {
		q += ` AND target_id = ?`
		args = append(args, f.TargetID)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("agentinstance list: %w", err)
	}
	defer rows.Close()
	return scanInstances(rows)
}

// UpdateStatus updates an instance's status and refreshes UpdatedAt. Returns
// whether a row was updated.
func (s *Store) UpdateStatus(id string, status string) (bool, error) {
	status, err := NormalizeStatus(status)
	if err != nil {
		return false, err
	}
	res, err := s.db.Exec(`UPDATE agent_instances
		SET status = ?, updated_at = strftime('%s','now')
		WHERE id = ?`, status, id)
	if err != nil {
		return false, fmt.Errorf("agentinstance update status: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TouchProviderSession records the provider-side session/thread id and refreshes
// LastUsedAt/UpdatedAt. Used after a task records the provider AgentSessionID.
func (s *Store) TouchProviderSession(id string, providerSessionID string) (bool, error) {
	now := nowUnix()
	res, err := s.db.Exec(`UPDATE agent_instances
		SET provider_session_id = ?, last_used_at = ?, updated_at = ?
		WHERE id = ?`, providerSessionID, now, now, id)
	if err != nil {
		return false, fmt.Errorf("agentinstance touch provider session: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TouchLastUsed refreshes LastUsedAt/UpdatedAt. Called when a new task reuses
// the instance.
func (s *Store) TouchLastUsed(id string) (bool, error) {
	now := nowUnix()
	res, err := s.db.Exec(`UPDATE agent_instances
		SET last_used_at = ?, updated_at = ?
		WHERE id = ?`, now, now, id)
	if err != nil {
		return false, fmt.Errorf("agentinstance touch last used: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FinalizeExecution records the runtime-selected backend and current launch id
// without permitting the frozen principal identity to drift.
func (s *Store) FinalizeExecution(id string, identity principal.ExecutionIdentity, launchID string) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	current, err := s.Get(id)
	if err != nil {
		return err
	}
	want := identity
	want.IsolationBackend = ""
	have := current.ExecutionIdentity
	have.IsolationBackend = ""
	if !reflect.DeepEqual(have, want) {
		return fmt.Errorf("agentinstance execution identity is immutable")
	}
	if current.IsolationBackend != "" && current.IsolationBackend != identity.IsolationBackend {
		return fmt.Errorf("agentinstance isolation backend already finalized as %s", current.IsolationBackend)
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE agent_instances
		SET execution_identity_json=?, launch_id=?, isolation_backend=?, updated_at=?
		WHERE id=?`, string(encoded), launchID, identity.IsolationBackend, nowUnix(), id)
	if err != nil {
		return fmt.Errorf("agentinstance finalize execution: %w", err)
	}
	return nil
}

// Delete removes an instance. Returns whether a row was deleted.
func (s *Store) Delete(id string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM agent_instances WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("agentinstance delete: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanInstance(sc scanner) (Instance, error) {
	var in Instance
	var credentialProfilesJSON string
	var identityJSON string
	if err := sc.Scan(&in.ID, &in.Name, &in.AgentType, &in.AgentProfile, &in.ModelRef, &in.OwnerSessionID, &in.TargetID,
		&in.TargetName, &in.CredentialProfile, &credentialProfilesJSON, &in.IsolationMode, &in.HomeRef,
		&in.WorkspaceRef, &in.ProviderSessionID, &identityJSON, &in.LaunchID, &in.IsolationBackend,
		&in.Path, &in.Status,
		&in.CreatedAt, &in.UpdatedAt, &in.LastUsedAt, &in.PermissionPolicy); err != nil {
		return Instance{}, err
	}
	if credentialProfilesJSON != "" {
		if err := json.Unmarshal([]byte(credentialProfilesJSON), &in.CredentialProfiles); err != nil {
			return Instance{}, fmt.Errorf("agentinstance decode credential_profiles: %w", err)
		}
	}
	if len(in.CredentialProfiles) == 0 && in.CredentialProfile != "" {
		in.CredentialProfiles = []string{in.CredentialProfile}
	}
	if identityJSON != "" {
		if err := json.Unmarshal([]byte(identityJSON), &in.ExecutionIdentity); err != nil {
			return Instance{}, fmt.Errorf("agentinstance decode execution identity: %w", err)
		}
	}
	if in.ExecutionIdentity.IdentityMode == "" {
		in.ExecutionIdentity = principal.LegacyHostIdentity(principal.SourceSystem)
	}
	if in.ExecutionIdentity.IsolationBackend == "" {
		in.ExecutionIdentity.IsolationBackend = in.IsolationBackend
	}
	return in, nil
}

func scanInstances(rows *sql.Rows) ([]Instance, error) {
	var out []Instance
	for rows.Next() {
		in, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *Store) addColumnIfMissing(stmt string) error {
	if _, err := s.db.Exec(stmt); err != nil && !isDuplicateColumnError(err) {
		return err
	}
	return nil
}

func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column")
}

func nowUnix() int64 { return time.Now().Unix() }
