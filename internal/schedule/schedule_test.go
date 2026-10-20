package schedule

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

func instant(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
func TestGolden(t *testing.T) {
	var cases []struct {
		Input          string
		Kind           Kind
		TZ, Now, After string
		Expected       []string
	}
	b, err := os.ReadFile("testdata/golden.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = yaml.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Input+"/"+tc.TZ, func(t *testing.T) {
			s, err := ParseWithClock(Spec{Kind: tc.Kind, Expr: tc.Input, Timezone: tc.TZ}, clockwork.NewFakeClockAt(instant(tc.Now)))
			if err != nil {
				t.Fatal(err)
			}
			got := Upcoming(s, instant(tc.After), 10)
			if len(got) != len(tc.Expected) {
				t.Fatalf("got %v want %v", got, tc.Expected)
			}
			for i, v := range got {
				if v.Format(time.RFC3339) != tc.Expected[i] {
					t.Fatalf("next[%d]=%s want %s; canonical %s", i, v.Format(time.RFC3339), tc.Expected[i], s.Canonical())
				}
			}
			if s.Describe() == "" {
				t.Fatal("empty description")
			}
		})
	}
}
func TestBoundsAllKinds(t *testing.T) {
	for kind, expr := range map[Kind]string{Cron: "0 9 * * *", Quartz: "0 0 9 * * ?", Natural: "daily at 9am", RRule: "FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=0", Once: "2026-01-02T09:00:00Z"} {
		t.Run(string(kind), func(t *testing.T) {
			start := instant("2026-01-02T09:00:00Z")
			end := instant("2026-01-05T09:00:00Z")
			count := 2
			s, err := ParseWithClock(Spec{Kind: kind, Expr: expr, StartsAt: &start, EndsAt: &end, Count: &count}, clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z")))
			if err != nil {
				t.Fatal(err)
			}
			got := Upcoming(s, instant("2026-01-01T00:00:00Z"), 10)
			want := 2
			if kind == Once {
				want = 1
			}
			if len(got) != want || !got[0].Equal(start) {
				t.Fatal(got)
			}
			if _, ok := s.Next(end); ok {
				t.Fatal("end/count ignored")
			}
			again := Upcoming(s, instant("2026-01-01T00:00:00Z"), 10)
			if len(again) != len(got) {
				t.Fatal("stateful schedule")
			}
		})
	}
}
func TestStructuredAndDetection(t *testing.T) {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	_, err := ParseWithClock(Spec{Expr: "every tusday at 9am"}, clk)
	var parseErr *Error
	if !errors.As(err, &parseErr) || parseErr.Position != 6 || parseErr.Length != 6 || !strings.Contains(parseErr.Suggestion, "tuesday") {
		t.Fatalf("%+v", err)
	}
	for expr, kind := range map[string]Kind{"2026-12-25T07:00:00Z": Once, "0 9 * * *": Cron, "0 0 9 ? * MON": Quartz, "0 0 9 * * * 2027": Quartz, "FREQ=DAILY": RRule, "every hour": Natural, "@hourly": Cron} {
		s, err := ParseWithClock(Spec{Expr: expr}, clk)
		if err != nil || s.Kind() != kind {
			t.Fatalf("%s: %v %v", expr, s, err)
		}
	}
}
func TestCronProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(10, 20))
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	for range 300 {
		expr := fmt.Sprintf("%d %d * * %d", random.IntN(60), random.IntN(24), random.IntN(7))
		s, err := ParseWithClock(Spec{Kind: Cron, Expr: expr}, clk)
		if err != nil {
			t.Fatal(err)
		}
		reference, err := parser.Parse(expr)
		if err != nil {
			t.Fatal(err)
		}
		after := clk.Now().Add(time.Duration(random.IntN(365*24)) * time.Hour)
		for range 10 {
			got, ok := s.Next(after)
			want := reference.Next(after)
			if !ok || !got.Equal(want) {
				t.Fatalf("%s %s != %s", expr, got, want)
			}
			after = got
		}
	}
}
func TestAbsoluteIntervalsAndExhaustion(t *testing.T) {
	clk := clockwork.NewFakeClockAt(instant("2026-03-08T06:00:00Z"))
	for _, expr := range []string{"every hour", "@every 1h"} {
		s, err := ParseWithClock(Spec{Expr: expr, Timezone: "America/New_York"}, clk)
		if err != nil {
			t.Fatal(err)
		}
		got := Upcoming(s, clk.Now(), 3)
		if !got[0].Equal(clk.Now().Add(time.Hour)) || MinGap(s, clk.Now(), 10) != time.Hour {
			t.Fatal(got)
		}
	}
	s, err := ParseWithClock(Spec{Kind: Quartz, Expr: "0 0 0 30 FEB ?"}, clk)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Next(clk.Now()); ok {
		t.Fatal("impossible date matched")
	}
}
func BenchmarkParse(b *testing.B) {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseWithClock(Spec{Expr: "2nd tuesday of every month at 9am", Timezone: "Asia/Kolkata"}, clk); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkNext(b *testing.B) {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	s, err := ParseWithClock(Spec{Expr: "2nd tuesday of every month at 9am", Timezone: "Asia/Kolkata"}, clk)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := s.Next(clk.Now()); !ok {
			b.Fatal("exhausted")
		}
	}
}

