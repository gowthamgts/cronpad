// Package events defines the domain event boundary consumed by alerting.
package events

import (
	"context"
	"time"
)

const (
	WorkspaceQuotaWarning  = "workspace.quota_warning"
	WorkspaceQuotaExceeded = "workspace.quota_exceeded"
	RunSucceeded           = "run.succeeded"
	RunDeferred            = "run.deferred"
	RunFailed              = "run.failed"
	RunSLABreached         = "run.sla_breached"
	RunTimeout             = "run.timeout"
	JobAutoDisabled        = "job.auto_disabled"
	JobRecovered           = "job.recovered"
	JobCompleted           = "job.completed"
	LeaseExpired           = "system.lease_expired"
	Degraded               = "system.degraded"
)

type Event struct {
	Type, WorkspaceID, JobID, RunID string
	At                              time.Time
	Attempt                         int
	Error                           string
}

// Bus receives committed state changes. Consumers own persistence and delivery.
type Bus interface{ Publish(context.Context, Event) }

// Nop is the alerting extension point until consumers are installed.
type Nop struct{}

func (Nop) Publish(context.Context, Event) {}
