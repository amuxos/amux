package bridge

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// providerErrorCodeRateLimit is the structured error code a provider sidecar
// attaches when the provider reported a usage or rate limit.
const providerErrorCodeRateLimit = "rate_limit"

// Usage/rate-limit wording seen from Claude, Codex-compatible providers and
// generic HTTP 429 responses. Matching is classification only; the provider
// text itself is never forwarded.
var providerUsageLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bhit\s+(?:your|the)\s+(?:(?:session|usage|weekly|daily|monthly|hourly|5-hour|opus|sonnet)\s+)?limit\b`),
	regexp.MustCompile(`(?i)\busage\s+limits?\s+(?:has\s+been\s+|have\s+been\s+|was\s+|is\s+)?(?:reached|exceeded|hit)\b`),
	regexp.MustCompile(`(?i)\b(?:reached|exceeded)\s+(?:your|the)\s+(?:(?:session|weekly|daily|monthly|hourly)\s+)?usage\s+limits?\b`),
	regexp.MustCompile(`(?i)\brate[\s_-]?limit(?:ed|s)?\b`),
	regexp.MustCompile(`(?i)\btoo\s+many\s+requests\b`),
	regexp.MustCompile(`(?i)\b(?:status|http|error|code)(?:\s+code)?[\s:=]*429\b`),
	regexp.MustCompile(`(?i)\bcode[=:"'\s]+(?:usage_limit_reached|usage_limit_exceeded)\b`),
}

// providerUsageLimitResetPattern extracts only a short, well-formed reset
// time such as "resets 1:50am (Asia/Shanghai)" or "try again at 3:05 PM".
// Every component is length-bounded so arbitrary provider text cannot be
// smuggled through.
var providerUsageLimitResetPattern = regexp.MustCompile(
	`(?i)\b(?:resets?(?:\s+at)?|try\s+again\s+(?:at|after))\s+` +
		`((?:jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]{0,6}\.?\s+[0-9]{1,2}(?:st|nd|rd|th)?,?\s+(?:at\s+)?)?` +
		`([0-9]{1,2}:[0-9]{2}(?:\s?(?:[ap]m|[ap]\.m\.))?|[0-9]{1,2}\s?(?:[ap]m|[ap]\.m\.))` +
		`(?:\s*\(([A-Za-z][A-Za-z0-9_+-]{0,30}(?:/[A-Za-z0-9_+-]{1,30}){0,2})\)|\s+((?:UTC|GMT)(?:[+-][0-9]{1,2}(?::?[0-9]{2})?)?)\b)?`,
)

func providerUsageLimited(text, code string) bool {
	if strings.EqualFold(strings.TrimSpace(code), providerErrorCodeRateLimit) {
		return true
	}
	for _, pattern := range providerUsageLimitPatterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

func providerUsageLimitReset(text string) string {
	m := providerUsageLimitResetPattern.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if date := strings.Join(strings.Fields(m[1]), " "); date != "" {
		parts = append(parts, date)
	}
	parts = append(parts, strings.Join(strings.Fields(m[2]), " "))
	switch {
	case m[3] != "":
		parts = append(parts, "("+m[3]+")")
	case m[4] != "":
		parts = append(parts, m[4])
	}
	return strings.Join(parts, " ")
}

func providerUsageLimitNotice(text string) string {
	if reset := providerUsageLimitReset(text); reset != "" {
		return fmt.Sprintf("⚠️ 模型账号已达到用量上限，将于 %s 重置。请届时重试，或切换其他模型/Agent。", reset)
	}
	return "⚠️ 模型账号已达到用量上限。请稍后重试，或切换其他模型/Agent。"
}

const providerErrorSummaryMaxRunes = 200

var (
	errorSummaryURLPattern      = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]{1,15}://[^\s/"'\x60<>?#]+)[^\s"'\x60<>]*`)
	errorSummaryWinPathPattern  = regexp.MustCompile(`\b[A-Za-z]:\\[^\s"'\x60<>]*`)
	errorSummaryPathPattern     = regexp.MustCompile(`(^|[^A-Za-z0-9_.:/~<-])(?:~|/[A-Za-z0-9_.@+-]+)(?:/[^\s"'\x60<>]*)+`)
	errorSummaryBearerPattern   = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{6,}`)
	errorSummaryKeyValuePattern = regexp.MustCompile(`(?i)\b((?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password|passwd|authorization|cookie)["']?\s*[:=]\s*)["']?[^\s"',;}]+`)
	errorSummaryPrefixedKey     = regexp.MustCompile(`(?i)\b(?:sk|pk|rk|ak|ghp|gho|ghs|xox[abprs])[-_][A-Za-z0-9_-]{8,}`)
	errorSummaryLongRunPattern  = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
)

// sanitizeProviderErrorSummary reduces provider error text to one short line
// with filesystem paths, URLs paths/queries and token-like strings redacted.
func sanitizeProviderErrorSummary(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if s == "" {
		return ""
	}
	s = errorSummaryURLPattern.ReplaceAllString(s, "$1/…")
	s = errorSummaryWinPathPattern.ReplaceAllString(s, "<path>")
	s = errorSummaryPathPattern.ReplaceAllString(s, "$1<path>")
	s = errorSummaryBearerPattern.ReplaceAllString(s, "$1 <redacted>")
	s = errorSummaryKeyValuePattern.ReplaceAllString(s, "$1<redacted>")
	s = errorSummaryPrefixedKey.ReplaceAllString(s, "<redacted>")
	s = errorSummaryLongRunPattern.ReplaceAllString(s, "<redacted>")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > providerErrorSummaryMaxRunes {
		runes := []rune(s)
		s = strings.TrimSpace(string(runes[:providerErrorSummaryMaxRunes])) + "…"
	}
	return s
}

func withProviderErrorSummary(notice, providerError string) string {
	summary := sanitizeProviderErrorSummary(providerError)
	if summary == "" {
		return notice
	}
	return notice + "\n详情：" + summary
}
