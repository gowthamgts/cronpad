// Command loadgen exercises the real authenticated HTTP API and delivery pipeline.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gowthamgts/cronpad/internal/app"
	"github.com/gowthamgts/cronpad/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	flush := flag.Duration("flush", 0, "result writer linger; 0 = group commit")
	mode := flag.String("delivery-mode", "durable", "durable or fast")
	jobs := flag.Int("jobs", 200, "jobs firing once per second (200 = 12k/min)")
	duration := flag.Duration("duration", 60*time.Second, "sustained measurement duration, whole seconds")
	prefill := flag.Int("prefill", 0, "expired history rows to prune concurrently")
	completeAtEnd := flag.Bool("complete-at-end", false, "end all job schedules at measurement end (includes completion burst)")
	stagger := flag.Bool("stagger", false, "spread job anchors uniformly across each second")
	janitor := flag.Bool("janitor", false, "run retention every second during measurement")
	dirFlag := flag.String("dir", "", "scratch data directory (retained when set)")
	flag.Parse()
	seconds := int(duration.Seconds())
	if *jobs <= 0 || seconds <= 0 {
		return fmt.Errorf("positive jobs and duration required")
	}
	clk := clockwork.NewRealClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := *dirFlag
	if dir == "" {
		var e error
		dir, e = os.MkdirTemp("", "cronpad-loadgen-")
		if e != nil {
			return e
		}
		defer func() { _ = os.RemoveAll(dir) }()
	} else if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	var deliveries atomic.Int64
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		deliveries.Add(1)
		w.WriteHeader(204)
	}))
	defer echo.Close()
	c, e := config.Load(ctx, "", nil, nil, nil)
	if e != nil {
		return e
	}
	c.Scheduler.ResultsFlushInterval = config.Duration(*flush)
	c.Scheduler.DeliveryMode = *mode
	if err := c.Validate(); err != nil {
		return err
	}
	c.Server.Addr = "127.0.0.1:0"
	c.Auth.DevLogin = true
	c.Secrets.EncryptionKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	c.Telemetry.TracesSampleRatio = 0
	c.Database.Path = filepath.Join(dir, "cronpad.db")
	c.Database.HistoryPath = filepath.Join(dir, "history.db")
	c.Limits.APIRateLimitPerMinute = 0
	c.Scheduler.TickInterval = config.Duration(50 * time.Millisecond)
	c.Workers.Count = 256
	c.Workers.QueueSize = 2048
	c.Server.ShutdownGrace = config.Duration(10 * time.Second)
	if *janitor {
		c.Retention.JanitorInterval = config.Duration(time.Second)
	}
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	defer func() { _ = provider.Shutdown(context.WithoutCancel(ctx)) }()
	a, e := app.New(ctx, c, "oss", nil, nil, clk, slog.New(slog.DiscardHandler))
	if e != nil {
		return e
	}
	defer func() { _ = a.Close() }()
	if *prefill > 0 {
		tx, e := a.DBs.History.Writer.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		for n := 0; n < *prefill; n++ {
			if _, e = tx.ExecContext(ctx, `INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status,start_lag_ms) VALUES(?,'expired','expired',?,'schedule','succeeded',10)`, fmt.Sprint(n), clk.Now().Add(-31*24*time.Hour).UnixMilli()+int64(n)); e != nil {
				_ = tx.Rollback()
				return e
			}
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	server := httptest.NewServer(a.Handler)
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	login, e := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/auth/dev/login", bytes.NewBufferString("name=loadgen"))
	if e != nil {
		return e
	}
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e := client.Do(login)
	if e != nil {
		return e
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 204 {
		return fmt.Errorf("login: %s", resp.Status)
	}
	csrf := ""
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "cp_csrf" {
			csrf = cookie.Value
		}
	}
	// Create jobs before starting the dispatcher; the measured window excludes setup.
	start := clk.Now().Truncate(time.Second).Add(time.Duration(5+*jobs/40) * time.Second)
	end := start.Add(time.Duration(seconds-1) * time.Second)
	scheduleEnd := end.Add(30 * time.Second)
	if *completeAtEnd {
		scheduleEnd = end
	}
	for n := 0; n < *jobs; n++ {
		offset := time.Duration(0)
		if *stagger {
			offset = time.Duration(n) * time.Second / time.Duration(*jobs)
		}
		body, _ := json.Marshal(map[string]any{"name": fmt.Sprintf("loadgen %d", n), "schedule": map[string]any{"expr": "@every 1s", "starts_at": start.Add(offset), "ends_at": scheduleEnd.Add(offset)}, "sink": map[string]any{"kind": "webhook", "config": map[string]string{"url": echo.URL}}})
		req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/api/v1/jobs", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		res, e := client.Do(req)
		if e != nil {
			return e
		}
		data, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != 201 {
			return fmt.Errorf("create job %d: %s %s", n, res.Status, data)
		}
	}
	if clk.Now().After(start) {
		return fmt.Errorf("job setup exceeded measurement lead time")
	}
	before := size(dir)
	// Start one second before the first fire. With -janitor, the first pass
	// therefore overlaps scheduling while the prefilled history still exists.
	if delay := start.Add(-time.Second).Sub(clk.Now()); delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-clk.After(delay):
		}
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, nil) }()
	defer func() { cancel(); <-done }()
	expected := *jobs * seconds
	deadline := end.Add(30 * time.Second)
	var count, success int
	for {
		if e = a.DBs.History.Reader.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(status='succeeded'),0) FROM runs WHERE scheduled_at>=?`, start.UnixMilli()).Scan(&count, &success); e != nil {
			return e
		}
		if success >= expected {
			break
		}
		if clk.Now().After(deadline) {
			return fmt.Errorf("timed out: runs=%d succeeded=%d expected=%d", count, success, expected)
		}
		<-clk.After(100 * time.Millisecond)
	}
	elapsed := max(float64(seconds), clk.Since(start).Seconds())
	// Stop after the measured deliveries, before the next occurrence. Schedule
	// completion is a separate optional burst, not part of sustained scheduling.
	cancel()
	inspectCtx := context.WithoutCancel(ctx)
	rows, e := a.DBs.History.Reader.QueryContext(inspectCtx, `SELECT start_lag_ms,claimed_at-scheduled_at,started_at-claimed_at,scheduled_at FROM runs WHERE scheduled_at>=? AND start_lag_ms IS NOT NULL`, start.UnixMilli())
	if e != nil {
		return e
	}
	lags, claims, waits, first, last := []int64{}, []int64{}, []int64{}, []int64{}, []int64{}
	for rows.Next() {
		var lag, claim, wait, scheduled int64
		if e = rows.Scan(&lag, &claim, &wait, &scheduled); e != nil {
			_ = rows.Close()
			return e
		}
		lags = append(lags, lag)
		claims = append(claims, claim)
		waits = append(waits, wait)
		if scheduled < start.Add(10*time.Second).UnixMilli() {
			first = append(first, lag)
		}
		if scheduled >= start.Add(time.Duration(max(0, seconds-10))*time.Second).UnixMilli() {
			last = append(last, lag)
		}
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return e
	}
	if *janitor && *prefill > 0 {
		var remaining, rolled int64
		if e = a.DBs.History.Reader.QueryRowContext(inspectCtx, `SELECT count(*) FROM runs WHERE workspace_id='expired'`).Scan(&remaining); e != nil {
			return e
		}
		if e = a.DBs.History.Reader.QueryRowContext(inspectCtx, `SELECT coalesce(sum(runs),0) FROM job_stats_daily WHERE workspace_id='expired'`).Scan(&rolled); e != nil {
			return e
		}
		fmt.Printf("JANITOR expired_raw_remaining=%d rolled_up=%d prefilled=%d\n", remaining, rolled, *prefill)
		if remaining != 0 || rolled != int64(*prefill) {
			return fmt.Errorf("prefilled history was not fully rolled and pruned")
		}
	}
	sort.Slice(lags, func(a, b int) bool { return lags[a] < lags[b] })
	pct := func(p float64) int64 { return lags[int(math.Ceil(float64(len(lags))*p))-1] }
	fmt.Printf("GOMAXPROCS=%d jobs=%d seconds=%d runs=%d succeeded=%d duplicates=%d throughput=%.2f runs/s (%.0f/min) lag_ms p50=%d p95=%d p99=%d sqlite_growth=%d bytes prefill=%d janitor=%t stagger=%t complete_at_end=%t\n", runtime.GOMAXPROCS(0), *jobs, seconds, count, success, count-expected, float64(success)/elapsed, float64(success)/elapsed*60, pct(.5), pct(.95), pct(.99), size(dir)-before, *prefill, *janitor, *stagger, *completeAtEnd)
	fmt.Printf("BREAKDOWN ms scheduled_to_claimed %s claimed_to_started %s first10s_lag %s last10s_lag %s\n", percentiles(claims), percentiles(waits), percentiles(first), percentiles(last))
	var data metricdata.ResourceMetrics
	if e = reader.Collect(inspectCtx, &data); e != nil {
		return fmt.Errorf("collect queue wait: %w", e)
	}
	fmt.Printf("DELIVERY mode=%s flush=%s delivered=%d\n", *mode, *flush, deliveries.Load())
	if deliveries.Load() != int64(expected) {
		return fmt.Errorf("unexpected delivery count: %d", deliveries.Load())
	}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == "cronpad.db.errors" || m.Name == "cronpad.dispatcher.backpressure" {
				if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
					var count int64
					for _, point := range sum.DataPoints {
						count += point.Value
					}
					fmt.Printf("PRESSURE metric=%s count=%d\n", m.Name, count)
					if count > 0 {
						return fmt.Errorf("load pressure %s=%d", m.Name, count)
					}
				}
			}
			if m.Name == "cronpad.run.queue_wait" {
				if h, ok := m.Data.(metricdata.Histogram[float64]); ok {
					for _, p := range h.DataPoints {
						minimum, _ := p.Min.Value()
						maximum, _ := p.Max.Value()
						fmt.Printf("QUEUE_WAIT histogram count=%d mean_ms=%.2f min_ms=%.2f max_ms=%.2f bounds_ms=%v buckets=%v\n", p.Count, p.Sum/float64(p.Count), minimum, maximum, p.Bounds, p.BucketCounts)
					}
				}
			}
		}
	}
	fmt.Println("PRESSURE db_errors=0 dispatcher_backpressure=0")
	if count != expected {
		return fmt.Errorf("unexpected run count")
	}
	if pct(.99) >= 1000 {
		return fmt.Errorf("p99 lag >= 1s")
	}
	return nil
}
func size(dir string) int64 {
	var total int64
	for _, name := range []string{"cronpad.db", "cronpad.db-wal", "history.db", "history.db-wal"} {
		if info, e := os.Stat(filepath.Join(dir, name)); e == nil {
			total += info.Size()
		}
	}
	return total
}

func percentiles(values []int64) string {
	if len(values) == 0 {
		return "empty"
	}
	sort.Slice(values, func(a, b int) bool { return values[a] < values[b] })
	pct := func(p float64) int64 { return values[int(math.Ceil(float64(len(values))*p))-1] }
	return fmt.Sprintf("p50=%d p95=%d p99=%d", pct(.5), pct(.95), pct(.99))
}
