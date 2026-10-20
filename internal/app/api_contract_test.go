package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/go-chi/chi/v5"
	"github.com/jonboulle/clockwork"

	contract "github.com/gowthamgts/cronpad/api"
	"github.com/gowthamgts/cronpad/internal/auth"
	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

const m3Job = `{"name":"report","schedule":{"expr":"every day at 9am"},"sink":{"kind":"webhook","config":{"url":"https://example.com"}}}`

// validated2xx records every operation that returned a 2xx response which passed schema
// validation, across all tests in the package. TestMain fails a full run when a spec
// operation never produced one, because a 404 against a missing ID proves nothing about
// the documented success schema.
var (
	validated2xxMu sync.Mutex
	validated2xx   = map[string]bool{}
)

func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 && flagValue("test.run") == "" && flagValue("test.short") != "true" {
		if missing := unvalidatedSuccessOperations(); len(missing) > 0 {
			_, _ = os.Stderr.WriteString("openapi: operations with no validated 2xx response (add a success-path request to the contract tests):\n  " + strings.Join(missing, "\n  ") + "\n")
			code = 1
		}
	}
	os.Exit(code)
}

func flagValue(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

func unvalidatedSuccessOperations() []string {
	doc, e := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if e != nil {
		return []string{e.Error()}
	}
	validated2xxMu.Lock()
	defer validated2xxMu.Unlock()
	var missing []string
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			has2xx := false
			for code := range op.Responses.Map() {
				if strings.HasPrefix(code, "2") {
					has2xx = true
				}
			}
			if has2xx && !validated2xx[method+" "+path] {
				missing = append(missing, method+" "+path)
			}
		}
	}
	sort.Strings(missing)
	return missing
}

type fixture struct {
	a       *App
	t       *testing.T
	spec    *openapi3.T
	router  routers.Router
	covered map[string]bool
}

func newFixture(t *testing.T) *fixture { return newFixtureWith(t, nil) }

