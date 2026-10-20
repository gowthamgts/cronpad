package worker_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/reconciler"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/testutil"
	"github.com/gowthamgts/cronpad/internal/worker"
)

// A stopped writer models SIGKILL losing the Go channel before any commit.
func TestFastCrashAfterEnqueueBeforeCommit(t *testing.T) {
	f := testutil.New(t)
	f.Engine.Config.Scheduler.DeliveryMode = "fast"
	f.Config.Scheduler.DeliveryMode = "fast"
	job := f.Job(t, "fast_crash", f.Clock.Now())
	o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
	ctx := context.Background()
	if err := f.Queue.Add(ctx, o.Format(), o.ScheduledAt, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Queue.Claim(ctx, f.Clock.Now(), 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	keys := make(chan string, 3)
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(_ context.Context, req sink.ExecRequest) sink.ExecResult {
		keys <- req.IdempotencyKey
		return sink.ExecResult{Outcome: sink.Success, Code: 200}
	})
	firstCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- f.Engine.Process(firstCtx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()}) }()
	var first string
	select {
	case first = <-keys:
	case <-clockwork.NewRealClock().After(5 * time.Second):
		t.Fatal("fast delivery waited for commit")
	}
	var rows int
	if err := f.DBs.History.Reader.QueryRowContext(ctx, `SELECT count(*) FROM runs`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal(rows, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Restart loses the old writer and its cache, then startup requeues inflight.
	f.Writer = results.New(f.Store, f.Clock, f.Metrics, time.Minute)
	f.Engine.Writer = f.Writer
	f.StartWriter(t)
	// Downtime exceeds the misfire threshold. A claimed delivery must survive
	// even the skip policy; quota remains unconsumed until actual admission.
	f.Clock.Advance(10 * time.Minute)
	if _, err := f.DBs.Control.Writer.ExecContext(ctx, `UPDATE jobs SET misfire_policy='skip'`); err != nil {
		t.Fatal(err)
	}
	r := &reconciler.Reconciler{Store: f.Store, Queue: f.Queue, Writer: f.Writer, Clock: f.Clock, Config: f.Config, Metrics: f.Metrics}
	f.Run(t, r.RecoverInflight)
	reservation, err := f.Store.HistoryReader().FindOccurrence(ctx, historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule"})
	if err != nil || reservation.ClaimedAt.Valid || reservation.StartedAt.Valid || reservation.QuotaDay.Valid {
		t.Fatal(reservation, err)
	}
	var usage int
	if err := f.DBs.History.Reader.QueryRowContext(ctx, `SELECT coalesce(sum(runs),0) FROM workspace_usage_monthly`).Scan(&usage); err != nil || usage != 0 {
		t.Fatal(usage, err)
	}
	if _, err := f.Queue.RequeueAll(ctx, f.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	f.Run(t, func(ctx context.Context) error { return r.Full(ctx, true) })
	items, err := f.Queue.Claim(ctx, f.Clock.Now(), 1, time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	f.Run(t, func(ctx context.Context) error {
		return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
	})
	second := <-keys
	if first != second || first != worker.OccurrenceRunID(o) {
		t.Fatalf("keys changed: %s %s", first, second)
	}
	var success int
	var stored sql.NullString
	if err := f.DBs.History.Reader.QueryRowContext(ctx, `SELECT count(*),sum(status='succeeded'),max(id) FROM runs`).Scan(&rows, &success, &stored); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || success != 1 || stored.String != first || len(keys) != 0 {
		t.Fatal(rows, success, stored, len(keys))
	}
	if err := f.DBs.History.Reader.QueryRowContext(ctx, `SELECT coalesce(sum(runs),0) FROM workspace_usage_monthly`).Scan(&usage); err != nil || usage != 1 {
		t.Fatal(usage, err)
	}
	// A committed final row still suppresses a subsequent redelivery.
	f.Run(t, func(ctx context.Context) error {
		return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
	})
	if len(keys) != 0 {
		t.Fatal("final run redelivered")
	}
	t.Logf("enqueue-before-commit + 10min downtime + skip-policy recovery: rows=%d succeeded=%d deliveries=2 stable_key=%s", rows, success, first)
}

func TestOccurrenceRunIdentity(t *testing.T) {
	for _, at := range []time.Time{time.UnixMilli(-1), time.UnixMilli(0), time.UnixMilli(1792000000000), time.UnixMilli(1 << 60)} {
		o := queue.Occurrence{JobID: "job_identity", ScheduledAt: at, Version: 1, Trigger: "s"}
		first := worker.OccurrenceRunID(o)
		o.Attempt = 3
		o.Version = 9
		if worker.OccurrenceRunID(o) != first {
			t.Fatal("retry/version changed occurrence identity")
		}
		o.Trigger = "m"
		if worker.OccurrenceRunID(o) == first {
			t.Fatal("trigger did not change identity")
		}
	}
}

func TestFastFinishWaitsForScheduleAdvance(t *testing.T) {
	f := testutil.New(t)
	f.Engine.Config.Scheduler.DeliveryMode = "fast"
	f.Engine.Config.AutoDisable.ConsecutiveFailures = 1
	f.StartWriter(t)
	job := f.Job(t, "advance_before_disable", f.Clock.Now())
	o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		return sink.ExecResult{Outcome: sink.Fatal, Code: 400}
	})
	advanced := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var processErr error
	go func() {
		defer close(done)
		processErr = f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now(), ScheduleAdvanced: advanced})
	}()
	t.Cleanup(func() { cancel(); <-done })
	f.Until(t, func() bool {
		run, err := f.Store.HistoryReader().FindOccurrence(ctx, historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule"})
		return err == nil && run.Status == "failed"
	})
	current, err := f.Store.ControlReader().DispatchJob(ctx, job.ID)
	if err != nil || current.State != "active" || current.OccurrenceCount != 0 {
		t.Fatal(current, err)
	}
	select {
	case <-done:
		t.Fatal("finished before schedule advanced")
	default:
	}
	f.Run(t, func(ctx context.Context) error {
		return f.Writer.Do(ctx, store.Control, func(ctx context.Context, tx store.Tx) error {
			n, err := tx.Control.AdvanceJob(ctx, sqlitegen.AdvanceJobParams{ID: job.ID, WorkspaceID: job.WorkspaceID, Version: 1, ScheduledAt: sql.NullInt64{Int64: o.ScheduledAt.UnixMilli(), Valid: true}, NextRunAt: sql.NullInt64{Int64: o.ScheduledAt.Add(time.Hour).UnixMilli(), Valid: true}, State: "active"})
			if err == nil && n != 1 {
				return fmt.Errorf("advance rows=%d", n)
			}
			return err
		})
	})
	advanced <- nil
	f.Until(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	if processErr != nil {
		t.Fatal(processErr)
	}
	current, err = f.Store.ControlReader().DispatchJob(ctx, job.ID)
	if err != nil || current.State != "disabled" || current.OccurrenceCount != 1 {
		t.Fatal(current, err)
	}
}
