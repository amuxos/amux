package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/mux"
	"github.com/amuxos/amux/internal/session"
)

// brokerArtifactSuffixes are the flat per-session files a broker produces in
// the sessions dir. The plain ".log" entry must come last: it is a suffix of
// the more specific ".acp.log"/".structured.log".
var brokerArtifactSuffixes = []string{
	".structured.sock",
	".acp.sock",
	".structured.log",
	".acp.log",
	".resume-session-id",
	".responses-chat-credential",
	".pid",
	".log",
}

// staleBroker is a broker process selected for reaping, with the reason it was
// classified stale (for logging).
type staleBroker struct {
	proc   mux.BrokerProcess
	reason string
}

// reapOrphanBrokers reconciles live broker processes against the session
// database and reaps any broker that no longer backs a live session, then
// removes broker artifact files for sessions that are gone entirely.
//
// Why this is needed: brokers are spawned with Setsid and never Wait()ed, so
// they outlive the amuxlet that started them. The graceful shutdown path kills
// them only on SIGINT/SIGTERM and only via the in-memory proc handle, so a
// SIGKILL, a crash, an `amux update` binary overwrite, or a prune-while-alive
// leaks the broker (reparented to init). Recovery is otherwise DB-row-driven
// and never looks at the process table, and sess.PID tracks the inner agent —
// not the broker — so a hung broker is invisible to it. This sweep treats the
// process table as ground truth and reaps whatever no longer serves a live
// session, which catches every one of those leak vectors regardless of cause.
//
// It is conservative by construction: a broker is only ever signalled when its
// session is provably gone or not running, or when it is a stale duplicate of a
// live session's current (newest) broker, and every signal is preceded by a
// re-read of the process to defeat PID reuse. A live session's current broker
// is never touched.
func (s *server) reapOrphanBrokers() (int, error) {
	procs, err := mux.ListBrokerProcesses()
	if err != nil {
		return 0, fmt.Errorf("list broker processes: %w", err)
	}
	states := s.sessionStates()
	sessDir := filepath.Join(s.amuxDir, "sessions")

	kill, removeFileSIDs := decideBrokerReap(procs, states, sessDir)

	reaped := 0
	// Hold relaunchMu so no session can transition created->running (spawning
	// its real broker) between the scan and the kill. The kill is an immediate
	// SIGKILL with no graceful wait, so the critical section stays short.
	s.relaunchMu.Lock()
	for _, sb := range kill {
		if s.killStaleBroker(sb) {
			reaped++
		}
	}
	for _, sid := range removeFileSIDs {
		s.cleanupSessionBrokerFiles(sid)
	}
	s.relaunchMu.Unlock()

	// File backlog: remove broker artifacts for sids the DB no longer knows.
	// This needs no lock — a session absent from the DB is never mid-launch.
	reaped += s.reapOrphanSessionFiles(sessDir, states)
	return reaped, nil
}

// decideBrokerReap classifies live broker processes into a kill set and the set
// of session ids whose flat broker files should be removed. It is pure (no
// I/O) so the policy is fully unit-testable; the caller performs the kills and
// removals. Only brokers whose socket lives under sessDir are considered, so a
// co-located amux under a different home is never touched.
func decideBrokerReap(procs []mux.BrokerProcess, states map[string]session.State, sessDir string) (kill []staleBroker, removeFileSIDs []string) {
	prefix := sessDir + string(os.PathSeparator)
	bySID := make(map[string][]mux.BrokerProcess)
	for _, bp := range procs {
		if strings.HasPrefix(bp.Socket, prefix) {
			bySID[bp.SessionID] = append(bySID[bp.SessionID], bp)
		}
	}
	for sid, group := range bySID {
		state, inDB := states[sid]
		switch {
		case inDB && state == session.StateCreated:
			// Mid-launch: the session's real broker may have just been spawned.
			// Never reap it; the launch path owns the socket from here.
		case inDB && (state == session.StateRunning || state == session.StateIdle):
			// Live session: keep its current (newest) broker, reap only older
			// duplicates left behind by a respawn. Never remove the session's
			// files — the current broker is using them.
			keep := newestBroker(group)
			for _, bp := range group {
				if bp.PID != keep.PID {
					kill = append(kill, staleBroker{bp, "duplicate"})
				}
			}
		case inDB:
			// sleeping / archived: no live broker should exist. Reap them all and
			// drop the flat broker files (the session row stays, so the profile
			// dir is left for a possible resume).
			for _, bp := range group {
				kill = append(kill, staleBroker{bp, "session-not-running"})
			}
			removeFileSIDs = append(removeFileSIDs, sid)
		default:
			// Not in DB at all: a fully orphaned broker. Reap it; its files are
			// removed by reapOrphanSessionFiles (which also clears the profile
			// dir for sessions the DB no longer knows).
			for _, bp := range group {
				kill = append(kill, staleBroker{bp, "session-gone"})
			}
		}
	}
	return kill, removeFileSIDs
}

