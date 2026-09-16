package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	agentActorTokenEnv     = "AMUX_ACTOR_TOKEN"
	agentActorTokenFileEnv = "AMUX_ACTOR_TOKEN_FILE"
)

// applyAgentActorToken puts the launch's actor credential in front of the agent
// process and nowhere else.
//
// runEnv reaches exec; recordEnv is persisted with the session and replayed on
// restart, so neither the token nor its file path may appear there — the hub
// mints a fresh one per launch anyway. Any inherited value is dropped first:
// the agent speaks for its own principal, never for whoever happened to start
// the daemon.
//
// tokenFile is the path amuxlet persists the credential to (see
// writeSessionActorToken). It travels next to the token value because the value
// alone goes stale: the hub mints a replacement credential on every restart and
// revokes the old one, and when a reconfigure fails after that mint the running
// process never relaunches. In-session binaries read the file fresh on every
// control-plane call (AMUX_ACTOR_TOKEN_FILE wins over AMUX_ACTOR_TOKEN), so the
// adopted credential keeps working even though the process itself was never
// restarted. AMUX_ACTOR_TOKEN stays for in-session binaries older than that.
func applyAgentActorToken(runEnv, recordEnv []string, token, tokenFile string) ([]string, []string) {
	runEnv = withoutEnv(runEnv, agentActorTokenEnv)
	runEnv = withoutEnv(runEnv, agentActorTokenFileEnv)
	recordEnv = withoutEnv(recordEnv, agentActorTokenEnv)
	recordEnv = withoutEnv(recordEnv, agentActorTokenFileEnv)
	if strings.TrimSpace(token) != "" {
		runEnv = append(runEnv, agentActorTokenEnv+"="+token)
	}
	if strings.TrimSpace(tokenFile) != "" {
		runEnv = append(runEnv, agentActorTokenFileEnv+"="+tokenFile)
	}
	return runEnv, recordEnv
}

func withoutEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// The hub mints the credential once, when the session is created, and hands it
// over in the launch request. But the process holding it dies on every restart,
// resume and crash recovery — and those relaunches are driven locally by
// amuxlet, which has no principal store to mint a replacement from. So the
// token is persisted next to the session at creation and replayed on relaunch.
//
// It lives in its own 0600 file rather than in the session's recorded env: that
// env is replayed on restart and rendered by `session show`, which is no place
// for a bearer credential. The file is removed with the session's other
// artifacts, and the credential expires on its own regardless.
func sessionActorTokenPath(amuxDir, sessionID string) string {
	return filepath.Join(amuxDir, "sessions", sessionID+".actor-token")
}

// storedActorToken is the on-disk form. The expiry travels with the token so a
// relaunch can say something useful about a credential the hub minted weeks ago
// and no longer remembers handing over.
type storedActorToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

func writeSessionActorToken(amuxDir, sessionID, token string, expiresAt int64) error {
	path := sessionActorTokenPath(amuxDir, sessionID)
	if strings.TrimSpace(token) == "" {
		// An unidentified launch mints nothing; leaving a previous file in place
		// would hand the next agent a credential this launch is not entitled to.
		removeSessionActorToken(amuxDir, sessionID)
		return nil
	}
	data, err := json.Marshal(storedActorToken{Token: strings.TrimSpace(token), ExpiresAt: expiresAt})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// readSessionActorToken returns the stored token and its expiry (0 when
// unknown). Files written before the expiry was recorded hold a bare token;
// they keep working rather than silently reading as "no credential".
func readSessionActorToken(amuxDir, sessionID string) (string, int64) {
	data, err := os.ReadFile(sessionActorTokenPath(amuxDir, sessionID))
	if err != nil {
		return "", 0
	}
	var stored storedActorToken
	if err := json.Unmarshal(data, &stored); err == nil && strings.TrimSpace(stored.Token) != "" {
		return strings.TrimSpace(stored.Token), stored.ExpiresAt
	}
	return strings.TrimSpace(string(data)), 0
}

func removeSessionActorToken(amuxDir, sessionID string) {
	_ = os.Remove(sessionActorTokenPath(amuxDir, sessionID))
}

// agentActorTokenExpiryWarning describes a credential that has lapsed or is
// about to, or "" when there is nothing to say.
//
// Nothing re-mints the credential locally: a relaunch replays whatever was
// stored at creation. Once it lapses, the agent keeps running and answering
// normally but its own `amux` calls start returning Unauthenticated — a failure
// nobody attributes to a credential unless something names it. Recovery is a
// restart through the hub, which mints a fresh one.
func agentActorTokenExpiryWarning(expiresAt int64, now time.Time) string {
	if expiresAt <= 0 {
		return ""
	}
	at := time.Unix(expiresAt, 0)
	if now.IsZero() {
		now = time.Now()
	}
	switch {
	case now.After(at):
		return fmt.Sprintf(
			"agent actor credential expired at %s; this agent can no longer create sessions or subagents of its own "+
				"(restart the session to mint a new one)",
			at.UTC().Format(time.RFC3339))
	case at.Sub(now) < credentialExpiryWarnWindow:
		return fmt.Sprintf(
			"agent actor credential expires in %s (%s); restart the session before then to mint a new one",
			at.Sub(now).Round(time.Hour), at.UTC().Format(time.RFC3339))
	}
	return ""
}

// exitCleanup is what the drain goroutine should do once an agent process ends.
type exitCleanup struct {
	// Archive is false when a relaunch has already taken the session over: the
	// session has a live process again and archiving it (or failing its turn)
	// would be acting on someone else's launch.
	Archive bool
	// RemoveLaunch is always the launch that actually ended. The run directory
	// belongs to that launch; a restart in flight owns a different one.
	RemoveLaunch string
}

// processExitCleanup decides from the launch the process was started for, not
// from whatever the session has moved on to. Restart kills the old process,
// rotates the launch id and prepares a fresh run directory before this cleanup
// wakes, so a decision made after the fact deletes the incoming launch's
// directory — intermittently, depending on which side wins.
func processExitCleanup(startedLaunchID, currentLaunchID string) exitCleanup {
	return exitCleanup{
		Archive:      currentLaunchID == startedLaunchID,
		RemoveLaunch: startedLaunchID,
	}
}
