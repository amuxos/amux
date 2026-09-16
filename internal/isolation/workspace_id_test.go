package isolation

import (
	"regexp"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/principal"
)

// Mirrors the root helper's executionPattern. A workspace name the helper
// rejects is a launch that cannot start, so the prefix must stay inside it.
var helperExecutionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func sourcedIdentity(source principal.Source, stateRef string) principal.ExecutionIdentity {
	return principal.ExecutionIdentity{
		IdentityMode:         principal.IdentityModeIdentified,
		EffectivePrincipalID: "11111111-1111-1111-1111-111111111111",
		Source:               source,
		ExecutionStateRef:    stateRef,
	}
}

// Under isolation the directory is chosen by principal, so a user's Feishu and
// CLI executions land side by side in one account with nothing in the name to
// tell them apart. The prefix restores that at a glance and makes per-source
// sweeps a glob instead of a database join.
func TestExecutionWorkspaceIDCarriesSourcePrefix(t *testing.T) {
	for _, tc := range []struct {
		source principal.Source
		want   string
	}{
		{principal.SourceFeishu, "feishu-exec_"},
		{principal.SourceCLI, "cli-exec_"},
		{principal.SourceMCP, "mcp-exec_"},
		{"", "exec_"},
	} {
		got := ExecutionWorkspaceID(sourcedIdentity(tc.source, "session:foo"))
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("source %q → %q, want prefix %q", tc.source, got, tc.want)
		}
		if !helperExecutionPattern.MatchString(got) {
			t.Errorf("source %q → %q, which the helper would reject", tc.source, got)
		}
	}
}

// The prefix must be purely additive: the same execution state ref has to keep
// hashing to the same suffix, or every existing workspace becomes unreachable.
func TestExecutionWorkspaceIDPrefixDoesNotDisturbTheHash(t *testing.T) {
	bare := ExecutionWorkspaceID(sourcedIdentity("", "session:foo"))
	feishu := ExecutionWorkspaceID(sourcedIdentity(principal.SourceFeishu, "session:foo"))
	if feishu != "feishu-"+bare {
		t.Fatalf("feishu id = %q, want %q", feishu, "feishu-"+bare)
	}
}

// A source that is not a plain token would produce a path component nobody
// vetted; dropping the prefix is safer than sanitizing into an ambiguous name.
func TestExecutionWorkspaceIDDropsUnsafeSource(t *testing.T) {
	got := ExecutionWorkspaceID(sourcedIdentity(principal.Source("../evil"), "session:foo"))
	if !strings.HasPrefix(got, "exec_") {
		t.Fatalf("unsafe source → %q, want an unprefixed name", got)
	}
}

// Sessions created before the prefix existed live in `exec_<hash>` directories.
// Recomputing their name would make relaunch fail its workspace assertion and
// make reclamation miss the tree, so an execution that already has a workspace
// keeps whatever it is actually sitting in.
func TestResolveExecutionWorkspaceIDKeepsAnExistingDirectory(t *testing.T) {
	identity := sourcedIdentity(principal.SourceFeishu, "session:foo")
	saved := "/var/lib/amux/principals/amux_abc/workspace/exec_0123456789abcdef01234567"
	if got := ResolveExecutionWorkspaceID(saved, identity); got != "exec_0123456789abcdef01234567" {
		t.Fatalf("resolved = %q, want the saved directory name", got)
	}
}

func TestResolveExecutionWorkspaceIDDerivesForANewExecution(t *testing.T) {
	identity := sourcedIdentity(principal.SourceFeishu, "session:foo")
	want := ExecutionWorkspaceID(identity)
	if got := ResolveExecutionWorkspaceID("", identity); got != want {
		t.Fatalf("resolved = %q, want derived %q", got, want)
	}
	if got := ResolveExecutionWorkspaceID("   ", identity); got != want {
		t.Fatalf("blank cwd resolved = %q, want derived %q", got, want)
	}
}

// A saved path whose basename could not have been produced by this scheme is
// not evidence of anything; deriving is safer than trusting it as a directory
// name to hand the privileged helper.
func TestResolveExecutionWorkspaceIDIgnoresAnImplausibleSavedPath(t *testing.T) {
	identity := sourcedIdentity(principal.SourceFeishu, "session:foo")
	want := ExecutionWorkspaceID(identity)
	for _, saved := range []string{"/tmp/../etc", "/home/someone/work", "/a/b/..", "/"} {
		if got := ResolveExecutionWorkspaceID(saved, identity); got != want {
			t.Errorf("saved %q resolved = %q, want derived %q", saved, got, want)
		}
	}
}
