package agentprofile

import (
	"os"
	"os/exec"
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

func TestBuiltinProfilesRegisterFromManifestAlone(t *testing.T) {
	if mode := os.Getenv("AMUX_TEST_MANIFEST_REGISTRATION"); mode != "" {
		var metadata map[string]BuiltinMetadata
		legacyDescription := ""
		if mode == "compatibility" {
			metadata = map[string]BuiltinMetadata{"reviewer": {Description: "Stale description"}, "legacy": {Description: "Legacy fallback"}}
			legacyDescription = "Legacy fallback"
		}
		err := RegisterCustomization(Customization{
			Builtin:         fstest.MapFS{"dist/agent-pack.manifest.json": {Data: []byte(`{"schema_version":"v1.1","pack_namespace":"builtin","profiles":{"reviewer":{"type":"codex","description":"Review code changes"},"legacy":{"type":"codex"},"unregistered":{"type":"claude","description":"Manifest-only profile"}}}`)}},
			BuiltinMetadata: metadata,
			DefaultProfile:  "unregistered", ManagedLocalDefaultAgent: "codex",
			Catalog: []byte(`{"schema_version":"v1","packs":[]}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		for ref, want := range map[string]string{"builtin/reviewer": "Review code changes", "builtin/legacy": legacyDescription, "builtin/unregistered": "Manifest-only profile"} {
			profile, ok := ResolveBuiltin(ref)
			if !ok || profile.Description != want {
				t.Fatalf("%s = %+v, found %v", ref, profile, ok)
			}
		}
		return
	}
	for _, mode := range []string{"manifest", "compatibility"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBuiltinProfilesRegisterFromManifestAlone$")
		cmd.Env = append(os.Environ(), "AMUX_TEST_MANIFEST_REGISTRATION="+mode)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s registration: %v\n%s", mode, err, output)
		}
	}
}
