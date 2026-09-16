package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/mux"
	"github.com/amuxos/amux/internal/session"
)

func bp(pid int, sid, sock string, start uint64) mux.BrokerProcess {
	return mux.BrokerProcess{PID: pid, SessionID: sid, Socket: sock, StartTime: start}
}

func killSet(kill []staleBroker) map[int]string {
	out := map[int]string{}
	for _, sb := range kill {
		out[sb.proc.PID] = sb.reason
	}
	return out
}

func TestDecideBrokerReap(t *testing.T) {
	sessDir := "/home/u/.amux/sessions"
	sock := func(sid, kind string) string { return filepath.Join(sessDir, sid+"."+kind+".sock") }

	states := map[string]session.State{
		"run-sid":      session.StateRunning,
		"idle-sid":     session.StateIdle,
		"created-sid":  session.StateCreated,
		"archived-sid": session.StateArchived,
		"sleeping-sid": session.StateSleeping,
	}

	procs := []mux.BrokerProcess{
		// session gone from DB -> reap
		bp(1001, "gone-sid", sock("gone-sid", "structured"), 100),
		// archived session -> reap + remove files
		bp(1002, "archived-sid", sock("archived-sid", "structured"), 100),
		// sleeping session -> reap + remove files
		bp(1003, "sleeping-sid", sock("sleeping-sid", "acp"), 100),
		// running session with two brokers -> keep newest (1005), reap older (1004)
		bp(1004, "run-sid", sock("run-sid", "structured"), 100),
		bp(1005, "run-sid", sock("run-sid", "structured"), 200),
		// idle session, single broker -> keep
		bp(1006, "idle-sid", sock("idle-sid", "structured"), 100),
		// created session -> mid-launch, never reap
		bp(1007, "created-sid", sock("created-sid", "structured"), 100),
		// socket outside this installation's sessions dir -> out of scope
		bp(1008, "gone-sid", "/other/home/.amux/sessions/gone-sid.structured.sock", 100),
	}

	kill, removeFiles := decideBrokerReap(procs, states, sessDir)
	got := killSet(kill)

	wantKill := map[int]string{
		1001: "session-gone",
		1002: "session-not-running",
		1003: "session-not-running",
		1004: "duplicate",
	}
	if len(got) != len(wantKill) {
		t.Fatalf("kill set = %v, want %v", got, wantKill)
	}
	for pid, reason := range wantKill {
		if got[pid] != reason {
			t.Errorf("pid %d reason = %q, want %q", pid, got[pid], reason)
		}
	}
	// The live current brokers must never be reaped.
	for _, pid := range []int{1005, 1006, 1007, 1008} {
		if _, ok := got[pid]; ok {
			t.Errorf("pid %d must not be reaped", pid)
		}
	}

	// Files are removed only for in-DB-but-not-running sessions here; gone
	// sessions are handled by reapOrphanSessionFiles.
	wantFiles := map[string]bool{"archived-sid": true, "sleeping-sid": true}
	if len(removeFiles) != len(wantFiles) {
		t.Fatalf("removeFiles = %v, want %v", removeFiles, wantFiles)
	}
	for _, sid := range removeFiles {
		if !wantFiles[sid] {
			t.Errorf("unexpected file removal for %q", sid)
		}
	}
}

func TestNewestBroker(t *testing.T) {
	// Highest start time wins.
	g := []mux.BrokerProcess{bp(1, "s", "x", 100), bp(2, "s", "x", 300), bp(3, "s", "x", 200)}
	if got := newestBroker(g); got.PID != 2 {
		t.Errorf("newestBroker by starttime = pid %d, want 2", got.PID)
	}
	// Equal start times (e.g. failed read) fall back to highest pid.
	g2 := []mux.BrokerProcess{bp(5, "s", "x", 0), bp(9, "s", "x", 0), bp(7, "s", "x", 0)}
	if got := newestBroker(g2); got.PID != 9 {
		t.Errorf("newestBroker tie-break by pid = pid %d, want 9", got.PID)
	}
}

func TestSessionIDFromArtifact(t *testing.T) {
	cases := map[string]string{
		"uuid-1.structured.sock":   "uuid-1",
		"uuid-1.acp.sock":          "uuid-1",
		"uuid-1.structured.log":    "uuid-1",
		"uuid-1.acp.log":           "uuid-1",
		"uuid-1.pid":               "uuid-1",
		"uuid-1.resume-session-id": "uuid-1",
		"uuid-1.log":               "uuid-1",
	}
	for name, want := range cases {
		if got, ok := sessionIDFromArtifact(name); !ok || got != want {
			t.Errorf("sessionIDFromArtifact(%q) = %q,%v want %q,true", name, got, ok, want)
		}
	}
	for _, bad := range []string{"sessions", "uuid-1.txt", "uuid-1"} {
		if _, ok := sessionIDFromArtifact(bad); ok {
			t.Errorf("sessionIDFromArtifact(%q) unexpectedly ok", bad)
		}
	}
}

func TestReapOrphanSessionFiles(t *testing.T) {
	amuxDir := t.TempDir()
	sessDir := filepath.Join(amuxDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := &server{amuxDir: amuxDir}

	orphan := "orphan-sid" // not in DB
	known := "known-sid"   // in DB (archived)
	states := map[string]session.State{"known-sid": session.StateArchived}

	// Lay down a full artifact set for both sids, plus a profile dir each.
	for _, sid := range []string{orphan, known} {
		for _, suf := range brokerArtifactSuffixes {
			if err := os.WriteFile(filepath.Join(sessDir, sid+suf), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(profileSessionDir(amuxDir, sid), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	reaped := srv.reapOrphanSessionFiles(sessDir, states)
	if reaped != 1 {
		t.Fatalf("reaped = %d, want 1", reaped)
	}
	// Orphan: all flat files and the profile dir gone.
	for _, suf := range brokerArtifactSuffixes {
		if _, err := os.Stat(filepath.Join(sessDir, orphan+suf)); !os.IsNotExist(err) {
			t.Errorf("orphan artifact %q still present", orphan+suf)
		}
	}
	if _, err := os.Stat(profileSessionDir(amuxDir, orphan)); !os.IsNotExist(err) {
		t.Errorf("orphan profile dir still present")
	}
	// Known session: everything preserved.
	for _, suf := range brokerArtifactSuffixes {
		if _, err := os.Stat(filepath.Join(sessDir, known+suf)); err != nil {
			t.Errorf("known artifact %q wrongly removed", known+suf)
		}
	}
	if _, err := os.Stat(profileSessionDir(amuxDir, known)); err != nil {
		t.Errorf("known profile dir wrongly removed")
	}
}
