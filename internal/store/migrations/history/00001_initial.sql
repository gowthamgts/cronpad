-- DECISION: the opener sets auto_vacuum=INCREMENTAL before goose allocates pages.
-- +goose Up
-- runs: one row per occurrence (one fire of one job), whatever happened to it
CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,               -- no FK: jobs live in the control DB
  workspace_id TEXT NOT NULL,
  scheduled_at INTEGER NOT NULL,
  trigger TEXT NOT NULL CHECK (trigger IN ('schedule','manual','retry_manual')),
  status TEXT NOT NULL CHECK (status IN ('running','deferred','succeeded','failed','skipped','cancelled')),
  attempts INTEGER NOT NULL DEFAULT 0,
  claimed_at INTEGER,
  started_at INTEGER,
  finished_at INTEGER,
  next_attempt_at INTEGER,
  start_lag_ms INTEGER,              -- started_at - scheduled_at (first attempt)
  e2e_ms INTEGER,                    -- finished_at - scheduled_at
  sla_breached INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  -- Retry repair retains the original job version after queue loss.
  job_version INTEGER NOT NULL DEFAULT 1,
  stats_rolled INTEGER NOT NULL DEFAULT 0,
  quota_day INTEGER,
  skip_reason TEXT,
  -- Crash recovery preserves SLA exclusions for cap-induced lateness.
  concurrency_deferred INTEGER NOT NULL DEFAULT 0,
  UNIQUE (job_id, scheduled_at, trigger)   -- idempotency for scheduled runs
);
CREATE INDEX runs_job_time ON runs(job_id, scheduled_at DESC);
CREATE INDEX runs_ws_time ON runs(workspace_id, scheduled_at DESC);
CREATE INDEX runs_status ON runs(status) WHERE status IN ('running','deferred');
CREATE INDEX runs_age ON runs(scheduled_at);                       -- retention

-- attempts: one row per delivery try of a run (1 + retries); carries the heavy payloads
CREATE TABLE attempts (
  run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  n INTEGER NOT NULL,
  started_at INTEGER NOT NULL,
  finished_at INTEGER,
  duration_ms INTEGER,
  outcome TEXT NOT NULL CHECK (outcome IN ('success','retryable','fatal','timeout')),
  status_code INTEGER,               -- HTTP code or exit code
  error TEXT,
  request_summary TEXT,              -- e.g. "POST https://… (412 bytes)"
  response_body TEXT,                -- truncated; NULL for successes unless retention.store_success_bodies
  PRIMARY KEY (run_id, n)
);
CREATE INDEX attempts_body_age ON attempts(started_at) WHERE response_body IS NOT NULL;   -- body retention

CREATE TABLE alert_outbox (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  rule_id TEXT NOT NULL,
  channel_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,          -- rule_id + job_id + event_type
  payload TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending','sent','failed','suppressed')),
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  sent_at INTEGER,
  -- Bounded delivery leases make concurrent standby processes safe.
  delivery_lease_until INTEGER NOT NULL DEFAULT 0,
  delivery_token TEXT NOT NULL DEFAULT ''
);
CREATE INDEX alert_outbox_pending ON alert_outbox(next_attempt_at) WHERE status = 'pending';
CREATE INDEX alert_outbox_dedupe ON alert_outbox(dedupe_key, created_at DESC);
CREATE INDEX alert_outbox_age ON alert_outbox(created_at) WHERE status <> 'pending';   -- retention

-- audit log (cheap, high value for debugging "who paused my job")
CREATE TABLE audit_log (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  actor TEXT NOT NULL,               -- user:<id> | key:<id> | system
  action TEXT NOT NULL,              -- job.create, job.pause, job.auto_disable, …
  target TEXT,
  details TEXT,
  created_at INTEGER NOT NULL
);
CREATE INDEX audit_ws_time ON audit_log(workspace_id, created_at DESC);
CREATE INDEX audit_age ON audit_log(created_at);                   -- retention

-- daily rollups for UI charts (avoid scanning runs)
CREATE TABLE job_stats_daily (
  job_id TEXT NOT NULL,
  day INTEGER NOT NULL,              -- unix day
  runs INTEGER NOT NULL DEFAULT 0,
  succeeded INTEGER NOT NULL DEFAULT 0,
  failed INTEGER NOT NULL DEFAULT 0,
  sla_breached INTEGER NOT NULL DEFAULT 0,
  p50_lag_ms INTEGER, p95_lag_ms INTEGER, p99_lag_ms INTEGER,
  workspace_id TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (job_id, day)
);

