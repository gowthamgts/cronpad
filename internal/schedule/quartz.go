package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type quartz struct {
	fields   [7][]int
	dom, dow string
}

func parseQuartz(expr string) (*quartz, error) {
	tokens := strings.Fields(strings.ToUpper(expr))
	if len(tokens) != 6 && len(tokens) != 7 {
		return nil, invalid(Quartz, 0, len(expr), "Quartz requires six or seven fields")
	}
	if ((tokens[3] == "?") == (tokens[5] == "?")) && (tokens[3] != "*" || tokens[5] != "*") {
		return nil, invalid(Quartz, fieldPosition(expr, 3), len(tokens[3]), "exactly one day field must be ? (or both must be *)")
	}
	q := &quartz{dom: tokens[3], dow: tokens[5]}
	ranges := [][2]int{{0, 59}, {0, 59}, {0, 23}, {1, 31}, {1, 12}, {1, 7}, {1970, 9999}}
	if len(tokens) == 6 {
		tokens = append(tokens, "*")
	}
	for i, token := range tokens {
		if i == 3 || i == 5 {
			if token == "?" {
				token = "*"
			}
			if (i == 3 && strings.ContainsAny(token, "LW")) || (i == 5 && strings.ContainsAny(token, "L#")) {
				if err := q.special(i, token); err != nil {
					return nil, invalid(Quartz, fieldPosition(expr, i), len(token), err.Error())
				}
				continue
			}
		}
		values, err := fieldValues(token, ranges[i][0], ranges[i][1], i)
		if err != nil {
			return nil, invalid(Quartz, fieldPosition(expr, i), len(tokens[i]), err.Error())
		}
		q.fields[i] = values
	}
	return q, nil
}
func fieldPosition(expr string, index int) int {
	pos := 0
	for i, token := range strings.Fields(expr) {
		at := strings.Index(expr[pos:], token) + pos
		if i == index {
			return at
		}
		pos = at + len(token)
	}
	return len(expr)
}
func fieldValues(token string, low, high, field int) ([]int, error) {
	names := map[string]int{}
	if field == 4 {
		for i, n := range []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"} {
			names[n] = i + 1
		}
	}
	if field == 5 {
		for i, n := range []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"} {
			names[n] = i + 1
		}
	}
	number := func(s string) (int, error) {
		if n, ok := names[s]; ok {
			return n, nil
		}
		n, e := strconv.Atoi(s)
		if e != nil || n < low || n > high {
			return 0, fmt.Errorf("value %q must be between %d and %d", s, low, high)
		}
		return n, nil
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(token, ",") {
		step := 1
		pieces := strings.Split(part, "/")
		if len(pieces) > 2 {
			return nil, fmt.Errorf("invalid step %q", part)
		}
		if len(pieces) == 2 {
			n, err := strconv.Atoi(pieces[1])
			if err != nil || n < 1 || n > high-low+1 {
				return nil, fmt.Errorf("invalid step %q", part)
			}
			step = n
		}
		a, b := low, high
		if pieces[0] != "*" {
			bounds := strings.Split(pieces[0], "-")
			var err error
			a, err = number(bounds[0])
			if err != nil {
				return nil, err
			}
			b = a
			if len(pieces) == 2 {
				b = high
			}
			if len(bounds) == 2 {
				b, err = number(bounds[1])
				if err != nil {
					return nil, err
				}
			} else if len(bounds) > 2 {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			if a > b {
				return nil, fmt.Errorf("descending range %q", part)
			}
		}
		for n := a; n <= b; n += step {
			seen[n] = true
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}
func (q *quartz) special(field int, s string) error {
	if field == 3 {
		if s == "L" || s == "LW" {
			return nil
		}
		if strings.HasPrefix(s, "L-") {
			n, e := strconv.Atoi(s[2:])
			if e == nil && n >= 0 && n <= 30 {
				return nil
			}
		}
		if strings.HasSuffix(s, "W") {
			n, e := strconv.Atoi(strings.TrimSuffix(s, "W"))
			if e == nil && n >= 1 && n <= 31 {
				return nil
			}
		}
	} else {
		if strings.HasSuffix(s, "L") {
			v, e := fieldValues(strings.TrimSuffix(s, "L"), 1, 7, 5)
			if e == nil && len(v) == 1 {
				return nil
			}
		}
		parts := strings.Split(s, "#")
		if len(parts) == 2 {
			v, e := fieldValues(parts[0], 1, 7, 5)
			n, e2 := strconv.Atoi(parts[1])
			if e == nil && e2 == nil && len(v) == 1 && n >= 1 && n <= 5 {
				return nil
			}
		}
	}
	return fmt.Errorf("invalid Quartz day modifier %q", s)
}
func contains(values []int, n int) bool {
	i := sort.SearchInts(values, n)
	return i < len(values) && values[i] == n
}
func nextValue(values []int, n int) (int, bool) {
	i := sort.SearchInts(values, n)
	if i == len(values) {
		return 0, false
	}
	return values[i], true
}
func (q *quartz) day(t time.Time) bool {
	last := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	day := t.Day()
	nearest := func(n int) int {
		if n > last {
			return 0
		}
		w := time.Date(t.Year(), t.Month(), n, 0, 0, 0, 0, time.UTC).Weekday()
		if w == time.Saturday {
			if n == 1 {
				return 3
			}
			return n - 1
		}
		if w == time.Sunday {
			if n == last {
				return n - 2
			}
			return n + 1
		}
		return n
	}
	if q.dom != "?" && q.dom != "*" {
		switch {
		case q.dom == "L":
			return day == last
		case q.dom == "LW":
			return day == nearest(last)
		case strings.HasPrefix(q.dom, "L-"):
			n, _ := strconv.Atoi(q.dom[2:])
			return day == last-n
		case strings.HasSuffix(q.dom, "W"):
			n, _ := strconv.Atoi(strings.TrimSuffix(q.dom, "W"))
			return day == nearest(n)
		default:
			return contains(q.fields[3], day)
		}
	}
	if q.dow == "?" || q.dow == "*" {
		return true
	}
	if strings.HasSuffix(q.dow, "L") {
		v, _ := fieldValues(strings.TrimSuffix(q.dow, "L"), 1, 7, 5)
		return int(t.Weekday())+1 == v[0] && day+7 > last
	}
	if strings.Contains(q.dow, "#") {
		parts := strings.Split(q.dow, "#")
		v, _ := fieldValues(parts[0], 1, 7, 5)
		n, _ := strconv.Atoi(parts[1])
		return int(t.Weekday())+1 == v[0] && (day-1)/7+1 == n
	}
	return contains(q.fields[5], int(t.Weekday())+1)
}
func (q *quartz) next(after time.Time) (time.Time, bool) {
	t := after.Truncate(time.Second).Add(time.Second)
	limit := after.AddDate(5, 0, 0)
	// At most one pass per day plus carries across the five-year horizon.
	for work := 0; work < 8192 && !t.After(limit); work++ {
		year, ok := nextValue(q.fields[6], t.Year())
		if !ok {
			return time.Time{}, false
		}
		if year != t.Year() {
			t = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		month, ok := nextValue(q.fields[4], int(t.Month()))
		if !ok {
			t = time.Date(t.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if month != int(t.Month()) {
			t = time.Date(t.Year(), time.Month(month), 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !q.day(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		hour, ok := nextValue(q.fields[2], t.Hour())
		if !ok {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if hour != t.Hour() {
			t = time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, time.UTC)
		}
		minute, ok := nextValue(q.fields[1], t.Minute())
		if !ok {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if minute != t.Minute() {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), minute, 0, 0, time.UTC)
		}
		second, ok := nextValue(q.fields[0], t.Second())
		if !ok {
			t = t.Truncate(time.Minute).Add(time.Minute)
			continue
		}
		t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), second, 0, time.UTC)
		if !t.After(limit) {
			return t, true
		}
	}
	return time.Time{}, false
}
func (q *quartz) describe() string {
	timepart := "At seconds " + ints(q.fields[0]) + ", minutes " + ints(q.fields[1]) + ", hours " + ints(q.fields[2])
	if len(q.fields[0]) == 1 && len(q.fields[1]) == 1 && len(q.fields[2]) == 1 {
		timepart = fmt.Sprintf("At %02d:%02d", q.fields[2][0], q.fields[1][0])
		if q.fields[0][0] != 0 {
			timepart += fmt.Sprintf(":%02d", q.fields[0][0])
		}
	}
	day := "every day"
	switch {
	case q.dom == "L":
		day = "on the last day of every month"
	case q.dom == "LW":
		day = "on the last weekday of every month"
	case strings.HasPrefix(q.dom, "L-"):
		day = "on day " + q.dom + " of every month"
	case strings.HasSuffix(q.dom, "W"):
		day = "on the weekday nearest day " + strings.TrimSuffix(q.dom, "W") + " of every month"
	case strings.Contains(q.dow, "#"):
		parts := strings.Split(q.dow, "#")
		v, _ := fieldValues(parts[0], 1, 7, 5)
		n, _ := strconv.Atoi(parts[1])
		day = "on the " + ordinal(n) + " " + time.Weekday(v[0]-1).String() + " of the month"
	case strings.HasSuffix(q.dow, "L"):
		v, _ := fieldValues(strings.TrimSuffix(q.dow, "L"), 1, 7, 5)
		day = "on the last " + time.Weekday(v[0]-1).String() + " of every month"
	case q.dow != "?" && q.dow != "*":
		var names []string
		for _, v := range q.fields[5] {
			names = append(names, time.Weekday(v-1).String())
		}
		day = "on " + strings.Join(names, ", ")
	case q.dom != "?" && q.dom != "*":
		day = "on day " + q.dom + " of every month"
	}
	return timepart + " " + day
}
