package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/internal/auth"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

func (j *Jobs) route(r chi.Router, method, path string, action ext.Action, h http.HandlerFunc) {
	if action == "" {
		panic("API action required")
	}
	r.Method(method, path, actionHandler{Action: action, Handler: j.authorize(action, j.mutationPolicy(h))})
}
func (j *Jobs) authorize(action ext.Action, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resource := ext.Resource{WorkspaceID: who(r).WorkspaceID, ID: chi.URLParam(r, "id"), Kind: strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")[0]}
		// Preserve v1's scope rejection before resource lookup (including 404s).
		identity := who(r)
		if identity.KeyID != "" && !auth.ScopeAllowed(identity.Scopes, auth.RequiredScope(action, resource.Kind)) {
			writeError(w, 403, "forbidden", "operation not allowed", nil)
			return
		}
		if resource.ID == "" {
			resource.ID = chi.URLParam(r, "ws")
		}
		if resource.ID == "" {
			resource.ID = chi.URLParam(r, "name")
		}
		if action == ext.KeysManage {
			resource.OwnerID = who(r).UserID
			if resource.ID != "" {
				key, err := j.Store.ControlReader().GetKey(r.Context(), sqlitegen.GetKeyParams{ID: resource.ID, WorkspaceID: who(r).WorkspaceID})
				if err != nil {
					writeError(w, 404, "not_found", "key not found", nil)
					return
				}
				resource.OwnerID = key.CreatedBy.String
			}
		}
		if err := j.Authorize(r.Context(), action, resource); err != nil {
			writeError(w, 403, "forbidden", "operation not allowed", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Authorize is the common REST/MCP decision boundary, intersecting key scopes
// with the active workspace authorizer.
func (j *Jobs) Authorize(ctx context.Context, action ext.Action, r ext.Resource) error {
	i := auth.Current(ctx)
	p := ext.Principal{UserID: i.UserID, WorkspaceID: i.WorkspaceID, KeyID: i.KeyID, Scopes: i.Scopes, Role: "owner"}
	var registry *auth.Authorization
	if j.Auth != nil {
		var err error
		p, err = j.Auth.Principal(ctx)
		if err != nil {
			return err
		}
		registry = j.Auth.Authz
	}
	return registry.Authorize(ctx, p, action, r)
}

// actionHandler keeps the declaration attached to the actual registered handler.
// Replacing a route without authorization cannot inherit an old map entry.
type actionHandler struct {
	Action ext.Action
	http.Handler
}

// RouteAction unwraps chi middleware and returns a route's authorization declaration.
func RouteAction(h http.Handler) ext.Action {
	for {
		if chain, ok := h.(*chi.ChainHandler); ok {
			h = chain.Endpoint
			continue
		}
		break
	}
	if tagged, ok := h.(actionHandler); ok {
		return tagged.Action
	}
	return ""
}
