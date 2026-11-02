package agentprofile

import (
	"strings"
	"testing"
)

func TestNativeVariantRoundTripAndInvalidSelection(t *testing.T) {
	ref := NativeVariantRef("future model", "opaque_backend__max")
	model, err := ResolveRuntimeModel(RuntimeOption{Ref: "codex"}, ref)
	if err != nil || model.AgentModel != "future model" || model.BackendVariant != "opaque_backend__max" || !model.NativeCapabilitiesUnknown {
		t.Fatalf("roundtrip: %+v %v", model, err)
	}
	for _, ref := range []string{nativeVariantPrefix + "???", NativeVariantRef("", "max"), NativeVariantRef("base", "bad\nvariant")} {
		if _, err := ResolveRuntimeModel(RuntimeOption{Ref: "codex"}, ref); err == nil || !strings.Contains(err.Error(), "原生模型变体无效") {
			t.Fatalf("invalid ref accepted or untranslated: %v", err)
		}
	}
	// Projecting from a fresh runtime catalog keeps the opaque backend selector.
	profile := Profile{Type: "codex", ExecutionRuntimeRef: "codex"}
	launch, err := ProjectModelSwitch(profile, nil, NativeVariantRef("future-model", "opaque_backend__max"))
	if err != nil || launch.BackendVariant != "opaque_backend__max" || !argsContainConfigKey(launch.ExtraArgs, "model_backend_variant", "opaque_backend__max") {
		t.Fatalf("projection: %+v %v", launch, err)
	}
}
