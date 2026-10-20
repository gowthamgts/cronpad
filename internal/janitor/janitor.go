// Package janitor rolls up history and prunes it through the result writer.
package janitor

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/attribute"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/telemetry"
)

type Janitor struct {
	Store      store.Store
	Limits     limits.Provider
	Writer     *results.Writer
	Clock      clockwork.Clock
	Config     config.ConfigRetention
	Metrics    *telemetry.Instruments
	Log        *slog.Logger
	nextRollup time.Time
}

func (j *Janitor) Run(ctx context.Context) error {
	ticker := j.Clock.NewTicker(j.Config.JanitorInterval.Duration())
	defer ticker.Stop()
	hourly := j.Clock.NewTicker(time.Hour)
	defer hourly.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-hourly.Chan():
			if err := j.rollup(ctx); err != nil && ctx.Err() == nil {
				j.Log.ErrorContext(ctx, "hourly rollup failed", logx.Error, err)
			} else {
				j.nextRollup = j.Clock.Now().Add(time.Hour)
			}
		case <-ticker.Chan():
			if err := j.Pass(ctx); err != nil && ctx.Err() == nil {
				j.Log.ErrorContext(ctx, "janitor failed", logx.Error, err)
			}
		}
	}
}

func (j *Janitor) pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-j.Clock.After(10 * time.Millisecond):
		return nil
	}
}
func (j *Janitor) maintain(ctx context.Context, db store.Database, fn func(context.Context, store.Tx) error) error {
	for j.Writer.Busy(db) {
		if err := j.pause(ctx); err != nil {
			return err
		}
	}
	return j.Writer.Maintain(ctx, db, fn)
}