func newFixtureWith(t *testing.T, tweak func(*config.Config)) *fixture {
	t.Helper()
	ctx := context.Background()
	c, e := config.Load(ctx, "", nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.Auth.DevLogin = true
	if tweak != nil {
		tweak(&c)
	}
	c.Secrets.EncryptionKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	dir := t.TempDir()
	c.Database.Path = filepath.Join(dir, "c.db")
	c.Database.HistoryPath = filepath.Join(dir, "h.db")
	a, e := New(ctx, c, "oss", nil, clockwork.NewRealClock(), slog.New(slog.DiscardHandler))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = a.Close() })
	doc, e := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if e != nil {
		t.Fatal(e)
	}
	if e = doc.Validate(ctx); e != nil {
		t.Fatal(e)
	}
	router, e := legacy.NewRouter(doc)
	if e != nil {
		t.Fatal(e)
	}
	return &fixture{a, t, doc, router, map[string]bool{}}
}
func (f *fixture) request(method, path, body string, headers map[string]string, cookies []*http.Cookie, status int) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequestWithContext(f.t.Context(), method, path, strings.NewReader(body))
	req.Header.Set("X-Dev-User", "alice")
	req.Header.Set("X-Request-ID", "contract-request")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Tests acquire real sessions through the same endpoint as the dashboard.
	if cookies == nil && req.Header.Get("Authorization") == "" && !strings.HasPrefix(path, "/auth/") {
		name := req.Header.Get("X-Dev-User")
		if name != "" {
			login := httptest.NewRecorder()
			loginRequest := httptest.NewRequestWithContext(f.t.Context(), "POST", "/auth/dev/login", strings.NewReader("name="+name))
			loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			f.a.Handler.ServeHTTP(login, loginRequest)
			// The dev endpoint accepts form data.
			if login.Code != 204 {
				f.t.Fatalf("dev login: %d %s", login.Code, login.Body.String())
			}
			cookies = login.Result().Cookies()
			for _, cookie := range cookies {
				if cookie.Name == "cp_csrf" {
					req.Header.Set("X-CSRF-Token", cookie.Value)
				}
			}
		}
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	route, params, e := f.router.FindRoute(req)
	if e != nil {
		f.t.Fatalf("missing contract %s %s: %v", method, path, e)
	}
	input := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route, Options: &openapi3filter.Options{AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil }}}
	if e = openapi3filter.ValidateRequest(context.Background(), input); e != nil {
		f.t.Fatalf("request %s %s: %v", method, path, e)
	}
	w := httptest.NewRecorder()
	f.a.Handler.ServeHTTP(w, req)
	if status != 0 && w.Code != status {
		f.t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	if w.Header().Get("X-Request-ID") != "contract-request" {
		f.t.Fatal("request ID not echoed")
	}
	response := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: w.Code, Header: w.Header(), Body: io.NopCloser(bytes.NewReader(w.Body.Bytes()))}
	if e = openapi3filter.ValidateResponse(context.Background(), response); e != nil {
		f.t.Fatalf("response %s %s (%d): %v\n%s", method, path, w.Code, e, w.Body.String())
	}
	f.covered[method+" "+route.Path] = true
	if w.Code/100 == 2 {
		validated2xxMu.Lock()
		validated2xx[method+" "+route.Path] = true
		validated2xxMu.Unlock()
	}
	return w
}
func object(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestOpenAPIContractEveryMountedRoute(t *testing.T) {
	f := newFixture(t)
	mounted := map[string]bool{}
	e := chi.Walk(f.a.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == f.a.Config.MCP.Path || route == "/robots.txt" || route == "/" || route == "/app" || strings.HasPrefix(route, "/app/") {
			return nil // dashboard assets are not part of the API contract
		}
		key := method + " " + route
		mounted[key] = true
		item := f.spec.Paths.Value(route)
		if item == nil || item.GetOperation(method) == nil {
			t.Errorf("mounted route missing operation: %s", key)
			return nil
		}
		path := strings.NewReplacer("{id}", "missing", "{ws}", "missing", "{name}", "MISSING", "{provider}", "missing").Replace(route)
		if route == "/api/v1/alert-channels/verify" {
			path += "?token=invalid"
		}
		body := ""
		if method == "POST" || method == "PATCH" || method == "PUT" {
			body = "{}"
			if method == "POST" && (route == "/api/v1/jobs" || route == "/api/v1/jobs/test") {
				body = strings.Replace(m3Job, "https://example.com", "http://127.0.0.1:1", 1)
			}
			if route == "/api/v1/api-keys" && method == "POST" {
				body = `{"name":"contract-key"}`
			}
			if strings.Contains(route, "/schedules/") {
				body = `{"expr":"every day at 9am"}`
			}
			if strings.Contains(route, "/secrets/") {
				body = `{"value":"test"}`
			}
		}
		f.request(method, path, body, nil, nil, 0)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	for path, item := range f.spec.Paths.Map() {
		for method := range item.Operations() {
			if !mounted[method+" "+path] {
				t.Errorf("operation not mounted: %s %s", method, path)
			}
			if !f.covered[method+" "+path] {
				t.Errorf("contract did not exercise %s %s", method, path)
			}
		}
	}
}
func TestAPIKeyFlowAndIsolation(t *testing.T) {
	f := newFixture(t)
	key := object(t, f.request("POST", "/api/v1/api-keys", `{"name":"read-only","scopes":["jobs:read","runs:read"]}`, nil, nil, 201))
	raw := key["key"].(string)
	headers := map[string]string{"Authorization": "Bearer " + raw}
	f.request("GET", "/api/v1/jobs", "", headers, nil, 200)
	f.request("POST", "/api/v1/jobs", m3Job, headers, nil, 403)
	f.request("DELETE", "/api/v1/api-keys/"+key["id"].(string), "", nil, nil, 204)
	f.request("GET", "/api/v1/jobs", "", headers, nil, 401)
	created := object(t, f.request("POST", "/api/v1/jobs", m3Job, map[string]string{"Idempotency-Key": "job-one"}, nil, 201))
	jobID := created["id"].(string)
	same := object(t, f.request("POST", "/api/v1/jobs", m3Job, map[string]string{"Idempotency-Key": "job-one"}, nil, 201))
	if same["id"] != jobID {
		t.Fatal("idempotency created another job")
	}
	f.request("POST", "/api/v1/jobs", strings.Replace(m3Job, "report", "different", 1), map[string]string{"Idempotency-Key": "job-one"}, nil, 409)
	ws := object(t, f.request("GET", "/api/v1/me", "", nil, nil, 200))["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	f.request("PUT", "/api/v1/secrets/TOKEN", `{"value":"TOP_SECRET_M3"}`, nil, nil, 204)
	k := object(t, f.request("POST", "/api/v1/api-keys", `{"name":"admin"}`, map[string]string{"Idempotency-Key": "admin-key"}, nil, 201))
	replay := object(t, f.request("POST", "/api/v1/api-keys", `{"name":"admin"}`, map[string]string{"Idempotency-Key": "admin-key"}, nil, 201))
	if replay["key"] != k["key"] {
		t.Fatal("key idempotency")
	}
	repository := store.New(f.a.DBs)
	_, e := repository.History().UpsertRun(context.Background(), historygen.UpsertRunParams{ID: "run_failed", JobID: jobID, WorkspaceID: ws, ScheduledAt: 1, Trigger: "schedule", Status: "failed", JobVersion: 1})
	if e != nil {
		t.Fatal(e)
	}
	assertWorkspaceIsolation(t, f, []isolationCase{{"GET", "/api/v1/workspaces/" + ws, ""}, {"PATCH", "/api/v1/workspaces/" + ws, `{"name":"stolen"}`}, {"DELETE", "/api/v1/api-keys/" + k["id"].(string), ""}, {"DELETE", "/api/v1/secrets/TOKEN", ""}, {"GET", "/api/v1/runs/run_failed", ""}, {"POST", "/api/v1/runs/run_failed/retry", ""}})
	for _, tc := range []isolationCase{{"GET", "", ""}, {"PATCH", "", `{"name":"stolen"}`}, {"DELETE", "", ""}, {"POST", "/pause", ""}, {"POST", "/resume", ""}, {"POST", "/enable", ""}, {"POST", "/trigger", ""}, {"POST", "/test", ""}, {"POST", "/rotate-signing-secret", ""}, {"GET", "/runs", ""}} {
		assertWorkspaceIsolation(t, f, []isolationCase{{tc.method, "/api/v1/jobs/" + jobID + tc.path, tc.body}})
	}
	for _, path := range []string{"jobs", "runs", "api-keys", "secrets", "audit-log"} {
		out := object(t, f.request("GET", "/api/v1/"+path, "", map[string]string{"X-Dev-User": "bob"}, nil, 200))
		if len(out["data"].([]any)) != 0 {
			t.Fatalf("cross workspace list %s leaked: %v", path, out)
		}
	}
	f.request("GET", "/api/v1/jobs/"+jobID, "", nil, nil, 200)
	f.request("PATCH", "/api/v1/jobs/"+jobID, `{"name":"updated"}`, nil, nil, 200)
	f.request("POST", "/api/v1/jobs/"+jobID+"/pause", "", nil, nil, 200)
	f.request("POST", "/api/v1/jobs/"+jobID+"/resume", "", nil, nil, 200)
	f.request("POST", "/api/v1/jobs/"+jobID+"/rotate-signing-secret", "", nil, nil, 200)
	f.request("POST", "/api/v1/jobs/"+jobID+"/trigger", "", nil, nil, 202)
	f.request("GET", "/api/v1/jobs/"+jobID+"/runs", "", nil, nil, 200)
	f.request("GET", "/api/v1/runs", "", nil, nil, 200)
	f.request("GET", "/api/v1/runs/run_failed", "", nil, nil, 200)
	f.request("POST", "/api/v1/runs/run_failed/retry", "", nil, nil, 202)
	f.request("GET", "/api/v1/stats/overview?window=7d", "", nil, nil, 200)
	slugOf := func(body string) string {
		var v struct{ Slug string }
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatal(err)
		}
		return v.Slug
	}
	before := slugOf(f.request("GET", "/api/v1/workspaces/"+ws, "", nil, nil, 200).Body.String())
	if after := slugOf(f.request("PATCH", "/api/v1/workspaces/"+ws, `{"name":"New workspace name"}`, nil, nil, 200).Body.String()); after != before {
		t.Fatalf("renaming the workspace changed its slug from %q to %q", before, after)
	}
	list := f.request("GET", "/api/v1/secrets", "", nil, nil, 200)
	if strings.Contains(list.Body.String(), "TOP_SECRET_M3") {
		t.Fatal("secret returned")
	}
	f.request("DELETE", "/api/v1/secrets/TOKEN", "", nil, nil, 204)
	f.request("DELETE", "/api/v1/jobs/"+jobID, "", nil, nil, 204)
	audit := object(t, f.request("GET", "/api/v1/audit-log", "", nil, nil, 200))
	if len(audit["data"].([]any)) < 10 {
		t.Fatal("missing audit entries", audit)
	}
	var stored string
	if e = f.a.DBs.Control.Reader.QueryRow("SELECT signing_secrets FROM jobs WHERE id=?", jobID).Scan(&stored); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(stored, "whsec_") {
		t.Fatal("plaintext signing secret")
	}
	rows, e := f.a.DBs.Control.Reader.Query("SELECT response FROM idempotency_records")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if e = rows.Scan(&stored); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(stored, "whsec_") || strings.Contains(stored, raw) || strings.Contains(stored, k["key"].(string)) {
			t.Fatal("plaintext idempotency secret")
		}
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
}

type isolationCase struct{ method, path, body string }

func assertWorkspaceIsolation(t *testing.T, f *fixture, cases []isolationCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run("isolation_"+tc.method+tc.path, func(t *testing.T) {
			f.request(tc.method, tc.path, tc.body, map[string]string{"X-Dev-User": "bob"}, nil, 404)
		})
	}
}
func TestSessionCSRFAndWorkspaceMembership(t *testing.T) {
	f := newFixture(t)
	service := f.a.deps.Session.(*auth.Service)
	u, e := service.Account(context.Background(), "fake", auth.Profile{Subject: "subject", Email: "session@example.com", Name: "Session", Verified: true})
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
	w := httptest.NewRecorder()
	service.Sessions.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e := service.Login(r.Context(), w, r, u.ID); e != nil {
			t.Error(e)
		}
	})).ServeHTTP(w, r)
	cookies := w.Result().Cookies()
	csrf := ""
	for _, cookie := range cookies {
		if cookie.Name == "cp_csrf" {
			csrf = cookie.Value
		}
		if cookie.Name == "cp_session" && (!cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure) {
			t.Fatal(cookie)
		}
	}
	if csrf == "" {
		t.Fatal("missing csrf")
	}
	f.request("GET", "/api/v1/me", "", nil, cookies, 200)
	f.request("POST", "/api/v1/jobs", m3Job, nil, cookies, 403)
	f.request("POST", "/api/v1/jobs", m3Job, map[string]string{"X-CSRF-Token": "wrong"}, cookies, 403)
	f.request("POST", "/api/v1/jobs", m3Job, map[string]string{"X-CSRF-Token": csrf}, cookies, 201)
	f.request("GET", "/api/v1/jobs", "", map[string]string{"Cronpad-Workspace": "ws_foreign"}, cookies, 404)
	f.request("POST", "/auth/logout", "", map[string]string{"X-CSRF-Token": csrf}, cookies, 204)
	f.request("GET", "/api/v1/me", "", nil, cookies, 401)
}

