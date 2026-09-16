package agentinstance

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "agent_instances.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDefaultPathUsesAMUXHome(t *testing.T) {
	amuxHome := filepath.Join(t.TempDir(), "amux-home")
	t.Setenv("AMUX_HOME", amuxHome)

	if got, want := DefaultPath(), filepath.Join(amuxHome, "agent_instances.db"); got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

func sampleInstance(id string) Instance {
	return Instance{
		ID:                id,
		Name:              "researcher",
		AgentType:         "claude",
		AgentProfile:      "team/researcher@abc123",
		ModelRef:          "example-model",
		OwnerSessionID:    "parent-1",
		TargetID:          "local-1",
		TargetName:        "local",
		CredentialProfile: "default",
		CredentialProfiles: []string{
			"github",
			"slack",
		},
		IsolationMode:     "none",
		HomeRef:           "/home/agent/inst-a",
		WorkspaceRef:      "/ws/agent/inst-a",
		ProviderSessionID: "prov-sess-a",
		Status:            StatusActive,
	}
}

func TestStoreCRUD(t *testing.T) {
	s := newTestStore(t)
	in := sampleInstance("inst-a")

	put, err := s.Put(in)
	if err != nil {
		t.Fatal(err)
	}
	if put.ID != "inst-a" || put.Status != StatusActive || put.CreatedAt == 0 || put.UpdatedAt == 0 || put.LastUsedAt == 0 {
		t.Fatalf("Put returned incomplete record: %+v", put)
	}

	got, err := s.Get("inst-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "researcher" || got.AgentType != "claude" || got.OwnerSessionID != "parent-1" ||
		got.AgentProfile != "team/researcher@abc123" || got.ModelRef != "example-model" ||
		got.TargetID != "local-1" || got.ProviderSessionID != "prov-sess-a" || got.Path != "" {
		t.Fatalf("Get mismatch: %+v", got)
	}
	if !equalStrings(got.CredentialProfiles, []string{"github", "slack"}) {
		t.Fatalf("credential_profiles = %v, want [github slack]", got.CredentialProfiles)
	}

	// Replace preserves CreatedAt, refreshes UpdatedAt, updates mutable fields.
	updated := in
	updated.Name = "researcher-v2"
	updated.ProviderSessionID = "prov-sess-b"
	updated.Status = StatusIdle
	rep, err := s.Put(updated)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Name != "researcher-v2" || rep.ProviderSessionID != "prov-sess-b" || rep.Status != StatusIdle {
		t.Fatalf("replace mismatch: %+v", rep)
	}
	if rep.CreatedAt != put.CreatedAt {
		t.Fatalf("CreatedAt changed on replace: %d != %d", rep.CreatedAt, put.CreatedAt)
	}
	if rep.UpdatedAt < put.UpdatedAt {
		t.Fatalf("UpdatedAt not refreshed: %d < %d", rep.UpdatedAt, put.UpdatedAt)
	}

	// UpdateStatus only changes status.
	ok, err := s.UpdateStatus("inst-a", StatusArchived)
	if err != nil || !ok {
		t.Fatalf("UpdateStatus: %v %v", ok, err)
	}
	got, _ = s.Get("inst-a")
	if got.Status != StatusArchived {
		t.Fatalf("UpdateStatus did not apply: %+v", got)
	}

	// Invalid status rejected.
	if _, err := s.UpdateStatus("inst-a", "bogus"); err == nil {
		t.Fatal("expected error for invalid status")
	}

	// TouchProviderSession.
	ok, err = s.TouchProviderSession("inst-a", "prov-sess-c")
	if err != nil || !ok {
		t.Fatalf("TouchProviderSession: %v %v", ok, err)
	}
	got, _ = s.Get("inst-a")
	if got.ProviderSessionID != "prov-sess-c" {
		t.Fatalf("TouchProviderSession did not apply: %+v", got)
	}

	// Delete.
	ok, err = s.Delete("inst-a")
	if err != nil || !ok {
		t.Fatalf("Delete: %v %v", ok, err)
	}
	if _, err := s.Get("inst-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Get after delete: %v", err)
	}
}

