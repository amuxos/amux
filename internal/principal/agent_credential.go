package principal

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AgentCredentialPrincipalForLaunch returns the principal represented by the
// live agent credential for launchID. It is used to rotate credentials for a
// legacy-host Feishu session without upgrading that session to an identified
// execution identity.
func (s *Store) AgentCredentialPrincipalForLaunch(launchID string) (string, error) {
	launchID = strings.TrimSpace(launchID)
	if launchID == "" {
		return "", ErrNotFound
	}
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	var principalID string
	err := s.db.QueryRow(`SELECT principal_id FROM agent_actor_credentials
	  WHERE launch_id=? AND active=1 AND expires_at>? ORDER BY created_at DESC LIMIT 1`,
		launchID, s.now().Unix()).Scan(&principalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := s.requireActive(principalID); err != nil {
		return "", err
	}
	return principalID, nil
}

// AgentCredentialTTL bounds how long a launch's agent credential stays usable.
// It is deliberately long enough to cover a realistic session lifetime — an
// agent that can no longer identify itself does not silently downgrade, it
// fails — and short enough that a leaked token is not useful forever.
const AgentCredentialTTL = 30 * 24 * time.Hour

// AgentCredential is the actor credential handed to an agent process so its own
// control-plane calls carry the identity of the principal it already runs as.
//
// Without one, a call from inside an isolated agent arrives unauthenticated;
// see isolation.ErrUnidentifiedExecution for why that is refused. With one,
// "ask amux to create another agent for me" produces a sibling under the same
// principal, in the same sandbox identity — which is what the user asking for
// it meant, and the widest thing they are entitled to.
type AgentCredential struct {
	CredentialID string
	Token        string
	PrincipalID  string
	LaunchID     string
	ExpiresAt    time.Time
}

// IssueAgentCredential mints the credential for one launch. Reissuing for the
// same launch id (a relaunch) supersedes the previous token, so a rotated
// session never leaves a usable credential behind.
func (s *Store) IssueAgentCredential(principalID, launchID string, ttl time.Duration) (AgentCredential, error) {
	principalID = strings.TrimSpace(principalID)
	launchID = strings.TrimSpace(launchID)
	if principalID == "" || launchID == "" {
		return AgentCredential{}, errors.New("principal id and launch id are required")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return AgentCredential{}, fmt.Errorf("generate agent credential: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	id := uuid.NewString()
	now := s.now()
	expires := now.Add(ttl)

	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return AgentCredential{}, err
	}
	defer tx.Rollback()
	if _, err := requireActiveTx(tx, principalID); err != nil {
		return AgentCredential{}, err
	}
	// Expired rows are useless the instant they lapse; sweeping on issue keeps
	// the table proportional to live launches without a separate job.
	if _, err := tx.Exec(`DELETE FROM agent_actor_credentials WHERE expires_at <= ?`, now.Unix()); err != nil {
		return AgentCredential{}, err
	}
	if _, err := tx.Exec(`UPDATE agent_actor_credentials SET active=0 WHERE launch_id=? AND active=1`, launchID); err != nil {
		return AgentCredential{}, err
	}
	if _, err := tx.Exec(`INSERT INTO agent_actor_credentials
	  (credential_id, token_hash, principal_id, launch_id, active, created_at, expires_at)
	  VALUES(?, ?, ?, ?, 1, ?, ?)`,
		id, hashEnrollmentToken(token), principalID, launchID, now.Unix(), expires.Unix()); err != nil {
		return AgentCredential{}, fmt.Errorf("create agent credential: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AgentCredential{}, err
	}
	return AgentCredential{
		CredentialID: id,
		Token:        token,
		PrincipalID:  principalID,
		LaunchID:     launchID,
		ExpiresAt:    expires,
	}, nil
}

// RevokeAgentCredentials retires every credential issued for a launch.
func (s *Store) RevokeAgentCredentials(launchID string) error {
	launchID = strings.TrimSpace(launchID)
	if launchID == "" {
		return nil
	}
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	_, err := s.db.Exec(`UPDATE agent_actor_credentials SET active=0 WHERE launch_id=?`, launchID)
	return err
}

// PruneExpiredAgentCredentials drops lapsed rows. Issuing sweeps too; this
// exists so a host that stops launching sessions still settles.
func (s *Store) PruneExpiredAgentCredentials() (int64, error) {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM agent_actor_credentials WHERE expires_at <= ?`, s.now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AuthenticateAgentCredential resolves an agent launch token to its principal.
// The returned actor is marked Delegated: it carries exactly that principal's
// own authority and can never name a run_as target.
func (s *Store) AuthenticateAgentCredential(token, requestID string) (ActorContext, error) {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	var id, principalID, launchID string
	var active int
	var expiresAt int64
	err := s.db.QueryRow(`SELECT credential_id, principal_id, launch_id, active, expires_at
	  FROM agent_actor_credentials WHERE token_hash=?`, hashEnrollmentToken(strings.TrimSpace(token))).
		Scan(&id, &principalID, &launchID, &active, &expiresAt)
	if err != nil || active != 1 {
		return ActorContext{}, errors.New("invalid agent credential")
	}
	if s.now().Unix() >= expiresAt {
		return ActorContext{}, errors.New("expired agent credential")
	}
	p, err := s.requireActive(principalID)
	if err != nil {
		return ActorContext{}, err
	}
	return ActorContext{
		ActorPrincipalID:        p.ID,
		Source:                  SourceCLI,
		Provider:                ProviderSystem,
		Scope:                   "agent",
		Subject:                 launchID,
		AuthenticationContextID: id,
		RequestID:               requestID,
		Delegated:               true,
	}, nil
}

// AuthenticateActor is the control plane's single token entry point. A human's
// enrollment token and an agent's launch credential are both bearer tokens on
// the same header; which table they live in is an implementation detail of the
// store, not of every caller.
func (s *Store) AuthenticateActor(token, requestID string) (ActorContext, error) {
	if strings.TrimSpace(token) == "" {
		return ActorContext{}, errors.New("missing actor credential")
	}
	actor, enrollErr := s.AuthenticateEnrollment(token, requestID)
	if enrollErr == nil {
		return actor, nil
	}
	// A disabled principal must report as disabled rather than as an unknown
	// token, so an operator sees why their own credential stopped working.
	if errors.Is(enrollErr, ErrDisabled) {
		return ActorContext{}, enrollErr
	}
	actor, agentErr := s.AuthenticateAgentCredential(token, requestID)
	if agentErr == nil {
		return actor, nil
	}
	if errors.Is(agentErr, ErrDisabled) {
		return ActorContext{}, agentErr
	}
	return ActorContext{}, errors.New("invalid actor credential")
}