func TestSecretExecutionRedactionAndLimitEnvelope(t *testing.T) {
	f := newFixture(t)
	f.request("PUT", "/api/v1/secrets/TOKEN", `{"value":"DO_NOT_RETURN_M3"}`, nil, nil, 204)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer DO_NOT_RETURN_M3" {
			t.Error("secret not resolved")
		}
		w.WriteHeader(500)
		_, _ = w.Write([]byte("DO_NOT_RETURN_M3"))
	}))
	defer server.Close()
	body := `{"name":"secret-test","schedule":{"expr":"every day at 9am"},"sink":{"kind":"webhook","config":{"url":"` + server.URL + `","headers":{"Authorization":"Bearer {{secret.TOKEN}}"}}}}`
	created := object(t, f.request("POST", "/api/v1/jobs", body, nil, nil, 201))
	out := f.request("POST", "/api/v1/jobs/"+created["id"].(string)+"/test", "", nil, nil, 200)
	if strings.Contains(out.Body.String(), "DO_NOT_RETURN_M3") || !strings.Contains(out.Body.String(), "[REDACTED]") {
		t.Fatal("secret not redacted", out.Body.String())
	}
	service := f.a.deps.Session.(*auth.Service)
	service.Limits.Config.MaxJobsPerWorkspace = 1
	out = f.request("POST", "/api/v1/jobs", m3Job, nil, nil, 403)
	if strings.TrimSpace(out.Body.String()) != `{"error":{"code":"limit_exceeded","limit":"max_jobs_per_workspace","value":1}}` {
		t.Fatal(out.Body.String())
	}
	service.Limits.Config.MaxRetryAttempts = 2
	out = f.request("POST", "/api/v1/jobs/test", strings.Replace(m3Job, `"report"`, `"retry-limit"`, 1), nil, nil, 403)
	if !strings.Contains(out.Body.String(), `"limit":"max_retry_attempts"`) {
		t.Fatal(out.Body.String())
	}
}

