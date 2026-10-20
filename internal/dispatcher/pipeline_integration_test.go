//go:build integration

package dispatcher_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gowthamgts/cronpad/internal/alert"
	"github.com/gowthamgts/cronpad/internal/api"
	"github.com/gowthamgts/cronpad/internal/auth"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/dispatcher"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/netguard"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/results"
	"github.com/gowthamgts/cronpad/internal/secrets"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/sink/webhook"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/internal/telemetry"
	"github.com/gowthamgts/cronpad/internal/testutil"
	"github.com/gowthamgts/cronpad/internal/worker"
)

func realRuntime(t *testing.T, q queue.Queue, url string) (*testutil.Fixture, *dispatcher.Dispatcher, clockwork.Clock) {
	f := testutil.New(t)
	prepared, err := store.NewPrepared(context.Background(), f.DBs)
	if err != nil {
		t.Fatal(err)
	}
	f.Store = prepared
	clk := clockwork.NewRealClock()
	f.Queue = q
	f.Config.Workers.Count = 1024
	f.Config.Workers.QueueSize = 2048
	f.Config.Scheduler.ClaimBatch = 1000
	f.Config.Scheduler.TickInterval = config.Duration(50 * time.Millisecond)
	f.Config.Scheduler.ReconcileInterval = config.Duration(250 * time.Millisecond)
	f.Config.Scheduler.ReaperInterval = config.Duration(100 * time.Millisecond)
	f.Config.Scheduler.OverdueGrace = config.Duration(100 * time.Millisecond)
	f.Config.Scheduler.LockTTL = config.Duration(3 * time.Second)
	f.Config.Scheduler.LeaseDuration = config.Duration(5 * time.Second)
	f.Config.Server.ShutdownGrace = config.Duration(10 * time.Second)
	f.Writer = results.New(f.Store, clk, f.Metrics, 5*time.Second)
	f.Engine.Store = f.Store
	f.Engine.Queue = q
	f.Engine.Clock = clk
	f.Engine.Config = f.Config
	f.Engine.Writer = f.Writer
	f.Engine.Limits = &limits.Registry{Config: f.Config.Limits, Store: f.Store, Clock: clk}
	if err := f.Engine.Limits.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Engine.Sinks = map[string]sink.Sink{"webhook": webhook.New(netguard.Policy{AllowPrivateNetworks: true}, clk, "integration", 1024, time.Second, webhook.WithConcurrency(f.Config.Workers.Count))}
	d := f.Dispatcher()
	d.Clock = clk
	return f, d, clk
}
func await(t *testing.T, clk clockwork.Clock, timeout time.Duration, condition func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := clk.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-ctx.Done():
			t.Fatal("pipeline condition timed out")
		case <-ticker.Chan():
		}
	}
}
func runReal(t *testing.T, d *dispatcher.Dispatcher) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-contextAfter(15 * time.Second):
			t.Error("pipeline shutdown timed out")
		}
	})
	return cancel, done
}
func contextAfter(d time.Duration) <-chan time.Time { return clockwork.NewRealClock().After(d) }
func seedJobs(t *testing.T, f *testutil.Fixture, n int, start, end time.Time, url string) {
	t.Helper()
	ctx := context.Background()
	signing := `[{"value":"whsec_dGVzdGluZy1rZXktMjQtYnl0ZXMtbG9uZw=="}]`
	cfg, _ := json.Marshal(map[string]string{"url": url})
	err := f.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		if err := tx.Control.SeedDevWorkspace(ctx, sqlitegen.SeedDevWorkspaceParams{ID: "ws_test", Name: "integration", Slug: "test", CreatedAt: start.UnixMilli()}); err != nil {
			return err
		}
		for i := range n {
			_, err := tx.Control.CreateJob(ctx, sqlitegen.CreateJobParams{ID: fmt.Sprintf("job_%06d", i), WorkspaceID: "ws_test", Name: fmt.Sprintf("job %d", i), Tags: "[]", ScheduleKind: "cron", ScheduleExpr: "@every 1s", ScheduleCanonical: "@every 1s", Timezone: "UTC", EndsAt: sql.NullInt64{Int64: end.UnixMilli(), Valid: true}, MisfirePolicy: "fire_once", SinkKind: "webhook", SinkConfig: string(cfg), SigningSecrets: sql.NullString{String: signing, Valid: true}, RetryPolicy: `{"max_attempts":1,"backoff":"fixed","initial_interval":"1s","max_interval":"1m","multiplier":2,"jitter":0}`, TimeoutMs: 1000, State: "active", Version: 1, NextRunAt: sql.NullInt64{Int64: start.UnixMilli(), Valid: true}, CreatedAt: start.Add(-time.Second).UnixMilli(), UpdatedAt: start.Add(-time.Second).UnixMilli(), ScheduleAnchorAt: sql.NullInt64{Int64: start.Add(-time.Second).UnixMilli(), Valid: true}, OverlapPolicy: "allow"})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The SLO is measured on production code. A separate scaled workload retains race coverage.
func TestPipeline1000Jobs30Seconds(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			for _, profile := range []string{"unlimited", "block", "allow"} {
				t.Run(profile, func(t *testing.T) { pipelineLoad(t, 1000, 30, true, profile) })
			}
		})
	}
}
func TestPipelineRaceLoad(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			pipelineLoad(t, 100, 10, false)
		})
	}
}

