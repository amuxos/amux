package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/session"
)

// The `amux mcp` server is spawned by the agent process (it is auto-injected
// into the agent's MCP config), so its parent is the agent, not amux. Its only
// built-in teardown is stdin EOF when the MCP host closes the pipe. When the
// agent instead dies abruptly (SIGKILL, broker teardown across runtime
// reconnects) EOF never arrives and the server — blocked on a stdin read — runs
// forever, reparented to init. Archived sessions therefore leak orphaned
// `amux mcp` servers.
//
// The watchdog gives the server an independent way to exit, binding its lifetime
// to the two owners it should never outlive:
//   - its parent agent process (≈ the session): a changed ppid means the agent
//     died (a process is only reparented when its parent exits);
//   - its owning amux session in the local runtime: archived/sleeping/absent
//     means the agent is gone.
type mcpWatchdogConfig struct {
	sessionID       string
	initialPPID     int
	ppidInterval    time.Duration
	sessionInterval time.Duration
	graceProbes     int // consecutive "owner lost" runtime probes required before exit
}

// mcpOrphaned reports whether the server's parent agent process has died. A
// process's parent only changes when the original parent exits (the child is
// reparented to init or a subreaper), so a changed ppid is a reliable
// "agent gone" signal that does not depend on the runtime being reachable.
func mcpOrphaned(initialPPID, currentPPID int) bool {
	if initialPPID <= 1 {
		return false // no real parent to lose (or already orphaned at start)
	}
	return currentPPID != initialPPID
}

// mcpOwnerSessionLost reports whether the owning session is definitively gone.
// A reachable runtime that lists the session as archived/sleeping, or no longer
// lists it at all, means the agent has stopped and this MCP server should exit.
// A runtime dial/list error is transient — the runtime restarts frequently and
// reconnects live sessions — so it must NOT trigger exit; orphan detection
// backstops a permanently dead runtime. Unknown states are treated
// conservatively as alive to avoid racing a session that is still starting up.
func mcpOwnerSessionLost(sessionID string, sessions []*sessionEntry, listErr error) bool {
	if strings.TrimSpace(sessionID) == "" {
		return false
	}
	if listErr != nil {
		return false
	}
	for _, s := range sessions {
		if s.Id != sessionID {
			continue
		}
		switch session.State(s.State) {
		case session.StateArchived, session.StateSleeping:
			return true // agent process stopped
		default:
			return false // running/created/idle/unknown → keep serving
		}
	}
	return true // runtime reachable but no longer knows this session
}

// startMCPWatchdog launches the self-termination watchdog in a goroutine. It is
// only meaningful for session-launched servers (sessionID set). onLost is
// invoked at most once with a human-readable reason; it is expected to tear the
// process down (the Serve loop may be blocked on a stdin read and cannot be
// unblocked by context cancellation alone).
func startMCPWatchdog(ctx context.Context, cfg mcpWatchdogConfig, onLost func(reason string)) {
	if cfg.ppidInterval <= 0 {
		cfg.ppidInterval = 5 * time.Second
	}
	if cfg.sessionInterval <= 0 {
		cfg.sessionInterval = 30 * time.Second
	}
	if cfg.graceProbes <= 0 {
		cfg.graceProbes = 2
	}
	go func() {
		ppidTicker := time.NewTicker(cfg.ppidInterval)
		defer ppidTicker.Stop()
		sessionTicker := time.NewTicker(cfg.sessionInterval)
		defer sessionTicker.Stop()
		lostStreak := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ppidTicker.C:
				if mcpOrphaned(cfg.initialPPID, os.Getppid()) {
					onLost("parent agent process exited")
					return
				}
			case <-sessionTicker.C:
				if cfg.sessionID == "" {
					continue
				}
				sessions, err := listLocalSessions(ctx)
				if mcpOwnerSessionLost(cfg.sessionID, sessions, err) {
					lostStreak++
					if lostStreak >= cfg.graceProbes {
						onLost(fmt.Sprintf("owning session %s is no longer running", cfg.sessionID))
						return
					}
				} else {
					lostStreak = 0
				}
			}
		}
	}()
}

// listLocalSessions queries the local runtime for its current sessions. The
// owning session always lives in the local amuxlet (the agent runs on this
// host), so the local runtime is the source of truth even when a hub is the
// task backend.
func listLocalSessions(ctx context.Context) ([]*sessionEntry, error) {
	lc, err := newLocalRuntimeClientWithTimeout(3 * time.Second)
	if err != nil {
		return nil, err
	}
	defer lc.Close()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return lc.List(cctx)
}
