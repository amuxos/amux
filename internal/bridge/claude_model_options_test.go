package bridge

import (
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
)

func claudeRuntime(t *testing.T, runtimes []agentprofile.RuntimeOption) agentprofile.RuntimeOption {
	t.Helper()
	for _, runtime := range runtimes {
		if runtime.Ref == "claude" {
			return runtime
		}
	}
	t.Fatalf("claude runtime missing from catalog")
	return agentprofile.RuntimeOption{}
}

func hasModelRef(runtime agentprofile.RuntimeOption, ref string) (agentprofile.RuntimeModelOption, bool) {
	for _, model := range runtime.Models {
		if model.Ref == ref {
			return model, true
		}
	}
	return agentprofile.RuntimeModelOption{}, false
}

func TestMergeClaudeRuntimeModelsAddsConfiguredRefs(t *testing.T) {
	t.Parallel()
	catalog, err := agentprofile.RuntimeCatalog()
	if err != nil {
		t.Fatalf("runtime catalog: %v", err)
	}
	base := claudeRuntime(t, catalog)

	merged := mergeClaudeRuntimeModels(catalog, []string{"claude-opus-5-5", "opus", "  "})
	claude := claudeRuntime(t, merged)

	model, ok := hasModelRef(claude, "claude-opus-5-5")
	if !ok {
		t.Fatalf("configured model claude-opus-5-5 was not added; models=%v", claude.Models)
	}
	if model.Kind != agentprofile.ModelKindNative || model.AgentModel != "claude-opus-5-5" || model.DisplayName != "claude-opus-5-5" {
		t.Fatalf("unexpected added model projection: %+v", model)
	}
	// The added model inherits the Claude runtime's reasoning efforts so the
	// effort picker keeps working for it.
	if len(model.SupportedReasoningEfforts) != len(base.DefaultReasoningEfforts) {
		t.Fatalf("expected inherited efforts %v, got %v", base.DefaultReasoningEfforts, model.SupportedReasoningEfforts)
	}
	// The already-present "opus" alias and the blank entry must not duplicate.
	count := 0
	for _, m := range claude.Models {
		if m.Ref == "opus" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("opus alias duplicated: count=%d", count)
	}

	// Other runtimes and the original catalog slice stay untouched.
	if _, ok := hasModelRef(claudeRuntime(t, catalog), "claude-opus-5-5"); ok {
		t.Fatalf("merge mutated the source catalog")
	}
}

func TestMergeClaudeRuntimeModelsIsIdempotent(t *testing.T) {
	t.Parallel()
	catalog, err := agentprofile.RuntimeCatalog()
	if err != nil {
		t.Fatalf("runtime catalog: %v", err)
	}
	once := mergeClaudeRuntimeModels(catalog, []string{"claude-opus-5-5"})
	twice := mergeClaudeRuntimeModels(once, []string{"claude-opus-5-5"})
	first := len(claudeRuntime(t, once).Models)
	second := len(claudeRuntime(t, twice).Models)
	if first != second {
		t.Fatalf("re-applying the merge changed model count: %d -> %d", first, second)
	}
}

func TestConfiguredClaudeModelAppearsInPicker(t *testing.T) {
	t.Parallel()
	catalog, err := agentprofile.RuntimeCatalog()
	if err != nil {
		t.Fatalf("runtime catalog: %v", err)
	}
	state := managementState{Runtimes: mergeClaudeRuntimeModels(catalog, []string{"claude-opus-5-5"})}
	options := managementRuntimeModelOptions(state, "claude")
	for _, option := range options {
		if option.Value == "claude-opus-5-5" {
			return
		}
	}
	t.Fatalf("configured model not offered in Feishu picker; options=%v", options)
}

func TestMergeClaudeRuntimeModelsNoConfigIsNoOp(t *testing.T) {
	t.Parallel()
	catalog, err := agentprofile.RuntimeCatalog()
	if err != nil {
		t.Fatalf("runtime catalog: %v", err)
	}
	if got := mergeClaudeRuntimeModels(catalog, nil); len(got) != len(catalog) {
		t.Fatalf("nil config changed catalog length")
	}
}