func TestRateLimitHeadersPerKey(t *testing.T) {
	f := newFixture(t)
	service := f.a.deps.Session.(*auth.Service)
	me := object(t, f.request("GET", "/api/v1/me", "", nil, nil, 200))
	ws := me["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	u := me["id"].(string)
	_, raw, e := service.MintKey(context.Background(), ws, u, "rate", []string{"*"})
	if e != nil {
		t.Fatal(e)
	}
	handler := service.Sessions.LoadAndSave(service.Middleware(service.RateLimit(1)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))))
	for _, status := range []int{204, 429} {
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/jobs", nil)
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("RateLimit-Limit") == "" || w.Header().Get("RateLimit-Remaining") == "" || w.Header().Get("RateLimit-Reset") == "" {
			t.Fatal(w.Code, w.Header())
		}
		if status == 429 && w.Header().Get("Retry-After") == "" {
			t.Fatal("missing Retry-After")
		}
	}
}

func TestFeaturesEndpointIsPublicAndSecretFree(t *testing.T) {
	f := newFixture(t)
	// Until someone signs up the dashboard shows onboarding, so no cache may keep it.
	w := f.request("GET", "/api/v1/features", "", map[string]string{"X-Dev-User": ""}, nil, 200)
	if w.Header().Get("Cache-Control") != "no-store" || object(t, w)["auth"].(map[string]any)["setup"] != true {
		t.Fatalf("before first user: %q %s", w.Header().Get("Cache-Control"), w.Body)
	}
	f.request("GET", "/api/v1/me", "", nil, nil, 200)
	w = f.request("GET", "/api/v1/features", "", map[string]string{"X-Dev-User": ""}, nil, 200)
	if w.Header().Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("cache-control %q", w.Header().Get("Cache-Control"))
	}
	body := object(t, w)
	auth := body["auth"].(map[string]any)
	if auth["setup"] != false || auth["signup"] != false {
		t.Fatalf("after first user: %v", auth)
	}
	if auth["dev_login"] != true || len(auth["providers"].([]any)) != 0 {
		t.Fatalf("features %v", body)
	}
	if body["sinks"].(map[string]any)["webhook"] != true {
		t.Fatalf("features %v", body)
	}
}

