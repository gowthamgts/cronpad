// Package dispatcher owns leadership, bounded claims and ordered pipeline shutdown.
package dispatcher

import (
	"container/list"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sync/errgroup"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/events"
	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/reconciler"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/schedule"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/telemetry"
	"github.com/gowthamgts/cronpad/internal/worker"
)

// Dispatcher is single-use; all child goroutines are joined by Run.
type Dispatcher struct {
	deferred   map[string]bool
	inflightMu sync.Mutex
	inflight   map[string]int

	renewCancel context.CancelFunc
	renewDone   chan struct{}
	Store       store.Store
	Queue       queue.Queue
	Writer      *results.Writer
	Workers     *worker.Engine
	Clock       clockwork.Clock
	Config      config.Config
	Metrics     *telemetry.Instruments
	Log         *slog.Logger
	Wake        <-chan struct{}
	state       atomic.Value
	held        atomic.Bool
	busy        atomic.Int64
	idle        chan struct{}
	cache       map[string]*list.Element
	lru         *list.List
}
type cached struct {
	freshAt  sql.NullInt64
	key      string
	job      sqlitegen.Job
	schedule schedule.Schedule
}

// State reports starting, standby, active or stopped without blocking on the queue.
func (d *Dispatcher) State() string {
	v := d.state.Load()
	if v == nil {
		return "starting"
	}
	return v.(string)
}
func (d *Dispatcher) setState(s string) {
	d.state.Store(s)
	held := float64(0)
	if d.held.Load() {
		held = 1
	}
	d.Metrics.Gauge("cronpad.dispatcher.lock_held", held)
}

// Ready checks leadership, heartbeat freshness, queue connectivity and writer pressure.
func (d *Dispatcher) Ready(ctx context.Context) (map[string]any, bool) {
	out := map[string]any{"dispatcher": d.State()}
	err := d.Queue.Ping(ctx)
	out["queue"] = err == nil
	var hb time.Time
	var herr error
	if err == nil {
		hb, herr = d.Queue.Heartbeat(ctx)
	} else {
		herr = err
	}
	fresh := herr == nil && !hb.IsZero() && d.Clock.Since(hb) >= 0 && d.Clock.Since(hb) < 5*d.Config.Scheduler.TickInterval.Duration()
	out["heartbeat"] = hb
	out["writer_backpressure"] = d.Writer.Backpressure()
	return out, err == nil && fresh && d.State() == "active" && !d.Writer.Backpressure()
}

