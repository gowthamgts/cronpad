package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

// DECISION: serialize create idempotency in this single-node server. SQLite records
// survive restarts; the lock covers the gap between a mutation and recording its response.
type mutationStateKey struct{}
type mutationState struct{ replayed bool }
type mutationWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *mutationWriter) WriteHeader(status int) {
	w.status = status

}
func (w *mutationWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.body.Write(b)
}
func (j *Jobs) mutations() func(http.Handler) http.Handler {
	var mu sync.Mutex
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" || r.Method == "HEAD" {
				next.ServeHTTP(w, r)
				return
			}
			if ws := who(r).WorkspaceID; ws != "" {
				suspended, err := store.Suspended(r.Context(), j.Store, ws)
				if err != nil {
					writeError(w, 500, "internal", "cannot read workspace state", nil)
					return
				}
				if suspended {
					writeError(w, 403, "workspace_suspended", "workspace is suspended", nil)
					return
				}
			}
			key := r.Header.Get("Idempotency-Key")
			cache := r.Method == "POST" && key != "" && r.URL.Path != "/api/v1/jobs" && r.URL.Path != "/api/v1/api-keys"
			hash := ""
			if len(key) > 200 {
				writeError(w, 400, "validation_failed", "idempotency key too long", nil)
				return
			}
			if cache {
				if j.Vault == nil {
					writeError(w, 503, "unavailable", "configure secrets.encryption_key for idempotency", nil)
					return
				}
				b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
				if e != nil {
					writeError(w, 400, "validation_failed", "body exceeds 1 MiB", nil)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(b))
				hash = fmt.Sprintf("%x", sha256.Sum256(append([]byte(r.Method+":"+r.URL.Path+":"), b...)))
				mu.Lock()
				defer mu.Unlock()
				record, e := j.Store.ControlReader().GetIdempotency(r.Context(), sqlitegen.GetIdempotencyParams{WorkspaceID: who(r).WorkspaceID, Key: key, ExpiresAt: j.Clock.Now().UnixMilli()})
				if e == nil {
					if record.RequestHash != hash {
						writeError(w, 409, "idempotency_conflict", "idempotency key was used with a different request", nil)
						return
					}
					body, e := j.Vault.Unwrap(record.Response, "idempotency:"+who(r).WorkspaceID+":"+key)
					if e != nil {
						writeError(w, 500, "internal", "cannot decrypt idempotency record", nil)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(int(record.Status))
					_, _ = io.WriteString(w, body)
					return
				}
				if !errors.Is(e, sql.ErrNoRows) {
					writeError(w, 500, "internal", "cannot read idempotency record", nil)
					return
				}
			}
			state := &mutationState{}
			ctx := context.WithValue(r.Context(), mutationStateKey{}, state)
			r = r.WithContext(ctx)
			mw := &mutationWriter{ResponseWriter: w}
			defer func() {
				if mw.status == 0 {
					mw.status = 200
				}
				w.WriteHeader(mw.status)
				_, _ = w.Write(mw.body.Bytes())
			}()
			next.ServeHTTP(mw, r)
			if mw.status < 200 || mw.status >= 300 || state.replayed {
				return
			}
			if cache {
				body, e := j.Vault.Wrap(mw.body.String(), "idempotency:"+who(r).WorkspaceID+":"+key)
				if e == nil {
					e = j.Store.Control().SaveIdempotency(ctx, sqlitegen.SaveIdempotencyParams{WorkspaceID: who(r).WorkspaceID, Key: key, RequestHash: hash, Response: body, Status: int64(mw.status), ExpiresAt: j.Clock.Now().Add(24 * time.Hour).UnixMilli()})
				}
				if e != nil {
					mw.body.Reset()
					writeError(mw, 500, "internal", "cannot save idempotency record", nil)
					return
				}
			}
			path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
			parts := strings.Split(path, "/")
			action, target := "", ""
			switch parts[0] {
			case "jobs":
				action = "job."
				if len(parts) > 2 {
					action += strings.ReplaceAll(parts[2], "-", "_")
				} else {
					switch r.Method {
					case "POST":
						action += "create"
					case "PATCH":
						action += "update"
					case "DELETE":
						action += "delete"
					}
				}
				target = chi.URLParam(r, "id")
				if strings.HasSuffix(action, "test") {
					return
				}
			case "runs":
				action = "job.retry"
				target = chi.URLParam(r, "id")
			case "api-keys":
				action = "api_key.create"
				if r.Method == "DELETE" {
					action = "api_key.revoke"
				}
				target = chi.URLParam(r, "id")
			case "alert-channels", "alert-rules":
				action = strings.ReplaceAll(parts[0], "-", "_") + "."
				switch r.Method {
				case "POST":
					action += "create"
				case "PATCH":
					action += "update"
				case "DELETE":
					action += "delete"
				}
				if len(parts) > 2 {
					action = strings.ReplaceAll(parts[0], "-", "_") + "." + parts[2]
				}
				target = chi.URLParam(r, "id")
			case "secrets":
				action = "secret.update"
				if r.Method == "DELETE" {
					action = "secret.delete"
				}
				target = chi.URLParam(r, "name")
			case "workspaces":
				action = "workspace.patch"
				target = chi.URLParam(r, "ws")
			}
			if action != "" {
				if target == "" {
					var out struct {
						ID string `json:"id"`
					}
					_ = json.Unmarshal(mw.body.Bytes(), &out)
					target = out.ID
				}
				if e := j.recordAudit(r, action, target); e != nil {
					mw.body.Reset()
					writeError(mw, 500, "internal", "cannot record audit event", nil)
				}
			}
		})
	}
}