func TestDevLoginDoesNotAuthenticateAnonymousRequests(t *testing.T) {
	f := newFixture(t)
	for _, header := range []string{"", "developer"} {
		r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/jobs", nil)
		r.Header.Set("X-Dev-User", header)
		w := httptest.NewRecorder()
		f.a.Handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("anonymous header %q: %d", header, w.Code)
		}
	}
}

// validateResponse checks a response from a test that talks to the handler directly (not
// through fixture.request) against the spec and records the operation as validated.
func validateResponse(t *testing.T, req *http.Request, w *httptest.ResponseRecorder) {
	t.Helper()
	doc, e := openapi3.NewLoader().LoadFromData(contract.OpenAPI)
	if e != nil {
		t.Fatal(e)
	}
	router, e := legacy.NewRouter(doc)
	if e != nil {
		t.Fatal(e)
	}
	route, params, e := router.FindRoute(req)
	if e != nil {
		t.Fatalf("missing contract %s %s: %v", req.Method, req.URL.Path, e)
	}
	input := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route}
	response := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: w.Code, Header: w.Header(), Body: io.NopCloser(bytes.NewReader(w.Body.Bytes()))}
	if e = openapi3filter.ValidateResponse(context.Background(), response); e != nil {
		t.Fatalf("response %s %s (%d): %v\n%s", req.Method, req.URL.Path, w.Code, e, w.Body.String())
	}
	if w.Code/100 == 2 {
		validated2xxMu.Lock()
		validated2xx[req.Method+" "+route.Path] = true
		validated2xxMu.Unlock()
	}
}

