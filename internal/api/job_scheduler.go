package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/gowthamgts/cronpad/internal/logx"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/schedule"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

// Scheduler is the API's boundary to dispatch scheduling. Wake must never block.
type Scheduler interface {
	Enqueue(context.Context, sqlitegen.Job) error
	Remove(context.Context, sqlitegen.Job) error
	Wake()
}

// QueueScheduler persists the next occurrence before publishing its deterministic member.
// DECISION: SQLite remains authoritative when the queue is unavailable; reconciliation
// repairs publishing failures when the dispatcher starts. API writes still succeed.
type QueueScheduler struct {
	Invalidate func(string)
	Store      store.Store
	Queue      queue.Queue
	Clock      clockwork.Clock
	WakeCh     chan struct{}
	Log        *slog.Logger
}

func (s *QueueScheduler) Wake() {
	select {
	case s.WakeCh <- struct{}{}:
	default:
	}
}
func jobAnchor(j sqlitegen.Job) time.Time {
	if j.ScheduleAnchorAt.Valid {
		return time.UnixMilli(j.ScheduleAnchorAt.Int64)
	}
	return time.UnixMilli(j.CreatedAt)
}
func jobSpec(j sqlitegen.Job) schedule.Spec {
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
	return spec
}
func (s *QueueScheduler) Enqueue(ctx context.Context, j sqlitegen.Job) error {
	if s.Invalidate != nil {
		s.Invalidate(j.ID)
	}
	if j.State != "active" {
		return nil
	}
	compute, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	parsed, e := schedule.ParseContext(compute, jobSpec(j), clockwork.NewFakeClockAt(jobAnchor(j)))
	if e != nil {
		return e
	}
	at, ok := parsed.Next(s.Clock.Now())
	if compute.Err() != nil {
		return fmt.Errorf("schedule computation budget exceeded")
	}
	next := sql.NullInt64{Int64: at.UnixMilli(), Valid: ok}
	rows, e := s.Store.Control().SetNextRun(ctx, sqlitegen.SetNextRunParams{NextRunAt: next, ID: j.ID, WorkspaceID: j.WorkspaceID, Version: j.Version})
	if e != nil {
		return fmt.Errorf("save next occurrence: %w", e)
	}
	if rows == 1 && ok {
		if e := s.Queue.Add(ctx, queue.Occurrence{JobID: j.ID, ScheduledAt: at, Version: j.Version, Trigger: "s"}.Format(), at, false); e != nil {
			s.logger().WarnContext(ctx, "queue publication deferred", logx.JobID, j.ID, logx.Error, e)
		}
	}
	s.Wake()
	return nil
}
func (s *QueueScheduler) Remove(ctx context.Context, j sqlitegen.Job) error {
	if s.Invalidate != nil {
		s.Invalidate(j.ID)
	}
	if j.NextRunAt.Valid {
		if e := s.Queue.Remove(ctx, queue.Occurrence{JobID: j.ID, ScheduledAt: time.UnixMilli(j.NextRunAt.Int64), Version: j.Version, Trigger: "s"}.Format()); e != nil {
			s.logger().WarnContext(ctx, "queue removal deferred", logx.JobID, j.ID, logx.Error, e)
		}
	}
	s.Wake()
	return nil
}

func (s *QueueScheduler) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
