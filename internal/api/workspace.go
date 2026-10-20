package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gowthamgts/cronpad/internal/store"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/pkg/ext"

	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

func readInput(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		writeError(w, 400, "validation_failed", "invalid JSON", nil)
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		writeError(w, 400, "validation_failed", "expected one JSON object", nil)
		return false
	}
	return true
}
func limitError(w http.ResponseWriter, e error) bool {
	var l *limits.Error
	if errors.As(e, &l) {
		status := 403
		if l.Limit == "max_runs_per_month_per_workspace" {
			status = 429
		}
		writeJSON(w, status, map[string]any{"error": map[string]any{"code": "limit_exceeded", "limit": l.Limit, "value": l.Value}})
		return true
	}
	if e != nil {
		writeError(w, 500, "internal", "cannot evaluate limit", nil)
		return true
	}
	return false
}
func (j *Jobs) mountWorkspace(r chi.Router) {
	j.route(r, "GET", "/api/v1/me", ext.JobsRead, j.me)
	j.route(r, "GET", "/api/v1/workspaces/{ws}", ext.JobsRead, j.workspace)
	j.route(r, "PATCH", "/api/v1/workspaces/{ws}", ext.WorkspaceAdminAction, j.patchWorkspace)
	j.route(r, "GET", "/api/v1/api-keys", ext.KeysManage, j.keys)
	j.route(r, "POST", "/api/v1/api-keys", ext.KeysManage, j.createKey)
	j.route(r, "DELETE", "/api/v1/api-keys/{id}", ext.KeysManage, j.revokeKey)
	j.route(r, "GET", "/api/v1/secrets", ext.SecretsWrite, j.secrets)
	j.route(r, "PUT", "/api/v1/secrets/{name}", ext.SecretsWrite, j.putSecret)
	j.route(r, "DELETE", "/api/v1/secrets/{name}", ext.SecretsWrite, j.deleteSecret)
	j.route(r, "GET", "/api/v1/stats/overview", ext.JobsRead, j.overview)
	j.route(r, "GET", "/api/v1/audit-log", ext.AuditRead, j.auditLog)
	j.mountAlerts(r)
}
func workspaceObject(ws sqlitegen.Workspace) map[string]any {
	return map[string]any{"id": ws.ID, "name": ws.Name, "slug": ws.Slug, "plan": ws.Plan, "created_at": time.UnixMilli(ws.CreatedAt).UTC()}
}
func (j *Jobs) me(w http.ResponseWriter, r *http.Request) {
	i := who(r)
	user, e := j.Store.ControlReader().UserByID(r.Context(), i.UserID)
	if e != nil {
		writeError(w, 404, "not_found", "user not found", nil)
		return
	}
	workspaces, e := j.Store.ControlReader().UserWorkspaces(r.Context(), i.UserID)
	if e != nil {
		writeError(w, 500, "internal", "cannot read workspaces", nil)
		return
	}
	data := []any{}
	for _, ws := range workspaces {
		if i.KeyID == "" || ws.ID == i.WorkspaceID {
			data = append(data, workspaceObject(ws))
		}
	}
	writeJSON(w, 200, map[string]any{"id": user.ID, "email": user.Email, "name": stringPtr(user.Name), "workspaces": data})
}
func (j *Jobs) workspace(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "ws") != who(r).WorkspaceID {
		writeError(w, 404, "not_found", "workspace not found", nil)
		return
	}
	ws, e := j.Store.ControlReader().GetWorkspace(r.Context(), who(r).WorkspaceID)
	if e != nil {
		writeError(w, 404, "not_found", "workspace not found", nil)
		return
	}
	writeJSON(w, 200, workspaceObject(ws))
}
func (j *Jobs) patchWorkspace(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "ws") != who(r).WorkspaceID {
		writeError(w, 404, "not_found", "workspace not found", nil)
		return
	}
	old, e := j.Store.ControlReader().GetWorkspace(r.Context(), who(r).WorkspaceID)
	if e != nil {
		writeError(w, 404, "not_found", "workspace not found", nil)
		return
	}
	in := struct{ Name, Slug string }{old.Name, old.Slug}
	if !readInput(w, r, &in) {
		return
	}
	if len(in.Name) < 1 || len(in.Name) > 200 || !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`).MatchString(in.Slug) {
		writeError(w, 400, "validation_failed", "invalid name or slug", nil)
		return
	}
	ws, e := j.Store.Control().PatchWorkspace(r.Context(), sqlitegen.PatchWorkspaceParams{Name: in.Name, Slug: in.Slug, ID: old.ID})
	if e != nil {
		writeError(w, 409, "conflict", "workspace slug unavailable", nil)
		return
	}
	writeJSON(w, 200, workspaceObject(ws))
}
func (j *Jobs) keys(w http.ResponseWriter, r *http.Request) {
	n, e := page(r)
	if e != nil {
		writeError(w, 400, "validation_failed", e.Error(), nil)
		return
	}
	rows, e := j.Store.ControlReader().ListKeys(r.Context(), sqlitegen.ListKeysParams{WorkspaceID: who(r).WorkspaceID, Cursor: r.URL.Query().Get("cursor"), PageSize: int64(n + 1)})
	if e != nil {
		writeError(w, 500, "internal", "cannot list keys", nil)
		return
	}
	var cursor *string
	if len(rows) > n {
		cursor = &rows[n-1].ID
		rows = rows[:n]
	}
	data := []any{}
	for _, k := range rows {
		principal, err := j.Auth.Principal(r.Context())
		if err != nil {
			writeError(w, 403, "forbidden", "operation not allowed", nil)
			return
		}
		if j.Auth.Authz.Authorize(r.Context(), principal, ext.KeysManage, ext.Resource{WorkspaceID: who(r).WorkspaceID, OwnerID: k.CreatedBy.String}) != nil {
			continue
		}

		var scopes []string
		_ = json.Unmarshal([]byte(k.Scopes), &scopes)
		data = append(data, map[string]any{"id": k.ID, "name": k.Name, "prefix": k.Prefix, "scopes": scopes, "created_at": time.UnixMilli(k.CreatedAt).UTC(), "last_used_at": ptrTime(k.LastUsedAt), "revoked_at": ptrTime(k.RevokedAt)})
	}
	writeJSON(w, 200, map[string]any{"data": data, "next_cursor": cursor})
}
func (j *Jobs) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if !readInput(w, r, &in) {
		return
	}
	if len(in.Name) < 1 || len(in.Name) > 200 {
		writeError(w, 400, "validation_failed", "name required (max 200)", nil)
		return
	}
	if len(in.Scopes) == 0 {
		in.Scopes = []string{"*"}
	}
	for _, s := range in.Scopes {
		if s != "*" && s != "jobs:read" && s != "jobs:write" && s != "runs:read" {
			writeError(w, 400, "validation_failed", "invalid scope", nil)
			return
		}
	}

	workspace := who(r).WorkspaceID
	params, raw, e := j.Auth.PrepareKey(workspace, who(r).UserID, in.Name, in.Scopes)
	if e != nil {
		writeError(w, 500, "internal", "cannot generate key", nil)
		return
	}
	encoded, _ := json.Marshal(in)
	hash := fmt.Sprintf("%x", sha256.Sum256(append([]byte("POST:/api/v1/api-keys:"), encoded...)))
	idem := r.Header.Get("Idempotency-Key")
	var out map[string]any
	var cached *sqlitegen.IdempotencyRecord
	now := j.Clock.Now()
	ctx := r.Context()
	e = j.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		if idem != "" {
			if j.Vault == nil {
				return fmt.Errorf("encryption key required")
			}
			record, e := tx.Control.GetIdempotency(ctx, sqlitegen.GetIdempotencyParams{WorkspaceID: workspace, Key: idem, ExpiresAt: now.UnixMilli()})
			if e == nil {
				cached = &record
				return nil
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		n, e := tx.Control.CountKeys(ctx, workspace)
		if e != nil {
			return e
		}
		if j.Limits != nil {
			if e = j.Limits.Check(ctx, workspace, limits.Action{Limit: "max_api_keys_per_workspace", Value: n + 1}); e != nil {
				return e
			}
		}
		key, e := tx.Control.CreateKey(ctx, params)
		if e != nil {
			return e
		}
		out = map[string]any{"id": key.ID, "name": key.Name, "prefix": key.Prefix, "scopes": in.Scopes, "key": raw, "created_at": time.UnixMilli(key.CreatedAt).UTC()}
		if idem != "" {
			body, e := json.Marshal(out)
			if e != nil {
				return e
			}
			enc, e := j.Vault.Wrap(string(body), "idempotency:"+workspace+":"+idem)
			if e != nil {
				return e
			}
			return tx.Control.SaveIdempotency(ctx, sqlitegen.SaveIdempotencyParams{WorkspaceID: workspace, Key: idem, RequestHash: hash, Response: enc, Status: 201, ExpiresAt: now.Add(24 * time.Hour).UnixMilli()})
		}
		return nil
	})
	if e != nil {
		limitError(w, e)
		return
	}
	if cached != nil {
		if state, ok := r.Context().Value(mutationStateKey{}).(*mutationState); ok {
			state.replayed = true
		}
		if cached.RequestHash != hash {
			writeError(w, 409, "idempotency_conflict", "idempotency key was used with a different request", nil)
			return
		}
		body, e := j.Vault.Unwrap(cached.Response, "idempotency:"+workspace+":"+idem)
		if e != nil {
			writeError(w, 500, "internal", "cannot decrypt idempotency record", nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(cached.Status))
		_, _ = io.WriteString(w, body)
		return
	}
	writeJSON(w, 201, out)
}
func (j *Jobs) revokeKey(w http.ResponseWriter, r *http.Request) {
	n, e := j.Store.Control().RevokeKey(r.Context(), sqlitegen.RevokeKeyParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID, Now: sql.NullInt64{Int64: j.Clock.Now().UnixMilli(), Valid: true}})
	if e != nil {
		writeError(w, 500, "internal", "cannot revoke key", nil)
		return
	}
	if n == 0 {
		writeError(w, 404, "not_found", "key not found", nil)
		return
	}
	w.WriteHeader(204)
}

var validSecretName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func (j *Jobs) secrets(w http.ResponseWriter, r *http.Request) {
	n, e := page(r)
	if e != nil {
		writeError(w, 400, "validation_failed", e.Error(), nil)
		return
	}
	rows, e := j.Store.ControlReader().ListSecrets(r.Context(), sqlitegen.ListSecretsParams{WorkspaceID: who(r).WorkspaceID, Cursor: r.URL.Query().Get("cursor"), PageSize: int64(n + 1)})
	if e != nil {
		writeError(w, 500, "internal", "cannot list secrets", nil)
		return
	}
	var cursor *string
	if len(rows) > n {
		cursor = &rows[n-1].Name
		rows = rows[:n]
	}
	data := []any{}
	for _, s := range rows {
		data = append(data, map[string]any{"name": s.Name, "created_at": time.UnixMilli(s.CreatedAt).UTC(), "updated_at": time.UnixMilli(s.UpdatedAt).UTC()})
	}
	writeJSON(w, 200, map[string]any{"data": data, "next_cursor": cursor})
}
func (j *Jobs) putSecret(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var in struct {
		Value *string `json:"value"`
	}
	if !readInput(w, r, &in) {
		return
	}
	if !validSecretName.MatchString(name) || in.Value == nil || len(*in.Value) > 8192 {
		writeError(w, 400, "validation_failed", "invalid secret name or value (max 8 KiB)", nil)
		return
	}
	if j.Vault == nil {
		writeError(w, 503, "unavailable", "configure secrets.encryption_key", nil)
		return
	}
	workspace := who(r).WorkspaceID
	b, e := j.Vault.Seal([]byte(*in.Value), workspace+":"+name)
	if e != nil {
		writeError(w, 500, "internal", "cannot encrypt secret", nil)
		return
	}
	now := j.Clock.Now().UnixMilli()
	if e = j.Store.Control().PutSecret(r.Context(), sqlitegen.PutSecretParams{WorkspaceID: workspace, Name: name, Ciphertext: b, CreatedAt: now, UpdatedAt: now}); e != nil {
		writeError(w, 500, "internal", "cannot save secret", nil)
		return
	}
	w.WriteHeader(204)
}
func (j *Jobs) deleteSecret(w http.ResponseWriter, r *http.Request) {
	n, e := j.Store.Control().DeleteSecret(r.Context(), sqlitegen.DeleteSecretParams{WorkspaceID: who(r).WorkspaceID, Name: chi.URLParam(r, "name")})
	if e != nil {
		writeError(w, 500, "internal", "cannot delete secret", nil)
		return
	}
	if n == 0 {
		writeError(w, 404, "not_found", "secret not found", nil)
		return
	}
	w.WriteHeader(204)
}
func (j *Jobs) overview(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	windows := map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}
	duration, ok := windows[window]
	if !ok {
		writeError(w, 400, "validation_failed", "invalid window", nil)
		return
	}
	ws := who(r).WorkspaceID
	states, e := j.Store.ControlReader().WorkspaceJobStates(r.Context(), ws)
	if e != nil {
		writeError(w, 500, "internal", "cannot read stats", nil)
		return
	}
	jobs := map[string]int64{"active": 0, "paused": 0, "disabled": 0, "completed": 0}
	for _, s := range states {
		jobs[s.State] = s.N
	}
	runs, lags, e := j.retainedStats(r.Context(), ws, "", duration)
	if e != nil {
		writeError(w, 500, "internal", "cannot read stats", nil)
		return
	}
	failing, e := j.Store.ControlReader().FailingJobs(r.Context(), ws)
	if e != nil {
		writeError(w, 500, "internal", "cannot read stats", nil)
		return
	}
	if failing == nil {
		failing = []sqlitegen.FailingJobsRow{}
	}
	writeJSON(w, 200, map[string]any{"window": window, "jobs": jobs, "runs": runs, "start_lag_ms": lags, "failing_jobs": failing})
}
func (j *Jobs) auditLog(w http.ResponseWriter, r *http.Request) {
	n, e := page(r)
	if e != nil {
		writeError(w, 400, "validation_failed", e.Error(), nil)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor == "" {
		cursor = "~"
	}
	rows, e := j.Store.HistoryReader().ListAudit(r.Context(), historygen.ListAuditParams{WorkspaceID: who(r).WorkspaceID, Action: r.URL.Query().Get("action"), Cursor: cursor, PageSize: int64(n + 1)})
	if e != nil {
		writeError(w, 500, "internal", "cannot list audit log", nil)
		return
	}
	var next *string
	if len(rows) > n {
		next = &rows[n-1].ID
		rows = rows[:n]
	}
	data := []any{}
	for _, row := range rows {
		var details any
		_ = json.Unmarshal([]byte(row.Details.String), &details)
		data = append(data, map[string]any{"id": row.ID, "actor": row.Actor, "action": row.Action, "target": stringPtr(row.Target), "details": details, "created_at": time.UnixMilli(row.CreatedAt).UTC()})
	}
	writeJSON(w, 200, map[string]any{"data": data, "next_cursor": next})
}
func (j *Jobs) recordAudit(r *http.Request, action, target string) error {
	i := who(r)
	actor := "user:" + i.UserID
	if i.KeyID != "" {
		actor = "key:" + i.KeyID
	}
	aid, e := id.New(id.Run, j.Clock.Now())
	if e != nil {
		return e
	}
	details := sql.NullString{}
	if marked, _ := r.Context().Value(mcpKey{}).(bool); marked {
		details = sql.NullString{String: `{"transport":"mcp"}`, Valid: true}
	}
	return j.Store.History().WriteAudit(r.Context(), historygen.WriteAuditParams{Details: details, ID: "audit_" + strings.TrimPrefix(aid, "run_"), WorkspaceID: i.WorkspaceID, Actor: actor, Action: action, Target: sql.NullString{String: target, Valid: target != ""}, CreatedAt: j.Clock.Now().UnixMilli()})
}
