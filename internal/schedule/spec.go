// Package schedule provides amuxd-side timed scheduling: at a given time it
// injects a prompt into an existing session. It owns its own SQLite store and a
// background tick loop. It is a hub-side capability and depends on the hub
// being able to route a "Submit" to the target runtime.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	cron "github.com/robfig/cron/v3"
)

// Kind classifies a parsed schedule spec.
const (
	KindInterval = "interval" // every <duration>
	KindDaily    = "daily"    // daily <HH:MM>
	KindOnce     = "once"     // at <when> or in <duration>
	KindCron     = "cron"     // cron <5-field expression or @descriptor>
)

// minInterval is the smallest accepted "every" duration.
const minInterval = time.Minute

// Spec is a parsed, normalized schedule specification.
type Spec struct {
	Raw  string // normalized text, e.g. "every 30m" or "at 2026-06-01T09:00:00+08:00"
	Kind string // one of the Kind* constants

	loc      *time.Location
	interval time.Duration // KindInterval
	daily    timeOfDay     // KindDaily
	once     time.Time     // KindOnce
	cron     cron.Schedule // KindCron
}

type timeOfDay struct{ hour, min int }

// cronParser accepts standard 5-field cron syntax plus descriptors (@hourly,
// @daily, @weekly, @monthly, @yearly, @every <duration>).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ParseLocation resolves an optional IANA timezone name (e.g. "Asia/Shanghai").
// Empty returns time.Local and "".
func ParseLocation(tz string) (*time.Location, string, error) {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return time.Local, "", nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, "", fmt.Errorf("invalid timezone %q: %w", tz, err)
	}
	return loc, tz, nil
}

// ParseSpec parses a schedule spec string in time.Local using time.Now().
func ParseSpec(s string) (Spec, error) {
	return ParseSpecInLocation(s, time.Local, time.Now())
}

// ParseSpecInLocation parses a schedule spec string evaluated in loc.
//
// Accepted forms (leading keyword is case-insensitive):
//
//	every <duration>     e.g. "every 30m", "every 2h", "every 1d", "every 1w" (min 1m)
//	daily <HH:MM>        e.g. "daily 09:00" (evaluated in loc)
//	at <when>            e.g. "at 2026-06-01T09:00:00+08:00" or "at 2026-06-01 09:00"
//	in <duration>        e.g. "in 10m", "in 2h", "in 1d" (one-shot normalized to "at <RFC3339>")
//	cron <expr>          e.g. "cron 0 9 * * 1-5" or "cron @daily" (evaluated in loc)
func ParseSpecInLocation(s string, loc *time.Location, now time.Time) (Spec, error) {
	if loc == nil {
		loc = time.Local
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return Spec{}, fmt.Errorf("empty schedule spec")
	}
	keyword, rest, _ := strings.Cut(trimmed, " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(keyword) {
	case "every":
		d, err := parseExtendedDuration(rest)
		if err != nil {
			return Spec{}, fmt.Errorf("invalid duration %q: %w", rest, err)
		}
		if d < minInterval {
			return Spec{}, fmt.Errorf("interval must be at least %s", minInterval)
		}
		return Spec{Raw: trimmed, Kind: KindInterval, loc: loc, interval: d}, nil
	case "daily":
		tod, err := parseTimeOfDay(rest)
		if err != nil {
			return Spec{}, err
		}
		return Spec{Raw: trimmed, Kind: KindDaily, loc: loc, daily: tod}, nil
	case "at":
		when, err := parseWhenInLocation(rest, loc)
		if err != nil {
			return Spec{}, err
		}
		return Spec{Raw: trimmed, Kind: KindOnce, loc: loc, once: when}, nil
	case "in":
		d, err := parseExtendedDuration(rest)
		if err != nil {
			return Spec{}, fmt.Errorf("invalid duration %q: %w", rest, err)
		}
		if d <= 0 {
			return Spec{}, fmt.Errorf("relative duration must be positive")
		}
		when := now.In(loc).Add(d).Truncate(time.Second)
		if !when.After(now) {
			when = now.In(loc).Add(time.Second).Truncate(time.Second)
		}
		return Spec{Raw: "at " + when.Format(time.RFC3339), Kind: KindOnce, loc: loc, once: when}, nil
	case "cron":
		sched, err := cronParser.Parse(rest)
		if err != nil {
			return Spec{}, fmt.Errorf("invalid cron %q: %w", rest, err)
		}
		return Spec{Raw: trimmed, Kind: KindCron, loc: loc, cron: sched}, nil
	default:
		return Spec{}, fmt.Errorf("unknown schedule spec %q (expected: every|daily|at|in|cron …)", trimmed)
	}
}

// parseExtendedDuration extends time.ParseDuration with day ("d" = 24h) and
// week ("w" = 7*24h) units, e.g. "1d", "2w", "1d12h".
func parseExtendedDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	var total time.Duration
	rem := s
	for len(rem) > 0 {
		i := 0
		for i < len(rem) && (rem[i] >= '0' && rem[i] <= '9') {
			i++
		}
		if i == 0 || i == len(rem) {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		n, err := strconv.ParseInt(rem[:i], 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		j := i
		for j < len(rem) && !(rem[j] >= '0' && rem[j] <= '9') {
			j++
		}
		unit := rem[i:j]
		switch unit {
		case "w":
			total += time.Duration(n) * 7 * 24 * time.Hour
		case "d":
			total += time.Duration(n) * 24 * time.Hour
		default:
			part, err := time.ParseDuration(rem[:j])
			if err != nil {
				return 0, err
			}
			total += part
		}
		rem = rem[j:]
	}
	return total, nil
}

func parseTimeOfDay(s string) (timeOfDay, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return timeOfDay{}, fmt.Errorf("invalid time-of-day %q (want HH:MM): %w", s, err)
	}
	return timeOfDay{hour: t.Hour(), min: t.Minute()}, nil
}

func parseWhenInLocation(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (want RFC3339 or \"2006-01-02 15:04\")", s)
}

// Recurring reports whether the spec fires more than once.
func (s Spec) Recurring() bool { return s.Kind != KindOnce }

func (s Spec) location(fallback *time.Location) *time.Location {
	if s.loc != nil {
		return s.loc
	}
	if fallback != nil {
		return fallback
	}
	return time.Local
}

// Next returns the first trigger time strictly after `after`. The bool is false
// when the spec has no further occurrences (a one-shot already in the past).
func (s Spec) Next(after time.Time) (time.Time, bool) {
	loc := s.location(after.Location())
	inLoc := after.In(loc)
	switch s.Kind {
	case KindInterval:
		return after.Add(s.interval), true
	case KindDaily:
		candidate := time.Date(inLoc.Year(), inLoc.Month(), inLoc.Day(), s.daily.hour, s.daily.min, 0, 0, loc)
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
		next := s.cron.Next(inLoc)
		if next.IsZero() {
			return time.Time{}, false
		}
		return next, true
	default:
		return time.Time{}, false
	}
}

// NextAfterScheduled computes the next occurrence strictly after `now` while
// anchoring interval specs to `scheduledAt` to avoid cumulative clock drift.
func (s Spec) NextAfterScheduled(scheduledAt, now time.Time) (time.Time, bool) {
	if s.Kind != KindInterval || scheduledAt.IsZero() || s.interval <= 0 {
		return s.Next(now)
	}
	elapsed := now.Sub(scheduledAt)
	if elapsed < 0 {
		return scheduledAt.Add(s.interval), true
	}
	steps := elapsed/s.interval + 1
	return scheduledAt.Add(steps * s.interval), true
}
