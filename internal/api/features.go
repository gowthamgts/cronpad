package api

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"

	"github.com/gowthamgts/cronpad/internal/config"
)

type featuresAuth struct {
	Providers       []string `json:"providers"`
	DevLogin        bool     `json:"dev_login"`
	OIDCDisplayName string   `json:"oidc_display_name,omitempty"`
	// Signup is auth.allow_signup; Setup is true until the first user exists.
	Signup bool `json:"signup"`
	Setup  bool `json:"setup"`
}
type featuresSinks struct {
	Webhook bool `json:"webhook"`
	Command bool `json:"command"`
}

// featuresDefaults is what a new job's code fields start with, so the dashboard can show them filled in.
type featuresDefaults struct {
	SuccessCodes     []string `json:"success_codes"`
	RetryOn          []string `json:"retry_on"`
	SuccessExitCodes []int    `json:"success_exit_codes"`
	RetryExitCodes   []int    `json:"retry_exit_codes"`
}

// Features describes what this server offers so the dashboard can adapt.
// It carries no client IDs, keys or other secrets.
type Features struct {
	PublicURL string           `json:"public_url"`
	Auth      featuresAuth     `json:"auth"`
	Sinks     featuresSinks    `json:"sinks"`
	Defaults  featuresDefaults `json:"defaults"`
}

// MountFeatures serves the unauthenticated, read-only GET /api/v1/features.
// providers lists the configured OAuth provider names; hasUsers reports whether anyone has signed up.
func MountFeatures(r chi.Router, c config.Config, publicURL string, providers []string, hasUsers func(context.Context) (bool, error), requestsPerMinute int) {
	names := slices.Sorted(slices.Values(providers))
	if names == nil {
		names = []string{}
	}
	body := Features{
		PublicURL: publicURL,
		Auth:      featuresAuth{Providers: names, DevLogin: c.Auth.DevLogin, OIDCDisplayName: c.Auth.OIDC.DisplayName, Signup: c.Auth.AllowSignup},
		Sinks:     featuresSinks{Webhook: c.Sinks.Webhook.Enabled, Command: c.Sinks.Command.Enabled && c.Mode != "hosted"},
	}
	body.Defaults = featuresDefaults{
		SuccessCodes:     append([]string{}, c.Sinks.Webhook.SuccessCodes...),
		RetryOn:          append([]string{}, c.Sinks.Webhook.RetryOn...),
		SuccessExitCodes: append([]int{}, c.Sinks.Command.SuccessExitCodes...),
		RetryExitCodes:   append([]int{}, c.Sinks.Command.RetryExitCodes...),
	}
	r.Group(func(r chi.Router) {
		if requestsPerMinute > 0 {
			r.Use(httprate.LimitBy(requestsPerMinute, time.Minute, peerIP, httprate.WithResponseHeaders(httprate.ResponseHeaders{Limit: "RateLimit-Limit", Remaining: "RateLimit-Remaining", Reset: "RateLimit-Reset", RetryAfter: "Retry-After"}), httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, 429, "rate_limited", "too many requests", nil)
			})))
		}
		// Users are never all removed, so once one exists the answer is cached for good.
		var populated atomic.Bool
		r.Get("/api/v1/features", func(w http.ResponseWriter, r *http.Request) {
			out := body
			if !populated.Load() {
				// Sign-in options matter more than onboarding: if the check fails, answer without it.
				if exists, err := hasUsers(r.Context()); err == nil {
					populated.Store(exists)
					out.Auth.Setup = !exists
				}
			}
			if out.Auth.Setup {
				// The first sign-in ends setup; don't let a cache keep showing it.
				w.Header().Set("Cache-Control", "no-store")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=60")
			}
			writeJSON(w, 200, out)
		})
	})
}
