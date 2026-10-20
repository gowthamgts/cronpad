// Package api implements the public HTTP API and its common error envelope.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"
	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/schedule"
)

// ErrorBody is the common API error payload. Errors adds structured parser diagnostics.
type ErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
	Errors  []*schedule.Error `json:"errors,omitempty"`
}
type envelope struct {
	Error ErrorBody `json:"error"`
}
type request struct {
	Expr     string        `json:"expr"`
	Kind     schedule.Kind `json:"kind"`
	Timezone string        `json:"timezone"`
	Count    *int          `json:"count"`
	After    string        `json:"after"`
}

// Validation is shared by offline CLI and HTTP consumers.
type Validation struct {
	Valid       bool              `json:"valid"`
	Kind        schedule.Kind     `json:"kind_detected"`
	Canonical   string            `json:"canonical,omitempty"`
	Description string            `json:"description,omitempty"`
	Errors      []*schedule.Error `json:"errors"`
}

// Preview includes normalized metadata plus strictly increasing UTC occurrences.
type Preview struct {
	Kind        schedule.Kind `json:"kind_detected"`
	Canonical   string        `json:"canonical"`
	Description string        `json:"description"`
	Timezone    string        `json:"timezone"`
	Next        []time.Time   `json:"next"`
}

// Validate checks syntax and the configured minimum gap using 100 intervals.
func Validate(spec schedule.Spec, clk clockwork.Clock, minimum time.Duration) (Validation, schedule.Schedule) {
	return ValidateContext(context.Background(), spec, clk, minimum)
}

// ValidateContext applies a caller-owned cooperative compute deadline.
func ValidateContext(ctx context.Context, spec schedule.Spec, clk clockwork.Clock, minimum time.Duration) (Validation, schedule.Schedule) {
	s, err := schedule.ParseContext(ctx, spec, clk)
	result := Validation{Kind: spec.Kind, Errors: []*schedule.Error{}}
	if err != nil {
		var parse *schedule.Error
		if errors.As(err, &parse) {
			result.Kind = parse.Kind
			result.Errors = append(result.Errors, parse)
		}
		return result, nil
	}
	result.Kind = s.Kind()
	result.Canonical = s.Canonical()
	result.Description = s.Describe()
	if gap := schedule.MinGap(s, clk.Now(), 100); gap > 0 && gap < minimum {
		result.Errors = append(result.Errors, &schedule.Error{Kind: s.Kind(), Message: "schedule interval is below the configured minimum " + minimum.String()})
	}
	if ctx.Err() != nil {
		result.Errors = append(result.Errors, &schedule.Error{Kind: s.Kind(), Message: "schedule computation budget exceeded"})
	}
	result.Valid = len(result.Errors) == 0
	return result, s
}

// Schedules mounts unauthenticated, per-IP rate-limited validation and preview routes.
// DECISION: httprate owns its internal wall-clock windows; schedule reference time is always injected.
func Schedules(r chi.Router, clk clockwork.Clock, minimum time.Duration, requestsPerMinute int) {
	handler := func(preview bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 16384)
			var input request
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, http.StatusBadRequest, "validation_failed", "invalid JSON request", nil)
				return
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				writeError(w, http.StatusBadRequest, "validation_failed", "expected one JSON object", nil)
				return
			}
			after := clk.Now()
			if input.After != "" {
				t, err := time.Parse(time.RFC3339Nano, input.After)
				if err != nil {
					writeError(w, 400, "validation_failed", "after must be RFC3339", nil)
					return
				}
				after = t
			}
			count := 10
			if input.Count != nil {
				count = *input.Count
			}
			if count < 1 || count > 100 {
				writeError(w, 400, "validation_failed", "count must be between 1 and 100", nil)
				return
			}
			zone := input.Timezone
			if zone == "" {
				zone = "UTC"
			}
			// DECISION: preview's explicit after also anchors interval/calendar phases and relative one-shots.
			reference := clk
			if input.After != "" {
				reference = clockwork.NewFakeClockAt(after)
			}
			// DECISION: 100ms of cooperative compute time per public request. Work stays
			// on the handler goroutine; cancellation does not leave runaway workers.
			ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
			defer cancel()
			result, s := ValidateContext(ctx, schedule.Spec{Kind: input.Kind, Expr: input.Expr, Timezone: zone}, reference, minimum)
			if !preview {
				writeJSON(w, 200, result)
				return
			}
			if !result.Valid {
				writeError(w, 400, "validation_failed", "invalid schedule", result.Errors)
				return
			}
			times := schedule.Upcoming(s, after, count)
			if ctx.Err() != nil {
				writeError(w, 400, "validation_failed", "schedule computation budget exceeded", []*schedule.Error{{Kind: s.Kind(), Message: "schedule computation budget exceeded"}})
				return
			}
			writeJSON(w, 200, Preview{Kind: s.Kind(), Canonical: s.Canonical(), Description: s.Describe(), Timezone: zone, Next: times})
		}
	}
	r.Group(func(r chi.Router) {
		if requestsPerMinute > 0 {
			r.Use(httprate.LimitBy(requestsPerMinute, time.Minute, peerIP, httprate.WithResponseHeaders(httprate.ResponseHeaders{Limit: "RateLimit-Limit", Remaining: "RateLimit-Remaining", Reset: "RateLimit-Reset", RetryAfter: "Retry-After"}), httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, 429, "rate_limited", "too many schedule requests", nil)
			})))
		}
		r.Post("/api/v1/schedules/validate", handler(false))
		r.Post("/api/v1/schedules/preview", handler(true))
	})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string, details []*schedule.Error) {
	var fields map[string]string
	if len(details) > 0 {
		fields = map[string]string{"schedule": details[0].Message}
	}
	writeJSON(w, status, envelope{Error: ErrorBody{Code: code, Message: message, Fields: fields, Errors: details}})
}

// DECISION: trust only the TCP peer; proxy headers need configured trusted-proxy middleware.
func peerIP(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return httprate.CanonicalizeIP(host), nil
}
