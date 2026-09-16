package task

import (
	"strings"
	"testing"
)

func TestParseEnvelope(t *testing.T) {
	out := `some agent chatter
here is my result:
{"amux_task_result": {"status": "ok", "summary": "did the thing", "changed_files": [], "artifacts": [], "diagnostics": []}}
trailing text`
	env := ParseEnvelope(out)
	if env == nil {
		t.Fatal("expected envelope")
	}
	if env.Summary != "did the thing" || env.Status != "ok" {
		t.Fatalf("got %+v", env)
	}
}

func TestParseEnvelopeNested(t *testing.T) {
	out := `{"amux_task_result": {"status": "partial", "summary": "nested {brace} ok", "changed_files": [{"path":"a.go","change_type":"modified"}]}}`
	env := ParseEnvelope(out)
	if env == nil {
		t.Fatal("expected envelope")
	}
	if env.Summary != "nested {brace} ok" {
		t.Fatalf("summary = %q", env.Summary)
	}
	if len(env.ChangedFiles) != 1 || env.ChangedFiles[0].Path != "a.go" {
		t.Fatalf("changed files = %+v", env.ChangedFiles)
	}
}

func TestParseEnvelopeLastWins(t *testing.T) {
	out := `{"amux_task_result":{"summary":"first"}} ... {"amux_task_result":{"summary":"second"}}`
	env := ParseEnvelope(out)
	if env == nil || env.Summary != "second" {
		t.Fatalf("got %+v", env)
	}
}

func TestParseEnvelopeNone(t *testing.T) {
	if ParseEnvelope("no marker here") != nil {
		t.Fatal("expected nil")
	}
	if ParseEnvelope(`{"amux_task_result": not json}`) != nil {
		t.Fatal("expected nil for invalid json")
	}
}

// TestParseEnvelopeBracePrecedingMarker guards the fix for the backward-scan bug:
// a '{' appearing before the marker must not mis-anchor the object parse.
func TestParseEnvelopeBracePrecedingMarker(t *testing.T) {
	out := `prelude {"unrelated":{}} then {"x":{},"amux_task_result":{"status":"ok","summary":"found me"}} end`
	env := ParseEnvelope(out)
	if env == nil || env.Summary != "found me" {
		t.Fatalf("got %+v", env)
	}
}

// TestAssembleResultIgnoresEnvelopeStatusAndChangedFiles guards the provenance
// rules: agent text may set summary but never the completion status or
// changed_files (those are owned by the control plane / workspace).
func TestAssembleResultIgnoresEnvelopeStatusAndChangedFiles(t *testing.T) {
	in := AssembleInput{
		Outcome:   TerminalOutcome{Category: OutcomeSuccess},
		AgentText: `{"amux_task_result":{"status":"failed","summary":"s","changed_files":[{"path":"fake.go","change_type":"added"}]}}`,
	}
	res := AssembleResult(in, nil)
	if res.Status != ResultOK {
		t.Fatalf("envelope status must not override outcome: %s", res.Status)
	}
	if len(res.ChangedFiles) != 0 {
		t.Fatalf("changed_files must not come from agent text: %+v", res.ChangedFiles)
	}
	if res.Summary != "s" {
		t.Fatalf("summary should come from envelope: %q", res.Summary)
	}
}

func TestAssembleResultSuccessFromEnvelope(t *testing.T) {
	in := AssembleInput{
		Outcome:   TerminalOutcome{Category: OutcomeSuccess},
		AgentText: `{"amux_task_result":{"status":"ok","summary":"reviewed scheduler"}}`,
	}
	res := AssembleResult(in, nil)
	if res.Status != ResultOK || res.Summary != "reviewed scheduler" {
		t.Fatalf("got %+v", res)
	}
	if !res.HasContent() {
		t.Fatal("expected content")
	}
}

func TestAssembleResultChangedFilesGivesContent(t *testing.T) {
	in := AssembleInput{
		Outcome:      TerminalOutcome{Category: OutcomeSuccess},
		ChangedFiles: []ChangedFile{{Path: "x.go", ChangeType: ChangeModified}},
	}
	res := AssembleResult(in, nil)
	if !res.HasContent() {
		t.Fatal("changed files should count as content")
	}
}

func TestAssembleResultErrorSummaryFallback(t *testing.T) {
	in := AssembleInput{Outcome: TerminalOutcome{Category: OutcomeError, RawError: "exploded"}}
	res := AssembleResult(in, nil)
	if res.Status != ResultFailed || res.Summary != "exploded" {
		t.Fatalf("got %+v", res)
	}
}

func TestAssembleResultRedacts(t *testing.T) {
	in := AssembleInput{
		Outcome:   TerminalOutcome{Category: OutcomeSuccess},
		AgentText: `{"amux_task_result":{"summary":"key is sk-ant-0123456789abcdef"}}`,
	}
	res := AssembleResult(in, NewRedactor())
	if res.Summary != "key is [REDACTED]" {
		t.Fatalf("summary = %q", res.Summary)
	}
}

