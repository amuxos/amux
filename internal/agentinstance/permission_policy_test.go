package agentinstance

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestInstancePermissionPolicySurvivesRestartAndCannotBeOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	in := sampleInstance("policy")
	in.PermissionPolicy = "deny-all"
	if _, err := s.Put(in); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Get(in.ID)
	if err != nil || got.PermissionPolicy != "deny-all" {
		t.Fatalf("saved policy = %+v, %v", got, err)
	}
	_, err = s.ClaimIdentity(in.ID, InstanceIdentity{PermissionPolicy: "policy-allow"})
	var conflict *IdentityConflictError
	if !errors.As(err, &conflict) || conflict.Field != "permission policy" {
		t.Fatalf("policy change = %v", err)
	}
	in.PermissionPolicy = ""
	in.Status = StatusIdle
	updated, err := s.Put(in)
	if err != nil || updated.PermissionPolicy != "deny-all" || updated.Status != StatusIdle {
		t.Fatalf("lifecycle update lost frozen policy: %+v, %v", updated, err)
	}
}

func TestLegacyInstancePermissionClaimRollsBackWithoutLosingLifecycle(t *testing.T) {
	s := newTestStore(t)
	in := sampleInstance("legacy-policy")
	if _, err := s.Put(in); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimIdentity(in.ID, InstanceIdentity{PermissionPolicy: "deny-all"})
	if err != nil || !claim.Changed || claim.Previous.PermissionPolicy != "" || claim.Claimed.PermissionPolicy != "deny-all" {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	if _, err := s.UpdateStatus(in.ID, StatusArchived); err != nil {
		t.Fatal(err)
	}
	if err := s.RollbackIdentityClaim(claim); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(in.ID)
	if err != nil || got.PermissionPolicy != "" || got.Status != StatusArchived {
		t.Fatalf("rollback = %+v, %v", got, err)
	}
}
