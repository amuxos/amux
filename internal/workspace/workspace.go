package workspace

import (
	"path/filepath"
	"strings"
	"unicode"
)

// DefaultDir returns the per-session workspace directory under amuxDir.
func DefaultDir(amuxDir, source, scope, sessionName string) string {
	return filepath.Join(amuxDir, "workspaces", sanitize(source), sanitize(scope), sanitize(sessionName))
}

func sanitize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune('_')
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}
