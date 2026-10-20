package dispatcher_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"

	"github.com/gowthamgts/cronpad/internal/logx"

	"github.com/gowthamgts/cronpad/internal/dispatcher"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/testutil"
)

func start(t *testing.T, f *testutil.Fixture, d *dispatcher.Dispatcher) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		f.Until(t, func() bool {
			select {
			case <-done:
				return true
			default:
				return d.State() == "stopped"
			}
		})
	})
	return cancel, done
}
func TestStartupRequeuesInflightAndShutdownDrains(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testStartupRequeuesInflightAndShutdownDrains(t)
		})
	}
}
func testStartupRequeuesInflightAndShutdownDrains(t *testing.T) {
	f := testutil.New(t)
	job := f.Job(t, "orphan", f.Clock.Now())
	member := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}.Format()
	_ = f.Queue.Add(context.Background(), member, f.Clock.Now(), false)
	_, _ = f.Queue.Claim(context.Background(), f.Clock.Now(), 1, time.Hour)
	d := f.Dispatcher()
	cancel, done := start(t, f, d)
	f.Until(t, func() bool {
		runs, err := f.Store.HistoryReader().StatsRuns(context.Background(), historygen.StatsRunsParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: 0})
		return err == nil && len(runs) == 1 && runs[0].Status == "succeeded"
	})
	cancel()
	f.Until(t, func() bool {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return true
		default:
			return false
		}
	})
	stats, err := f.Queue.Stats(context.Background())
	if err != nil || stats.Inflight != 0 {
		t.Fatal(stats, err)
	}
	if d.State() != "stopped" {
		t.Fatal(d.State())
	}
}
func TestSecondProcessStandbyAndPromotion(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testSecondProcessStandbyAndPromotion(t)
		})
	}
}
func testSecondProcessStandbyAndPromotion(t *testing.T) {
	f := testutil.New(t)
	d := f.Dispatcher()
	cancel, _ := start(t, f, d)
	f.Until(t, func() bool { return d.State() == "active" })
	other := testutil.New(t)
	other.Clock = f.Clock
	other.Store = f.Store
	other.Queue = f.Queue
	other.Writer = results.New(f.Store, f.Clock, f.Metrics, f.Config.Scheduler.LeaseDuration.Duration())
	other.Engine.Store = f.Store
	other.Engine.Queue = f.Queue
	other.Engine.Clock = f.Clock
	other.Engine.Writer = other.Writer
	second := other.Dispatcher()
	stop, _ := start(t, other, second)
	f.Until(t, func() bool { return second.State() == "standby" })
	cancel()
	f.Until(t, func() bool { return second.State() == "active" })
	stop()
}

type controlledQueue struct {
	queue.Queue
	lost   atomic.Bool
	claims atomic.Int64
}

