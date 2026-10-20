package schedule

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
)

func TestWorstCaseBudget(t *testing.T) {
	for _, tc := range []struct {
		kind  Kind
		input string
	}{
		{Once, "O" + strings.Repeat("\x17", 4000)},
		{RRule, "FREQ=DAILY;COUNT=100000"},
		{RRule, "FREQ=MONTHLY;COUNT=100000"},
		{RRule, "FREQ=DAILY;BYMONTH=2;BYYEARDAY=366"},
		{RRule, "FREQ=SECONDLY;BYDAY=MO;BYHOUR=9;BYMINUTE=0;BYSECOND=0"},
		{RRule, "FREQ=DAILY;BYHOUR=" + strings.Repeat("9,", 600) + "9;BYMINUTE=" + strings.Repeat("0,", 600) + "0"},
		{Cron, ", * * * * 0"},
		{Quartz, "0 0 0 30 FEB ?"},
		{Natural, "every 30 seconds between 9:30am and 9:31am"},
	} {
		t.Run(string(tc.kind)+"/"+tc.input[:min(len(tc.input), 40)], func(t *testing.T) { assertParserBudget(t, tc.kind, tc.input) })
	}
}
func TestParseContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ParseContext(ctx, Spec{Kind: RRule, Expr: "FREQ=DAILY;COUNT=100000"}, clockwork.NewFakeClock()); err == nil {
		t.Fatal("canceled computation succeeded")
	}
	ctx, cancel = context.WithCancel(context.Background())
	s, err := ParseContext(ctx, Spec{Expr: "daily at 9am"}, clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z")))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, ok := s.Next(instant("2026-01-01T00:00:00Z")); ok {
		t.Fatal("canceled Next succeeded")
	}
}
func BenchmarkWorstCase(b *testing.B) {
	for _, input := range []string{"FREQ=DAILY;COUNT=100000", "FREQ=MONTHLY;COUNT=100000", "FREQ=DAILY;BYMONTH=2;BYYEARDAY=366"} {
		b.Run(input, func(b *testing.B) {
			clk := clockwork.NewFakeClockAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			for b.Loop() {
				s, err := ParseWithClock(Spec{Kind: RRule, Expr: input, Timezone: "America/New_York"}, clk)
				if err != nil {
					b.Fatal(err)
				}
				_ = Upcoming(s, clk.Now(), 3)
			}
		})
	}
}

func TestCountAcrossDatelineGap(t *testing.T) {
	start := instant("2011-12-29T10:00:00Z")
	s, err := ParseWithClock(Spec{Kind: RRule, Expr: "FREQ=DAILY;COUNT=3", Timezone: "Pacific/Apia"}, clockwork.NewFakeClockAt(start))
	if err != nil {
		t.Fatal(err)
	}
	if got := Upcoming(s, start, 5); len(got) != 2 {
		t.Fatal(got)
	}
}
