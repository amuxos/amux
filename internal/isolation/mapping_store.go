package isolation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type MappingStore struct {
	db       *sql.DB
	uidStart int
	uidEnd   int
	mu       sync.Mutex
}

func OpenMappingStore(path string, uidStart int) (*MappingStore, error) {
	return OpenMappingStoreRange(path, uidStart, 2147483000)
}

func OpenMappingStoreRange(path string, uidStart, uidEnd int) (*MappingStore, error) {
	if uidStart < 65536 || uidStart > 2147483000 {
		return nil, fmt.Errorf("uid range start %d is outside the safe range", uidStart)
	}
	if uidEnd < uidStart || uidEnd > 2147483000 {
		return nil, fmt.Errorf("uid range end %d must be between %d and 2147483000", uidEnd, uidStart)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", mappingSQLiteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS principal_mappings (
  principal_id TEXT PRIMARY KEY,
  account TEXT NOT NULL UNIQUE,
  uid INTEGER NOT NULL UNIQUE,
  gid INTEGER NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL
);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &MappingStore{db: db, uidStart: uidStart, uidEnd: uidEnd}, nil
}

func (s *MappingStore) Close() error { return s.db.Close() }

func (s *MappingStore) Ensure(principalID string) (AccountMapping, error) {
	id, err := uuid.Parse(strings.TrimSpace(principalID))
	if err != nil {
		return AccountMapping{}, fmt.Errorf("invalid principal id: %w", err)
	}
	principalID = id.String()
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return AccountMapping{}, err
	}
	defer tx.Rollback()
	if mapping, ok, err := getMapping(tx, principalID); err != nil || ok {
		if ok {
			if mapping.UID < s.uidStart || mapping.UID > s.uidEnd ||
				mapping.GID < s.uidStart || mapping.GID > s.uidEnd {
				return AccountMapping{}, fmt.Errorf(
					"existing mapping uid/gid %d/%d is outside configured range %d-%d",
					mapping.UID, mapping.GID, s.uidStart, s.uidEnd,
				)
			}
			now := time.Now().UTC()
			if _, err := tx.Exec(`UPDATE principal_mappings SET last_used_at=? WHERE principal_id=?`, now.UnixNano(), principalID); err != nil {
				return AccountMapping{}, err
			}
			mapping.LastUsedAt = now
			if err := tx.Commit(); err != nil {
				return AccountMapping{}, err
			}
		}
		return mapping, err
	}
	var maxUID sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(uid) FROM principal_mappings`).Scan(&maxUID); err != nil {
		return AccountMapping{}, err
	}
	next := s.uidStart
	if maxUID.Valid && int(maxUID.Int64) >= next {
		next = int(maxUID.Int64) + 1
	}
	if next > s.uidEnd {
		return AccountMapping{}, fmt.Errorf("uid range %d-%d exhausted", s.uidStart, s.uidEnd)
	}
	account, err := availableAccount(tx, principalID)
	if err != nil {
		return AccountMapping{}, err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`INSERT INTO principal_mappings(principal_id,account,uid,gid,created_at,last_used_at)
VALUES(?,?,?,?,?,?)`, principalID, account, next, next, now.UnixNano(), now.UnixNano()); err != nil {
		return AccountMapping{}, err
	}
	if err := tx.Commit(); err != nil {
		return AccountMapping{}, err
	}
	return AccountMapping{PrincipalID: principalID, Account: account, UID: next, GID: next, CreatedAt: now, LastUsedAt: now}, nil
}

func (s *MappingStore) get(principalID string) (AccountMapping, bool, error) {
	return getMapping(s.db, principalID)
}

type mappingQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func getMapping(queryer mappingQueryer, principalID string) (AccountMapping, bool, error) {
	var mapping AccountMapping
	var created, used int64
	err := queryer.QueryRow(`SELECT principal_id,account,uid,gid,created_at,last_used_at
FROM principal_mappings WHERE principal_id=?`, principalID).
		Scan(&mapping.PrincipalID, &mapping.Account, &mapping.UID, &mapping.GID, &created, &used)
	if err == sql.ErrNoRows {
		return AccountMapping{}, false, nil
	}
	if err != nil {
		return AccountMapping{}, false, err
	}
	mapping.CreatedAt = time.Unix(0, created).UTC()
	mapping.LastUsedAt = time.Unix(0, used).UTC()
	return mapping, true, nil
}

func (s *MappingStore) List() ([]AccountMapping, error) {
	rows, err := s.db.Query(`SELECT principal_id,account,uid,gid,created_at,last_used_at
FROM principal_mappings ORDER BY uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var mappings []AccountMapping
	for rows.Next() {
		var mapping AccountMapping
		var created, used int64
		if err := rows.Scan(&mapping.PrincipalID, &mapping.Account, &mapping.UID, &mapping.GID, &created, &used); err != nil {
			return nil, err
		}
		mapping.CreatedAt = time.Unix(0, created).UTC()
		mapping.LastUsedAt = time.Unix(0, used).UTC()
		mappings = append(mappings, mapping)
	}
	return mappings, rows.Err()
}

func (s *MappingStore) Delete(principalID string) error {
	result, err := s.db.Exec(`DELETE FROM principal_mappings WHERE principal_id=?`, strings.TrimSpace(principalID))
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func availableAccount(queryer mappingQueryer, principalID string) (string, error) {
	sum := sha256.Sum256([]byte(principalID))
	encoded := hex.EncodeToString(sum[:])
	for length := 12; length <= 32; length += 4 {
		account := "amux_" + encoded[:length]
		var existing string
		err := queryer.QueryRow(`SELECT principal_id FROM principal_mappings WHERE account=?`, account).Scan(&existing)
		switch err {
		case sql.ErrNoRows:
			return account, nil
		case nil:
			if existing == principalID {
				return account, nil
			}
		default:
			return "", err
		}
	}
	return "", fmt.Errorf("account hash collision for principal %s", strconv.Quote(principalID))
}

func mappingSQLiteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_txlock=immediate"
}