func (q *controlledQueue) Acquire(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	if q.lost.Load() {
		return false, nil
	}
	return q.Queue.Acquire(ctx, token, ttl)
}
func (q *controlledQueue) Renew(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	if q.lost.Load() {
		return false, nil
	}
	return q.Queue.Renew(ctx, token, ttl)
}
func (q *controlledQueue) Claim(ctx context.Context, now time.Time, n int, lease time.Duration) ([]queue.Item, error) {
	q.claims.Add(1)
	return q.Queue.Claim(ctx, now, n, lease)
}
func TestLockLossStopsClaimingAndClockBackwards(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testLockLossStopsClaimingAndClockBackwards(t)
		})
	}
}
func testLockLossStopsClaimingAndClockBackwards(t *testing.T) {
	f := testutil.New(t)
	q := &controlledQueue{Queue: f.Queue}
	f.Queue = q
	f.Engine.Queue = q
	d := f.Dispatcher()
	cancel, _ := start(t, f, d)
	f.Until(t, func() bool { return d.State() == "active" })
	q.lost.Store(true)
	f.Clock.Advance(f.Config.Scheduler.TickInterval.Duration())
	f.Until(t, func() bool { return d.State() == "standby" })
	before := q.claims.Load()
	f.Clock.Advance(2 * time.Second)
	if q.claims.Load() != before {
		t.Fatal("claim after ownership loss")
	}
	q.lost.Store(false)
	f.Until(t, func() bool { return d.State() == "active" })
	_ = q.SetHeartbeat(context.Background(), f.Clock.Now().Add(time.Hour))
	cancel()
}
func TestMassiveFanInCapsClaimsToFreeWorkers(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testMassiveFanInCapsClaimsToFreeWorkers(t)
		})
	}
}
func testMassiveFanInCapsClaimsToFreeWorkers(t *testing.T) {
	f := testutil.New(t)
	entered := make(chan struct{}, 20)
	release := make(chan struct{})
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		entered <- struct{}{}
		<-release
		return sink.ExecResult{Outcome: sink.Success}
	})
	f.Config.Scheduler.SLAThreshold = config.Duration(24 * time.Hour)
	f.Engine.Config = f.Config
	ctx := context.Background()
	now := f.Clock.Now().UnixMilli()
	if _, err := f.DBs.Control.Writer.ExecContext(ctx, `INSERT INTO workspaces(id,name,slug,created_at) VALUES('ws_test','burst','burst',?)`, now); err != nil {
		t.Fatal(err)
	}
	// One setup statement seeds a genuine 10,000-job fan-in without thousands of
	// individual test setup transactions or wall-clock sleeps.
	_, err := f.DBs.Control.Writer.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i<9999)
 INSERT INTO jobs(id,workspace_id,name,schedule_kind,schedule_expr,schedule_canonical,timezone,sink_kind,sink_config,retry_policy,timeout_ms,state,next_run_at,created_at,updated_at)
 SELECT printf('job_burst%05d',i),'ws_test','burst','cron','@every 1h','@every 1h','UTC','webhook','{}','{"max_attempts":1,"backoff":"fixed","initial_interval":"1s","max_interval":"1m","multiplier":2,"jitter":0}',1000,'active',?,?,? FROM n`, now, now, now)
	if err != nil {
		t.Fatal(err)
	}

	d := f.Dispatcher()
	cancel, _ := start(t, f, d)
	f.Until(t, func() bool { return len(entered) == f.Config.Workers.Count })
	stats, err := f.Queue.Stats(context.Background())
	if err != nil || stats.Inflight > int64(f.Config.Workers.Count) || stats.Due < 9996 {
		t.Fatal(stats, err)
	}
	close(release)
	cancel()
}

func TestBackwardClockWarning(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testBackwardClockWarning(t)
		})
	}
}
func testBackwardClockWarning(t *testing.T) {
	f := testutil.New(t)
	var output bytes.Buffer
	f.Engine.Log = logx.Setup(logx.Config{Format: "json", Level: "warn"}, &output, nil)
	d := f.Dispatcher()
	wake := make(chan struct{}, 1)
	d.Wake = wake
	cancel, done := start(t, f, d)
	f.Until(t, func() bool {
		hb, _ := f.Queue.Heartbeat(context.Background())
		return d.State() == "active" && !hb.IsZero()
	})
	f.Clock.Advance(-time.Hour)
	wake <- struct{}{}
	f.Until(t, func() bool { hb, _ := f.Queue.Heartbeat(context.Background()); return !hb.After(f.Clock.Now()) })
	cancel()
	f.Until(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	if !strings.Contains(output.String(), "clock moved backwards") {
		t.Fatal(output.String())
	}
}

type renewQueue struct {
	queue.Queue
	renewed atomic.Int64
}

func (q *renewQueue) Renew(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	ok, err := q.Queue.Renew(ctx, token, ttl)
	if ok {
		q.renewed.Add(1)
	}
	return ok, err
}
func TestGracefulDrainRetainsLeadership(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testGracefulDrainRetainsLeadership(t)
		})
	}
}
func testGracefulDrainRetainsLeadership(t *testing.T) {
	f := testutil.New(t)
	f.Config.Scheduler.LockTTL = config.Duration(300 * time.Millisecond)
	f.Config.Server.ShutdownGrace = config.Duration(time.Second)
	f.Engine.Config = f.Config
	q := &renewQueue{Queue: f.Queue}
	f.Queue = q
	f.Engine.Queue = q
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		close(entered)
		<-release
		return sink.ExecResult{Outcome: sink.Success}
	})
	job := f.Job(t, "drain", f.Clock.Now())
	_ = q.Add(context.Background(), queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}.Format(), f.Clock.Now(), false)
	d := f.Dispatcher()
	cancel, done := start(t, f, d)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f.Until(t, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	cancel()
	for range 4 {
		before := q.renewed.Load()
		f.Until(t, func() bool { return q.renewed.Load() > before })
		ok, err := q.Acquire(context.Background(), "second", time.Second)
		if ok || err != nil {
			t.Fatal("leadership lost during graceful drain", ok, err)
		}
	}
	releaseOnce.Do(func() { close(release) })
	f.Until(t, func() bool {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return true
		default:
			return false
		}
	})
}

type unavailable struct{ queue.Queue }

func (q unavailable) Acquire(context.Context, string, time.Duration) (bool, error) {
	return false, errors.New("queue unavailable")
}
func (q unavailable) Ping(context.Context) error { return errors.New("queue unavailable") }
func TestQueueOutageReadiness(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testQueueOutageReadiness(t)
		})
	}
}
func testQueueOutageReadiness(t *testing.T) {
	f := testutil.New(t)
	f.Queue = unavailable{f.Queue}
	f.Engine.Queue = f.Queue
	d := f.Dispatcher()
	cancel, _ := start(t, f, d)
	f.Until(t, func() bool { return d.State() == "standby" })
	status, ready := d.Ready(context.Background())
	if ready || status["queue"] != false {
		t.Fatal(status, ready)
	}
	cancel()
}

type gatedStore struct {
	store.Store
	blocked atomic.Bool
}

func (s *gatedStore) WithTx(ctx context.Context, db store.Database, apply func(store.Tx) error) error {
	if s.blocked.Load() {
		return errors.New("SQLite unavailable")
	}
	return s.Store.WithTx(ctx, db, apply)
}
func TestSQLiteFailurePausesAndResumesClaims(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testSQLiteFailurePausesAndResumesClaims(t)
		})
	}
}
func testSQLiteFailurePausesAndResumesClaims(t *testing.T) {
	f := testutil.New(t)
	q := &controlledQueue{Queue: f.Queue}
	f.Queue = q
	f.Engine.Queue = q
	s := &gatedStore{Store: f.Store}
	f.Writer.Store = s
	d := f.Dispatcher()
	cancel, _ := start(t, f, d)
	f.Until(t, func() bool { return d.State() == "active" })
	s.blocked.Store(true)
	future, err := f.Writer.Submit(context.Background(), store.Control, func(context.Context, store.Tx) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.Until(t, func() bool { return f.Writer.Backpressure() })
	pressureHeartbeat, _ := q.Heartbeat(context.Background())
	f.Until(t, func() bool { hb, _ := q.Heartbeat(context.Background()); return hb.After(pressureHeartbeat) })
	before := q.claims.Load()
	heartbeat, _ := q.Heartbeat(context.Background())
	job := f.Job(t, "backpressure", f.Clock.Now())
	_ = q.Add(context.Background(), queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}.Format(), f.Clock.Now(), false)
	f.Until(t, func() bool { hb, _ := q.Heartbeat(context.Background()); return hb.After(heartbeat) })
	if q.claims.Load() != before {
		t.Fatal("claimed while writer unavailable")
	}
	s.blocked.Store(false)
	f.Run(t, func(ctx context.Context) error { return future.Wait(ctx) })
	f.Until(t, func() bool {
		rows, err := f.Store.HistoryReader().StatsRuns(context.Background(), historygen.StatsRunsParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: 0})
		return err == nil && len(rows) == 1 && rows[0].Status == "succeeded"
	})
	cancel()
}
func TestMalformedMemberIsAcknowledged(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testMalformedMemberIsAcknowledged(t)
		})
	}
}
func testMalformedMemberIsAcknowledged(t *testing.T) {
	f := testutil.New(t)
	d := f.Dispatcher()
	var output bytes.Buffer
	d.Log = logx.Setup(logx.Config{Format: "json", Level: "error"}, &output, nil)
	cancel, done := start(t, f, d)
	f.Until(t, func() bool { return d.State() == "active" })
	_ = f.Queue.Add(context.Background(), "bad|member", f.Clock.Now(), false)
	f.Until(t, func() bool {
		present, err := f.Queue.Contains(context.Background(), []string{"bad|member"})
		return err == nil && !present[0]
	})
	cancel()
	f.Until(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
	if !strings.Contains(output.String(), "malformed queue member") {
		t.Fatal(output.String())
	}
}

func TestWorkspaceConcurrencyDefersBeforeAdvanceAndReclaim(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			f := testutil.New(t)
			f.Engine.Limits = &limits.Registry{Config: config.ConfigLimits{MaxConcurrentRunsPerWorkspace: 1}, Store: f.Store, Clock: f.Clock}
			first := f.Job(t, "first", f.Clock.Now())
			second := f.Job(t, "second", f.Clock.Now())
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(ctx context.Context, r sink.ExecRequest) sink.ExecResult {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
				return sink.ExecResult{Outcome: sink.Success}
			})
			d := f.Dispatcher()
			cancel, done := start(t, f, d)
			f.Until(t, func() bool { return len(entered) == 1 })
			f.Until(t, func() bool {
				stats, e := f.Queue.Stats(context.Background())
				return e == nil && stats.Inflight == 1 && stats.Due >= 2
			})

			// Force a claim of the already-advanced first occurrence while its delivery holds the cap.
			member := queue.Occurrence{JobID: first.ID, ScheduledAt: time.UnixMilli(first.NextRunAt.Int64), Version: first.Version, Trigger: "s"}.Format()
			_ = f.Queue.Add(context.Background(), member, f.Clock.Now(), false)
			close(release)
			f.Until(t, func() bool {
				var n int
				_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status='succeeded'`).Scan(&n)
				return n == 2
			})
			var deferred, breached int
			if e := f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT concurrency_deferred,sla_breached FROM runs WHERE job_id=?`, second.ID).Scan(&deferred, &breached); e != nil || deferred != 1 || breached != 0 {
				t.Fatal(deferred, breached, e)
			}
			for _, job := range []string{first.ID, second.ID} {
				row, e := f.Store.ControlReader().GetJob(context.Background(), sqlitegen.GetJobParams{ID: job, WorkspaceID: first.WorkspaceID})
				if e != nil || row.OccurrenceCount != 1 {
					t.Fatal(row.OccurrenceCount, e)
				}
			}
			cancel()
			f.Until(t, func() bool {
				select {
				case e := <-done:
					if e != nil {
						t.Fatal(e)
					}
					return true
				default:
					return false
				}
			})
		})
	}
}
func TestSuspendedWorkspaceSkipsAndResumes(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			f := testutil.New(t)
			job := f.Job(t, "suspended", f.Clock.Now())
			if e := store.SetSuspension(context.Background(), f.Store, job.WorkspaceID, "abuse", true, f.Clock.Now()); e != nil {
				t.Fatal(e)
			}
			d := f.Dispatcher()
			cancel, done := start(t, f, d)
			f.Until(t, func() bool {
				rows, e := f.Store.HistoryReader().StatsRuns(context.Background(), historygen.StatsRunsParams{JobID: job.ID, WorkspaceID: job.WorkspaceID})
				return e == nil && len(rows) > 0 && rows[0].Status == "skipped"
			})
			var reason string
			if e := f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT skip_reason FROM runs LIMIT 1`).Scan(&reason); e != nil || reason != "workspace_suspended" {
				t.Fatal(reason, e)
			}
			if e := store.SetSuspension(context.Background(), f.Store, job.WorkspaceID, "", false, f.Clock.Now()); e != nil {
				t.Fatal(e)
			}
			f.Clock.Advance(time.Hour)
			f.Until(t, func() bool {
				var n int
				_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status='succeeded'`).Scan(&n)
				return n == 1
			})
			cancel()
			f.Until(t, func() bool {
				select {
				case e := <-done:
					if e != nil {
						t.Fatal(e)
					}
					return true
				default:
					return false
				}
			})
		})
	}
}

func TestMonthlyQuotaSkipsAdvancesAndResumesAtRollover(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			f := testutil.New(t)
			now := f.Clock.Now().UTC()
			f.Clock.Advance(time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Add(-5 * time.Second).Sub(now))
			job := f.Job(t, "monthly", f.Clock.Now())
			if _, e := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET schedule_expr='@every 1s',schedule_canonical='@every 1s' WHERE id=?`, job.ID); e != nil {
				t.Fatal(e)
			}
			quota := &limits.Registry{Config: config.ConfigLimits{MaxRunsPerMonthPerWorkspace: 1, OverQuota: "block"}, Store: f.Store, Clock: f.Clock}
			f.Engine.Limits = quota
			d := f.Dispatcher()
			cancel, done := start(t, f, d)
			count := func(status string) int {
				var n int
				_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status=?`, status).Scan(&n)
				return n
			}
			f.Until(t, func() bool { return count("succeeded") == 1 })
			f.Clock.Advance(time.Second)
			f.Until(t, func() bool { return count("skipped") >= 1 })
			var reason string
			if e := f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT skip_reason FROM runs WHERE status='skipped' LIMIT 1`).Scan(&reason); e != nil || reason != "quota_exceeded" {
				t.Fatal(reason, e)
			}
			row, e := f.Store.ControlReader().GetJob(context.Background(), sqlitegen.GetJobParams{ID: job.ID, WorkspaceID: job.WorkspaceID})
			if e != nil || row.OccurrenceCount < 2 || row.State != "active" {
				t.Fatal(row, e)
			}
			f.Clock.Advance(5 * time.Second)
			f.Until(t, func() bool { return count("succeeded") == 2 })
			if n, e := quota.Usage(context.Background(), job.WorkspaceID); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			cancel()
			f.Until(t, func() bool {
				select {
				case e := <-done:
					if e != nil {
						t.Fatal(e)
					}
					return true
				default:
					return false
				}
			})
		})
	}
}

