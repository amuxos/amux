package task

import (
	"regexp"
	"strings"
)

// redactionMask replaces any detected secret material.
const redactionMask = "[REDACTED]"

// secretPatterns matches common credential shapes so result/transcript redaction
// is defence-in-depth even when the literal secret value is unknown.
var secretPatterns = []*regexp.Regexp{
	// Provider API keys (Anthropic sk-ant-..., OpenAI sk-...).
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}`),
	// "KEY=value" style env leaks for known secret-bearing names.
	regexp.MustCompile(`(?i)(ANTHROPIC_API_KEY|OPENAI_API_KEY|AWS_SECRET_ACCESS_KEY|GH_TOKEN|GITHUB_TOKEN)\s*[=:]\s*\S+`),
	// Bearer tokens.
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{16,}`),
}

// Redactor removes secret material from text before it flows back to the main
// agent. It redacts both explicitly known literal values (the secrets actually
// materialized for this task) and common credential patterns.
type Redactor struct {
	literals []string
}

// NewRedactor builds a redactor that masks the given literal secret values (in
// addition to the built-in patterns). Empty/short values are ignored.
func NewRedactor(literals ...string) *Redactor {
	r := &Redactor{}
	for _, s := range literals {
		if len(strings.TrimSpace(s)) >= 6 {
			r.literals = append(r.literals, s)
		}
	}
	return r
}

// String redacts a single string.
func (r *Redactor) String(s string) string {
	if s == "" {
		return s
	}
	for _, lit := range r.literals {
		if lit != "" {
			s = strings.ReplaceAll(s, lit, redactionMask)
		}
	}
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, redactionMask)
	}
	return s
}

// Bytes redacts a byte slice.
func (r *Redactor) Bytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	return []byte(r.String(string(b)))
}

// Result redacts the human-readable and reference fields of a TaskResult in
// place. Every agent-derived string that can flow back to the main agent is
// masked: summary, diagnostics, diff references and artifact URIs.
//
// details_ref and full_transcript are NOT redacted here:
//   - details_ref is a control-plane-generated task id reference (set by the
//     runner under structured-summary), not agent output. A task id
//     `task-<uuid>` contains an `sk-<uuid>` substring that the sk- secret
//     pattern would otherwise mangle into an unusable reference.
//   - full_transcript is already redacted at assembly time (AssembleResult
//     redacts the agent transcript before inlining it, then appends a
//     control-plane truncation pointer that names the task id). Redacting it
//     again here would mangle that truncation pointer's task id.
func (r *Redactor) Result(res *TaskResult) {
	if res == nil {
		return
	}
	res.Summary = r.String(res.Summary)
	// full_transcript and details_ref intentionally left unredacted (above).
	for i := range res.Diagnostics {
		res.Diagnostics[i].Message = r.String(res.Diagnostics[i].Message)
	}
	for i := range res.ChangedFiles {
		res.ChangedFiles[i].DiffRef = r.String(res.ChangedFiles[i].DiffRef)
	}
	for i := range res.Artifacts {
		res.Artifacts[i].URI = r.String(res.Artifacts[i].URI)
	}
}
