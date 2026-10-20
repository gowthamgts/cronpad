package worker_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/testutil"
	"github.com/gowthamgts/cronpad/internal/worker"
)

func TestMonthlyQuotaPlanChangeRetentionAndRollover(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testMonthlyQuotaPlanChangeRetentionAndRollover(t)
		})
	}
}
func testMonthlyQuotaPlanChangeRetentionAndRollover(t *testing.T) {
	f := testutil.New(t)
	now := f.Clock.Now().UTC()
	f.Clock.Advance(time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Minute).Sub(now))
	f.StartWriter(t)
	job := f.Job(t, "quota", f.Clock.Now())
	var max, calls atomic.Int64
	r := &limits.Registry{Store: f.Store, Clock: f.Clock}
	if err := r.Register("plan", func(_ context.Context, _, name string) (int64, bool, error) {
		return max.Load(), name == "max_runs_per_month_per_workspace", nil
	}); err != nil {
		t.Fatal(err)
	}
	f.Engine.Limits = r
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		calls.Add(1)
		return sink.ExecResult{Outcome: sink.Success}
	})
	first := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: job.Version, Trigger: "s"}
	run := func(o queue.Occurrence) {
		f.Run(t, func(ctx context.Context) error {
			return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
		})
	}
	run(first)
	max.Store(1)
	var quota *limits.Error
	if err := r.CheckRun(context.Background(), job.WorkspaceID); !errors.As(err, &quota) {
		t.Fatalf("plan change lost unlimited usage: %v", err)
	}
	second := first
	second.ScheduledAt = second.ScheduledAt.Add(time.Second)
	run(second)
	if calls.Load() != 1 {
		t.Fatal("quota exceeded", calls.Load())
	}
	// Retention cannot reopen an already-used monthly slot.
	if _, err := f.DBs.History.Writer.ExecContext(context.Background(), `DELETE FROM runs WHERE status='succeeded'`); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckRun(context.Background(), job.WorkspaceID); !errors.As(err, &quota) {
		t.Fatal("retention reopened quota", err)
	}
	f.Engine.Limits = &limits.Registry{Config: config.ConfigLimits{MaxRunsPerMonthPerWorkspace: 1}, Store: f.Store, Clock: f.Clock}
	third := second
	third.ScheduledAt = third.ScheduledAt.Add(time.Second)
	run(third)
	if calls.Load() != 1 {
		t.Fatal("fresh registry lost quota")
	}
	f.Clock.Advance(time.Minute)
	second.ScheduledAt = f.Clock.Now()
	run(second)
	if calls.Load() != 2 {
		t.Fatal("next month did not resume schedule", calls.Load())
	}
	n, err := f.Store.HistoryReader().CountMonthlyRuns(context.Background(), historygen.CountMonthlyRunsParams{WorkspaceID: job.WorkspaceID, Month: f.Clock.Now().UTC().Format("2006-01")})
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
func TestMonthlyQuotaConcurrentClaims(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testMonthlyQuotaConcurrentClaims(t)
		})
	}
}
func testMonthlyQuotaConcurrentClaims(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	f.Engine.Limits = &limits.Registry{Config: config.ConfigLimits{MaxRunsPerMonthPerWorkspace: 5}, Store: f.Store, Clock: f.Clock}
	var calls atomic.Int64
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		calls.Add(1)
		return sink.ExecResult{Outcome: sink.Success}
	})
	work := []worker.Work{}
	for i := 0; i < 20; i++ {
		job := f.Job(t, fmt.Sprint(i), f.Clock.Now())
		work = append(work, worker.Work{Occurrence: queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: job.Version, Trigger: "s"}, ClaimedAt: f.Clock.Now()})
	}
	f.Run(t, func(ctx context.Context) error {
		g, ctx := errgroup.WithContext(ctx)
		for _, w := range work {
			g.Go(func() error { return f.Engine.Process(ctx, w) })
		}
		return g.Wait()
	})
	if calls.Load() != 5 {
		t.Fatal("non-atomic quota", calls.Load())
	}
	var deferred int
	if err := f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status='skipped' AND skip_reason='quota_exceeded' AND claimed_at IS NULL`).Scan(&deferred); err != nil || deferred != 15 {
		t.Fatal(deferred, err)
	}
}

func TestMonthlyQuotaUsesAdmissionDayAcrossMidnight(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testMonthlyQuotaUsesAdmissionDayAcrossMidnight(t)
		})
	}
}
func testMonthlyQuotaUsesAdmissionDayAcrossMidnight(t *testing.T) {
	for _, overlap := range []string{"allow", "skip_if_running"} {
		t.Run(overlap, func(t *testing.T) {
			f := testutil.New(t)
			f.StartWriter(t)
			job := f.Job(t, "midnight", f.Clock.Now())
			if _, err := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET overlap_policy=? WHERE id=?`, overlap, job.ID); err != nil {
				t.Fatal(err)
			}
			f.Engine.Limits = &limits.Registry{Config: config.ConfigLimits{MaxRunsPerMonthPerWorkspace: 1}, Store: f.Store, Clock: f.Clock}
			admitted := f.Clock.Now()
			claimed := admitted.Add(-time.Millisecond)
			work := worker.Work{Occurrence: queue.Occurrence{JobID: job.ID, ScheduledAt: claimed, Version: job.Version, Trigger: "s"}, ClaimedAt: claimed}
			f.Run(t, func(ctx context.Context) error { return f.Engine.Process(ctx, work) })
			n, err := f.Store.HistoryReader().CountMonthlyRuns(context.Background(), historygen.CountMonthlyRunsParams{WorkspaceID: job.WorkspaceID, Month: admitted.UTC().Format("2006-01")})
			if err != nil || n != 1 {
				t.Fatal(overlap, n, err)
			}

		})
	}
}

func TestMonthlyUsageCountsFiredRunsAndNeverRetryAttempts(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	job := f.Job(t, "meter", f.Clock.Now())
	if _, e := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET retry_policy='{"max_attempts":2,"backoff":"fixed","initial_interval":"1s","max_interval":"1s","multiplier":1,"jitter":0}' WHERE id=?`, job.ID); e != nil {
		t.Fatal(e)
	}
	quota := &limits.Registry{Config: config.ConfigLimits{MaxRunsPerMonthPerWorkspace: 1, OverQuota: "allow"}, Store: f.Store, Clock: f.Clock}
	f.Engine.Limits = quota
	var calls atomic.Int64
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		if calls.Add(1) == 1 {
			return sink.ExecResult{Outcome: sink.Retryable}
		}
		return sink.ExecResult{Outcome: sink.Success}
	})
	o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
	process := func() {
		t.Helper()
		f.Run(t, func(ctx context.Context) error {
			return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
		})
	}
	process()
	o.Attempt = 1
	process()
	if n, e := quota.Usage(context.Background(), job.WorkspaceID); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	o.Attempt = 0
	o.Trigger = "m"
	process()
	o.Trigger = "r"
	process()
	if n, e := quota.Usage(context.Background(), job.WorkspaceID); e != nil || n != 3 {
		t.Fatal(n, e)
	}
	if n, e := f.Store.HistoryReader().CountMonthlyRuns(context.Background(), historygen.CountMonthlyRunsParams{WorkspaceID: job.WorkspaceID, Month: f.Clock.Now().UTC().Format("2006-01")}); e != nil || n != 3 {
		t.Fatal(n, e)
	}
}
