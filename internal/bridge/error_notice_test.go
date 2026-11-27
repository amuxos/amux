package bridge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBridgeErrorNoticeClaudeSessionLimitWithReset(t *testing.T) {
	t.Parallel()
	const providerText = "You've hit your session limit · resets 1:50am (Asia/Shanghai)"
	for _, session := range []string{"", "work-session"} {
		got := bridgeErrorNotice(session, providerText, true)
		const want = "⚠️ 模型账号已达到用量上限，将于 1:50am (Asia/Shanghai) 重置。请届时重试，或切换其他模型/Agent。"
		if got != want {
			t.Fatalf("bridgeErrorNotice(%q) = %q; want %q", session, got, want)
		}
		if strings.Contains(got, "session restart") || strings.Contains(got, "work-session") {
			t.Fatalf("usage limit notice must not suggest a session restart: %q", got)
		}
	}
}

func TestBridgeErrorNoticeUsageLimitWithoutReset(t *testing.T) {
	t.Parallel()
	const want = "⚠️ 模型账号已达到用量上限。请稍后重试，或切换其他模型/Agent。"
	for _, providerText := range []string{
		"You've hit your usage limit.",
		"Claude AI usage limit reached",
		"You have exceeded your weekly usage limit",
		`API Error: 429 {"type":"error","error":{"type":"rate_limit_error","message":"Number of requests has exceeded your rate limit"}}`,
		"HTTP 429 Too Many Requests",
		"stream error (code=usage_limit_reached)",
	} {
		got := bridgeErrorNotice("work-session", providerText, true)
		if got != want {
			t.Fatalf("bridgeErrorNotice(%q) = %q; want %q", providerText, got, want)
		}
	}
}

func TestBridgeErrorNoticeUsageLimitFromStructuredCode(t *testing.T) {
	t.Parallel()
	got := bridgeErrorNoticeWithCode("work-session", "something opaque happened", "rate_limit", true)
	if !strings.Contains(got, "用量上限") || strings.Contains(got, "session restart") {
		t.Fatalf("rate_limit code notice = %q", got)
	}
	ordinary := bridgeErrorNoticeWithCode("work-session", "something opaque happened", "result_error", true)
	if strings.Contains(ordinary, "用量上限") || !strings.Contains(ordinary, "Agent 执行出错") {
		t.Fatalf("result_error code was misclassified: %q", ordinary)
	}
}

func TestBridgeErrorNoticeUsageLimitResetIsBounded(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"You've hit your limit · resets 3pm (America/Los_Angeles)":        "3pm (America/Los_Angeles)",
		"You've hit your usage limit. Try again at 3:05 PM.":              "3:05 PM",
		"Usage limit reached, resets Oct 12, 9:00am UTC+8 for your plan":  "Oct 12, 9:00am UTC+8",
		"You've hit your weekly limit · resets 11:00 (Asia/Shanghai) now": "11:00 (Asia/Shanghai)",
	}
	for providerText, wantReset := range cases {
		if got := providerUsageLimitReset(providerText); got != wantReset {
			t.Fatalf("providerUsageLimitReset(%q) = %q; want %q", providerText, got, wantReset)
		}
	}
	// Anything that is not a well-formed time is dropped, never forwarded.
	for _, providerText := range []string{
		"You've hit your session limit · resets soon, see /home/alice/secret.txt",
		"You've hit your session limit · resets 1:50am (" + strings.Repeat("A", 80) + ")",
	} {
		got := bridgeErrorNotice("work-session", providerText, true)
		if strings.Contains(got, "/home/alice") || strings.Contains(got, strings.Repeat("A", 40)) || strings.Contains(got, "soon") {
			t.Fatalf("reset extraction leaked provider text: %q", got)
		}
		if !strings.Contains(got, "用量上限") {
			t.Fatalf("usage limit not recognized: %q", got)
		}
	}
}

func TestBridgeErrorNoticeGenericFallbackIncludesSanitizedSummary(t *testing.T) {
	t.Parallel()
	providerText := "Claude Code process exited\n  at /home/box/.amux/run/claude/proc.js:12\n" +
		"while reading /Users/alice/project/.env and /tmp/amux-123/sock with key sk-ant-api03-abcdefghijklmnop " +
		"Authorization: Bearer abc.def.ghi-jkl token=supersecretvalue " +
		"blob " + strings.Repeat("Zx9", 20) + " via https://api.example.com/v1/messages?key=leaky"
	got := bridgeErrorNotice("work-session", providerText, false)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("generic notice should have exactly one detail line: %q", got)
	}
	if lines[0] != "⚠️ Agent 执行出错，请稍后重试或 `amux session restart work-session`。" {
		t.Fatalf("generic notice prefix changed: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "详情：Claude Code process exited at <path>") {
		t.Fatalf("detail line = %q", lines[1])
	}
	for _, leaked := range []string{
		"/home/box", "/Users/alice", "/tmp/amux", ".env", "sk-ant", "abcdefghijklmnop",
		"abc.def.ghi", "supersecretvalue", "Zx9Zx9Zx9Zx9", "/v1/messages", "leaky",
	} {
		if strings.Contains(got, leaked) {
			t.Fatalf("generic notice leaked %q: %q", leaked, got)
		}
	}
	if !strings.Contains(lines[1], "<redacted>") || !strings.Contains(lines[1], "https://api.example.com/…") {
		t.Fatalf("detail line missing redaction markers: %q", lines[1])
	}
}

func TestBridgeErrorNoticeGenericFallbackTruncatesSummary(t *testing.T) {
	t.Parallel()
	providerText := strings.Repeat("错误 ", 300)
	got := bridgeErrorNotice("", providerText, false)
	_, detail, ok := strings.Cut(got, "\n详情：")
	if !ok {
		t.Fatalf("missing detail line: %q", got)
	}
	if strings.Contains(detail, "\n") {
		t.Fatalf("detail must be one line: %q", detail)
	}
	if n := utf8.RuneCountInString(detail); n > providerErrorSummaryMaxRunes+1 {
		t.Fatalf("detail has %d runes; want <= %d", n, providerErrorSummaryMaxRunes+1)
	}
	if !strings.HasSuffix(detail, "…") {
		t.Fatalf("truncated detail should end with an ellipsis: %q", detail)
	}
}

func TestBridgeErrorNoticeGenericFallbackSkipsEmptySummary(t *testing.T) {
	t.Parallel()
	for _, providerText := range []string{"", "  \n\t "} {
		if got := bridgeErrorNotice("work-session", providerText, false); got != "⚠️ Agent 执行出错，请稍后重试或 `amux session restart work-session`。" {
			t.Fatalf("bridgeErrorNotice(%q) = %q", providerText, got)
		}
		if got := bridgeErrorNotice("", providerText, false); got != "⚠️ Agent 执行出错，请稍后重试或新建话题。" {
			t.Fatalf("bridgeErrorNotice(%q) = %q", providerText, got)
		}
	}
}
