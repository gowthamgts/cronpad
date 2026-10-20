package reconciler_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/reconciler"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/testutil"
	"github.com/gowthamgts/cronpad/internal/worker"
)

func rec(f *testutil.Fixture) *reconciler.Reconciler {
	return &reconciler.Reconciler{Store: f.Store, Queue: f.Queue, Writer: f.Writer, Clock: f.Clock, Config: f.Config, Metrics: f.Metrics, Epoch: "epoch"}
}
func TestDowntimeMisfires(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) { t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode); testDowntimeMisfires(t) })
	}
}
func testDowntimeMisfires(t *testing.T) {
	for _, tc := range []struct {
		policy     string
		fire, skip int
	}{{"fire_once", 1, 9}, {"skip", 0, 10}, {"fire_all", 3, 7}} {
		t.Run(tc.policy, func(t *testing.T) {
			f := testutil.New(t)
			f.StartWriter(t)
			past := f.Clock.Now().Add(-9 * time.Minute)
			job := f.Job(t, tc.policy, past)
			_, err := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET schedule_expr='@every 1m',schedule_anchor_at=?,misfire_policy=? WHERE id=?`, past.Add(-time.Minute).UnixMilli(), tc.policy, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			r := rec(f)
			r.Config.Scheduler.MisfireFireAllCap = 3
			f.Run(t, func(ctx context.Context) error { return r.Full(ctx, true) })
			runs, err := f.Store.HistoryReader().ListRuns(context.Background(), historygen.ListRunsParams{WorkspaceID: job.WorkspaceID, JobID: job.ID, TagFilter: "", TagJobs: "[]", Status: "", TriggerFilter: "", SlaOnly: 0, FromTime: 0, ToTime: 1<<63 - 1, CursorTime: 0, CursorID: "", PageSize: 100})
			if err != nil {
				t.Fatal(err)
			}
			fire, skip := 0, 0
			for _, run := range runs {
				if run.Status == "deferred" {
					fire++
				}
				if run.Status == "skipped" {
					skip++
				}
			}
			if fire != tc.fire || skip != tc.skip {
				t.Fatalf("fire=%d skip=%d", fire, skip)
			}
			j, err := f.Store.ControlReader().GetJob(context.Background(), sqlitegen.GetJobParams{ID: job.ID, WorkspaceID: job.WorkspaceID})
			if err != nil || !j.NextRunAt.Valid || j.NextRunAt.Int64 <= f.Clock.Now().UnixMilli() || j.OccurrenceCount != 10 {
				t.Fatal(j, err)
			}
		})
	}
}
func TestEpochLossOverdueAndRetryRepair(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testEpochLossOverdueAndRetryRepair(t)
		})
	}
}
func testEpochLossOverdueAndRetryRepair(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	r := rec(f)
	ctx := context.Background()
	job := f.Job(t, "future", f.Clock.Now().Add(time.Hour))
	f.Run(t, func(ctx context.Context) error { return r.Check(ctx) })
	member := queue.Occurrence{JobID: job.ID, ScheduledAt: time.UnixMilli(job.NextRunAt.Int64), Version: 1, Trigger: "s"}.Format()
	v, _ := f.Queue.Contains(ctx, []string{member})
	if !v[0] {
		t.Fatal("future occurrence not repaired after epoch loss")
	}
	_ = f.Queue.Remove(ctx, member)
	_ = f.Queue.SetEpoch(ctx, "another_epoch")
	f.Run(t, func(ctx context.Context) error { return r.Check(ctx) })
	v, _ = f.Queue.Contains(ctx, []string{member})
	if !v[0] {
		t.Fatal("lost epoch was not rebuilt")
	}
	overdue := f.Job(t, "overdue", f.Clock.Now().Add(-time.Minute))
	o := queue.Occurrence{JobID: overdue.ID, ScheduledAt: time.UnixMilli(overdue.NextRunAt.Int64), Version: 1, Trigger: "s"}
	f.Run(t, func(ctx context.Context) error { return r.Check(ctx) })
	v, _ = f.Queue.Contains(ctx, []string{o.Format()})
	if !v[0] {
		t.Fatal("lost member was not repaired")
	}
	_, err := f.Store.History().UpsertRun(ctx, historygen.UpsertRunParams{ID: "run_retry", JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: f.Clock.Now().Add(-time.Hour).UnixMilli(), Trigger: "schedule", Status: "deferred", NextAttemptAt: sql.NullInt64{Int64: f.Clock.Now().Add(-time.Minute).UnixMilli(), Valid: true}, JobVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.Run(t, func(ctx context.Context) error { return r.Check(ctx) })
	stats, err := f.Queue.Stats(ctx)
	if err != nil || stats.Due < 3 {
		t.Fatal(stats, err)
	}
	_, err = f.DBs.Control.Writer.ExecContext(ctx, `UPDATE jobs SET version=2 WHERE id=?`, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.Run(t, func(ctx context.Context) error { return r.Full(ctx, false) })
	run, err := f.Store.HistoryReader().GetRun(ctx, historygen.GetRunParams{ID: "run_retry", WorkspaceID: job.WorkspaceID})
	if err != nil || run.Status != "cancelled" {
		t.Fatal(run, err)
	}
}
func TestStartupRecomputesCounters(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testStartupRecomputesCounters(t)
		})
	}
}
func testStartupRecomputesCounters(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	job := f.Job(t, "counter", f.Clock.Now().Add(time.Hour))
	ctx := context.Background()
	for n := range 3 {
		run, err := f.Store.History().UpsertRun(ctx, historygen.UpsertRunParams{ID: []string{"run_a", "run_b", "run_c"}[n], JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: f.Clock.Now().Add(time.Duration(n-3) * time.Second).UnixMilli(), Trigger: "schedule", Status: "failed"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Store.History().CompleteRun(ctx, historygen.CompleteRunParams{ID: run.ID, WorkspaceID: job.WorkspaceID, Status: "failed", Attempts: 1, FinishedAt: sql.NullInt64{Int64: f.Clock.Now().Add(time.Duration(n-3) * time.Second).UnixMilli(), Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
	}
	r := rec(f)
	r.Config.Scheduler.LeaseDuration = config.Duration(time.Minute)
	f.Clock.Advance(10 * time.Minute)
	f.Run(t, func(ctx context.Context) error { return r.RecoverCounters(ctx) })
	updated, err := f.Store.ControlReader().GetJob(ctx, sqlitegen.GetJobParams{ID: job.ID, WorkspaceID: job.WorkspaceID})
	if err != nil || updated.ConsecutiveFailures != 3 || updated.LastRunStatus.String != "failed" {
		t.Fatal(updated, err)
	}
	_, err = f.Store.Control().ChangeJobState(ctx, sqlitegen.ChangeJobStateParams{ID: job.ID, WorkspaceID: job.WorkspaceID, Version: 1, State: "active", ConsecutiveFailures: 0, ResetFailures: 1, Now: f.Clock.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	f.Run(t, func(ctx context.Context) error { return r.RecoverCounters(ctx) })
	updated, err = f.Store.ControlReader().GetJob(ctx, sqlitegen.GetJobParams{ID: job.ID, WorkspaceID: job.WorkspaceID})
	if err != nil || updated.ConsecutiveFailures != 0 || updated.FailuresResetVersion != 2 {
		t.Fatal("re-enable reset did not survive recovery", updated, err)
	}
}

func TestRunningRunSurvivesLostLeaseIndex(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testRunningRunSurvivesLostLeaseIndex(t)
		})
	}
}
func testRunningRunSurvivesLostLeaseIndex(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	j := f.Job(t, "lost_inflight", f.Clock.Now().Add(time.Hour))
	logical := f.Clock.Now().Add(-time.Minute)
	_, err := f.Store.History().UpsertRun(context.Background(), historygen.UpsertRunParams{ID: "run_orphan", JobID: j.ID, WorkspaceID: j.WorkspaceID, ScheduledAt: logical.UnixMilli(), Trigger: "schedule", JobVersion: 1, Status: "running", ClaimedAt: sql.NullInt64{Int64: logical.UnixMilli(), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	r := rec(f)
	f.Run(t, func(ctx context.Context) error { return r.Check(ctx) })
	o := queue.Occurrence{JobID: j.ID, ScheduledAt: logical, Version: 1, Trigger: "s"}
	present, err := f.Queue.Contains(context.Background(), []string{o.Format()})
	if err != nil || !present[0] {
		t.Fatal("running occurrence lost after queue rebuild", present, err)
	}
}

func TestFastDeletedOrphanRetainsHistory(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncommitted", true: "already_succeeded"}[terminal], func(t *testing.T) {
			f := testutil.New(t)
			f.Config.Scheduler.DeliveryMode = "fast"
			f.StartWriter(t)
			job := f.Job(t, "deleted_orphan", f.Clock.Now())
			o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
			ctx := context.Background()
			key := worker.OccurrenceRunID(o)
			if terminal {
				key = "run_existing"
				if _, err := f.Store.History().UpsertRun(ctx, historygen.UpsertRunParams{ID: key, JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule", Status: "succeeded"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.Queue.Add(ctx, o.Format(), o.ScheduledAt, false); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Queue.Claim(ctx, f.Clock.Now(), 1, time.Minute); err != nil {
				t.Fatal(err)
			}
			if _, err := f.DBs.Control.Writer.ExecContext(ctx, `UPDATE jobs SET deleted_at=?,version=version+1,next_run_at=NULL WHERE id=?`, f.Clock.Now().UnixMilli(), job.ID); err != nil {
				t.Fatal(err)
			}
			r := rec(f)
			for range 2 {
				f.Run(t, r.RecoverInflight)
			}
			if _, err := f.Queue.RequeueAll(ctx, f.Clock.Now()); err != nil {
				t.Fatal(err)
			}
			f.Run(t, func(ctx context.Context) error { return r.Full(ctx, true) })
			run, err := f.Store.HistoryReader().GetRun(ctx, historygen.GetRunParams{ID: key, WorkspaceID: job.WorkspaceID})
			if err != nil {
				t.Fatal(err)
			}
			if terminal {
				if run.Status != "succeeded" {
					t.Fatal(run)
				}
			} else if run.Status != "cancelled" || !strings.Contains(run.LastError.String, "job deleted before recovery; delivery may have occurred") || run.ClaimedAt.Valid {
				t.Fatal(run)
			}
			stats, err := f.Queue.Stats(ctx)
			if err != nil || stats.Due != 0 || stats.Inflight != 0 {
				t.Fatal(stats, err)
			}
		})
	}
}

func TestFastPendingHistorySurvivesQueueLossAndMisfire(t *testing.T) {
	f := testutil.New(t)
	f.Config.Scheduler.DeliveryMode = "fast"
	f.StartWriter(t)
	job := f.Job(t, "pending_queue_loss", f.Clock.Now())
	at := f.Clock.Now()
	ctx := context.Background()
	if _, err := f.Store.History().UpsertRun(ctx, historygen.UpsertRunParams{ID: "run_pending", JobID: job.ID, WorkspaceID: job.WorkspaceID, JobVersion: 1, ScheduledAt: at.UnixMilli(), Trigger: "schedule", Status: "deferred", NextAttemptAt: sql.NullInt64{Int64: at.UnixMilli(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	f.Clock.Advance(10 * time.Minute)
	if _, err := f.DBs.Control.Writer.ExecContext(ctx, `UPDATE jobs SET misfire_policy='skip'`); err != nil {
		t.Fatal(err)
	}
	f.Run(t, func(ctx context.Context) error { return rec(f).Full(ctx, true) })
	run, err := f.Store.HistoryReader().GetRun(ctx, historygen.GetRunParams{ID: "run_pending", WorkspaceID: job.WorkspaceID})
	if err != nil || run.Status != "deferred" {
		t.Fatal(run, err)
	}
	member := queue.Occurrence{JobID: job.ID, ScheduledAt: at, Version: 1, Trigger: "s"}.Format()
	present, err := f.Queue.Contains(ctx, []string{member})
	if err != nil || !present[0] {
		t.Fatal(present, err)
	}
}
