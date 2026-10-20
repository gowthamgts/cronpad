//go:build integration

package dispatcher_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/queue"
)

// testQueue is a real SQLite queue plus a way to destroy its contents, which
// the dispatcher must recover from.
type testQueue struct {
	queue.Queue
	lose func(t *testing.T)
}

func realQueue(t *testing.T) testQueue {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	return testQueue{sqliteQueue(t, path, clockwork.NewRealClock()), func(t *testing.T) { emptySQLiteQueue(t, path) }}
}

func sqliteQueue(t *testing.T, path string, clk clockwork.Clock) *queue.SQLite {
	t.Helper()
	q, err := queue.NewSQLite(context.Background(), path, clk, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

// emptySQLiteQueue deletes every row through a second connection, as an
// operator deleting queue.db contents (or a lost file) would.
func emptySQLiteQueue(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.ExecContext(t.Context(), `DELETE FROM due; DELETE FROM inflight; DELETE FROM meta;`); err != nil {
		t.Fatal(err)
	}
}
