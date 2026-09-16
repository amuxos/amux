package principal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	now      func() time.Time
	policyMu sync.RWMutex
}

func DefaultPath() string { return filepath.Join(amuxhome.Dir(), "principals.db") }

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("principal db mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("principal db open: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("principal db foreign keys: %w", err)
	}
	s := &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("principal db chmod: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS principals (
  principal_id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  display_name TEXT NOT NULL,
  status TEXT NOT NULL,
  environment_target_id TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS principal_aliases (
  provider TEXT NOT NULL,
  scope TEXT NOT NULL,
  subject TEXT NOT NULL,
  principal_id TEXT NOT NULL REFERENCES principals(principal_id),
  PRIMARY KEY(provider, scope, subject)
);
CREATE INDEX IF NOT EXISTS idx_principal_aliases_principal
  ON principal_aliases(principal_id);
CREATE TABLE IF NOT EXISTS principal_delegations (
  delegation_id TEXT PRIMARY KEY,
  actor_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
  effective_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
  active INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(actor_principal_id, effective_principal_id)
);
CREATE TABLE IF NOT EXISTS service_principal_members (
  service_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
  member_principal_id TEXT NOT NULL REFERENCES principals(principal_id),
  active INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(service_principal_id, member_principal_id)
);
CREATE TABLE IF NOT EXISTS authorization_decisions (
  decision_id TEXT PRIMARY KEY,
  actor_principal_id TEXT NOT NULL,
  effective_principal_id TEXT NOT NULL,
  delegation_id TEXT NOT NULL DEFAULT '',
  policy_revision TEXT NOT NULL,
  request_id TEXT NOT NULL DEFAULT '',
  allowed INTEGER NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS enrollment_credentials (
  credential_id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  provider TEXT NOT NULL,
  scope TEXT NOT NULL,
  subject TEXT NOT NULL,
  principal_id TEXT NOT NULL REFERENCES principals(principal_id),
  active INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_actor_credentials (
  credential_id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  principal_id TEXT NOT NULL REFERENCES principals(principal_id),
  launch_id TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_actor_credentials_launch
  ON agent_actor_credentials(launch_id);
CREATE TABLE IF NOT EXISTS principal_policy_revision (
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  revision INTEGER NOT NULL
);
INSERT OR IGNORE INTO principal_policy_revision(singleton, revision) VALUES(1, 1);
`)
	if err != nil {
		return fmt.Errorf("principal db migrate: %w", err)
	}
	return nil
}

func (s *Store) Create(p Principal) (Principal, error) {
	if strings.TrimSpace(p.ID) == "" {
		p.ID = uuid.NewString()
	}
	if p.Status == "" {
		p.Status = StatusActive
	}
	if err := validatePrincipal(p); err != nil {
		return Principal{}, err
	}
	now := s.now()
	meta, err := json.Marshal(p.Metadata)
	if err != nil {
		return Principal{}, fmt.Errorf("encode principal metadata: %w", err)
	}
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO principals
	  (principal_id, kind, display_name, status, environment_target_id, metadata_json, created_at, updated_at)
	  VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Kind, strings.TrimSpace(p.DisplayName), p.Status, strings.TrimSpace(p.EnvironmentTargetID),
		string(meta), now.Unix(), now.Unix())
	if err != nil {
		return Principal{}, fmt.Errorf("create principal: %w", err)
	}
	if err := bumpRevisionTx(tx); err != nil {
		return Principal{}, err
	}
	if err := tx.Commit(); err != nil {
		return Principal{}, err
	}
	return s.Get(p.ID)
}

func (s *Store) Get(id string) (Principal, error) {
	return getPrincipal(s.db, id)
}

type principalQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func getPrincipal(queryer principalQueryer, id string) (Principal, error) {
	var p Principal
	var metadata string
	var created, updated int64
	err := queryer.QueryRow(`SELECT principal_id, kind, display_name, status,
	  environment_target_id, metadata_json, created_at, updated_at
	  FROM principals WHERE principal_id=?`, strings.TrimSpace(id)).
		Scan(&p.ID, &p.Kind, &p.DisplayName, &p.Status, &p.EnvironmentTargetID, &metadata, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrNotFound
	}
	if err != nil {
		return Principal{}, fmt.Errorf("get principal: %w", err)
	}
	if metadata != "" {
		_ = json.Unmarshal([]byte(metadata), &p.Metadata)
	}
	p.CreatedAt = time.Unix(created, 0).UTC()
	p.UpdatedAt = time.Unix(updated, 0).UTC()
	return p, nil
}

func (s *Store) List() ([]Principal, error) {
	rows, err := s.db.Query(`SELECT principal_id, kind, display_name, status,
	  environment_target_id, metadata_json, created_at, updated_at
	  FROM principals ORDER BY created_at, principal_id`)
	if err != nil {
		return nil, fmt.Errorf("list principals: %w", err)
	}
	defer rows.Close()
	var out []Principal
	for rows.Next() {
		var p Principal
		var metadata string
		var created, updated int64
		if err := rows.Scan(&p.ID, &p.Kind, &p.DisplayName, &p.Status, &p.EnvironmentTargetID,
			&metadata, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan principal: %w", err)
		}
		_ = json.Unmarshal([]byte(metadata), &p.Metadata)
		p.CreatedAt = time.Unix(created, 0).UTC()
		p.UpdatedAt = time.Unix(updated, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Disable(id string) error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE principals SET status=?, updated_at=? WHERE principal_id=?`,
		StatusDisabled, s.now().Unix(), id)
	if err != nil {
		return fmt.Errorf("disable principal: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err := bumpRevisionTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// EnsureAliasPrincipal resolves a scoped alias to its principal, creating an
// active human principal and binding it when the alias is unknown. Both steps
// share one transaction: two messages from the same new sender arriving at once
// must not produce two principals, because each principal becomes a real OS
// account on every runtime that runs it.
//
// An existing binding is authoritative and never re-pointed — that would strip
// a returning sender of their HOME and history.
func (s *Store) EnsureAliasPrincipal(a Alias, displayName string) (Principal, error) {
	a.Scope = strings.TrimSpace(a.Scope)
	a.Subject = strings.TrimSpace(a.Subject)
	if a.Provider == "" || a.Scope == "" || a.Subject == "" {
		return Principal{}, errors.New("provider, scope and subject are required")
	}
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Principal{}, err
	}
	defer tx.Rollback()

	var boundID string
	err = tx.QueryRow(`SELECT principal_id FROM principal_aliases
	  WHERE provider=? AND scope=? AND subject=?`, a.Provider, a.Scope, a.Subject).Scan(&boundID)
	switch {
	case err == nil:
		p, getErr := getPrincipal(tx, boundID)
		if getErr != nil {
			return Principal{}, getErr
		}
		return p, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return Principal{}, err
	}

	now := s.now()
	created := Principal{
		ID:          uuid.NewString(),
		Kind:        KindHuman,
		DisplayName: strings.TrimSpace(displayName),
		Status:      StatusActive,
	}
	if created.DisplayName == "" {
		created.DisplayName = a.Subject
	}
	if err := validatePrincipal(created); err != nil {
		return Principal{}, err
	}
	if _, err := tx.Exec(`INSERT INTO principals
	  (principal_id, kind, display_name, status, environment_target_id, metadata_json, created_at, updated_at)
	  VALUES(?, ?, ?, ?, '', '{}', ?, ?)`,
		created.ID, created.Kind, created.DisplayName, created.Status, now.Unix(), now.Unix()); err != nil {
		return Principal{}, fmt.Errorf("create principal: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO principal_aliases(provider, scope, subject, principal_id)
	  VALUES(?, ?, ?, ?)`, a.Provider, a.Scope, a.Subject, created.ID); err != nil {
		return Principal{}, fmt.Errorf("bind alias: %w", err)
	}
	if err := bumpRevisionTx(tx); err != nil {
		return Principal{}, err
	}
	created.CreatedAt = now.UTC()
	created.UpdatedAt = now.UTC()
	return created, tx.Commit()
}

// ListAliases returns every scoped binding. Aliases are how an actor at some
// entry point becomes a principal, so an operator who cannot read them back
// cannot answer "who is bound to whom" without opening the database.
func (s *Store) ListAliases() ([]Alias, error) {
	rows, err := s.db.Query(`SELECT provider, scope, subject, principal_id
	  FROM principal_aliases ORDER BY provider, scope, subject`)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.Provider, &a.Scope, &a.Subject, &a.PrincipalID); err != nil {
			return nil, fmt.Errorf("scan alias: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) BindAlias(a Alias) error {
	if a.Provider == "" || strings.TrimSpace(a.Scope) == "" || strings.TrimSpace(a.Subject) == "" {
		return errors.New("provider, scope and subject are required")
	}
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := getPrincipal(tx, a.PrincipalID)
	if err != nil {
		return err
	}
	if p.Status != StatusActive {
		return ErrDisabled
	}
	_, err = tx.Exec(`INSERT INTO principal_aliases(provider, scope, subject, principal_id)
	  VALUES(?, ?, ?, ?)
	  ON CONFLICT(provider, scope, subject) DO UPDATE SET principal_id=excluded.principal_id`,
		a.Provider, strings.TrimSpace(a.Scope), strings.TrimSpace(a.Subject), a.PrincipalID)
	if err != nil {
		return fmt.Errorf("bind principal alias: %w", err)
	}
	if err := bumpRevisionTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ResolveAlias(provider Provider, scope, subject string) (Principal, error) {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	return s.resolveAlias(provider, scope, subject)
}

func (s *Store) resolveAlias(provider Provider, scope, subject string) (Principal, error) {
	var id string
	err := s.db.QueryRow(`SELECT principal_id FROM principal_aliases
	  WHERE provider=? AND scope=? AND subject=?`, provider, strings.TrimSpace(scope), strings.TrimSpace(subject)).
		Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrAliasNotFound
	}
	if err != nil {
		return Principal{}, fmt.Errorf("resolve principal alias: %w", err)
	}
	p, err := s.Get(id)
	if err != nil {
		return Principal{}, err
	}
	if p.Status != StatusActive {
		return Principal{}, ErrDisabled
	}
	return p, nil
}

func (s *Store) GrantDelegation(actorID, effectiveID string) (string, error) {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := requireActiveTx(tx, actorID); err != nil {
		return "", err
	}
	if _, err := requireActiveTx(tx, effectiveID); err != nil {
		return "", err
	}
	id := uuid.NewString()
	now := s.now().Unix()
	_, err = tx.Exec(`INSERT INTO principal_delegations
	  (delegation_id, actor_principal_id, effective_principal_id, active, created_at, updated_at)
	  VALUES(?, ?, ?, 1, ?, ?)
	  ON CONFLICT(actor_principal_id, effective_principal_id)
	  DO UPDATE SET active=1, updated_at=excluded.updated_at`,
		id, actorID, effectiveID, now, now)
	if err != nil {
		return "", fmt.Errorf("grant delegation: %w", err)
	}
	var actual string
	if err := tx.QueryRow(`SELECT delegation_id FROM principal_delegations
	  WHERE actor_principal_id=? AND effective_principal_id=?`, actorID, effectiveID).Scan(&actual); err != nil {
		return "", fmt.Errorf("read delegation: %w", err)
	}
	if err := bumpRevisionTx(tx); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return actual, nil
}

func (s *Store) RevokeDelegation(id string) error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE principal_delegations SET active=0, updated_at=?
	  WHERE delegation_id=?`, s.now().Unix(), id)
	if err != nil {
		return fmt.Errorf("revoke delegation: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err := bumpRevisionTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DelegationEffectivePrincipal(id string) (string, error) {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	var effective string
	err := s.db.QueryRow(`SELECT effective_principal_id FROM principal_delegations
	  WHERE delegation_id=?`, strings.TrimSpace(id)).Scan(&effective)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get delegation: %w", err)
	}
	return effective, nil
}

func (s *Store) GrantServiceMember(serviceID, memberID string) error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	service, err := requireActiveTx(tx, serviceID)
	if err != nil {
		return err
	}
	if service.Kind != KindService {
		return errors.New("target principal is not a service")
	}
	if _, err := requireActiveTx(tx, memberID); err != nil {
		return err
	}
	now := s.now().Unix()
	_, err = tx.Exec(`INSERT INTO service_principal_members
	  (service_principal_id, member_principal_id, active, created_at, updated_at)
	  VALUES(?, ?, 1, ?, ?)
	  ON CONFLICT(service_principal_id, member_principal_id)
	  DO UPDATE SET active=1, updated_at=excluded.updated_at`, serviceID, memberID, now, now)
	if err != nil {
		return fmt.Errorf("grant service member: %w", err)
	}
	if err := bumpRevisionTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeServiceMember(serviceID, memberID string) error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE service_principal_members SET active=0, updated_at=?
	  WHERE service_principal_id=? AND member_principal_id=?`,
		s.now().Unix(), strings.TrimSpace(serviceID), strings.TrimSpace(memberID))
	if err != nil {
		return fmt.Errorf("revoke service member: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	if err := bumpRevisionTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) requireActive(id string) (Principal, error) {
	p, err := s.Get(id)
	if err != nil {
		return Principal{}, err
	}
	if p.Status != StatusActive {
		return Principal{}, ErrDisabled
	}
	return p, nil
}

func requireActiveTx(tx *sql.Tx, id string) (Principal, error) {
	p, err := getPrincipal(tx, id)
	if err != nil {
		return Principal{}, err
	}
	if p.Status != StatusActive {
		return Principal{}, ErrDisabled
	}
	return p, nil
}

func (s *Store) policyRevision() string {
	var rev int64
	if err := s.db.QueryRow(`SELECT revision FROM principal_policy_revision WHERE singleton=1`).Scan(&rev); err != nil {
		return "0"
	}
	return fmt.Sprintf("%d", rev)
}

func bumpRevisionTx(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE principal_policy_revision SET revision=revision+1 WHERE singleton=1`)
	return err
}

func hashEnrollmentToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
