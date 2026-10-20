package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-jose/go-jose/v4"
	"github.com/jonboulle/clockwork"
	"golang.org/x/oauth2"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/store"
)

func testService(t *testing.T) *Service {
	t.Helper()
	ctx := context.Background()
	c, e := config.Load(ctx, "", nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	dbs, e := store.Open(ctx, filepath.Join(dir, "c.db"), filepath.Join(dir, "h.db"), time.Second, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = dbs.Close() })
	return New(ctx, store.New(dbs), clockwork.NewRealClock(), c, []byte(strings.Repeat("s", 32)))
}
func TestAccountVerifiedLinkingAndSignupPolicy(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	p := Profile{"one", "Alice@Example.com", "Alice", true}
	u, e := s.Account(ctx, "github", p)
	if e != nil {
		t.Fatal(e)
	}
	p.Subject = "two"
	other, e := s.Account(ctx, "google", p)
	if e != nil || other.ID != u.ID {
		t.Fatalf("link: %v %v", other, e)
	}
	ws, e := s.Store.ControlReader().UserWorkspaces(ctx, u.ID)
	if e != nil || len(ws) != 1 {
		t.Fatal(ws, e)
	}
	s.Config.AllowSignup = false
	p.Subject = "three"
	p.Email = "other@example.com"
	if _, e = s.Account(ctx, "google", p); e == nil {
		t.Fatal("signup accepted")
	}
	s.Config.AllowSignup = true
	s.Config.AllowedEmailDomains = []string{"company.com"}
	if _, e = s.Account(ctx, "google", p); e == nil {
		t.Fatal("domain accepted")
	}
	p.Email = "test@company.com"
	p.Verified = false
	if _, e = s.Account(ctx, "google", p); e == nil {
		t.Fatal("unverified accepted")
	}
}
func TestAccountFollowsProviderEmailChange(t *testing.T) {
	s := testService(t)
	s.Config.AllowSignup = true
	ctx := context.Background()
	u, e := s.Account(ctx, "google", Profile{"sub", "old@example.com", "Ada", true})
	if e != nil {
		t.Fatal(e)
	}
	moved, e := s.Account(ctx, "google", Profile{"sub", "New@Example.com", "Ada", true})
	if e != nil || moved.ID != u.ID || moved.Email != "new@example.com" {
		t.Fatalf("email change: %+v %v", moved, e)
	}
	if stored, e := s.Store.ControlReader().UserByID(ctx, u.ID); e != nil || stored.Email != "new@example.com" {
		t.Fatalf("stored: %+v %v", stored, e)
	}
	// The freed address no longer matches the user, so it can't link a stranger to it.
	other, e := s.Account(ctx, "github", Profile{"gh", "old@example.com", "Bob", true})
	if e != nil || other.ID == u.ID {
		t.Fatalf("old address: %+v %v", other, e)
	}
	// Another account already holds the new address: sign in, keep the current one.
	kept, e := s.Account(ctx, "google", Profile{"sub", "old@example.com", "Ada", true})
	if e != nil || kept.ID != u.ID || kept.Email != "new@example.com" {
		t.Fatalf("collision: %+v %v", kept, e)
	}
	s.Config.AllowedEmailDomains = []string{"example.com"}
	if _, e = s.Account(ctx, "google", Profile{"sub", "ada@elsewhere.org", "Ada", true}); e == nil {
		t.Fatal("domain policy skipped for a changed email")
	}
}
func TestSignupClosedByDefaultExceptFirstUser(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	if s.Config.AllowSignup {
		t.Fatal("signup must be off by default")
	}
	if exists, e := s.HasUsers(ctx); e != nil || exists {
		t.Fatal(exists, e)
	}
	first, e := s.Account(ctx, "google", Profile{"one", "owner@example.com", "Owner", true})
	if e != nil {
		t.Fatalf("first user refused: %v", e)
	}
	if exists, e := s.HasUsers(ctx); e != nil || !exists {
		t.Fatal(exists, e)
	}
	if _, e = s.Account(ctx, "google", Profile{"two", "stranger@example.com", "Stranger", true}); !errors.Is(e, ErrSignupClosed) {
		t.Fatalf("second signup: %v", e)
	}
	// Existing users still sign in and link providers by verified email.
	again, e := s.Account(ctx, "github", Profile{"gh", "owner@example.com", "Owner", true})
	if e != nil || again.ID != first.ID {
		t.Fatalf("link: %+v %v", again, e)
	}
	// Development login is local-only and may create any number of users.
	if _, e = s.Account(ctx, "dev", Profile{"dev", "dev@dev.invalid", "dev", true}); e != nil {
		t.Fatalf("dev login: %v", e)
	}
}
func TestCallbackExplainsSignupRefusal(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	s.Providers["fake"] = &fakeProvider{}
	r := chi.NewRouter()
	s.Mount(r)
	h := s.Sessions.LoadAndSave(r)
	callback := func() *httptest.ResponseRecorder {
		start := httptest.NewRecorder()
		h.ServeHTTP(start, httptest.NewRequestWithContext(t.Context(), "GET", "/auth/fake/login", nil))
		u, _ := url.Parse(start.Header().Get("Location"))
		req := httptest.NewRequestWithContext(t.Context(), "GET", "/auth/fake/callback?code=c&state="+u.Query().Get("state"), nil)
		req.AddCookie(start.Result().Cookies()[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if _, e := s.Account(ctx, "google", Profile{"owner", "owner@example.com", "Owner", true}); e != nil {
		t.Fatal(e)
	}
	if w := callback(); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?error=signup_closed" {
		t.Fatal(w.Code, w.Header().Get("Location"), w.Body.String())
	}
	s.Config.AllowSignup = true
	s.Config.AllowedEmailDomains = []string{"company.com"}
	if w := callback(); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?error=domain_not_allowed" {
		t.Fatal(w.Code, w.Header().Get("Location"), w.Body.String())
	}
}
func TestOIDCDiscoveryPKCEAndNonce(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	nonce, verified := "nonce", true
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("code_verifier") != "verifier" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
			if e != nil {
				t.Error(e)
				return
			}
			claims, _ := json.Marshal(map[string]any{"iss": issuer, "aud": "client", "sub": "subject", "email": "alice@example.com", "email_verified": verified, "name": "Alice", "nonce": nonce, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
			signed, e := signer.Sign(claims)
			if e != nil {
				t.Error(e)
				return
			}
			raw, e := signed.CompactSerialize()
			if e != nil {
				t.Error(e)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "token_type": "Bearer", "id_token": raw})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	issuer = server.URL
	p, e := NewOIDC(context.Background(), issuer, "client", "secret", issuer+"/callback", func() time.Time { return now })
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(p.AuthorizationURL("state", "verifier", "nonce"))
	q := u.Query()
	if q.Get("state") != "state" || q.Get("nonce") != "nonce" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatal(q)
	}
	for _, tc := range []struct {
		name, verifier, wantNonce string
		verified                  bool
		ok                        bool
	}{{"valid", "verifier", "nonce", true, true}, {"pkce", "bad", "nonce", true, false}, {"nonce", "verifier", "bad", true, false}, {"unverified", "verifier", "nonce", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			verified = tc.verified
			_, e := p.Exchange(context.Background(), "code", tc.verifier, tc.wantNonce)
			if (e == nil) != tc.ok {
				t.Fatal(e)
			}
		})
	}
}
func TestGitHubPrimaryVerifiedEmail(t *testing.T) {
	verified, primary := true, true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "access", "token_type": "Bearer"})
		case "/user":
			if r.Header.Get("Authorization") != "Bearer access" {
				t.Error("missing bearer")
			}
			_, _ = w.Write([]byte(`{"id":7,"login":"alice"}`))
		case "/user/emails":
			_, _ = fmt.Fprintf(w, `[{"email":"private@example.com","primary":false,"verified":true},{"email":"alice@example.com","primary":%t,"verified":%t}]`, primary, verified)
		}
	}))
	defer server.Close()
	p := &GitHub{Config: oauth2.Config{ClientID: "c", ClientSecret: "s", Endpoint: oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token"}}, APIURL: server.URL}
	profile, e := p.Exchange(context.Background(), "code", "verifier", "")
	if e != nil || profile.Email != "alice@example.com" {
		t.Fatal(profile, e)
	}
	verified = false
	if _, e = p.Exchange(context.Background(), "code", "verifier", ""); e == nil {
		t.Fatal("unverified accepted")
	}
	verified = true
	primary = false
	if _, e = p.Exchange(context.Background(), "code", "verifier", ""); e == nil {
		t.Fatal("non-primary accepted")
	}
}

type fakeProvider struct{ calls int }

func (p *fakeProvider) AuthorizationURL(state, _, _ string) string {
	return "https://provider.example/?state=" + state
}
func (p *fakeProvider) Exchange(context.Context, string, string, string) (Profile, error) {
	p.calls++
	return Profile{"s", "alice@example.com", "Alice", true}, nil
}
func TestCallbackTamperingAndSessionFlags(t *testing.T) {
	s := testService(t)
	s.Secure = true
	s.Sessions.Cookie.Secure = true
	p := &fakeProvider{}
	s.Providers["fake"] = p
	r := chi.NewRouter()
	s.Mount(r)
	h := s.Sessions.LoadAndSave(r)
	start := httptest.NewRecorder()
	h.ServeHTTP(start, httptest.NewRequestWithContext(t.Context(), "GET", "/auth/fake/login", nil))
	cookie := start.Result().Cookies()[0]
	u, _ := url.Parse(start.Header().Get("Location"))
	state := u.Query().Get("state")
	for _, tc := range []struct{ name, raw, state string }{{"signature", cookie.Value + "x", state}, {"state", cookie.Value, "bad"}, {"provider", cookie.Value, state}} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/auth/fake/callback"
			if tc.name == "provider" {
				path = "/auth/other/callback"
			}
			req := httptest.NewRequestWithContext(t.Context(), "GET", path+"?code=c&state="+tc.state, nil)
			req.AddCookie(&http.Cookie{Name: "cp_oauth", Value: tc.raw})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 400 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if p.calls != 0 {
		t.Fatal("tampered flow reached provider")
	}
	req := httptest.NewRequestWithContext(t.Context(), "GET", "/auth/fake/callback?code=c&state="+state, nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 302 || w.Header().Get("Location") != "/" {
		t.Fatal(w.Code, w.Header().Get("Location"), w.Body.String())
	}
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "cp_session" {
			found = true
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
				t.Fatal(c)
			}
		}
	}
	if !found {
		t.Fatal("no session")
	}
}

