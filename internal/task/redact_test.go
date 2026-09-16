package task

import "testing"

func TestRedactorLiterals(t *testing.T) {
	r := NewRedactor("supersecretvalue", "short") // "short" is < 6 chars and ignored
	got := r.String("token=supersecretvalue and short stays")
	if got != "token=[REDACTED] and short stays" {
		t.Fatalf("got %q", got)
	}
}

func TestRedactorPatterns(t *testing.T) {
	r := NewRedactor()
	cases := map[string]string{
		"my key sk-ant-0123456789abcdef here":      "my key [REDACTED] here",
		"ANTHROPIC_API_KEY=abc123def456ghi":        "[REDACTED]",
		"Authorization: Bearer abcdef0123456789xy": "Authorization: [REDACTED]",
	}
	for in, want := range cases {
		if got := r.String(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactorResult(t *testing.T) {
	r := NewRedactor("topsecret123")
	res := &TaskResult{
		Summary:     "the value topsecret123 leaked",
		Diagnostics: []Diagnostic{{Severity: SeverityWarning, Message: "saw topsecret123"}},
	}
	r.Result(res)
	if res.Summary != "the value [REDACTED] leaked" {
		t.Fatalf("summary = %q", res.Summary)
	}
	if res.Diagnostics[0].Message != "saw [REDACTED]" {
		t.Fatalf("diag = %q", res.Diagnostics[0].Message)
	}
}
