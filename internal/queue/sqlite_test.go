package queue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
)

func openTestSQLite(t *testing.T, path string, clk clockwork.Clock) *SQLite {
	t.Helper()
	q, err := NewSQLite(context.Background(), path, clk, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

func TestSQLiteSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	clk := clockwork.NewFakeClockAt(time.Unix(1700000000, 0))
	path := filepath.Join(t.TempDir(), "queue.db")
	q := openTestSQLite(t, path, clk)
	now := clk.Now()
	for _, m := range []string{"a", "b"} {
		if err := q.Add(ctx, m, now, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.Claim(ctx, now, 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := q.SetEpoch(ctx, "e1"); err != nil {
		t.Fatal(err)
	}
	if ok, err := q.Acquire(ctx, "owner", time.Minute); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q = openTestSQLite(t, path, clk)
	stats, err := q.Stats(ctx)
	if err != nil || stats != (Stats{Due: 1, Inflight: 1}) {
		t.Fatal(stats, err)
	}
	if epoch, err := q.Epoch(ctx); epoch != "e1" || err != nil {
		t.Fatal(epoch, err)
	}
	// The lock outlives a restart until its TTL, so a quick restart can't race a live owner.
	if ok, err := q.Acquire(ctx, "other", time.Minute); ok || err != nil {
		t.Fatal(ok, err)
	}
	clk.Advance(time.Minute)
	if ok, err := q.Acquire(ctx, "other", time.Minute); !ok || err != nil {
		t.Fatal(ok, err)
	}
}

func TestSQLiteSchemaVersionResets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	q := openTestSQLite(t, path, clockwork.NewRealClock())
	if err := q.Add(ctx, "a", time.Unix(1, 0), false); err != nil {
		t.Fatal(err)
	}
	if _, err := q.db.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	q = openTestSQLite(t, path, clockwork.NewRealClock())
	stats, err := q.Stats(ctx)
	if err != nil || stats != (Stats{}) {
		t.Fatal("old schema survived", stats, err)
	}
}

func TestSQLiteReplacesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.db")
	if err := os.WriteFile(path, []byte("this is not a database, just enough bytes to look like one......"), 0600); err != nil {
		t.Fatal(err)
	}
	q := openTestSQLite(t, path, clockwork.NewRealClock())
	if err := q.Add(context.Background(), "a", time.Unix(1, 0), false); err != nil {
		t.Fatal(err)
	}
	aside, err := filepath.Glob(filepath.Join(dir, "queue.db.corrupt-*"))
	if err != nil || len(aside) != 1 {
		t.Fatal("corrupt file not kept aside", aside, err)
	}
}

func TestSQLiteLockAcrossHandles(t *testing.T) {
	ctx := context.Background()
	clk := clockwork.NewFakeClockAt(time.Unix(1700000000, 0))
	path := filepath.Join(t.TempDir(), "queue.db")
	a := openTestSQLite(t, path, clk)
	b := openTestSQLite(t, path, clk)
	if ok, err := a.Acquire(ctx, "a", 15*time.Second); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := b.Acquire(ctx, "b", 15*time.Second); ok || err != nil {
		t.Fatal("second process acquired a held lock", ok, err)
	}
	clk.Advance(15 * time.Second)
	if ok, err := b.Acquire(ctx, "b", 15*time.Second); !ok || err != nil {
		t.Fatal("expired lock not taken over", ok, err)
	}
	if ok, err := a.Renew(ctx, "a", 15*time.Second); ok || err != nil {
		t.Fatal("previous owner renewed a lost lock", ok, err)
	}
}

func TestSQLiteContainsManyMembers(t *testing.T) {
	ctx := context.Background()
	q := openTestSQLite(t, filepath.Join(t.TempDir(), "queue.db"), clockwork.NewRealClock())
	ms := make([]string, 2000)
	for i := range ms {
		ms[i] = fmt.Sprintf("job_%04d|1700000000000|0|1|s", i)
		if i%2 == 0 {
			if err := q.Add(ctx, ms[i], time.Unix(1, 0), false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := q.Claim(ctx, time.Unix(1, 0), 10, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := q.Contains(ctx, ms)
	if err != nil {
		t.Fatal(err)
	}
	for i, ok := range got {
		if ok != (i%2 == 0) {
			t.Fatalf("member %d: present=%v", i, ok)
		}
	}
}

func TestSQLiteReadOnlyFileFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.db")
	q := openTestSQLite(t, path, clockwork.NewRealClock())
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Chmod(p, 0400)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	q, err := NewSQLite(ctx, path, clockwork.NewRealClock(), 100*time.Millisecond)
	if err != nil {
		return // refusing to open is an acceptable failure
	}
	t.Cleanup(func() { _ = q.Close() })
	if err = q.Add(ctx, "a", time.Unix(1, 0), false); err == nil {
		t.Fatal("write to a read-only queue succeeded")
	}
}
