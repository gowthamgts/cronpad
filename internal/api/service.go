package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/internal/auth"
)

type mcpKey struct{}

// AuthLimiter is shared by the REST API and MCP transport, including discovery.
func (j *Jobs) AuthLimiter() func(http.Handler) http.Handler {
	if j.Auth != nil {
		return j.Auth.RateLimit(j.Config.Limits.APIRateLimitPerMinute)
	}
	return func(h http.Handler) http.Handler { return h }
}

// Call executes an authenticated application operation through the same handlers
// and mutation middleware as REST. No network request or duplicate business logic.
// DECISION: retain the existing handler-based service boundary, using an in-memory
// response adapter rather than moving all API logic for the MCP server.
func (j *Jobs) Call(ctx context.Context, method, path string, body json.RawMessage) (json.RawMessage, error) {
	identity := auth.Current(ctx)
	if identity.KeyID == "" {
		return nil, fmt.Errorf("forbidden: API key scope does not allow this operation")
	}
	ctx = context.WithValue(ctx, mcpKey{}, true)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, chi.NewRouteContext())
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("operation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	// A fresh request has no chi route context from the outer MCP route.
	j.operations.ServeHTTP(w, req)
	if w.Code >= 400 {
		return nil, fmt.Errorf("%s", w.Body.String())
	}
	if w.Body.Len() == 0 {
		return json.RawMessage(`{"ok":true}`), nil
	}
	var out any
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("operation response: %w", err)
	}
	redactMCP(out)
	return json.Marshal(out)
}

// Secret material returned once by REST must never be forwarded to a model.
func redactMCP(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, x := range v {
			if key == "signing_secret" || key == "previous_signing_secret" || key == "signing_secrets" || key == "secret" || key == "key" {
				delete(v, key)
			} else if key == "headers" || key == "env" {
				if values, ok := x.(map[string]any); ok {
					for name := range values {
						values[name] = "[redacted]"
					}
				}
			} else if key == "body" {
				v[key] = "[redacted]"
			} else if key == "url" {
				if raw, ok := x.(string); ok {
					if parsed, err := url.Parse(raw); err == nil {
						parsed.User = nil
						q := parsed.Query()
						for name := range q {
							q.Set(name, "[redacted]")
						}
						parsed.RawQuery = q.Encode()
						v[key] = parsed.String()
					}
				}
			} else if strings.Contains(strings.ToLower(key), "password") || strings.Contains(strings.ToLower(key), "token") {
				v[key] = "[redacted]"
			} else {
				redactMCP(x)
			}
		}
	case []any:
		for _, x := range v {
			redactMCP(x)
		}
	}
}
