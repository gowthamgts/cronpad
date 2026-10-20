package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

func parseRule(expr string, anchor time.Time, loc *time.Location) (func(time.Time) (time.Time, bool), string, string, int, *time.Time, error) {
	fail := func(pos int, message string) (func(time.Time) (time.Time, bool), string, string, int, *time.Time, error) {
		return nil, "", "", 0, nil, invalid(RRule, pos, len(strings.SplitN(expr[pos:], ";", 2)[0]), message)
	}
	expr = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(expr)), "RRULE:")
	if !strings.Contains(expr, "FREQ=") {
		return fail(0, "RRULE requires FREQ")
	}
	opt, err := rrule.StrToROption(expr)
	if err != nil {
		return fail(ruleErrorPosition(expr, err.Error()), err.Error())
	}
	if opt.Interval < 0 || opt.Interval > 1000000 || opt.Count < 0 || opt.Count > 100000 {
		return fail(0, "interval/count outside supported bounds")
	}
	for _, part := range strings.Split(expr, ";") {
		if part == "INTERVAL=0" || part == "COUNT=0" {
			return fail(strings.Index(expr, part), "interval and count must be positive")
		}
	}
	opt.Dtstart = wall(anchor.In(loc)).Truncate(time.Second)
	if opt.Dtstart.IsZero() {
		return fail(0, "anchor must be a nonzero date")
	}
	// rrule-go indexes month masks before checking whether large ordinal weekdays exist.
	// DECISION: reject impossible month-scoped ordinals before entering that upstream path.
	for _, day := range opt.Byweekday {
		n := day.N()
		if n != 0 && opt.Freq > rrule.MONTHLY {
			return fail(strings.Index(expr, "BYDAY="), "ordinal weekdays require MONTHLY or YEARLY frequency")
		}
		if (opt.Freq == rrule.MONTHLY || (opt.Freq == rrule.YEARLY && len(opt.Bymonth) > 0)) && (n < -5 || n > 5) {
			return fail(strings.Index(expr, "BYDAY="), "month-scoped weekday ordinal must be between -5 and 5")
		}
	}
	normalizeRule(opt)
	original, err := rrule.NewRRule(*opt)
	if err != nil {
		return fail(ruleErrorPosition(expr, err.Error()), err.Error())
	}
	canonical := original.OrigOptions.RRuleString()
	description := describeRule(original.Options)
	count := opt.Count
	var until *time.Time
	if !opt.Until.IsZero() {
		u := opt.Until.UTC()
		until = &u
	}
	if fixedRule(*opt) {
		unit := time.Second
		switch opt.Freq {
		case rrule.HOURLY:
			unit = time.Hour
		case rrule.MINUTELY:
			unit = time.Minute
		}
		return intervalNext(anchor.Truncate(time.Second), time.Duration(original.Options.Interval)*unit), canonical, description, count, until, nil
	}
	normalized := original.Options
	for _, values := range [][]int{normalized.Byhour, normalized.Byminute, normalized.Bysecond, normalized.Bymonth, normalized.Bymonthday} {
		sort.Ints(values)
	}
	normalized.Count = 0
	normalized.Until = time.Time{}
	if !reachableTimes(normalized) {
		return fail(0, "time filters cannot match the interval phase")
	}
	base := func(after time.Time) (time.Time, bool) {
		o := normalized
		start := o.Dtstart
		// DECISION: seek by whole recurrence periods; normalized defaults preserve DTSTART fields.
		// This prevents dense rules from replaying all occurrences since creation on every Next.
		if after.After(start) {
			iv := o.Interval
			switch o.Freq {
			case rrule.YEARLY:
				n := (after.Year()-start.Year())/iv - 1
				if n > 0 {
					o.Dtstart = time.Date(start.Year()+n*iv, 1, 1, 0, 0, 0, 0, time.UTC)
				}
			case rrule.MONTHLY:
				months := (after.Year()-start.Year())*12 + int(after.Month()-start.Month())
				n := months/iv - 1
				if n > 0 {
					o.Dtstart = time.Date(start.Year(), start.Month()+time.Month(n*iv), 1, 0, 0, 0, 0, time.UTC)
				}
			case rrule.WEEKLY, rrule.DAILY:
				days := (after.Unix() - start.Unix()) / 86400
				step := int64(iv)
				if o.Freq == rrule.WEEKLY {
					step *= 7
				}
				n := days/step - 1
				if n > 0 {
					o.Dtstart = start.AddDate(0, 0, int(n*step))
				}
			case rrule.HOURLY, rrule.MINUTELY, rrule.SECONDLY:
				unit := time.Second
				switch o.Freq {
				case rrule.WEEKLY:
					unit = 7 * 24 * time.Hour
				case rrule.DAILY:
					unit = 24 * time.Hour
				case rrule.HOURLY:
					unit = time.Hour
				case rrule.MINUTELY:
					unit = time.Minute
				}
				step := time.Duration(iv) * unit
				n := after.Sub(start)/step - 1
				if n > 0 {
					o.Dtstart = start.Add(n * step)
				}
			}
		}
		o.Until = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
		if !possibleDate(o) {
			return time.Time{}, false
		}
		rule, e := rrule.NewRRule(o)
		if e != nil {
			return time.Time{}, false
		}
		t := rule.After(after, false)
		return t, !t.IsZero() && !t.After(o.Until)
	}
	return civilNext(base, loc), canonical, description, count, until, nil
}
func ruleErrorPosition(expr, message string) int {
	upper := strings.ToUpper(message)
	for _, part := range strings.Split(expr, ";") {
		key := strings.Split(part, "=")[0]
		if strings.Contains(upper, key) {
			return strings.Index(expr, part)
		}
	}
	return 0
}
func reachableTimes(o rrule.ROption) bool {
	if o.Freq < rrule.HOURLY {
		return true
	}
	cycle := 86400
	unit := 1
	switch o.Freq {
	case rrule.HOURLY:
		cycle = 24
		unit = 3600
	case rrule.MINUTELY:
		cycle = 1440
		unit = 60
	}
	g := gcd(cycle, o.Interval)
	anchor := (o.Dtstart.Hour()*3600 + o.Dtstart.Minute()*60 + o.Dtstart.Second()) / unit
	for v := 0; v < cycle; v++ {
		if (v-anchor)%g != 0 {
			continue
		}
		second := v * unit
		h, m, s := second/3600, (second/60)%60, second%60
		if len(o.Byhour) > 0 && !contains(o.Byhour, h) {
			continue
		}
		if unit <= 60 && len(o.Byminute) > 0 && !contains(o.Byminute, m) {
			continue
		}
		if unit == 1 && len(o.Bysecond) > 0 && !contains(o.Bysecond, s) {
			continue
		}
		return true
	}
	return false
}
func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
func possibleDate(o rrule.ROption) bool {
	// A year-day/month contradiction otherwise makes the upstream iterator scan to year 9999.
	// Leap and common representative years cover every possible year-day/month pairing.
	if len(o.Byyearday) > 0 && len(o.Bymonth) > 0 {
		matched := false
		for _, year := range []int{2000, 2001} {
			days := 365
			if year == 2000 {
				days = 366
			}
			for _, day := range o.Byyearday {
				if day < 0 {
					day = days + 1 + day
				}
				if day < 1 || day > days {
					continue
				}
				date := time.Date(year, 1, day, 0, 0, 0, 0, time.UTC)
				if contains(o.Bymonth, int(date.Month())) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}

	if len(o.Bymonth) == 0 || len(o.Bymonthday) == 0 {
		return true
	}
	for _, month := range o.Bymonth {
		last := time.Date(2000, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
		for _, d := range o.Bymonthday {
			if d < 0 {
				d = -d
			}
			if d <= last {
				return true
			}
		}
	}
	return false
}
func ints(v []int) string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = strconv.Itoa(n)
	}
	return strings.Join(out, ",")
}
func ordinal(n int) string {
	switch n {
	case -2:
		return "second to last"
	case -1:
		return "last"
	case 1:
		return "first"
	case 2:
		return "second"
	case 3:
		return "third"
	case 4:
		return "fourth"
	case 5:
		return "fifth"
	}
	return fmt.Sprint(n) + "th"
}
func describeRule(o rrule.ROption) string {
	unit := []string{"year", "month", "week", "day", "hour", "minute", "second"}[o.Freq]
	phrase := "Every " + unit
	if o.Interval > 1 {
		phrase = fmt.Sprintf("Every %d %ss", o.Interval, unit)
	}
	var days []string
	for _, d := range o.Byweekday {
		n := d.N()
		name := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}[d.Day()]
		if n != 0 {
			name = "the " + ordinal(n) + " " + name
		}
		days = append(days, name)
	}
	if len(days) > 0 {
		phrase += " on " + strings.Join(days, " and ")
	}
	if len(o.Bymonthday) > 0 {
		phrase += " on day " + ints(o.Bymonthday)
	}
	if len(o.Bymonth) > 0 {
		var names []string
		for _, m := range o.Bymonth {
			names = append(names, time.Month(m).String())
		}
		phrase += " in " + strings.Join(names, ", ")
	}
	if len(o.Byhour) > 0 && len(o.Byminute) > 0 {
		var times []string
		for _, h := range o.Byhour {
			for _, m := range o.Byminute {
				times = append(times, fmt.Sprintf("%02d:%02d", h, m))
			}
		}
		phrase += " at " + strings.Join(times, ", ")
	}
	return phrase
}
func describeCron(expr string) string {
	aliases := map[string]string{"@yearly": "At 00:00 on January 1 every year", "@annually": "At 00:00 on January 1 every year", "@monthly": "At 00:00 on the first day of every month", "@weekly": "At 00:00 on Sunday every week", "@daily": "At 00:00 every day", "@midnight": "At 00:00 every day", "@hourly": "At the start of every hour"}
	if text := aliases[expr]; text != "" {
		return text
	}
	f := strings.Fields(expr)
	if len(f) == 5 {
		f = append([]string{"0"}, f...)
	}
	timepart := "Every second"
	if f[0] != "*" {
		timepart = "At second " + describeField(f[0], nil)
		if f[0] == "0" {
			timepart = "Every minute"
		}
		if strings.HasPrefix(f[0], "*/") {
			timepart = "Every " + f[0][2:] + " seconds"
		}
	}
	if f[1] != "*" {
		if strings.HasPrefix(f[1], "*/") {
			timepart = "Every " + f[1][2:] + " minutes"
		} else {
			timepart = "At minute " + describeField(f[1], nil) + " of every hour"
		}
	}
	if f[2] != "*" {
		hours, e1 := fieldValues(strings.ToUpper(f[2]), 0, 23, 2)
		minutes, e2 := fieldValues(f[1], 0, 59, 1)
		if e1 == nil && e2 == nil && len(hours)*len(minutes) <= 12 {
			var clocks []string
			for _, h := range hours {
				for _, m := range minutes {
					clock := fmt.Sprintf("%02d:%02d", h, m)
					if f[0] != "0" {
						clock += " (seconds " + describeField(f[0], nil) + ")"
					}
					clocks = append(clocks, clock)
				}
			}
			timepart = "At " + strings.Join(clocks, " and ")
		} else {
			timepart += " during hours " + describeField(f[2], nil)
		}
	}
	days := map[string]string{}
	for i, n := range []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"} {
		days[strconv.Itoa(i)] = n
		days[strings.ToUpper(n[:3])] = n
	}
	months := map[string]string{}
	for m := 1; m <= 12; m++ {
		name := time.Month(m).String()
		months[strconv.Itoa(m)] = name
		months[strings.ToUpper(name[:3])] = name
	}
	switch {
	case f[3] != "*" && f[5] != "*":
		timepart += " on day " + describeField(f[3], nil) + " of the month or on " + describeField(f[5], days)
	case f[3] != "*":
		timepart += " on day " + describeField(f[3], nil) + " of every month"
	case f[5] != "*":
		timepart += " on " + describeField(f[5], days)
	default:
		timepart += " every day"
	}
	if f[4] != "*" {
		timepart += " in " + describeField(f[4], months)
	}
	return timepart
}
func describeField(value string, names map[string]string) string {
	if value == "*" {
		return "every value"
	}
	if strings.Contains(value, ",") {
		parts := strings.Split(value, ",")
		for i, p := range parts {
			parts[i] = describeField(p, names)
		}
		return strings.Join(parts, " and ")
	}
	if strings.Contains(value, "/") {
		parts := strings.SplitN(value, "/", 2)
		return "every " + parts[1] + " values from " + describeField(parts[0], names)
	}
	if strings.Contains(value, "-") {
		parts := strings.SplitN(value, "-", 2)
		return describeField(parts[0], names) + " through " + describeField(parts[1], names)
	}
	if name := names[strings.ToUpper(value)]; name != "" {
		return name
	}
	return value
}

