package isolation

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
)

func policyTestConfig(t *testing.T, enabled bool, backend string) config.IsolationConfig {
	t.Helper()
	dir := t.TempDir()
	return config.IsolationConfig{
		Enabled:       enabled,
		Backend:       backend,
		UIDRangeStart: 700000,
		UIDRangeEnd:   799999,
		HelperSocket:  filepath.Join(dir, "helper.sock"),
		MappingDB:     filepath.Join(dir, "mapping.db"),
		PrincipalRoot: filepath.Join(dir, "principals"),
	}
}

func policyTestRuntime(t *testing.T, enabled bool, backend string) *Runtime {
	t.Helper()
	rt, err := NewRuntime(policyTestConfig(t, enabled, backend))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func identifiedTestIdentity() principal.ExecutionIdentity {
	return principal.ExecutionIdentity{
		IdentityMode:         principal.IdentityModeIdentified,
		EffectivePrincipalID: "11111111-1111-1111-1111-111111111111",
	}
}

// A host that isolates identified executions must not quietly run unidentified
// ones as the service account: that is how a sandboxed agent escapes by asking
// the control plane to start a second session for it.
func TestRequireIdentifiedExecutionRejectsLegacyHostWhenIsolationConfigured(t *testing.T) {
	rt := policyTestRuntime(t, true, principal.BackendLinuxDAC)
	err := rt.RequireIdentifiedExecution(principal.LegacyHostIdentity(principal.SourceCLI))
	if !errors.Is(err, ErrUnidentifiedExecution) {
		t.Fatalf("unidentified creation error = %v, want ErrUnidentifiedExecution", err)
	}
}

func TestRequireIdentifiedExecutionAllowsIdentified(t *testing.T) {
	rt := policyTestRuntime(t, true, principal.BackendLinuxDAC)
	if err := rt.RequireIdentifiedExecution(identifiedTestIdentity()); err != nil {
		t.Fatalf("identified creation error = %v, want nil", err)
	}
}

// The rule is a property of the isolation policy, not of helper health: hosts
// that never opted in keep their legacy behavior.
func TestRequireIdentifiedExecutionAllowsLegacyHostWhenIsolationDisabled(t *testing.T) {
	rt := policyTestRuntime(t, false, principal.BackendLinuxDAC)
	if err := rt.RequireIdentifiedExecution(principal.LegacyHostIdentity(principal.SourceCLI)); err != nil {
		t.Fatalf("disabled-isolation creation error = %v, want nil", err)
	}
}

func TestRequireIdentifiedExecutionAllowsLegacyHostWhenPassthroughConfigured(t *testing.T) {
	rt := policyTestRuntime(t, true, principal.BackendPassthrough)
	if err := rt.RequireIdentifiedExecution(principal.LegacyHostIdentity(principal.SourceCLI)); err != nil {
		t.Fatalf("passthrough creation error = %v, want nil", err)
	}
}

func TestRequireIdentifiedExecutionAllowsNilRuntime(t *testing.T) {
	var rt *Runtime
	if err := rt.RequireIdentifiedExecution(principal.LegacyHostIdentity(principal.SourceCLI)); err != nil {
		t.Fatalf("nil-runtime creation error = %v, want nil", err)
	}
}
