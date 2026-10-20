package schedule

import (
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
)

func fuzzParser(f *testing.F, kind Kind, seeds ...string) {
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) { assertParserBudget(t, kind, input) })

}
func FuzzCron(f *testing.F) {
	fuzzParser(f, Cron, "0 9 * * *", "*/30 * * * * *", "@every 90m", "30 2 * * *", "0 0 30 FEB *")
}
func FuzzQuartz(f *testing.F) {
	fuzzParser(f, Quartz, "0 0 9 ? * 3#2", "0 0 18 L * ?", "0 0 9 15W * ?", "0 0 0 30 FEB ?", "0 0 0 * * * 2027")
}
func FuzzRRule(f *testing.F) {
	fuzzParser(f, RRule, "FREQ=DAILY;BYHOUR=9", "FREQ=MONTHLY;BYDAY=2TU", "FREQ=SECONDLY;INTERVAL=2;BYSECOND=1", "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30")
}
func FuzzNatural(f *testing.F) {
	fuzzParser(f, Natural, "2nd tuesday of every month at 9am", "every day at 9am and 6pm", "in 20 minutes", "every hour between 9am and 5pm on weekdays", "every tusday")
}
func FuzzOnce(f *testing.F) {
	fuzzParser(f, Once, "2026-12-25T07:00:00Z", "2026-12-25T07:00:00+05:30", "bad")
}

func exerciseParser(kind Kind, input string) string {
	clk := clockwork.NewFakeClockAt(instant("2026-01-01T00:00:00Z"))
	s, err := ParseWithClock(Spec{Kind: kind, Expr: input, Timezone: "America/New_York"}, clk)
	if err != nil {
		return ""
	}
	after := clk.Now()
	for range 3 {
		next, ok := s.Next(after)
		if !ok {
			break
		}
		if !next.After(after) || next.Location() != time.UTC {
			return fmt.Sprintf("Next is not strictly increasing UTC: %s => %s", after, next)
		}
		after = next
	}
	if s.Canonical() == "" || s.Describe() == "" {
		return "empty schedule metadata"
	}
	return ""
}

func assertParserBudget(t *testing.T, kind Kind, input string) {
	t.Helper()
	// DECISION: 200ms is deliberately generous versus microsecond normal inputs;
	// a two-second hard watchdog makes non-returning dependencies record a corpus failure.
	wallClock := clockwork.NewRealClock()
	started := wallClock.Now()
	timer := wallClock.NewTimer(2 * time.Second)
	defer timer.Stop()
	done := make(chan string, 1)
	go func() { done <- exerciseParser(kind, input) }()
	select {
	case failure := <-done:
		if failure != "" {
			t.Fatalf("input %q: %s", input, failure)
		}
		if elapsed := wallClock.Since(started); elapsed > 200*time.Millisecond {
			t.Fatalf("slow input %q: %s", input, elapsed)
		}
	case <-timer.Chan():
		t.Fatalf("parser/Next watchdog exceeded 2s: input %q", input)
	}
}
