package modelservice

import "testing"

func TestSessionQuotaBorrowedLifecycle(t *testing.T) {
	const sid = "test-session-quota-1"
	ClearSessionQuotaBorrowed(sid)
	if IsSessionQuotaBorrowed(sid) {
		t.Fatal("fresh session should not be marked borrowed")
	}
	SetSessionQuotaBorrowed(sid, true)
	if !IsSessionQuotaBorrowed(sid) {
		t.Fatal("expected borrowed=true after SetSessionQuotaBorrowed(true)")
	}
	SetSessionQuotaBorrowed(sid, false)
	if IsSessionQuotaBorrowed(sid) {
		t.Fatal("expected borrowed=false after SetSessionQuotaBorrowed(false)")
	}
}
