// Package testutil supplies owned pipeline fixtures for deterministic tests.
package testutil

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/dispatcher"
	"github.com/gowthamgts/cronpad/internal/events"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/telemetry"
	"github.com/gowthamgts/cronpad/internal/worker"
)

type SinkFunc func(context.Context, sink.ExecRequest) sink.ExecResult

func (SinkFunc) Kind() string                                                    { return "webhook" }
func (SinkFunc) Validate(json.RawMessage, sink.Mode) error                       { return nil }
func (f SinkFunc) Execute(c context.Context, r sink.ExecRequest) sink.ExecResult { return f(c, r) }

type workerEngine = worker.Engine
type Fixture struct {
	Clock   *clockwork.FakeClock
	Config  config.Config
	DBs     *store.DBs
	Store   *store.SQLite
	Queue   queue.Queue
	Writer  *results.Writer
	Engine  *workerEngine
	Metrics *telemetry.Instruments
}

func New(t testing.TB) *Fixture {
	t.Helper()
	ctx := context.Background()
	c, err := config.Load(ctx, "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	clk := clockwork.NewFakeClockAt(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	dbs, err := store.Open(ctx, filepath.Join(dir, "control.db"), filepath.Join(dir, "history.db"), time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbs.Close() })
	s := store.New(dbs)
	m, err := telemetry.NewInstruments(noop.NewMeterProvider().Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	q := queue.NewMemory(clk)
	c.Workers.Count = 4
	c.Workers.QueueSize = 4
	c.Scheduler.TickInterval = config.Duration(100 * time.Millisecond)
	c.Scheduler.LockTTL = config.Duration(time.Minute)
	c.Server.ShutdownGrace = config.Duration(time.Second)
	writer := results.New(s, clk, m, c.Scheduler.LeaseDuration.Duration())
	engine := &worker.Engine{Store: s, Queue: q, Writer: writer, Clock: clk, Config: c, Sinks: map[string]sink.Sink{"webhook": SinkFunc(func(context.Context, sink.ExecRequest) sink.ExecResult {
		return sink.ExecResult{Outcome: sink.Success, Code: 200}
	})}, Metrics: m, Log: slog.New(slog.DiscardHandler), Tracer: tracenoop.NewTracerProvider().Tracer("test"), Events: events.Nop{}, RNG: rand.New(rand.NewSource(42))}
	return &Fixture{clk, c, dbs, s, q, writer, engine, m}
}
func (f *Fixture) Dispatcher() *dispatcher.Dispatcher {
	return &dispatcher.Dispatcher{Store: f.Store, Queue: f.Queue, Writer: f.Writer, Workers: f.Engine, Clock: f.Clock, Config: f.Config, Metrics: f.Metrics, Log: f.Engine.Log}
}
func (f *Fixture) Job(t testing.TB, name string, at time.Time) sqlitegen.Job {
	t.Helper()
	ctx := context.Background()
	err := f.Store.Control().SeedDevWorkspace(ctx, sqlitegen.SeedDevWorkspaceParams{ID: "ws_test", Name: "test", Slug: "test", CreatedAt: f.Clock.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := f.Store.Control().CreateJob(ctx, sqlitegen.CreateJobParams{ID: "job_" + name, WorkspaceID: "ws_test", Name: name, Tags: "[]", ScheduleKind: "cron", ScheduleExpr: "@every 1h", ScheduleCanonical: "@every 1h", Timezone: "UTC", MisfirePolicy: "fire_once", SinkKind: "webhook", SinkConfig: `{"url":"https://example.org"}`, RetryPolicy: `{"max_attempts":1,"backoff":"fixed","initial_interval":"1s","max_interval":"1m","multiplier":2,"jitter":0}`, TimeoutMs: 1000, State: "active", Version: 1, NextRunAt: sql.NullInt64{Int64: at.UnixMilli(), Valid: true}, CreatedAt: f.Clock.Now().UnixMilli(), UpdatedAt: f.Clock.Now().UnixMilli(), OverlapPolicy: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// Until yields CPU while advancing only the injected clock. No wall-clock sleeps.
func (f *Fixture) Until(t testing.TB, predicate func() bool) {
	t.Helper()
	guard, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for !predicate() {
		select {
		case <-guard.Done():
			t.Fatal("fixture condition timed out")
		default:
		}
		f.Clock.Advance(time.Millisecond)
		for range 100 {
			runtime.Gosched()
		}
	}
}
func (f *Fixture) StartWriter(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Writer.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}
func (f *Fixture) Run(t testing.TB, fn func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
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
		t.Fatal(fmt.Errorf("fixture operation: %w", err))
	}
}
