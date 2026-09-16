package cli

import (
	"encoding/json"
	"testing"

	"github.com/amuxos/amux/internal/clientop"
)

// TestDerivedScheduledTaskSchemaCoversHandWrittenFields pins the migration from
// a hand-written schema constant to one derived from the request struct. The
// derived schema may add fields (that is the point), but it must not drop one a
// caller could already set.
func TestDerivedScheduledTaskSchemaCoversHandWrittenFields(t *testing.T) {
	raw, err := json.Marshal(clientop.JSONSchema(clientop.ScheduledTaskSpec{}))
	if err != nil {
		t.Fatalf("marshal derived schema: %v", err)
	}
	var derived struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &derived); err != nil {
		t.Fatalf("unmarshal derived schema: %v", err)
	}

	for _, field := range []string{
		"agent", "task", "run_as", "isolation", "credential_profile",
		"permission_policy", "workspace", "timeout_seconds", "trusted_mode_ack",
	} {
		if _, ok := derived.Properties[field]; !ok {
			t.Errorf("derived schema dropped %q, which the hand-written schema advertised", field)
		}
	}
	if len(derived.Required) != 2 || derived.Required[0] != "agent" || derived.Required[1] != "task" {
		t.Errorf("derived required = %v, want [agent task]", derived.Required)
	}
}
