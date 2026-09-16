package clientop

import (
	"reflect"
	"strings"
)

// JSONSchema derives an MCP tool input schema from a request struct, so a field
// added to the struct cannot be silently missing from the tool surface. Tags:
//
//	json:"name,omitempty"  field name; omitempty means optional
//	desc:"..."             human description shown to the model
//	enum:"a,b,c"           closed value set
//	schema:"-"             field is accepted/decoded (json tag still applies)
//	                       but deliberately not advertised on the tool surface;
//	                       use this for a field that exists on the wire type
//	                       for another caller (e.g. the CLI) but that nothing
//	                       downstream reads yet, so promising it to MCP callers
//	                       would be misleading
//
// Supported field shapes: string (including named string types), bool, the
// integer kinds, []string, struct, and pointer-to-struct. Anything else
// (embedded/anonymous fields, map, a slice whose element is not a string, a
// struct with no fields, or any kind not listed above) panics rather than
// emitting a schema that silently disagrees with what encoding/json actually
// produces for that field — see schemaForField and schemaForType. JSONSchema is
// called at tool-registration time on a compile-time-known type, so an
// unsupported shape is a programming error to catch immediately, not a runtime
// condition for callers to handle.
//
// Every integer field in this package today (timeout_seconds, schedule_id,
// from_seq, ...) is a non-negative count, so every integer schema carries
// "minimum": 0. That is a deliberate assumption baked in from those fields,
// not a general rule: a future signed integer field (e.g. a priority delta)
// will need an escape hatch added here, since as written it would wrongly
// forbid negative values.
func JSONSchema(v any) map[string]any {
	return schemaForType(reflect.TypeOf(v))
}

func schemaForType(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	properties := map[string]any{}
	required := []any{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous {
			// Checked before the IsExported skip below: encoding/json promotes
			// an embedded struct's exported fields to the parent even when the
			// embedded type's own name is unexported, so relying on
			// IsExported here would let an embedded field slip past this
			// check and be silently dropped instead of panicking.
			panic("clientop: JSONSchema does not support embedded fields (" + t.Name() + "." + field.Name +
				"); encoding/json flattens them but the deriver does not, so the schema would silently omit them")
		}
		if !field.IsExported() {
			continue
		}
		name, optional, skip := jsonFieldName(field)
		if skip {
			continue
		}
		properties[name] = schemaForField(field)
		if !optional {
			required = append(required, name)
		}
	}
	out := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func jsonFieldName(field reflect.StructField) (name string, optional, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" || field.Tag.Get("schema") == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = field.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			optional = true
		}
	}
	return name, optional, false
}

func hasExportedFields(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).IsExported() {
			return true
		}
	}
	return false
}

func schemaForField(field reflect.StructField) map[string]any {
	t := field.Type
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	var out map[string]any
	switch t.Kind() {
	case reflect.Struct:
		if !hasExportedFields(t) {
			// A struct with no exported fields — the canonical case is time.Time,
			// which has only unexported fields and marshals to a JSON string —
			// would otherwise recurse into {"type":"object","properties":{}}, a
			// schema that disagrees with encoding/json. Named string types (e.g.
			// task.Mode) reach the reflect.String case, not here.
			panic("clientop: JSONSchema does not support struct " + t.String() +
				" with no exported fields (" + field.Name + "); it does not marshal to a JSON object")
		}
		out = schemaForType(t)
	case reflect.Bool:
		out = map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		out = map[string]any{"type": "integer", "minimum": 0}
	case reflect.Slice:
		if elem := t.Elem(); elem.Kind() != reflect.String {
			panic("clientop: JSONSchema does not support slice element kind " + elem.Kind().String() +
				" (" + field.Name + " []" + elem.String() + "); only []string is handled")
		}
		out = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	case reflect.Map:
		panic("clientop: JSONSchema does not support map fields (" + field.Name + " " + t.String() +
			"); add explicit handling before using a map in a request struct")
	case reflect.String:
		out = map[string]any{"type": "string"}
	default:
		panic("clientop: JSONSchema does not support kind " + t.Kind().String() +
			" (" + field.Name + " " + t.String() + "); add explicit handling. Falling back to " +
			"\"string\" would emit a schema that lies about what encoding/json produces for this field")
	}
	if desc := field.Tag.Get("desc"); desc != "" {
		out["description"] = desc
	}
	if enum := field.Tag.Get("enum"); enum != "" {
		out["enum"] = strings.Split(enum, ",")
	}
	return out
}
