package schedule

import (
	"testing"
	"time"
)

func TestParseSpecKinds(t *testing.T) {
	cases := []struct {
		in   string
		kind string
		ok   bool
	}{
		{"every 30m", KindInterval, true},
		{"every 1h30m", KindInterval, true},
		{"every 1d", KindInterval, true},
		{"every 2w", KindInterval, true},
		{"every 1d12h", KindInterval, true},
		{"EVERY 2h", KindInterval, true},
		{"every 30s", "", false}, // below minimum
		{"every", "", false},
		{"daily 09:00", KindDaily, true},
		{"daily 9:00", KindDaily, true},
		{"daily 25:00", "", false},
		{"at 2026-06-01 09:00", KindOnce, true},
		{"at 2026-06-01T09:00:00+08:00", KindOnce, true},
		{"at not-a-time", "", false},
		{"in 10m", KindOnce, true},
		{"in 2h", KindOnce, true},
		{"in 1d", KindOnce, true},
		{"in 0s", "", false},
		{"in -5m", "", false},
		{"cron 0 9 * * 1-5", KindCron, true},
		{"cron @daily", KindCron, true},
		{"cron @hourly", KindCron, true},
		{"cron bogus", "", false},
		{"nonsense", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		s, err := ParseSpec(c.in)
		if c.ok {
			if err != nil {
				t.Errorf("ParseSpec(%q) unexpected error: %v", c.in, err)
				continue
			}
			if s.Kind != c.kind {
				t.Errorf("ParseSpec(%q) kind = %q, want %q", c.in, s.Kind, c.kind)
			}
		} else if err == nil {
			t.Errorf("ParseSpec(%q) expected error, got kind %q", c.in, s.Kind)
		}
	}
}

func TestNextInterval(t *testing.T) {
	s, err := ParseSpec("every 30m")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	next, ok := s.Next(now)
	if !ok || !next.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("Next = %v, %v; want %v", next, ok, now.Add(30*time.Minute))
	}
	if !s.Recurring() {
		t.Error("interval should be recurring")
	}
}

func TestNextDaily(t *testing.T) {
	s, err := ParseSpec("daily 09:00")
	if err != nil {
		t.Fatal(err)
	}
	// Before today's 09:00 -> today 09:00.
	now := time.Date(2026, 6, 1, 8, 0, 0, 0, time.Local)
	next, ok := s.Next(now)
	want := time.Date(2026, 6, 1, 9, 0, 0, 0, time.Local)
	if !ok || !next.Equal(want) {
		t.Fatalf("Next(before) = %v; want %v", next, want)
	}
	// After today's 09:00 -> tomorrow 09:00.
	now = time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	next, _ = s.Next(now)
	want = time.Date(2026, 6, 2, 9, 0, 0, 0, time.Local)
	if !next.Equal(want) {
		t.Fatalf("Next(after) = %v; want %v", next, want)
	}
}

func TestNextOnce(t *testing.T) {
	s, err := ParseSpec("at 2026-06-01 09:00")
	if err != nil {
		t.Fatal(err)
	}
	if s.Recurring() {
		t.Error("once should not be recurring")
	}
	before := time.Date(2026, 6, 1, 8, 0, 0, 0, time.Local)
	if _, ok := s.Next(before); !ok {
		t.Error("future one-shot should have a next run")
	}
	after := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	if _, ok := s.Next(after); ok {
		t.Error("past one-shot should have no next run")
	}
}

func TestNextCron(t *testing.T) {
	s, err := ParseSpec("cron 0 9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	next, ok := s.Next(now)
	want := time.Date(2026, 6, 2, 9, 0, 0, 0, time.Local)
	if !ok || !next.Equal(want) {
		t.Fatalf("Next = %v; want %v", next, want)
	}
}

func TestParseSpecInLocationTimezoneAndRelativeIn(t *testing.T) {
	shanghai, name, err := ParseLocation("Asia/Shanghai")
	if err != nil || name != "Asia/Shanghai" {
		t.Fatalf("ParseLocation(Asia/Shanghai) = %v, %q, %v", shanghai, name, err)
	}
	if _, _, err := ParseLocation("Mars/Olympus"); err == nil {
		t.Fatal("expected error for invalid timezone")
	}

	// 2026-06-01 00:30 UTC is 2026-06-01 08:30 Asia/Shanghai -> daily 09:00 fires at 01:00 UTC.
	nowUTC := time.Date(2026, 6, 1, 0, 30, 0, 0, time.UTC)
	daily, err := ParseSpecInLocation("daily 09:00", shanghai, nowUTC)
	if err != nil {
		t.Fatal(err)
	}
	next, ok := daily.Next(nowUTC)
	wantUTC := time.Date(2026, 6, 1, 1, 0, 0, 0, time.UTC)
	if !ok || !next.Equal(wantUTC) {
		t.Fatalf("daily in Asia/Shanghai = %v, want %v", next.UTC(), wantUTC)
	}

	cronSpec, err := ParseSpecInLocation("cron 0 9 * * *", shanghai, nowUTC)
	if err != nil {
		t.Fatal(err)
	}
	cronNext, ok := cronSpec.Next(nowUTC)
	if !ok || !cronNext.Equal(wantUTC) {
		t.Fatalf("cron in Asia/Shanghai = %v, want %v", cronNext.UTC(), wantUTC)
	}

	// Relative "in 15m" normalizes to an RFC3339 "at ..." in loc.
	rel, err := ParseSpecInLocation("in 15m", shanghai, nowUTC)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Kind != KindOnce || rel.Raw != "at 2026-06-01T08:45:00+08:00" {
		t.Fatalf("in 15m normalized = %+v", rel)
	}
	relNext, ok := rel.Next(nowUTC)
	if !ok || !relNext.Equal(nowUTC.Add(15*time.Minute)) {
		t.Fatalf("in 15m next = %v %v", relNext, ok)
	}
}

func TestSpecIntervalNextAfterScheduledZeroDrift(t *testing.T) {
	s, err := ParseSpec("every 1m")
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	scheduled := anchor
	// Simulate 10 consecutive fires each delayed by 18s (like a 20s poll lag).
	for i := 1; i <= 10; i++ {
		now := scheduled.Add(18 * time.Second)
		next, ok := s.NextAfterScheduled(scheduled, now)
		want := anchor.Add(time.Duration(i) * time.Minute)
		if !ok || !next.Equal(want) {
			t.Fatalf("step %d: next = %v, want %v (no drift)", i, next, want)
		}
		scheduled = next
	}
	// If downtime skipped 3.5 intervals, jumps to the next grid slot strictly after now.
	afterDowntime, ok := s.NextAfterScheduled(anchor, anchor.Add(3*time.Minute+30*time.Second))
	want := anchor.Add(4 * time.Minute)
	if !ok || !afterDowntime.Equal(want) {
		t.Fatalf("after downtime: next = %v, want %v", afterDowntime, want)
	}
}
