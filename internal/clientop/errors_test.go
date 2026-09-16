package clientop

import (
	"errors"
	"testing"
)

func TestFieldErrorRendersWithSurfaceVocabulary(t *testing.T) {
	err := RequiredError(FieldAgent)

	cli := err.Render(map[Field]string{FieldAgent: "--agent"})
	if cli != "--agent is required" {
		t.Fatalf("cli render = %q, want %q", cli, "--agent is required")
	}

	mcp := err.Render(map[Field]string{FieldAgent: "task.agent"})
	if mcp != "task.agent is required" {
		t.Fatalf("mcp render = %q, want %q", mcp, "task.agent is required")
	}
}

func TestFieldErrorUnknownFieldFallsBackToItsIdentifier(t *testing.T) {
	// A surface that forgot to name a field must still produce a usable
	// message rather than an empty one.
	if got := RequiredError(FieldAgent).Render(nil); got != "agent is required" {
		t.Fatalf("fallback render = %q, want %q", got, "agent is required")
	}
}

func TestFieldErrorIsAnError(t *testing.T) {
	var target *FieldError
	if !errors.As(error(RequiredError(FieldAgent)), &target) {
		t.Fatal("RequiredError does not unwrap to *FieldError")
	}
	if got := RequiredError(FieldAgent).Error(); got != "agent is required" {
		t.Fatalf("Error() = %q, want %q", got, "agent is required")
	}
}

func TestFieldErrorRequiredEitherRendersBothFields(t *testing.T) {
	err := RequiredEitherError(FieldWorkspaceLocalDir, FieldRepoCacheKey, "with workspace policy git-worktree")

	got := err.Render(map[Field]string{
		FieldWorkspaceLocalDir: "--local-dir",
		FieldRepoCacheKey:      "--repo-cache-key",
	})
	want := "--local-dir or --repo-cache-key is required with workspace policy git-worktree"
	if got != want {
		t.Fatalf("render = %q, want %q", got, want)
	}
}

func TestFieldErrorInvalidRendersValueAndExpectation(t *testing.T) {
	err := InvalidError(FieldPermissionPolicy, "custom-policy", "default, policy-allow, or deny-all")

	got := err.Render(nil)
	want := `permission_policy has invalid value "custom-policy" (expected default, policy-allow, or deny-all)`
	if got != want {
		t.Fatalf("render = %q, want %q", got, want)
	}
}
