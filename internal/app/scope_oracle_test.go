package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gowthamgts/cronpad/internal/api"
	"github.com/gowthamgts/cronpad/internal/auth"
	"github.com/gowthamgts/cronpad/internal/mcpserver"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

// v1ScopeOracle preserves the pre-N2 auth.Allowed policy independently of the
// production action/resource mapping. Public routes never called that policy.
func v1ScopeOracle(scopes []string, method, path string) bool {
	required := "*"
	if strings.HasPrefix(path, "/api/v1/jobs") {
		required = "jobs:write"
		if method == "GET" {
			required = "jobs:read"
		}
		if strings.HasSuffix(path, "/runs") {
			required = "runs:read"
		}
	} else if strings.HasPrefix(path, "/api/v1/runs") {
		required = "runs:read"
		if method != "GET" {
			required = "jobs:write"
		}
	}
	for _, scope := range scopes {
		if scope == "*" || scope == required {
			return true
		}
	}
	return false
}
func representativeScopes() [][]string {
	return [][]string{{"jobs:read"}, {"jobs:write"}, {"runs:read"}, {"jobs:read", "runs:read"}, {"*"}, {}, {"jobs:read", "jobs:write", "runs:read"}, {"workspace:admin", "audit:read", "jobs:trigger"}}
}

func TestEveryProtectedRouteMatchesV1ScopeOracle(t *testing.T) {
	f := newFixture(t)
	scopeSets := representativeScopes()
	keys := make([]string, len(scopeSets))
	for n, scopes := range scopeSets {
		keys[n] = mcpKey(t, f, "scope-oracle", scopes)
	}
	operations := &api.Jobs{}
	err := chi.Walk(f.a.router, func(method, path string, h http.Handler, _ ...func(http.Handler) http.Handler) error {
		action := api.RouteAction(h)
		if action == "" {
			return nil
		} // Existing exact public-action coverage checks these separately.
		kind := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")[0]
		for n, scopes := range scopeSets {
			t.Run(fmt.Sprintf("%s %s/%v", method, path, scopes), func(t *testing.T) {
				ctx := auth.WithIdentity(context.Background(), auth.Identity{WorkspaceID: "workspace", UserID: "owner", KeyID: "key", Scopes: scopes})
				allowed := operations.Authorize(ctx, action, ext.Resource{WorkspaceID: "workspace", Kind: kind, OwnerID: "owner"}) == nil
				want := v1ScopeOracle(scopes, method, path)
				if allowed != want {
					t.Fatalf("action=%s kind=%s allowed=%v v1=%v", action, kind, allowed, want)
				}
				if !want {
					requestPath := strings.NewReplacer("{id}", "missing", "{ws}", "missing", "{name}", "MISSING").Replace(path)
					req := httptest.NewRequestWithContext(t.Context(), method, requestPath, strings.NewReader(`{}`))
					req.Header.Set("Authorization", "Bearer "+keys[n])
					req.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					f.a.Handler.ServeHTTP(response, req)
					if response.Code != 403 {
						t.Fatalf("credential denial happened after business logic: status=%d body=%s", response.Code, response.Body.String())
					}
				}
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// scopeProbe uses the production API authorization boundary and records the REST
// operation actually reached by each tool, without invoking database mutations.
type scopeProbe struct {
	scopes       []string
	method, path string
	called       bool
}

func (s *scopeProbe) Authorize(ctx context.Context, a ext.Action, r ext.Resource) error {
	ctx = auth.WithIdentity(ctx, auth.Identity{WorkspaceID: "workspace", UserID: "owner", KeyID: "key", Scopes: s.scopes})
	return (&api.Jobs{}).Authorize(ctx, a, r)
}
func (s *scopeProbe) Call(_ context.Context, method, path string, _ json.RawMessage) (json.RawMessage, error) {
	s.method = method
	s.path = path
	s.called = true
	return json.RawMessage(`{"stats_24h":{},"runs":{}}`), nil
}

func TestEveryMCPToolMatchesUnderlyingV1ScopeOracle(t *testing.T) {
	cases := map[string]struct {
		method, path string
		args         map[string]any
	}{
		"validate_schedule": {"POST", "/api/v1/schedules/validate", map[string]any{"expr": "every hour"}},
		"preview_schedule":  {"POST", "/api/v1/schedules/preview", map[string]any{"expr": "every hour"}},
		"list_jobs":         {"GET", "/api/v1/jobs", map[string]any{}},
		"get_job":           {"GET", "/api/v1/jobs/job_test", map[string]any{"job_id": "job_test"}},
		"create_job":        {"POST", "/api/v1/jobs", map[string]any{"name": "test", "schedule": map[string]any{"expr": "every hour"}, "sink": map[string]any{"kind": "webhook", "config": map[string]any{"url": "https://example.com"}}}},
		"update_job":        {"PATCH", "/api/v1/jobs/job_test", map[string]any{"job_id": "job_test", "name": "updated"}},
		"pause_job":         {"POST", "/api/v1/jobs/job_test/pause", map[string]any{"job_id": "job_test"}},
		"resume_job":        {"POST", "/api/v1/jobs/job_test/resume", map[string]any{"job_id": "job_test"}},
		"trigger_job":       {"POST", "/api/v1/jobs/job_test/trigger", map[string]any{"job_id": "job_test"}},
		"delete_job":        {"DELETE", "/api/v1/jobs/job_test", map[string]any{"job_id": "job_test"}},
		"list_runs":         {"GET", "/api/v1/jobs/job_test/runs", map[string]any{"job_id": "job_test"}},
		"get_run":           {"GET", "/api/v1/runs/run_test", map[string]any{"run_id": "run_test"}},
		"get_stats":         {"GET", "/api/v1/stats/overview", map[string]any{}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probe := &scopeProbe{}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := mcpserver.New(probe).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "scope-oracle", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		tc, ok := cases[tool.Name]
		if !ok {
			t.Fatalf("tool has no v1 scope oracle: %s", tool.Name)
		}
		variants := []struct {
			method, path string
			args         map[string]any
		}{tc}
		if tool.Name == "get_stats" {
			variants = append(variants, struct {
				method, path string
				args         map[string]any
			}{"GET", "/api/v1/jobs/job_test", map[string]any{"job_id": "job_test"}})
		}
		for _, tc := range variants {
			for _, scopes := range representativeScopes() {
				t.Run(fmt.Sprintf("%s/%s/%v", tool.Name, tc.path, scopes), func(t *testing.T) {
					probe.scopes = scopes
					probe.called = false
					result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: tc.args})
					if err != nil {
						t.Fatal(err)
					}
					// v1's MCP adapter exempted the same public schedule utilities as REST.
					want := strings.HasPrefix(tc.path, "/api/v1/schedules/") || v1ScopeOracle(scopes, tc.method, tc.path)
					if !result.IsError != want || probe.called != want {
						t.Fatalf("allowed=%v called=%v v1=%v result=%v", !result.IsError, probe.called, want, result)
					}
					if want && (probe.method != tc.method || probe.path != tc.path) {
						t.Fatalf("operation=%s %s expected=%s %s", probe.method, probe.path, tc.method, tc.path)
					}
				})
			}
		}
	}
}
