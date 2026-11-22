package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Kinds of schedule expression.
const (
	KindCron     = "cron"
	KindInterval = "interval"
	KindOnce     = "once"
)

// Plan is a parsed schedule expression bound to one timezone.
type Plan struct {
	Expr  string
	Kind  string
	Loc   *time.Location
	cron  cron.Schedule
	every time.Duration
	once  time.Time
}

// Parse accepts:
//
//	0 7 * * 1-5          standard five-field cron (names and @descriptors ok)
//	every 2h             recurring interval
//	30m / in 30m         one-shot after the duration
//	2026-11-23T07:00:00  one-shot at an ISO timestamp (in loc when no zone)
func Parse(expr string, loc *time.Location) (*Plan, error) {
	if loc == nil {
		loc = time.Local
	}
	raw := strings.TrimSpace(expr)
	if raw == "" {
		return nil, fmt.Errorf("schedule is empty")
	}
	lower := strings.ToLower(raw)
	now := time.Now().In(loc)

	if strings.HasPrefix(lower, "every ") {
		d, err := parseScheduleDuration(strings.TrimSpace(lower[len("every "):]))
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid interval %q", raw)
		}
		return &Plan{Expr: raw, Kind: KindInterval, Loc: loc, every: d}, nil
	}
	if strings.HasPrefix(lower, "in ") {
		d, err := parseScheduleDuration(strings.TrimSpace(lower[len("in "):]))
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid delay %q", raw)
		}
		return &Plan{Expr: raw, Kind: KindOnce, Loc: loc, once: now.Add(d)}, nil
	}
	if d, err := parseScheduleDuration(lower); err == nil && d > 0 && !strings.Contains(lower, " ") {
		return &Plan{Expr: raw, Kind: KindOnce, Loc: loc, once: now.Add(d)}, nil
	}
	if t, ok := parseTimestamp(raw, loc); ok {
		return &Plan{Expr: raw, Kind: KindOnce, Loc: loc, once: t}, nil
	}

	if len(strings.Fields(raw)) == 5 || strings.HasPrefix(raw, "@") {
		sched, err := cron.ParseStandard(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid cron expression %q: %w", raw, err)
		}
		return &Plan{Expr: raw, Kind: KindCron, Loc: loc, cron: sched}, nil
	}
	return nil, fmt.Errorf("schedule %q must be cron, \"every <duration>\", \"<duration>\", or an ISO timestamp", raw)
}

// Next returns the first firing strictly after t. It returns the zero time
// for a one-shot that has already passed. Callers should pass the scheduled
// time (not the completion time) to avoid interval drift.
func (p *Plan) Next(t time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	switch p.Kind {
	case KindCron:
		if p.cron == nil {
			return time.Time{}
		}
		return p.cron.Next(t.In(p.Loc))
	case KindInterval:
		if t.IsZero() {
			t = time.Now()
		}
		return t.Add(p.every)
	case KindOnce:
		if p.once.After(t) {
			return p.once
		}
		return time.Time{}
	}
	return time.Time{}
}

// OneShot reports whether the plan can fire at most once.
func (p *Plan) OneShot() bool { return p != nil && p.Kind == KindOnce }

// Once returns the one-shot instant (zero for recurring plans).
func (p *Plan) Once() time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.once
}

// Describe returns a short human-readable form for API responses.
func (p *Plan) Describe() string {
	if p == nil {
		return ""
	}
	switch p.Kind {
	case KindCron:
		return "cron " + p.Expr
	case KindInterval:
		return "every " + p.every.String()
	case KindOnce:
		return "once at " + p.once.Format(time.RFC3339)
	}
	return p.Expr
}

// parseTimestamp tries the usual ISO forms in loc.
func parseTimestamp(s string, loc *time.Location) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		var (
			t   time.Time
			err error
		)
		if strings.Contains(layout, "Z07:00") {
			t, err = time.Parse(layout, s)
		} else {
			t, err = time.ParseInLocation(layout, s, loc)
		}
		if err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseScheduleDuration extends time.ParseDuration with d (24h) and w (7d).
func parseScheduleDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	for s != "" {
		i := strings.IndexAny(s, "dw")
		if i < 0 {
			d, err := time.ParseDuration(s)
			if err != nil {
				return 0, err
			}
			return total + d, nil
		}
		j := i
		for j > 0 {
			c := s[j-1]
			if (c >= '0' && c <= '9') || c == '.' {
				j--
				continue
			}
			break
		}
		if j == i {
			return 0, fmt.Errorf("missing number before %q", s[i:i+1])
		}
		// Anything before the number is a normal duration prefix ("12h" in
		// "12h1d") and must be parsed too; the previous implementation
		// silently dropped it.
		if j > 0 {
			d, err := time.ParseDuration(s[:j])
			if err != nil {
				return 0, err
			}
			total += d
		}
		f, err := strconv.ParseFloat(s[j:i], 64)
		if err != nil || f < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		unit := 24 * time.Hour
		if s[i] == 'w' {
			unit = 7 * 24 * time.Hour
		}
		total += time.Duration(f * float64(unit))
		s = s[i+1:]
	}
	return total, nil
}
