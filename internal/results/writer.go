// Package results serializes and batches hot-path writes independently per database.
package results

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sync/errgroup"

	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/telemetry"
)

type command struct {
	apply func(context.Context, store.Tx) error
	done  chan error
	solo  bool
}

// Future resolves only after its batch commits; callers may abandon their wait safely.
type Future struct{ done <-chan error }

func (f Future) Wait(ctx context.Context) error {
	select {
	case e := <-f.done:
		return e
	case <-ctx.Done():
		return ctx.Err()
	}
}

type recent struct {
	run     historygen.Run
	expires time.Time
}
type Writer struct {
	FlushInterval    time.Duration
	MaxBatch         int
	Store            store.Store
	Clock            clockwork.Clock
	Metrics          *telemetry.Instruments
	control, history chan command
	failed           [2]atomic.Bool
	mu               sync.Mutex
	recent           map[string]recent
	TTL              time.Duration
	nextPrune        time.Time
}

func New(s store.Store, c clockwork.Clock, m *telemetry.Instruments, lease time.Duration) *Writer {
	return &Writer{MaxBatch: 500, Store: s, Clock: c, Metrics: m, control: make(chan command, 10000), history: make(chan command, 10000), recent: map[string]recent{}, TTL: 2 * lease}
}
func (w *Writer) Submit(ctx context.Context, db store.Database, apply func(context.Context, store.Tx) error) (Future, error) {
	ch := w.control
	if db == store.History {
		ch = w.history
	}
	done := make(chan error, 1)
	select {
	case ch <- command{apply: apply, done: done}:
		return Future{done}, nil
	case <-ctx.Done():
		return Future{}, ctx.Err()
	}
}
func (w *Writer) Do(ctx context.Context, db store.Database, apply func(context.Context, store.Tx) error) error {
	f, e := w.Submit(ctx, db, apply)
	if e != nil {
		return e
	}
	return f.Wait(ctx)
}

// Busy reports pressure before maintenance is admitted.
func (w *Writer) Busy(db store.Database) bool {
	ch := w.control
	if db == store.History {
		ch = w.history
	}
	return len(ch) > cap(ch)/2
}