CREATE INDEX runs_job_finished ON runs(job_id,finished_at DESC);
CREATE INDEX runs_finished ON runs(finished_at) WHERE finished_at IS NOT NULL;

-- DECISION: daily quotas count first claims, so delayed occurrences and delivery
-- retries cannot bypass or consume another day's quota.
CREATE INDEX runs_ws_claimed ON runs(workspace_id,claimed_at) WHERE claimed_at IS NOT NULL;

CREATE INDEX runs_unrolled ON runs(scheduled_at) WHERE stats_rolled=0 AND status NOT IN ('running','deferred');

CREATE INDEX stats_day ON job_stats_daily(day);
CREATE INDEX stats_workspace_day ON job_stats_daily(workspace_id,day);
-- Exact lag_ms histograms allow incremental rollups, including late completions,
-- without retaining raw runs or averaging percentiles.
CREATE TABLE job_stats_lags (
 job_id TEXT NOT NULL, day INTEGER NOT NULL, lag_ms INTEGER NOT NULL, n INTEGER NOT NULL,
 PRIMARY KEY(job_id,day,lag_ms),
 FOREIGN KEY(job_id,day) REFERENCES job_stats_daily(job_id,day) ON DELETE CASCADE
);

-- DECISION: durable O(1) quota accounting must survive restarts, retention,
-- and plan changes from unlimited to limited. Maintain it inside the existing
-- first-claim write, including unlimited workspaces; deleting history never
-- returns an already-consumed quota slot.

CREATE TABLE workspace_daily_usage (
 workspace_id TEXT NOT NULL,
 day INTEGER NOT NULL,
 runs INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(workspace_id,day)
);
CREATE INDEX workspace_daily_usage_day ON workspace_daily_usage(day);
CREATE INDEX runs_workspace_claimed ON runs(workspace_id,claimed_at) WHERE claimed_at IS NOT NULL;

CREATE TABLE workspace_usage_monthly (
 workspace_id TEXT NOT NULL, month TEXT NOT NULL, runs INTEGER NOT NULL DEFAULT 0,
 quota_warned_at INTEGER, quota_exceeded_at INTEGER,
 PRIMARY KEY(workspace_id,month)
);
CREATE INDEX workspace_usage_monthly_age ON workspace_usage_monthly(month);
-- DECISION: transactional triggers retain exact metering across crashes and pruning.
-- Admission is the first started_at, after overlap and quota checks, never a retry.
-- +goose StatementBegin
CREATE TRIGGER runs_monthly_insert AFTER INSERT ON runs WHEN NEW.started_at IS NOT NULL AND NEW.status='running'
BEGIN
 INSERT INTO workspace_usage_monthly(workspace_id,month,runs) VALUES(NEW.workspace_id,strftime('%Y-%m',NEW.started_at/1000,'unixepoch'),1)
 ON CONFLICT(workspace_id,month) DO UPDATE SET runs=runs+1;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER runs_monthly_start AFTER UPDATE OF started_at ON runs WHEN OLD.started_at IS NULL AND NEW.started_at IS NOT NULL AND NEW.status='running'
BEGIN
 INSERT INTO workspace_usage_monthly(workspace_id,month,runs) VALUES(NEW.workspace_id,strftime('%Y-%m',NEW.started_at/1000,'unixepoch'),1)
 ON CONFLICT(workspace_id,month) DO UPDATE SET runs=runs+1;
END;
-- +goose StatementEnd
CREATE TABLE workspace_email_usage (
 workspace_id TEXT NOT NULL, day TEXT NOT NULL, sent INTEGER NOT NULL DEFAULT 0,
 suppressed INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(workspace_id,day)
);

-- +goose Down
DROP TRIGGER runs_monthly_start;
DROP TRIGGER runs_monthly_insert;
DROP TABLE workspace_email_usage;
DROP TABLE workspace_usage_monthly;
DROP TABLE workspace_daily_usage;
DROP TABLE job_stats_lags;
DROP TABLE job_stats_daily;
DROP TABLE audit_log;
DROP TABLE alert_outbox;
DROP TABLE attempts;
DROP TABLE runs;