// Pass always finishes the rollup before deleting raw history.
func (j *Janitor) Pass(ctx context.Context) (err error) {
	start := j.Clock.Now()
	var total int64
	defer func() {
		elapsed := float64(j.Clock.Since(start).Microseconds()) / 1000
		j.Metrics.Record(ctx, "cronpad.janitor.duration", elapsed)
		j.Log.InfoContext(ctx, "janitor pass", logx.DurationMS, elapsed, logx.Deleted, total, logx.Error, err)
	}()
	// DECISION: retention shorter than an hour still requires a rollup before every prune.
	if !start.Before(j.nextRollup) || j.Config.Runs.Duration() > 0 {
		if err = j.rollup(ctx); err != nil {
			return err
		}
		j.nextRollup = start.Add(time.Hour)
	}
	type prune struct {
		table, statement string
		db               store.Database
		window           time.Duration
		cutoff           int64
		workspace        string
	}
	// DECISION: preserve running/deferred recovery state until terminal; only
	// accounted terminal runs may be pruned, even with sub-second retention.
	rules := []prune{
		{"runs", `DELETE FROM runs WHERE rowid IN (SELECT rowid FROM runs WHERE scheduled_at<? AND stats_rolled=1 LIMIT ?)`, store.History, j.Config.Runs.Duration(), 0, ""},
		{"attempts", `UPDATE attempts SET response_body=NULL WHERE rowid IN (SELECT rowid FROM attempts WHERE started_at<? AND response_body IS NOT NULL LIMIT ?)`, store.History, j.Config.AttemptBodies.Duration(), 0, ""},
		{"audit_log", `DELETE FROM audit_log WHERE rowid IN (SELECT rowid FROM audit_log WHERE created_at<? LIMIT ?)`, store.History, j.Config.AuditLog.Duration(), 0, ""},
		{"alert_outbox", `DELETE FROM alert_outbox WHERE rowid IN (SELECT rowid FROM alert_outbox WHERE status<>'pending' AND created_at<? LIMIT ?)`, store.History, j.Config.AlertOutbox.Duration(), 0, ""},
		{"sessions", `DELETE FROM sessions WHERE rowid IN (SELECT rowid FROM sessions WHERE expiry<? LIMIT ?)`, store.Control, time.Nanosecond, start.UnixMilli(), ""},
		{"workspace_daily_usage", `DELETE FROM workspace_daily_usage WHERE rowid IN (SELECT rowid FROM workspace_daily_usage WHERE day<? LIMIT ?)`, store.History, 48 * time.Hour, start.UnixMilli()/86400000 - 2, ""},
		{"job_stats_daily", `DELETE FROM job_stats_daily WHERE rowid IN (SELECT rowid FROM job_stats_daily WHERE day<? LIMIT ?)`, store.History, j.Config.JobStatsDaily.Duration(), 0, ""},
	}
	rules = append(rules, prune{"workspace_usage_monthly", `DELETE FROM workspace_usage_monthly WHERE rowid IN (SELECT rowid FROM workspace_usage_monthly WHERE month<? LIMIT ?)`, store.History, 400 * 24 * time.Hour, 0, ""})
	// DECISION: keep the global indexed range path; only overridden workspaces
	// need workspace-indexed batches. audit_ws_time already serves these deletes.
	exclusions := map[string][]string{}
	if j.Limits != nil && j.Store != nil {
		workspaces, e := j.Store.ControlReader().AllWorkspaceIDs(ctx)
		if e != nil {
			return e
		}
		for _, ws := range workspaces {
			set, e := j.Limits.For(ctx, ws)
			if e != nil {
				return e
			}
			for index, window := range []time.Duration{set.Retention.Runs, set.Retention.AttemptBodies, set.Retention.AuditLog} {
				base := rules[index]
				if window == 0 || window == base.window {
					continue
				}
				rule := base
				rule.window = window
				rule.workspace = ws
				switch rule.table {
				case "runs":
					rule.statement = `DELETE FROM runs WHERE rowid IN (SELECT rowid FROM runs WHERE scheduled_at<? AND stats_rolled=1 AND workspace_id=? LIMIT ?)`
				case "attempts":
					rule.statement = `UPDATE attempts SET response_body=NULL WHERE rowid IN (SELECT a.rowid FROM runs r JOIN attempts a ON a.run_id=r.id WHERE a.started_at<? AND a.response_body IS NOT NULL AND r.workspace_id=? LIMIT ?)`
				case "audit_log":
					rule.statement = `DELETE FROM audit_log WHERE rowid IN (SELECT rowid FROM audit_log WHERE created_at<? AND workspace_id=? LIMIT ?)`
				}
				rules = append(rules, rule)
				exclusions[rule.table] = append(exclusions[rule.table], ws)
			}
		}
	}
	for _, r := range rules {
		if r.window == 0 {
			continue
		}
		cutoff := r.cutoff
		if cutoff == 0 {
			cutoff = start.Add(-r.window).UnixMilli()
			if r.table == "job_stats_daily" {
				cutoff /= 86400000
			}
		}
		for {
			var n int64
			err = j.maintain(ctx, r.db, func(ctx context.Context, tx store.Tx) error {
				args := []any{cutoff}
				statement := r.statement
				if r.table == "workspace_usage_monthly" {
					args = []any{start.Add(-r.window).UTC().Format("2006-01")}
				}
				if r.workspace != "" {
					args = append(args, r.workspace)
				} else if excluded := exclusions[r.table]; len(excluded) > 0 {
					placeholders := strings.TrimSuffix(strings.Repeat("?,", len(excluded)), ",")
					filter := "workspace_id NOT IN (" + placeholders + ")"
					if r.table == "attempts" {
						filter = "run_id NOT IN (SELECT id FROM runs WHERE workspace_id IN (" + placeholders + "))"
					}
					statement = strings.Replace(statement, " LIMIT ?)", " AND "+filter+" LIMIT ?)", 1)
					for _, ws := range excluded {
						args = append(args, ws)
					}
				}
				args = append(args, j.Config.JanitorBatch)
				result, e := tx.SQL.ExecContext(ctx, statement, args...)
				if e != nil {
					return fmt.Errorf("prune %s: %w", r.table, e)
				}
				n, e = result.RowsAffected()
				return e
			})
			if err != nil {
				return err
			}
			total += n
			j.Metrics.Add(ctx, "cronpad.janitor.deleted", n, attribute.String("table", r.table))
			if n < int64(j.Config.JanitorBatch) {
				break
			}
			if err = j.pause(ctx); err != nil {
				return err
			}
		}
	}
	for _, db := range []store.Database{store.Control, store.History} {
		if err = j.maintain(ctx, db, func(ctx context.Context, tx store.Tx) error {
			_, e := tx.SQL.ExecContext(ctx, "PRAGMA incremental_vacuum(2000)")
			return e
		}); err != nil {
			return err
		}
	}
	return nil
}

