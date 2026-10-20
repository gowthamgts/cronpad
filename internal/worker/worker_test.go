package worker_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/logx"

	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/telemetry"
	"github.com/gowthamgts/cronpad/internal/testutil"
	"github.com/gowthamgts/cronpad/internal/worker"
)

func TestSuccessAndRunDedupe(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testSuccessAndRunDedupe(t)
		})
	}
}
func testSuccessAndRunDedupe(t *testing.T) {
	f := testutil.New(t)
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	metrics, err := telemetry.NewInstruments(provider.Meter("dedupe"))
	if err != nil {
		t.Fatal(err)
	}
	f.Engine.Metrics = metrics
	f.StartWriter(t)
	job := f.Job(t, "success", f.Clock.Now())
	o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
	var calls atomic.Int64
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		calls.Add(1)
		return sink.ExecResult{Outcome: sink.Success, Code: 200}
	})
	for range 2 {
		f.Run(t, func(ctx context.Context) error {
			return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
		})
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	var suppressed int64
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == "cronpad.duplicates_suppressed" {
				for _, point := range metric.Data.(metricdata.Sum[int64]).DataPoints {
					suppressed += point.Value
				}
			}
		}
	}
	if suppressed != 1 {
		t.Fatalf("duplicates_suppressed=%d, want 1", suppressed)
	}
	run, err := f.Store.HistoryReader().FindOccurrence(context.Background(), historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule"})
	if err != nil || run.Status != "succeeded" || run.Attempts != 1 {
		t.Fatal(run, err)
	}
}
func TestPanicRetryAndAutoDisable(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testPanicRetryAndAutoDisable(t)
		})
	}
}
func testPanicRetryAndAutoDisable(t *testing.T) {
	for _, kind := range []string{"panic", "retry", "auto_disable"} {
		t.Run(kind, func(t *testing.T) {
			f := testutil.New(t)
			f.StartWriter(t)
			job := f.Job(t, kind, f.Clock.Now())
			if kind == "retry" {
				_, err := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET retry_policy=? WHERE id=?`, `{"max_attempts":3,"backoff":"fixed","initial_interval":"1s","max_interval":"2s","multiplier":2,"jitter":0}`, job.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			f.Engine.Config.AutoDisable.ConsecutiveFailures = 2
			f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
				if kind == "panic" {
					panic("deliberate panic")
				}
				return sink.ExecResult{Outcome: sink.Retryable, Code: 503, RetryAfter: time.Hour}
			})
			o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
			n := 1
			if kind == "retry" {
				n = 3
			}
			if kind == "auto_disable" {
				n = 2
			}
			for attempt := range n {
				f.Run(t, func(ctx context.Context) error {
					return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
				})
				if kind == "retry" && attempt < 2 {
					at, ok, err := f.Queue.Peek(context.Background())
					if err != nil || !ok || at.Sub(f.Clock.Now()) > 2*time.Second {
						t.Fatal(at, ok, err)
					}
					f.Clock.Advance(max(0, at.Sub(f.Clock.Now())))
					o.Attempt++
				} else if kind == "auto_disable" {
					o.ScheduledAt = o.ScheduledAt.Add(time.Hour)
				}
			}
			updated, err := f.Store.ControlReader().GetJob(context.Background(), sqlitegen.GetJobParams{ID: job.ID, WorkspaceID: job.WorkspaceID})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "auto_disable" && updated.State != "disabled" {
				t.Fatal(updated)
			}
			runs, err := f.Store.HistoryReader().ListRuns(context.Background(), historygen.ListRunsParams{WorkspaceID: job.WorkspaceID, JobID: job.ID, TagFilter: "", TagJobs: "[]", Status: "", TriggerFilter: "", SlaOnly: 0, FromTime: 0, ToTime: 1<<63 - 1, CursorTime: 0, CursorID: "", PageSize: 10})
			if err != nil || len(runs) == 0 || runs[0].Status != "failed" {
				t.Fatal(runs, err)
			}
		})
	}
}
func TestOverlapAndStaleVersion(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testOverlapAndStaleVersion(t)
		})
	}
}
func testOverlapAndStaleVersion(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	job := f.Job(t, "overlap", f.Clock.Now())
	_, err := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET overlap_policy='skip_if_running' WHERE id=?`, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Store.History().UpsertRun(context.Background(), historygen.UpsertRunParams{ID: "run_previous", JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: f.Clock.Now().Add(-time.Hour).UnixMilli(), Trigger: "schedule", Status: "deferred"})
	if err != nil {
		t.Fatal(err)
	}
	o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
	f.Run(t, func(ctx context.Context) error {
		return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
	})
	run, err := f.Store.HistoryReader().FindOccurrence(context.Background(), historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule"})
	if err != nil || run.Status != "skipped" || run.SkipReason.String != "previous_run_in_progress" || run.LastError.Valid {
		t.Fatal(run, err)
	}
	o.ScheduledAt = o.ScheduledAt.Add(time.Hour)
	o.Version = 99
	f.Run(t, func(ctx context.Context) error {
		return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
	})
	_, err = f.Store.HistoryReader().FindOccurrence(context.Background(), historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule"})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("stale occurrence executed", err)
	}
}
func TestManualPathsAndCancellationRecovery(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testManualPathsAndCancellationRecovery(t)
		})
	}
}
func testManualPathsAndCancellationRecovery(t *testing.T) {
	for _, trigger := range []string{"m", "r"} {
		t.Run(trigger, func(t *testing.T) {
			f := testutil.New(t)
			f.StartWriter(t)
			job := f.Job(t, "manual", f.Clock.Now())
			_, err := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET state='paused' WHERE id=?`, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: trigger}
			f.Run(t, func(ctx context.Context) error {
				return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
			})
			run, err := f.Store.HistoryReader().FindOccurrence(context.Background(), historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: worker.TriggerName(trigger)})
			if err != nil || run.Status != "succeeded" {
				t.Fatal(run, err)
			}
		})
	}
}
func TestKillWorkerThenRedeliver(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testKillWorkerThenRedeliver(t)
		})
	}
}
func testKillWorkerThenRedeliver(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	job := f.Job(t, "killed", f.Clock.Now())
	o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
	_ = f.Queue.Add(context.Background(), o.Format(), f.Clock.Now(), false)
	_, _ = f.Queue.Claim(context.Background(), f.Clock.Now(), 1, time.Second)
	entered := make(chan struct{})
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(ctx context.Context, r sink.ExecRequest) sink.ExecResult {
		close(entered)
		<-ctx.Done()
		return sink.ExecResult{Outcome: sink.Timeout}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()}) }()
	f.Until(t, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
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
	f.Clock.Advance(2 * time.Second)
	n, err := f.Queue.Reap(context.Background(), f.Clock.Now(), 10)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult { return sink.ExecResult{Outcome: sink.Success} })
	f.Run(t, func(ctx context.Context) error {
		return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
	})
	run, err := f.Store.HistoryReader().FindOccurrence(context.Background(), historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule"})
	if err != nil || run.Status != "succeeded" || run.Attempts != 1 {
		t.Fatal(run, err)
	}
}

type leaseQueue struct {
	queue.Queue
	code   int
	called chan struct{}
}

func (q *leaseQueue) Extend(ctx context.Context, m string, at time.Time) (int, error) {
	select {
	case q.called <- struct{}{}:
	default:
	}
	return q.code, nil
}
func TestLeaseLostAndRescuedHaveRunContext(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testLeaseLostAndRescuedHaveRunContext(t)
		})
	}
}
func testLeaseLostAndRescuedHaveRunContext(t *testing.T) {
	for _, code := range []int{0, 2} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := testutil.New(t)
			f.StartWriter(t)
			f.Engine.Config.Scheduler.LeaseDuration = config.Duration(300 * time.Millisecond)
			q := &leaseQueue{Queue: f.Queue, code: code, called: make(chan struct{}, 1)}
			f.Engine.Queue = q
			job := f.Job(t, "lease", f.Clock.Now())
			o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(ctx context.Context, r sink.ExecRequest) sink.ExecResult {
				f.Engine.Log.InfoContext(ctx, "deep sink log")
				close(entered)
				<-release
				return sink.ExecResult{Outcome: sink.Success}
			})
			var output bytes.Buffer
			f.Engine.Log = logx.Setup(logx.Config{Format: "json", Level: "info"}, &output, nil)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			var finished atomic.Bool
			ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}}))
			go func() {
				done <- f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
				finished.Store(true)
			}()
			t.Cleanup(func() {
				once.Do(func() { close(release) })
				cancel()
				f.Until(t, func() bool { return finished.Load() })
			})
			f.Until(t, func() bool {
				select {
				case <-entered:
					return true
				default:
					return false
				}
			})
			f.Until(t, func() bool {
				select {
				case <-q.called:
					return true
				default:
					return false
				}
			})
			once.Do(func() { close(release) })
			f.Until(t, func() bool { return finished.Load() })
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			text := output.String()
			decoder := json.NewDecoder(strings.NewReader(text))
			for decoder.More() {
				var record map[string]any
				if err := decoder.Decode(&record); err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{logx.JobID, logx.RunID, logx.Occ, logx.WorkspaceID, logx.Attempt, logx.Sink, logx.TraceID, logx.SpanID} {
					if record[field] == nil || record[field] == "" {
						t.Fatal("missing context", field, record)
					}
				}
			}
			wanted := "lease lost"
			if code == 2 {
				wanted = "lease rescued after reap"
			}
			if !strings.Contains(text, wanted) {
				t.Fatal(text)
			}
		})
	}
}

type failAckOnce struct {
	queue.Queue
	failed atomic.Bool
}

func (q *failAckOnce) Ack(ctx context.Context, m, retry string, at time.Time) error {
	if !q.failed.Swap(true) {
		return errors.New("lost ack connection")
	}
	return q.Queue.Ack(ctx, m, retry, at)
}
func TestCommittedDeferralBeforeAckResumesCorrectAttempt(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testCommittedDeferralBeforeAckResumesCorrectAttempt(t)
		})
	}
}
func testCommittedDeferralBeforeAckResumesCorrectAttempt(t *testing.T) {
	f := testutil.New(t)
	f.StartWriter(t)
	job := f.Job(t, "deferral", f.Clock.Now())
	_, err := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET retry_policy=? WHERE id=?`, `{"max_attempts":3,"backoff":"fixed","initial_interval":"2s","max_interval":"2s","multiplier":2,"jitter":0}`, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := &failAckOnce{Queue: f.Queue}
	f.Engine.Queue = q
	var calls atomic.Int64
	f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		calls.Add(1)
		return sink.ExecResult{Outcome: sink.Retryable, Code: 503}
	})
	o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
	var firstErr error
	f.Run(t, func(ctx context.Context) error {
		firstErr = f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
		return nil
	})
	if firstErr == nil {
		t.Fatal("ack fault not injected")
	}
	run, err := f.Store.HistoryReader().FindOccurrence(context.Background(), historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule"})
	if err != nil || run.Status != "deferred" {
		t.Fatal(run, err)
	}
	retryAt := run.NextAttemptAt.Int64
	// Expire the recent-map entry to exercise the durable SQL recovery path.
	f.Writer.TTL = 0
	f.Writer.Remember(run)
	f.Run(t, func(ctx context.Context) error {
		return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
	})
	if calls.Load() != 1 {
		t.Fatal("re-delivered committed attempt", calls.Load())
	}
	at, ok, err := q.Peek(context.Background())
	if err != nil || !ok || at.UnixMilli() != retryAt {
		t.Fatal("retry backoff lost", at, ok, err)
	}
	run, err = f.Store.HistoryReader().GetRun(context.Background(), historygen.GetRunParams{ID: run.ID, WorkspaceID: job.WorkspaceID})
	if err != nil || run.Status != "deferred" || run.Attempts != 1 {
		t.Fatal(run, err)
	}
}

func TestSLABreachIgnoresRetryBackoff(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			f := testutil.New(t)
			f.StartWriter(t)
			job := f.Job(t, "sla", f.Clock.Now())
			_, err := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET sla_threshold_ms=5000, retry_policy=? WHERE id=?`, `{"max_attempts":2,"backoff":"fixed","initial_interval":"10s","max_interval":"10s","multiplier":1,"jitter":0}`, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int64
			f.Engine.Sinks["webhook"] = testutil.SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
				if calls.Add(1) == 1 {
					return sink.ExecResult{Outcome: sink.Retryable, Code: 503}
				}
				return sink.ExecResult{Outcome: sink.Success, Code: 200}
			})
			find := func(o queue.Occurrence) historygen.Run {
				t.Helper()
				run, err := f.Store.HistoryReader().FindOccurrence(context.Background(), historygen.FindOccurrenceParams{JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: o.ScheduledAt.UnixMilli(), Trigger: "schedule"})
				if err != nil {
					t.Fatal(err)
				}
				return run
			}

			// On time, then a retry 10s later: the retry's wait is not start lag.
			o := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
			f.Run(t, func(ctx context.Context) error {
				return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
			})
			at, ok, err := f.Queue.Peek(context.Background())
			if err != nil || !ok {
				t.Fatal(at, ok, err)
			}
			f.Clock.Advance(max(0, at.Sub(f.Clock.Now())))
			o.Attempt = 1
			f.Run(t, func(ctx context.Context) error {
				return f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: f.Clock.Now()})
			})
			if run := find(o); run.Status != "succeeded" || run.Attempts != 2 || run.SlaBreached != 0 || run.StartLagMs.Int64 > 5000 {
				t.Fatalf("retried run marked late: %+v", run)
			}

			// A first attempt that starts after the threshold is still late.
			late := queue.Occurrence{JobID: job.ID, ScheduledAt: f.Clock.Now(), Version: 1, Trigger: "s"}
			f.Clock.Advance(6 * time.Second)
			f.Run(t, func(ctx context.Context) error {
				return f.Engine.Process(ctx, worker.Work{Occurrence: late, ClaimedAt: f.Clock.Now()})
			})
			if run := find(late); run.SlaBreached != 1 {
				t.Fatalf("late first attempt not flagged: %+v", run)
			}
		})
	}
}