// Operations the generic sweep and the flow tests only reach through error paths.
func TestOpenAPISuccessPathsOfRemainingOperations(t *testing.T) {
	f := newFixtureWith(t, func(c *config.Config) {
		c.Alerts.Email = config.ConfigAlertsEmail{Enabled: true, SMTPHost: "127.0.0.1", SMTPPort: 1, From: "alerts@example.com"}
	})
	// Queueing a verification mail goes through the history writer, which the dispatcher drives.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = f.a.Dispatcher.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	f.request("POST", "/auth/dev/login", "name=alice", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, nil, 204)
	me := object(t, f.request("GET", "/api/v1/me", "", nil, nil, 200))
	ws := me["workspaces"].([]any)[0].(map[string]any)["id"].(string)
	f.request("GET", "/api/v1/workspaces/"+ws, "", nil, nil, 200)

	job := object(t, f.request("POST", "/api/v1/jobs", m3Job, nil, nil, 201))["id"].(string)
	if _, e := f.a.DBs.Control.Writer.Exec("UPDATE jobs SET state='disabled' WHERE id=?", job); e != nil {
		t.Fatal(e)
	}
	f.request("POST", "/api/v1/jobs/"+job+"/enable", "", nil, nil, 200)

	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer hook.Close()
	webhook := object(t, f.request("POST", "/api/v1/alert-channels", `{"kind":"webhook","name":"ops","config":{"url":"`+hook.URL+`","format":"generic"}}`, nil, nil, 201))["id"].(string)
	f.request("POST", "/api/v1/alert-channels/"+webhook+"/test", "", nil, nil, 200)

	email := object(t, f.request("POST", "/api/v1/alert-channels", `{"kind":"email","name":"mail","config":{"to":["owner@example.com"]}}`, nil, nil, 201))["id"].(string)
	f.request("POST", "/api/v1/alert-channels/"+email+"/verify", "", nil, nil, 204)
	row, e := store.New(f.a.DBs).ControlReader().GetAlertChannel(context.Background(), sqlitegen.GetAlertChannelParams{ID: email, WorkspaceID: ws})
	if e != nil {
		t.Fatal(e)
	}
	link, e := f.a.notifier.VerificationLink(row, "owner@example.com")
	if e != nil {
		t.Fatal(e)
	}
	f.request("GET", strings.TrimPrefix(link, f.a.notifier.PublicURL), "", nil, nil, 200)
}
