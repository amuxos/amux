package modelservice

import (
	"strings"
	"sync"
	"time"
)

const (
	maxQuotaSourceEntries = 4096
	quotaSourceTTL        = 24 * time.Hour
)

type quotaSourceEntry struct {
	borrowed  bool
	updatedAt time.Time
}

var quotaSourceState = struct {
	sync.Mutex
	entries map[string]quotaSourceEntry
}{
	entries: make(map[string]quotaSourceEntry),
}

// SetSessionQuotaBorrowed records whether the most recent upstream model call
// for sessionID was fulfilled by a shared quota pool rather than the caller's
// own account.
func SetSessionQuotaBorrowed(sessionID string, borrowed bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	now := time.Now()
	quotaSourceState.Lock()
	defer quotaSourceState.Unlock()
	if !borrowed {
		delete(quotaSourceState.entries, sessionID)
		return
	}
	if len(quotaSourceState.entries) >= maxQuotaSourceEntries {
		cutoff := now.Add(-quotaSourceTTL)
		for k, v := range quotaSourceState.entries {
			if v.updatedAt.Before(cutoff) {
				delete(quotaSourceState.entries, k)
			}
		}
		if len(quotaSourceState.entries) >= maxQuotaSourceEntries {
			var oldestKey string
			var oldestTime time.Time
			for k, v := range quotaSourceState.entries {
				if oldestKey == "" || v.updatedAt.Before(oldestTime) {
					oldestKey, oldestTime = k, v.updatedAt
				}
			}
			if oldestKey != "" {
				delete(quotaSourceState.entries, oldestKey)
			}
		}
	}
	quotaSourceState.entries[sessionID] = quotaSourceEntry{borrowed: true, updatedAt: now}
}

// IsSessionQuotaBorrowed reports whether sessionID is currently using shared
// pool quota.
func IsSessionQuotaBorrowed(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	quotaSourceState.Lock()
	defer quotaSourceState.Unlock()
	entry, ok := quotaSourceState.entries[sessionID]
	if !ok {
		return false
	}
	if time.Since(entry.updatedAt) > quotaSourceTTL {
		delete(quotaSourceState.entries, sessionID)
		return false
	}
	return entry.borrowed
}

// ClearSessionQuotaBorrowed removes any recorded quota attribution for sessionID.
func ClearSessionQuotaBorrowed(sessionID string) {
	SetSessionQuotaBorrowed(sessionID, false)
}
