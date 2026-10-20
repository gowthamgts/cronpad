// Package worker delivers leased occurrences and commits history before queue acknowledgements.
package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/secrets"

	"github.com/jonboulle/clockwork"
	"github.com/oklog/ulid/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/events"
	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/retry"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/telemetry"
)

// Work retains the claim timestamp independently of its logical occurrence time.
type Work struct {
	WorkspaceID         string
	ConcurrencyReserved bool
	// QuotaReserved is an admission already reserved by the dispatcher.
	QuotaReserved bool
	Occurrence    queue.Occurrence
	ClaimedAt     time.Time
	// ScheduleAdvanced resolves the fast dispatcher advancement before final control/ack.
	ScheduleAdvanced <-chan error
}

// Engine shares deduplication and retry RNG state across all worker goroutines.
type Engine struct {
	Vault     *secrets.Vault
	Limits    *limits.Registry
	finishing map[string]*finishSlot
	Cache     *JobCache
	Store     store.Store
	Queue     queue.Queue
	Writer    *results.Writer
	Clock     clockwork.Clock
	Config    config.Config
	Sinks     map[string]sink.Sink
	Metrics   *telemetry.Instruments
	Log       *slog.Logger
	Tracer    trace.Tracer
	Events    events.Bus
	RNG       *rand.Rand
	mu        sync.Mutex
	active    map[string]bool
}

// TriggerName converts compact queue triggers to durable run trigger values.
func TriggerName(s string) string {
	switch s {
	case "m":
		return "manual"
	case "r":
		return "retry_manual"
	default:
		return "schedule"
	}
}

// TriggerCode is the inverse of TriggerName for durable deferred runs.
func TriggerCode(s string) string {
	switch s {
	case "manual":
		return "m"
	case "retry_manual":
		return "r"
	default:
		return "s"
	}
}

// OccurrenceRunID is stable even when the process dies before the first history commit.
// DECISION: keep the run ULID shape; hash the durable occurrence identity into
// its entropy, excluding retry number and job version just like the history UNIQUE key.
func OccurrenceRunID(o queue.Occurrence) string {
	entropy := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%s", o.JobID, o.ScheduledAt.UnixMilli(), TriggerName(o.Trigger)))
	// Clamp the display timestamp; the full original timestamp remains in entropy.
	timestamp := uint64(min(max(o.ScheduledAt.UnixMilli(), 0), int64(ulid.MaxTime())))
	return "run_" + ulid.MustNew(timestamp, bytes.NewReader(entropy[:])).String()
}

func ni(v int64) sql.NullInt64   { return sql.NullInt64{Int64: v, Valid: true} }
func ns(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }

