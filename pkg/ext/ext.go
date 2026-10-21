// Package ext defines the small public contract for optional cronpad extensions.
// Extensions own their tables and must never write core tables through these handles.
package ext

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonboulle/clockwork"
)

// DB selects the migration target file.
type DB string

const (
	Control DB = "control"
	History DB = "history"
)

// Extension is installed before the HTTP server starts. Start must return on cancellation.
// Names and config keys must be unique, lower snake_case identifiers.
type Extension interface {
	Name() string
	ConfigSection() (key string, target any)
	Migrations() (db DB, fsys fs.FS)
	Routes(r chi.Router, d Deps)
	Start(ctx context.Context, d Deps) error
}

// ConfigView exposes non-secret core configuration without leaking internal packages.
// DECISION: a minimal typed view is enough for now; additions remain additive.
type ConfigView interface {
	Mode() string
	PublicURL() string
}

// StatsReader returns instance-wide counts: users, workspaces, workspaces.suspended,
// jobs and jobs.<state> (active, paused, disabled, completed). Keys are additive.
type StatsReader interface {
	ReadStats(ctx context.Context) (map[string]int64, error)
}
type LimitsRegistry interface {
	Register(name string, provider any) error
}
type SessionReader interface{ SignedIn(r *http.Request) bool }
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Deps are server-owned dependencies and additive public semver API.
// Core database handles are restricted to extension-owned tables.
type Deps struct {
	Config  ConfigView
	Clock   clockwork.Clock
	Log     *slog.Logger
	Control *sql.DB
	History *sql.DB
	Stats   StatsReader
	Limits  LimitsRegistry
	Session SessionReader
	Mailer  Mailer
	// Auth registers global/workspace login providers and session policies.
	Auth AuthRegistry
	// Authz replaces the default authorizer; the last registration wins.
	Authz AuthzRegistry
	// Entitlements exposes optional feature availability; OSS always returns false.
	Entitlements Entitlements
	// Directory provisions audited users and memberships through core services.
	Directory Directory
	// Audit subscribes to committed audit events asynchronously.
	Audit AuditRegistry
	// Events subscribes to run and job lifecycle events asynchronously.
	Events EventRegistry
	// Workspaces changes audited plans and suspension state with cache invalidation.
	Workspaces WorkspaceAdmin
}

// OverQuotaPolicy determines whether monthly limits block new occurrences.
type OverQuotaPolicy string

const (
	Block OverQuotaPolicy = "block"
	Allow OverQuotaPolicy = "allow"
)

// Retention overrides global windows; zero inherits the global configuration.
type Retention struct{ Runs, AttemptBodies, AuditLog time.Duration }

// LimitSet is the public workspace plan contract. Zero capacity means unlimited.
type LimitSet struct {
	MaxJobs, MaxRunsPerMonth, MaxConcurrentRuns, MaxAPIKeys, MaxRetryAttempts, MaxAlertEmailsPerDay int
	MinInterval                                                                                     time.Duration
	OverQuota                                                                                       OverQuotaPolicy
	Retention                                                                                       Retention
}

// LimitsProvider may be passed to Deps.Limits.Register by an extension.
type LimitsProvider interface {
	For(context.Context, string) (LimitSet, error)
}
