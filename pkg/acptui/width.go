package acptui

import "strings"

// RuneDisplayWidth returns the terminal display width of a rune.
func RuneDisplayWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case r >= 0x1100 && (r <= 0x115f ||
		r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1faff)):
		return 2
	default:
		return 1
	}
}

// DisplayWidth returns the terminal display width of a string.
func DisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += RuneDisplayWidth(r)
	}
	return w
}

// WrapPlainLine wraps a single line of text to fit within width columns.
func WrapPlainLine(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	remaining := []rune(text)
	if len(remaining) == 0 {
		return []string{""}
	}
	var out []string
	for len(remaining) > 0 {
		take, next := TakeRunesByWidth(remaining, width)
		out = append(out, string(take))
		remaining = remaining[next:]
	}
	return out
}

// WrapWithPrefix wraps text with a prefix on the first line and a
// continuation prefix on subsequent lines.
func WrapWithPrefix(prefix, text string, width int, continuationPrefix string) []string {
	if width < len(prefix)+1 {
		width = len(prefix) + 1
	}
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		if raw == "" {
			out = append(out, prefix)
			prefix = continuationPrefix
			continue
		}
		remaining := []rune(raw)
		currentPrefix := prefix
		first := true
		for len(remaining) > 0 {
			limit := width - DisplayWidth(currentPrefix)
			if limit < 1 {
				limit = 1
			}
			take, next := TakeRunesByWidth(remaining, limit)
			out = append(out, currentPrefix+string(take))
			remaining = remaining[next:]
			if len(remaining) > 0 {
				currentPrefix = continuationPrefix
				first = false
			}
		}
		if first {
			prefix = continuationPrefix
		}
	}
	if len(out) == 0 {
		return []string{prefix}
	}
	return out
}

// TakeRunesByWidth takes runes from the slice that fit within the given
// display width limit. Returns the taken runes and the count consumed.
func TakeRunesByWidth(runes []rune, limit int) ([]rune, int) {
	if limit <= 0 {
		return nil, 0
	}
	width := 0
	i := 0
	for i < len(runes) {
		w := RuneDisplayWidth(runes[i])
		if width+w > limit && i > 0 {
			break
		}
		if width+w > limit && i == 0 {
			i++
			break
		}
		width += w
		i++
	}
	return runes[:i], i
}

// TruncateToWidth truncates a string to fit within the given display width.
func TruncateToWidth(text string, width int) string {
	if width < 1 {
		return ""
	}
	var out strings.Builder
	w := 0
	for _, r := range text {
		rw := RuneDisplayWidth(r)
		if w+rw > width {
			break
		}
		out.WriteRune(r)
		w += rw
	}
	return out.String()
}