type loadLimits struct{ set limits.Set }

func (p loadLimits) For(context.Context, string) (limits.Set, error) { return p.set, nil }

type countedHistory struct {
	historygen.Querier
	finds atomic.Int64
}

func (h *countedHistory) FindOccurrence(ctx context.Context, p historygen.FindOccurrenceParams) (historygen.Run, error) {
	h.finds.Add(1)
	return h.Querier.FindOccurrence(ctx, p)
}

type countedHistoryStore struct {
	store.Store
	history *countedHistory
}

func (s countedHistoryStore) HistoryReader() historygen.Querier { return s.history }
func pipelineLoad(t *testing.T, jobs, seconds int, enforceLag bool, profiles ...string) {
	expected := jobs * seconds
	q := realQueue(t)
	var delivered atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		delivered.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	f, d, clk := realRuntime(t, q, srv.URL)
	profile := "unlimited"
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	set := limits.Set{OverQuota: limits.Block}
	if profile != "unlimited" {
		set.MaxRunsPerMonth = 1000000
	}
	if profile == "allow" {
		set.OverQuota = limits.Allow
	}
	if err := f.Engine.Limits.Register("pipeline", loadLimits{set}); err != nil {
		t.Fatal(err)
	}
	historyReads := &countedHistory{Querier: f.Store.HistoryReader()}
	d.Store = countedHistoryStore{Store: f.Store, history: historyReads}
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	metrics, err := telemetry.NewInstruments(provider.Meter("pipeline"))
	if err != nil {
		t.Fatal(err)
	}
	f.Metrics, f.Engine.Metrics, f.Writer.Metrics, d.Metrics = metrics, metrics, metrics, metrics
	start := clk.Now().Truncate(time.Second).Add(5 * time.Second)

	end := start.Add(time.Duration(seconds-1) * time.Second)
	seedJobs(t, f, jobs, start, end, srv.URL)
	t.Cleanup(func() {
		var rows, success int
		var lag sql.NullInt64
		_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*),sum(status='succeeded'),max(start_lag_ms) FROM runs`).Scan(&rows, &success, &lag)
		t.Logf("final rows=%d succeeded=%d delivered=%d maxLag=%d state=%s", rows, success, delivered.Load(), lag.Int64, d.State())
		if success != expected {
			rows, err := f.DBs.History.Reader.QueryContext(context.Background(), `SELECT status,coalesce(last_error,''),count(*) FROM runs GROUP BY status,last_error LIMIT 10`)
			if err == nil {
				defer func() { _ = rows.Close() }()
				for rows.Next() {
					var status, msg string
					var count int
					_ = rows.Scan(&status, &msg, &count)
					t.Logf("status=%s count=%d error=%s", status, count, msg)
				}
				if err := rows.Err(); err != nil {
					t.Logf("read run diagnostics: %v", err)
				}
			}
		}
	})
	cancel, _ := runReal(t, d)
	await(t, clk, 5*time.Second, func() bool { return d.State() == "active" })
	nextPoll := clk.Now()
	await(t, clk, 45*time.Second, func() bool {
		if clk.Now().Before(nextPoll) {
			return false
		}
		nextPoll = clk.Now().Add(500 * time.Millisecond)
		var n int
		err := f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status='succeeded'`).Scan(&n)
		if err != nil {
			t.Log(err)
		}
		return err == nil && n == expected
	})
	cancel()
	await(t, clk, 15*time.Second, func() bool { return d.State() == "stopped" })
	var total, unique int
	err = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*),count(DISTINCT job_id||'|'||scheduled_at||'|'||trigger) FROM runs`).Scan(&total, &unique)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.DBs.History.Reader.QueryContext(context.Background(), `SELECT start_lag_ms FROM runs ORDER BY start_lag_ms`)
	if err != nil {
		t.Fatal(err)
	}
	lags := []int64{}
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		lags = append(lags, n)
	}
	_ = rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(lags)
	p50 := lags[(len(lags)*50-1)/100]
	p99 := lags[(len(lags)*99-1)/100]
	if used, err := f.Engine.Limits.Usage(context.Background(), "ws_test"); err != nil || used != int64(expected) {
		t.Errorf("in-memory usage=%d expected=%d error=%v", used, expected, err)
	}
	missed := expected - total
	duplicates := total - unique
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	pressure := map[string]int64{"cronpad.db.errors": 0, "cronpad.dispatcher.backpressure": 0}
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if _, ok := pressure[metric.Name]; ok {
				for _, point := range metric.Data.(metricdata.Sum[int64]).DataPoints {
					pressure[metric.Name] += point.Value
				}
			}
		}
	}
	for name, count := range pressure {
		t.Logf("PRESSURE %s=%d", name, count)
		if count != 0 {
			t.Errorf("unexpected pipeline pressure %s=%d", name, count)
		}
	}
	t.Logf("PIPELINE profile=%s jobs=%d seconds=%d expected=%d recorded=%d delivered=%d missed=%d duplicates=%d p50_start_lag_ms=%d p99_start_lag_ms=%d dispatcher_identity_reads=%d enforce_lag=%v", profile, jobs, seconds, expected, total, delivered.Load(), missed, duplicates, p50, p99, historyReads.finds.Load(), enforceLag)
	if profile == "block" && historyReads.finds.Load() > int64(jobs*2) {
		t.Errorf("per-occurrence SQLite identity reads returned: %d", historyReads.finds.Load())
	}
	if missed != 0 || duplicates != 0 || enforceLag && p99 >= 1000 || delivered.Load() != int64(expected) {
		t.Fail()
	}
}
func TestPipelineQueueLossAndLostMember(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testPipelineQueueLossAndLostMember(t)
		})
	}
}
func testPipelineQueueLossAndLostMember(t *testing.T) {
	q := realQueue(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	t.Cleanup(srv.Close)
	f, d, clk := realRuntime(t, q, srv.URL)
	start := clk.Now().Truncate(time.Second).Add(3 * time.Second)
	seedJobs(t, f, 5, start, start.Add(4*time.Second), srv.URL)
	_, _ = runReal(t, d)
	await(t, clk, 5*time.Second, func() bool { return d.State() == "active" })
	before, err := f.Queue.Epoch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	flushAt := clk.Now()
	q.lose(t)
	await(t, clk, f.Config.Scheduler.ReconcileInterval.Duration(), func() bool {
		after, _ := f.Queue.Epoch(context.Background())
		return after != "" && after != before && d.State() == "active"
	})
	t.Logf("queue loss epoch/leadership recovery_ms=%d reconcile_interval_ms=%d", clk.Since(flushAt).Milliseconds(), f.Config.Scheduler.ReconcileInterval.Duration().Milliseconds())
	member := queue.Occurrence{JobID: "job_000000", ScheduledAt: start, Version: 1, Trigger: "s"}.Format()
	_ = f.Queue.Remove(context.Background(), member)
	await(t, clk, 10*time.Second, func() bool {
		var n int
		_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status='succeeded'`).Scan(&n)
		return n == 25
	})
	var maxLag int64
	_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT max(start_lag_ms) FROM runs`).Scan(&maxLag)
	if maxLag > 1000 {
		t.Fatal(maxLag)
	}
}
func TestPipelineDowntimePolicies(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testPipelineDowntimePolicies(t)
		})
	}
}
func testPipelineDowntimePolicies(t *testing.T) {
	for _, tc := range []struct {
		policy     string
		fire, skip int
	}{{"fire_once", 1, 9}, {"skip", 0, 10}, {"fire_all", 3, 7}} {
		t.Run(tc.policy, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
			t.Cleanup(srv.Close)
			f, d, clk := realRuntime(t, realQueue(t), srv.URL)
			start := clk.Now().Add(-9 * time.Minute)
			seedJobs(t, f, 1, start, clk.Now().Add(time.Hour), srv.URL)
			_, err := f.DBs.Control.Writer.ExecContext(context.Background(), `UPDATE jobs SET schedule_expr='@every 1m',schedule_anchor_at=?,misfire_policy=?`, start.Add(-time.Minute).UnixMilli(), tc.policy)
			if err != nil {
				t.Fatal(err)
			}
			d.Config.Scheduler.MisfireFireAllCap = 3
			cancel, _ := runReal(t, d)
			await(t, clk, 10*time.Second, func() bool {
				var terminal int
				_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status IN ('succeeded','skipped')`).Scan(&terminal)
				return terminal == 10
			})
			cancel()
			await(t, clk, 15*time.Second, func() bool { return d.State() == "stopped" })
			var fired, skipped int
			_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status='succeeded'`).Scan(&fired)
			_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE status='skipped'`).Scan(&skipped)
			if fired != tc.fire || skipped != tc.skip {
				t.Fatal(fired, skipped)
			}
			t.Logf("downtime=10m policy=%s delivered=%d skipped=%d", tc.policy, fired, skipped)
		})
	}
}
func TestPipelineKilledWorkerLeaseRecovery(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testPipelineKilledWorkerLeaseRecovery(t)
		})
	}
}
func testPipelineKilledWorkerLeaseRecovery(t *testing.T) {
	var calls atomic.Int64
	keys := make(chan string, 4)
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		keys <- r.Header.Get("Idempotency-Key")
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-r.Context().Done():
			case <-contextAfter(2 * time.Second):
			}
			return
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(srv.Close)
	f, _, clk := realRuntime(t, realQueue(t), srv.URL)
	f.Engine.Config.Scheduler.LeaseDuration = config.Duration(150 * time.Millisecond)
	f.Writer.TTL = 300 * time.Millisecond
	f.StartWriter(t)
	now := clk.Now()
	seedJobs(t, f, 1, now, now.Add(time.Hour), srv.URL)
	o := queue.Occurrence{JobID: "job_000000", ScheduledAt: time.UnixMilli(now.UnixMilli()), Version: 1, Trigger: "s"}
	_ = f.Queue.Add(context.Background(), o.Format(), now, false)
	_, _ = f.Queue.Claim(context.Background(), now, 1, 150*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Engine.Process(ctx, worker.Work{Occurrence: o, ClaimedAt: now}) }()
	select {
	case <-entered:
	case <-contextAfter(5 * time.Second):
		t.Fatal("sink did not start")
	}
	cancel()
	select {
	case <-done:
	case <-contextAfter(5 * time.Second):
		t.Fatal("worker did not stop")
	}
	await(t, clk, 5*time.Second, func() bool { n, err := f.Queue.Reap(context.Background(), clk.Now(), 10); return err == nil && n == 1 })
	items, err := f.Queue.Claim(context.Background(), clk.Now(), 1, time.Second)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if err = f.Engine.Process(context.Background(), worker.Work{Occurrence: o, ClaimedAt: clk.Now()}); err != nil {
		t.Fatal(err)
	}
	var total, success int
	_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*),sum(status='succeeded') FROM runs`).Scan(&total, &success)
	if total != 1 || success != 1 {
		t.Fatal(total, success)
	}
	if calls.Load() > 2 {
		t.Fatalf("too many deliveries: %d", calls.Load())
	}
	firstKey := <-keys
	if firstKey == "" {
		t.Fatal("missing idempotency key")
	}
	for len(keys) > 0 {
		if key := <-keys; key != firstKey {
			t.Fatalf("key changed %s -> %s", firstKey, key)
		}
	}
	t.Logf("killed worker recovery: rows=%d succeeded=%d HTTP attempts=%d", total, success, calls.Load())
}
func sendAPI(t *testing.T, h http.Handler, method, path, body string, status int) map[string]any {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	login := httptest.NewRequestWithContext(t.Context(), "POST", "/auth/dev/login", strings.NewReader("name=integration"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	session := httptest.NewRecorder()
	h.ServeHTTP(session, login)
	if session.Code != 204 {
		t.Fatalf("integration login: %d %s", session.Code, session.Body.String())
	}
	for _, cookie := range session.Result().Cookies() {
		req.AddCookie(cookie)
		if cookie.Name == "cp_csrf" {
			req.Header.Set("X-CSRF-Token", cookie.Value)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("%s %s: got %d: %s", method, path, w.Code, w.Body.String())
	}
	var out map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func TestPipelineEditAutoDisableAndReenable(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testPipelineEditAutoDisableAndReenable(t)
		})
	}
}
func testPipelineEditAutoDisableAndReenable(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(srv.Close)
	f, d, clk := realRuntime(t, realQueue(t), srv.URL)
	d.Config.AutoDisable.ConsecutiveFailures = 2
	f.Engine.Config.AutoDisable.ConsecutiveFailures = 2
	cfg := f.Config
	cfg.Auth.DevLogin = true
	cache := worker.NewJobCache(f.Store, 10000)
	f.Engine.Cache = cache
	scheduler := &api.QueueScheduler{Store: f.Store, Queue: f.Queue, Clock: clk, Invalidate: cache.Invalidate, WakeCh: make(chan struct{}, 1), Log: f.Engine.Log}
	d.Wake = scheduler.WakeCh
	authentication := auth.New(context.Background(), f.Store, clk, cfg, make([]byte, 32))
	vault, err := secrets.New(base64.StdEncoding.EncodeToString(make([]byte, 32)), f.Store)
	if err != nil {
		t.Fatal(err)
	}
	f.Engine.Vault = vault
	jobs := &api.Jobs{Vault: vault, Auth: authentication, Store: f.Store, Queue: f.Queue, Scheduler: scheduler, Clock: clk, Config: cfg, Webhook: f.Engine.Sinks["webhook"].(*webhook.Webhook)}
	router := chi.NewRouter()
	authentication.Mount(router)
	jobs.Mount(router)
	handler := authentication.Sessions.LoadAndSave(router)
	body := fmt.Sprintf(`{"name":"disable integration","schedule":{"expr":"@every 1s"},"sink":{"kind":"webhook","config":{"url":%q}},"retry":{"max_attempts":1}}`, srv.URL)
	created := sendAPI(t, handler, "POST", "/api/v1/jobs", body, 201)
	jobID := created["id"].(string)
	path := "/api/v1/jobs/" + jobID
	cancel, _ := runReal(t, d)
	await(t, clk, 10*time.Second, func() bool {
		var state string
		_ = f.DBs.Control.Reader.QueryRowContext(context.Background(), `SELECT state FROM jobs WHERE id=?`, jobID).Scan(&state)
		return state == "disabled"
	})
	disabled := sendAPI(t, handler, "GET", path, "", 200)
	if disabled["consecutive_failures"] != float64(2) {
		t.Fatal(disabled)
	}
	history := sendAPI(t, handler, "GET", path+"/runs", "", 200)
	failedID := history["data"].([]any)[0].(map[string]any)["id"].(string)
	fail.Store(false)
	enabled := sendAPI(t, handler, "POST", path+"/enable", "", 200)
	if enabled["consecutive_failures"] != float64(0) {
		t.Fatal(enabled)
	}
	next, err := time.Parse(time.RFC3339Nano, enabled["next_run_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	old := queue.Occurrence{JobID: jobID, ScheduledAt: next, Version: 1, Trigger: "s"}
	_ = f.Queue.Add(context.Background(), old.Format(), next, false)
	await(t, clk, 10*time.Second, func() bool {
		var success int
		_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE job_id=? AND status='succeeded'`, jobID).Scan(&success)
		return success >= 1
	})
	retried := sendAPI(t, handler, "POST", "/api/v1/runs/"+failedID+"/retry", "", 202)
	await(t, clk, 10*time.Second, func() bool {
		var status string
		_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT status FROM runs WHERE id=?`, retried["id"]).Scan(&status)
		return status == "succeeded"
	})
	cancel()
	await(t, clk, 15*time.Second, func() bool { return d.State() == "stopped" })
	var failures, obsolete int
	_ = f.DBs.Control.Reader.QueryRowContext(context.Background(), `SELECT consecutive_failures FROM jobs WHERE id=?`, jobID).Scan(&failures)
	_ = f.DBs.History.Reader.QueryRowContext(context.Background(), `SELECT count(*) FROM runs WHERE job_id=? AND job_version=1 AND scheduled_at=?`, jobID, next.UnixMilli()).Scan(&obsolete)
	if failures != 0 || obsolete != 0 {
		t.Fatal(failures, obsolete)
	}
}

func TestAlertDeliveredWithinFiveSeconds(t *testing.T) {
	for _, mode := range []string{"durable", "fast"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CRONPAD_SCHEDULER__DELIVERY_MODE", mode)
			testAlertDeliveredWithinFiveSeconds(t)
		})
	}
}
func testAlertDeliveredWithinFiveSeconds(t *testing.T) {
	clk := clockwork.NewRealClock()
	delivered := make(chan time.Duration, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alert" {
			w.WriteHeader(500)
			return
		}
		var payload alert.Payload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if payload.Event != "run.failed" {
			t.Errorf("event: %s", payload.Event)
		}
		delivered <- clk.Since(payload.OccurredAt)
		w.WriteHeader(200)
	}))
	defer server.Close()
	f, d, clk := realRuntime(t, realQueue(t), server.URL+"/job")
	start := clk.Now().Add(time.Second)
	seedJobs(t, f, 1, start, start.Add(500*time.Millisecond), server.URL+"/job")
	vault, err := secrets.New(base64.StdEncoding.EncodeToString(make([]byte, 32)), f.Store)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := vault.Wrap("whsec_dGVzdGluZy1rZXktMjQtYnl0ZXMtbG9uZw==", "alert:ch_latency")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{"url": server.URL + "/alert", "format": "generic"})
	if err = f.Store.Control().SaveAlertChannel(context.Background(), sqlitegen.SaveAlertChannelParams{ID: "ch_latency", WorkspaceID: "ws_test", Kind: "webhook", Name: "latency", Config: string(cfg), SigningSecret: secret, CreatedAt: clk.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err = f.Store.Control().SaveAlertRule(context.Background(), sqlitegen.SaveAlertRuleParams{ID: "ar_latency", WorkspaceID: "ws_test", Name: "latency", JobSelector: `{"all":true}`, Events: `["run.failed"]`, Conditions: `{}`, ChannelIds: `["ch_latency"]`, Enabled: 1, CreatedAt: clk.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	evaluator := &alert.Evaluator{Store: f.Store, Writer: f.Writer, Clock: clk, Config: f.Config, PublicURL: "https://cronpad.test"}
	if err = evaluator.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Engine.Events, f.Engine.Alerts = evaluator, evaluator
	runReal(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	notifier := &alert.Notifier{Store: f.Store, Writer: f.Writer, Clock: clk, Vault: vault, Policy: netguard.Policy{AllowPrivateNetworks: true}, Metrics: f.Metrics}
	go func() { done <- notifier.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	select {
	case latency := <-delivered:
		if latency >= 5*time.Second {
			t.Fatalf("alert latency %s exceeds 5s", latency)
		}
		t.Logf("measured failure-to-alert latency: %s", latency)
	case <-clk.After(8 * time.Second):
		t.Fatal("alert not delivered within 5s of scheduled failure")
	}
}