type fairnessLimits struct{ cap int }

func (p fairnessLimits) For(_ context.Context, ws string) (limits.Set, error) {
	if ws == "ws_test" {
		return limits.Set{MaxConcurrentRuns: p.cap, MaxRunsPerMonth: 1000000}, nil
	}
	return limits.Set{MaxRunsPerMonth: 1000000}, nil
}
func TestCappedWorkspaceDoesNotBlockOtherWorkspace(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			baseline := fairnessRun(t, mode, 0)
			capped := fairnessRun(t, mode, 1)
			bound := max(250*time.Millisecond, baseline+100*time.Millisecond)
			t.Logf("FAIRNESS mode=%s A_occurrences=200 baseline_B_lag_ms=%d capped_B_lag_ms=%d bound_ms=%d all_A_exactly_once=true", mode, baseline.Milliseconds(), capped.Milliseconds(), bound.Milliseconds())
			if capped > bound {
				t.Fatalf("capped neighbour delayed B: %v versus baseline %v (bound %v)", capped, baseline, bound)
			}
		})
	}
}
func fairnessRun(t *testing.T, mode string, cap int) time.Duration {
	t.Helper()
	t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
	f := testutil.New(t)
	prepared, err := store.NewPrepared(context.Background(), f.DBs)
	if err != nil {
		t.Fatal(err)
	}
	f.Store = prepared
	f.Engine.Store = prepared
	f.Writer.Store = prepared
	f.Config.Workers.Count = 256
	f.Config.Workers.QueueSize = 256
	f.Config.Scheduler.ResultsFlushInterval = config.Duration(50 * time.Millisecond)
	for i := 0; i < 200; i++ {
		f.Job(t, fmt.Sprintf("a%03d", i), f.Clock.Now())
	}
	b := f.Job(t, "z_b", f.Clock.Now())
	if _, err := f.DBs.Control.Writer.ExecContext(context.Background(), `INSERT INTO workspaces(id,name,slug,plan,created_at) VALUES('ws_b','B','b','free',0); UPDATE jobs SET workspace_id='ws_b' WHERE id=?`, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET ends_at=?,timeout_ms=60000`, f.Clock.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	quota := &limits.Registry{Store: f.Store, Clock: f.Clock}
	if err := quota.Register("fairness", fairnessLimits{cap}); err != nil {
		t.Fatal(err)
	}
	f.Engine.Limits = quota
	release := make(chan struct{})
	var once sync.Once
	var bLag atomic.Int64
	bLag.Store(-1)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(ctx context.Context, r sink.ExecRequest) sink.ExecResult {
		if r.Job.ID == b.ID {
			bLag.Store(f.Clock.Since(r.ScheduledAt).Nanoseconds())
			once.Do(func() { close(release) })
		}
		if r.Job.ID == "job_a000" {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return sink.ExecResult{Outcome: sink.Success}
	})
	d := f.Dispatcher()

	cancel, done := start(t, f, d)
	fairnessUntil(t, f, time.Millisecond, func() bool { return bLag.Load() >= 0 })
	lag := time.Duration(bLag.Load())
	t.Logf("fairness B cap=%d lag=%v", cap, lag)
	fairnessUntil(t, f, 25*time.Millisecond, func() bool {
		var n int
		_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status='succeeded'`).Scan(&n)
		return n == 201
	})

	cancel()
	f.Until(t, func() bool {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return true
		default:
			return false
		}
	})
	for query, want := range map[string]int{
		`SELECT count(*) FROM runs WHERE workspace_id='ws_test'`:                                       200,
		`SELECT count(*) FROM runs WHERE workspace_id='ws_test' AND attempts=1 AND status='succeeded'`: 200,
		`SELECT count(*) FROM attempts`:                                                                201,
	} {
		var n int
		if err := f.DBs.History.Reader.QueryRowContext(context.Background(), query).Scan(&n); err != nil || n != want {
			t.Fatal(query, n, want, err)
		}
	}
	var n int
	if err := f.DBs.Control.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM jobs WHERE occurrence_count=1`).Scan(&n); err != nil || n != 201 {
		t.Fatal("occurrence counts", n, err)
	}
	return lag
}

// Pace only the fake clock, allowing SQLite/worker goroutines to settle between
// increments. Fast unrestricted advancement otherwise measures test CPU yielding,
// and can leap hours ahead of an unfinished database transaction under -race.
func fairnessUntil(t *testing.T, f *testutil.Fixture, step time.Duration, predicate func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tick := clockwork.NewRealClock().NewTicker(time.Millisecond)
	defer tick.Stop()
	for !predicate() {
		select {
		case <-ctx.Done():
			var count, success int
			var last string
			_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*),coalesce(sum(status='succeeded'),0),coalesce(max(last_error),'') FROM runs`).Scan(&count, &success, &last)
			t.Fatalf("fairness condition timed out: rows=%d success=%d last_error=%q fake_now=%s", count, success, last, f.Clock.Now())
		case <-tick.Chan():
			f.Clock.Advance(step)
		}
	}
}
