package mux

import (
	"errors"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/session"
)

func TestStructuredProviderExtensionAdmission(t *testing.T) {
	const id = "example-provider-extension"
	if _, _, err := StartStructuredProc(StructuredLaunchOptions{Provider: id}); err == nil {
		t.Fatal("unregistered structured provider was admitted")
	}
	wantErr := errors.New("provider startup failed")
	factory := func(opts StructuredLaunchOptions, watchdog config.StructuredProviderConfig) (session.Proc, <-chan []byte, error) {
		if opts.SessionID != "session" || opts.ResumeSessionID != "native-thread" || watchdog.TurnIdleTimeoutSeconds != 23 {
			t.Fatalf("extension launch inputs changed: %+v, %+v", opts, watchdog)
		}
		return nil, nil, wantErr
	}
	if err := RegisterStructuredProvider(id, factory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { structuredProviderMu.Lock(); delete(structuredProviders, id); structuredProviderMu.Unlock() })
	_, _, err := StartStructuredProc(StructuredLaunchOptions{Provider: id, SessionID: "session", ResumeSessionID: "native-thread", TurnIdleTimeoutSeconds: 23})
	if !errors.Is(err, wantErr) {
		t.Fatalf("startup error = %v", err)
	}
	for _, duplicate := range []string{id, "codex-app-server", "claude-sdk", "pi-rpc"} {
		if err := RegisterStructuredProvider(duplicate, factory); err == nil {
			t.Fatalf("replaced provider %q", duplicate)
		}
	}
}