// RRULE BY parts are sets. Deduplication bounds Cartesian expansion independently
// of how many repeated values an adversarial expression contains.
func normalizeRule(o *rrule.ROption) {
	for _, values := range []*[]int{&o.Byhour, &o.Byminute, &o.Bysecond, &o.Bymonth, &o.Bymonthday, &o.Byyearday, &o.Byweekno, &o.Bysetpos, &o.Byeaster} {
		sort.Ints(*values)
		unique := (*values)[:0]
		for _, n := range *values {
			if len(unique) == 0 || unique[len(unique)-1] != n {
				unique = append(unique, n)
			}
		}
		*values = unique
	}
	seen := map[rrule.Weekday]bool{}
	days := o.Byweekday[:0]
	for _, day := range o.Byweekday {
		if !seen[day] {
			days = append(days, day)
			seen[day] = true
		}
	}
	o.Byweekday = days
}
func fixedRule(o rrule.ROption) bool {
	return o.Freq >= rrule.HOURLY && len(o.Byhour) == 0 && len(o.Byminute) == 0 && len(o.Bysecond) == 0 && len(o.Byweekday) == 0 && len(o.Bymonth) == 0 && len(o.Bymonthday) == 0 && len(o.Byyearday) == 0 && len(o.Byweekno) == 0 && len(o.Bysetpos) == 0 && len(o.Byeaster) == 0
}
