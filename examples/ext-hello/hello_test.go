package hello_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	hello "github.com/gowthamgts/cronpad/examples/ext-hello"
	"github.com/gowthamgts/cronpad/internal/app"
	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

func testRequest(t *testing.T, client *http.Client, method, target string, form url.Values) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return client.Do(req)
}

func TestPublicExtension(t *testing.T) {
	ctx := context.Background()
	e := hello.New()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[hello]\ngreeting='welcome'\n[unknown]\nvalue=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sections, err := app.Sections([]ext.Extension{e})
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(ctx, path, nil, sections, logger)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "unknown config section") || !strings.Contains(logs.String(), "unknown") {
		t.Fatal(logs.String())
	}
	c.Database.Path = filepath.Join(dir, "control.db")
	c.Database.HistoryPath = filepath.Join(dir, "history.db")
	a, err := app.New(ctx, c, "hello", []ext.Extension{e}, nil, clockwork.NewFakeClock(), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	server := httptest.NewServer(a.Handler)
	defer server.Close()
	for route, want := range map[string]string{"/hello": "welcome", "/healthz": "ok", "/readyz": `"dispatcher":"starting"`, "/version": `"edition":"hello"`} {
		resp, err := testRequest(t, server.Client(), http.MethodGet, server.URL+route, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		// DECISION: readiness requires a running dispatcher; this fixture mounts only routes.
		status := 200
		if route == "/readyz" {
			status = 503
		}
		if err != nil || resp.StatusCode != status || !strings.Contains(string(body), want) {
			t.Fatalf("%s: %d %s %v", route, resp.StatusCode, body, err)
		}
	}
	var n int
	if err := a.DBs.Control.Reader.QueryRowContext(ctx, "SELECT count(*) FROM goose_hello WHERE version_id=1 AND is_applied=1").Scan(&n); err != nil || n != 1 {
		t.Fatalf("extension goose table: %d %v", n, err)
	}
	if _, err := a.DBs.Control.Writer.ExecContext(ctx, "INSERT INTO hello_messages(message) VALUES ('ok')"); err != nil {
		t.Fatal(err)
	}
}

func TestV2Hooks(t *testing.T) {
	ctx := context.Background()
	e := hello.New()
	c, err := config.Load(ctx, "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Auth.DevLogin = true
	dir := t.TempDir()
	c.Database.Path = filepath.Join(dir, "c.db")
	c.Database.HistoryPath = filepath.Join(dir, "h.db")
	a, err := app.New(ctx, c, "hello", []ext.Extension{e}, nil, clockwork.NewRealClock(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	server := httptest.NewServer(a.Handler)
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	resp, err := testRequest(t, client, http.MethodGet, server.URL+"/auth/sso?email=hello@example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/auth/hello/login" {
		t.Fatal(resp.Status)
	}
	resp, err = testRequest(t, client, http.MethodGet, server.URL+"/auth/hello/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), "/hello?state=") {
		t.Fatal(resp.Status)
	}
	stateURL, _ := url.Parse(resp.Header.Get("Location"))
	resp, err = testRequest(t, client, http.MethodGet, server.URL+"/auth/hello/callback?state="+stateURL.Query().Get("state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("dummy provider accepted unsigned callback")
	}
	resp, err = testRequest(t, client, http.MethodPost, server.URL+"/auth/dev/login", url.Values{"name": {"hooks"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.Status)
	}
	resp, err = testRequest(t, client, http.MethodGet, server.URL+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	var me struct {
		ID         string
		Workspaces []struct{ ID string }
	}
	err = json.NewDecoder(resp.Body).Decode(&me)
	_ = resp.Body.Close()
	if err != nil || len(me.Workspaces) != 1 {
		t.Fatal(me, err)
	}
	if e.Authorized.Load() == 0 || e.Policies.Load() == 0 {
		t.Fatal("authorization/session policy hook not invoked")
	}
	ws := me.Workspaces[0].ID
	for _, f := range []ext.Feature{ext.SAML, ext.OIDCWorkspace, ext.SCIM, ext.RBAC, ext.AuditStream, ext.HA} {
		if e.Hooks.Entitlements.Has(ctx, ws, f) {
			t.Fatalf("OSS entitled to %s", f)
		}
	}
	user, err := e.Hooks.Directory.UpsertUser(ctx, ext.DirectoryUser{Email: "provisioned@example.invalid", Name: "Provisioned"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := e.Hooks.Directory.UpsertUser(ctx, ext.DirectoryUser{Email: "PROVISIONED@example.invalid", Name: "Updated"})
	if err != nil || same != user {
		t.Fatal(same, err)
	}
	if err = e.Hooks.Directory.SetMembership(ctx, ws, user, "member"); err != nil {
		t.Fatal(err)
	}
	if err = e.Hooks.Directory.RemoveMembership(ctx, ws, user); err != nil {
		t.Fatal(err)
	}
	if err = e.Hooks.Directory.DeactivateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err = e.Hooks.Workspaces.SetPlan(ctx, ws, "pro"); err != nil {
		t.Fatal(err)
	}
	if err = e.Hooks.Workspaces.Suspend(ctx, ws, "test"); err != nil {
		t.Fatal(err)
	}
	if err = e.Hooks.Workspaces.Unsuspend(ctx, ws); err != nil {
		t.Fatal(err)
	}
	var plan string
	if err = a.DBs.Control.Reader.QueryRowContext(ctx, "SELECT plan FROM workspaces WHERE id=?", ws).Scan(&plan); err != nil || plan != "pro" {
		t.Fatal(plan, err)
	}
	want := map[string]bool{"directory.user.upsert": false, "directory.membership.set": false, "directory.membership.remove": false, "directory.user.deactivate": false, "workspace.plan": false, "workspace.suspend": false, "workspace.unsuspend": false}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(want) > 0 {
		select {
		case event := <-e.Events:
			var committed int
			if err = a.DBs.History.Reader.QueryRowContext(ctx, "SELECT count(*) FROM audit_log WHERE id=?", event.ID).Scan(&committed); err != nil || committed != 1 {
				t.Fatal("uncommitted event", err)
			}
			delete(want, event.Action)
		case <-deadline.C:
			t.Fatalf("missing audit events: %v", want)
		}
	}
}
