package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/gowthamgts/cronpad/internal/auth"
	"github.com/gowthamgts/cronpad/internal/limits"
	"github.com/gowthamgts/cronpad/internal/logx"
	"github.com/gowthamgts/cronpad/internal/secrets"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/pkg/ext"

	"github.com/gowthamgts/cronpad/internal/alert"
	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/id"
	"github.com/gowthamgts/cronpad/internal/queue"
	"github.com/gowthamgts/cronpad/internal/retry"
	"github.com/gowthamgts/cronpad/internal/schedule"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/sink/command"
	"github.com/gowthamgts/cronpad/internal/sink/webhook"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/internal/store/historygen"
	"github.com/gowthamgts/cronpad/internal/store/sqlitegen"
)

type jobSchedule struct {
	Expr     string        `json:"expr"`
	Kind     schedule.Kind `json:"kind"`
	Timezone string        `json:"timezone"`
	StartsAt *time.Time    `json:"starts_at"`
	EndsAt   *time.Time    `json:"ends_at"`
	Count    *int          `json:"count"`
}
type jobSink struct {
	Kind   string          `json:"kind"`
	Config json.RawMessage `json:"config"`
}
type jobInput struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Tags        []string        `json:"tags"`
	Schedule    jobSchedule     `json:"schedule"`
	Sink        jobSink         `json:"sink"`
	Retry       json.RawMessage `json:"retry"`
	Timeout     string          `json:"timeout"`
	SLA         *string         `json:"sla_threshold"`
	Misfire     string          `json:"misfire_policy"`
	Overlap     string          `json:"overlap_policy"`
	AutoDisable *int64          `json:"auto_disable_threshold"`
}
type Jobs struct {
	Store     store.Store
	Queue     queue.Queue
	Scheduler Scheduler
	Clock     clockwork.Clock
	Config    config.Config
	Webhook   *webhook.Webhook
	Alerts    *alert.Evaluator
	Notifier  *alert.Notifier
	validate  *validator.Validate
	// testAuth is set only by the test-only newJobs constructor.
	testAuth       func(http.Handler) http.Handler
	Auth           *auth.Service
	Vault          *secrets.Vault
	Limits         *limits.Registry
	APILimiter     func(http.Handler) http.Handler
	operations     http.Handler
	mutationPolicy func(http.Handler) http.Handler
}

func (j *Jobs) Mount(r chi.Router) {
	r.Get("/api/v1/alert-channels/verify", j.verifyAlertChannel)
	v := validator.New()
	_ = v.RegisterValidation("tz", func(fl validator.FieldLevel) bool { _, e := time.LoadLocation(fl.Field().String()); return e == nil })
	_ = v.RegisterValidation("duration", func(fl validator.FieldLevel) bool {
		var d config.Duration
		return d.UnmarshalText([]byte(fl.Field().String())) == nil && d > 0
	})
	_ = v.RegisterValidation("url_public", func(fl validator.FieldLevel) bool { return j.Webhook.Policy.CheckURL(fl.Field().String()) == nil })
	_ = v.RegisterValidationCtx("schedule", func(parent context.Context, fl validator.FieldLevel) bool {
		ctx, cancel := context.WithTimeout(parent, 100*time.Millisecond)
		defer cancel()
		spec := schedule.Spec{Expr: fl.Field().String()}
		if other, ok := fl.Parent().Interface().(jobSchedule); ok {
			spec.Kind = other.Kind
			spec.Timezone = other.Timezone
			spec.StartsAt = other.StartsAt
			spec.EndsAt = other.EndsAt
			spec.Count = other.Count
		}
		_, e := schedule.ParseContext(ctx, spec, j.Clock)
		return e == nil
	})
	j.validate = v
	j.APILimiter = j.AuthLimiter()
	// Share idempotency serialization across routes and REST/MCP transports.
	j.mutationPolicy = j.mutations()
	ops := chi.NewRouter()
	j.mountOperations(ops)
	Schedules(ops, j.Clock, j.Config.Limits.MinInterval.Duration(), 0)
	j.operations = ops
	r.Group(func(r chi.Router) {
		if j.Auth != nil {
			r.Use(j.Auth.Middleware)
			r.Use(j.APILimiter)
		} else if j.testAuth != nil && j.Config.Auth.DevLogin {
			r.Use(j.testAuth)
		} else {
			r.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					writeError(w, 401, "unauthorized", "authentication required", nil)
				})
			})
		}
		j.mountOperations(r)
	})
}
func (j *Jobs) mountOperations(r chi.Router) {
	j.route(r, "GET", "/api/v1/usage", ext.JobsRead, j.usage)
	if j.Auth != nil {
		j.mountWorkspace(r)
	}
	j.route(r, "POST", "/api/v1/jobs", ext.JobsWrite, j.create)
	j.route(r, "GET", "/api/v1/jobs", ext.JobsRead, j.list)
	j.route(r, "POST", "/api/v1/jobs/test", ext.JobsWrite, j.testUnsaved)
	j.route(r, "GET", "/api/v1/jobs/{id}", ext.JobsRead, j.get)
	j.route(r, "PATCH", "/api/v1/jobs/{id}", ext.JobsWrite, j.patch)
	j.route(r, "DELETE", "/api/v1/jobs/{id}", ext.JobsWrite, j.delete)
	for _, a := range []string{"pause", "resume", "enable"} {
		j.route(r, "POST", "/api/v1/jobs/{id}/"+a, ext.JobsWrite, j.state)
	}
	j.route(r, "POST", "/api/v1/jobs/{id}/trigger", ext.JobsTrigger, j.trigger)
	j.route(r, "POST", "/api/v1/jobs/{id}/test", ext.JobsWrite, j.testSaved)
	j.route(r, "POST", "/api/v1/jobs/{id}/rotate-signing-secret", ext.JobsWrite, j.rotate)
	j.route(r, "GET", "/api/v1/jobs/{id}/runs", ext.RunsRead, j.runs)
	j.route(r, "GET", "/api/v1/runs", ext.RunsRead, j.runs)
	j.route(r, "GET", "/api/v1/runs/{id}", ext.RunsRead, j.run)
	j.route(r, "POST", "/api/v1/runs/{id}/retry", ext.RunsRetry, j.retryRun)
}

