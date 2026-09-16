package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiEnsureWorkspaceTrustedWritesTrustJSON(t *testing.T) {
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home)
	cwd := filepath.Join(home, "proj")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}

	a := &PiAdapter{}
	if err := a.EnsureWorkspaceTrusted(cwd, env); err != nil {
		t.Fatalf("EnsureWorkspaceTrusted: %v", err)
	}

	path := filepath.Join(home, ".pi", "agent", "trust.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trust.json: %v", err)
	}
	trust := map[string]bool{}
	if err := json.Unmarshal(data, &trust); err != nil {
		t.Fatalf("parse trust.json: %v", err)
	}
	if !trust[canonicalCwdKey(cwd)] {
		t.Fatalf("trust.json = %v, want %q trusted", trust, cwd)
	}
}

func TestPiEnsureWorkspaceTrustedPreservesExistingEntries(t *testing.T) {
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home)
	path := filepath.Join(home, ".pi", "agent", "trust.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	existing := map[string]bool{"/existing/project": true}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	cwd := filepath.Join(home, "proj")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	a := &PiAdapter{}
	if err := a.EnsureWorkspaceTrusted(cwd, env); err != nil {
		t.Fatalf("EnsureWorkspaceTrusted: %v", err)
	}
	got := readTestPiTrust(t, path)
	if !got["/existing/project"] {
		t.Fatalf("existing trust entry lost: %v", got)
	}
	if !got[canonicalCwdKey(cwd)] {
		t.Fatalf("new trust entry missing: %v", got)
	}
}

func TestPiCleanupWorkspaceTrustRemovesEntry(t *testing.T) {
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home)
	cwd := filepath.Join(home, "proj")
	path := filepath.Join(home, ".pi", "agent", "trust.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	existing := map[string]bool{cwd: true, "/other": true}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	a := &PiAdapter{}
	if err := a.CleanupWorkspaceTrust(cwd, env); err != nil {
		t.Fatalf("CleanupWorkspaceTrust: %v", err)
	}
	got := readTestPiTrust(t, path)
	if got[canonicalCwdKey(cwd)] {
		t.Fatalf("cwd still trusted after cleanup: %v", got)
	}
	if !got["/other"] {
		t.Fatalf("unrelated trust entry lost: %v", got)
	}
}

func TestPiTrustPathHonorsPIEnvOverride(t *testing.T) {
	agentDir := t.TempDir()
	path := piTrustPath([]string{"PI_CODING_AGENT_DIR=" + agentDir, "HOME=/tmp/fake-home"})
	want := filepath.Join(agentDir, "trust.json")
	if path != want {
		t.Fatalf("piTrustPath = %q, want %q", path, want)
	}
	path = piTrustPath([]string{"HOME=/tmp/fake-home"})
	want = filepath.Join("/tmp/fake-home", ".pi", "agent", "trust.json")
	if path != want {
		t.Fatalf("piTrustPath(home) = %q, want %q", path, want)
	}
}

func TestPiAdapterImplementsWorkspaceTruster(t *testing.T) {
	a := &PiAdapter{}
	if _, ok := interface{}(a).(WorkspaceTruster); !ok {
		t.Fatal("PiAdapter does not implement WorkspaceTruster")
	}
	if got := piTrustPath(nil); got != "" && !strings.Contains(got, ".pi") {
		t.Fatalf("piTrustPath() = %q, want .pi path or empty", got)
	}
}

func readTestPiTrust(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	trust := map[string]bool{}
	if err := json.Unmarshal(data, &trust); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return trust
}
