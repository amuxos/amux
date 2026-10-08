package agentprofile

import (
	"os"
	"os/exec"
	"reflect"
	"testing"
	"testing/fstest"
)

func testRuntimeProvider() *Provider {
	return &Provider{Platform: "openai-compat", WireAPI: "responses", BaseURL: "https://models.example/v1", UpstreamModel: "upstream-model", APIKeyEnv: "MODEL_API_KEY", Thinking: map[string]string{"mode": "enabled"}}
}

func TestRuntimeProviderRegistrationOwnsBindings(t *testing.T) {
	if os.Getenv("AMUX_TEST_RUNTIME_PROVIDER_REGISTRATION") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeProviderRegistrationOwnsBindings$")
		cmd.Env = append(os.Environ(), "AMUX_TEST_RUNTIME_PROVIDER_REGISTRATION=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("registration failed: %v\n%s", err, out)
		}
		return
	}
	provider := testRuntimeProvider()
	bindings := map[string]*Provider{"custom/model": provider}
	err := RegisterCustomization(Customization{Builtin: builtinProfileFS, DefaultProfile: "codex", ManagedLocalDefaultAgent: "codex", Catalog: []byte(`{"schema_version":"v1","packs":[]}`), RuntimeProviders: bindings, RuntimeCatalog: []RuntimeCatalogSource{{Ref: "codex", Models: []string{"custom/model"}}}})
	if err != nil {
		t.Fatal(err)
	}
	// Mutation of the caller's configuration and returned catalog cannot alter a
	// later launch or the registered connection settings.
	provider.BaseURL = "https://changed.example"
	provider.Thinking["mode"] = "changed"
	delete(bindings, "custom/model")
	builtinProfileFS = fstest.MapFS{}
	for range 2 {
		catalog, err := RuntimeCatalog()
		if err != nil {
			t.Fatal(err)
		}
		model := catalog[0].Models[0]
		if model.Kind != ModelKindProvider || model.AgentModel != "upstream-model" || model.ProviderRef != "custom" || model.Provider.BaseURL != "https://models.example/v1" || model.Provider.CredentialProfile != "openai-compat" || model.Provider.Thinking["mode"] != "enabled" {
			t.Fatal("runtime provider binding changed")
		}
		model.Provider.BaseURL = "https://returned.example"
		model.Provider.Thinking["mode"] = "returned"
	}
}

func TestInvalidRuntimeProviderRegistrationIsAtomic(t *testing.T) {
	beforeProviders, beforeSources := runtimeProviders, runtimeCatalogSources
	for _, tc := range []struct {
		name, ref string
		provider  *Provider
	}{
		{"missing", "custom/model", nil},
		{"invalid reference", "unqualified", testRuntimeProvider()},
		{"invalid connection", "custom/model", &Provider{Platform: "openai-compat", APIKeyEnv: "MODEL_API_KEY"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := RegisterCustomization(Customization{Builtin: builtinProfileFS, DefaultProfile: "codex", ManagedLocalDefaultAgent: "codex", Catalog: []byte(`{"schema_version":"v1","packs":[]}`), RuntimeProviders: map[string]*Provider{tc.ref: tc.provider}, RuntimeCatalog: []RuntimeCatalogSource{{Ref: "codex", Models: []string{tc.ref}}}})
			if err == nil {
				t.Fatal("invalid runtime provider accepted")
			}
			if customizationRegistered || !reflect.DeepEqual(beforeProviders, runtimeProviders) || !reflect.DeepEqual(beforeSources, runtimeCatalogSources) {
				t.Fatal("failed registration changed runtime state")
			}
		})
	}
}
