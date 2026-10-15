package agentstream

import "strings"

// IsUserInput distinguishes a structured business question from an execution
// approval while retaining the compatible permission transport envelope.
func (p PermissionRequest) IsUserInput() bool {
	return p.ToolCall != nil && strings.TrimSpace(p.ToolCall.Question) != ""
}