// killStaleBroker signals a broker's and its agent's process groups after
// confirming the broker process is still the one we scanned (PID-reuse guard).
// It reports whether a signal was sent.
func (s *server) killStaleBroker(sb staleBroker) bool {
	if !sb.proc.StillBroker() {
		return false
	}
	sb.proc.KillGroup()
	fmt.Fprintf(os.Stderr, "runtime: broker reap: killed orphan broker pid=%d session=%s agent=%d reason=%s deleted_exe=%v\n",
		sb.proc.PID, sb.proc.SessionID, sb.proc.AgentPID, sb.reason, sb.proc.DeletedExe)
	return true
}

// cleanupSessionBrokerFiles removes the flat per-session broker artifacts
// (pid/sockets/logs/resume scratch) for a session id. It never touches the
// session's profile dir or workspace.
func (s *server) cleanupSessionBrokerFiles(sid string) {
	dir := filepath.Join(s.amuxDir, "sessions")
	for _, suf := range brokerArtifactSuffixes {
		_ = os.Remove(filepath.Join(dir, sid+suf))
	}
}

// reapOrphanSessionFiles removes broker artifact files (and the now-orphaned
// profile dir) for session ids that have no row in the session DB. It returns
// the number of orphaned session ids cleaned.
func (s *server) reapOrphanSessionFiles(sessDir string, states map[string]session.State) int {
	entries, err := os.ReadDir(sessDir)
	if err != nil {
		return 0
	}
	seen := make(map[string]bool)
	reaped := 0
	for _, e := range entries {
		sid, ok := sessionIDFromArtifact(e.Name())
		if !ok || seen[sid] {
			continue
		}
		seen[sid] = true
		if _, inDB := states[sid]; inDB {
			continue // session still known to the runtime; leave its files
		}
		s.cleanupSessionBrokerFiles(sid)
		_ = os.RemoveAll(profileSessionDir(s.amuxDir, sid))
		reaped++
	}
	return reaped
}

// sessionStates maps every known session id to its current state.
func (s *server) sessionStates() map[string]session.State {
	states := make(map[string]session.State)
	for _, sess := range s.mgr.List() {
		snap := sess.Snapshot()
		states[snap.ID] = snap.State
	}
	return states
}

// sessionIDFromArtifact recovers the session id from a broker artifact
// filename like "<sid>.structured.sock". The ".log" suffix is checked last
// because the more specific ".structured.log"/".acp.log" share its suffix.
func sessionIDFromArtifact(name string) (string, bool) {
	for _, suf := range brokerArtifactSuffixes {
		if strings.HasSuffix(name, suf) {
			if sid := strings.TrimSuffix(name, suf); sid != "" {
				return sid, true
			}
		}
	}
	return "", false
}

// newestBroker returns the most recently started broker in a group, breaking
// ties (and a failed start-time read) by highest pid. The newest broker is the
// one that most recently (re)bound the session socket, i.e. the current one.
func newestBroker(group []mux.BrokerProcess) mux.BrokerProcess {
	best := group[0]
	for _, bp := range group[1:] {
		if bp.StartTime > best.StartTime || (bp.StartTime == best.StartTime && bp.PID > best.PID) {
			best = bp
		}
	}
	return best
}
