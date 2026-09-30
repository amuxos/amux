package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
)

type fakeManagedLocalActions struct {
	createdSpec     agentprofile.ManagedProfileSpec
	updatedName     string
	updatedText     string
	updatedRevision string
	snapshot        agentprofile.ManagedLibrarySnapshot
	snapshotCalls   int
	forkSource      string
	forkTarget      string
	history         []agentprofile.ManagedLibraryVersion
	restoredTarget  string
	restoredCurrent string
}

func (f *fakeManagedLocalActions) Create(_ context.Context, spec agentprofile.ManagedProfileSpec) (agentprofile.ManagedLibraryMutation, error) {
	f.createdSpec = spec
	return agentprofile.ManagedLibraryMutation{ProfileRef: "local/" + spec.Name, Revision: "abc123"}, nil
}

func (f *fakeManagedLocalActions) UpdateInstructions(_ context.Context, name, instructions, expectedRevision string) (agentprofile.ManagedLibraryMutation, error) {
	f.updatedName = name
	f.updatedText = instructions
	f.updatedRevision = expectedRevision
	return agentprofile.ManagedLibraryMutation{ProfileRef: "local/" + name, Revision: "def456"}, nil
}

func (f *fakeManagedLocalActions) Snapshot(context.Context) (agentprofile.ManagedLibrarySnapshot, error) {
	f.snapshotCalls++
	return f.snapshot, nil
}

func (f *fakeManagedLocalActions) ForkBuiltin(_ context.Context, source, target string) (agentprofile.ManagedLibraryMutation, error) {
	f.forkSource, f.forkTarget = source, target
	return agentprofile.ManagedLibraryMutation{ProfileRef: "local/" + target, Revision: "fork123"}, nil
}

func (f *fakeManagedLocalActions) History(context.Context, int) ([]agentprofile.ManagedLibraryVersion, error) {
	return f.history, nil
}

func (f *fakeManagedLocalActions) Restore(_ context.Context, target, current string) (agentprofile.ManagedLibraryMutation, error) {
	f.restoredTarget, f.restoredCurrent = target, current
	return agentprofile.ManagedLibraryMutation{ProfileRef: "local", Revision: "restore123"}, nil
}

func useFakeManagedLocalActions(t *testing.T, fake *fakeManagedLocalActions) {
	t.Helper()
	previous := newManagedLocalActions
	newManagedLocalActions = func() managedLocalActions { return fake }
	t.Cleanup(func() { newManagedLocalActions = previous })
}