func TestStoreGetByOwnerAndList(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"a", "b", "c"} {
		in := sampleInstance(id)
		in.Name = id
		if id == "c" {
			in.OwnerSessionID = "parent-2"
			in.Status = StatusIdle
		}
		if _, err := s.Put(in); err != nil {
			t.Fatal(err)
		}
	}

	owned, err := s.GetByOwner("parent-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 2 {
		t.Fatalf("GetByOwner parent-1 = %d, want 2", len(owned))
	}
	for _, in := range owned {
		if in.OwnerSessionID != "parent-1" {
			t.Fatalf("GetByOwner leaked owner: %+v", in)
		}
	}

	// List filtered by target + status.
	list, err := s.List(Filter{TargetID: "local-1", Status: StatusIdle})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "c" {
		t.Fatalf("List(target+idle) = %+v, want only c", list)
	}

	// Empty filter returns all, newest first.
	all, err := s.List(Filter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("List() = %d (err %v), want 3", len(all), err)
	}
}

func TestStoreClaimIdentityFillsLegacyFieldsAndRejectsChanges(t *testing.T) {
	s := newTestStore(t)
	in := sampleInstance("legacy-identity")
	in.AgentProfile = ""
	in.ModelRef = ""
	if _, err := s.Put(in); err != nil {
		t.Fatal(err)
	}

	claim, err := s.ClaimIdentity("legacy-identity", InstanceIdentity{
		AgentType: "claude", AgentProfile: "team/researcher@rev-1", ModelRef: "gpt-5.5",
	})
	if err != nil {
		t.Fatalf("ClaimIdentity: %v", err)
	}
	if !claim.Changed {
		t.Fatal("ClaimIdentity Changed = false, want legacy fields filled")
	}
	got, err := s.Get("legacy-identity")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentProfile != "team/researcher@rev-1" || got.ModelRef != "gpt-5.5" {
		t.Fatalf("claimed instance = %+v", got)
	}

	_, err = s.ClaimIdentity("legacy-identity", InstanceIdentity{
		AgentType: "claude", AgentProfile: "team/other@rev-2", ModelRef: "gpt-5.5",
	})
	var conflict *IdentityConflictError
	if !errors.As(err, &conflict) || conflict.Field != "profile" {
		t.Fatalf("ClaimIdentity conflict = %v, want profile conflict", err)
	}
}

func TestStoreRollbackIdentityClaimPreservesLifecycleUpdates(t *testing.T) {
	s := newTestStore(t)
	in := sampleInstance("legacy-rollback")
	in.AgentProfile = ""
	in.ModelRef = ""
	if _, err := s.Put(in); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimIdentity("legacy-rollback", InstanceIdentity{
		AgentType: "claude", AgentProfile: "team/researcher@rev-1", ModelRef: "gpt-5.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateStatus("legacy-rollback", StatusArchived); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackIdentityClaim(claim); err != nil {
		t.Fatalf("RollbackIdentityClaim: %v", err)
	}
	got, err := s.Get("legacy-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentProfile != "" || got.ModelRef != "" || got.Status != StatusArchived {
		t.Fatalf("rolled back instance = %+v, want empty legacy identity and archived lifecycle", got)
	}
}

func TestStoreRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent_instances.db")
	s1, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	in := sampleInstance("persist-a")
	if _, err := s1.Put(in); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.UpdateStatus("persist-a", StatusIdle); err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	// Reopen: migration is idempotent and data survives.
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Get("persist-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusIdle || got.ProviderSessionID != "prov-sess-a" {
		t.Fatalf("restart recovery mismatch: %+v", got)
	}
}

func TestStorePutRejectsInvalidStatus(t *testing.T) {
	s := newTestStore(t)
	in := sampleInstance("bad")
	in.Status = "nope"
	if _, err := s.Put(in); err == nil {
		t.Fatal("expected error for invalid status on Put")
	}
}

func TestStoreTouchLastUsed(t *testing.T) {
	s := newTestStore(t)
	in := sampleInstance("touch-a")
	if _, err := s.Put(in); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get("touch-a")
	ok, err := s.TouchLastUsed("touch-a")
	if err != nil || !ok {
		t.Fatalf("TouchLastUsed: %v %v", ok, err)
	}
	after, _ := s.Get("touch-a")
	if after.LastUsedAt < before.LastUsedAt {
		t.Fatalf("LastUsedAt not refreshed: %d < %d", after.LastUsedAt, before.LastUsedAt)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
