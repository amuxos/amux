package adapter

import "testing"

func TestRegisteredAdaptersDeclareSupportedProtocol(t *testing.T) {
	for _, name := range Names() {
		a, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
		got := a.Capabilities().Protocol
		provider, _ := got.StructuredProvider()
		if got != ProtocolACP && provider == "" {
			t.Errorf("%s protocol = %q, want ACP or structured protocol", name, got)
		}
	}
}

// NativeResult reports whether the agent's provider emits a native turn-result
// event, so the task runner can skip injecting the fallback result envelope.
// Every currently registered agent produces a native result.
func TestNativeResultCapabilityByAgent(t *testing.T) {
	for _, name := range Names() {
		a, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q): %v", name, err)
		}
		if !a.Capabilities().NativeResult {
			t.Errorf("%s Capabilities().NativeResult = false, want true", name)
		}
	}
}
