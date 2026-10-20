package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/internal/config"
)

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func devLogin(t *testing.T, f *fixture) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/auth/dev/login", strings.NewReader("name=alice"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f.a.Handler.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("dev login: %d %s", w.Code, w.Body.String())
	}
	return w
}

func TestSignedInHintCookieOnSharedDomain(t *testing.T) {
	f := newFixtureWith(t, func(c *config.Config) {
		c.Mode = "hosted"
		c.Server.PublicURL = "http://dash.example.com"
	})
	login := devLogin(t, f)
	cookies := login.Result().Cookies()
	hint := findCookie(cookies, "cronpad_signed_in")
	if hint == nil || hint.Value != "1" || hint.Domain != "example.com" || hint.HttpOnly || hint.SameSite != http.SameSiteLaxMode || hint.MaxAge <= 0 {
		t.Fatalf("hint cookie %+v", hint)
	}
	if session := findCookie(cookies, "cp_session"); session == nil || session.Domain != "" || !session.HttpOnly {
		t.Fatalf("session cookie must stay host-only and HttpOnly: %+v", session)
	}

	// Signing out clears the hint on the shared domain.
	logout := httptest.NewRequestWithContext(t.Context(), "POST", "/auth/logout", nil)
	for _, c := range cookies {
		logout.AddCookie(c)
		if c.Name == "cp_csrf" {
			logout.Header.Set("X-CSRF-Token", c.Value)
		}
	}
	w := httptest.NewRecorder()
	f.a.Handler.ServeHTTP(w, logout)
	if w.Code != 204 {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
	if cleared := findCookie(w.Result().Cookies(), "cronpad_signed_in"); cleared == nil || cleared.MaxAge >= 0 || cleared.Domain != "example.com" {
		t.Fatalf("logout must clear the hint: %+v", cleared)
	}

	// A stale hint without a session is cleared on the next 401.
	stale := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/me", nil)
	stale.AddCookie(&http.Cookie{Name: "cronpad_signed_in", Value: "1"})
	w = httptest.NewRecorder()
	f.a.Handler.ServeHTTP(w, stale)
	if w.Code != 401 {
		t.Fatalf("stale hint: %d", w.Code)
	}
	if cleared := findCookie(w.Result().Cookies(), "cronpad_signed_in"); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("401 must clear a stale hint: %+v", cleared)
	}
}

func TestNoSignedInHintWhenSelfHosted(t *testing.T) {
	f := newFixture(t)
	if hint := findCookie(devLogin(t, f).Result().Cookies(), "cronpad_signed_in"); hint != nil {
		t.Fatalf("unexpected hint cookie %+v", hint)
	}
}

func TestDashboardIndexCarriesCookieDomain(t *testing.T) {
	files := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><html><head><title>x</title></head></html>")}}
	r := chi.NewRouter()
	mountDashboard(r, files, "/agent", "example.com")
	body := get(t, r, "/jobs").Body.String()
	if !strings.Contains(body, `<head>`+"\n    "+`<meta name="cronpad-cookie-domain" content="example.com" />`) {
		t.Fatalf("missing cookie-domain meta: %s", body)
	}
	if body := get(t, dashboardRouter(files), "/").Body.String(); strings.Contains(body, "cronpad-cookie-domain") {
		t.Fatalf("meta without cookie domain: %s", body)
	}
}
