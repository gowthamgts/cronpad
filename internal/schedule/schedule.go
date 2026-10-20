// Package schedule parses deterministic calendar and elapsed-time schedules.
package schedule

import (
	"context"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/jonboulle/clockwork"
	"github.com/robfig/cron/v3"

	"github.com/gowthamgts/cronpad/internal/schedule/nl"
)

// Kind identifies an expression grammar.
type Kind string

const (
	Auto    Kind = "auto"
	Cron    Kind = "cron"
	Quartz  Kind = "quartz"
	RRule   Kind = "rrule"
	Natural Kind = "natural"
	Once    Kind = "once"
)

// Schedule is immutable and safe to query concurrently. Next is strictly after its argument.
type Schedule interface {
	Next(after time.Time) (time.Time, bool)
	Canonical() string
	Kind() Kind
	Describe() string
}

// Spec describes a schedule and optional inclusive start/end bounds and global occurrence cap.
type Spec struct {
	Kind             Kind   `json:"kind"`
	Expr             string `json:"expr"`
	Timezone         string `json:"timezone"`
	StartsAt, EndsAt *time.Time
	Count            *int
}

// Error identifies the offending token by byte offset and length.
type Error struct {
	Kind       Kind   `json:"kind"`
	Position   int    `json:"position"`
	Length     int    `json:"length"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s at byte %d: %s", e.Kind, e.Position, e.Message)
}
func invalid(k Kind, pos, length int, message string) error {
	return &Error{Kind: k, Position: pos, Length: length, Message: message}
}

// Parse uses a real clock for expressions relative to creation time. Call ParseWithClock in components and tests.
func Parse(spec Spec) (Schedule, error) { return ParseWithClock(spec, clockwork.NewRealClock()) }

// ParseWithClock parses using an injected creation clock; StartsAt overrides the recurrence anchor.
func ParseWithClock(spec Spec, clk clockwork.Clock) (Schedule, error) {
	return ParseContext(context.Background(), spec, clk)
}

// ParseContext additionally cooperates with a request's compute deadline.
func ParseContext(ctx context.Context, spec Spec, clk clockwork.Clock) (Schedule, error) {
	if err := ctx.Err(); err != nil {
		return nil, invalid(spec.Kind, 0, 0, "schedule computation budget exceeded")
	}

	kind := spec.Kind
	if kind == "" || kind == Auto {
		kind = detect(spec.Expr)
	}
	if len(spec.Expr) > 4096 {
		return nil, invalid(kind, 4096, 0, "expression exceeds 4096 bytes")
	}
	if strings.TrimSpace(spec.Expr) == "" {
		return nil, invalid(kind, 0, len(spec.Expr), "expression is required")
	}
	zone := spec.Timezone
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, invalid(kind, 0, 0, "unknown IANA timezone: "+zone)
	}
	now := clk.Now().UTC()
	anchor := now
	if spec.StartsAt != nil {
		anchor = spec.StartsAt.UTC()
	}
	if spec.Count != nil && (*spec.Count < 1 || *spec.Count > 100000) {
		return nil, invalid(kind, 0, 0, "count must be between 1 and 100000")
	}
	if spec.StartsAt != nil && spec.EndsAt != nil && spec.EndsAt.Before(*spec.StartsAt) {
		return nil, invalid(kind, 0, 0, "ends_at is before starts_at")
	}
	var next func(time.Time) (time.Time, bool)
	canonical := strings.TrimSpace(spec.Expr)
	description := ""
	var countRules []string
	var intrinsicEnd *time.Time
	intrinsicCount := 0
	switch kind {
	case Once:
		at, e := time.Parse(time.RFC3339Nano, canonical)
		if e != nil {
			return nil, invalid(kind, 0, len(canonical), "expected an RFC3339 timestamp")
		}
		next = one(at)
		canonical = at.UTC().Format(time.RFC3339Nano)
		description = "Once at " + at.In(loc).Format("2006-01-02 15:04:05 MST")
	case Cron:
		parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
		if strings.HasPrefix(canonical, "TZ=") || strings.HasPrefix(canonical, "CRON_TZ=") {
			return nil, invalid(kind, 0, strings.Index(canonical, " "), "set the timezone field instead of an embedded timezone")
		}
		// robfig accepts empty comma-list components, including an entirely empty
		// seconds mask; Next then scans every second for five years. Reject malformed lists.
		for i, field := range strings.Fields(canonical) {
			if strings.Contains(field, ",") {
				for _, part := range strings.Split(field, ",") {
					if part == "" {
						return nil, invalid(kind, fieldPosition(spec.Expr, i), len(field), "cron list contains an empty item")
					}
				}
			}
		}
		base, e := parser.Parse(canonical)
		if e != nil {
			return nil, cronError(kind, canonical, e)
		}
		if fields, ok := base.(*cron.SpecSchedule); ok {
			for i, mask := range []uint64{fields.Second, fields.Minute, fields.Hour, fields.Dom, fields.Month, fields.Dow} {
				if mask == 0 {
					index := i
					if len(strings.Fields(canonical)) == 5 {
						index--
					}
					return nil, invalid(kind, fieldPosition(spec.Expr, max(0, index)), 0, "cron field has no possible value")
				}
			}
		}
		if delay, ok := base.(cron.ConstantDelaySchedule); ok {
			if delay.Delay <= 0 {
				return nil, invalid(kind, 0, len(canonical), "interval must be positive")
			}
			duration, e := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(canonical, "@every")))
			if e != nil || duration <= 0 {
				return nil, invalid(kind, 0, len(canonical), "interval must be positive")
			}
			next = intervalNext(anchor, duration)
			description = "Every " + duration.String()
		} else {
			next = civilNext(func(after time.Time) (time.Time, bool) { t := base.Next(after); return t, !t.IsZero() }, loc)
			description = describeCron(canonical)
		}
		canonical = strings.Join(strings.Fields(canonical), " ")
	case Quartz:
		q, e := parseQuartz(canonical)
		if e != nil {
			return nil, e
		}
		next = civilNext(q.next, loc)
		canonical = strings.Join(strings.Fields(strings.ToUpper(canonical)), " ")
		description = q.describe()
	case RRule:
		countRules = []string{canonical}
		next, canonical, description, intrinsicCount, intrinsicEnd, err = parseRule(canonical, anchor, loc)
		if err != nil {
			return nil, err
		}
	case Natural:
		result, e := nl.Parse(spec.Expr, anchor.In(loc))
		if e != nil {
			return nil, &Error{Kind: kind, Position: e.Position, Length: e.Length, Message: e.Message, Suggestion: e.Suggestion}
		}
		canonical = result.Canonical
		description = result.Description
		intrinsicCount = result.Count
		intrinsicEnd = result.Until
		if result.Once != nil {
			next = one(*result.Once)
			canonical = result.Once.UTC().Format(time.RFC3339Nano)
		} else if result.OnceWall != nil {
			at := resolve(*result.OnceWall, loc)
			next = one(at)
			canonical = at.Format(time.RFC3339Nano)
		} else if result.Interval > 0 {
			next = intervalNext(anchor, result.Interval)
		} else {
			countRules = result.Rules
			var schedules []func(time.Time) (time.Time, bool)
			for _, rule := range result.Rules {
				f, _, _, _, _, e := parseRule(rule, anchor, loc)
				if e != nil {
					return nil, e
				}
				schedules = append(schedules, f)
			}
			next = func(after time.Time) (time.Time, bool) {
				var best time.Time
				for _, f := range schedules {
					if t, ok := f(after); ok && (best.IsZero() || t.Before(best)) {
						best = t
					}
				}
				return best, !best.IsZero()
			}
		}
	default:
		return nil, invalid(kind, 0, 0, "unknown schedule kind")
	}
	s := &parsed{kind: kind, canonical: canonical, description: description, next: next, ctx: ctx, start: spec.StartsAt, end: spec.EndsAt}
	if intrinsicEnd != nil && (s.end == nil || intrinsicEnd.Before(*s.end)) {
		s.end = intrinsicEnd
	}
	count := intrinsicCount
	if spec.Count != nil && (count == 0 || *spec.Count < count) {
		count = *spec.Count
	}
	if count > 0 {
		// DECISION: counts are global from creation/StartsAt, never reset by a preview's after.
		// Precompute the last allowed occurrence once; Next stays stateless and concurrency-safe.
		cursor := anchor
		if spec.StartsAt != nil {
			cursor = anchor.Add(-time.Nanosecond)
		} else if kind == RRule && intrinsicCount > 0 {
			cursor = anchor.Truncate(time.Second).Add(-time.Nanosecond)
		}
		if last, ok := simpleCountLast(ctx, countRules, anchor, cursor, loc, count); ok {
			s.last = last
			s.counted = true
			return s, nil
		}
		countNext := s.nextBounded
		if len(countRules) > 0 {
			iterator := countIterator(countRules, anchor, loc)
			countNext = func(after time.Time) (time.Time, bool) {
				if ctx.Err() != nil {
					return time.Time{}, false
				}
				for {
					t, ok := iterator()
					if !ok {
						return time.Time{}, false
					}
					if t.After(after) && (s.start == nil || !t.Before(*s.start)) {
						if s.end != nil && t.After(*s.end) {
							return time.Time{}, false
						}
						return t, true
					}
				}
			}
		}
		for range count {
			if ctx.Err() != nil {
				return nil, invalid(kind, 0, 0, "schedule computation budget exceeded")
			}
			t, ok := countNext(cursor)
			if !ok {
				break
			}
			s.last = t
			cursor = t
		}
		s.counted = true
	}
	return s, nil
}

type parsed struct {
	ctx                    context.Context
	kind                   Kind
	canonical, description string
	next                   func(time.Time) (time.Time, bool)
	start, end             *time.Time
	counted                bool
	last                   time.Time
}

func (s *parsed) Canonical() string { return s.canonical }
func (s *parsed) Kind() Kind        { return s.kind }
func (s *parsed) Describe() string  { return s.description }
func (s *parsed) nextBounded(after time.Time) (time.Time, bool) {
	if s.ctx.Err() != nil {
		return time.Time{}, false
	}
	if s.start != nil && after.Before(*s.start) {
		after = s.start.Add(-time.Nanosecond)
	}
	t, ok := s.next(after)
	if !ok || !t.After(after) || (s.end != nil && t.After(*s.end)) {
		return time.Time{}, false
	}
	return t.UTC(), true
}
func (s *parsed) Next(after time.Time) (time.Time, bool) {
	t, ok := s.nextBounded(after)
	if !ok || (s.counted && (s.last.IsZero() || t.After(s.last))) {
		return time.Time{}, false
	}
	return t, true
}
func one(at time.Time) func(time.Time) (time.Time, bool) {
	return func(after time.Time) (time.Time, bool) { return at.UTC(), at.After(after) }
}
func intervalNext(anchor time.Time, d time.Duration) func(time.Time) (time.Time, bool) {
	return func(after time.Time) (time.Time, bool) {
		if after.Before(anchor) {
			return anchor.UTC(), true
		}
		n := after.Sub(anchor)/d + 1
		if n <= 0 || n > time.Duration(1<<63-1)/d {
			return time.Time{}, false
		}
		t := anchor.Add(n * d)
		return t.UTC(), t.After(after)
	}
}

// Upcoming returns up to n strictly increasing UTC occurrences. Exhaustion returns a non-nil empty slice.
func Upcoming(s Schedule, after time.Time, n int) []time.Time {
	out := make([]time.Time, 0)
	for i := 0; i < n; i++ {
		t, ok := s.Next(after)
		if !ok || !t.After(after) {
			break
		}
		out = append(out, t)
		after = t
	}
	return out
}

// MinGap measures the smallest gap in samples+1 successive occurrences; zero means fewer than two.
// DECISION: this is a finite preview check, not a proof about all future calendar transitions.
func MinGap(s Schedule, after time.Time, samples int) time.Duration {
	var minimum time.Duration
	times := Upcoming(s, after, samples+1)
	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		if minimum == 0 || gap < minimum {
			minimum = gap
		}
	}
	return minimum
}
func detect(expr string) Kind {
	s := strings.TrimSpace(expr)
	if _, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return Once
	}
	fields := strings.Fields(s)
	if strings.Contains(strings.ToUpper(s), "FREQ=") {
		return RRule
	}
	if len(fields) >= 5 && len(fields) <= 7 && cronLeadingField(fields[0]) {
		special := len(fields) == 7
		if len(fields) == 6 {
			for _, index := range []int{3, 5} {
				f := strings.ToUpper(fields[index])
				if strings.ContainsAny(f, "?#") || f == "L" || f == "LW" || strings.HasPrefix(f, "L-") || strings.HasSuffix(f, "W") || strings.HasSuffix(f, "L") {
					special = true
				}
			}
		}
		if special {
			return Quartz
		}
		return Cron
	}

	if strings.HasPrefix(s, "@") {
		return Cron
	}
	return Natural
}
func cronLeadingField(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && !strings.ContainsRune("*,-/?", r) {
			return false
		}
	}
	return s != ""
}
func cronError(k Kind, expr string, cause error) error {
	fields := strings.Fields(expr)
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if len(fields) == 5 || len(fields) == 6 {
		for index, token := range fields {
			trial := make([]string, len(fields))
			for i := range trial {
				trial[i] = "*"
			}
			trial[index] = token
			if _, err := parser.Parse(strings.Join(trial, " ")); err != nil {
				return invalid(k, fieldPosition(expr, index), len(token), cause.Error())
			}
		}
	}
	return invalid(k, 0, len(expr), cause.Error())
}
