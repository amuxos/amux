// Package clientop holds the one implementation each agent-operable amux action
// has. The CLI and the MCP tool surface both call into it, so a rule cannot be
// fixed on one surface and left stale on the other.
//
// Validation errors carry a field identifier rather than finished prose: the
// same rule reads as "--agent is required" on the CLI and "task.agent is
// required" over MCP, and each surface owns its own vocabulary.
package clientop

import "fmt"

// Field identifies one input across every surface.
type Field string

const (
	FieldAgent             Field = "agent"
	FieldTask              Field = "task"
	FieldPermissionPolicy  Field = "permission_policy"
	FieldWorkspaceLocalDir Field = "workspace.local_dir"
	FieldRepoCacheKey      Field = "workspace.repo.cache_key"
	FieldWorkspacePolicy   Field = "workspace.policy"
	FieldSpec              Field = "spec"
	FieldScheduleID        Field = "schedule_id"
	FieldSessionName       Field = "name"
	FieldPrompt            Field = "prompt"
	FieldScheduleType      Field = "schedule_type"
	FieldRunMode           Field = "run_mode"
	FieldDeliveryTarget    Field = "delivery_target"
	FieldDeliveryPolicy    Field = "delivery_policy"
	FieldTargetID          Field = "target_id"
	FieldScheduledSession  Field = "session_name"
)

// FieldError is a validation failure attributable to one input.
type FieldError struct {
	Field   Field
	Problem string
	// Also names a second field when the rule is "one of these two".
	Also Field
	// RawValue is the caller's rejected input, kept so a surface can quote it
	// in its own phrasing.
	RawValue string
}

func (e *FieldError) Error() string { return e.Render(nil) }

// Render names the fields using the calling surface's vocabulary. A field the
// surface did not name falls back to its identifier.
func (e *FieldError) Render(names map[Field]string) string {
	subject := name(names, e.Field)
	if e.Also != "" {
		subject += " or " + name(names, e.Also)
	}
	return subject + " " + e.Problem
}

func name(names map[Field]string, f Field) string {
	if n, ok := names[f]; ok && n != "" {
		return n
	}
	return string(f)
}

// RequiredError reports a missing mandatory input.
func RequiredError(f Field) *FieldError {
	return &FieldError{Field: f, Problem: "is required"}
}

// RequiredEitherError reports a rule satisfied by either of two inputs.
func RequiredEitherError(f, also Field, condition string) *FieldError {
	return &FieldError{Field: f, Also: also, Problem: "is required " + condition}
}

// InvalidError reports an input the caller supplied with an unusable value.
func InvalidError(f Field, value, expected string) *FieldError {
	return &FieldError{
		Field:    f,
		Problem:  fmt.Sprintf("has invalid value %q (expected %s)", value, expected),
		RawValue: value,
	}
}
