package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gowthamgts/cronpad/pkg/ext"

	"github.com/gowthamgts/cronpad/internal/alert"
	"github.com/gowthamgts/cronpad/internal/events"
	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/sink/webhook"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

func (j *Jobs) mountAlerts(r chi.Router) {
	j.route(r, "GET", "/api/v1/alert-channels", ext.AlertsWrite, j.alertChannels)
	j.route(r, "POST", "/api/v1/alert-channels", ext.AlertsWrite, j.saveAlertChannel)
	j.route(r, "GET", "/api/v1/alert-channels/{id}", ext.AlertsWrite, j.getAlertChannel)
	j.route(r, "PATCH", "/api/v1/alert-channels/{id}", ext.AlertsWrite, j.saveAlertChannel)
	j.route(r, "DELETE", "/api/v1/alert-channels/{id}", ext.AlertsWrite, j.deleteAlertChannel)
	j.route(r, "POST", "/api/v1/alert-channels/{id}/test", ext.AlertsWrite, j.testAlertChannel)
	j.route(r, "POST", "/api/v1/alert-channels/{id}/verify", ext.AlertsWrite, j.resendAlertVerification)
	j.route(r, "GET", "/api/v1/alert-rules", ext.AlertsWrite, j.alertRules)
	j.route(r, "POST", "/api/v1/alert-rules", ext.AlertsWrite, j.saveAlertRule)
	j.route(r, "GET", "/api/v1/alert-rules/{id}", ext.AlertsWrite, j.getAlertRule)
	j.route(r, "PATCH", "/api/v1/alert-rules/{id}", ext.AlertsWrite, j.saveAlertRule)
	j.route(r, "DELETE", "/api/v1/alert-rules/{id}", ext.AlertsWrite, j.deleteAlertRule)
}
func alertRead(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "validation_failed", "invalid alert JSON", nil)
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		writeError(w, 400, "validation_failed", "expected one JSON object", nil)
		return false
	}
	return true
}
func alertError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "not_found", "alert resource not found", nil)
	} else {
		writeError(w, 500, "internal", "cannot access alert resource", nil)
	}
	return true
}
func channelObject(c sqlitegen.AlertChannel) map[string]any {
	return map[string]any{"id": c.ID, "kind": c.Kind, "name": c.Name, "config": json.RawMessage(c.Config), "verified": c.VerifiedAt.Valid, "created_at": time.UnixMilli(c.CreatedAt).UTC()}
}
func ruleObject(r sqlitegen.AlertRule) map[string]any {
	return map[string]any{"id": r.ID, "name": r.Name, "job_selector": json.RawMessage(r.JobSelector), "events": json.RawMessage(r.Events), "conditions": json.RawMessage(r.Conditions), "channel_ids": json.RawMessage(r.ChannelIds), "cooldown": (time.Duration(r.CooldownMs) * time.Millisecond).String(), "enabled": r.Enabled != 0}
}
func (j *Jobs) alertChannels(w http.ResponseWriter, r *http.Request) {
	rows, err := j.Store.ControlReader().ListAlertChannels(r.Context(), who(r).WorkspaceID)
	if alertError(w, err) {
		return
	}
	out := []any{}
	for _, row := range rows {
		out = append(out, channelObject(row))
	}
	writeJSON(w, 200, map[string]any{"data": out, "next_cursor": nil})
}
func (j *Jobs) getAlertChannel(w http.ResponseWriter, r *http.Request) {
	row, err := j.Store.ControlReader().GetAlertChannel(r.Context(), sqlitegen.GetAlertChannelParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
	if !alertError(w, err) {
		writeJSON(w, 200, channelObject(row))
	}
}
func (j *Jobs) saveAlertChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind   string          `json:"kind"`
		Name   string          `json:"name"`
		Config json.RawMessage `json:"config"`
	}
	now := j.Clock.Now()
	ch := sqlitegen.AlertChannel{WorkspaceID: who(r).WorkspaceID, CreatedAt: now.UnixMilli()}
	fresh := r.Method == "POST"
	if !fresh {
		var err error
		ch, err = j.Store.ControlReader().GetAlertChannel(r.Context(), sqlitegen.GetAlertChannelParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
		if alertError(w, err) {
			return
		}
		in.Kind = ch.Kind
		in.Name = ch.Name
		in.Config = json.RawMessage(ch.Config)
	}
	if !alertRead(w, r, &in) {
		return
	}
	if len(strings.TrimSpace(in.Name)) == 0 || len(in.Name) > 200 {
		writeError(w, 400, "validation_failed", "name must contain 1..200 characters", nil)
		return
	}
	if err := alert.ValidateChannel(in.Kind, in.Config, j.Webhook.Policy); err != nil {
		writeError(w, 400, "validation_failed", err.Error(), nil)
		return
	}
	if in.Kind == "email" && !j.Config.Alerts.Email.Enabled {
		writeError(w, 400, "validation_failed", "email alerts are not configured", nil)
		return
	}
	if j.Notifier == nil {
		writeError(w, 503, "unavailable", "alert delivery is not configured", nil)
		return
	}
	changed := fresh || ch.Kind != in.Kind || ch.Config != string(in.Config)
	secret := ""
	var err error
	if fresh {
		ch.ID, err = id.New(id.Run, now)
		ch.ID = strings.Replace(ch.ID, "run_", "ch_", 1)
		if alertError(w, err) {
			return
		}
	}
	if in.Kind == "webhook" && (fresh || ch.Kind != "webhook") {
		if j.Vault == nil {
			writeError(w, 503, "unavailable", "configure secrets.encryption_key for webhook alert channels", nil)
			return
		}
		secret, err = webhook.GenerateSecret()
		if alertError(w, err) {
			return
		}
		ch.SigningSecret, err = j.Vault.Wrap(secret, "alert:"+ch.ID)
		if alertError(w, err) {
			return
		}
	}
	ch.Kind = in.Kind
	ch.Name = in.Name
	ch.Config = string(in.Config)
	if changed {
		ch.VerifiedAt = sql.NullInt64{}
	}
	ctx := r.Context()
	err = j.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		if err := tx.Control.SaveAlertChannel(ctx, sqlitegen.SaveAlertChannelParams(ch)); err != nil {
			return err
		}
		if changed {
			if err := tx.Control.ClearAlertVerifications(ctx, ch.ID); err != nil {
				return err
			}
			if ch.Kind == "email" {
				var cfg alert.ChannelConfig
				_ = json.Unmarshal(in.Config, &cfg)
				for _, address := range cfg.To {
					if err := tx.Control.AddAlertVerification(ctx, sqlitegen.AddAlertVerificationParams{ChannelID: ch.ID, Address: address}); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if alertError(w, err) {
		return
	}
	if changed && ch.Kind == "email" {
		if alertError(w, j.Notifier.QueueVerification(r.Context(), ch)) {
			return
		}
	}
	out := channelObject(ch)
	if secret != "" {
		out["signing_secret"] = secret
	}
	status := 200
	if fresh {
		status = 201
	}
	writeJSON(w, status, out)
}
func (j *Jobs) deleteAlertChannel(w http.ResponseWriter, r *http.Request) {
	n, err := j.Store.Control().DeleteAlertChannel(r.Context(), sqlitegen.DeleteAlertChannelParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
	if err == nil && n == 0 {
		err = sql.ErrNoRows
	}
	if !alertError(w, err) {
		w.WriteHeader(204)
	}
}
func (j *Jobs) testAlertChannel(w http.ResponseWriter, r *http.Request) {
	ch, err := j.Store.ControlReader().GetAlertChannel(r.Context(), sqlitegen.GetAlertChannelParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
	if alertError(w, err) {
		return
	}
	if j.Notifier == nil {
		writeError(w, 503, "unavailable", "alert delivery is not configured", nil)
		return
	}
	start := j.Clock.Now()
	outcome := "success"
	msg := ""
	if err = j.Notifier.Test(r.Context(), ch); err != nil {
		outcome = "fatal"
		msg = err.Error()
	}
	writeJSON(w, 200, map[string]any{"outcome": outcome, "duration_ms": j.Clock.Since(start).Milliseconds(), "error": msg})
}
func (j *Jobs) resendAlertVerification(w http.ResponseWriter, r *http.Request) {
	ch, err := j.Store.ControlReader().GetAlertChannel(r.Context(), sqlitegen.GetAlertChannelParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
	if alertError(w, err) {
		return
	}
	if ch.Kind != "email" || ch.VerifiedAt.Valid {
		writeError(w, 400, "validation_failed", "channel does not need verification", nil)
		return
	}
	if j.Notifier == nil {
		writeError(w, 503, "unavailable", "email is not configured", nil)
		return
	}
	if !alertError(w, j.Notifier.QueueVerification(r.Context(), ch)) {
		w.WriteHeader(204)
	}
}
func (j *Jobs) verifyAlertChannel(w http.ResponseWriter, r *http.Request) {
	if j.Notifier == nil {
		writeError(w, 503, "unavailable", "email is not configured", nil)
		return
	}
	if err := j.Notifier.Verify(r.Context(), r.URL.Query().Get("token")); err != nil {
		writeError(w, 400, "validation_failed", "invalid or expired verification link", nil)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "verified"})
}
func (j *Jobs) alertRules(w http.ResponseWriter, r *http.Request) {
	rows, err := j.Store.ControlReader().ListAlertRules(r.Context(), who(r).WorkspaceID)
	if alertError(w, err) {
		return
	}
	out := []any{}
	for _, row := range rows {
		out = append(out, ruleObject(row))
	}
	writeJSON(w, 200, map[string]any{"data": out, "next_cursor": nil})
}
func (j *Jobs) getAlertRule(w http.ResponseWriter, r *http.Request) {
	row, err := j.Store.ControlReader().GetAlertRule(r.Context(), sqlitegen.GetAlertRuleParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
	if !alertError(w, err) {
		writeJSON(w, 200, ruleObject(row))
	}
}
func (j *Jobs) saveAlertRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string           `json:"name"`
		Selector   alert.Selector   `json:"job_selector"`
		Events     []string         `json:"events"`
		Conditions alert.Conditions `json:"conditions"`
		Channels   []string         `json:"channel_ids"`
		Cooldown   string           `json:"cooldown"`
		Enabled    bool             `json:"enabled"`
	}
	row := sqlitegen.AlertRule{WorkspaceID: who(r).WorkspaceID, CreatedAt: j.Clock.Now().UnixMilli()}
	fresh := r.Method == "POST"
	in.Selector.All = true
	in.Cooldown = "15m"
	in.Enabled = true
	if !fresh {
		var err error
		row, err = j.Store.ControlReader().GetAlertRule(r.Context(), sqlitegen.GetAlertRuleParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
		if alertError(w, err) {
			return
		}
		in.Name = row.Name
		_ = json.Unmarshal([]byte(row.JobSelector), &in.Selector)
		_ = json.Unmarshal([]byte(row.Events), &in.Events)
		_ = json.Unmarshal([]byte(row.Conditions), &in.Conditions)
		_ = json.Unmarshal([]byte(row.ChannelIds), &in.Channels)
		in.Cooldown = (time.Duration(row.CooldownMs) * time.Millisecond).String()
		in.Enabled = row.Enabled != 0
	}
	// Decode nested selector as a replacement rather than retaining default all=true.
	var raw map[string]json.RawMessage
	if !alertRead(w, r, &raw) {
		return
	}
	b, _ := json.Marshal(raw)
	if _, ok := raw["job_selector"]; ok {
		in.Selector = alert.Selector{}
	}
	if _, ok := raw["conditions"]; ok {
		in.Conditions = alert.Conditions{}
	}
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		writeError(w, 400, "validation_failed", "invalid alert rule JSON", nil)
		return
	}
	if requested, ok := raw["conditions"]; ok {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(requested, &fields)
		if _, ok := fields["min_consecutive"]; ok && in.Conditions.MinConsecutive < 1 {
			writeError(w, 400, "validation_failed", "min_consecutive must be at least 1", nil)
			return
		}
		if _, ok := fields["min_attempt"]; ok && in.Conditions.MinAttempt < 1 {
			writeError(w, 400, "validation_failed", "min_attempt must be at least 1", nil)
			return
		}
	}
	duration, err := time.ParseDuration(in.Cooldown)
	modes := 0
	if in.Selector.All {
		modes++
	}
	if len(in.Selector.JobIDs) > 0 {
		modes++
	}
	if len(in.Selector.Tags) > 0 {
		modes++
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || len(in.Events) == 0 || len(in.Events) > 12 || len(in.Channels) == 0 || len(in.Channels) > 50 || modes != 1 || err != nil || duration < 0 || in.Conditions.MinAttempt < 0 || in.Conditions.MinConsecutive < 0 {
		writeError(w, 400, "validation_failed", "invalid name, selector, events, channels, conditions or cooldown", nil)
		return
	}
	for _, event := range in.Events {
		switch event {
		case events.RunSucceeded, events.RunFailed, events.RunDeferred, events.RunTimeout, events.RunSLABreached, events.JobRecovered, events.JobAutoDisabled, events.JobCompleted, events.LeaseExpired, events.WorkspaceQuotaWarning, events.WorkspaceQuotaExceeded:
		case events.Degraded:
			if j.Config.Mode == "hosted" {
				writeError(w, 400, "validation_failed", "system.degraded is for self-hosted operators", nil)
				return
			}
		default:
			writeError(w, 400, "validation_failed", "unknown alert event", nil)
			return
		}
	}
	for _, channel := range in.Channels {
		_, err = j.Store.ControlReader().GetAlertChannel(r.Context(), sqlitegen.GetAlertChannelParams{ID: channel, WorkspaceID: row.WorkspaceID})
		if alertError(w, err) {
			return
		}
	}
	for _, job := range in.Selector.JobIDs {
		_, err = j.Store.ControlReader().GetJob(r.Context(), sqlitegen.GetJobParams{ID: job, WorkspaceID: row.WorkspaceID})
		if alertError(w, err) {
			return
		}
	}
	if fresh {
		row.ID, err = id.New(id.Run, j.Clock.Now())
		row.ID = strings.Replace(row.ID, "run_", "ar_", 1)
		if alertError(w, err) {
			return
		}
	}
	encode := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	row.Name = in.Name
	row.JobSelector = encode(in.Selector)
	row.Events = encode(in.Events)
	row.Conditions = encode(in.Conditions)
	row.ChannelIds = encode(in.Channels)
	row.CooldownMs = duration.Milliseconds()
	row.Enabled = 0
	if in.Enabled {
		row.Enabled = 1
	}
	err = j.Store.Control().SaveAlertRule(r.Context(), sqlitegen.SaveAlertRuleParams(row))
	if alertError(w, err) {
		return
	}
	if j.Alerts != nil && alertError(w, j.Alerts.Refresh(r.Context())) {
		return
	}
	status := 200
	if fresh {
		status = 201
	}
	writeJSON(w, status, ruleObject(row))
}
func (j *Jobs) deleteAlertRule(w http.ResponseWriter, r *http.Request) {
	n, err := j.Store.Control().DeleteAlertRule(r.Context(), sqlitegen.DeleteAlertRuleParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
	if err == nil && n == 0 {
		err = sql.ErrNoRows
	}
	if alertError(w, err) {
		return
	}
	if j.Alerts != nil && alertError(w, j.Alerts.Refresh(r.Context())) {
		return
	}
	w.WriteHeader(204)
}

// RefreshAlerts supports callers which mutate rules outside the HTTP API.
func (j *Jobs) RefreshAlerts(ctx context.Context) error {
	if j.Alerts != nil {
		return j.Alerts.Refresh(ctx)
	}
	return nil
}
