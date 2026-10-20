package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

func TestTransactionsAndScopedQueries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbs, e := Open(ctx, filepath.Join(dir, "c.db"), filepath.Join(dir, "h.db"), time.Second, true)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = dbs.Close() }()
	s := New(dbs)
	abort := errors.New("abort")
	e = s.WithTx(ctx, Control, func(tx Tx) error {
		if e := tx.Control.CreateWorkspace(ctx, sqlitegen.CreateWorkspaceParams{ID: "ws_rollback", Name: "rollback", Slug: "rollback", CreatedAt: 1}); e != nil {
			return e
		}
		return abort
	})
	if !errors.Is(e, abort) {
		t.Fatal(e)
	}
	if _, e = s.ControlReader().GetWorkspace(ctx, "ws_rollback"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
	e = s.Control().CreateWorkspace(ctx, sqlitegen.CreateWorkspaceParams{ID: "ws_a", Name: "a", Slug: "a", CreatedAt: 1})
	if e != nil {
		t.Fatal(e)
	}
	job, e := s.Control().CreateJob(ctx, sqlitegen.CreateJobParams{ID: "job_a", WorkspaceID: "ws_a", Name: "a", Tags: `["test"]`, ScheduleKind: "cron", ScheduleExpr: "@daily", ScheduleCanonical: "@daily", Timezone: "UTC", SinkKind: "webhook", SinkConfig: `{"url":"https://example.org"}`, RetryPolicy: `{}`, State: "active", Version: 1, OverlapPolicy: "allow", NextRunAt: sql.NullInt64{Int64: 100, Valid: true}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ControlReader().GetJob(ctx, sqlitegen.GetJobParams{ID: job.ID, WorkspaceID: "ws_other"}); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
	arg := sqlitegen.AdvanceJobParams{ID: job.ID, WorkspaceID: job.WorkspaceID, Version: 2, ScheduledAt: job.NextRunAt, NextRunAt: sql.NullInt64{Int64: 200, Valid: true}, State: "active"}
	n, e := s.Control().AdvanceJob(ctx, arg)
	if n != 0 || e != nil {
		t.Fatal(n, e)
	}
	arg.Version = 1
	n, e = s.Control().AdvanceJob(ctx, arg)
	if n != 1 || e != nil {
		t.Fatal(n, e)
	}
	n, e = s.Control().AdvanceJob(ctx, arg)
	if n != 0 || e != nil {
		t.Fatal("duplicate advance", n, e)
	}
	input := historygen.UpsertRunParams{ID: "run_a", JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: 100, Trigger: "schedule", Status: "running"}
	run, e := s.History().UpsertRun(ctx, input)
	if e != nil {
		t.Fatal(e)
	}
	input.ID = "run_duplicate"
	duplicate, e := s.History().UpsertRun(ctx, input)
	if e != nil || duplicate.ID != run.ID {
		t.Fatal(duplicate, e)
	}
	attempt := historygen.InsertAttemptParams{RunID: run.ID, WorkspaceID: "ws_other", N: 0, StartedAt: 100, Outcome: "success"}
	if e = s.History().InsertAttempt(ctx, attempt); e != nil {
		t.Fatal(e)
	}
	rows, e := s.HistoryReader().GetAttempts(ctx, historygen.GetAttemptsParams{ID: run.ID, WorkspaceID: run.WorkspaceID})
	if e != nil || len(rows) != 0 {
		t.Fatal(rows, e)
	}
	attempt.WorkspaceID = job.WorkspaceID
	e = s.WithTx(ctx, History, func(tx Tx) error { return tx.History.InsertAttempt(ctx, attempt) })
	if e != nil {
		t.Fatal(e)
	}
	rows, e = s.HistoryReader().GetAttempts(ctx, historygen.GetAttemptsParams{ID: run.ID, WorkspaceID: run.WorkspaceID})
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
}
