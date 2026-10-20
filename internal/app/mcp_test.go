package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gowthamgts/cronpad/internal/auth"
	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/mcpserver"
	"github.com/gowthamgts/cronpad/internal/store"
)

func mcpKey(t *testing.T, f *fixture, user string, scopes []string) string {
	t.Helper()
	s := auth.New(context.Background(), store.New(f.a.DBs), f.a.deps.Clock, f.a.Config, nil)
	u, e := s.Account(context.Background(), "dev", auth.Profile{Subject: user, Email: user + "@example.com", Name: user, Verified: true})
	if e != nil {
		t.Fatal(e)
	}
	ws, e := s.Store.ControlReader().PersonalWorkspace(context.Background(), u.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, raw, e := s.MintKey(context.Background(), ws.ID, u.ID, "mcp", scopes)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func mcpConnect(t *testing.T, endpoint, key string) *mcp.ClientSession {
	t.Helper()
	session, e := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: mcpserver.HTTPClient(key), DisableStandaloneSSE: true}, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
func mcpCall(t *testing.T, s *mcp.ClientSession, name string, args any, wantError string) map[string]any {
	t.Helper()
	r, e := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(r)
	if wantError != "" {
		if !r.IsError || !strings.Contains(string(b), wantError) {
			t.Fatalf("%s: %s", name, b)
		}
		return nil
	}
	if r.IsError {
		t.Fatalf("%s: %s", name, b)
	}
	var v map[string]any
	data, _ := json.Marshal(r.StructuredContent)
	if e = json.Unmarshal(data, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func TestMCPFlowAndIsolation(t *testing.T) {
	f := newFixture(t)
	key := mcpKey(t, f, "mcp-alice", []string{"*"})
	server := httptest.NewServer(f.a.Handler)
	defer server.Close()
	s := mcpConnect(t, server.URL+"/mcp", key)
	tools, e := s.ListTools(context.Background(), nil)
	if e != nil || len(tools.Tools) != 13 {
		t.Fatalf("tools: %+v %v", tools, e)
	}
	for _, tool := range tools.Tools {
		if !strings.Contains(tool.Description, "Example:") {
			t.Errorf("missing example %s", tool.Name)
		}
		if tool.Name == "delete_job" || tool.Name == "pause_job" {
			if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Errorf("missing destructive hint %s", tool.Name)
			}
		}
		if tool.Name == "get_run" && !tool.Annotations.ReadOnlyHint {
			t.Error("missing readonly hint")
		}
	}
	resources, e := s.ListResources(context.Background(), nil)
	if e != nil || len(resources.Resources) != 0 {
		t.Fatalf("resources: %v %v", resources, e)
	}
	templates, e := s.ListResourceTemplates(context.Background(), nil)
	if e != nil || len(templates.ResourceTemplates) != 2 {
		t.Fatalf("templates %v %v", templates, e)
	}
	prompts, e := s.ListPrompts(context.Background(), nil)
	if e != nil || len(prompts.Prompts) != 1 {
		t.Fatalf("prompts %v %v", prompts, e)
	}
	schedule := map[string]any{"expr": "every day at 9am", "timezone": "Asia/Kolkata"}
	if v := mcpCall(t, s, "validate_schedule", schedule, ""); v["valid"] != true {
		t.Fatal(v)
	}
	mcpCall(t, s, "preview_schedule", schedule, "")
	job := map[string]any{"name": "mcp-report", "schedule": schedule, "sink": map[string]any{"kind": "webhook", "config": map[string]any{"url": "https://example.com/hook?token=literal-secret", "headers": map[string]any{"Authorization": "Bearer literal-secret"}}}}
	created := mcpCall(t, s, "create_job", job, "")
	encodedCreated, _ := json.Marshal(created)
	if strings.Contains(string(encodedCreated), "literal-secret") {
		t.Fatal("literal sink credential leaked")
	}
	id := created["id"].(string)
	if _, ok := created["signing_secret"]; ok {
		t.Fatal("secret leaked")
	}
	trigger := mcpCall(t, s, "trigger_job", map[string]any{"job_id": id}, "")
	runID, _ := trigger["run_id"].(string)
	if runID == "" {
		t.Fatal(trigger)
	}
	mcpCall(t, s, "get_run", map[string]any{"run_id": runID}, "")
	mcpCall(t, s, "list_runs", map[string]any{"job_id": id}, "")
	mcpCall(t, s, "get_stats", map[string]any{"job_id": id}, "")
	mcpCall(t, s, "get_stats", map[string]any{}, "")
	resource, e := s.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "cronpad://jobs/" + id})
	if e != nil || !strings.Contains(resource.Contents[0].Text, id) {
		t.Fatalf("resource %v %v", resource, e)
	}
	if _, e = s.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "debug_failing_job", Arguments: map[string]string{"job_id": id}}); e != nil {
		t.Fatal(e)
	}
	read := mcpConnect(t, server.URL+"/mcp", mcpKey(t, f, "mcp-alice", []string{"jobs:read"}))
	mcpCall(t, read, "get_job", map[string]any{"job_id": id}, "")
	mcpCall(t, read, "validate_schedule", schedule, "")
	mcpCall(t, read, "create_job", job, "forbidden")
	mcpCall(t, read, "list_runs", map[string]any{"job_id": id, "limit": 10}, "forbidden")
	other := mcpConnect(t, server.URL+"/mcp", mcpKey(t, f, "mcp-bob", []string{"*"}))
	mcpCall(t, other, "get_job", map[string]any{"job_id": id}, "not_found")
	mcpCall(t, other, "get_run", map[string]any{"run_id": runID}, "not_found")
	if _, e = other.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "cronpad://jobs/" + id}); e == nil {
		t.Fatal("cross-workspace resource leaked")
	}
	rows, e := f.a.deps.History.QueryContext(context.Background(), "SELECT actor,details FROM audit_log WHERE target = ?", id)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = rows.Close() }()
	found := false
	for rows.Next() {
		var actor, details string
		if e = rows.Scan(&actor, &details); e != nil {
			t.Fatal(e)
		}
		if strings.HasPrefix(actor, "key:") && strings.Contains(details, "mcp") {
			found = true
		}
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	if !found {
		t.Error("missing MCP audit marker")
	}
	mcpCall(t, s, "update_job", map[string]any{"job_id": id, "timezone": "UTC", "sla_threshold": nil}, "")
	mcpCall(t, s, "pause_job", map[string]any{"job_id": id}, "")
	mcpCall(t, s, "resume_job", map[string]any{"job_id": id}, "")
	mcpCall(t, s, "delete_job", map[string]any{"job_id": id}, "")
}
func TestTransportAuthDisabledAndPath(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		f := newFixtureWith(t, func(c *config.Config) { c.MCP.Enabled = enabled; c.MCP.Path = "/agent" })
		req := httptest.NewRequestWithContext(t.Context(), "POST", "/agent", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		f.a.Handler.ServeHTTP(w, req)
		want := 404
		if enabled {
			want = 401
		}
		if w.Code != want {
			t.Fatalf("enabled=%v: %d", enabled, w.Code)
		}
	}
}
func TestStdioProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("builds subprocess binary")
	}
	f := newFixture(t)
	key := mcpKey(t, f, "proxy", []string{"*"})
	server := httptest.NewServer(f.a.Handler)
	defer server.Close()
	binary := filepath.Join(t.TempDir(), "cronpad")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/cronpad")
	if output, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "mcp", "--url", server.URL)
	command.Env = append(os.Environ(), "CRONPAD_API_KEY="+key)
	session, e := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = session.Close() }()
	mcpCall(t, session, "validate_schedule", map[string]any{"expr": "every day at 9am"}, "")
	if _, e = session.ListResourceTemplates(ctx, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = session.ListPrompts(ctx, nil); e != nil {
		t.Fatal(e)
	}
}

func TestSharedRateLimit(t *testing.T) {
	f := newFixtureWith(t, func(c *config.Config) { c.Limits.APIRateLimitPerMinute = 2 })
	key := mcpKey(t, f, "rate", []string{"*"})
	for _, path := range []string{"/mcp", "/mcp", "/api/v1/jobs"} {
		req := httptest.NewRequestWithContext(t.Context(), "GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		f.a.Handler.ServeHTTP(w, req)
		if path == "/api/v1/jobs" && (w.Code != 429 || w.Header().Get("RateLimit-Limit") == "") {
			t.Fatalf("REST/MCP limit not shared: %d %s", w.Code, w.Body.String())
		}
	}
}
