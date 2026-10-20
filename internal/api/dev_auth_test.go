package api

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/gowthamgts/cronpad/internal/auth"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

// DECISION: development-only identities are deterministic per X-Dev-User, with a
// personal workspace each. This is deliberately not a trusted production identity.
func DevAuth(s store.Store, clk clockwork.Clock, enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !enabled {
				writeError(w, 401, "unauthorized", "authentication required", nil)
				return
			}
			name := r.Header.Get("X-Dev-User")
			if name == "" {
				name = "developer"
			}
			if len(name) > 200 || strings.ContainsAny(name, "\r\n") {
				writeError(w, 400, "validation_failed", "invalid development user", nil)
				return
			}
			sum := sha256.Sum256([]byte(name))
			suffix := fmt.Sprintf("%x", sum[:16])
			who := auth.Identity{UserID: "usr_" + suffix, WorkspaceID: "ws_" + suffix, Scopes: []string{"*"}}
			ctx := r.Context()
			e := s.WithTx(ctx, store.Control, func(tx store.Tx) error {
				now := clk.Now().UnixMilli()
				if e := tx.Control.SeedDevUser(ctx, sqlitegen.SeedDevUserParams{ID: who.UserID, Email: suffix + "@dev.invalid", Name: sql.NullString{String: name, Valid: true}, CreatedAt: now}); e != nil {
					return e
				}
				if e := tx.Control.SeedDevWorkspace(ctx, sqlitegen.SeedDevWorkspaceParams{ID: who.WorkspaceID, Name: name + "'s workspace", Slug: suffix, CreatedAt: now}); e != nil {
					return e
				}
				return tx.Control.SeedDevMember(ctx, sqlitegen.SeedDevMemberParams{WorkspaceID: who.WorkspaceID, UserID: who.UserID})
			})
			if e != nil {
				writeError(w, 500, "internal", "cannot initialize development identity", nil)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), who)))
		})
	}
}
