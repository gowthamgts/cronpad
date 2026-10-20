// Package limits applies configured quotas and registered plan overrides.
package limits

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/store"
	"github.com/gowthamgts/cronpad/pkg/ext"
)

type Set = ext.LimitSet
type Retention = ext.Retention
type OverQuotaPolicy = ext.OverQuotaPolicy
type Provider = ext.LimitsProvider

const (
	Block = ext.Block
	Allow = ext.Allow
)

type Action struct {
	Limit string
	Value int64
}
type Error struct {
	Limit string `json:"limit"`
	Value int64  `json:"value"`
}

func (e *Error) Error() string { return fmt.Sprintf("limit %s exceeded (%d)", e.Limit, e.Value) }

// LegacyProvider preserves the v1 per-action registration callback.
type LegacyProvider func(context.Context, string, string) (int64, bool, error)
type cached struct {
	set   Set
	until time.Time
}

// ConfigProvider supplies the default limits and retention for every workspace.
type ConfigProvider struct {
	Limits    config.ConfigLimits
	Retention config.ConfigRetention
}

func (p ConfigProvider) For(_ context.Context, _ string) (Set, error) {
	c := p.Limits
	policy := OverQuotaPolicy(c.OverQuota)
	if policy == "" {
		policy = Block
	}
	return Set{MaxJobs: c.MaxJobsPerWorkspace, MaxRunsPerMonth: c.MaxRunsPerMonthPerWorkspace, MaxConcurrentRuns: c.MaxConcurrentRunsPerWorkspace, MaxAPIKeys: c.MaxAPIKeysPerWorkspace, MaxRetryAttempts: c.MaxRetryAttempts, MaxAlertEmailsPerDay: c.MaxAlertEmailsPerDay, MinInterval: c.MinInterval.Duration(), OverQuota: policy, Retention: Retention{Runs: p.Retention.Runs.Duration(), AttemptBodies: p.Retention.AttemptBodies.Duration(), AuditLog: p.Retention.AuditLog.Duration()}}, nil
}

type Registry struct {
	Config       config.ConfigLimits
	Retention    config.ConfigRetention
	Store        store.Store
	Clock        clockwork.Clock
	mu           sync.Mutex
	providers    map[string]LegacyProvider
	plans        map[string]Provider
	order        []string
	cache        map[string]cached
	usageMonth   string
	usage        map[string]int64
	reservations map[string]string
}

func (r *Registry) now() time.Time {
	if r.Clock == nil {
		return clockwork.NewRealClock().Now()
	}
	return r.Clock.Now()
}

// Register accepts a v2 Provider or the existing per-action callback.
func (r *Registry) Register(name string, provider any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = map[string]LegacyProvider{}
		r.plans = map[string]Provider{}
	}
	if _, ok := r.providers[name]; ok {
		return fmt.Errorf("duplicate limits provider %q", name)
	}
	if _, ok := r.plans[name]; ok {
		return fmt.Errorf("duplicate limits provider %q", name)
	}
	switch p := provider.(type) {
	case Provider:
		r.plans[name] = p
	case LegacyProvider:
		r.providers[name] = p
	case func(context.Context, string, string) (int64, bool, error):
		r.providers[name] = p
	default:
		return fmt.Errorf("invalid limits provider")
	}
	r.order = append(r.order, name)
	r.cache = nil
	return nil
}

// Invalidate makes a committed workspace plan change visible immediately.
func (r *Registry) Invalidate(ws string) { r.mu.Lock(); delete(r.cache, ws); r.mu.Unlock() }

