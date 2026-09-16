package task

import (
	"encoding/json"
	"strings"
)

// envelope is the fallback text result contract a child agent may print when its
// provider does not emit structured result events. Per the design it is parsed
// only at the adapter boundary; the control plane stores the structured form.
type envelope struct {
	Status       string        `json:"status"`
	Summary      string        `json:"summary"`
	ChangedFiles []ChangedFile `json:"changed_files"`
	Artifacts    []ArtifactRef `json:"artifacts"`
	Diagnostics  []Diagnostic  `json:"diagnostics"`
}

// ParseEnvelope extracts the last `amux_task_result` envelope embedded in agent
// output text, if present. For each occurrence of the marker key it decodes the
// JSON object that is the key's value (scanning forward with a string/escape
// aware brace matcher), so a `{` appearing before the marker cannot mis-anchor
// the parse. Returns nil when no valid envelope is found.
func ParseEnvelope(out string) *envelope {
	const marker = `"amux_task_result"`
	var found *envelope
	for i := 0; ; {
		idx := strings.Index(out[i:], marker)
		if idx < 0 {
			break
		}
		valStart, ok := objectValueStart(out, i+idx+len(marker))
		if ok {
			if obj, ok := matchObject(out[valStart:]); ok {
				var env envelope
				if json.Unmarshal([]byte(obj), &env) == nil {
					found = &env // keep the last valid one
				}
			}
		}
		i = i + idx + len(marker)
	}
	return found
}

// objectValueStart, given the index just past the marker key, skips an optional
// `:` and whitespace and returns the index of the value's opening `{`. It fails
// (false) if the value is not an object.
func objectValueStart(s string, pos int) (int, bool) {
	seenColon := false
	for pos < len(s) {
		c := s[pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			pos++
		case c == ':' && !seenColon:
			seenColon = true
			pos++
		case c == '{' && seenColon:
			return pos, true
		default:
			return 0, false
		}
	}
	return 0, false
}

// matchObject returns the balanced JSON object that begins at s[0]=='{',
// honouring string literals and escapes. ok is false if no balanced object ends.
func matchObject(s string) (string, bool) {
	if len(s) == 0 || s[0] != '{' {
		return "", false
	}
	depth := 0
	inStr := false
	esc := false
	for i, c := range s {
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// skip
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[:i+1], true
			}
		}
	}
	return "", false
}

// AssembleInput carries the raw materials gathered during a task run that are
// combined into the final TaskResult.
type AssembleInput struct {
	Outcome      TerminalOutcome
	AgentText    string        // accumulated agent output (for envelope fallback)
	FinalMessage string        // provider-native final message (native result path)
	ChangedFiles []ChangedFile // derived by amuxlet from the workspace
	Diagnostics  []Diagnostic  // e.g. denied permissions
	UsageJson    []byte
	Artifacts    []ArtifactRef
	// Granularity selects how much of the result is inlined; default is summary.
	Granularity ResultGranularity
	// TaskID is the owning task id, used to populate details_ref under
	// structured-summary so the caller can fetch the full transcript via
	// amux.task_stream / amux task logs.
	TaskID string
}

// AssembleResult builds the structured TaskResult from a terminal outcome and the
// gathered content. Completion judgement (signal vs content) lives in the runner;
// this only shapes the result payload. The Redactor masks secret material.
func AssembleResult(in AssembleInput, red *Redactor) *TaskResult {
	res := &TaskResult{
		ChangedFiles: in.ChangedFiles,
		Diagnostics:  in.Diagnostics,
		Artifacts:    in.Artifacts,
		UsageJson:    in.UsageJson,
	}

	switch in.Outcome.Category {
	case OutcomeSuccess:
		res.Status = ResultOK
	case OutcomeError:
		res.Status = ResultFailed
	default:
		res.Status = ResultPartial
	}

	// The fallback envelope is untrusted agent text parsed only at this boundary.
	// It may contribute the human-readable summary (and, if the workspace yielded
	// nothing, artifacts/diagnostics), but it must NOT drive the completion
	// signal or changed_files provenance:
	//   - status: owned by the control plane (terminal outcome), never agent text.
	//   - changed_files: owned by the amuxlet workspace diff, never agent text.
	//
	// Summary source precedence:
	//   1. envelope.Summary  — an agent that volunteers an envelope (richer/terser)
	//   2. FinalMessage      — the provider's native final message (native path)
	//   3. Outcome.RawError  — error fallback
	if env := ParseEnvelope(in.AgentText); env != nil {
		res.Summary = env.Summary
		if len(res.Artifacts) == 0 {
			res.Artifacts = env.Artifacts
		}
		if len(env.Diagnostics) > 0 {
			res.Diagnostics = append(res.Diagnostics, env.Diagnostics...)
		}
	}
	if res.Summary == "" && strings.TrimSpace(in.FinalMessage) != "" {
		res.Summary = strings.TrimSpace(in.FinalMessage)
	}
	if in.Outcome.RawError != "" && res.Summary == "" {
		res.Summary = in.Outcome.RawError
	}

	// Result granularity (v0.40): control how much of the agent transcript is
	// inlined back to the main agent. summary (default) leaves only the summary;
	// full inlines the complete redacted transcript (capped); structured-summary
	// attaches a details_ref (task_id) for on-demand fetch via stream/logs.
	granularity := in.Granularity
	if granularity == "" {
		granularity = GranularitySummary
	}
	switch granularity {
	case GranularityFull:
		transcript := strings.TrimSpace(in.AgentText)
		if red != nil {
			transcript = red.String(transcript)
		}
		if len(transcript) > MaxFullTranscriptBytes {
			cut := MaxFullTranscriptBytes
			// Walk back to a newline boundary so we don't split mid-token.
			if i := strings.LastIndexByte(transcript[:cut], '\n'); i > 0 {
				cut = i
			}
			transcript = transcript[:cut] + "\n[truncated; full transcript via `amux.task_stream " +
				in.TaskID + "` or `amux task logs " + in.TaskID + "`]"
		}
		res.FullTranscript = transcript
	case GranularityStructured:
		res.DetailsRef = in.TaskID
	}

	if red != nil {
		red.Result(res)
	}
	return res
}

// HasContent reports whether the result carries any caller-usable content. Used
// by the runner to distinguish succeeded from failed:missing_result.
func (r *TaskResult) HasContent() bool {
	if r == nil {
		return false
	}
	return strings.TrimSpace(r.Summary) != "" || len(r.ChangedFiles) > 0 || len(r.Artifacts) > 0
}
