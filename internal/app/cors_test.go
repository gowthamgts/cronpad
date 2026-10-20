package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gowthamgts/cronpad/internal/config"
)

func TestCORSAllowsOnlyListedOriginsWithoutCredentials(t *testing.T) {
	f := newFixtureWith(t, func(c *config.Config) { c.UI.CORSOrigins = []string{"https://site.example/"} })

	preflight := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/schedules/preview", nil)
	preflight.Header.Set("Origin", "https://site.example")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflight.Header.Set("Access-Control-Request-Headers", "content-type")
	w := httptest.NewRecorder()
	f.a.Handler.ServeHTTP(w, preflight)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "https://site.example" || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Content-Type") {
		t.Fatalf("preflight: %d %v", w.Code, w.Header())
	}
	if w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentials must not be allowed cross-origin")
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/schedules/preview", strings.NewReader(`{"expr":"every day at 9am","timezone":"UTC","count":2}`))
	req.Header.Set("Origin", "https://site.example")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	f.a.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "https://site.example" {
		t.Fatalf("preview: %d %v %s", w.Code, w.Header(), w.Body)
	}

	for _, path := range []string{"/api/v1/schedules/preview", "/"} {
		req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.Header.Set("Origin", "https://evil.example")
		w = httptest.NewRecorder()
		f.a.Handler.ServeHTTP(w, req)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("%s: unlisted origin allowed: %q", path, got)
		}
	}
}