func TestDSTAcrossCalendarKinds(t *testing.T) {
	for _, zone := range []string{"America/New_York", "Australia/Lord_Howe", "Europe/London"} {
		for kind, expr := range map[Kind]string{Cron: "30 2 * * *", Quartz: "0 30 2 * * ?", Natural: "daily at 2:30am", RRule: "FREQ=DAILY;BYHOUR=2;BYMINUTE=30;BYSECOND=0"} {
			clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
			s, err := ParseWithClock(Spec{Kind: kind, Expr: expr, Timezone: zone}, clk)
			if err != nil {
				t.Fatal(err)
			}
			reference, err := ParseWithClock(Spec{Kind: Cron, Expr: "30 2 * * *", Timezone: zone}, clk)
			if err != nil {
				t.Fatal(err)
			}
			after := instant("2026-03-01T00:00:00Z")
			for range 400 {
				got, ok := s.Next(after)
				want, wantOK := reference.Next(after)
				if ok != wantOK || !got.Equal(want) {
					t.Fatalf("%s %s %s != %s", zone, kind, got, want)
				}
				after = got
			}
		}
	}
}
func TestOrdinalAndTimesClauses(t *testing.T) {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	for expr, want := range map[string]string{"at 9am every day": "2026-01-01T09:00:00Z", "second to last friday of the month": "2026-01-23T00:00:00Z", "every day at 9:30am and 6:45pm": "2026-01-01T09:30:00Z", "every hour from 9am to 5pm on weekdays": "2026-01-01T09:00:00Z"} {
		s, err := ParseWithClock(Spec{Expr: expr}, clk)
		if err != nil {
			t.Fatal(err)
		}
		next, ok := s.Next(clk.Now())
		if !ok || next.Format(time.RFC3339) != want {
			t.Fatalf("%s: %s", expr, next)
		}
	}
}

func TestLeapCenturyAndInvalidIntervals(t *testing.T) {
	clk := clockwork.NewFakeClockAt(instant("2097-01-01T00:00:00Z"))
	s, err := ParseWithClock(Spec{Kind: RRule, Expr: "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29;BYHOUR=0;BYMINUTE=0;BYSECOND=0"}, clk)
	if err != nil {
		t.Fatal(err)
	}
	next, ok := s.Next(clk.Now())
	if !ok || !next.Equal(instant("2104-02-29T00:00:00Z")) {
		t.Fatal(next, ok)
	}
	for _, expr := range []string{"@every -1h", "@every 0s"} {
		if _, err := ParseWithClock(Spec{Expr: expr}, clk); err == nil {
			t.Fatalf("accepted %s", expr)
		}
	}
}

func TestWindowsAndRangeOffsets(t *testing.T) {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	for expr, want := range map[string]string{"every hour between 9:30am and 5:15pm": "2026-01-01T09:30:00Z", "every 10 minutes between 9:30am and 10:15am": "2026-01-01T09:30:00Z", "every 30 seconds between 9:30am and 9:31am": "2026-01-01T09:30:00Z"} {
		s, err := ParseWithClock(Spec{Kind: Natural, Expr: expr}, clk)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := s.Next(clk.Now())
		if !ok || got.Format(time.RFC3339) != want {
			t.Fatalf("%s: %s", expr, got)
		}
	}
	_, err := ParseWithClock(Spec{Kind: Cron, Expr: "0 99 * * *"}, clk)
	var detail *Error
	if !errors.As(err, &detail) || detail.Position != 2 {
		t.Fatal(err)
	}
}

func TestFiniteNaturalBounds(t *testing.T) {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	for expr, want := range map[string]int{"every day at 9am 10 times": 10, "every 10 minutes for 2 hours": 12, "every day at 9am until 2026-01-04": 3} {
		s, err := ParseWithClock(Spec{Kind: Natural, Expr: expr}, clk)
		if err != nil {
			t.Fatal(err)
		}
		got := Upcoming(s, clk.Now(), 100)
		if len(got) != want {
			t.Fatalf("%s: %v", expr, got)
		}
	}
	if _, err := ParseWithClock(Spec{Expr: "in NaN hours"}, clk); err == nil {
		t.Fatal("accepted NaN")
	}
}
func TestQuartzYearListsAndNearestWeekday(t *testing.T) {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	for _, expr := range []string{"0 0 9 * * MON", "0 0 9 ? * ?", "0 0 9 L-40 * ?", "0 0 9 ? * 6#6"} {
		if _, err := ParseWithClock(Spec{Kind: Quartz, Expr: expr}, clk); err == nil {
			t.Fatalf("accepted %s", expr)
		}
	}
	for expr, want := range map[string]string{"0 0 9 1W AUG ?": "2026-08-03T09:00:00Z", "0 0 9 ? * 5L": "2026-01-29T09:00:00Z", "0 0 0 1 JAN ? 2027,2029-2031": "2027-01-01T00:00:00Z"} {
		s, err := ParseWithClock(Spec{Kind: Quartz, Expr: expr}, clk)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := s.Next(clk.Now())
		if !ok || got.Format(time.RFC3339) != want {
			t.Fatal(expr, got)
		}
	}
}
func TestOverlapAfterSecondOccurrence(t *testing.T) {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	s, err := ParseWithClock(Spec{Kind: Cron, Expr: "30 1 * * *", Timezone: "America/New_York"}, clk)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.Next(instant("2026-11-01T06:00:00Z"))
	if !ok || got.Format(time.RFC3339) != "2026-11-02T06:30:00Z" {
		t.Fatal(got)
	}
}

func TestRRuleCountIncludesDTStart(t *testing.T) {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	s, err := ParseWithClock(Spec{Kind: RRule, Expr: "FREQ=DAILY;COUNT=10"}, clk)
	if err != nil {
		t.Fatal(err)
	}
	if got := Upcoming(s, clk.Now(), 20); len(got) != 9 {
		t.Fatal(got)
	}
}