func TestAPIKeyTouchIssuedOncePerMinute(t *testing.T) {
	ctx := context.Background()
	s := testService(t)
	fake := clockwork.NewFakeClock()
	s.Clock = fake
	u, e := s.Account(ctx, "dev", Profile{Subject: "k", Email: "k@example.com", Name: "K", Verified: true})
	if e != nil {
		t.Fatal(e)
	}
	ws, e := s.Store.ControlReader().PersonalWorkspace(ctx, u.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, raw, e := s.MintKey(ctx, ws.ID, u.ID, "k", []string{"*"})
	if e != nil {
		t.Fatal(e)
	}
	touches := 0
	s.onTouch = func() { touches++ }
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	call := func() {
		req := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for range 50 {
		call()
		fake.Advance(time.Second)
	}
	if touches != 1 {
		t.Fatalf("touches within a minute = %d, want 1", touches)
	}
	fake.Advance(time.Minute)
	call()
	if touches != 2 {
		t.Fatalf("touches after a minute = %d, want 2", touches)
	}
}

func TestTouchCacheBounded(t *testing.T) {
	var c touchCache
	now := time.Unix(0, 0)
	for i := range touchCacheMax * 3 {
		c.due(fmt.Sprint(i), now)
	}
	if len(c.last) > touchCacheMax {
		t.Fatalf("cache grew to %d", len(c.last))
	}
}
