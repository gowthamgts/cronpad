package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gowthamgts/cronpad/internal/store/historygen"
)

func TestMonthlyUsageRollbackRetentionAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	control, history := filepath.Join(dir, "c.db"), filepath.Join(dir, "h.db")
	dbs, err := Open(ctx, control, history, time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dbs.Close() }()
	day := int64(100 * 86400000)
	if _, err = dbs.History.Writer.ExecContext(ctx, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status,claimed_at,started_at) VALUES('before','j','w',0,'schedule','running',?,?),('pending','j','w',1,'schedule','deferred',NULL,NULL)`, day, day); err != nil {
		t.Fatal(err)
	}
	if _, err = dbs.History.Writer.ExecContext(ctx, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status,skip_reason) VALUES('skip','j','w',2,'schedule','skipped','misfire'),('cancel','j','w',3,'manual','cancelled',NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err = dbs.History.Writer.ExecContext(ctx, `UPDATE runs SET status='succeeded' WHERE id='before'`); err != nil {
		t.Fatal(err)
	}
	count := func(want int64) {
		t.Helper()
		n, e := New(dbs).HistoryReader().CountMonthlyRuns(ctx, historygen.CountMonthlyRunsParams{WorkspaceID: "w", Month: time.UnixMilli(day).UTC().Format("2006-01")})
		if e != nil || n != want {
			t.Fatalf("monthly=%d want=%d err=%v", n, want, e)
		}
	}
	count(1)
	var reason string
	var last sql.NullString
	if err = dbs.History.Reader.QueryRowContext(ctx, `SELECT skip_reason,last_error FROM runs WHERE id='skip'`).Scan(&reason, &last); err != nil || reason != "misfire" || last.Valid {
		t.Fatal(reason, last, err)
	}
	tx, err := dbs.History.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET status='running',started_at=? WHERE id='pending'`, day); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	count(1)
	if _, err = dbs.History.Writer.ExecContext(ctx, `UPDATE runs SET status='running',started_at=? WHERE id='pending'`, day); err != nil {
		t.Fatal(err)
	}
	count(2)
	if _, err = dbs.History.Writer.ExecContext(ctx, `UPDATE runs SET claimed_at=claimed_at WHERE id='pending'`); err != nil {
		t.Fatal(err)
	}
	count(2)
	if _, err = dbs.History.Writer.ExecContext(ctx, `DELETE FROM runs`); err != nil {
		t.Fatal(err)
	}
	count(2)
	if err = dbs.Close(); err != nil {
		t.Fatal(err)
	}
	dbs, err = Open(ctx, control, history, time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	count(2)
	n, err := New(dbs).HistoryReader().CountMonthlyRuns(ctx, historygen.CountMonthlyRunsParams{WorkspaceID: "w", Month: "2099-01"})
	if err != nil || n != 0 {
		t.Fatalf("next day=%d err=%v", n, err)
	}
	rows, err := dbs.History.Reader.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT runs FROM workspace_usage_monthly WHERE workspace_id='w' AND month='1970-04'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	indexed := false
	for rows.Next() {
		var a, b, c int
		var detail string
		if err = rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		indexed = indexed || strings.Contains(detail, "SEARCH workspace_usage_monthly USING INDEX")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("monthly usage must use primary-key lookup")
	}
}
