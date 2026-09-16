package clientop

import (
	"encoding/json"
	"testing"
	"time"
)

func TestJSONSchemaDerivesScheduledTaskSpec(t *testing.T) {
	raw, err := json.Marshal(JSONSchema(ScheduledTaskSpec{}))
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	if got["type"] != "object" {
		t.Fatalf("type = %v, want object", got["type"])
	}

	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %T, want object", got["properties"])
	}
	for _, want := range []string{"agent", "task", "run_as", "isolation", "credential_profile",
		"permission_policy", "workspace", "timeout_seconds", "trusted_mode_ack"} {
		if _, ok := props[want]; !ok {
			t.Fatalf("properties missing %q; got %v", want, keysOf(props))
		}
	}

	// A field without omitempty is required; everything else is optional.
	required, _ := got["required"].([]any)
	if len(required) != 2 || required[0] != "agent" || required[1] != "task" {
		t.Fatalf("required = %v, want [agent task]", required)
	}
}

func TestJSONSchemaCarriesEnumAndDescription(t *testing.T) {
	props := JSONSchema(ScheduledTaskSpec{})["properties"].(map[string]any)
	isolation := props["isolation"].(map[string]any)
	enum, _ := isolation["enum"].([]string)
	if len(enum) != 2 || enum[0] != "process" || enum[1] != "private-home" {
		t.Fatalf("isolation enum = %v, want [process private-home]", enum)
	}
	if desc, _ := isolation["description"].(string); desc == "" {
		t.Fatal("isolation description is empty; the desc tag was not read")
	}
}

func TestJSONSchemaRecursesIntoNestedStructs(t *testing.T) {
	props := JSONSchema(ScheduledTaskSpec{})["properties"].(map[string]any)
	workspace := props["workspace"].(map[string]any)
	wsProps := workspace["properties"].(map[string]any)
	repo, ok := wsProps["repo"].(map[string]any)
	if !ok {
		t.Fatalf("workspace.repo missing; got %v", keysOf(wsProps))
	}
	repoProps := repo["properties"].(map[string]any)
	if _, ok := repoProps["cache_key"]; !ok {
		t.Fatalf("workspace.repo.cache_key missing; got %v", keysOf(repoProps))
	}
}

func TestJSONSchemaMapsGoTypesToJSONTypes(t *testing.T) {
	props := JSONSchema(ScheduledTaskSpec{})["properties"].(map[string]any)
	for field, want := range map[string]string{
		"agent":            "string",
		"timeout_seconds":  "integer",
		"trusted_mode_ack": "boolean",
		"workspace":        "object",
	} {
		got := props[field].(map[string]any)["type"]
		if got != want {
			t.Fatalf("%s type = %v, want %v", field, got, want)
		}
	}
}

func TestJSONSchemaPanicsOnEmbeddedField(t *testing.T) {
	type inner struct {
		Foo string `json:"foo"`
	}
	type probe struct {
		inner
	}
	defer func() {
		if recover() == nil {
			t.Fatal("JSONSchema did not panic on an embedded field")
		}
	}()
	JSONSchema(probe{})
}

func TestJSONSchemaPanicsOnMapField(t *testing.T) {
	type probe struct {
		Tags map[string]string `json:"tags,omitempty"`
	}
	defer func() {
		if recover() == nil {
			t.Fatal("JSONSchema did not panic on a map field")
		}
	}()
	JSONSchema(probe{})
}

func TestJSONSchemaPanicsOnNonStringSliceField(t *testing.T) {
	type probe struct {
		Nums []int `json:"nums,omitempty"`
	}
	defer func() {
		if recover() == nil {
			t.Fatal("JSONSchema did not panic on a []int field")
		}
	}()
	JSONSchema(probe{})
}

// TestJSONSchemaPanicsOnUnsupportedKind pins that an unsupported field kind is
// a loud programming error, not a silently-wrong "string" schema. This is the
// guarantee P4's 13 session tools stand on: a float, a uint, an interface, or
// any other kind the deriver does not model must fail at registration time.
func TestJSONSchemaPanicsOnUnsupportedKind(t *testing.T) {
	for _, tt := range []struct {
		name  string
		probe any
	}{
		{"float64", struct {
			F float64 `json:"f,omitempty"`
		}{}},
		{"uint32", struct {
			U uint32 `json:"u,omitempty"`
		}{}},
		{"interface", struct {
			A any `json:"a,omitempty"`
		}{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("JSONSchema did not panic on an unsupported kind")
				}
			}()
			JSONSchema(tt.probe)
		})
	}
}

// TestJSONSchemaPanicsOnFieldlessStruct pins that a struct with no fields — the
// canonical case being time.Time, which marshals to a JSON string — is refused
// rather than emitted as a lying {"type":"object"} schema.
func TestJSONSchemaPanicsOnFieldlessStruct(t *testing.T) {
	type probe struct {
		When time.Time `json:"when,omitempty"`
	}
	defer func() {
		if recover() == nil {
			t.Fatal("JSONSchema did not panic on a fieldless struct")
		}
	}()
	JSONSchema(probe{})
}

func TestJSONSchemaOmitsFieldsTaggedSchemaDash(t *testing.T) {
	type probe struct {
		Visible string `json:"visible"`
		Hidden  string `json:"hidden" schema:"-"`
	}
	schema := JSONSchema(probe{})

	props := schema["properties"].(map[string]any)
	if _, ok := props["hidden"]; ok {
		t.Fatalf("properties includes %q, want schema:\"-\" to omit it", "hidden")
	}
	if _, ok := props["visible"]; !ok {
		t.Fatalf("properties missing %q", "visible")
	}

	required, _ := schema["required"].([]any)
	for _, r := range required {
		if r == "hidden" {
			t.Fatalf("required = %v, want %q omitted despite lacking omitempty", required, "hidden")
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
