// Package schedule provides amuxd-side timed scheduling: at a given time it
// injects a prompt into an existing session. It owns its own SQLite store and a
// background tick loop. It is a hub-side capability and depends on the hub
// being able to route a "Submit" to the target runtime.
package schedule

import (
	"fmt"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
)

// Kind classifies a parsed schedule spec.
const (
	KindInterval = "interval" // every <duration>
	KindDaily    = "daily"    // daily <HH:MM>
	KindOnce     = "once"     // at <when>
	KindCron     = "cron"     // cron <5-field expression>
)

// minInterval is the smallest accepted "every" duration. The tick loop runs at
// a coarser cadence than seconds, so sub-minute intervals are not meaningful.
const minInterval = time.Minute

// Spec is a parsed, normalized schedule specification.
type Spec struct {
	Raw  string // original text, e.g. "every 30m"
	Kind string // one of the Kind* constants

	interval time.Duration // KindInterval
	daily    timeOfDay     // KindDaily
	once     time.Time     // KindOnce (local)
	cron     cron.Schedule // KindCron
}

type timeOfDay struct{ hour, min int }

// cronParser accepts the standard 5-field cron syntax (no seconds field).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ParseSpec parses a schedule spec string into a Spec.
//
// Accepted forms (leading keyword is case-insensitive):
//
//	every <duration>     e.g. "every 30m", "every 2h", "every 1h30m" (min 1m)
//	daily <HH:MM>        e.g. "daily 09:00" (server local time)
//	at <when>            e.g. "at 2026-06-01T09:00:00+08:00" or "at 2026-06-01 09:00"
//	cron <expr>          e.g. "cron 0 9 * * 1-5" (standard 5-field, local time)
func ParseSpec(s string) (Spec, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return Spec{}, fmt.Errorf("empty schedule spec")
	}
	keyword, rest, _ := strings.Cut(trimmed, " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(keyword) {
	case "every":
		d, err := time.ParseDuration(rest)
		if err != nil {
			return Spec{}, fmt.Errorf("invalid duration %q: %w", rest, err)
		}
		if d < minInterval {
			return Spec{}, fmt.Errorf("interval must be at least %s", minInterval)
		}
		return Spec{Raw: trimmed, Kind: KindInterval, interval: d}, nil
	case "daily":
		tod, err := parseTimeOfDay(rest)
		if err != nil {
			return Spec{}, err
		}
		return Spec{Raw: trimmed, Kind: KindDaily, daily: tod}, nil
	case "at":
		when, err := parseWhen(rest)
		if err != nil {
			return Spec{}, err
		}
		return Spec{Raw: trimmed, Kind: KindOnce, once: when}, nil
	case "cron":
		sched, err := cronParser.Parse(rest)
		if err != nil {
			return Spec{}, fmt.Errorf("invalid cron %q: %w", rest, err)
		}
		return Spec{Raw: trimmed, Kind: KindCron, cron: sched}, nil
	default:
		return Spec{}, fmt.Errorf("unknown schedule spec %q (expected: every|daily|at|cron …)", trimmed)
	}
}

func parseTimeOfDay(s string) (timeOfDay, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return timeOfDay{}, fmt.Errorf("invalid time-of-day %q (want HH:MM): %w", s, err)
	}
	return timeOfDay{hour: t.Hour(), min: t.Minute()}, nil
}

func parseWhen(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (want RFC3339 or \"2006-01-02 15:04\")", s)
}

// Recurring reports whether the spec fires more than once.
func (s Spec) Recurring() bool { return s.Kind != KindOnce }

// Next returns the first trigger time strictly after `after`. The bool is false
// when the spec has no further occurrences (a one-shot already in the past).
func (s Spec) Next(after time.Time) (time.Time, bool) {
	switch s.Kind {
	case KindInterval:
		return after.Add(s.interval), true
	case KindDaily:
		candidate := time.Date(after.Year(), after.Month(), after.Day(), s.daily.hour, s.daily.min, 0, 0, after.Location())
		if !candidate.After(after) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate, true
	case KindOnce:
		if s.once.After(after) {
			return s.once, true
		}
		return time.Time{}, false
	case KindCron:
		next := s.cron.Next(after)
		if next.IsZero() {
			return time.Time{}, false
		}
		return next, true
	default:
		return time.Time{}, false
	}
}
