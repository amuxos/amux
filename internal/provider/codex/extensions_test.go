package codex

import (
	"context"
	"strings"
	"testing"
)

func TestProviderRegistrationAdmissionAndCapabilities(t *testing.T) {
	const name = "registered-test-provider"
	_, _, err := Start(context.Background(), StartOptions{ProviderName: name, Executable: "/missing/provider"})
	if err == nil || !strings.Contains(err.Error(), "unsupported app-server provider") {
		t.Fatalf("unregistered provider reached process launch: %v", err)
	}
	registration := ProviderRegistration{InputModalities: []string{"text", "image"}, InputCapabilitySource: "test:defaults"}
	if err := RegisterProvider(name, registration); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { providerMu.Lock(); delete(registeredProviders, name); providerMu.Unlock() })
	registration.InputModalities[1] = "mutated"
	p := &Proc{providerName: name}
	p.discoverInputCapabilities(context.Background(), "")
	modalities, source := p.InputCapabilities()
	if strings.Join(modalities, ",") != "text,image" || source != "test:defaults" {
		t.Fatalf("registered capability metadata mutated: %v, %q", modalities, source)
	}
	for _, duplicate := range []string{name, "codex"} {
		if err := RegisterProvider(duplicate, registration); err == nil {
			t.Fatalf("replaced provider %q", duplicate)
		}
	}
	if err := RegisterProvider("missing-source", ProviderRegistration{InputModalities: []string{"image"}}); err == nil {
		t.Fatal("accepted modalities without a source")
	}
}
