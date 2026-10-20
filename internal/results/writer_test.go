package results_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/testutil"
)

type failingStore struct {
	store.Store
	fail  atomic.Bool
	tries atomic.Int64
}

func (s *failingStore) WithTx(ctx context.Context, db store.Database, fn func(store.Tx) error) error {
	s.tries.Add(1)
	if s.fail.Load() {
		return errors.New("simulated SQLite busy")
	}
	return s.Store.WithTx(ctx, db, fn)
}
func TestRetryBackpressureAndDrain(t *testing.T) {
	f := testutil.New(t)
	s := &failingStore{Store: f.Store}
	s.fail.Store(true)
	f.Writer.Store = s
	f.StartWriter(t)
	var applied atomic.Int64
	future, err := f.Writer.Submit(context.Background(), store.Control, func(context.Context, store.Tx) error { applied.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.Until(t, func() bool { return f.Writer.Backpressure() })
	if applied.Load() != 0 {
		t.Fatal("applied during SQLite failure")
	}
	s.fail.Store(false)
	f.Run(t, func(ctx context.Context) error { return future.Wait(ctx) })
	if applied.Load() != 1 || f.Writer.Backpressure() {
		t.Fatal(applied.Load(), f.Writer.Backpressure())
	}
}
func TestCancelledCommandDoesNotPoisonBatch(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	future, err := f.Writer.Submit(context.Background(), store.Control, func(context.Context, store.Tx) error { return context.Canceled })
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int64
	valid, err := f.Writer.Submit(context.Background(), store.Control, func(context.Context, store.Tx) error { count.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.Run(t, func(ctx context.Context) error { return valid.Wait(ctx) })
	if err = future.Wait(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if count.Load() < 1 {
		t.Fatal("valid command lost")
	}
}
func TestRecentRunTTL(t *testing.T) {
	f := testutil.New(t)
	job := f.Job(t, "recent", f.Clock.Now())
	run, err := f.Store.History().UpsertRun(context.Background(), historygen.UpsertRunParams{ID: "run_recent", JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: f.Clock.Now().UnixMilli(), Trigger: "schedule", Status: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	f.Writer.Remember(run)
	if _, ok := f.Writer.RecentOccurrence(job.ID, run.ScheduledAt, run.Trigger); !ok {
		t.Fatal("missing cache")
	}
	f.Clock.Advance(2*f.Config.Scheduler.LeaseDuration.Duration() + time.Millisecond)
	if _, ok := f.Writer.Recent(run.ID); ok {
		t.Fatal("stale cache")
	}
	if _, err := f.Writer.LoadRun(context.Background(), run.ID, run.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}
