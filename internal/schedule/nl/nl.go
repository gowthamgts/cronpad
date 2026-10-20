// Package nl tokenizes English scheduling clauses and compiles them to RRULEs.
package nl

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Token retains byte positions in the original UTF-8 input.
type Token struct {
	Text             string
	Position, Length int
}

// Error locates a rejected token and suggests a nearby vocabulary word.
type Error struct {
	Position, Length    int
	Message, Suggestion string
}

// Result contains calendar rules or a single instant; interval rules use elapsed time.
type Result struct {
	Canonical, Description string
	Rules                  []string
	Once, OnceWall         *time.Time
	Interval               time.Duration
	Count                  int
	Until                  *time.Time
}

// Tokenize splits whitespace and commas while retaining time/date punctuation.
func Tokenize(input string) []Token {
	var out []Token
	start := -1
	for i, r := range input {
		if unicode.IsSpace(r) || r == ',' {
			if start >= 0 {
				out = append(out, Token{strings.ToLower(input[start:i]), start, i - start})
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, Token{strings.ToLower(input[start:]), start, len(input) - start})
	}
	return out
}

type parser struct {
	input                   string
	tokens                  []Token
	i                       int
	now                     time.Time
	freq                    string
	interval                int
	days, monthdays, months []string
	position                int
	times                   [][3]int
	window                  []int
	windowClocks            [2][3]int
	result                  Result
}

// Parse is a recursive-descent clause parser. Creation time is supplied by the owner.
func Parse(input string, now time.Time) (Result, *Error) {
	p := &parser{input: input, tokens: Tokenize(input), now: now, interval: 1}
	if len(p.tokens) == 0 {
		return Result{}, &Error{Message: "expression is required"}
	}
	if result, ok, err := p.oneshot(); ok || err != nil {
		return result, err
	}
	for p.i < len(p.tokens) {
		if err := p.clause(); err != nil {
			return Result{}, err
		}
	}
	if p.freq == "" {
		return Result{}, p.errorAt(0, "expected a recurrence frequency")
	}
	return p.compile(), nil
}
func (p *parser) word() string {
	if p.i >= len(p.tokens) {
		return ""
	}
	return p.tokens[p.i].Text
}
func (p *parser) take(word string) bool {
	if p.word() == word {
		p.i++
		return true
	}
	return false
}
func (p *parser) errorAt(i int, message string) *Error {
	if i >= len(p.tokens) {
		return &Error{Position: len(p.input), Message: message}
	}
	token := p.tokens[i]
	suggestion := ""
	distance := 3
	for _, word := range vocabulary() {
		d := editDistance(token.Text, word)
		if d < distance {
			distance = d
			suggestion = p.input[:token.Position] + word + p.input[token.Position+token.Length:]
		}
	}
	return &Error{Position: token.Position, Length: token.Length, Message: message, Suggestion: suggestion}
}
func (p *parser) oneshot() (Result, bool, *Error) {
	if p.take("in") {
		duration, err := p.duration()
		if err != nil {
			return Result{}, true, err
		}
		if p.i != len(p.tokens) {
			return Result{}, true, p.errorAt(p.i, "unexpected token")
		}
		at := p.now.Add(duration)
		return Result{Once: &at, Description: "Once in " + duration.String()}, true, nil
	}
	if p.word() == "today" || p.word() == "tomorrow" || (weekday(p.word()) != "" && len(p.tokens) > 1 && p.tokens[1].Text == "at") {
		day := p.word()
		p.i++
		if !p.take("at") {
			return Result{}, true, p.errorAt(p.i, "expected at followed by a time")
		}
		clock, err := p.time()
		if err != nil {
			return Result{}, true, err
		}
		if p.i != len(p.tokens) {
			return Result{}, true, p.errorAt(p.i, "unexpected token")
		}
		date := time.Date(p.now.Year(), p.now.Month(), p.now.Day(), clock[0], clock[1], clock[2], 0, time.UTC)
		if day == "tomorrow" {
			date = date.AddDate(0, 0, 1)
		} else if code := weekday(day); code != "" {
			target := weekdayIndex(code)
			delta := (target - int(p.now.Weekday()) + 7) % 7
			date = date.AddDate(0, 0, delta)
			if delta == 0 && wallNow(p.now).Compare(date) >= 0 {
				date = date.AddDate(0, 0, 7)
			}
		}
		return Result{OnceWall: &date, Description: "Once at " + date.Format("2006-01-02 15:04")}, true, nil
	}
	saved := p.i
	p.take("at")
	if _, err := time.Parse("2006-01-02", p.word()); err == nil {
		date, _ := time.Parse("2006-01-02", p.word())
		p.i++
		clock, err := p.time()
		if err != nil {
			return Result{}, true, err
		}
		if p.i != len(p.tokens) {
			return Result{}, true, p.errorAt(p.i, "unexpected token")
		}
		date = date.Add(time.Duration(clock[0])*time.Hour + time.Duration(clock[1])*time.Minute + time.Duration(clock[2])*time.Second)
		return Result{OnceWall: &date, Description: "Once at " + date.Format("2006-01-02 15:04")}, true, nil
	}
	p.i = saved
	return Result{}, false, nil
}
func wallNow(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}
func (p *parser) clause() *Error {
	word := p.word()
	switch word {
	case "every", "each", "the", "of", "on", "and":
		p.i++
		return nil
	case "hourly", "daily", "weekly", "monthly", "yearly", "annually", "quarterly":
		p.i++
		p.freq = map[string]string{"hourly": "HOURLY", "daily": "DAILY", "weekly": "WEEKLY", "monthly": "MONTHLY", "yearly": "YEARLY", "annually": "YEARLY", "quarterly": "MONTHLY"}[word]
		if word == "quarterly" {
			p.interval = 3
			p.monthdays = []string{"1"}
		}
		return nil
	case "at":
		p.i++
		for {
			clock, err := p.time()
			if err != nil {
				return err
			}
			p.times = append(p.times, clock)
			if p.word() != "and" || p.i+1 >= len(p.tokens) || !isTime(p.tokens[p.i+1].Text) {
				break
			}
			p.i++
		}
		return nil
	case "between", "from":
		p.i++
		start, err := p.time()
		if err != nil {
			return err
		}
		if !p.take("and") && !p.take("to") {
			return p.errorAt(p.i, "expected and or to in window")
		}
		end, err := p.time()
		if err != nil {
			return err
		}
		if seconds(end) < seconds(start) {
			return p.errorAt(p.i-1, "window end must not precede start")
		}
		p.windowClocks = [2][3]int{start, end}

		for h := start[0]; h <= end[0]; h++ {
			p.window = append(p.window, h)
		}
		return nil
	case "until":
		p.i++
		date, err := time.ParseInLocation("2006-01-02", p.word(), p.now.Location())
		if err != nil {
			return p.errorAt(p.i, "expected YYYY-MM-DD after until")
		}
		p.i++
		p.result.Until = &date
		return nil
	case "for":
		p.i++
		duration, err := p.duration()
		if err != nil {
			return err
		}
		end := p.now.Add(duration)
		p.result.Until = &end
		return nil
	case "weekday", "weekdays", "weekend", "weekends":
		p.i++
		if strings.HasPrefix(word, "weekday") {
			p.days = append(p.days, "MO", "TU", "WE", "TH", "FR")
		} else {
			p.days = append(p.days, "SA", "SU")
		}
		if p.freq == "" {
			p.freq = "WEEKLY"
		}
		return nil
	}
	if freq := unit(word); freq != "" && (word != "second" || p.i+1 >= len(p.tokens) || (weekday(p.tokens[p.i+1].Text) == "" && p.tokens[p.i+1].Text != "to")) {
		p.freq = freq
		p.i++
		return nil
	}
	if code := weekday(word); code != "" {
		p.days = append(p.days, code)
		p.i++
		if p.freq == "" {
			p.freq = "WEEKLY"
		}
		return nil
	}
	if month := monthNumber(word); month > 0 {
		p.months = append(p.months, strconv.Itoa(month))
		p.i++
		return nil
	}
	if n, err := strconv.Atoi(word); err == nil {
		if p.i+1 < len(p.tokens) && p.tokens[p.i+1].Text == "times" {
			if n < 1 || n > 100000 {
				return p.errorAt(p.i, "count must be between 1 and 100000")
			}
			p.result.Count = n
			p.i += 2
			return nil
		}
		if p.i+1 < len(p.tokens) && unit(p.tokens[p.i+1].Text) != "" {
			if n < 1 || n > 1000000 {
				return p.errorAt(p.i, "interval must be between 1 and 1000000")
			}
			p.interval = n
			p.freq = unit(p.tokens[p.i+1].Text)
			p.i += 2
			return nil
		}
		if n >= 1 && n <= 31 {
			p.monthdays = append(p.monthdays, strconv.Itoa(n))
			p.i++
			return nil
		}
	}
	ordinalStart := p.i
	if n, ok := p.ordinal(); ok {
		if (weekday(p.word()) != "" || p.word() == "weekday" || p.word() == "weekdays") && (n < -5 || n > 5) {
			return p.errorAt(ordinalStart, "weekday ordinal must be between first and fifth, or last")
		}
		if code := weekday(p.word()); code != "" {
			p.days = append(p.days, strconv.Itoa(n)+code)
			p.i++
		} else if p.word() == "weekday" || p.word() == "weekdays" {
			p.days = append(p.days, "MO", "TU", "WE", "TH", "FR")
			p.position = n
			p.i++
		} else {
			p.monthdays = append(p.monthdays, strconv.Itoa(n))
			p.take("day")
		}
		if p.freq == "" {
			p.freq = "MONTHLY"
		}
		return nil
	}
	return p.errorAt(p.i, "unknown word "+strconv.Quote(word))
}
func (p *parser) ordinal() (int, bool) {
	word := p.word()
	values := map[string]int{"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5, "last": -1}
	if word == "second" && p.i+2 < len(p.tokens) && p.tokens[p.i+1].Text == "to" && p.tokens[p.i+2].Text == "last" {
		p.i += 3
		return -2, true
	}
	if n, ok := values[word]; ok {
		p.i++
		return n, true
	}
	for _, suffix := range []string{"st", "nd", "rd", "th"} {
		if strings.HasSuffix(word, suffix) {
			n, e := strconv.Atoi(strings.TrimSuffix(word, suffix))
			if e == nil && n >= 1 && n <= 31 {
				p.i++
				return n, true
			}
		}
	}
	return 0, false
}
func (p *parser) duration() (time.Duration, *Error) {
	i := p.i
	if p.i+1 >= len(p.tokens) {
		return 0, p.errorAt(i, "expected number and duration unit")
	}
	n, err := strconv.ParseFloat(p.word(), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 1000000 {
		return 0, p.errorAt(i, "expected a positive duration")
	}
	p.i++
	units := map[string]time.Duration{"SECONDLY": time.Second, "MINUTELY": time.Minute, "HOURLY": time.Hour, "DAILY": 24 * time.Hour, "WEEKLY": 7 * 24 * time.Hour}
	d, ok := units[unit(p.word())]
	if !ok {
		return 0, p.errorAt(p.i, "expected seconds, minutes, hours, days, or weeks")
	}
	p.i++
	if n > float64(time.Duration(1<<63-1))/float64(d) {
		return 0, p.errorAt(i, "duration is too large")
	}
	return time.Duration(n * float64(d)), nil
}
func (p *parser) time() ([3]int, *Error) {
	word := p.word()
	i := p.i
	p.i++
	if word == "noon" {
		return [3]int{12, 0, 0}, nil
	}
	if word == "midnight" {
		return [3]int{}, nil
	}
	suffix := ""
	for _, s := range []string{"am", "pm"} {
		if strings.HasSuffix(word, s) {
			suffix = s
			word = strings.TrimSuffix(word, s)
		}
	}
	parts := strings.Split(word, ":")
	if len(parts) > 3 || word == "" {
		return [3]int{}, p.errorAt(i, "expected a time")
	}
	var result [3]int
	for i, part := range parts {
		n, e := strconv.Atoi(part)
		if e != nil {
			return result, p.errorAt(p.i-1, "expected a time")
		}
		result[i] = n
	}
	if suffix != "" {
		if result[0] < 1 || result[0] > 12 {
			return result, p.errorAt(i, "12-hour time must use 1 through 12")
		}
		result[0] %= 12
		if suffix == "pm" {
			result[0] += 12
		}
	}
	if result[0] < 0 || result[0] > 23 || result[1] < 0 || result[1] > 59 || result[2] < 0 || result[2] > 59 {
		return result, p.errorAt(i, "time is outside valid hour/minute/second range")
	}
	return result, nil
}
func isTime(s string) bool {
	return s == "noon" || s == "midnight" || strings.Contains(s, ":") || strings.HasSuffix(s, "am") || strings.HasSuffix(s, "pm")
}
func (p *parser) compile() Result {
	result := p.result
	freq := "FREQ=" + p.freq
	prefix := freq
	intervalOnly := (p.freq == "HOURLY" || p.freq == "MINUTELY" || p.freq == "SECONDLY") && len(p.times) == 0 && len(p.window) == 0 && len(p.days) == 0 && len(p.monthdays) == 0 && len(p.months) == 0
	if p.interval != 1 || intervalOnly {
		prefix += ";INTERVAL=" + strconv.Itoa(p.interval)
	}
	if len(p.days) > 0 {
		prefix += ";BYDAY=" + strings.Join(unique(p.days), ",")
	}
	if len(p.monthdays) > 0 {
		prefix += ";BYMONTHDAY=" + strings.Join(unique(p.monthdays), ",")
	}
	if len(p.months) > 0 {
		prefix += ";BYMONTH=" + strings.Join(unique(p.months), ",")
	}
	if p.position != 0 {
		prefix += ";BYSETPOS=" + strconv.Itoa(p.position)
	}
	if intervalOnly {
		units := map[string]time.Duration{"HOURLY": time.Hour, "MINUTELY": time.Minute, "SECONDLY": time.Second}
		result.Interval = time.Duration(p.interval) * units[p.freq]
		result.Canonical = prefix
		unit := map[string]string{"HOURLY": "hour", "MINUTELY": "minute", "SECONDLY": "second"}[p.freq]
		if unit == "" {
			unit = strings.ToLower(strings.TrimSuffix(p.freq, "LY"))
		}
		if p.interval == 1 {
			result.Description = "Every " + unit
		} else {
			result.Description = fmt.Sprintf("Every %d %ss", p.interval, unit)
		}
	} else {
		if len(p.times) == 0 {
			p.times = [][3]int{{0, 0, 0}}
		}
		if len(p.window) > 0 {
			result.Rules = windowRules(prefix, p.freq, p.windowClocks[0], p.windowClocks[1])
		} else {
			for _, clock := range p.times {
				result.Rules = append(result.Rules, prefix+fmt.Sprintf(";BYHOUR=%d;BYMINUTE=%d;BYSECOND=%d", clock[0], clock[1], clock[2]))
			}
			if len(p.times) > 1 {
				same := true
				hours := []int{}
				for _, clock := range p.times {
					same = same && clock[1] == p.times[0][1] && clock[2] == p.times[0][2]
					hours = append(hours, clock[0])
				}
				if same {
					sort.Ints(hours)
					result.Rules = []string{prefix + fmt.Sprintf(";BYHOUR=%s;BYMINUTE=%d;BYSECOND=%d", intList(hours), p.times[0][1], p.times[0][2])}
				}
			}
		}
		result.Canonical = strings.Join(result.Rules, "\nRRULE:")
		result.Description = describe(p)
	}
	if result.Count > 0 {
		result.Canonical += ";COUNT=" + strconv.Itoa(result.Count)
	}
	if result.Until != nil {
		result.Canonical += ";UNTIL=" + result.Until.UTC().Format("20060102T150405Z")
	}
	return result
}
func unique(values []string) []string {
	out := []string{}
	for _, v := range values {
		found := false
		for _, u := range out {
			found = found || u == v
		}
		if !found {
			out = append(out, v)
		}
	}
	return out
}
func intList(values []int) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strconv.Itoa(v)
	}
	return strings.Join(out, ",")
}
func unit(word string) string {
	word = strings.TrimSuffix(word, "s")
	return map[string]string{"second": "SECONDLY", "minute": "MINUTELY", "hour": "HOURLY", "day": "DAILY", "week": "WEEKLY", "month": "MONTHLY", "year": "YEARLY"}[word]
}
func weekday(word string) string {
	return map[string]string{"monday": "MO", "mon": "MO", "tuesday": "TU", "tue": "TU", "wednesday": "WE", "wed": "WE", "thursday": "TH", "thu": "TH", "friday": "FR", "fri": "FR", "saturday": "SA", "sat": "SA", "sunday": "SU", "sun": "SU"}[word]
}
func weekdayIndex(code string) int {
	for i, v := range []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"} {
		if v == code {
			return i
		}
	}
	return 0
}
func monthNumber(word string) int {
	for i, name := range []string{"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"} {
		if word == name || word == name[:3] {
			return i + 1
		}
	}
	return 0
}
func vocabulary() []string {
	return strings.Fields("every each hour hours hourly minute minutes second seconds day days daily week weeks weekly month months monthly year years yearly quarterly weekday weekdays weekend weekends monday tuesday wednesday thursday friday saturday sunday first third fourth fifth last january february march april may june july august september october november december at on of the and between from to until for times today tomorrow noon midnight")
}
func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	row := make([]int, len(y)+1)
	for j := range row {
		row[j] = j
	}
	for i, c := range x {
		previous := row[0]
		row[0] = i + 1
		for j, d := range y {
			old := row[j+1]
			cost := 0
			if c != d {
				cost = 1
			}
			row[j+1] = min(row[j+1]+1, row[j]+1, previous+cost)
			previous = old
		}
	}
	return row[len(y)]
}
func describe(p *parser) string {
	names := map[string]string{"MO": "Monday", "TU": "Tuesday", "WE": "Wednesday", "TH": "Thursday", "FR": "Friday", "SA": "Saturday", "SU": "Sunday"}
	ordinal := func(s string) string {
		return map[string]string{"1": "first", "2": "second", "3": "third", "4": "fourth", "5": "fifth", "-1": "last", "-2": "second to last"}[s]
	}
	day := "every " + map[string]string{"DAILY": "day", "WEEKLY": "week", "MONTHLY": "month", "YEARLY": "year", "HOURLY": "hour", "MINUTELY": "minute", "SECONDLY": "second"}[p.freq]
	if p.interval > 1 {
		day = fmt.Sprintf("every %d %ss", p.interval, strings.TrimPrefix(day, "every "))
	}
	if len(p.days) == 5 && p.position == 0 {
		day = "on weekdays"
	} else if len(p.days) == 2 && p.days[0] == "SA" && p.days[1] == "SU" {
		day = "on weekends"
	} else if len(p.days) > 0 {
		var labels []string
		for _, code := range p.days {
			if len(code) > 2 {
				n := code[:len(code)-2]
				labels = append(labels, "the "+ordinal(n)+" "+names[code[len(code)-2:]])
			} else {
				labels = append(labels, names[code])
			}
		}
		day = "on " + strings.Join(labels, " and ")
		if p.freq == "MONTHLY" {
			day += " of every month"
		} else if p.interval > 1 {
			day += fmt.Sprintf(" every %d weeks", p.interval)
		}
	}
	if p.position != 0 {
		day = "on the " + ordinal(strconv.Itoa(p.position)) + " weekday of every month"
	}
	if len(p.monthdays) > 0 {
		day = "on day " + strings.Join(p.monthdays, ", ") + " of every " + strings.ToLower(strings.TrimSuffix(p.freq, "LY"))
		if p.monthdays[0] == "-1" {
			day = "on the last day of every month"
		}
	}
	if len(p.months) > 0 {
		m, _ := strconv.Atoi(p.months[0])
		day = "on " + time.Month(m).String() + " " + strings.Join(p.monthdays, ", ") + " every year"
	}
	var times []string
	for _, clock := range p.times {
		times = append(times, fmt.Sprintf("%02d:%02d", clock[0], clock[1]))
	}
	if len(p.window) > 0 {
		return "Every " + strings.ToLower(strings.TrimSuffix(p.freq, "LY")) + " between " + fmt.Sprintf("%02d:%02d and %02d:%02d ", p.windowClocks[0][0], p.windowClocks[0][1], p.windowClocks[1][0], p.windowClocks[1][1]) + day
	}
	return "At " + strings.Join(times, " and ") + " " + day
}