// TestAssembleResultSummaryDefaultNoTranscript: the default granularity leaves
// only the summary; full_transcript and details_ref stay empty.
func TestAssembleResultSummaryDefaultNoTranscript(t *testing.T) {
	in := AssembleInput{
		Outcome:   TerminalOutcome{Category: OutcomeSuccess},
		AgentText: `{"amux_task_result":{"summary":"did it"}}`,
		TaskID:    "task-1",
		// Granularity left zero => summary default.
	}
	res := AssembleResult(in, nil)
	if res.Summary != "did it" {
		t.Fatalf("summary = %q", res.Summary)
	}
	if res.FullTranscript != "" {
		t.Fatalf("summary mode must not inline transcript: %q", res.FullTranscript)
	}
	if res.DetailsRef != "" {
		t.Fatalf("summary mode must not set details_ref: %q", res.DetailsRef)
	}
}

// TestAssembleResultFullInlinesTranscript: granularity=full inlines the
// complete (redacted) agent transcript.
func TestAssembleResultFullInlinesTranscript(t *testing.T) {
	in := AssembleInput{
		Outcome:     TerminalOutcome{Category: OutcomeSuccess},
		AgentText:   "thinking...\ndoing work...\nfinal answer",
		Granularity: GranularityFull,
		TaskID:      "task-1",
	}
	res := AssembleResult(in, nil)
	if res.FullTranscript == "" {
		t.Fatal("full mode must inline transcript")
	}
	if !strings.Contains(res.FullTranscript, "final answer") {
		t.Fatalf("full_transcript missing content: %q", res.FullTranscript)
	}
	if res.DetailsRef != "" {
		t.Fatalf("full mode must not set details_ref: %q", res.DetailsRef)
	}
}

// TestAssembleResultFullTruncatesLargeTranscript: transcripts over the cap are
// truncated and carry a pointer to stream/logs for the full content.
func TestAssembleResultFullTruncatesLargeTranscript(t *testing.T) {
	big := strings.Repeat("abcdef\n", (MaxFullTranscriptBytes/7)+10)
	in := AssembleInput{
		Outcome:     TerminalOutcome{Category: OutcomeSuccess},
		AgentText:   big,
		Granularity: GranularityFull,
		TaskID:      "task-99",
	}
	res := AssembleResult(in, nil)
	if !strings.Contains(res.FullTranscript, "truncated") {
		preview := res.FullTranscript
		if len(preview) > 200 {
			preview = preview[:200]
		}
		t.Fatalf("expected truncation marker in full_transcript: %q (len=%d)", preview, len(res.FullTranscript))
	}
	if !strings.Contains(res.FullTranscript, "task-99") {
		t.Fatalf("truncation pointer must name the task_id: %q", res.FullTranscript)
	}
	if len(res.FullTranscript) > MaxFullTranscriptBytes+300 {
		t.Fatalf("full_transcript not truncated enough: len=%d", len(res.FullTranscript))
	}
}

// TestAssembleResultStructuredSummarySetsDetailsRef: structured-summary attaches
// details_ref=task_id and does not inline the transcript.
func TestAssembleResultStructuredSummarySetsDetailsRef(t *testing.T) {
	in := AssembleInput{
		Outcome:     TerminalOutcome{Category: OutcomeSuccess},
		AgentText:   `{"amux_task_result":{"summary":"quick result"}}`,
		Granularity: GranularityStructured,
		TaskID:      "task-42",
	}
	res := AssembleResult(in, nil)
	if res.Summary != "quick result" {
		t.Fatalf("summary = %q", res.Summary)
	}
	if res.DetailsRef != "task-42" {
		t.Fatalf("details_ref = %q, want task-42", res.DetailsRef)
	}
	if res.FullTranscript != "" {
		t.Fatalf("structured-summary must not inline transcript: %q", res.FullTranscript)
	}
}

// TestAssembleResultFullRedactsSecrets: the inlined transcript must be
// redacted before flowing back to the main agent.
func TestAssembleResultFullRedactsSecrets(t *testing.T) {
	in := AssembleInput{
		Outcome:     TerminalOutcome{Category: OutcomeSuccess},
		AgentText:   "the key is sk-ant-0123456789abcdef and done",
		Granularity: GranularityFull,
		TaskID:      "task-1",
	}
	res := AssembleResult(in, NewRedactor())
	if strings.Contains(res.FullTranscript, "sk-ant-0123456789abcdef") {
		t.Fatalf("full_transcript leaked secret: %q", res.FullTranscript)
	}
	if !strings.Contains(res.FullTranscript, "[REDACTED]") {
		t.Fatalf("full_transcript not redacted: %q", res.FullTranscript)
	}
}
