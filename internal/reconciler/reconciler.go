// Package reconciler repairs the queue index from authoritative SQLite state.
package reconciler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/attribute"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/events"
	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/schedule"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/telemetry"
	"github.com/gowthamgts/cronpad/internal/worker"
)

type Reconciler struct {
	Events  events.Bus
	Cache   *worker.JobCache
	Store   store.Store
	Queue   queue.Queue
	Writer  *results.Writer
	Clock   clockwork.Clock
	Config  config.Config
	Metrics *telemetry.Instruments
	Epoch   string
}

// Parse restores the immutable creation/edit anchor for every schedule kind.
func Parse(ctx context.Context, j sqlitegen.Job) (schedule.Schedule, error) {
	spec := schedule.Spec{Kind: schedule.Kind(j.ScheduleKind), Expr: j.ScheduleExpr, Timezone: j.Timezone}
	if j.StartsAt.Valid {
		t := time.UnixMilli(j.StartsAt.Int64)
		spec.StartsAt = &t
	}
	if j.EndsAt.Valid {
		t := time.UnixMilli(j.EndsAt.Int64)
		spec.EndsAt = &t
	}
	if j.MaxOccurrences.Valid {
		n := int(j.MaxOccurrences.Int64)
		spec.Count = &n
	}
	anchor := j.CreatedAt
	if j.ScheduleAnchorAt.Valid {
		anchor = j.ScheduleAnchorAt.Int64
	}
	return schedule.ParseContext(ctx, spec, clockwork.NewFakeClockAt(time.UnixMilli(anchor)))
}
func ni(n int64) sql.NullInt64   { return sql.NullInt64{Int64: n, Valid: true} }
func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// RecoverCounters closes the history-before-control crash window using committed terminal runs.
func (r *Reconciler) RecoverCounters(ctx context.Context) error {
	// DECISION: anchor the crash window to committed history, not wall-clock now;
	// a long downtime must not make the last process's un-applied results too old.
	anchor, err := r.Store.HistoryReader().RecoveryAnchor(ctx)
	if err != nil {
		return err
	}
	base := r.Clock.Now()
	if anchor > 0 {
		base = minTime(base, time.UnixMilli(anchor))
	}
	jobs, err := r.Store.HistoryReader().RecentJobs(ctx, ni(base.Add(-2*r.Config.Scheduler.LeaseDuration.Duration()).UnixMilli()))
	if err != nil {
		return err
	}
	type pending struct {
		f        results.Future
		j        sqlitegen.Job
		failures int64
		disabled *int64
	}
	wait := []pending{}
	for _, job := range jobs {
		current, err := r.Store.ControlReader().GetJob(ctx, sqlitegen.GetJobParams{ID: job.JobID, WorkspaceID: job.WorkspaceID})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		last, err := r.Store.HistoryReader().LastJobFinalRun(ctx, historygen.LastJobFinalRunParams{JobID: job.JobID, WorkspaceID: job.WorkspaceID, ResetVersion: current.FailuresResetVersion})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		failures, err := r.Store.HistoryReader().RecoveredFailures(ctx, historygen.RecoveredFailuresParams{JobID: job.JobID, WorkspaceID: job.WorkspaceID, ResetVersion: current.FailuresResetVersion})
		if err != nil {
			return err
		}
		disabled := new(int64)
		limit := int64(r.Config.AutoDisable.ConsecutiveFailures)
		if current.AutoDisableThreshold.Valid {
			limit = current.AutoDisableThreshold.Int64
		}
		if r.Config.Mode == "hosted" {
			limit = min(limit, int64(r.Config.AutoDisable.ConsecutiveFailures))
		}
		f, err := r.Writer.Submit(ctx, store.Control, func(dbCtx context.Context, tx store.Tx) error {
			if err := tx.Control.RecoverJobResult(dbCtx, sqlitegen.RecoverJobResultParams{ID: job.JobID, WorkspaceID: job.WorkspaceID, Failures: failures, LastRunAt: ni(last.ScheduledAt), Status: ns(last.Status)}); err != nil {
				return err
			}
			if r.Config.AutoDisable.Enabled && failures >= limit && last.Status == "failed" {
				var err error
				*disabled, err = tx.Control.DisableJob(dbCtx, sqlitegen.DisableJobParams{ID: current.ID, WorkspaceID: current.WorkspaceID, Version: current.Version, Now: r.Clock.Now().UnixMilli(), Reason: ns(fmt.Sprintf("consecutive_failures:%d", failures))})
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		wait = append(wait, pending{f, current, failures, disabled})
	}
	for _, p := range wait {
		if err := p.f.Wait(ctx); err != nil {
			return err
		}
		if *p.disabled == 0 {
			continue
		}
		if r.Cache != nil {
			r.Cache.Invalidate(p.j.ID)
		}
		if p.j.NextRunAt.Valid {
			if err := r.Queue.Remove(ctx, queue.Occurrence{JobID: p.j.ID, ScheduledAt: time.UnixMilli(p.j.NextRunAt.Int64), Version: p.j.Version, Trigger: "s"}.Format()); err != nil {
				return err
			}
		}
		auditID, err := id.New(id.Run, r.Clock.Now())
		if err != nil {
			return err
		}
		err = r.Writer.Do(ctx, store.History, func(dbCtx context.Context, tx store.Tx) error {
			return tx.History.InsertAudit(dbCtx, historygen.InsertAuditParams{ID: auditID, WorkspaceID: p.j.WorkspaceID, Action: "job.auto_disable", Target: ns(p.j.ID), Details: ns(fmt.Sprintf(`{"consecutive_failures":%d,"recovered":true}`, p.failures)), CreatedAt: r.Clock.Now().UnixMilli()})
		})
		if err != nil {
			return err
		}
		r.Metrics.Add(ctx, "cronpad.jobs.auto_disabled", 1)
		if r.Events != nil {
			r.Events.Publish(ctx, events.Event{Type: events.JobAutoDisabled, JobID: p.j.ID, WorkspaceID: p.j.WorkspaceID, At: r.Clock.Now()})
		}
	}
	return nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// RecoverInflight materializes fast-mode claims before startup misfire policy.
// DECISION: an orphan may already have reached its receiver even if no history
// committed. Reserve its stable identity as running, without inventing a start
// timestamp or consuming quota; normal worker admission supplies those later.
func (r *Reconciler) RecoverInflight(ctx context.Context) error {
	var cursor uint64
	for {
		members, next, err := r.Queue.ScanInflight(ctx, cursor)
		if err != nil {
			return fmt.Errorf("scan orphaned claims: %w", err)
		}
		futures := make([]results.Future, 0, len(members))
		for _, member := range members {
			o, err := queue.ParseOccurrence(member)
			if err != nil {
				continue
			}
			job, err := r.Store.ControlReader().RecoveryClaimJob(ctx, o.JobID)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			future, err := r.Writer.Submit(ctx, store.History, func(dbCtx context.Context, tx store.Tx) error {
				run, err := tx.History.BeginRun(dbCtx, historygen.BeginRunParams{ID: worker.OccurrenceRunID(o), JobID: o.JobID, WorkspaceID: job.WorkspaceID, JobVersion: o.Version, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: worker.TriggerName(o.Trigger), Attempt: int64(o.Attempt)})
				if err != nil {
					return err
				}
				if job.DeletedAt.Valid {
					switch run.Status {
					case "succeeded", "failed", "skipped", "cancelled":
						return nil
					}
					_, err = tx.History.CompleteRun(dbCtx, historygen.CompleteRunParams{ID: run.ID, WorkspaceID: job.WorkspaceID, Status: "cancelled", Attempts: run.Attempts, FinishedAt: ni(r.Clock.Now().UnixMilli()), LastError: ns("job deleted before recovery; delivery may have occurred")})
				}
				return err
			})
			if err != nil {
				return err
			}
			futures = append(futures, future)
		}
		for _, future := range futures {
			if err := future.Wait(ctx); err != nil {
				return err
			}
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}

// Full repairs future occurrences and retries; startup alone applies downtime misfires.
func (r *Reconciler) Full(ctx context.Context, startup bool) error {
	cursor := ""
	for {
		jobs, err := r.Store.ControlReader().ActiveJobs(ctx, sqlitegen.ActiveJobsParams{Cursor: cursor, PageSize: 1000})
		if err != nil {
			return fmt.Errorf("active jobs: %w", err)
		}
		items := []queue.Item{}
		for _, j := range jobs {
			if r.Cache != nil {
				r.Cache.Put(j)
			}
			if startup && j.NextRunAt.Valid {
				threshold := r.Config.Scheduler.SLAThreshold.Duration()
				if j.SlaThresholdMs.Valid {
					threshold = time.Duration(j.SlaThresholdMs.Int64) * time.Millisecond
				}
				if r.Clock.Now().Sub(time.UnixMilli(j.NextRunAt.Int64)) > threshold {
					if err := r.misfire(ctx, j); err != nil {
						return err
					}
					continue
				}
			}
			if !j.NextRunAt.Valid {
				s, err := Parse(ctx, j)
				if err != nil {
					return err
				}
				next, ok := s.Next(r.Clock.Now())
				j.NextRunAt = sql.NullInt64{Int64: next.UnixMilli(), Valid: ok}
				if err = r.Writer.Do(ctx, store.Control, func(dbCtx context.Context, tx store.Tx) error {
					ctx := dbCtx
					_, err := tx.Control.SetNextRun(ctx, sqlitegen.SetNextRunParams{ID: j.ID, WorkspaceID: j.WorkspaceID, Version: j.Version, NextRunAt: j.NextRunAt})
					return err
				}); err != nil {
					return err
				}
			}
			if j.NextRunAt.Valid {
				items = append(items, queue.Item{Member: queue.Occurrence{JobID: j.ID, ScheduledAt: time.UnixMilli(j.NextRunAt.Int64), Version: j.Version, Trigger: "s"}.Format(), Due: time.UnixMilli(j.NextRunAt.Int64)})
			}
		}
		members := make([]string, len(items))
		for n, item := range items {
			members[n] = item.Member
		}
		present, err := r.Queue.Contains(ctx, members)
		if err != nil {
			return err
		}
		for n, item := range items {
			if !present[n] {
				if err = r.Queue.Add(ctx, item.Member, item.Due, true); err != nil {
					return err
				}
				r.Metrics.Add(ctx, "cronpad.reconciler.repaired", 1, attribute.String("kind", "full_sweep"))
			}
		}
		if len(jobs) < 1000 {
			break
		}
		cursor = jobs[len(jobs)-1].ID
	}
	if err := r.deferred(ctx, true); err != nil {
		return err
	}
	// DECISION: defer scan mutations until iteration completes. This also avoids
	// offset-cursor implementations skipping entries as members disappear.
	stale := []string{}
	var scan uint64
	for {
		members, next, err := r.Queue.ScanDue(ctx, scan)
		if err != nil {
			return err
		}
		for _, m := range members {
			o, err := queue.ParseOccurrence(m)
			if err != nil {
				stale = append(stale, m)
				continue
			}
			var j sqlitegen.Job
			if r.Cache != nil {
				j, err = r.Cache.Get(ctx, o.JobID, o.Version)
			} else {
				j, err = r.Store.ControlReader().DispatchJob(ctx, o.JobID)
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if errors.Is(err, sql.ErrNoRows) || j.Version != o.Version || j.State == "disabled" || j.State == "paused" && o.Trigger == "s" {
				stale = append(stale, m)
			}
		}
		scan = next
		if scan == 0 {
			break
		}
	}
	for _, member := range stale {
		if err := r.Queue.Remove(ctx, member); err != nil {
			return err
		}
	}
	return nil
}

// Check detects epoch loss before performing the cheap indexed overdue repair.
func (r *Reconciler) Check(ctx context.Context) error {
	epoch, err := r.Queue.Epoch(ctx)
	if err != nil {
		return err
	}
	if epoch != r.Epoch {
		if err = r.Queue.SetEpoch(ctx, r.Epoch); err != nil {
			return err
		}
		if err = r.Full(ctx, false); err != nil {
			return err
		}
	}
	jobs, err := r.Store.ControlReader().OverdueJobs(ctx, ni(r.Clock.Now().Add(-r.Config.Scheduler.OverdueGrace.Duration()).UnixMilli()))
	if err != nil {
		return err
	}
	members := make([]string, len(jobs))
	for i, j := range jobs {
		members[i] = queue.Occurrence{JobID: j.ID, ScheduledAt: time.UnixMilli(j.NextRunAt.Int64), Version: j.Version, Trigger: "s"}.Format()
	}
	present, err := r.Queue.Contains(ctx, members)
	if err != nil {
		return err
	}
	for i, j := range jobs {
		if present[i] {
			r.Metrics.Add(ctx, "cronpad.reconciler.backlogged", 1, attribute.String("kind", "scheduled"))
		} else {
			if err = r.Queue.Add(ctx, members[i], time.UnixMilli(j.NextRunAt.Int64), true); err != nil {
				return err
			}
			r.Metrics.Add(ctx, "cronpad.reconciler.repaired", 1, attribute.String("kind", "lost_member"))
		}
	}
	return r.deferred(ctx, false)
}
func (r *Reconciler) repair(ctx context.Context, o queue.Occurrence, due time.Time, kind string) error {
	v, err := r.Queue.Contains(ctx, []string{o.Format()})
	if err != nil {
		return err
	}
	if !v[0] {
		if err = r.Queue.Add(ctx, o.Format(), due, true); err != nil {
			return err
		}
		r.Metrics.Add(ctx, "cronpad.reconciler.repaired", 1, attribute.String("kind", kind))
	}
	return nil
}
func (r *Reconciler) deferred(ctx context.Context, full bool) error {
	cursor := ""
	for {
		var runs []historygen.Run
		var err error
		if full {
			runs, err = r.Store.HistoryReader().AllPendingRuns(ctx, cursor)
		} else {
			runs, err = r.Store.HistoryReader().OverduePendingRuns(ctx, ni(r.Clock.Now().Add(-r.Config.Scheduler.OverdueGrace.Duration()).UnixMilli()))
		}
		if err != nil {
			return err
		}
		for _, run := range runs {
			j, err := r.Store.ControlReader().GetJob(ctx, sqlitegen.GetJobParams{ID: run.JobID, WorkspaceID: run.WorkspaceID})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if errors.Is(err, sql.ErrNoRows) || run.JobVersion != j.Version || j.State == "disabled" || j.State == "paused" && run.Trigger == "schedule" {
				if err = r.Writer.Do(ctx, store.History, func(dbCtx context.Context, tx store.Tx) error {
					ctx := dbCtx
					_, err := tx.History.CompleteRun(ctx, historygen.CompleteRunParams{ID: run.ID, WorkspaceID: run.WorkspaceID, Status: "cancelled", Attempts: run.Attempts, FinishedAt: ni(r.Clock.Now().UnixMilli()), LastError: ns("job version changed")})
					return err
				}); err != nil {
					return err
				}
				continue
			}
			o := queue.Occurrence{JobID: j.ID, ScheduledAt: time.UnixMilli(run.ScheduledAt), Attempt: int(run.Attempts), Version: run.JobVersion, Trigger: worker.TriggerCode(run.Trigger)}
			due := r.Clock.Now()
			kind := "lost_inflight"
			if run.NextAttemptAt.Valid {
				due = time.UnixMilli(run.NextAttemptAt.Int64)
				kind = "lost_retry"
			}
			if err = r.repair(ctx, o, due, kind); err != nil {
				return err
			}
		}
		if !full || len(runs) < 1000 {
			return nil
		}
		cursor = runs[len(runs)-1].ID
	}
}
func (r *Reconciler) misfire(ctx context.Context, j sqlitegen.Job) error {
	s, err := Parse(ctx, j)
	if err != nil {
		return err
	}
	now := r.Clock.Now()
	at := time.UnixMilli(j.NextRunAt.Int64)
	selected := []time.Time{}
	futures := make([]results.Future, 0, 500)
	count, skipped := int64(0), int64(0)
	var last time.Time
	next := sql.NullInt64{}
	flush := func() error {
		for _, f := range futures {
			if err := f.Wait(ctx); err != nil {
				return err
			}
		}
		futures = futures[:0]
		return nil
	}
	// DECISION: enumerate missed times in bounded write batches, retaining only
	// fire_all's configured cap (or fire_once's final time), never the whole outage.
	for !at.After(now) {
		following, ok := s.Next(at)
		chosen := j.MisfirePolicy == "fire_once" && (!ok || following.After(now)) || j.MisfirePolicy == "fire_all" && count < int64(r.Config.Scheduler.MisfireFireAllCap)
		// A recovered fast claim is already admitted for replay, even if its
		// first history commit was lost. It is not a missed, unclaimed fire.
		if r.Config.Scheduler.DeliveryMode == "fast" {
			prior, found := r.Store.HistoryReader().FindOccurrence(ctx, historygen.FindOccurrenceParams{JobID: j.ID, WorkspaceID: j.WorkspaceID, ScheduledAt: at.UnixMilli(), Trigger: "schedule"})
			if found != nil && !errors.Is(found, sql.ErrNoRows) {
				return found
			}
			if found == nil && (prior.Status == "running" || prior.Status == "deferred") {
				chosen = true
			}
		}
		logical := at
		runID, err := id.New(id.Run, now)
		if r.Config.Scheduler.DeliveryMode == "fast" {
			runID = worker.OccurrenceRunID(queue.Occurrence{JobID: j.ID, ScheduledAt: at, Trigger: "s"})
		}
		if err != nil {
			return err
		}
		if chosen {
			selected = append(selected, at)
		} else {
			skipped++
		}
		f, err := r.Writer.Submit(ctx, store.History, func(dbCtx context.Context, tx store.Tx) error {
			status := "skipped"
			retryAt := sql.NullInt64{}
			if chosen {
				status = "deferred"
				retryAt = ni(now.UnixMilli())
			}
			run, err := tx.History.UpsertRun(dbCtx, historygen.UpsertRunParams{ID: runID, JobID: j.ID, WorkspaceID: j.WorkspaceID, JobVersion: j.Version, ScheduledAt: logical.UnixMilli(), Trigger: "schedule", Status: status, NextAttemptAt: retryAt})
			if err != nil {
				return err
			}
			if !chosen && !run.StartedAt.Valid && run.Status != "succeeded" && run.Status != "failed" && run.Status != "cancelled" {
				_, err = tx.History.CompleteRun(dbCtx, historygen.CompleteRunParams{ID: run.ID, WorkspaceID: j.WorkspaceID, Status: "skipped", Attempts: run.Attempts, FinishedAt: ni(now.UnixMilli()), SkipReason: ns("misfire")})
			}
			return err
		})
		if err != nil {
			return err
		}
		futures = append(futures, f)
		count++
		last = at
		if len(futures) == 500 {
			if err = flush(); err != nil {
				return err
			}
		}
		if !ok {
			break
		}
		at = following
		if at.After(now) {
			next = ni(at.UnixMilli())
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err = flush(); err != nil {
		return err
	}
	state := "active"
	if !next.Valid {
		state = "completed"
	}
	var changed int64
	err = r.Writer.Do(ctx, store.Control, func(dbCtx context.Context, tx store.Tx) error {
		var err error
		changed, err = tx.Control.RecoverSchedule(dbCtx, sqlitegen.RecoverScheduleParams{ID: j.ID, WorkspaceID: j.WorkspaceID, Version: j.Version, Previous: j.NextRunAt, NextRunAt: next, State: state, Missed: count, LastRunAt: ni(last.UnixMilli())})
		return err
	})
	if err != nil {
		return err
	}
	if changed == 0 {
		return nil
	}
	if err = r.Queue.Remove(ctx, queue.Occurrence{JobID: j.ID, ScheduledAt: time.UnixMilli(j.NextRunAt.Int64), Version: j.Version, Trigger: "s"}.Format()); err != nil {
		return err
	}
	for _, logical := range selected {
		if err = r.Queue.Add(ctx, queue.Occurrence{JobID: j.ID, ScheduledAt: logical, Version: j.Version, Trigger: "s"}.Format(), now, true); err != nil {
			return err
		}
	}
	r.Metrics.Add(ctx, "cronpad.runs", skipped, attribute.String("sink", j.SinkKind), attribute.String("status", "skipped"))
	if next.Valid {
		return r.repair(ctx, queue.Occurrence{JobID: j.ID, ScheduledAt: time.UnixMilli(next.Int64), Version: j.Version, Trigger: "s"}, time.UnixMilli(next.Int64), "full_sweep")
	}
	if r.Events != nil {
		r.Events.Publish(ctx, events.Event{Type: events.JobCompleted, WorkspaceID: j.WorkspaceID, JobID: j.ID, At: now})
	}
	return nil
}
