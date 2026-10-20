package logx

// Standard log attribute names keep queries consistent across components.
const (
	Deleted     = "deleted"
	Time        = "time"
	Level       = "level"
	MSg         = "msg"
	Service     = "service"
	Version     = "version"
	TraceID     = "trace_id"
	SpanID      = "span_id"
	JobID       = "job_id"
	RunID       = "run_id"
	Occ         = "occ"
	Attempt     = "attempt"
	Trigger     = "trigger"
	ScheduledAt = "scheduled_at"
	WorkspaceID = "workspace_id"
	Sink        = "sink"
	Outcome     = "outcome"
	StatusCode  = "status_code"
	DurationMS  = "duration_ms"
	StartLagMS  = "start_lag_ms"
	SLABreached = "sla_breached"
	Error       = "error"
	RequestID   = "request_id"
	Method      = "method"
	Route       = "route"
	Path        = "path"
	Status      = "status"
	Actor       = "actor"
	Claimed     = "claimed"
	FreeSlots   = "free_slots"
	TickMS      = "tick_ms"
	Section     = "section"
	Edition     = "edition"
	Address     = "address"
)

// DeliveryMode identifies the configured scheduler delivery policy.
const DeliveryMode = "delivery_mode"

// Authorizer names the active extension authorization implementation.
const Authorizer = "authorizer"
