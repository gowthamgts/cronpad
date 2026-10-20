-- DECISION: the opener sets auto_vacuum=INCREMENTAL before goose allocates pages.
-- +goose Up
-- ============================ control DB (cronpad.db) ============================
-- users / auth
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  name TEXT,
  avatar_url TEXT,
  created_at INTEGER NOT NULL,
  last_login_at INTEGER,
  deactivated_at INTEGER
);
CREATE TABLE identities (
  provider TEXT NOT NULL,            -- google | github | oidc:<id>
  subject TEXT NOT NULL,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (provider, subject)
);
CREATE TABLE sessions (token TEXT PRIMARY KEY, data BLOB NOT NULL, expiry INTEGER NOT NULL);
CREATE INDEX sessions_expiry ON sessions(expiry);

CREATE TABLE workspaces (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  plan TEXT NOT NULL DEFAULT 'free',
  created_at INTEGER NOT NULL,
  suspended_at INTEGER,
  suspended_reason TEXT
);
CREATE TABLE workspace_members (
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('owner','member')),
  PRIMARY KEY (workspace_id, user_id)
);
CREATE TABLE api_keys (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,              -- first 8 chars shown in UI, e.g. cp_ab12cd34
  hash BLOB NOT NULL,                -- sha256 of full key
  scopes TEXT NOT NULL DEFAULT '["*"]',
  created_by TEXT REFERENCES users(id),
  created_at INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at INTEGER
);
CREATE UNIQUE INDEX api_keys_hash ON api_keys(hash);

-- Encrypted values (API tokens, passwords) that sink configs reference as {{secret.NAME}}
-- instead of embedding them in plain text.
CREATE TABLE secrets (
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name TEXT NOT NULL,                -- UPPER_SNAKE
  ciphertext BLOB NOT NULL,          -- AES-256-GCM, nonce prefixed
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (workspace_id, name)
);

-- jobs
CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  description TEXT,
  tags TEXT NOT NULL DEFAULT '[]',
  schedule_kind TEXT NOT NULL CHECK (schedule_kind IN ('cron','quartz','natural','rrule','once')),
  schedule_expr TEXT NOT NULL,       -- as entered by user
  schedule_canonical TEXT NOT NULL,  -- normalized form (cron string, RRULE, or RFC3339 for once)
  timezone TEXT NOT NULL DEFAULT 'UTC',
  starts_at INTEGER,
  ends_at INTEGER,
  max_occurrences INTEGER,
  occurrence_count INTEGER NOT NULL DEFAULT 0,
  misfire_policy TEXT NOT NULL DEFAULT 'fire_once',
  sink_kind TEXT NOT NULL CHECK (sink_kind IN ('webhook','command')),
  sink_config TEXT NOT NULL CHECK (json_valid(sink_config)),
  retry_policy TEXT NOT NULL CHECK (json_valid(retry_policy)),
  timeout_ms INTEGER NOT NULL,
  sla_threshold_ms INTEGER,          -- override of global
  state TEXT NOT NULL CHECK (state IN ('active','paused','disabled','completed')),
  disabled_reason TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  auto_disable_threshold INTEGER,    -- null = global default; capped by global in hosted mode
  next_run_at INTEGER,               -- null when not active/exhausted
  last_run_at INTEGER,
  last_run_status TEXT,
  version INTEGER NOT NULL DEFAULT 1,-- bumped on every schedule/sink change; carried in occurrence member
  created_by TEXT REFERENCES users(id),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  deleted_at INTEGER,
  overlap_policy TEXT NOT NULL DEFAULT 'allow' CHECK (overlap_policy IN ('allow','skip_if_running')),
  -- Encrypted JSON envelope; legacy plaintext is encrypted by the application at startup.
  signing_secrets TEXT CHECK (signing_secrets IS NULL OR json_valid(signing_secrets)),
  -- Preserve interval phases and relative one-shots across edits and restarts.
  schedule_anchor_at INTEGER,
  -- Re-enable resets survive crash recovery; older runs cannot re-add failures.
  failures_reset_version INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX jobs_ws ON jobs(workspace_id) WHERE deleted_at IS NULL;
CREATE INDEX jobs_next ON jobs(next_run_at) WHERE state = 'active' AND deleted_at IS NULL;

-- alerts
CREATE TABLE alert_channels (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('email','webhook')),
  name TEXT NOT NULL,
  config TEXT NOT NULL CHECK (json_valid(config)),
  verified_at INTEGER,               -- email channels require verification click
  created_at INTEGER NOT NULL,
  signing_secret TEXT NOT NULL DEFAULT ''
);
CREATE TABLE alert_rules (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  job_selector TEXT NOT NULL DEFAULT '{"all":true}',   -- {"all":true} | {"job_ids":[…]} | {"tags":[…]}
  events TEXT NOT NULL,              -- JSON array of alert event types
  conditions TEXT NOT NULL DEFAULT '{}',              -- e.g. {"min_consecutive":3}
  channel_ids TEXT NOT NULL,
  cooldown_ms INTEGER NOT NULL DEFAULT 900000,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);

CREATE TABLE idempotency_records (
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 key TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 response TEXT NOT NULL,
 status INTEGER NOT NULL,
 expires_at INTEGER NOT NULL,
 PRIMARY KEY(workspace_id,key)
);

CREATE TABLE alert_verifications (
 channel_id TEXT NOT NULL REFERENCES alert_channels(id) ON DELETE CASCADE,
 address TEXT NOT NULL,
 verified_at INTEGER,
 PRIMARY KEY(channel_id,address)
);
CREATE INDEX alert_channels_ws ON alert_channels(workspace_id);
CREATE INDEX alert_rules_ws ON alert_rules(workspace_id);

-- +goose Down
DROP TABLE alert_verifications;
DROP TABLE idempotency_records;
DROP TABLE alert_rules;
DROP TABLE alert_channels;
DROP TABLE jobs;
DROP TABLE secrets;
DROP TABLE api_keys;
DROP TABLE workspace_members;
DROP TABLE workspaces;
DROP TABLE sessions;
DROP TABLE identities;
DROP TABLE users;
