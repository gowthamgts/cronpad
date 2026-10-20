//go:build integration

package dispatcher_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/testutil"
	"github.com/gowthamgts/cronpad/internal/worker"
)

// The child deliberately never starts its writer, then is killed after delivery.
// This exercises actual process loss of an enqueued admission, with a real queue
// shared by the killed child and the restarted parent.
func TestFastSIGKILLBeforeHistoryCommit(t *testing.T) {
	ctx := context.Background()
	child := os.Getenv("LAG_CRASH_CHILD") == "1"
	dir := os.Getenv("LAG_CRASH_DIR")
	if !child {
		dir = t.TempDir()
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestFastSIGKILLBeforeHistoryCommit$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), "LAG_CRASH_CHILD=1", "LAG_CRASH_DIR="+dir, "TMPDIR="+dir)
		log, err := os.Create(filepath.Join(dir, "child.log"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = log.Close() }()
		cmd.Stdout, cmd.Stderr = log, log
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		clk := clockwork.NewRealClock()
		await(t, clk, 10*time.Second, func() bool { _, err := os.Stat(filepath.Join(dir, "delivered")); return err == nil })
		if err = cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		if err = cmd.Wait(); err == nil {
			t.Fatal("child was not killed")
		}
	}
	f := testutil.New(t)
	dbs, err := store.Open(ctx, filepath.Join(dir, "control.db"), filepath.Join(dir, "history.db"), time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbs.Close() })
	f.DBs, f.Store = dbs, store.New(dbs)
	f.Queue = sqliteQueue(t, filepath.Join(dir, "queue.db"), f.Clock)
	f.Writer = results.New(f.Store, f.Clock, f.Metrics, time.Minute)
	f.Engine.Store, f.Engine.Queue, f.Engine.Writer = f.Store, f.Queue, f.Writer
	f.Engine.Config.Scheduler.DeliveryMode = "fast"
	f.Config.Scheduler.DeliveryMode = "fast"
	o := queue.Occurrence{JobID: "job_sigkill", ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
	if child {
		f.Job(t, "sigkill", o.ScheduledAt)
		if err = f.Queue.Add(ctx, o.Format(), o.ScheduledAt, false); err != nil {
			t.Fatal(err)
		}
		if _, err = f.Queue.Claim(ctx, f.Clock.Now(), 1, time.Minute); err != nil {
			t.Fatal(err)
		}
		f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(_ context.Context, req sink.ExecRequest) sink.ExecResult {
			if err := os.WriteFile(filepath.Join(dir, "delivered"), []byte(req.IdempotencyKey), 0600); err != nil {
				panic(err)
			}
			return sink.ExecResult{Outcome: sink.Success, Code: 204}
		})
		if err = f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()}); err != nil {
			t.Fatal(err)
		}
		t.Fatal("unstarted writer unexpectedly committed")
	}
	first, err := os.ReadFile(filepath.Join(dir, "delivered"))
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = f.DBs.History.Reader.QueryRowContext(ctx, `SELECT count(*) FROM runs`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal(rows, err)
	}
	f.Clock.Advance(10 * time.Minute)
	if _, err = f.DBs.Control.Writer.ExecContext(ctx, `UPDATE jobs SET misfire_policy='skip'`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(_ context.Context, req sink.ExecRequest) sink.ExecResult {
		calls.Add(1)
		if req.IdempotencyKey != string(first) {
			t.Errorf("key changed %s -> %s", first, req.IdempotencyKey)
		}
		return sink.ExecResult{Outcome: sink.Success, Code: 204}
	})
	d := f.Dispatcher()
	cancel, _ := start(t, f, d)
	f.Until(t, func() bool {
		var n int
		err := f.DBs.History.Reader.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE status='succeeded'`).Scan(&n)
		return err == nil && n == 1
	})
	cancel()
	f.Until(t, func() bool { return d.State() == "stopped" })
	var success int
	if err = f.DBs.History.Reader.QueryRowContext(ctx, `SELECT count(*),sum(status='succeeded') FROM runs`).Scan(&rows, &success); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || success != 1 || calls.Load() != 1 {
		t.Fatal(rows, success, calls.Load())
	}
	t.Logf("SIGKILL enqueue-before-commit + 10min downtime + skip-policy startup: rows=%d succeeded=%d deliveries=2 stable_key=%s", rows, success, first)
}