// Maintain executes one short transaction, never sharing a result batch.
func (w *Writer) Maintain(ctx context.Context, db store.Database, apply func(context.Context, store.Tx) error) error {
	ch := w.control
	if db == store.History {
		ch = w.history
	}
	done := make(chan error, 1)
	select {
	case ch <- command{apply: apply, done: done, solo: true}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return (Future{done}).Wait(ctx)
}
func (w *Writer) Backpressure() bool {
	return (w.failed[0].Load() || w.failed[1].Load()) || len(w.control) == cap(w.control) || len(w.history) == cap(w.history)
}

// Close is called only after all producers have stopped. Run drains both queues.
func (w *Writer) Close() { close(w.control); close(w.history) }
func (w *Writer) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return w.loop(ctx, store.Control, w.control) })
	g.Go(func() error { return w.loop(ctx, store.History, w.history) })
	return g.Wait()
}
func (w *Writer) loop(ctx context.Context, db store.Database, ch <-chan command) error {
	index := 0
	if db == store.History {
		index = 1
	}
	batch := make([]command, 0, w.MaxBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		start := w.Clock.Now()
		delay := 10 * time.Millisecond
		for {
			// DECISION: cancellation belongs to the batch transaction. SQL callbacks
			// receive its values without individual Done channels, avoiding one driver
			// watchdog goroutine per statement. busy_timeout bounds lock waits; cancelling
			// the owning transaction rolls it back before another batch can commit.
			bad := -1
			e := w.Store.WithTx(ctx, db, func(tx store.Tx) error {
				for n, c := range batch {
					if e := c.apply(context.WithoutCancel(ctx), tx); e != nil {
						bad = n
						return e
					}
				}
				return nil
			})
			if e == nil {
				w.failed[index].Store(false)
				w.Metrics.Record(ctx, "cronpad.results.batch_size", float64(len(batch)), attribute.String("db", string(db)))
				w.Metrics.Record(ctx, "cronpad.results.flush_duration", float64(w.Clock.Since(start).Microseconds())/1000, attribute.String("db", string(db)))
				for _, c := range batch {
					c.done <- nil
				}
				batch = batch[:0]
				return nil
			}
			// A cancelled producer must not poison unrelated commands in its batch.
			if bad >= 0 && (errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) || errors.Is(e, sql.ErrNoRows)) {
				batch[bad].done <- e
				batch = append(batch[:bad], batch[bad+1:]...)
				if len(batch) == 0 {
					w.failed[index].Store(false)
					return nil
				}
				continue
			}
			w.failed[index].Store(true)
			w.Metrics.Add(ctx, "cronpad.db.errors", 1, attribute.String("op", "flush"))
			select {
			case <-ctx.Done():
				for _, c := range batch {
					c.done <- ctx.Err()
				}
				return fmt.Errorf("flush %s: %w", db, ctx.Err())
			case <-w.Clock.After(delay):
			}
			delay = min(2*delay, time.Second)
		}
	}
	// add batches c, committing around solo commands and at MaxBatch.
	add := func(c command) error {
		if c.solo {
			if e := flush(); e != nil {
				return e
			}
		}
		batch = append(batch, c)
		if c.solo || len(batch) == w.MaxBatch {
			return flush()
		}
		return nil
	}
	cancelled := func() error {
		for _, c := range batch {
			c.done <- ctx.Err()
		}
		return ctx.Err()
	}
	if w.FlushInterval <= 0 {
		// DECISION: group commit. An idle writer commits a command immediately;
		// commands that arrive while a commit runs share the next transaction, so
		// batches grow with load instead of every write waiting for a ticker.
		for {
			select {
			case c, ok := <-ch:
				if !ok {
					return flush()
				}
				if e := add(c); e != nil {
					return e
				}
			case <-ctx.Done():
				return cancelled()
			}
			for drained := false; !drained; {
				select {
				case c, ok := <-ch:
					if !ok {
						return flush()
					}
					if e := add(c); e != nil {
						return e
					}
				default:
					drained = true
				}
			}
			if e := flush(); e != nil {
				return e
			}
		}
	}
	timer := w.Clock.NewTicker(w.FlushInterval)
	defer timer.Stop()
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return flush()
			}
			if e := add(c); e != nil {
				return e
			}
		case <-timer.Chan():
			if e := flush(); e != nil {
				return e
			}
		case <-ctx.Done():
			return cancelled()
		}
	}
}
func (w *Writer) Remember(run historygen.Run) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.Clock.Now()
	if !now.Before(w.nextPrune) {
		for key, v := range w.recent {
			if !now.Before(v.expires) {
				delete(w.recent, key)
			}
		}
		w.nextPrune = now.Add(w.TTL / 2)
	}
	w.recent[run.ID] = recent{run, now.Add(w.TTL)}
	w.recent[occurrenceKey(run.JobID, run.ScheduledAt, run.Trigger)] = recent{run, now.Add(w.TTL)}
}
func (w *Writer) Recent(id string) (historygen.Run, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.recent[id]
	if ok && !w.Clock.Now().Before(r.expires) {
		delete(w.recent, id)
		ok = false
	}
	return r.run, ok
}

func occurrenceKey(job string, at int64, trigger string) string {
	return fmt.Sprintf("occ:%s:%d:%s", job, at, trigger)
}
func (w *Writer) RecentOccurrence(job string, at int64, trigger string) (historygen.Run, bool) {
	return w.Recent(occurrenceKey(job, at, trigger))
}
func (w *Writer) LoadRun(ctx context.Context, id, workspace string) (historygen.Run, error) {
	if run, ok := w.Recent(id); ok && run.WorkspaceID == workspace {
		return run, nil
	}
	return w.Store.HistoryReader().GetRun(ctx, historygen.GetRunParams{ID: id, WorkspaceID: workspace})
}
