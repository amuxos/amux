package agentstream

import (
	"encoding/json"
	"fmt"
	"reflect"
)

const MaxExtensionPayloadBytes = 64 << 10

type AssistantMessage struct {
	Text string `json:"text"`
}

func (AssistantMessage) Kind() Kind        { return KindAssistantMessage }
func (AssistantMessage) eventBody()        {}
func (AssistantMessage) turnPayload()      {}
func (v AssistantMessage) cloneBody() Body { return v }

type ThinkingDelta struct {
	Text string `json:"text"`
}

func (ThinkingDelta) Kind() Kind        { return KindThinkingDelta }
func (ThinkingDelta) eventBody()        {}
func (ThinkingDelta) turnPayload()      {}
func (v ThinkingDelta) cloneBody() Body { return v }

type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

type PlanReplaced struct {
	Entries []PlanEntry `json:"entries"`
}

func (PlanReplaced) Kind() Kind   { return KindPlanReplaced }
func (PlanReplaced) eventBody()   {}
func (PlanReplaced) turnPayload() {}
func (v PlanReplaced) cloneBody() Body {
	v.Entries = append([]PlanEntry(nil), v.Entries...)
	return v
}

type ToolPhase string

const (
	ToolPhaseStarted   ToolPhase = "started"
	ToolPhaseUpdated   ToolPhase = "updated"
	ToolPhaseCompleted ToolPhase = "completed"
	ToolPhaseFailed    ToolPhase = "failed"
)

type Tool struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Title         string          `json:"title"`
	Input         json.RawMessage `json:"input,omitempty"`
	OutputPreview string          `json:"output_preview"`
	Error         string          `json:"error"`
	Content       json.RawMessage `json:"content,omitempty"`
	Locations     json.RawMessage `json:"locations,omitempty"`
	RawOutput     json.RawMessage `json:"raw_output,omitempty"`
}

type ToolChanged struct {
	Phase ToolPhase `json:"phase"`
	Tool  Tool      `json:"tool"`
}

func (ToolChanged) Kind() Kind   { return KindToolChanged }
func (ToolChanged) eventBody()   {}
func (ToolChanged) turnPayload() {}
func (v ToolChanged) cloneBody() Body {
	v.Tool = cloneTool(v.Tool)
	return v
}