func who(r *http.Request) auth.Identity { return auth.Current(r.Context()) }
func readJob(w http.ResponseWriter, r *http.Request, target *jobInput) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(target); e != nil {
		writeError(w, 400, "validation_failed", "invalid job JSON", nil)
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		writeError(w, 400, "validation_failed", "expected one JSON object", nil)
		return false
	}
	return true
}
func (j *Jobs) defaults() jobInput {
	return jobInput{Tags: []string{}, Schedule: jobSchedule{Kind: schedule.Auto, Timezone: "UTC"}, Timeout: "", Misfire: j.Config.Scheduler.DefaultMisfirePolicy, Overlap: "allow"}
}
func nullTime(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}
func ptrTime(t sql.NullInt64) *time.Time {
	if !t.Valid {
		return nil
	}
	v := time.UnixMilli(t.Int64).UTC()
	return &v
}
func stringPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
func numberPtr(s sql.NullInt64) *int64 {
	if !s.Valid {
		return nil
	}
	return &s.Int64
}
func (j *Jobs) build(ctx context.Context, in jobInput, old *sqlitegen.Job) (sqlitegen.Job, int, string) {
	job := sqlitegen.Job{}
	if old != nil {
		job = *old
	}
	if len(in.Name) == 0 || len(in.Name) > 200 || len(in.Description) > 10000 || len(in.Tags) > 50 {
		return job, 400, "name, description or tags outside allowed bounds"
	}
	if in.Schedule.Timezone == "" {
		in.Schedule.Timezone = "UTC"
	}
	if j.validate.VarCtx(ctx, in.Schedule.Timezone, "tz") != nil {
		return job, 400, "invalid timezone"
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	spec := schedule.Spec{Expr: in.Schedule.Expr, Kind: in.Schedule.Kind, Timezone: in.Schedule.Timezone, StartsAt: in.Schedule.StartsAt, EndsAt: in.Schedule.EndsAt, Count: in.Schedule.Count}
	anchor := j.Clock.Now()
	if old != nil && reflect.DeepEqual(in.Schedule, inputFrom(*old).Schedule) {
		anchor = jobAnchor(*old)
	}
	reference := clockwork.NewFakeClockAt(anchor)
	job.ScheduleAnchorAt = sql.NullInt64{Int64: anchor.UnixMilli(), Valid: true}
	if j.validate.VarWithValueCtx(ctx, in.Schedule.Expr, in.Schedule, "required,schedule") != nil {
		return job, 400, "invalid schedule"
	}
	parsed, e := schedule.ParseContext(ctx, spec, reference)
	if e != nil {
		return job, 400, e.Error()
	}
	if gap := schedule.MinGap(parsed, j.Clock.Now(), 100); gap > 0 {
		if j.Limits != nil {
			if err := j.Limits.Check(ctx, auth.Current(ctx).WorkspaceID, limits.Action{Limit: "min_interval", Value: gap.Milliseconds()}); err != nil {
				var l *limits.Error
				if errors.As(err, &l) {
					return job, 403, fmt.Sprintf("limit:min_interval:%d", l.Value)
				}
				return job, 500, "cannot evaluate limit"
			}
		} else if gap < j.Config.Limits.MinInterval.Duration() {
			return job, 403, fmt.Sprintf("limit:min_interval:%d", j.Config.Limits.MinInterval.Duration().Milliseconds())
		}
	}
	if ctx.Err() != nil {
		return job, 400, "schedule computation budget exceeded"
	}
	if len(in.Sink.Config) > 65536 {
		return job, 400, "sink config exceeds 64 KiB"
	}
	in.Sink.Config = withSinkDefaults(j.Config, in.Sink.Kind, in.Sink.Config)
	texts := []string{}
	switch in.Sink.Kind {
	case "webhook":
		if !j.Config.Sinks.Webhook.Enabled {
			return job, 400, "webhook sink is disabled"
		}
		if e = j.Webhook.Validate(in.Sink.Config, sink.Mode(j.Config.Mode)); e != nil {
			return job, 400, e.Error()
		}
		var wc webhook.Config
		_ = json.Unmarshal(in.Sink.Config, &wc)
		texts = append(texts, wc.URL)
		if wc.Body != nil {
			texts = append(texts, *wc.Body)
		}
		for _, v := range wc.Headers {
			texts = append(texts, v)
		}
	case "command":
		if e = command.New(j.Config.Sinks.Command).Validate(in.Sink.Config, sink.Mode(j.Config.Mode)); e != nil {
			return job, 400, e.Error()
		}
		var cc command.Config
		_ = json.Unmarshal(in.Sink.Config, &cc)
		texts = append(texts, cc.Args...)
		for _, v := range cc.Env {
			texts = append(texts, v)
		}
	default:
		return job, 400, "unknown sink kind"
	}
	for _, s := range texts {
		refs, _ := sink.References(s)
		for _, name := range refs {
			if j.Vault == nil {
				return job, 400, "unknown secret " + name
			}
			if _, err := j.Vault.Resolve(ctx, auth.Current(ctx).WorkspaceID, name); err != nil {
				return job, 400, "unknown secret " + name
			}
		}
	}
	cap := j.Config.Limits.MaxRetryAttempts
	if j.Limits != nil {
		cap = 0
	}
	defaults := retry.Defaults(j.Config.Retry.Defaults)
	if old != nil {
		_ = json.Unmarshal([]byte(old.RetryPolicy), &defaults)
	}
	p, e := retry.Decode(in.Retry, defaults, cap)
	if e != nil {
		return job, 400, e.Error()
	}
	if j.Limits != nil {
		if e = j.Limits.Check(ctx, auth.Current(ctx).WorkspaceID, limits.Action{Limit: "max_retry_attempts", Value: int64(p.MaxAttempts)}); e != nil {
			var l *limits.Error
			if errors.As(e, &l) {
				return job, 403, fmt.Sprintf("limit:max_retry_attempts:%d", l.Value)
			}
			return job, 500, "cannot evaluate limit"
		}
	}
	if in.Timeout == "" {
		in.Timeout = j.Config.Sinks.Webhook.DefaultTimeout.String()
		if in.Sink.Kind == "command" {
			in.Timeout = j.Config.Sinks.Command.DefaultTimeout.String()
		}
	}
	if j.validate.VarCtx(ctx, in.Timeout, "duration") != nil {
		return job, 400, "invalid timeout"
	}
	var timeout config.Duration
	_ = timeout.UnmarshalText([]byte(in.Timeout))
	if in.Sink.Kind == "webhook" && timeout.Duration() > j.Config.Sinks.Webhook.MaxTimeout.Duration() {
		return job, 400, "timeout exceeds maximum"
	}
	if in.Misfire != "fire_once" && in.Misfire != "skip" && in.Misfire != "fire_all" {
		return job, 400, "invalid misfire_policy"
	}
	if in.Overlap != "allow" && in.Overlap != "skip_if_running" {
		return job, 400, "invalid overlap_policy"
	}
	if in.AutoDisable != nil && (*in.AutoDisable < 1 || j.Config.Mode == "hosted" && *in.AutoDisable > int64(j.Config.AutoDisable.ConsecutiveFailures)) {
		return job, 400, "invalid auto_disable_threshold"
	}
	job.Name = in.Name
	job.Description = sql.NullString{String: in.Description, Valid: in.Description != ""}
	tags, _ := json.Marshal(in.Tags)
	if in.Tags == nil {
		tags = []byte("[]")
	}
	job.Tags = string(tags)
	job.ScheduleKind = string(parsed.Kind())
	job.ScheduleExpr = in.Schedule.Expr
	job.ScheduleCanonical = parsed.Canonical()
	job.Timezone = in.Schedule.Timezone
	job.StartsAt = nullTime(in.Schedule.StartsAt)
	job.EndsAt = nullTime(in.Schedule.EndsAt)
	job.MaxOccurrences = sql.NullInt64{}
	if in.Schedule.Count != nil {
		job.MaxOccurrences = sql.NullInt64{Int64: int64(*in.Schedule.Count), Valid: true}
	}
	job.SinkKind = in.Sink.Kind
	job.SinkConfig = string(in.Sink.Config)
	b, _ := json.Marshal(p)
	job.RetryPolicy = string(b)
	job.TimeoutMs = timeout.Duration().Milliseconds()
	job.SlaThresholdMs = sql.NullInt64{}
	if in.SLA != nil {
		var d config.Duration
		if d.UnmarshalText([]byte(*in.SLA)) != nil || d <= 0 {
			return job, 400, "invalid sla_threshold"
		}
		job.SlaThresholdMs = sql.NullInt64{Int64: d.Duration().Milliseconds(), Valid: true}
	}
	job.AutoDisableThreshold = sql.NullInt64{}
	if in.AutoDisable != nil {
		job.AutoDisableThreshold = sql.NullInt64{Int64: *in.AutoDisable, Valid: true}
	}
	job.MisfirePolicy = in.Misfire
	job.OverlapPolicy = in.Overlap
	job.UpdatedAt = j.Clock.Now().UnixMilli()
	return job, 0, ""
}
func inputFrom(job sqlitegen.Job) jobInput {
	in := jobInput{Name: job.Name, Description: job.Description.String, Schedule: jobSchedule{Expr: job.ScheduleExpr, Kind: schedule.Kind(job.ScheduleKind), Timezone: job.Timezone, StartsAt: ptrTime(job.StartsAt), EndsAt: ptrTime(job.EndsAt)}, Sink: jobSink{job.SinkKind, json.RawMessage(job.SinkConfig)}, Retry: json.RawMessage(job.RetryPolicy), Timeout: (time.Duration(job.TimeoutMs) * time.Millisecond).String(), Misfire: job.MisfirePolicy, Overlap: job.OverlapPolicy, AutoDisable: numberPtr(job.AutoDisableThreshold)}
	_ = json.Unmarshal([]byte(job.Tags), &in.Tags)
	if job.MaxOccurrences.Valid {
		n := int(job.MaxOccurrences.Int64)
		in.Schedule.Count = &n
	}
	if job.SlaThresholdMs.Valid {
		s := (time.Duration(job.SlaThresholdMs.Int64) * time.Millisecond).String()
		in.SLA = &s
	}
	return in
}
func (j *Jobs) object(ctx context.Context, job sqlitegen.Job, detail bool) map[string]any {
	in := inputFrom(job)
	var rp any
	_ = json.Unmarshal([]byte(job.RetryPolicy), &rp)
	sc := map[string]any{"expr": job.ScheduleExpr, "kind": job.ScheduleKind, "canonical": job.ScheduleCanonical, "timezone": job.Timezone, "starts_at": ptrTime(job.StartsAt), "ends_at": ptrTime(job.EndsAt), "count": numberPtr(job.MaxOccurrences)}
	out := map[string]any{"id": job.ID, "name": job.Name, "description": stringPtr(job.Description), "tags": in.Tags, "schedule": sc, "sink": in.Sink, "retry": rp, "timeout": in.Timeout, "sla_threshold": in.SLA, "misfire_policy": job.MisfirePolicy, "overlap_policy": job.OverlapPolicy, "auto_disable_threshold": numberPtr(job.AutoDisableThreshold), "state": job.State, "disabled_reason": stringPtr(job.DisabledReason), "consecutive_failures": job.ConsecutiveFailures, "next_run_at": ptrTime(job.NextRunAt), "last_run_at": ptrTime(job.LastRunAt), "last_run_status": stringPtr(job.LastRunStatus), "version": job.Version, "created_at": time.UnixMilli(job.CreatedAt).UTC(), "updated_at": time.UnixMilli(job.UpdatedAt).UTC()}
	compute, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	s, e := schedule.ParseContext(compute, jobSpec(job), clockwork.NewFakeClockAt(jobAnchor(job)))
	if e == nil {
		sc["description"] = s.Describe()
		if detail {
			next := []time.Time{}
			if job.State == "active" {
				next = schedule.Upcoming(s, j.Clock.Now(), 5)
			}
			out["next_runs"] = next
		}
	}
	if detail {
		stats := map[string]any{"runs": int64(0), "succeeded": int64(0), "failed": int64(0), "sla_breached": int64(0), "p95_start_lag_ms": nil}
		counts, lags, e := j.retainedStats(ctx, job.WorkspaceID, job.ID, 24*time.Hour)
		if e == nil {
			stats["runs"] = counts["total"]
			stats["succeeded"] = counts["succeeded"]
			stats["failed"] = counts["failed"]
			stats["sla_breached"] = counts["sla_breached"]
			if counts["total"] > 0 {
				stats["p95_start_lag_ms"] = lags["p95"]
			}
		}

		out["stats_24h"] = stats
	}
	return out
}
func (j *Jobs) load(w http.ResponseWriter, r *http.Request) (sqlitegen.Job, bool) {
	job, e := j.Store.ControlReader().GetJob(r.Context(), sqlitegen.GetJobParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
	if errors.Is(e, sql.ErrNoRows) {
		writeError(w, 404, "not_found", "job not found", nil)
		return job, false
	}
	if e != nil {
		writeError(w, 500, "internal", "cannot read job", nil)
		return job, false
	}
	return job, true
}
func validationError(w http.ResponseWriter, status int, message string) {
	if strings.HasPrefix(message, "limit:") {
		parts := strings.Split(message, ":")
		n, _ := strconv.ParseInt(parts[2], 10, 64)
		limitError(w, &limits.Error{Limit: parts[1], Value: n})
		return
	}
	code := "validation_failed"
	if status == 403 {
		code = "limit_exceeded"
		if strings.Contains(message, "sink") {
			code = "forbidden"
		}
	}
	writeError(w, status, code, message, nil)
}
func (j *Jobs) create(w http.ResponseWriter, r *http.Request) {
	if j.Auth != nil && j.Vault == nil {
		writeError(w, 503, "unavailable", "configure secrets.encryption_key", nil)
		return
	}
	in := j.defaults()
	if !readJob(w, r, &in) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 200 {
		writeError(w, 400, "validation_failed", "idempotency key too long", nil)
		return
	}
	encoded, _ := json.Marshal(in)
	hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
	now := j.Clock.Now()
	workspace := who(r).WorkspaceID
	replay := func(record sqlitegen.IdempotencyRecord) {
		if state, ok := r.Context().Value(mutationStateKey{}).(*mutationState); ok {
			state.replayed = true
		}
		if record.RequestHash != hash {
			writeError(w, 409, "idempotency_conflict", "idempotency key was used with a different request", nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(record.Status))
		body := record.Response
		if j.Vault != nil {
			var err error
			body, err = j.Vault.Unwrap(body, "idempotency:"+workspace+":"+key)
			if err != nil {
				writeError(w, 500, "internal", "cannot decrypt idempotency record", nil)
				return
			}
		}
		_, _ = io.WriteString(w, body)
	}
	if key != "" {
		record, e := j.Store.ControlReader().GetIdempotency(r.Context(), sqlitegen.GetIdempotencyParams{WorkspaceID: workspace, Key: key, ExpiresAt: now.UnixMilli()})
		if e == nil {
			replay(record)
			return
		}
		if !errors.Is(e, sql.ErrNoRows) {
			writeError(w, 500, "internal", "cannot read idempotency record", nil)
			return
		}
	}
	job, status, msg := j.build(r.Context(), in, nil)
	if status != 0 {
		validationError(w, status, msg)
		return
	}
	var e error
	job.ID, e = id.New(id.Job, now)
	if e != nil {
		writeError(w, 500, "internal", "cannot generate job ID", nil)
		return
	}
	job.WorkspaceID = workspace
	job.CreatedBy = sql.NullString{String: who(r).UserID, Valid: true}
	job.CreatedAt = now.UnixMilli()
	job.State = "active"
	job.Version = 1
	job.FailuresResetVersion = 1
	compute, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
	defer cancel()
	parsed, e := schedule.ParseContext(compute, jobSpec(job), clockwork.NewFakeClockAt(jobAnchor(job)))
	if e != nil {
		writeError(w, 400, "validation_failed", "cannot compute next occurrence", nil)
		return
	}
	next, ok := parsed.Next(now)
	if compute.Err() != nil {
		writeError(w, 400, "validation_failed", "schedule computation budget exceeded", nil)
		return
	}
	job.NextRunAt = sql.NullInt64{Int64: next.UnixMilli(), Valid: ok}
	if !ok {
		job.State = "completed"
	}
	secret, e := webhook.GenerateSecret()
	if e != nil {
		writeError(w, 500, "internal", "cannot generate signing secret", nil)
		return
	}
	b, _ := json.Marshal([]sink.SigningSecret{{Value: secret}})
	job.SigningSecrets = sql.NullString{String: string(b), Valid: true}
	var quotaErr error
	var cached *sqlitegen.IdempotencyRecord
	var out map[string]any
	ctx := r.Context()
	e = j.Store.WithTx(ctx, store.Control, func(tx store.Tx) error {
		if key != "" {
			record, e := tx.Control.GetIdempotency(ctx, sqlitegen.GetIdempotencyParams{WorkspaceID: workspace, Key: key, ExpiresAt: now.UnixMilli()})
			if e == nil {
				cached = &record
				return nil
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		n, e := tx.Control.CountJobs(ctx, workspace)
		if e != nil {
			return e
		}
		if j.Limits != nil {
			quotaErr = j.Limits.Check(ctx, workspace, limits.Action{Limit: "max_jobs_per_workspace", Value: n + 1})
			if quotaErr != nil {
				return quotaErr
			}
		} else if max := j.Config.Limits.MaxJobsPerWorkspace; max > 0 && n >= int64(max) {
			quotaErr = &limits.Error{Limit: "max_jobs_per_workspace", Value: int64(max)}
			return quotaErr
		}
		stored := job
		if j.Vault != nil {
			enc, err := j.Vault.Wrap(job.SigningSecrets.String, "signing:"+job.ID)
			if err != nil {
				return err
			}
			stored.SigningSecrets.String = enc
		}
		job, e = tx.Control.CreateJob(ctx, createParams(stored))
		if e != nil {
			return e
		}
		out = j.object(ctx, job, false)
		if job.SinkKind == "webhook" {
			out["signing_secret"] = secret
		}
		if key != "" {
			body, e := json.Marshal(out)
			if e != nil {
				return e
			}
			return tx.Control.SaveIdempotency(ctx, sqlitegen.SaveIdempotencyParams{WorkspaceID: workspace, Key: key, RequestHash: hash, Response: j.encryptResponse(string(body), workspace, key), Status: 201, ExpiresAt: now.Add(24 * time.Hour).UnixMilli()})
		}
		return nil
	})
	if limitError(w, quotaErr) {
		return
	}
	if e != nil {
		writeError(w, 500, "internal", "cannot create job", nil)
		return
	}
	if cached != nil {
		replay(*cached)
		return
	}
	if e = j.Scheduler.Enqueue(r.Context(), job); e != nil {
		writeError(w, 500, "internal", "cannot schedule job", nil)
		return
	}
	writeJSON(w, 201, out)
}
func (j *Jobs) get(w http.ResponseWriter, r *http.Request) {
	if job, ok := j.load(w, r); ok {
		writeJSON(w, 200, j.object(r.Context(), job, true))
	}
}
func (j *Jobs) patch(w http.ResponseWriter, r *http.Request) {
	old, ok := j.load(w, r)
	if !ok {
		return
	}
	in := inputFrom(old)
	if !readJob(w, r, &in) {
		return
	}
	job, status, msg := j.build(r.Context(), in, &old)
	if status != 0 {
		validationError(w, status, msg)
		return
	}
	job.NextRunAt = sql.NullInt64{}
	updated, e := j.updateJob(r.Context(), job)
	if errors.Is(e, sql.ErrNoRows) {
		writeError(w, 409, "conflict", "job changed concurrently", nil)
		return
	}
	if e != nil {
		writeError(w, 500, "internal", "cannot update job", nil)
		return
	}
	_ = j.Scheduler.Remove(r.Context(), old)
	if e = j.Scheduler.Enqueue(r.Context(), updated); e != nil {
		writeError(w, 500, "internal", "cannot schedule job", nil)
		return
	}
	updated, ok = j.load(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, j.object(r.Context(), updated, false))
}
func (j *Jobs) delete(w http.ResponseWriter, r *http.Request) {
	old, ok := j.load(w, r)
	if !ok {
		return
	}
	n, e := j.Store.Control().DeleteJob(r.Context(), sqlitegen.DeleteJobParams{Now: sql.NullInt64{Int64: j.Clock.Now().UnixMilli(), Valid: true}, ID: old.ID, WorkspaceID: old.WorkspaceID, Version: old.Version})
	if e != nil {
		writeError(w, 500, "internal", "cannot delete job", nil)
		return
	}
	if n != 1 {
		writeError(w, 409, "conflict", "job changed concurrently", nil)
		return
	}
	_ = j.Scheduler.Remove(r.Context(), old)
	w.WriteHeader(204)
}
func (j *Jobs) state(w http.ResponseWriter, r *http.Request) {
	old, ok := j.load(w, r)
	if !ok {
		return
	}
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	from, to := "active", "paused"
	if action == "resume" {
		from, to = "paused", "active"
	}
	if action == "enable" {
		from, to = "disabled", "active"
	}
	if old.State != from {
		writeError(w, 409, "conflict", "invalid job state transition", nil)
		return
	}
	fails := old.ConsecutiveFailures
	if action == "enable" {
		fails = 0
	}
	updated, e := j.Store.Control().ChangeJobState(r.Context(), sqlitegen.ChangeJobStateParams{ResetFailures: func() int64 {
		if action == "enable" {
			return 1
		}
		return 0
	}(), State: to, ConsecutiveFailures: fails, Now: j.Clock.Now().UnixMilli(), ID: old.ID, WorkspaceID: old.WorkspaceID, Version: old.Version})
	if e != nil {
		writeError(w, 409, "conflict", "job changed concurrently", nil)
		return
	}
	_ = j.Scheduler.Remove(r.Context(), old)
	if e = j.Scheduler.Enqueue(r.Context(), updated); e != nil {
		writeError(w, 500, "internal", "cannot schedule job", nil)
		return
	}
	updated, ok = j.load(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, j.object(r.Context(), updated, false))
}
func (j *Jobs) trigger(w http.ResponseWriter, r *http.Request) {
	job, ok := j.load(w, r)
	if !ok {
		return
	}
	if j.Limits != nil && limitError(w, j.Limits.CheckRun(r.Context(), job.WorkspaceID)) {
		return
	}
	now := j.Clock.Now()
	runID, _ := id.New(id.Run, now)
	run, e := j.Store.History().UpsertRun(r.Context(), historygen.UpsertRunParams{JobVersion: job.Version, ID: runID, JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: now.UnixMilli(), Trigger: "manual", Status: "deferred", NextAttemptAt: sql.NullInt64{Int64: now.UnixMilli(), Valid: true}})
	if e != nil {
		writeError(w, 500, "internal", "cannot create run", nil)
		return
	}
	// DECISION: pending manual runs are durably deferred before publishing, so a
	// queue outage leaves next_attempt_at available for reconciliation.
	if run.ID != runID {
		writeJSON(w, 202, map[string]string{"run_id": run.ID})
		return
	}

	if e := j.Queue.Add(r.Context(), queue.Occurrence{JobID: job.ID, ScheduledAt: now, Version: job.Version, Trigger: "m"}.Format(), now, false); e != nil {
		slog.WarnContext(r.Context(), "manual trigger publication deferred", logx.JobID, job.ID, logx.RunID, run.ID, logx.Error, e)
	}
	j.Scheduler.Wake()
	writeJSON(w, 202, map[string]string{"run_id": run.ID})
}
func (j *Jobs) rotate(w http.ResponseWriter, r *http.Request) {
	job, ok := j.load(w, r)
	if !ok {
		return
	}
	if job.SinkKind != "webhook" {
		writeError(w, 400, "validation_failed", "only webhook jobs have signing secrets", nil)
		return
	}
	var old []sink.SigningSecret
	plain := job.SigningSecrets.String
	if j.Vault != nil {
		var err error
		plain, err = j.Vault.Unwrap(plain, "signing:"+job.ID)
		if err != nil {
			writeError(w, 500, "internal", "cannot decrypt signing secret", nil)
			return
		}
	}
	_ = json.Unmarshal([]byte(plain), &old)
	secret, e := webhook.GenerateSecret()
	if e != nil {
		writeError(w, 500, "internal", "cannot generate signing secret", nil)
		return
	}
	next := []sink.SigningSecret{{Value: secret}}
	if len(old) > 0 {
		expiry := j.Clock.Now().Add(24 * time.Hour)
		if old[0].ExpiresAt != nil && old[0].ExpiresAt.Before(expiry) {
			expiry = *old[0].ExpiresAt
		}
		old[0].ExpiresAt = &expiry
		next = append(next, old[0])
	}
	b, _ := json.Marshal(next)
	job.SigningSecrets = sql.NullString{String: string(b), Valid: true}
	job.UpdatedAt = j.Clock.Now().UnixMilli()
	updated, e := j.updateJob(r.Context(), job)
	if e != nil {
		writeError(w, 409, "conflict", "job changed concurrently", nil)
		return
	}
	_ = j.Scheduler.Remove(r.Context(), job)
	_ = j.Scheduler.Enqueue(r.Context(), updated)
	writeJSON(w, 200, map[string]string{"signing_secret": secret})
}
func (j *Jobs) testSaved(w http.ResponseWriter, r *http.Request) {
	job, ok := j.load(w, r)
	if ok {
		j.executeTest(w, r, job)
	}
}
func (j *Jobs) testUnsaved(w http.ResponseWriter, r *http.Request) {
	in := j.defaults()
	if !readJob(w, r, &in) {
		return
	}
	job, status, msg := j.build(r.Context(), in, nil)
	if status != 0 {
		validationError(w, status, msg)
		return
	}
	job.ID, _ = id.New(id.Job, j.Clock.Now())
	job.WorkspaceID = who(r).WorkspaceID
	secret, e := webhook.GenerateSecret()
	if e != nil {
		writeError(w, 500, "internal", "cannot generate signing secret", nil)
		return
	}
	b, _ := json.Marshal([]sink.SigningSecret{{Value: secret}})
	job.SigningSecrets = sql.NullString{String: string(b), Valid: true}
	j.executeTest(w, r, job)
}
func (j *Jobs) executeTest(w http.ResponseWriter, r *http.Request, job sqlitegen.Job) {
	var delivery sink.Sink = j.Webhook
	if job.SinkKind == "command" && j.Config.Sinks.Command.Enabled && j.Config.Mode != "hosted" {
		delivery = command.New(j.Config.Sinks.Command)
	}
	if job.SinkKind == "command" && (!j.Config.Sinks.Command.Enabled || j.Config.Mode == "hosted") || job.SinkKind == "webhook" && !j.Config.Sinks.Webhook.Enabled {
		writeError(w, 403, "forbidden", "sink is disabled or unavailable", nil)
		return
	}
	var secrets []sink.SigningSecret
	plain := job.SigningSecrets.String
	if j.Vault != nil && strings.Contains(plain, `"encrypted"`) {
		var err error
		plain, err = j.Vault.Unwrap(plain, "signing:"+job.ID)
		if err != nil {
			writeError(w, 500, "internal", "cannot decrypt signing secret", nil)
			return
		}
	}
	_ = json.Unmarshal([]byte(plain), &secrets)
	runID, _ := id.New(id.Run, j.Clock.Now())
	start := j.Clock.Now()
	res := delivery.Execute(r.Context(), sink.ExecRequest{Job: sink.JobSnapshot{ID: job.ID, Name: job.Name, WorkspaceID: job.WorkspaceID, Config: json.RawMessage(job.SinkConfig), Timeout: time.Duration(job.TimeoutMs) * time.Millisecond, SigningSecrets: secrets}, RunID: runID, ScheduledAt: start, IdempotencyKey: runID, Secrets: j.resolver(job.WorkspaceID)})
	writeJSON(w, 200, map[string]any{"outcome": res.Outcome, "status_code": res.Code, "duration_ms": j.Clock.Since(start).Milliseconds(), "response_body": string(res.ResponseBody), "request_summary": res.RequestSummary, "error": res.Error})
}
func page(r *http.Request) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return 50, nil
	}
	n, e := strconv.Atoi(s)
	if e != nil || n < 1 || n > 200 {
		return 0, fmt.Errorf("limit must be 1..200")
	}
	return n, nil
}
func (j *Jobs) list(w http.ResponseWriter, r *http.Request) {
	n, e := page(r)
	if e != nil {
		writeError(w, 400, "validation_failed", e.Error(), nil)
		return
	}
	q := r.URL.Query()
	rows, e := j.Store.ControlReader().ListJobs(r.Context(), sqlitegen.ListJobsParams{WorkspaceID: who(r).WorkspaceID, State: q.Get("state"), Tag: q.Get("tag"), Search: q.Get("q"), Cursor: q.Get("cursor"), PageSize: int64(n + 1)})
	if e != nil {
		writeError(w, 500, "internal", "cannot list jobs", nil)
		return
	}
	var cursor *string
	if len(rows) > n {
		s := rows[n-1].ID
		cursor = &s
		rows = rows[:n]
	}
	data := []any{}
	for _, job := range rows {
		data = append(data, j.object(r.Context(), job, false))
	}
	writeJSON(w, 200, map[string]any{"data": data, "next_cursor": cursor})
}
func runObject(run historygen.Run) map[string]any {
	return map[string]any{"id": run.ID, "job_id": run.JobID, "scheduled_at": time.UnixMilli(run.ScheduledAt).UTC(), "trigger": run.Trigger, "status": run.Status, "attempts": run.Attempts, "claimed_at": ptrTime(run.ClaimedAt), "started_at": ptrTime(run.StartedAt), "finished_at": ptrTime(run.FinishedAt), "next_attempt_at": ptrTime(run.NextAttemptAt), "start_lag_ms": numberPtr(run.StartLagMs), "e2e_ms": numberPtr(run.E2eMs), "sla_breached": run.SlaBreached != 0, "last_error": stringPtr(run.LastError), "skip_reason": stringPtr(run.SkipReason)}
}
func (j *Jobs) run(w http.ResponseWriter, r *http.Request) {
	run, e := j.Store.HistoryReader().GetRun(r.Context(), historygen.GetRunParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
	if errors.Is(e, sql.ErrNoRows) {
		writeError(w, 404, "not_found", "run not found", nil)
		return
	}
	if e != nil {
		writeError(w, 500, "internal", "cannot get run", nil)
		return
	}
	attempts, e := j.Store.HistoryReader().GetAttempts(r.Context(), historygen.GetAttemptsParams{ID: run.ID, WorkspaceID: run.WorkspaceID})
	if e != nil {
		writeError(w, 500, "internal", "cannot get attempts", nil)
		return
	}
	out := runObject(run)
	data := []any{}
	for _, a := range attempts {
		data = append(data, map[string]any{"n": a.N, "started_at": time.UnixMilli(a.StartedAt).UTC(), "finished_at": ptrTime(a.FinishedAt), "duration_ms": numberPtr(a.DurationMs), "outcome": a.Outcome, "status_code": numberPtr(a.StatusCode), "error": stringPtr(a.Error), "request_summary": stringPtr(a.RequestSummary), "response_body": stringPtr(a.ResponseBody)})
	}
	out["attempts"] = data
	writeJSON(w, 200, out)
}
func (j *Jobs) runs(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "id")
	if jobID != "" {
		if _, ok := j.load(w, r); !ok {
			return
		}
	}
	n, e := page(r)
	if e != nil {
		writeError(w, 400, "validation_failed", e.Error(), nil)
		return
	}
	q := r.URL.Query()
	if jobID == "" {
		jobID = q.Get("job_id")
	}
	tagJobs := "[]"
	if tag := q.Get("tag"); tag != "" {
		ids, e := j.Store.ControlReader().JobsWithTag(r.Context(), sqlitegen.JobsWithTagParams{WorkspaceID: who(r).WorkspaceID, Tag: tag})
		if e != nil {
			writeError(w, 500, "internal", "cannot filter runs", nil)
			return
		}
		b, _ := json.Marshal(ids)
		tagJobs = string(b)
	}
	from, to := int64(math.MinInt64), int64(math.MaxInt64)
	for _, key := range []string{"from", "to"} {
		if s := q.Get(key); s != "" {
			t, e := time.Parse(time.RFC3339Nano, s)
			if e != nil {
				writeError(w, 400, "validation_failed", "invalid time bound", nil)
				return
			}
			if key == "from" {
				from = t.UnixMilli()
			} else {
				to = t.UnixMilli()
			}
		}
	}
	ct := int64(0)
	ci := ""
	if c := q.Get("cursor"); c != "" {
		b, e := base64.RawURLEncoding.DecodeString(c)
		if e != nil {
			writeError(w, 400, "validation_failed", "invalid cursor", nil)
			return
		}
		parts := strings.SplitN(string(b), "|", 2)
		if len(parts) != 2 {
			writeError(w, 400, "validation_failed", "invalid cursor", nil)
			return
		}
		ct, e = strconv.ParseInt(parts[0], 10, 64)
		if e != nil {
			writeError(w, 400, "validation_failed", "invalid cursor", nil)
			return
		}
		ci = parts[1]
	}
	sla := int64(0)
	if q.Get("sla_breached") == "true" {
		sla = 1
	}
	rows, e := j.Store.HistoryReader().ListRuns(r.Context(), historygen.ListRunsParams{WorkspaceID: who(r).WorkspaceID, JobID: jobID, Status: q.Get("status"), TriggerFilter: q.Get("trigger"), TagFilter: q.Get("tag"), TagJobs: tagJobs, SlaOnly: sla, FromTime: from, ToTime: to, CursorTime: ct, CursorID: ci, PageSize: int64(n + 1)})
	if e != nil {
		writeError(w, 500, "internal", "cannot list runs", nil)
		return
	}
	var cursor *string
	if len(rows) > n {
		last := rows[n-1]
		s := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%s", last.ScheduledAt, last.ID)))
		cursor = &s
		rows = rows[:n]
	}
	data := []any{}
	for _, row := range rows {
		data = append(data, runObject(row))
	}
	writeJSON(w, 200, map[string]any{"data": data, "next_cursor": cursor})
}

func createParams(j sqlitegen.Job) sqlitegen.CreateJobParams {
	return sqlitegen.CreateJobParams(j)
}

func updateParams(j sqlitegen.Job) sqlitegen.UpdateJobParams {
	return sqlitegen.UpdateJobParams{ID: j.ID, WorkspaceID: j.WorkspaceID, Name: j.Name, Description: j.Description, Tags: j.Tags, ScheduleKind: j.ScheduleKind, ScheduleExpr: j.ScheduleExpr, ScheduleCanonical: j.ScheduleCanonical, Timezone: j.Timezone, StartsAt: j.StartsAt, EndsAt: j.EndsAt, MaxOccurrences: j.MaxOccurrences, OccurrenceCount: j.OccurrenceCount, MisfirePolicy: j.MisfirePolicy, SinkKind: j.SinkKind, SinkConfig: j.SinkConfig, RetryPolicy: j.RetryPolicy, TimeoutMs: j.TimeoutMs, SlaThresholdMs: j.SlaThresholdMs, State: j.State, DisabledReason: j.DisabledReason, ConsecutiveFailures: j.ConsecutiveFailures, AutoDisableThreshold: j.AutoDisableThreshold, NextRunAt: j.NextRunAt, LastRunAt: j.LastRunAt, LastRunStatus: j.LastRunStatus, Version: j.Version, UpdatedAt: j.UpdatedAt, DeletedAt: j.DeletedAt, OverlapPolicy: j.OverlapPolicy, SigningSecrets: j.SigningSecrets, ScheduleAnchorAt: j.ScheduleAnchorAt, FailuresResetVersion: j.FailuresResetVersion}
}

// retryRun creates one idempotent retry_manual occurrence per original logical time.
// DECISION: the occurrence uniqueness constraint coalesces repeated manual retries;
// attempts within that retry retain the new run's idempotency key.
func (j *Jobs) retryRun(w http.ResponseWriter, r *http.Request) {
	origin, err := j.Store.HistoryReader().GetRun(r.Context(), historygen.GetRunParams{ID: chi.URLParam(r, "id"), WorkspaceID: who(r).WorkspaceID})
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "not_found", "run not found", nil)
		return
	}
	if err != nil {
		writeError(w, 500, "internal", "cannot get run", nil)
		return
	}
	if origin.Status != "failed" {
		writeError(w, 409, "conflict", "only failed runs can be retried", nil)
		return
	}
	job, err := j.Store.ControlReader().GetJob(r.Context(), sqlitegen.GetJobParams{ID: origin.JobID, WorkspaceID: who(r).WorkspaceID})
	if err != nil {
		writeError(w, 404, "not_found", "job not found", nil)
		return
	}
	if j.Limits != nil && limitError(w, j.Limits.CheckRun(r.Context(), job.WorkspaceID)) {
		return
	}
	runID, err := id.New(id.Run, j.Clock.Now())
	if err != nil {
		writeError(w, 500, "internal", "cannot generate run ID", nil)
		return
	}
	run, err := j.Store.History().UpsertRun(r.Context(), historygen.UpsertRunParams{JobVersion: job.Version, ID: runID, JobID: job.ID, WorkspaceID: job.WorkspaceID, ScheduledAt: origin.ScheduledAt, Trigger: "retry_manual", Status: "deferred", NextAttemptAt: sql.NullInt64{Int64: j.Clock.Now().UnixMilli(), Valid: true}})
	if err != nil {
		writeError(w, 500, "internal", "cannot create manual retry", nil)
		return
	}
	if run.Status == "failed" {
		writeError(w, 409, "conflict", "manual retry already exhausted", nil)
		return
	}
	if run.Status == "deferred" {
		_ = j.Queue.Add(r.Context(), queue.Occurrence{JobID: job.ID, ScheduledAt: time.UnixMilli(origin.ScheduledAt), Version: job.Version, Trigger: "r"}.Format(), j.Clock.Now(), false)
		j.Scheduler.Wake()
	}
	writeJSON(w, 202, runObject(run))
}

func (j *Jobs) encryptResponse(body, ws, key string) string {
	if j.Vault == nil {
		return body
	}
	out, e := j.Vault.Wrap(body, "idempotency:"+ws+":"+key)
	if e != nil {
		return ""
	}
	return out
}
func (j *Jobs) updateJob(ctx context.Context, job sqlitegen.Job) (sqlitegen.Job, error) {
	if j.Vault != nil && !strings.Contains(job.SigningSecrets.String, `"encrypted"`) {
		enc, e := j.Vault.Wrap(job.SigningSecrets.String, "signing:"+job.ID)
		if e != nil {
			return job, e
		}
		job.SigningSecrets.String = enc
	}
	return j.Store.Control().UpdateJob(ctx, updateParams(job))
}
func (j *Jobs) resolver(ws string) sink.SecretResolver {
	if j.Vault == nil {
		return nil
	}
	return j.Vault.Resolver(ws)
}
