package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonboulle/clockwork"
)

func TestSchedulesHandlers(t *testing.T) {
	clk := clockwork.NewFakeClockAt(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	r := chi.NewRouter()
	Schedules(r, clk, time.Second, 600)
	cases := []struct {
		name, path, body string
		status           int
		contains         string
	}{
		{"validate", "validate", `{"expr":"0 0 9 ? * 3#2","timezone":"Europe/London"}`, 200, `"valid":true`},
		{"invalid validate", "validate", `{"expr":"every tusday at 9am"}`, 200, `"position":6`},
		{"preview", "preview", `{"expr":"2nd tuesday of every month at 9am","timezone":"Asia/Kolkata","count":3}`, 200, `2026-11-10T03:30:00Z`},
		{"relative", "preview", `{"expr":"in 20 minutes","after":"2026-01-01T00:00:00Z"}`, 200, `2026-01-01T00:20:00Z`},
		{"invalid preview", "preview", `{"expr":"every tusday"}`, 400, `"code":"validation_failed"`},
		{"bad timezone", "validate", `{"expr":"daily","timezone":"bad/zone"}`, 200, `"valid":false`},
		{"bad JSON", "validate", `{`, 400, `"code":"validation_failed"`},
		{"too many", "preview", `{"expr":"daily","count":101}`, 400, `count must`},
		{"zero count", "preview", `{"expr":"daily","count":0}`, 400, `count must`},
		{"bad after", "preview", `{"expr":"daily","after":"bad"}`, 400, `after must`},
		{"multiple JSON", "preview", `{"expr":"daily"} {}`, 400, `one JSON object`},
		{"unknown field", "preview", `{"expr":"daily","typo":true}`, 400, `invalid JSON`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/schedules/"+tc.path, strings.NewReader(tc.body))
			req.RemoteAddr = "192.0.2.1:1234"
			r.ServeHTTP(w, req)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Content-Type") != "application/json" {
				t.Fatal(w.Header())
			}
		})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/schedules/preview", strings.NewReader(`{"expr":"daily at 9am"}`)))
	var result Preview
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Next) != 10 || result.Timezone != "UTC" {
		t.Fatal(result)
	}
}
func TestRateLimitAndMinimumGap(t *testing.T) {
	clk := clockwork.NewFakeClockAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	r := chi.NewRouter()
	Schedules(r, clk, time.Minute, 1)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/schedules/validate", strings.NewReader(`{"expr":"every 30 seconds"}`))
		req.RemoteAddr = "192.0.2.10:1111"
		r.ServeHTTP(w, req)
		if i == 0 && (w.Code != 200 || !strings.Contains(w.Body.String(), `"valid":false`)) {
			t.Fatal(w.Body.String())
		}
		if i == 1 && (w.Code != 429 || !strings.Contains(w.Body.String(), `"code":"rate_limited"`)) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestScheduleComputeDeadline(t *testing.T) {
	clk := clockwork.NewFakeClockAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	r := chi.NewRouter()
	Schedules(r, clk, time.Second, 0)
	for _, route := range []string{"validate", "preview"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequestWithContext(ctx, "POST", "/api/v1/schedules/"+route, strings.NewReader(`{"expr":"FREQ=DAILY;COUNT=100000","kind":"rrule"}`))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), "computation budget exceeded") {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body.String())
		}
		want := 200
		if route == "preview" {
			want = 400
		}
		if w.Code != want {
			t.Fatal(w.Code)
		}
	}
}
