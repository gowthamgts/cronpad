package janitor_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/janitor"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/testutil"
)

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
func scalar(t *testing.T, db *sql.DB, q string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func runPass(t *testing.T, f *testutil.Fixture, j *janitor.Janitor) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- j.Pass(context.Background()) }()
	var err error
	finished := false
	f.Until(t, func() bool {
		select {
		case err = <-done:
			finished = true
		default:
		}
		return finished
	})
	if err != nil {
		t.Fatal(err)
	}
}
func start(t *testing.T, f *testutil.Fixture) *janitor.Janitor {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Writer.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	c := f.Config.Retention
	c.JanitorBatch = 2
	return &janitor.Janitor{Writer: f.Writer, Clock: f.Clock, Config: c, Metrics: f.Metrics, Log: slog.New(slog.DiscardHandler)}
}
func TestRetentionRollupAndVacuum(t *testing.T) {
	f := testutil.New(t)
	j := start(t, f)
	db := f.DBs.History.Writer
	old := f.Clock.Now().Add(-31 * 24 * time.Hour).UnixMilli()
	fresh := f.Clock.Now().UnixMilli()
	for n := 0; n < 5; n++ {
		status := "succeeded"
		if n == 4 {
			status = "failed"
		}
		exec(t, db, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status,start_lag_ms,sla_breached) VALUES(?,?,?,?,'schedule',?,?,?)`, fmt.Sprint(n), "job_test", "ws_test", old+int64(n), status, n*10, n%2)
		exec(t, db, `INSERT INTO attempts(run_id,n,started_at,outcome,response_body) VALUES(?,1,?,'fatal',?)`, fmt.Sprint(n), old, string(make([]byte, 20000)))
	}
	exec(t, db, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status,start_lag_ms) VALUES('late','job_test','ws_test',?,'schedule','deferred',50)`, old+10)
	exec(t, db, `INSERT INTO job_stats_daily(job_id,day,runs) VALUES('expired-stat',?,1)`, f.Clock.Now().Add(-401*24*time.Hour).UnixMilli()/86400000)
	exec(t, db, `INSERT INTO job_stats_lags VALUES('expired-stat',?,1,1)`, f.Clock.Now().Add(-401*24*time.Hour).UnixMilli()/86400000)
	exec(t, db, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status) VALUES('fresh','j','w',?,'manual','failed')`, fresh)
	exec(t, db, `INSERT INTO attempts VALUES('fresh',1,?,NULL,NULL,'fatal',NULL,NULL,NULL,'body')`, old)
	for _, status := range []string{"pending", "sent", "failed", "suppressed"} {
		exec(t, db, `INSERT INTO alert_outbox(id,workspace_id,rule_id,channel_id,event_type,dedupe_key,payload,status,next_attempt_at,created_at) VALUES(?,'w','r','c','failed','d','{}',?,0,?)`, status, status, old)
	}
	exec(t, db, `INSERT INTO audit_log VALUES('old','w','system','test',NULL,NULL,?)`, old)
	exec(t, f.DBs.Control.Writer, `INSERT INTO sessions VALUES('expired',x'00',?)`, old)
	exec(t, f.DBs.Control.Writer, `INSERT INTO sessions VALUES('live',x'00',?)`, fresh+100000)
	runPass(t, f, j)
	for q, want := range map[string]int64{
		`SELECT count(*) FROM runs`: 2, `SELECT count(*) FROM attempts`: 1, `SELECT count(response_body) FROM attempts`: 0,
		`SELECT count(*) FROM alert_outbox`: 1, `SELECT count(*) FROM alert_outbox WHERE status='pending'`: 1, `SELECT count(*) FROM audit_log`: 0,
		`SELECT runs FROM job_stats_daily WHERE job_id='job_test'`: 5, `SELECT succeeded FROM job_stats_daily WHERE job_id='job_test'`: 4,
		`SELECT failed FROM job_stats_daily WHERE job_id='job_test'`: 1, `SELECT sla_breached FROM job_stats_daily WHERE job_id='job_test'`: 2,
		`SELECT p50_lag_ms FROM job_stats_daily WHERE job_id='job_test'`: 20, `SELECT p95_lag_ms FROM job_stats_daily WHERE job_id='job_test'`: 40,
		`SELECT p99_lag_ms FROM job_stats_daily WHERE job_id='job_test'`: 40,
	} {
		if got := scalar(t, db, q); got != want {
			t.Fatalf("%s: got %d want %d", q, got, want)
		}
	}
	if n := scalar(t, f.DBs.Control.Writer, `SELECT count(*) FROM sessions`); n != 1 {
		t.Fatal(n)
	}
	runPass(t, f, j)
	if n := scalar(t, db, `SELECT runs FROM job_stats_daily WHERE job_id='job_test'`); n != 5 {
		t.Fatal("rollup not idempotent", n)
	}
	if n := scalar(t, db, `SELECT count(*) FROM job_stats_lags WHERE job_id='expired-stat'`); n != 0 {
		t.Fatal("expired histogram survived", n)
	}
	exec(t, db, `UPDATE runs SET status='failed',sla_breached=1 WHERE id='late'`)
	runPass(t, f, j)
	if n := scalar(t, db, `SELECT runs FROM job_stats_daily WHERE job_id='job_test'`); n != 6 {
		t.Fatal("late completion lost", n)
	}
	if n := scalar(t, db, `SELECT count(*) FROM runs WHERE id='late'`); n != 0 {
		t.Fatal("terminal expired run survived", n)
	}
	// A large disposable allocation proves incremental vacuum returns free pages.
	exec(t, db, `CREATE TABLE vacuum_fixture(body BLOB)`)
	exec(t, db, `INSERT INTO vacuum_fixture VALUES(zeroblob(1000000))`)
	exec(t, db, `DELETE FROM vacuum_fixture`)
	free := scalar(t, db, `PRAGMA freelist_count`)
	pages := scalar(t, db, `PRAGMA page_count`)
	runPass(t, f, j)
	if after := scalar(t, db, `PRAGMA freelist_count`); after >= free {
		t.Fatalf("freelist %d -> %d", free, after)
	}
	if after := scalar(t, db, `PRAGMA page_count`); after >= pages {
		t.Fatalf("pages %d -> %d", pages, after)
	}
}
func TestZeroRetention(t *testing.T) {
	f := testutil.New(t)
	j := start(t, f)
	j.Config = config.ConfigRetention{JanitorBatch: 1}
	exec(t, f.DBs.History.Writer, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status) VALUES('r','j','w',0,'manual','failed')`)
	runPass(t, f, j)
	if n := scalar(t, f.DBs.History.Writer, `SELECT count(*) FROM runs`); n != 1 {
		t.Fatal(n)
	}
}
func TestPressureYieldsAndBatches(t *testing.T) {
	f := testutil.New(t)
	j := &janitor.Janitor{Writer: f.Writer, Clock: f.Clock, Config: f.Config.Retention, Metrics: f.Metrics, Log: slog.New(slog.DiscardHandler)}
	for i := 0; i < 5001; i++ {
		if _, e := f.Writer.Submit(context.Background(), store.History, func(context.Context, store.Tx) error { return nil }); e != nil {
			t.Fatal(e)
		}
	}
	if !f.Writer.Busy(store.History) {
		t.Fatal("expected pressure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- j.Pass(ctx) }()
	if err := f.Clock.BlockUntilContext(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		t.Fatalf("did not yield: %v", e)
	default:
	}
	cancel()
	if e := <-done; e == nil {
		t.Fatal("expected cancellation")
	}
}

type observedStore struct {
	store.Store
	maxDeleted int64
}

func (s *observedStore) WithTx(ctx context.Context, db store.Database, fn func(store.Tx) error) error {
	return s.Store.WithTx(ctx, db, func(tx store.Tx) error {
		var before, after int64
		if db == store.History {
			if e := tx.SQL.QueryRowContext(ctx, "SELECT count(*) FROM runs").Scan(&before); e != nil {
				return e
			}
		}
		if e := fn(tx); e != nil {
			return e
		}
		if db == store.History {
			if e := tx.SQL.QueryRowContext(ctx, "SELECT count(*) FROM runs").Scan(&after); e != nil {
				return e
			}
			s.maxDeleted = max(s.maxDeleted, before-after)
		}
		return nil
	})
}
func TestDeletionTransactionsAreBounded(t *testing.T) {
	f := testutil.New(t)
	observed := &observedStore{Store: f.Store}
	f.Writer = results.New(observed, f.Clock, f.Metrics, time.Minute)
	j := start(t, f)
	for i := 0; i < 9; i++ {
		exec(t, f.DBs.History.Writer, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status) VALUES(?,'j','w',?,'manual','failed')`, fmt.Sprint(i), i)
	}
	runPass(t, f, j)
	if observed.maxDeleted != 2 {
		t.Fatalf("maximum deleted per tx=%d want 2", observed.maxDeleted)
	}
}

func TestHourlyRollupIndependentOfPruneInterval(t *testing.T) {
	f := testutil.New(t)
	j := start(t, f)
	j.Config.JanitorInterval = config.Duration(2 * time.Hour)
	exec(t, f.DBs.History.Writer, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status) VALUES('hourly','j','w',0,'manual','failed')`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- j.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	// The janitor's prune and hourly tickers; the group-commit writer holds none.
	if err := f.Clock.BlockUntilContext(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	f.Clock.Advance(time.Hour)
	f.Until(t, func() bool { return scalar(t, f.DBs.History.Reader, `SELECT count(*) FROM job_stats_daily`) == 1 })
	if n := scalar(t, f.DBs.History.Reader, `SELECT count(*) FROM runs`); n != 1 {
		t.Fatal("pruned before configured interval", n)
	}
}

type retentionProvider struct{}

func (retentionProvider) For(_ context.Context, ws string) (limits.Set, error) {
	if ws == "global" {
		return limits.Set{}, nil
	}
	days := 24 * time.Hour
	if ws == "long" {
		days = 10 * 24 * time.Hour
	}
	return limits.Set{Retention: limits.Retention{Runs: days, AttemptBodies: days, AuditLog: days}}, nil
}
func TestPerWorkspaceRetention(t *testing.T) {
	f := testutil.New(t)
	j := start(t, f)
	j.Store = f.Store
	j.Limits = retentionProvider{}
	old := f.Clock.Now().Add(-5 * 24 * time.Hour).UnixMilli()
	for _, ws := range []string{"short", "long", "global"} {
		exec(t, f.DBs.Control.Writer, `INSERT INTO workspaces(id,name,slug,plan,created_at) VALUES(?,?,?,'free',0)`, ws, ws, ws)
		exec(t, f.DBs.History.Writer, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status) VALUES(?,?,?,?,'schedule','failed')`, ws, ws, ws, old)
		exec(t, f.DBs.History.Writer, `INSERT INTO attempts(run_id,n,started_at,outcome,response_body) VALUES(?,0,?,'fatal','body')`, ws, old)
		exec(t, f.DBs.History.Writer, `INSERT INTO audit_log(id,workspace_id,actor,action,created_at) VALUES(?,?,'system','test',?)`, ws, ws, old)
	}
	runPass(t, f, j)
	for _, table := range []string{"runs", "audit_log"} {
		if got := scalar(t, f.DBs.History.Writer, `SELECT count(*) FROM `+table+` WHERE workspace_id='global'`); got != 1 {
			t.Fatal(table, got)
		}
		if got := scalar(t, f.DBs.History.Writer, `SELECT count(*) FROM `+table+` WHERE workspace_id='short'`); got != 0 {
			t.Fatal(table, got)
		}
		if got := scalar(t, f.DBs.History.Writer, `SELECT count(*) FROM `+table+` WHERE workspace_id='long'`); got != 1 {
			t.Fatal(table, got)
		}
	}
	if got := scalar(t, f.DBs.History.Writer, `SELECT count(response_body) FROM attempts WHERE run_id='long'`); got != 1 {
		t.Fatal(got)
	}
}