type sample struct {
	id, job, workspace, status string
	day, breach                int64
	lag                        sql.NullInt64
}

func (j *Janitor) rollup(ctx context.Context) error {
	// DECISION: each sample entails several writes; cap rollup transactions at
	// 100 samples as well as janitor_batch to bound time spent ahead of results.
	batch := min(j.Config.JanitorBatch, 100)
	for {
		var count int
		err := j.maintain(ctx, store.History, func(ctx context.Context, tx store.Tx) error {
			rows, e := tx.SQL.QueryContext(ctx, `SELECT id,job_id,workspace_id,status,scheduled_at/86400000,sla_breached,start_lag_ms FROM runs WHERE stats_rolled=0 AND status NOT IN ('running','deferred') ORDER BY scheduled_at LIMIT ?`, batch)
			if e != nil {
				return fmt.Errorf("rollup read: %w", e)
			}
			samples := []sample{}
			for rows.Next() {
				var s sample
				if e = rows.Scan(&s.id, &s.job, &s.workspace, &s.status, &s.day, &s.breach, &s.lag); e != nil {
					_ = rows.Close()
					return e
				}
				samples = append(samples, s)
			}
			e = rows.Err()
			_ = rows.Close()
			if e != nil {
				return e
			}
			count = len(samples)
			touched := map[struct {
				job string
				day int64
			}]bool{}
			for _, s := range samples {
				succeeded, failed := 0, 0
				if s.status == "succeeded" {
					succeeded = 1
				}
				if s.status == "failed" {
					failed = 1
				}
				_, e = tx.SQL.ExecContext(ctx, `INSERT INTO job_stats_daily(job_id,workspace_id,day,runs,succeeded,failed,sla_breached) VALUES(?,?,?,1,?,?,?) ON CONFLICT(job_id,day) DO UPDATE SET runs=runs+1,succeeded=succeeded+excluded.succeeded,failed=failed+excluded.failed,sla_breached=sla_breached+excluded.sla_breached`, s.job, s.workspace, s.day, succeeded, failed, s.breach)
				if e != nil {
					return fmt.Errorf("rollup counts: %w", e)
				}
				if s.lag.Valid {
					_, e = tx.SQL.ExecContext(ctx, `INSERT INTO job_stats_lags VALUES(?,?,?,1) ON CONFLICT(job_id,day,lag_ms) DO UPDATE SET n=n+1`, s.job, s.day, s.lag.Int64)
					if e != nil {
						return e
					}
				}
				_, e = tx.SQL.ExecContext(ctx, `UPDATE runs SET stats_rolled=1 WHERE id=?`, s.id)
				if e != nil {
					return e
				}
				touched[struct {
					job string
					day int64
				}{s.job, s.day}] = true
			}
			for k := range touched {
				_, e = tx.SQL.ExecContext(ctx, `WITH ranked AS (SELECT lag_ms,sum(n) OVER(ORDER BY lag_ms) cumulative,sum(n) OVER() total FROM job_stats_lags WHERE job_id=? AND day=?) UPDATE job_stats_daily SET p50_lag_ms=(SELECT min(lag_ms) FROM ranked WHERE cumulative>=ceil(total*0.50)),p95_lag_ms=(SELECT min(lag_ms) FROM ranked WHERE cumulative>=ceil(total*0.95)),p99_lag_ms=(SELECT min(lag_ms) FROM ranked WHERE cumulative>=ceil(total*0.99)) WHERE job_id=? AND day=?`, k.job, k.day, k.job, k.day)
				if e != nil {
					return fmt.Errorf("rollup percentiles: %w", e)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if count < batch {
			return nil
		}
		if err = j.pause(ctx); err != nil {
			return err
		}
	}
}