func seconds(t [3]int) int { return t[0]*3600 + t[1]*60 + t[2] }

// windowRules splits boundary hours so RRULE filters retain exact minute/second windows.
func windowRules(prefix, freq string, start, end [3]int) []string {
	if freq == "HOURLY" {
		var hours []int
		for h := start[0]; h <= end[0]; h++ {
			t := [3]int{h, start[1], start[2]}
			if seconds(t) <= seconds(end) {
				hours = append(hours, h)
			}
		}
		return []string{prefix + fmt.Sprintf(";BYHOUR=%s;BYMINUTE=%d;BYSECOND=%d", intList(hours), start[1], start[2])}
	}
	if freq != "MINUTELY" && freq != "SECONDLY" {
		return []string{prefix + fmt.Sprintf(";BYHOUR=%d;BYMINUTE=%d;BYSECOND=%d", start[0], start[1], start[2])}
	}
	var out []string
	for h := start[0]; h <= end[0]; h++ {
		var full []int
		for m := 0; m < 60; m++ {
			if freq == "MINUTELY" {
				t := [3]int{h, m, start[2]}
				if seconds(t) >= seconds(start) && seconds(t) <= seconds(end) {
					full = append(full, m)
				}
				continue
			}
			var allowed []int
			for s := 0; s < 60; s++ {
				t := [3]int{h, m, s}
				if seconds(t) >= seconds(start) && seconds(t) <= seconds(end) {
					allowed = append(allowed, s)
				}
			}
			if len(allowed) == 60 {
				full = append(full, m)
			} else if len(allowed) > 0 {
				out = append(out, prefix+fmt.Sprintf(";BYHOUR=%d;BYMINUTE=%d;BYSECOND=%s", h, m, intList(allowed)))
			}
		}
		if len(full) > 0 {
			last := "0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59"
			if freq == "MINUTELY" {
				last = strconv.Itoa(start[2])
			}
			out = append(out, prefix+fmt.Sprintf(";BYHOUR=%d;BYMINUTE=%s;BYSECOND=%s", h, intList(full), last))
		}
	}
	return out
}
