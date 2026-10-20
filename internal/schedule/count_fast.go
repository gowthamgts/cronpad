package schedule

import (
	"context"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// simpleCountLast counts Gregorian period cycles instead of replaying up to 100k
// simple daily/monthly/yearly occurrences. Calendar periods retain local DST policy.
func simpleCountLast(ctx context.Context, rules []string, anchor, after time.Time, loc *time.Location, count int) (time.Time, bool) {
	if len(rules) != 1 {
		return time.Time{}, false
	}
	opt, err := rrule.StrToROption(strings.TrimPrefix(rules[0], "RRULE:"))
	if err != nil {
		return time.Time{}, false
	}
	normalizeRule(opt)
	opt.Dtstart = wall(anchor.In(loc))
	opt.Count = 0
	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return time.Time{}, false
	}
	o := rule.Options
	if len(o.Byhour) == 0 && o.Freq < rrule.HOURLY {
		o.Byhour = []int{o.Dtstart.Hour()}
	}
	if len(o.Byminute) == 0 && o.Freq < rrule.MINUTELY {
		o.Byminute = []int{o.Dtstart.Minute()}
	}
	if len(o.Bysecond) == 0 && o.Freq < rrule.SECONDLY {
		o.Bysecond = []int{o.Dtstart.Second()}
	}
	if o.Freq > rrule.DAILY || len(o.Byhour) != 1 || len(o.Byminute) != 1 || len(o.Bysecond) != 1 || len(o.Bysetpos) > 0 || len(o.Byweekno) > 0 || len(o.Byyearday) > 0 || len(o.Byeaster) > 0 {
		return time.Time{}, false
	}
	iv := o.Interval
	start := wall(anchor.In(loc))
	var candidate func(int) time.Time
	cycle := 1
	switch o.Freq {
	case rrule.DAILY:
		if len(o.Bymonth) > 0 || len(o.Bymonthday) > 0 || len(o.Byweekday) > 0 {
			return time.Time{}, false
		}
		candidate = func(n int) time.Time {
			return time.Date(start.Year(), start.Month(), start.Day()+n*iv, o.Byhour[0], o.Byminute[0], o.Bysecond[0], 0, time.UTC)
		}
	case rrule.WEEKLY:
		if len(o.Bymonth) > 0 || len(o.Bymonthday) > 0 || len(o.Byweekday) != 1 || o.Byweekday[0].N() != 0 {
			return time.Time{}, false
		}
		start = start.AddDate(0, 0, -((int(start.Weekday()) + 6 - o.Wkst.Day() + 7) % 7))
		day := (o.Byweekday[0].Day() - o.Wkst.Day() + 7) % 7
		candidate = func(n int) time.Time {
			return time.Date(start.Year(), start.Month(), start.Day()+day+n*iv*7, o.Byhour[0], o.Byminute[0], o.Bysecond[0], 0, time.UTC)
		}
	case rrule.MONTHLY, rrule.YEARLY:
		if len(o.Byweekday) > 0 || len(o.Bymonthday) != 1 {
			return time.Time{}, false
		}
		month := int(start.Month())
		if o.Freq == rrule.YEARLY {
			if len(o.Bymonth) != 1 {
				return time.Time{}, false
			}
			month = o.Bymonth[0]
			cycle = 400 / gcd(iv, 400)
		} else {
			if len(o.Bymonth) > 0 {
				return time.Time{}, false
			}
			cycle = 4800 / gcd(iv, 4800)
		}
		candidate = func(n int) time.Time {
			year, m := start.Year(), month
			if o.Freq == rrule.YEARLY {
				year += n * iv
			} else {
				m += n * iv
			}
			first := time.Date(year, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
			last := first.AddDate(0, 1, -1).Day()
			day := o.Bymonthday[0]
			if day < 0 {
				day = last + 1 + day
			}
			if day < 1 || day > last {
				return time.Time{}
			}
			return time.Date(first.Year(), first.Month(), day, o.Byhour[0], o.Byminute[0], o.Bysecond[0], 0, time.UTC)
		}
	}
	if candidate == nil {
		return time.Time{}, false
	}
	// Only the first period can predate DTSTART/after. Whole cycles then repeat their valid-date mask.
	first := 0
	for first < cycle+1 {
		t := candidate(first)
		if !t.IsZero() && resolve(t, loc).After(after) {
			break
		}
		first++
	}
	if first > cycle {
		return time.Time{}, false
	}
	var valid []int
	for n := 0; n < cycle; n++ {
		if ctx.Err() != nil {
			return time.Time{}, false
		}
		if !candidate(first + n).IsZero() {
			valid = append(valid, n)
		}
	}
	if len(valid) == 0 {
		return time.Time{}, false
	}
	index := first + (count-1)/len(valid)*cycle + valid[(count-1)%len(valid)]
	last := candidate(index)
	if last.Year() > 9999 {
		return time.Date(10000, 1, 3, 0, 0, 0, 0, time.UTC), true
	}
	if !safeDateCount(loc, anchor, last) {
		return time.Time{}, false
	}
	return resolve(last, loc), true
}

// A dateline jump can collapse two civil dates into one instant. Keep the
// deduplicating iterator for that rare case instead of using period arithmetic.
func safeDateCount(loc *time.Location, start, end time.Time) bool {
	if loc == time.UTC {
		return true
	}
	cursor := start.Add(-48 * time.Hour)
	limit := end.Add(48 * time.Hour)
	for work := 0; work < 20000; work++ {
		_, boundary := cursor.In(loc).ZoneBounds()
		if boundary.IsZero() || boundary.After(limit) {
			return true
		}
		if !boundary.After(cursor) {
			cursor = cursor.AddDate(0, 0, 1)
			continue
		}
		_, before := boundary.Add(-time.Nanosecond).In(loc).Zone()
		_, after := boundary.In(loc).Zone()
		if after-before >= 86400 {
			return false
		}
		cursor = boundary
	}
	return false
}
