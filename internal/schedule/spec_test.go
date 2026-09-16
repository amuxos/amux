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
		{"EVERY 2h", KindInterval, true},
		{"every 30s", "", false}, // below minimum
		{"every", "", false},
		{"daily 09:00", KindDaily, true},
		{"daily 9:00", KindDaily, true},
		{"daily 25:00", "", false},
		{"at 2026-06-01 09:00", KindOnce, true},
		{"at 2026-06-01T09:00:00+08:00", KindOnce, true},
		{"at not-a-time", "", false},
		{"cron 0 9 * * 1-5", KindCron, true},
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
