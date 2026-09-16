package agentprofile

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestCustomizationFailurePreservesPublicProfiles(t *testing.T) {
	beforeProfiles, beforeCatalog := Builtins(), CatalogPacks()
	beforeFS, beforeDefault := builtinProfileFS, DefaultProfileRef
	err := RegisterCustomization(Customization{
		Builtin:         fstest.MapFS{"dist/agent-pack.manifest.json": {Data: []byte(`{"schema_version":"v1.1","pack_namespace":"builtin","profiles":{"invalid":{"type":"unregistered-adapter"}}}`)}},
		BuiltinMetadata: map[string]BuiltinMetadata{"invalid": {Description: "Invalid"}},
		DefaultProfile:  "invalid", ManagedLocalDefaultAgent: "codex",
		Catalog: []byte(`{"schema_version":"v1","packs":[]}`),
	})
	if err == nil {
		t.Fatal("invalid extension builtin pack was accepted")
	}
	if !reflect.DeepEqual(builtinProfileFS, beforeFS) || DefaultProfileRef != beforeDefault || !reflect.DeepEqual(CatalogPacks(), beforeCatalog) {
		t.Fatal("failed registration changed public defaults")
	}
	for _, before := range beforeProfiles {
		after, ok := ResolveBuiltin(before.Ref())
		if !ok || !reflect.DeepEqual(after, before) {
			t.Fatalf("failed registration changed builtin %s", before.Ref())
		}
	}
	if customizationRegistered {
		t.Fatal("failed registration closed the registration window")
	}
}
