package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/trace"

	"github.com/gowthamgts/cronpad/internal/logx"
)

func TestRequestLogContract(t *testing.T) {
	for _, tc := range []struct {
		name, method, target, route string
		status                      int
	}{
		{"success", "GET", "/jobs/private-id?token=query-secret", "/jobs/{id}", 201},
		{"implicit success", "GET", "/empty", "/empty", 200},
		{"failure", "GET", "/failure", "/failure", 503},
		{"panic", "GET", "/panic", "/panic", 500},
		{"unmatched", "GET", "/secret-path?token=query-secret", "<unmatched>", 404},
		{"method not allowed", "POST", "/empty", "<unmatched>", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := logx.Setup(logx.Config{Service: "cronpad", Version: "test"}, &output, nil)
			clk := clockwork.NewFakeClock()
			router := chi.NewRouter()
			router.Use(middleware.RequestID, requestLogging(logger, clk), middleware.Recoverer)
			router.Get("/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
				// Nested logs inherit request fields and the original trace context.
				logger.InfoContext(r.Context(), "nested request operation")
				clk.Advance(7 * time.Millisecond)
				w.WriteHeader(201)
			})
			router.Get("/empty", func(http.ResponseWriter, *http.Request) {})
			router.Get("/failure", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) })
			router.Get("/panic", func(http.ResponseWriter, *http.Request) { panic("test panic") })
			sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
			ctx := trace.ContextWithSpanContext(t.Context(), sc)
			req := httptest.NewRequestWithContext(ctx, tc.method, tc.target, nil)
			req.Header.Set("X-Request-ID", "request-contract")
			req.Header.Set("Authorization", "Bearer header-secret")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			decoder := json.NewDecoder(&output)
			var completion map[string]any
			for decoder.More() {
				var record map[string]any
				if err := decoder.Decode(&record); err != nil {
					t.Fatal(err)
				}
				for key, want := range map[string]string{
					logx.Service: "cronpad", logx.Version: "test", logx.RequestID: "request-contract",
					logx.Method: tc.method, logx.TraceID: sc.TraceID().String(), logx.SpanID: sc.SpanID().String(),
				} {
					if record[key] != want {
						t.Fatalf("%s = %v, want %s: %v", key, record[key], want, record)
					}
				}
				if record[logx.Time] == nil || record[logx.Level] != slog.LevelInfo.String() {
					t.Fatalf("missing log envelope: %v", record)
				}
				encoded, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"private-id", "query-secret", "secret-path", "header-secret"} {
					if strings.Contains(string(encoded), secret) {
						t.Fatalf("request log leaked %q: %s", secret, encoded)
					}
				}
				if record[logx.MSg] == "request finished" {
					completion = record
				}
			}
			if completion == nil || completion[logx.Route] != tc.route || completion[logx.StatusCode] != float64(tc.status) {
				t.Fatalf("invalid completion record: %v", completion)
			}
			wantDuration := float64(0)
			if tc.name == "success" {
				wantDuration = 7
			}
			if completion[logx.DurationMS] != wantDuration {
				t.Fatalf("duration = %v, want %v", completion[logx.DurationMS], wantDuration)
			}
		})
	}
}
