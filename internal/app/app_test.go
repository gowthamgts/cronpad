package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

type lifecycleExtension struct {
	started chan struct{}
	stopped chan struct{}
	err     error
}

func (*lifecycleExtension) Name() string                 { return "lifecycle" }
func (*lifecycleExtension) ConfigSection() (string, any) { return "", nil }
func (*lifecycleExtension) Migrations() (ext.DB, fs.FS)  { return ext.Control, nil }
func (*lifecycleExtension) Routes(chi.Router, ext.Deps)  {}
func (e *lifecycleExtension) Start(ctx context.Context, _ ext.Deps) error {
	close(e.started)
	if e.err != nil {
		return e.err
	}
	<-ctx.Done()
	close(e.stopped)
	return nil
}
func TestRunOwnsExtensionShutdown(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "failure"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, err := config.Load(ctx, "", nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			c.Database.Path = filepath.Join(dir, "control.db")
			c.Database.HistoryPath = filepath.Join(dir, "history.db")
			c.Server.Addr = "127.0.0.1:0"
			e := &lifecycleExtension{started: make(chan struct{}), stopped: make(chan struct{})}
			sentinel := errors.New("extension failed")
			if fail {
				e.err = sentinel
			}
			a, err := New(ctx, c, "oss", []ext.Extension{e}, clockwork.NewFakeClock(), slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = a.Close() }()
			result := make(chan error, 1)
			go func() { result <- a.Run(ctx, nil) }()
			select {
			case <-e.started:
			case <-ctx.Done():
				t.Fatal("extension did not start")
			}
			guard, stopGuard := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopGuard()
			cancel()
			select {
			case err = <-result:
				if fail && !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
				if !fail && err != nil {
					t.Fatal(err)
				}
			case <-guard.Done():
				t.Fatal("shutdown blocked")
			}
			if !fail {
				select {
				case <-e.stopped:
				default:
					t.Fatal("extension not stopped")
				}
			}
		})
	}
}

func TestReadinessReportsDependenciesAndDispatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := config.Load(ctx, "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c.Database.Path = filepath.Join(dir, "control.db")
	c.Database.HistoryPath = filepath.Join(dir, "history.db")
	c.Server.Addr = "127.0.0.1:0"
	clk := clockwork.NewFakeClock()
	a, err := New(ctx, c, "oss", nil, clk, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, nil) }()
	guard, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	for {
		ready := httptest.NewRecorder()
		a.Handler.ServeHTTP(ready, httptest.NewRequestWithContext(t.Context(), "GET", "/readyz", nil))
		if ready.Code == 200 {
			validateResponse(t, httptest.NewRequestWithContext(t.Context(), "GET", "/readyz", nil), ready)
			var payload map[string]any
			if err = json.Unmarshal(ready.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["sqlite_writable"] != true || payload["queue"] != true || payload["dispatcher"] != "active" {
				t.Fatal(payload)
			}
			break
		}
		select {
		case <-guard.Done():
			t.Fatal("dispatcher never became ready")
		default:
		}
		clk.Advance(time.Millisecond)
		runtime.Gosched()
	}
	if err = a.queue.Close(); err != nil {
		t.Fatal(err)
	}
	notReady := httptest.NewRecorder()
	a.Handler.ServeHTTP(notReady, httptest.NewRequestWithContext(t.Context(), "GET", "/readyz", nil))
	if notReady.Code != 503 {
		t.Fatal(notReady.Code)
	}
	healthy := httptest.NewRecorder()
	a.Handler.ServeHTTP(healthy, httptest.NewRequestWithContext(t.Context(), "GET", "/healthz", nil))
	if healthy.Code != 200 {
		t.Fatal(healthy.Code)
	}
	cancel()
	select {
	case <-done:
		// The closed queue may surface as a Run error on shutdown; it must still stop.
	case <-guard.Done():
		t.Fatal("shutdown blocked")
	}
}
