package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/internal/config"
)

func dashboardRouter(files fstest.MapFS) http.Handler {
	r := chi.NewRouter()
	mountDashboard(r, files, "/agent", "", "")
	return r
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
	return w
}

func TestDashboardServing(t *testing.T) {
	h := dashboardRouter(fstest.MapFS{
		"index.html":        {Data: []byte("<html>spa</html>")},
		"assets/app-abc.js": {Data: []byte("console.log(1)")},
		"favicon.svg":       {Data: []byte("<svg/>")},
	})
	for target, want := range map[string]string{
		"/app": "/", "/app/": "/", "/app/jobs": "/jobs",
		"/app/jobs/job_1?tab=runs": "/jobs/job_1?tab=runs",
		"/app/runs/run_1":          "/runs/run_1", "/app//example.com": "/example.com",
	} {
		if w := get(t, h, target); w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != want {
			t.Fatalf("%s -> %d %q, want %s", target, w.Code, w.Header().Get("Location"), want)
		}
	}
	for _, target := range []string{"/", "/jobs/job_1", "/settings/keys"} {
		w := get(t, h, target)
		if w.Code != 200 || w.Body.String() != "<html>spa</html>" {
			t.Fatalf("%s -> %d %q", target, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s cache %q", target, w.Header().Get("Cache-Control"))
		}
		if csp := w.Header().Get("Content-Security-Policy"); csp != dashboardCSP {
			t.Fatalf("%s csp %q", target, csp)
		}
	}
	w := get(t, h, "/assets/app-abc.js")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset -> %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if w := get(t, h, "/assets/missing.js"); w.Code != 404 {
		t.Fatalf("missing asset -> %d", w.Code)
	}
	if w := get(t, h, "/../../etc/passwd"); w.Code == 200 && w.Body.String() != "<html>spa</html>" {
		t.Fatalf("traversal served %q", w.Body.String())
	}
}

func TestDashboardWithoutBuild(t *testing.T) {
	w := get(t, dashboardRouter(fstest.MapFS{".gitkeep": {}}), "/")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unbuilt dashboard -> %d", w.Code)
	}
}

func TestDashboardMountFollowsConfig(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		f := newFixtureWith(t, func(c *config.Config) { c.UI.Dashboard = enabled })
		w := get(t, f.a.Handler, "/")
		if enabled && w.Code == http.StatusNotFound {
			t.Fatalf("dashboard enabled but / not mounted")
		}
		if !enabled && w.Code != http.StatusNotFound {
			t.Fatalf("dashboard disabled but / answered %d", w.Code)
		}
	}
}

func TestDashboardPreservesBackendRoutes(t *testing.T) {
	f := newFixture(t)
	for _, target := range []string{"/api/unknown", "/api/v1/unknown"} {
		w := get(t, f.a.Handler, target)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Code != "not_found" {
			t.Fatalf("%s -> %d %v %s", target, w.Code, w.Header(), w.Body)
		}
	}
	for _, target := range []string{"/healthz", "/readyz", "/version", "/robots.txt"} {
		w := get(t, f.a.Handler, target)
		validStatus := w.Code == http.StatusOK || target == "/readyz" && w.Code == http.StatusServiceUnavailable
		if !validStatus || strings.Contains(w.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("backend route %s -> %d %v", target, w.Code, w.Header())
		}
	}
	for _, target := range []string{"/auth/unknown", "/mcp/unknown", "/metrics", "/llms.txt", "/healthz/unknown"} {
		w := get(t, f.a.Handler, target)
		if w.Code != http.StatusNotFound || strings.Contains(w.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("reserved path %s -> %d %v", target, w.Code, w.Header())
		}
	}
	if w := get(t, dashboardRouter(fstest.MapFS{"index.html": {Data: []byte("spa")}}), "/agent/unknown"); w.Code != http.StatusNotFound {
		t.Fatalf("custom MCP path -> %d", w.Code)
	}
}

func TestDashboardIndexMakesOGImageAbsolute(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte(`<head><meta property="og:image" content="/og.png" /></head>`)}}
	r := chi.NewRouter()
	mountDashboard(r, files, "/agent", "", "https://app.example.com/")
	if body := get(t, r, "/").Body.String(); !strings.Contains(body, `content="https://app.example.com/og.png"`) {
		t.Fatalf("og:image not absolute: %s", body)
	}
	if body := get(t, dashboardRouter(files), "/").Body.String(); !strings.Contains(body, `content="/og.png"`) {
		t.Fatalf("og:image changed without a public URL: %s", body)
	}
}
