package schedule

import "time"

// wall encodes local calendar components in UTC so recurrence libraries cannot choose DST policy.
func wall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}
func resolve(w time.Time, loc *time.Location) time.Time {
	var best, gap time.Time
	offsets := map[int]bool{}
	for h := -48; h <= 48; h += 12 {
		probe := w.Add(time.Duration(h) * time.Hour).In(loc)
		_, off := probe.Zone()
		offsets[off] = true
		_, end := probe.ZoneBounds()
		if !end.IsZero() {
			before := wall(end.Add(-time.Nanosecond).In(loc))
			after := wall(end.In(loc))
			if before.Before(w) && after.After(w) && (gap.IsZero() || end.Before(gap)) {
				gap = end
			}
		}
	}
	for off := range offsets {
		candidate := w.Add(-time.Duration(off) * time.Second)
		if wall(candidate.In(loc)).Equal(w) && (best.IsZero() || candidate.Before(best)) {
			best = candidate
		}
	}
	if !best.IsZero() {
		return best.UTC()
	}
	return gap.UTC()
}
func civilNext(base func(time.Time) (time.Time, bool), loc *time.Location) func(time.Time) (time.Time, bool) {
	return func(after time.Time) (time.Time, bool) {
		cursor := wall(after.In(loc))
		limit := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
		for i := 0; i < 10000; i++ {
			candidate, ok := base(cursor)
			if !ok || candidate.IsZero() || !candidate.After(cursor) || candidate.After(limit) {
				return time.Time{}, false
			}
			resolved := resolve(candidate, loc)
			if !resolved.IsZero() && resolved.After(after) {
				return resolved, true
			}
			cursor = candidate
		}
		return time.Time{}, false
	}
}
