package app

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/internal/api"
)

// Public exceptions are exact method/path pairs, never a permissive prefix.
var publicActions = map[string]bool{
	"GET /healthz": true, "GET /readyz": true, "GET /version": true, "GET /robots.txt": true,
	"GET /": true, "GET /app": true, "GET /app/*": true,
	"GET /auth/{provider}/login": true, "GET /auth/{provider}/callback": true,
	"GET /auth/sso": true, "POST /auth/dev/login": true, "POST /auth/logout": true,
	"GET /api/v1/openapi.yaml": true, "GET /api/v1/features": true,
	"POST /api/v1/schedules/validate": true, "POST /api/v1/schedules/preview": true,
	"GET /api/v1/alert-channels/verify": true,
}

func missingActions(r chi.Routes, mcpPath string) []string {
	var missing []string
	_ = chi.Walk(r, func(method, path string, h http.Handler, _ ...func(http.Handler) http.Handler) error {
		// MCP transport authenticates a credential; individual tools/resources call
		// the common application Authorize boundary before invoking operations.
		if path == mcpPath || publicActions[method+" "+path] {
			return nil
		}
		if api.RouteAction(h) == "" {
			missing = append(missing, fmt.Sprintf("%s %s", method, path))
		}
		return nil
	})
	return missing
}
func TestEveryRouteDeclaresAction(t *testing.T) {
	f := newFixture(t)
	if missing := missingActions(f.a.router, f.a.Config.MCP.Path); len(missing) > 0 {
		t.Fatalf("routes without actions: %v", missing)
	}
	// Negative control: prove a newly mounted handler cannot bypass this test.
	f.a.router.Get("/api/v1/unprotected-test", func(http.ResponseWriter, *http.Request) {})
	if missing := missingActions(f.a.router, f.a.Config.MCP.Path); len(missing) != 1 || missing[0] != "GET /api/v1/unprotected-test" {
		t.Fatalf("coverage failed to reject new route: %v", missing)
	}
}
