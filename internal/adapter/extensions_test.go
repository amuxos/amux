package adapter

import "testing"

func TestAdapterAndProtocolExtensions(t *testing.T) {
	const protocol Protocol = "example_rpc"
	const name = "example-extension"
	if err := RegisterProtocol(protocol, ProtocolRegistration{ProviderID: "example-provider", WatchdogKey: "example", CodexCompatible: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { protocolMu.Lock(); delete(registeredProtocols, protocol); protocolMu.Unlock() })
	if err := Register(name, func() Adapter { return &extensionAdapter{} }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		adapterMu.Lock()
		defer adapterMu.Unlock()
		for i, entry := range registeredAdapters {
			if entry.name == name {
				registeredAdapters = append(registeredAdapters[:i], registeredAdapters[i+1:]...)
				break
			}
		}
	})
	if !IsCodexCompatible(name) {
		t.Fatal("registered protocol lost compatibility")
	}
	if provider, key := protocol.StructuredProvider(); provider != "example-provider" || key != "example" {
		t.Fatalf("protocol = %q, %q", provider, key)
	}
	if err := Register(name, func() Adapter { return &CodexAdapter{} }); err == nil {
		t.Fatal("replaced registered adapter")
	}
	if err := Register("codex", func() Adapter { return &CodexAdapter{} }); err == nil {
		t.Fatal("replaced built-in adapter")
	}
	if err := RegisterProtocol(ProtocolCodexAppServer, ProtocolRegistration{ProviderID: "example"}); err == nil {
		t.Fatal("replaced built-in protocol")
	}
}

type extensionAdapter struct{ CodexAdapter }

func (*extensionAdapter) Capabilities() Capabilities { return Capabilities{Protocol: "example_rpc"} }