func TestAgentLocalCreateUsesInlineInstructions(t *testing.T) {
	fake := &fakeManagedLocalActions{}
	useFakeManagedLocalActions(t, fake)
	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"local", "create", "reviewer", "--instructions", "Review changes", "--description", "Review code changes", "--type", "claude", "--model", "opus", "--permission", "plan"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent local create: %v", err)
	}
	if fake.createdSpec.Description != "Review code changes" || fake.createdSpec.Name != "reviewer" || fake.createdSpec.Type != "claude" || fake.createdSpec.Model != "opus" || fake.createdSpec.PermissionMode != "plan" || fake.createdSpec.Instructions != "Review changes" {
		t.Fatalf("created spec = %+v", fake.createdSpec)
	}
	if !strings.Contains(out.String(), "Created local expert local/reviewer") || !strings.Contains(out.String(), "abc123") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAgentLocalCreateRejectsAmbiguousInstructions(t *testing.T) {
	useFakeManagedLocalActions(t, &fakeManagedLocalActions{})
	cmd := newAgentCmd()
	cmd.SetArgs([]string{"local", "create", "reviewer", "--instructions", "inline", "--instructions-file", "AGENTS.md"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "exactly one of --instructions or --instructions-file") {
		t.Fatalf("error = %v", err)
	}
}

func TestAgentLocalUpdatePassesExpectedRevision(t *testing.T) {
	fake := &fakeManagedLocalActions{}
	useFakeManagedLocalActions(t, fake)
	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"local", "update", "reviewer", "--instructions", "new", "--expected-revision", "abc123"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent local update: %v", err)
	}
	if fake.updatedName != "reviewer" || fake.updatedText != "new" || fake.updatedRevision != "abc123" {
		t.Fatalf("update = name:%q text:%q revision:%q", fake.updatedName, fake.updatedText, fake.updatedRevision)
	}
	if fake.snapshotCalls != 0 {
		t.Fatalf("snapshot calls = %d, want 0 for an explicit revision", fake.snapshotCalls)
	}
	if !strings.Contains(out.String(), "Updated local expert local/reviewer") || !strings.Contains(out.String(), "def456") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAgentLocalUpdateUsesCurrentRevisionWhenFlagIsOmitted(t *testing.T) {
	fake := &fakeManagedLocalActions{snapshot: agentprofile.ManagedLibrarySnapshot{
		Initialized: true,
		Revision:    "abc123",
	}}
	useFakeManagedLocalActions(t, fake)
	cmd := newAgentCmd()
	cmd.SetArgs([]string{"local", "update", "reviewer", "--instructions", "new"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent local update: %v", err)
	}
	if fake.snapshotCalls != 1 {
		t.Fatalf("snapshot calls = %d, want 1", fake.snapshotCalls)
	}
	if fake.updatedRevision != "abc123" {
		t.Fatalf("updated revision = %q, want abc123", fake.updatedRevision)
	}
}

func TestAgentLocalUpdateWithoutRevisionRequiresInitializedLibrary(t *testing.T) {
	fake := &fakeManagedLocalActions{}
	useFakeManagedLocalActions(t, fake)
	cmd := newAgentCmd()
	cmd.SetArgs([]string{"local", "update", "reviewer", "--instructions", "new"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "local expert library is not initialized") {
		t.Fatalf("error = %v", err)
	}
	if fake.updatedName != "" {
		t.Fatalf("unexpected update of %q", fake.updatedName)
	}
}

func TestAgentLocalStatus(t *testing.T) {
	fake := &fakeManagedLocalActions{snapshot: agentprofile.ManagedLibrarySnapshot{
		Initialized:      true,
		Namespace:        "local",
		Revision:         "abc123",
		OriginPath:       "/tmp/origin.git",
		MaterializedPath: "/tmp/repo",
		Profiles:         []string{"one", "two"},
	}}
	useFakeManagedLocalActions(t, fake)
	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"local", "status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent local status: %v", err)
	}
	for _, want := range []string{"initialized: true", "namespace: local", "revision: abc123", "local/one", "local/two"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output = %q, want %q", out.String(), want)
		}
	}
}

func TestAgentForkCopiesBuiltinToLocal(t *testing.T) {
	fake := &fakeManagedLocalActions{}
	useFakeManagedLocalActions(t, fake)
	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"fork", "builtin/codex", "local/my-codex"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent fork: %v", err)
	}
	if fake.forkSource != "builtin/codex" || fake.forkTarget != "my-codex" {
		t.Fatalf("fork = %q -> %q", fake.forkSource, fake.forkTarget)
	}
	if !strings.Contains(out.String(), "local/my-codex") || !strings.Contains(out.String(), "fork123") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestAgentLocalHistoryAndRestore(t *testing.T) {
	fake := &fakeManagedLocalActions{
		snapshot: agentprofile.ManagedLibrarySnapshot{Initialized: true, Revision: "current123"},
		history:  []agentprofile.ManagedLibraryVersion{{Revision: "abc123", Subject: "amux: create local expert reviewer"}},
	}
	useFakeManagedLocalActions(t, fake)
	history := newAgentCmd()
	var out bytes.Buffer
	history.SetOut(&out)
	history.SetArgs([]string{"local", "history"})
	if err := history.Execute(); err != nil {
		t.Fatalf("history: %v", err)
	}
	if !strings.Contains(out.String(), "abc123") || !strings.Contains(out.String(), "create local expert") {
		t.Fatalf("history output = %q", out.String())
	}
	restore := newAgentCmd()
	restore.SetArgs([]string{"local", "restore", "abc123"})
	if err := restore.Execute(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if fake.restoredTarget != "abc123" || fake.restoredCurrent != "current123" {
		t.Fatalf("restore = target:%q current:%q", fake.restoredTarget, fake.restoredCurrent)
	}
}
