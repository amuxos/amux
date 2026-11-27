package bridge

import (
	"testing"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestPublicManagementAcceptsNativeRuntimeModelNames(t *testing.T) {
	t.Parallel()
	for _, runtimeRef := range []string{"codex", "claude", "pi"} {
		t.Run(runtimeRef, func(t *testing.T) {
			state := managementState{DefaultAgent: "builtin/" + runtimeRef, DefaultRuntimeRef: runtimeRef}
			action := &callback.CallBackAction{FormValue: map[string]any{
				"default_agent":       state.DefaultAgent,
				"default_runtime_ref": runtimeRef,
				"default_model_ref":   "example-native-model",
			}}
			if got := managementModelSubmissionError(action, state); got != "" {
				t.Fatalf("native model rejected: %s", got)
			}
			action.FormValue["default_runtime_ref"] = "unavailable"
			if got := managementModelSubmissionError(action, state); got == "" {
				t.Fatal("model accepted for unavailable runtime")
			}
		})
	}
}