// Run starts the owned writer/pool, cycles leadership, then drains before releasing its lock.
func (d *Dispatcher) Run(ctx context.Context) error {
	if d.Workers.Cache == nil {
		d.Workers.Cache = worker.NewJobCache(d.Store, 10000)
	}
	// DECISION: cap the admission crash window at 50ms even for slow flush configurations.
	d.Writer.FlushInterval = min(50*time.Millisecond, d.Config.Scheduler.ResultsFlushInterval.Duration())
	d.Writer.MaxBatch = d.Config.Scheduler.ResultsMaxBatch
	d.Log.InfoContext(ctx, "scheduler delivery mode", logx.DeliveryMode, d.Config.Scheduler.DeliveryMode)
	d.idle = make(chan struct{}, 1)
	d.cache = map[string]*list.Element{}
	d.lru = list.New()
	d.setState("starting")
	writerCtx, cancelWriter := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWriter()
	writerDone := make(chan error, 1)
	go func() { writerDone <- d.Writer.Run(writerCtx) }()
	workCtx, cancelWorkers := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWorkers()
	pool, _ := errgroup.WithContext(workCtx)
	workQ := make(chan worker.Work, min(d.Config.Workers.Count, d.Config.Workers.QueueSize))
	for range d.Config.Workers.Count {
		pool.Go(func() error {
			for w := range workQ {
				err := d.Workers.Process(workCtx, w)
				if w.ConcurrencyReserved {
					d.release(w.WorkspaceID)
				}
				if err != nil && !errors.Is(err, context.Canceled) {
					d.Log.ErrorContext(logx.With(workCtx, logx.JobID, w.Occurrence.JobID, logx.Occ, w.Occurrence.Format()), "run processing failed", logx.Error, err)
				}
				if d.busy.Add(-1) == 0 {
					select {
					case d.idle <- struct{}{}:
					default:
					}
				}
			}
			return nil
		})
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		close(workQ)
		_ = pool.Wait()
		d.Writer.Close()
		<-writerDone
		return fmt.Errorf("lock token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	loopErr := d.lead(ctx, token, workQ)
	close(workQ)
	poolDone := make(chan error, 1)
	go func() { poolDone <- pool.Wait() }()
	timer := d.Clock.NewTimer(d.Config.Server.ShutdownGrace.Duration())
	select {
	case <-poolDone:
	case <-timer.Chan():
		cancelWorkers()
		<-poolDone
	}
	timer.Stop()
	d.Writer.Close()
	timer = d.Clock.NewTimer(d.Config.Server.ShutdownGrace.Duration())
	select {
	case err := <-writerDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			loopErr = errors.Join(loopErr, err)
		}
	case <-timer.Chan():
		cancelWriter()
		<-writerDone
	}
	timer.Stop()
	d.stopRenew()
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	_, _ = d.Queue.Release(release, token)
	cancel()
	d.held.Store(false)
	d.setState("stopped")
	if errors.Is(loopErr, context.Canceled) {
		return nil
	}
	return loopErr
}
func (d *Dispatcher) lead(ctx context.Context, token string, workQ chan<- worker.Work) error {
	for ctx.Err() == nil {
		ok, err := d.Queue.Acquire(ctx, token, d.Config.Scheduler.LockTTL.Duration())
		if err != nil || !ok {
			d.setState("standby")
			if err != nil {
				d.queueError(ctx, "acquire", err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-d.Clock.After(time.Second):
			}
			continue
		}
		d.held.Store(true)
		d.setState("starting")
		session, cancel := context.WithCancel(ctx)

		d.startRenew(ctx, token, cancel)
		err = d.session(session, token, workQ)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d.stopRenew()
		d.held.Store(false)
		d.setState("standby")
		if err != nil && ctx.Err() == nil {
			d.Log.WarnContext(ctx, "dispatcher session interrupted", logx.Error, err)
		}
		// Finish work from the previous tenure before orphan recovery can requeue it.
		for d.busy.Load() != 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-d.idle:
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, _ = d.Queue.Release(ctx, token)
	}
	return ctx.Err()
}
func (d *Dispatcher) session(ctx context.Context, token string, workQ chan<- worker.Work) error {
	r := &reconciler.Reconciler{Store: d.Store, Queue: d.Queue, Writer: d.Writer, Clock: d.Clock, Config: d.Config, Metrics: d.Metrics, Cache: d.Workers.Cache, Events: d.Workers.Events}
	if d.Config.Scheduler.DeliveryMode == "fast" {
		if err := r.RecoverInflight(ctx); err != nil {
			return err
		}
	}
	if _, err := d.Queue.RequeueAll(ctx, d.Clock.Now()); err != nil {
		return err
	}
	epochBytes := make([]byte, 24)
	if _, err := rand.Read(epochBytes); err != nil {
		return err
	}
	epoch := hex.EncodeToString(epochBytes)
	if err := d.Queue.SetEpoch(ctx, epoch); err != nil {
		return err
	}
	r.Epoch = epoch
	if err := r.RecoverCounters(ctx); err != nil {
		return err
	}
	if err := r.Full(ctx, true); err != nil {
		return err
	}
	d.cache = map[string]*list.Element{}
	d.lru.Init()
	d.setState("active")
	reap := d.Clock.NewTicker(d.Config.Scheduler.ReaperInterval.Duration())
	defer reap.Stop()
	reconcile := d.Clock.NewTicker(d.Config.Scheduler.ReconcileInterval.Duration())
	defer reconcile.Stop()
	full := d.Clock.NewTicker(d.Config.Scheduler.FullReconcileInterval.Duration())
	defer full.Stop()
	sample := d.Clock.NewTicker(5 * time.Second)
	defer sample.Stop()
	timer := d.Clock.NewTimer(0)
	defer timer.Stop()
	lastHeartbeat, _ := d.Queue.Heartbeat(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reap.Chan():
			members, err := d.Queue.ReapAndList(ctx, d.Clock.Now(), 1000)
			if err != nil {
				return err
			}
			if len(members) > 0 {
				d.Metrics.Add(ctx, "cronpad.leases.expired", int64(len(members)))
				for _, member := range members {
					o, err := queue.ParseOccurrence(member)
					if err != nil {
						continue
					}
					job, err := d.Workers.Cache.Get(ctx, o.JobID, o.Version)
					workspace := job.WorkspaceID
					if errors.Is(err, sql.ErrNoRows) {
						workspace, err = d.Store.HistoryReader().OccurrenceWorkspace(ctx, historygen.OccurrenceWorkspaceParams{JobID: o.JobID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: worker.TriggerName(o.Trigger)})
					}
					if errors.Is(err, sql.ErrNoRows) {
						continue
					}
					if err != nil {
						return err
					}
					d.Workers.Events.Publish(ctx, events.Event{Type: events.LeaseExpired, WorkspaceID: workspace, JobID: o.JobID, At: d.Clock.Now()})
				}
			}

		case <-reconcile.Chan():
			if err := r.Check(ctx); err != nil {
				return err
			}
		case <-full.Chan():
			if err := r.Full(ctx, false); err != nil {
				return err
			}
		case <-sample.Chan():
			if err := d.sample(ctx); err != nil {
				return err
			}
		case <-d.Wake:
		case <-timer.Chan():
		}
		if !d.held.Load() {
			return fmt.Errorf("dispatcher lock lost")
		}
		start := d.Clock.Now()
		if !lastHeartbeat.IsZero() && start.Before(lastHeartbeat) {
			d.Log.WarnContext(ctx, "clock moved backwards", logx.ScheduledAt, start)
		}
		// DECISION: verify ownership immediately before each claim as well as renewing
		// independently. A wiped queue or token replacement must not permit another claim.
		ok, err := d.Queue.Renew(ctx, token, d.Config.Scheduler.LockTTL.Duration())
		if err != nil || !ok {
			d.held.Store(false)
			return fmt.Errorf("dispatcher ownership lost")
		}
		free := min(d.Config.Workers.Count-int(d.busy.Load()), cap(workQ)-len(workQ))
		delay := d.Config.Scheduler.TickInterval.Duration()
		limit := min(free, d.Config.Scheduler.ClaimBatch)
		if limit <= 0 || d.Writer.Backpressure() {
			d.Metrics.Add(ctx, "cronpad.dispatcher.backpressure", 1)
		} else {
			items, err := d.Queue.Claim(ctx, start, limit, d.Config.Scheduler.LeaseDuration.Duration())
			if err != nil {
				return err
			}
			d.Metrics.Add(ctx, "cronpad.dispatcher.claimed", int64(len(items)))
			if err = d.deliver(ctx, items, start, workQ); err != nil {
				return err
			}
			if len(items) == limit {
				delay = 0
			} else {
				next, ok, err := d.Queue.Peek(ctx)
				if err != nil {
					return err
				}
				if ok {
					delay = max(0, min(delay, next.Sub(d.Clock.Now())))
				}
			}
		}
		if err = d.Queue.SetHeartbeat(ctx, d.Clock.Now()); err != nil {
			return err
		}
		lastHeartbeat = d.Clock.Now()
		d.Metrics.Record(ctx, "cronpad.dispatcher.tick_duration", float64(d.Clock.Since(start).Microseconds())/1000)
		d.Log.DebugContext(ctx, "dispatcher tick", logx.FreeSlots, free, logx.TickMS, d.Clock.Since(start).Milliseconds())
		timer.Reset(delay)
	}
}
func (d *Dispatcher) load(ctx context.Context, o queue.Occurrence) (sqlitegen.Job, schedule.Schedule, error) {
	// The shared configuration cache is invalidated by committed API edits. Check
	// it even on a parsed-schedule hit so stale LRU entries cannot create run rows.
	j, err := d.Workers.Cache.Get(ctx, o.JobID, o.Version)
	if err != nil {
		return j, nil, err
	}
	if j.Version != o.Version || j.State == "paused" || j.State == "disabled" {
		return j, nil, nil
	}
	key := fmt.Sprintf("%s:%d", o.JobID, o.Version)
	if element := d.cache[key]; element != nil {
		d.lru.MoveToFront(element)
		return j, element.Value.(cached).schedule, nil
	}
	s, err := reconciler.Parse(ctx, j)
	if err != nil {
		return j, nil, err
	}
	element := d.lru.PushFront(cached{key: key, job: j, schedule: s})
	d.cache[key] = element
	if d.lru.Len() > 10000 {
		last := d.lru.Back()
		delete(d.cache, last.Value.(cached).key)
		d.lru.Remove(last)
	}
	return j, s, nil
}

func (d *Dispatcher) deliver(ctx context.Context, items []queue.Item, claimed time.Time, ch chan<- worker.Work) (deliveryErr error) {
	type pending struct {
		reason        string
		counted       bool
		quotaReserved bool

		o                 queue.Occurrence
		j                 sqlitegen.Job
		history, control  results.Future
		next              time.Time
		ok, advance, sent bool
		advanced          chan error
		rows              *int64
	}
	wait := []pending{}
	workspaceSuspensions := map[string]bool{}
	defer func() {
		for _, p := range wait {
			if deliveryErr != nil && !p.sent {
				if p.counted {
					d.release(p.j.WorkspaceID)
				}
				if d.Workers.Limits != nil {
					d.Workers.Limits.Release(worker.OccurrenceRunID(p.o))
				}
			}
			if p.advanced != nil {
				p.advanced <- deliveryErr
			}
		}
	}()
	for _, item := range items {
		o, err := queue.ParseOccurrence(item.Member)
		if err != nil {
			d.Metrics.Add(ctx, "cronpad.queue.errors", 1, attribute.String("op", "malformed_member"))
			d.Log.ErrorContext(ctx, "malformed queue member", logx.Occ, item.Member, logx.Error, err)
			delete(d.deferred, item.Member)
			if err = d.Queue.Ack(ctx, item.Member, "", time.Time{}); err != nil {
				return err
			}
			continue
		}
		j, s, err := d.load(ctx, o)
		if errors.Is(err, sql.ErrNoRows) {
			delete(d.deferred, item.Member)
			if err = d.Queue.Ack(ctx, item.Member, "", time.Time{}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if j.Version != o.Version || j.State == "paused" && o.Trigger == "s" || j.State == "disabled" {
			delete(d.deferred, item.Member)
			wait = append(wait, pending{o: o})
			continue
		}
		suspended, known := workspaceSuspensions[j.WorkspaceID]
		if !known {
			var err error
			suspended, err = store.Suspended(ctx, d.Store, j.WorkspaceID)
			if err != nil {
				return err
			}
			workspaceSuspensions[j.WorkspaceID] = suspended
		}
		reason := ""
		counted := false
		quotaReserved := false
		if suspended {
			reason = "workspace_suspended"
		}
		if reason == "" && d.Workers.Limits != nil {
			set, err := d.Workers.Limits.For(ctx, j.WorkspaceID)
			if err != nil {
				return err
			}
			reservedNew := false
			if o.Attempt == 0 && set.MaxRunsPerMonth > 0 && set.OverQuota != limits.Allow {
				fresh, err := d.newAdmission(ctx, j, o)
				if err != nil {
					return err
				}
				if fresh {
					var err error
					reservedNew, err = d.Workers.Limits.ReserveNew(ctx, j.WorkspaceID, worker.OccurrenceRunID(o))
					quotaReserved = err == nil
					if err != nil {
						var quota *limits.Error
						if !errors.As(err, &quota) {
							return err
						}
						reason = "quota_exceeded"
					}
				}
			}
			if reason == "" && !d.acquire(j.WorkspaceID, set.MaxConcurrentRuns) {
				if reservedNew {
					d.Workers.Limits.Release(worker.OccurrenceRunID(o))
				}
				d.Metrics.Add(ctx, "cronpad.dispatcher.concurrency_deferred", 1)
				// DECISION: keep deferral metadata in memory until admission. Re-claims
				// perform no history writes; a crash before admission may lose the SLA exclusion.
				if d.deferred == nil {
					d.deferred = map[string]bool{}
				}
				d.deferred[item.Member] = true
				if err := d.Queue.Defer(ctx, item.Member, claimed.Add(250*time.Millisecond)); err != nil {
					return err
				}
				continue
			}
			if reason == "" {
				counted = true
			}

		}
		if reason == "" && d.Workers.Limits == nil {
			counted = d.acquire(j.WorkspaceID, 0)
		}
		if reason != "" && counted {
			d.release(j.WorkspaceID)
			counted = false
		}
		concurrencyDeferred := d.deferred[item.Member]
		delete(d.deferred, item.Member)
		d.fresh(o, sql.NullInt64{})
		if o.Trigger != "s" || o.Attempt != 0 {
			if concurrencyDeferred && o.Attempt == 0 {
				if _, err := d.Writer.Submit(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
					run, err := tx.History.UpsertRun(ctx, historygen.UpsertRunParams{ID: worker.OccurrenceRunID(o), JobID: j.ID, WorkspaceID: j.WorkspaceID, JobVersion: o.Version, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: worker.TriggerName(o.Trigger), Status: "deferred"})
					if err != nil {
						return err
					}
					return tx.History.MarkConcurrencyDeferred(ctx, historygen.MarkConcurrencyDeferredParams{ID: run.ID, WorkspaceID: j.WorkspaceID})
				}); err != nil {
					return err
				}
			}

			wait = append(wait, pending{o: o, j: j, reason: reason, counted: counted, quotaReserved: quotaReserved})
			continue
		}
		next, ok := s.Next(o.ScheduledAt)
		runID, err := id.New(id.Run, claimed)
		if d.Config.Scheduler.DeliveryMode == "fast" {
			runID = worker.OccurrenceRunID(o)
		}
		if err != nil {
			return err
		}
		// DECISION: persist the claimed occurrence before advancing its control row.
		// The queue can lose its data between claim and delivery; this deferred placeholder
		// lets retry repair recover that gap even though next_run_at already advanced.
		history, err := d.Writer.Submit(ctx, store.History, func(dbCtx context.Context, tx store.Tx) error {
			run, err := tx.History.UpsertRun(dbCtx, historygen.UpsertRunParams{ID: runID, JobID: j.ID, WorkspaceID: j.WorkspaceID, JobVersion: o.Version, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule", Status: "deferred", NextAttemptAt: sql.NullInt64{Int64: claimed.UnixMilli(), Valid: true}})
			if err == nil && concurrencyDeferred {
				err = tx.History.MarkConcurrencyDeferred(dbCtx, historygen.MarkConcurrencyDeferredParams{ID: run.ID, WorkspaceID: j.WorkspaceID})
			}
			return err
		})
		if err != nil {
			return err
		}
		// DECISION: fast delivery overlaps the existing ordered advancement commits.
		// Until history commits, SQLite still points at this occurrence; queue
		// inflight recovery and deterministic run IDs cover a crash before commit.
		sent := d.Config.Scheduler.DeliveryMode == "fast" && reason == ""
		var advanced chan error
		if sent {
			advanced = make(chan error, 1)
			d.busy.Add(1)
			select {
			case ch <- worker.Work{Occurrence: o, ClaimedAt: claimed, ScheduleAdvanced: advanced, WorkspaceID: j.WorkspaceID, ConcurrencyReserved: counted, QuotaReserved: quotaReserved}:
			case <-ctx.Done():
				d.busy.Add(-1)
				return ctx.Err()
			}
		}
		wait = append(wait, pending{o: o, j: j, reason: reason, counted: counted, quotaReserved: quotaReserved, history: history, next: next, ok: ok, advance: true, sent: sent, advanced: advanced, rows: new(int64)})
	}
	for n := range wait {
		p := &wait[n]
		if !p.advance {
			continue
		}
		if err := p.history.Wait(ctx); err != nil {
			return err
		}
		o, j, next, ok, rows := p.o, p.j, p.next, p.ok, p.rows
		state := "active"
		if !ok {
			state = "completed"
		}
		f, err := d.Writer.Submit(ctx, store.Control, func(dbCtx context.Context, tx store.Tx) error {
			var err error
			*rows, err = tx.Control.AdvanceJob(dbCtx, sqlitegen.AdvanceJobParams{ID: j.ID, WorkspaceID: j.WorkspaceID, Version: o.Version, ScheduledAt: sql.NullInt64{Int64: o.ScheduledAt.UnixMilli(), Valid: true}, NextRunAt: sql.NullInt64{Int64: next.UnixMilli(), Valid: ok}, State: state})
			return err
		})
		if err != nil {
			return err
		}
		p.control = f
	}
	for _, p := range wait {
		if p.advance {
			if err := p.control.Wait(ctx); err != nil {
				return err
			}
			if *p.rows == 1 {
				if p.ok {
					if err := d.Queue.Add(ctx, queue.Occurrence{JobID: p.o.JobID, ScheduledAt: p.next, Version: p.o.Version, Trigger: "s"}.Format(), p.next, false); err != nil {
						return err
					}
					d.fresh(p.o, sql.NullInt64{Int64: p.next.UnixMilli(), Valid: true})
				} else {
					d.Workers.Events.Publish(ctx, events.Event{Type: events.JobCompleted, WorkspaceID: p.j.WorkspaceID, JobID: p.o.JobID, At: d.Clock.Now()})
				}
			}
		}
		if p.reason != "" {
			if err := d.Workers.Skip(ctx, p.j, p.o, p.reason); err != nil {
				return err
			}
			continue
		}
		if p.sent {
			continue
		}
		d.busy.Add(1)
		select {
		case ch <- worker.Work{Occurrence: p.o, ClaimedAt: claimed, WorkspaceID: p.j.WorkspaceID, ConcurrencyReserved: p.counted, QuotaReserved: p.quotaReserved}:
		case <-ctx.Done():
			d.busy.Add(-1)
			return ctx.Err()
		}
	}
	return nil
}

func (d *Dispatcher) sample(ctx context.Context) error {
	stats, err := d.Queue.Stats(ctx)
	if err != nil {
		return err
	}
	d.Metrics.Gauge("cronpad.queue.due_size", float64(stats.Due))
	d.Metrics.Gauge("cronpad.queue.inflight_size", float64(stats.Inflight))
	at, ok, err := d.Queue.Peek(ctx)
	if err != nil {
		return err
	}
	age := float64(0)
	if ok {
		age = max(0, float64(d.Clock.Since(at).Milliseconds()))
	}
	d.Metrics.Gauge("cronpad.queue.oldest_due_age", age)
	counts, err := d.Store.ControlReader().JobCounts(ctx)
	if err != nil {
		return err
	}
	for _, state := range []string{"active", "paused", "disabled", "completed"} {
		d.Metrics.Gauge("cronpad.jobs", 0, attribute.String("state", state))
	}
	for _, c := range counts {
		d.Metrics.Gauge("cronpad.jobs", float64(c.Total), attribute.String("state", c.State))
	}
	return nil
}
func (d *Dispatcher) queueError(ctx context.Context, op string, err error) {
	d.Metrics.Add(ctx, "cronpad.queue.errors", 1, attribute.String("op", op))
	d.Log.WarnContext(ctx, "queue unavailable", logx.Error, err)
	d.Workers.Events.Publish(ctx, events.Event{Type: events.Degraded, At: d.Clock.Now(), Error: err.Error()})
}

// Renewal remains owned until drain/flush completes, so graceful shutdown does
// not accidentally surrender leadership while workers still hold leases.
func (d *Dispatcher) startRenew(ctx context.Context, token string, stopClaims context.CancelFunc) {
	renewCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	d.renewCancel = cancel
	d.renewDone = make(chan struct{})
	go func() {
		defer close(d.renewDone)
		ticker := d.Clock.NewTicker(max(time.Millisecond, d.Config.Scheduler.LockTTL.Duration()/3))
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.Chan():
				ok, err := d.Queue.Renew(renewCtx, token, d.Config.Scheduler.LockTTL.Duration())
				if err != nil || !ok {
					d.held.Store(false)
					d.setState("standby")
					if err != nil {
						d.queueError(renewCtx, "lock_renew", err)
					}
					stopClaims()
					return
				}
			}
		}
	}()
}
func (d *Dispatcher) stopRenew() {
	if d.renewCancel != nil {
		d.renewCancel()
		<-d.renewDone
		d.renewCancel = nil
		d.renewDone = nil
	}
}

func (d *Dispatcher) acquire(ws string, max int) bool {
	d.inflightMu.Lock()
	defer d.inflightMu.Unlock()
	if d.inflight == nil {
		d.inflight = map[string]int{}
	}
	if max > 0 && d.inflight[ws] >= max {
		return false
	}
	d.inflight[ws]++
	return true
}
func (d *Dispatcher) release(ws string) {
	d.inflightMu.Lock()
	defer d.inflightMu.Unlock()
	if d.inflight[ws] > 0 {
		d.inflight[ws]--
		if d.inflight[ws] == 0 {
			delete(d.inflight, ws)
		}
	}
}

// fresh tracks only the current head in the existing bounded parsed-job LRU.
func (d *Dispatcher) fresh(o queue.Occurrence, at sql.NullInt64) {
	if o.Trigger != "s" || o.Attempt != 0 {
		return
	}
	if element := d.cache[fmt.Sprintf("%s:%d", o.JobID, o.Version)]; element != nil {
		entry := element.Value.(cached)
		if !at.Valid && entry.freshAt.Valid && entry.freshAt.Int64 != o.ScheduledAt.UnixMilli() {
			return
		}
		entry.freshAt = at
		element.Value = entry
	}
}
func (d *Dispatcher) newAdmission(ctx context.Context, j sqlitegen.Job, o queue.Occurrence) (bool, error) {
	if element := d.cache[fmt.Sprintf("%s:%d", o.JobID, o.Version)]; element != nil {
		at := element.Value.(cached).freshAt
		if o.Trigger == "s" && at.Valid && at.Int64 == o.ScheduledAt.UnixMilli() {
			return true, nil
		}
	}
	// DECISION: only a successor just enqueued after our own successful advance is
	// known fresh. Startup/recovery and evicted heads retain the durable identity check.
	previous, ok := d.Writer.RecentOccurrence(j.ID, o.ScheduledAt.UnixMilli(), worker.TriggerName(o.Trigger))
	if !ok {
		var err error
		previous, err = d.Store.HistoryReader().FindOccurrence(ctx, historygen.FindOccurrenceParams{JobID: j.ID, WorkspaceID: j.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: worker.TriggerName(o.Trigger)})
		if errors.Is(err, sql.ErrNoRows) {
			d.fresh(o, sql.NullInt64{Int64: o.ScheduledAt.UnixMilli(), Valid: true})
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
	fresh := !previous.StartedAt.Valid && previous.Status != "skipped" && previous.Status != "cancelled" && previous.Status != "succeeded" && previous.Status != "failed"
	if fresh {
		d.fresh(o, sql.NullInt64{Int64: o.ScheduledAt.UnixMilli(), Valid: true})
	}
	return fresh, nil
}
