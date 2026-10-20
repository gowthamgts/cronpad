package schedule

import (
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// countIterator reuses recurrence state during finite-count construction instead of
// rebuilding and seeking a new RRULE for every occurrence (repeated allocation/work).
// The iterator is constructor-local; published schedules remain immutable.
func countIterator(rules []string, anchor time.Time, loc *time.Location) func() (time.Time, bool) {
	type source struct {
		next     rrule.Next
		pending  time.Time
		absolute bool
	}
	sources := make([]source, 0, len(rules))
	for _, expr := range rules {
		opt, err := rrule.StrToROption(strings.TrimPrefix(expr, "RRULE:"))
		if err != nil {
			continue
		}
		normalizeRule(opt)
		opt.Count = 0
		opt.Until = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
		opt.Dtstart = wall(anchor.In(loc)).Truncate(time.Second)
		if fixedRule(*opt) {
			unit := time.Second
			switch opt.Freq {
			case rrule.HOURLY:
				unit = time.Hour
			case rrule.MINUTELY:
				unit = time.Minute
			}
			d := time.Duration(max(1, opt.Interval)) * unit
			at := anchor.Truncate(time.Second)
			sources = append(sources, source{absolute: true, next: func() (time.Time, bool) { current := at; at = at.Add(d); return current, at.After(current) }})
			continue
		}
		if !possibleDate(*opt) {
			continue
		}
		rule, err := rrule.NewRRule(*opt)
		if err != nil {
			continue
		}
		sources = append(sources, source{next: rule.Iterator()})
	}
	var previous time.Time
	return func() (time.Time, bool) {
		for work := 0; work < 10000; work++ {
			var best time.Time
			index := -1
			for i := range sources {
				source := &sources[i]
				if source.pending.IsZero() && source.next != nil {
					candidate, ok := source.next()
					if !ok {
						source.next = nil
						continue
					}
					source.pending = candidate
					if !source.absolute {
						source.pending = resolve(candidate, loc)
					}
				}
				if !source.pending.IsZero() && (best.IsZero() || source.pending.Before(best)) {
					best = source.pending
					index = i
				}
			}
			if index < 0 {
				return time.Time{}, false
			}
			sources[index].pending = time.Time{}
			if best.After(previous) {
				previous = best
				return best, true
			}
		}
		return time.Time{}, false
	}
}