type PermissionToolCall struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	RawInput    json.RawMessage `json:"raw_input,omitempty"`
	Question    string          `json:"question"`
	MultiSelect bool            `json:"multi_select"`
	AllowOther  bool            `json:"allow_other,omitempty"`
	Secret      bool            `json:"secret,omitempty"`
	Blocking    *bool           `json:"blocking,omitempty"`
	// Async delivery has no pending provider RPC; answers become queued input.
	Delivery       string `json:"delivery,omitempty"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
}

type PermissionOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
}

type PermissionRequest struct {
	ID       string              `json:"id"`
	Provider string              `json:"provider"`
	ToolCall *PermissionToolCall `json:"tool_call"`
	Options  []PermissionOption  `json:"options"`
	Details  json.RawMessage     `json:"details,omitempty"`
}

type PermissionRequested struct {
	Request PermissionRequest `json:"request"`
}

func (PermissionRequested) Kind() Kind   { return KindPermissionRequested }
func (PermissionRequested) eventBody()   {}
func (PermissionRequested) turnPayload() {}
func (v PermissionRequested) cloneBody() Body {
	v.Request = clonePermissionRequest(v.Request)
	return v
}

type PermissionResolved struct {
	RequestID string   `json:"request_id"`
	OptionIDs []string `json:"option_ids"`
	Message   string   `json:"message"`
}

func (PermissionResolved) Kind() Kind   { return KindPermissionResolved }
func (PermissionResolved) eventBody()   {}
func (PermissionResolved) turnPayload() {}
func (v PermissionResolved) cloneBody() Body {
	v.OptionIDs = append([]string(nil), v.OptionIDs...)
	return v
}

type UsageObserved struct {
	Usage json.RawMessage `json:"usage,omitempty"`
}

func (UsageObserved) Kind() Kind   { return KindUsageObserved }
func (UsageObserved) eventBody()   {}
func (UsageObserved) turnPayload() {}
func (v UsageObserved) cloneBody() Body {
	v.Usage = cloneBytes(v.Usage)
	return v
}

type ResultObserved struct {
	Text    string          `json:"text"`
	Usage   json.RawMessage `json:"usage,omitempty"`
	CostUSD float64         `json:"cost_usd"`
}

func (ResultObserved) Kind() Kind   { return KindResultObserved }
func (ResultObserved) eventBody()   {}
func (ResultObserved) turnPayload() {}
func (v ResultObserved) cloneBody() Body {
	v.Usage = cloneBytes(v.Usage)
	return v
}

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Diagnostic struct {
	Severity  Severity `json:"severity"`
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Retryable bool     `json:"retryable"`
}

func (Diagnostic) Kind() Kind        { return KindDiagnostic }
func (Diagnostic) eventBody()        {}
func (Diagnostic) turnPayload()      {}
func (v Diagnostic) cloneBody() Body { return v }

type SessionDiagnostic struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
}

func (SessionDiagnostic) Kind() Kind        { return KindSessionDiagnostic }
func (SessionDiagnostic) eventBody()        {}
func (SessionDiagnostic) sessionPayload()   {}
func (v SessionDiagnostic) cloneBody() Body { return v }

// ExtensionObserved is bounded opaque observation data. Consumers must not use
// it to drive turn lifecycle, permission, or task outcome decisions.
type ExtensionObserved struct {
	Namespace string          `json:"namespace"`
	Schema    string          `json:"schema"`
	Payload   json.RawMessage `json:"payload"`
}

func (ExtensionObserved) Kind() Kind      { return KindExtensionObserved }
func (ExtensionObserved) eventBody()      {}
func (ExtensionObserved) sessionPayload() {}
func (v ExtensionObserved) cloneBody() Body {
	v.Payload = cloneBytes(v.Payload)
	return v
}

func cloneTool(tool Tool) Tool {
	tool.Input = cloneBytes(tool.Input)
	tool.Content = cloneBytes(tool.Content)
	tool.Locations = cloneBytes(tool.Locations)
	tool.RawOutput = cloneBytes(tool.RawOutput)
	return tool
}

func clonePermissionRequest(request PermissionRequest) PermissionRequest {
	if request.ToolCall != nil {
		toolCall := *request.ToolCall
		toolCall.RawInput = cloneBytes(toolCall.RawInput)
		request.ToolCall = &toolCall
	}
	request.Options = append([]PermissionOption(nil), request.Options...)
	request.Details = cloneBytes(request.Details)
	return request
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}

func normalizeTurnPayload(body TurnPayload) (Body, error) {
	cloned, err := clonePayload(body)
	if err != nil {
		return nil, err
	}
	switch value := cloned.(type) {
	case AssistantDelta, AssistantMessage, ThinkingDelta, PlanReplaced:
		return value, nil
	case ToolChanged:
		if !validToolPhase(value.Phase) || !validRawJSON(value.Tool.Input) ||
			!validRawJSON(value.Tool.Content) || !validRawJSON(value.Tool.Locations) ||
			!validRawJSON(value.Tool.RawOutput) {
			return nil, invalidPayload("invalid tool change")
		}
		return value, nil
	case PermissionRequested:
		if value.Request.ID == "" || !validRawJSON(value.Request.Details) ||
			(value.Request.ToolCall != nil && !validRawJSON(value.Request.ToolCall.RawInput)) {
			return nil, invalidPayload("invalid permission request")
		}
		return value, nil
	case PermissionResolved:
		if value.RequestID == "" {
			return nil, invalidPayload("permission resolution has no request id")
		}
		return value, nil
	case UsageObserved:
		if !validRawJSON(value.Usage) {
			return nil, invalidPayload("usage is not valid JSON")
		}
		return value, nil
	case ResultObserved:
		if !validRawJSON(value.Usage) {
			return nil, invalidPayload("result usage is not valid JSON")
		}
		return value, nil
	case Diagnostic:
		if !validSeverity(value.Severity) {
			return nil, invalidPayload("invalid diagnostic severity")
		}
		return value, nil
	default:
		return nil, invalidPayload("payload is not turn-scoped")
	}
}

func normalizeSessionPayload(body SessionPayload) (Body, error) {
	cloned, err := clonePayload(body)
	if err != nil {
		return nil, err
	}
	switch value := cloned.(type) {
	case SessionMetadataChanged, CommandsReplaced, GoalChanged:
		return value, nil
	case SessionDiagnostic:
		if !validSeverity(value.Severity) {
			return nil, invalidPayload("invalid session diagnostic severity")
		}
		return value, nil
	case ExtensionObserved:
		if value.Namespace == "" || value.Schema == "" || len(value.Payload) > MaxExtensionPayloadBytes || !json.Valid(value.Payload) {
			return nil, invalidPayload("invalid extension observation")
		}
		return value, nil
	default:
		return nil, invalidPayload("payload is not session-scoped")
	}
}

func clonePayload(body Body) (Body, error) {
	if body == nil {
		return nil, ErrInvalidPayload
	}
	value := reflect.ValueOf(body)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil, ErrInvalidPayload
	}
	return body.cloneBody(), nil
}

func validToolPhase(phase ToolPhase) bool {
	switch phase {
	case ToolPhaseStarted, ToolPhaseUpdated, ToolPhaseCompleted, ToolPhaseFailed:
		return true
	default:
		return false
	}
}

func validSeverity(severity Severity) bool {
	switch severity {
	case SeverityInfo, SeverityWarning, SeverityError:
		return true
	default:
		return false
	}
}

// Empty provider details mean "not reported" and are valid. Non-empty details
// must be valid JSON before entering the canonical stream.
func validRawJSON(value json.RawMessage) bool {
	return len(value) == 0 || json.Valid(value)
}

func invalidPayload(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidPayload, reason)
}
