package bridge

import (
	"github.com/amuxos/amux/internal/i18n"
	"strings"
)

// Session cards retain native per-viewer translations throughout rendering.
// Optional locale arguments keep non-card callers on their historical Chinese
// default; no process-global preference or user-content lookup is involved.
func sessionLocale(locales []i18n.Locale) i18n.Locale {
	if len(locales) > 0 {
		return locales[0]
	}
	return i18n.Chinese
}

func sessionCopy(locales []i18n.Locale, key string, args ...any) string {
	return managementCatalog.Format(sessionLocale(locales), "session."+key, args...)
}

func sessionMessage(key string, args ...any) i18n.Text {
	return managementCatalog.Text("session."+key, args...)
}

func sessionSubtaskStatus(status string, locales []i18n.Locale) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done", "success", "succeeded":
		return sessionCopy(locales, "status.done")
	case "failed", "error":
		return sessionCopy(locales, "status.failed")
	case "cancelled", "canceled", "stopped":
		return sessionCopy(locales, "status.stopped")
	case "timed_out", "timeout":
		return sessionCopy(locales, "subtasks.timeout")
	case "running", "in_progress":
		return sessionCopy(locales, "status.running")
	case "waiting_permission":
		return sessionCopy(locales, "status.waiting")
	default:
		return status
	}
}