// For resolves the effective plan; v2 provider results have a sixty-second TTL.
func (r *Registry) For(ctx context.Context, ws string) (Set, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, _ := (ConfigProvider{r.Config, r.Retention}).For(ctx, ws)
	if c, ok := r.cache[ws]; ok && r.now().Before(c.until) {
		s = c.set
	} else {
		for _, name := range r.order {
			if p := r.plans[name]; p != nil {
				v, e := p.For(ctx, ws)
				if e != nil {
					return Set{}, fmt.Errorf("workspace limits: %w", e)
				}
				s = v
			}
		}
		if s.OverQuota == "" {
			s.OverQuota = Block
		}
		// DECISION: immutable config defaults resolve directly in O(1); cache only
		// external plan lookups, avoiding tenant cache entries on unlimited installs.
		if len(r.plans) > 0 {
			if r.cache == nil {
				r.cache = map[string]cached{}
			}
			r.cache[ws] = cached{s, r.now().Add(time.Minute)}
		}
	}
	// DECISION: legacy callbacks remain uncached, preserving their v1 dynamic behavior.
	for _, name := range r.order {
		if p := r.providers[name]; p != nil {
			monthlyOverride := false
			for key, target := range map[string]*int{"max_jobs_per_workspace": &s.MaxJobs, "max_runs_per_month_per_workspace": &s.MaxRunsPerMonth, "max_concurrent_runs_per_workspace": &s.MaxConcurrentRuns, "max_api_keys_per_workspace": &s.MaxAPIKeys, "max_retry_attempts": &s.MaxRetryAttempts, "max_alert_emails_per_day": &s.MaxAlertEmailsPerDay} {
				n, ok, e := p(ctx, ws, key)
				if e != nil {
					return Set{}, e
				}
				if ok {
					*target = int(n)
					if key == "max_runs_per_month_per_workspace" {
						monthlyOverride = true
					}
				}
			}
			// DECISION: v1 callback daily overrides get the same ×30 migration as config;
			// a callback providing both keys explicitly prefers its monthly value.
			daily, hasDaily, err := p(ctx, ws, "max_runs_per_day_per_workspace")
			if err != nil {
				return Set{}, err
			}
			if hasDaily && !monthlyOverride {
				s.MaxRunsPerMonth = int(daily * 30)
			}
			n, ok, e := p(ctx, ws, "min_interval")
			if e != nil {
				return Set{}, e
			}
			if ok {
				s.MinInterval = time.Duration(n) * time.Millisecond
			}
		}
	}
	return s, nil
}
func (r *Registry) Effective(ctx context.Context, ws, name string) (int64, error) {
	s, e := r.For(ctx, ws)
	if e != nil {
		return 0, e
	}
	switch name {
	case "max_jobs_per_workspace":
		return int64(s.MaxJobs), nil
	case "max_api_keys_per_workspace":
		return int64(s.MaxAPIKeys), nil
	case "max_retry_attempts":
		return int64(s.MaxRetryAttempts), nil
	case "max_runs_per_month_per_workspace":
		return int64(s.MaxRunsPerMonth), nil
	case "max_concurrent_runs_per_workspace":
		return int64(s.MaxConcurrentRuns), nil
	case "max_alert_emails_per_day":
		return int64(s.MaxAlertEmailsPerDay), nil
	case "max_runs_per_day_per_workspace":
		return int64(r.Config.MaxRunsPerDayPerWorkspace), nil
	case "min_interval":
		return s.MinInterval.Milliseconds(), nil
	}
	return 0, fmt.Errorf("unknown limit %q", name)
}
func (r *Registry) Check(ctx context.Context, ws string, a Action) error {
	n, e := r.Effective(ctx, ws, a.Limit)
	if e != nil {
		return e
	}
	if n > 0 && (a.Limit == "min_interval" && a.Value < n || a.Limit != "min_interval" && a.Value > n) {
		return &Error{a.Limit, n}
	}
	return nil
}
func (r *Registry) CheckRun(ctx context.Context, ws string) error {
	s, e := r.For(ctx, ws)
	if e != nil {
		return e
	}
	if s.MaxRunsPerMonth <= 0 || s.OverQuota == Allow {
		return nil
	}
	n, e := r.Usage(ctx, ws)
	if e != nil {
		return e
	}
	if n >= int64(s.MaxRunsPerMonth) {
		return &Error{"max_runs_per_month_per_workspace", int64(s.MaxRunsPerMonth)}
	}
	return nil
}
