package cli

import (
	"errors"
	"testing"

	"github.com/amuxos/amux/internal/session"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestMCPOrphaned(t *testing.T) {
	cases := []struct {
		name        string
		initialPPID int
		currentPPID int
		want        bool
	}{
		{"no real parent at start", 1, 1, false},
		{"zero initial ppid", 0, 1, false},
		{"unchanged ppid is alive", 1234, 1234, false},
		{"reparented to init is orphaned", 1234, 1, true},
		{"reparented to subreaper is orphaned", 1234, 9999, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpOrphaned(tc.initialPPID, tc.currentPPID); got != tc.want {
				t.Fatalf("mcpOrphaned(%d,%d)=%v want %v", tc.initialPPID, tc.currentPPID, got, tc.want)
			}
		})
	}
}

func sessionList(id, state string) []*sessionEntry {
	return []*sessionEntry{{Session: pb.Session{Id: id, State: state}}}
}

func TestMCPOwnerSessionLost(t *testing.T) {
	const sid = "sess-1"
	cases := []struct {
		name     string
		sid      string
		sessions []*sessionEntry
		listErr  error
		want     bool
	}{
		{"empty session id never lost", "", sessionList(sid, string(session.StateRunning)), nil, false},
		{"runtime dial error is transient", sid, nil, errors.New("connection refused"), false},
		{"running owner is alive", sid, sessionList(sid, string(session.StateRunning)), nil, false},
		{"created owner is alive", sid, sessionList(sid, string(session.StateCreated)), nil, false},
		{"idle owner is alive", sid, sessionList(sid, string(session.StateIdle)), nil, false},
		{"archived owner is lost", sid, sessionList(sid, string(session.StateArchived)), nil, true},
		{"sleeping owner is lost", sid, sessionList(sid, string(session.StateSleeping)), nil, true},
		{"absent owner on reachable runtime is lost", sid, sessionList("other", string(session.StateRunning)), nil, true},
		{"empty runtime is lost", sid, nil, nil, true},
		{"unknown state is conservatively alive", sid, sessionList(sid, "bootstrapping"), nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpOwnerSessionLost(tc.sid, tc.sessions, tc.listErr); got != tc.want {
				t.Fatalf("mcpOwnerSessionLost(%q,...,%v)=%v want %v", tc.sid, tc.listErr, got, tc.want)
			}
		})
	}
}
