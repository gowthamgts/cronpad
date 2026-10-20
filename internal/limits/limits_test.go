package limits

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
)

func TestQuotaBoundariesAndOverrides(t *testing.T) {
	r := &Registry{Config: config.ConfigLimits{MaxJobsPerWorkspace: 100, MaxAPIKeysPerWorkspace: 20, MaxRetryAttempts: 10, MaxRunsPerDayPerWorkspace: 50000, MinInterval: config.Duration(60000000000)}}
	for _, tc := range []struct {
		limit    string
		value    int64
		exceeded bool
	}{{"max_jobs_per_workspace", 100, false}, {"max_jobs_per_workspace", 101, true}, {"max_api_keys_per_workspace", 21, true}, {"max_retry_attempts", 11, true}, {"max_runs_per_day_per_workspace", 50001, true}, {"min_interval", 59999, true}, {"min_interval", 60000, false}} {
		t.Run(tc.limit, func(t *testing.T) {
			e := r.Check(context.Background(), "ws", Action{tc.limit, tc.value})
			var l *Error
			if errors.As(e, &l) != tc.exceeded {
				t.Fatal(e)
			}
		})
	}
	if e := r.Register("plan", func(_ context.Context, workspace, action string) (int64, bool, error) {
		return 200, workspace == "paid" && action == "max_jobs_per_workspace", nil
	}); e != nil {
		t.Fatal(e)
	}
	if e := r.Check(context.Background(), "paid", Action{"max_jobs_per_workspace", 150}); e != nil {
		t.Fatal(e)
	}
	if e := r.Check(context.Background(), "free", Action{"max_jobs_per_workspace", 150}); e == nil {
		t.Fatal("free override leaked")
	}
	if e := r.Register("bad", 42); e == nil {
		t.Fatal("invalid provider accepted")
	}
}

// A nil Store proves unlimited preflight does no history queries.
func TestUnlimitedDailyQuotaSkipsStorage(t *testing.T) {
	r := &Registry{}
	if err := r.CheckRun(context.Background(), "workspace"); err != nil {
		t.Fatal(err)
	}
}

type testProvider struct {
	set   Set
	calls int
}

func (p *testProvider) For(context.Context, string) (Set, error) { p.calls++; return p.set, nil }
func TestProviderTTLAndInvalidation(t *testing.T) {
	clk := clockwork.NewFakeClock()
	p := &testProvider{set: Set{MaxRunsPerMonth: 5}}
	r := &Registry{Clock: clk}
	if err := r.Register("plan", p); err != nil {
		t.Fatal(err)
	}
	get := func(want int) {
		t.Helper()
		s, e := r.For(context.Background(), "w")
		if e != nil || s.MaxRunsPerMonth != want {
			t.Fatal(s, e)
		}
	}
	get(5)
	p.set.MaxRunsPerMonth = 9
	get(5)
	clk.Advance(60 * time.Second)
	get(9)
	p.set.MaxRunsPerMonth = 12
	r.Invalidate("w")
	get(12)
	if p.calls != 3 {
		t.Fatal(p.calls)
	}
}

func TestLegacyCallbacksApplyOnCachedV2Plan(t *testing.T) {
	r := &Registry{Clock: clockwork.NewFakeClock()}
	p := &testProvider{set: Set{MaxJobs: 2}}
	if e := r.Register("plan", p); e != nil {
		t.Fatal(e)
	}
	if e := r.Register("legacy", func(context.Context, string, string) (int64, bool, error) { return 7, true, nil }); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		s, e := r.For(context.Background(), "w")
		if e != nil || s.MaxJobs != 7 {
			t.Fatal(s, e)
		}
	}
	if p.calls != 1 {
		t.Fatal(p.calls)
	}
}

func TestLegacyDailyCallbackConvertsToMonthly(t *testing.T) {
	r := &Registry{}
	if err := r.Register("v1", func(_ context.Context, _, key string) (int64, bool, error) {
		return 2, key == "max_runs_per_day_per_workspace", nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err := r.For(context.Background(), "w")
	if err != nil || s.MaxRunsPerMonth != 60 {
		t.Fatal(s, err)
	}
}