// Process owns one occurrence, including its lease-extender goroutine.
func (e *Engine) Process(ctx context.Context, w Work) error {
	o := w.Occurrence
	member := o.Format()
	ctx = logx.With(ctx, logx.JobID, o.JobID, logx.Occ, member, logx.Attempt, o.Attempt, logx.Trigger, o.Trigger, logx.ScheduledAt, o.ScheduledAt)
	e.mu.Lock()
	if e.Cache == nil {
		e.Cache = NewJobCache(e.Store, 10000)
	}
	cache := e.Cache
	e.mu.Unlock()
	job, err := cache.Get(ctx, o.JobID, o.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return e.Queue.Ack(ctx, member, "", time.Time{})
	}
	if err != nil {
		return fmt.Errorf("load worker job: %w", err)
	}
	if job.Version != o.Version || job.State == "disabled" || job.State == "paused" && o.Trigger == "s" {
		return e.cancel(ctx, job, o)
	}
	if job.State == "completed" && o.Trigger == "s" && (!job.LastRunAt.Valid || job.LastRunAt.Int64 < o.ScheduledAt.UnixMilli()) {
		_, err := e.Store.HistoryReader().FindOccurrence(ctx, historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: TriggerName(o.Trigger)})
		if errors.Is(err, sql.ErrNoRows) {
			return e.Queue.Ack(ctx, member, "", time.Time{})
		}
		if err != nil {
			return err
		}
	}
	suspended, err := store.Suspended(ctx, e.Store, job.WorkspaceID)
	if err != nil {
		return err
	}
	if suspended {
		return e.Skip(ctx, job, o, "workspace_suspended")
	}
	ctx = logx.With(ctx, logx.WorkspaceID, job.WorkspaceID, logx.Sink, job.SinkKind)
	ctx, span := e.Tracer.Start(ctx, "cronpad.run")
	defer span.End()
	span.SetAttributes(attribute.String("cronpad.job_id", job.ID), attribute.Int("cronpad.attempt", o.Attempt), attribute.String("cronpad.sink", job.SinkKind))
	key := fmt.Sprintf("%s|%d|%s", o.JobID, o.ScheduledAt.UnixMilli(), o.Trigger)
	e.mu.Lock()
	if e.active == nil {
		e.active = map[string]bool{}
	}
	if e.active[key] {
		e.mu.Unlock()
		e.Metrics.Add(ctx, "cronpad.duplicates_suppressed", 1)
		return e.Queue.Ack(ctx, member, "", time.Time{})
	}
	e.active[key] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.active, key); e.mu.Unlock() }()
	if known, ok := e.Writer.RecentOccurrence(job.ID, o.ScheduledAt.UnixMilli(), TriggerName(o.Trigger)); ok && final(known.Status) {
		if w.QuotaReserved && e.Limits != nil {
			e.Limits.Release(OccurrenceRunID(o))
		}
		e.Metrics.Add(ctx, "cronpad.duplicates_suppressed", 1)
		return e.Queue.Ack(ctx, member, "", time.Time{})
	}
	now := e.Clock.Now()
	runID, err := id.New(id.Run, now)
	if err != nil {
		return err
	}
	// DECISION: a dispatcher-reserved first admission can deliver speculatively
	// in fast mode. Direct worker quota admissions and skip_if_running retain
	// the commit barrier; existing starts retain durable deduplication.
	fast := e.Config.Scheduler.DeliveryMode == "fast" && o.Attempt == 0 && job.OverlapPolicy != "skip_if_running"
	preview := historygen.Run{}
	if fast {
		runID = OccurrenceRunID(o)
		existing, found := e.Store.HistoryReader().FindOccurrence(ctx, historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: TriggerName(o.Trigger)})
		if found != nil && !errors.Is(found, sql.ErrNoRows) {
			return found
		}
		if found == nil {
			runID = existing.ID
			fast = !existing.ClaimedAt.Valid && !final(existing.Status) && existing.Attempts == 0
		}
		preview = historygen.Run{ID: runID, JobID: job.ID, WorkspaceID: job.WorkspaceID}
	}
	var run historygen.Run
	duplicate, skipped := false, false
	var quotaErr error
	threshold := e.Config.Scheduler.SLAThreshold.Duration()
	if job.SlaThresholdMs.Valid {
		threshold = time.Duration(job.SlaThresholdMs.Int64) * time.Millisecond
	}
	lag := now.Sub(o.ScheduledAt)
	breach := int64(0)
	// Start lag, and so the SLA, is about the first attempt only: a retry runs
	// late by design (it waited out its backoff) and must not mark the run late.
	if lag > threshold && o.Attempt == 0 {
		breach = 1
	}
	firstStart := false
	monthlyMax := int64(0)
	notificationMax := 0
	if e.Limits != nil {
		if err := e.Limits.Seed(ctx); err != nil {
			return err
		}
		set, err := e.Limits.For(ctx, job.WorkspaceID)
		if err != nil {
			return err
		}
		notificationMax = set.MaxRunsPerMonth
		if set.OverQuota != limits.Allow {
			monthlyMax = int64(set.MaxRunsPerMonth)
		}
	}
	fast = fast && (monthlyMax == 0 || w.QuotaReserved)
	reservationKey := OccurrenceRunID(o)
	startFuture, err := e.Writer.Submit(ctx, store.History, func(dbCtx context.Context, tx store.Tx) (startErr error) {
		reserved := w.QuotaReserved
		defer func() {
			if startErr != nil && reserved && e.Limits != nil {
				e.Limits.Release(reservationKey)
			}
		}()
		defer func() {
			if startErr == nil && firstStart {
				if recorder, ok := e.Events.(interface {
					RecordQuota(context.Context, store.Tx, string, int) error
				}); ok && notificationMax > 0 {
					startErr = recorder.RecordQuota(dbCtx, tx, job.WorkspaceID, notificationMax)
					if startErr != nil {
						return
					}
				}
			}
			if startErr == nil && firstStart && run.SlaBreached != 0 {
				if recorder, ok := e.Events.(interface {
					RecordStart(context.Context, store.Tx, sqlitegen.Job, historygen.Run, int) error
				}); ok {
					startErr = recorder.RecordStart(dbCtx, tx, job, run, o.Attempt)
				}
			}
		}()
		ctx := dbCtx
		quotaErr = nil
		var err error
		// DECISION: serialized admission verifies durable identity before reserving;
		// retries and reclaims of already-started occurrences never consume usage.
		if e.Limits != nil && !reserved && (monthlyMax > 0 || o.Attempt > 0) {
			previous, found := tx.History.FindOccurrence(ctx, historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: TriggerName(o.Trigger)})
			if errors.Is(found, sql.ErrNoRows) || found == nil && !previous.StartedAt.Valid && !final(previous.Status) {
				if err := e.Limits.Reserve(ctx, job.WorkspaceID, reservationKey); err != nil {
					var quota *limits.Error
					if errors.As(err, &quota) {
						quotaErr = err
						return nil
					}
					return err
				}
				reserved = true
			} else if found != nil {
				return found
			}
		}
		if job.OverlapPolicy != "skip_if_running" {
			run, err = tx.History.BeginRun(ctx, historygen.BeginRunParams{QuotaDay: ni(now.UTC().UnixMilli() / 86400000), Attempt: int64(o.Attempt), ID: runID, JobID: job.ID, WorkspaceID: job.WorkspaceID, JobVersion: job.Version, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: TriggerName(o.Trigger), ClaimedAt: ni(w.ClaimedAt.UnixMilli()), StartedAt: ni(now.UnixMilli()), StartLagMs: ni(lag.Milliseconds()), SlaBreached: breach})
			if err != nil {
				return err
			}
			duplicate = final(run.Status)
			if duplicate && reserved && e.Limits != nil {
				e.Limits.Release(reservationKey)
				reserved = false
			}
			firstStart = !duplicate && run.Attempts == 0 && run.StartedAt.Int64 == now.UnixMilli()
			if firstStart && !reserved && e.Limits != nil && monthlyMax == 0 {
				if err = e.Limits.Reserve(ctx, job.WorkspaceID, reservationKey); err != nil {
					return err
				}
				reserved = true
			}
			return nil
		}
		run, err = tx.History.UpsertRun(ctx, historygen.UpsertRunParams{QuotaDay: ni(now.UTC().UnixMilli() / 86400000), JobVersion: job.Version, ID: runID, JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: TriggerName(o.Trigger), Status: "deferred"})
		if err != nil {
			return err
		}
		if final(run.Status) {
			if reserved && e.Limits != nil {
				e.Limits.Release(reservationKey)
				reserved = false
			}
			duplicate = true
			return nil
		}
		if int64(o.Attempt) < run.Attempts {
			return nil
		}
		if job.OverlapPolicy == "skip_if_running" {
			open, err := tx.History.OtherOpenRun(ctx, historygen.OtherOpenRunParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ID: run.ID, ScheduledAt: o.ScheduledAt.UnixMilli()})
			if err != nil {
				return err
			}
			if open != 0 {
				skipped = true
				if e.Limits != nil {
					e.Limits.Release(reservationKey)
					reserved = false
				}
				run, err = tx.History.CompleteRun(ctx, historygen.CompleteRunParams{ID: run.ID, WorkspaceID: job.WorkspaceID, Status: "skipped", Attempts: 0, FinishedAt: ni(now.UnixMilli()), SkipReason: ns("previous_run_in_progress")})
				return err
			}
		}
		firstStart = !run.StartedAt.Valid
		run, err = tx.History.StartRun(ctx, historygen.StartRunParams{QuotaDay: ni(now.UTC().UnixMilli() / 86400000), ID: run.ID, WorkspaceID: job.WorkspaceID, ClaimedAt: ni(w.ClaimedAt.UnixMilli()), StartedAt: ni(now.UnixMilli()), StartLagMs: ni(lag.Milliseconds()), SlaBreached: breach})
		if err == nil && firstStart && !reserved && e.Limits != nil && monthlyMax == 0 {
			err = e.Limits.Reserve(ctx, job.WorkspaceID, reservationKey)
			reserved = err == nil
		}
		return err
	})
	var earlyResult sink.ExecResult
	var earlyStart time.Time
	var earlyDuration time.Duration
	if err == nil && fast {
		span.SetAttributes(attribute.String("cronpad.run_id", preview.ID))
		earlyResult, earlyStart, earlyDuration = e.deliver(logx.With(ctx, logx.RunID, preview.ID), member, job, preview, o)
	}
	if err == nil {
		err = startFuture.Wait(ctx)
	}
	if err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	if e.Limits != nil {
		e.Limits.Forget(reservationKey)
	}
	if quotaErr != nil {
		return e.Skip(ctx, job, o, "quota_exceeded")
	}
	e.Writer.Remember(run)
	ctx = logx.With(ctx, logx.RunID, run.ID)
	span.SetAttributes(attribute.String("cronpad.run_id", run.ID))
	if duplicate {
		e.Metrics.Add(ctx, "cronpad.duplicates_suppressed", 1)
		return e.Queue.Ack(ctx, member, "", time.Time{})
	}
	if skipped {
		e.Metrics.Add(ctx, "cronpad.runs", 1, attribute.String("sink", job.SinkKind), attribute.String("status", "skipped"))
		return e.Queue.Ack(ctx, member, "", time.Time{})
	}
	now = e.Clock.Now()
	if fast {
		now = earlyStart
	}
	lag = now.Sub(o.ScheduledAt)
	if lag > threshold && o.Attempt == 0 && run.ConcurrencyDeferred == 0 {
		breach = 1
	} else if run.ConcurrencyDeferred != 0 {
		breach = 0
	}
	// A crash after deferral commits but before ack must resume the committed
	// retry number/time rather than delivering an already-recorded attempt early.
	if int64(o.Attempt) < run.Attempts {
		retry := o
		retry.Attempt = int(run.Attempts)
		due := e.Clock.Now()
		if run.NextAttemptAt.Valid {
			due = time.UnixMilli(run.NextAttemptAt.Int64)
		}
		e.Metrics.Add(ctx, "cronpad.duplicates_suppressed", 1)
		return e.Queue.Ack(ctx, member, retry.Format(), due)
	}
	if firstStart {
		e.Metrics.Record(ctx, "cronpad.run.start_lag", float64(lag.Microseconds())/1000, attribute.String("sink", job.SinkKind), attribute.String("trigger", TriggerName(o.Trigger)))
		if breach != 0 {
			e.Metrics.Add(ctx, "cronpad.runs.sla_breached", 1, attribute.String("sink", job.SinkKind))
			e.emit(ctx, events.RunSLABreached, job, run, o)
		}
	}
	e.Metrics.Record(ctx, "cronpad.run.queue_wait", float64(now.Sub(w.ClaimedAt).Microseconds())/1000)
	var res sink.ExecResult
	var start time.Time
	var duration time.Duration
	if fast {
		res, start, duration = earlyResult, earlyStart, earlyDuration
	} else {
		res, start, duration = e.deliver(ctx, member, job, run, o)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	unlockFinish := e.finishLock(job.ID)
	defer unlockFinish()
	span.SetAttributes(attribute.String("cronpad.outcome", string(res.Outcome)))
	status := "failed"
	if res.Outcome == sink.Success {
		status = "succeeded"
	}
	policy := retry.Defaults(e.Config.Retry.Defaults)
	_ = json.Unmarshal([]byte(job.RetryPolicy), &policy)
	retryMember := ""
	nextAttempt := sql.NullInt64{}
	finished := ni(e.Clock.Now().UnixMilli())
	if (res.Outcome == sink.Retryable || res.Outcome == sink.Timeout) && o.Attempt+1 < policy.MaxAttempts {
		status = "deferred"
		e.mu.Lock()
		delay := policy.Delay(o.Attempt+1, e.RNG, res.RetryAfter)
		e.mu.Unlock()
		nextAttempt = ni(e.Clock.Now().Add(delay).UnixMilli())
		finished = sql.NullInt64{}
		next := o
		next.Attempt++
		retryMember = next.Format()
	}
	err = e.Writer.Do(ctx, store.History, func(dbCtx context.Context, tx store.Tx) error {
		ctx := dbCtx
		if firstStart {
			if err := tx.History.CorrectFirstStart(ctx, historygen.CorrectFirstStartParams{ID: run.ID, WorkspaceID: job.WorkspaceID, StartedAt: ni(start.UnixMilli()), StartLagMs: ni(start.Sub(o.ScheduledAt).Milliseconds()), SlaBreached: breach}); err != nil {
				return err
			}
		}
		body := sql.NullString{}
		if status != "succeeded" || e.Config.Retention.StoreSuccessBodies {
			body = ns(string(res.ResponseBody))
		}
		if err := tx.History.InsertAttempt(ctx, historygen.InsertAttemptParams{RunID: run.ID, WorkspaceID: job.WorkspaceID, N: int64(o.Attempt), StartedAt: start.UnixMilli(), FinishedAt: ni(e.Clock.Now().UnixMilli()), DurationMs: ni(duration.Milliseconds()), Outcome: string(res.Outcome), StatusCode: ni(int64(res.Code)), Error: ns(res.Error), RequestSummary: ns(res.RequestSummary), ResponseBody: body}); err != nil {
			return err
		}
		var err error
		run, err = tx.History.CompleteRun(ctx, historygen.CompleteRunParams{ID: run.ID, WorkspaceID: job.WorkspaceID, Status: status, Attempts: int64(o.Attempt + 1), FinishedAt: finished, NextAttemptAt: nextAttempt, E2eMs: ni(e.Clock.Now().Sub(o.ScheduledAt).Milliseconds()), LastError: ns(res.Error)})
		if err != nil {
			return err
		}
		if recorder, ok := e.Events.(interface {
			RecordRun(context.Context, store.Tx, sqlitegen.Job, historygen.Run, sink.Outcome, bool) error
		}); ok {
			return recorder.RecordRun(ctx, tx, job, run, res.Outcome, firstStart)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("finish history: %w", err)
	}
	e.Writer.Remember(run)
	// DECISION: delivery may overlap advancement, but a fast auto-disable must
	// not clear next_run_at before the dispatcher counts this occurrence.
	// History is already durable if claim cancellation interrupts this barrier.
	if w.ScheduleAdvanced != nil {
		select {
		case err := <-w.ScheduleAdvanced:
			if err != nil {
				return fmt.Errorf("schedule advancement: %w", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var updated sqlitegen.ApplyRunResultLiteRow
	recovered, disabled := false, false
	if status != "deferred" {
		err = e.Writer.Do(ctx, store.Control, func(dbCtx context.Context, tx store.Tx) error {
			ctx := dbCtx
			before, err := tx.Control.JobFailureCount(ctx, sqlitegen.JobFailureCountParams{ID: job.ID, WorkspaceID: job.WorkspaceID, Version: job.Version})
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			updated, err = tx.Control.ApplyRunResultLite(ctx, sqlitegen.ApplyRunResultLiteParams{ID: job.ID, WorkspaceID: job.WorkspaceID, Version: job.Version, Status: ns(status), ScheduledAt: ni(o.ScheduledAt.UnixMilli()), Now: e.Clock.Now().UnixMilli()})
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			recovered = status == "succeeded" && before > 0
			limit := int64(e.Config.AutoDisable.ConsecutiveFailures)
			if updated.AutoDisableThreshold.Valid {
				limit = updated.AutoDisableThreshold.Int64
			}
			if e.Config.Mode == "hosted" {
				limit = min(limit, int64(e.Config.AutoDisable.ConsecutiveFailures))
			}
			if e.Config.AutoDisable.Enabled && status == "failed" && updated.ConsecutiveFailures >= limit {
				n, err := tx.Control.DisableJob(ctx, sqlitegen.DisableJobParams{ID: job.ID, WorkspaceID: job.WorkspaceID, Version: updated.Version, Reason: ns(fmt.Sprintf("consecutive_failures:%d", updated.ConsecutiveFailures)), Now: e.Clock.Now().UnixMilli()})
				disabled = n > 0
				return err
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("finish control: %w", err)
		}
	}
	if disabled {
		e.Cache.Invalidate(job.ID)
		if updated.NextRunAt.Valid {
			_ = e.Queue.Remove(ctx, queue.Occurrence{JobID: job.ID, ScheduledAt: time.UnixMilli(updated.NextRunAt.Int64), Version: updated.Version, Trigger: "s"}.Format())
		}
		auditID, err := id.New(id.Run, e.Clock.Now())
		if err != nil {
			return err
		}
		err = e.Writer.Do(ctx, store.History, func(dbCtx context.Context, tx store.Tx) error {
			ctx := dbCtx
			return tx.History.InsertAudit(ctx, historygen.InsertAuditParams{ID: auditID, WorkspaceID: job.WorkspaceID, Action: "job.auto_disable", Target: ns(job.ID), Details: ns(fmt.Sprintf(`{"consecutive_failures":%d}`, updated.ConsecutiveFailures)), CreatedAt: e.Clock.Now().UnixMilli()})
		})
		if err != nil {
			return err
		}
		e.Metrics.Add(ctx, "cronpad.jobs.auto_disabled", 1)
		e.emit(ctx, events.JobAutoDisabled, job, run, o)
	}
	if err = e.Queue.Ack(ctx, member, retryMember, time.UnixMilli(nextAttempt.Int64)); err != nil {
		return fmt.Errorf("ack run: %w", err)
	}
	e.Metrics.Add(ctx, "cronpad.attempts", 1, attribute.String("sink", job.SinkKind), attribute.String("outcome", string(res.Outcome)))
	e.Metrics.Add(ctx, "cronpad.runs", 1, attribute.String("sink", job.SinkKind), attribute.String("status", status))
	e.Metrics.Record(ctx, "cronpad.run.sink_duration", float64(duration.Microseconds())/1000, attribute.String("sink", job.SinkKind), attribute.String("outcome", string(res.Outcome)))
	if status != "deferred" {
		e.Metrics.Record(ctx, "cronpad.run.e2e", float64(e.Clock.Since(o.ScheduledAt).Microseconds())/1000, attribute.String("sink", job.SinkKind), attribute.String("status", status))
	}
	event := events.RunFailed
	if status == "succeeded" {
		event = events.RunSucceeded
	}
	if status == "deferred" {
		event = events.RunDeferred
	}
	e.emit(ctx, event, job, run, o)
	if recovered {
		e.emit(ctx, events.JobRecovered, job, run, o)
	}
	if res.Outcome == sink.Timeout {
		e.emit(ctx, events.RunTimeout, job, run, o)
	}
	e.Log.InfoContext(ctx, "run finished", logx.Outcome, res.Outcome, logx.StatusCode, res.Code, logx.DurationMS, duration.Milliseconds(), logx.StartLagMS, run.StartLagMs.Int64, logx.SLABreached, run.SlaBreached != 0, logx.Error, res.Error)
	return nil
}

// deliver owns and joins the lease extender on every return path.
func (e *Engine) deliver(ctx context.Context, member string, job sqlitegen.Job, run historygen.Run, o queue.Occurrence) (sink.ExecResult, time.Time, time.Duration) {
	leaseCtx, stopLease := context.WithCancel(ctx)
	leaseDone := make(chan struct{})
	go func() { defer close(leaseDone); e.extend(leaseCtx, member) }()
	start := e.Clock.Now()
	res := e.execute(ctx, job, run, o)
	stopLease()
	<-leaseDone
	return res, start, e.Clock.Since(start)
}

func final(s string) bool {
	return s == "succeeded" || s == "failed" || s == "skipped" || s == "cancelled"
}
func (e *Engine) emit(ctx context.Context, kind string, j sqlitegen.Job, r historygen.Run, o queue.Occurrence) {
	e.Events.Publish(ctx, events.Event{Type: kind, WorkspaceID: j.WorkspaceID, JobID: j.ID, RunID: r.ID, At: e.Clock.Now(), Attempt: o.Attempt, Error: r.LastError.String})
}
func (e *Engine) execute(ctx context.Context, j sqlitegen.Job, r historygen.Run, o queue.Occurrence) (res sink.ExecResult) {
	defer func() {
		if p := recover(); p != nil {
			res = sink.ExecResult{Outcome: sink.Fatal, Error: fmt.Sprintf("sink panic: %v", p)}
			e.Log.ErrorContext(ctx, "sink panicked", logx.Error, res.Error)
		}
	}()
	s := e.Sinks[j.SinkKind]
	if s == nil {
		return sink.ExecResult{Outcome: sink.Fatal, Error: "sink disabled or unavailable"}
	}
	var secrets []sink.SigningSecret
	plain := j.SigningSecrets.String
	var resolver sink.SecretResolver
	if e.Vault != nil {
		var err error
		plain, err = e.Vault.Unwrap(plain, "signing:"+j.ID)
		if err != nil {
			return sink.ExecResult{Outcome: sink.Fatal, Error: "cannot decrypt signing secrets"}
		}
		resolver = e.Vault.Resolver(j.WorkspaceID)
	}
	_ = json.Unmarshal([]byte(plain), &secrets)
	ctx, cancel := context.WithTimeout(ctx, time.Duration(j.TimeoutMs)*time.Millisecond)
	defer cancel()
	res = s.Execute(ctx, sink.ExecRequest{Job: sink.JobSnapshot{ID: j.ID, Name: j.Name, WorkspaceID: j.WorkspaceID, Config: json.RawMessage(j.SinkConfig), Timeout: time.Duration(j.TimeoutMs) * time.Millisecond, SigningSecrets: secrets}, RunID: r.ID, ScheduledAt: o.ScheduledAt, Attempt: o.Attempt, IdempotencyKey: r.ID, Secrets: resolver})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.Outcome = sink.Timeout
		if res.Error == "" {
			res.Error = "sink execution timed out"
		}
	}
	switch res.Outcome {
	case sink.Success, sink.Retryable, sink.Fatal, sink.Timeout:
	default:
		res.Outcome = sink.Fatal
		res.Error = "sink returned an invalid outcome"
	}
	return res
}
func (e *Engine) extend(ctx context.Context, member string) {
	ticker := e.Clock.NewTicker(max(time.Millisecond, e.Config.Scheduler.LeaseDuration.Duration()/3))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			n, err := e.Queue.Extend(ctx, member, e.Clock.Now().Add(e.Config.Scheduler.LeaseDuration.Duration()))
			if err != nil {
				e.Metrics.Add(ctx, "cronpad.queue.errors", 1, attribute.String("op", "extend"))
				e.Log.WarnContext(ctx, "lease extension failed", logx.Error, err)
				continue
			}
			if n == 0 {
				e.Metrics.Add(ctx, "cronpad.leases.lost", 1)
				e.Log.WarnContext(ctx, "lease lost")
				return
			}
			if n == 2 {
				e.Log.WarnContext(ctx, "lease rescued after reap")
			}
		}
	}
}
func (e *Engine) cancel(ctx context.Context, j sqlitegen.Job, o queue.Occurrence) error {
	if e.Limits != nil {
		e.Limits.Release(OccurrenceRunID(o))
	}
	run, err := e.Store.HistoryReader().FindOccurrence(ctx, historygen.FindOccurrenceParams{JobID: j.ID, WorkspaceID: j.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: TriggerName(o.Trigger)})
	if err == nil && run.JobVersion == o.Version && !final(run.Status) {
		err = e.Writer.Do(ctx, store.History, func(dbCtx context.Context, tx store.Tx) error {
			ctx := dbCtx
			_, err := tx.History.CompleteRun(ctx, historygen.CompleteRunParams{ID: run.ID, WorkspaceID: j.WorkspaceID, Status: "cancelled", Attempts: run.Attempts, FinishedAt: ni(e.Clock.Now().UnixMilli())})
			return err
		})
		if err != nil {
			return err
		}
		e.Metrics.Add(ctx, "cronpad.runs", 1, attribute.String("sink", j.SinkKind), attribute.String("status", "cancelled"))
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return e.Queue.Ack(ctx, o.Format(), "", time.Time{})
}

type finishSlot struct {
	mu   sync.Mutex
	refs int
}

func (e *Engine) finishLock(job string) func() {
	e.mu.Lock()
	if e.finishing == nil {
		e.finishing = map[string]*finishSlot{}
	}
	slot := e.finishing[job]
	if slot == nil {
		slot = &finishSlot{}
		e.finishing[job] = slot
	}
	slot.refs++
	e.mu.Unlock()
	slot.mu.Lock()
	return func() {
		slot.mu.Unlock()
		e.mu.Lock()
		slot.refs--
		if slot.refs == 0 {
			delete(e.finishing, job)
		}
		e.mu.Unlock()
	}
}

// Skip records a visible skipped occurrence without consuming quota or delivering.
func (e *Engine) Skip(ctx context.Context, j sqlitegen.Job, o queue.Occurrence, reason string) error {
	recorded := false
	if e.Limits != nil {
		e.Limits.Release(OccurrenceRunID(o))
	}
	err := e.Writer.Do(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
		run, err := tx.History.UpsertRun(ctx, historygen.UpsertRunParams{ID: OccurrenceRunID(o), JobID: j.ID, WorkspaceID: j.WorkspaceID, JobVersion: o.Version, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: TriggerName(o.Trigger), Status: "skipped"})
		if err != nil {
			return err
		}
		if run.StartedAt.Valid || final(run.Status) && run.Status != "skipped" || run.SkipReason.Valid {
			return nil
		}
		recorded = true
		_, err = tx.History.CompleteRun(ctx, historygen.CompleteRunParams{ID: run.ID, WorkspaceID: j.WorkspaceID, Status: "skipped", Attempts: run.Attempts, SkipReason: ns(reason), FinishedAt: ni(e.Clock.Now().UnixMilli())})
		return err
	})
	if err != nil {
		return fmt.Errorf("skip occurrence: %w", err)
	}
	if recorded {
		e.Metrics.Add(ctx, "cronpad.runs", 1, attribute.String("sink", j.SinkKind), attribute.String("status", "skipped"))
	}
	return e.Queue.Ack(ctx, o.Format(), "", time.Time{})
}
