-- name: CreateJob :one
INSERT INTO jobs (id, workspace_id, name, description, tags, schedule_kind, schedule_expr, schedule_canonical, timezone, starts_at, ends_at, max_occurrences, occurrence_count, misfire_policy, sink_kind, sink_config, retry_policy, timeout_ms, sla_threshold_ms, state, disabled_reason, consecutive_failures, auto_disable_threshold, next_run_at, last_run_at, last_run_status, version, created_by, created_at, updated_at, deleted_at, overlap_policy, signing_secrets, schedule_anchor_at, failures_reset_version) VALUES (sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(name), sqlc.arg(description), sqlc.arg(tags), sqlc.arg(schedule_kind), sqlc.arg(schedule_expr), sqlc.arg(schedule_canonical), sqlc.arg(timezone), sqlc.arg(starts_at), sqlc.arg(ends_at), sqlc.arg(max_occurrences), sqlc.arg(occurrence_count), sqlc.arg(misfire_policy), sqlc.arg(sink_kind), sqlc.arg(sink_config), sqlc.arg(retry_policy), sqlc.arg(timeout_ms), sqlc.arg(sla_threshold_ms), sqlc.arg(state), sqlc.arg(disabled_reason), sqlc.arg(consecutive_failures), sqlc.arg(auto_disable_threshold), sqlc.arg(next_run_at), sqlc.arg(last_run_at), sqlc.arg(last_run_status), sqlc.arg(version), sqlc.arg(created_by), sqlc.arg(created_at), sqlc.arg(updated_at), sqlc.arg(deleted_at), sqlc.arg(overlap_policy), sqlc.arg(signing_secrets),sqlc.narg(schedule_anchor_at),sqlc.arg(failures_reset_version)) RETURNING *;
-- name: GetJob :one
SELECT * FROM jobs WHERE id=? AND workspace_id=? AND deleted_at IS NULL;
-- name: ListJobs :many
SELECT * FROM jobs WHERE workspace_id=sqlc.arg(workspace_id) AND deleted_at IS NULL
AND (sqlc.arg(state)='' OR state=sqlc.arg(state))
AND (sqlc.arg(tag)='' OR EXISTS(SELECT 1 FROM json_each(jobs.tags) WHERE value=sqlc.arg(tag)))
AND (sqlc.arg(search)='' OR instr(lower(name||' '||coalesce(description,'')),lower(sqlc.arg(search)))>0)
AND (sqlc.arg(cursor)='' OR id<sqlc.arg(cursor)) ORDER BY id DESC LIMIT sqlc.arg(page_size);
-- name: CountJobs :one
SELECT count(*) FROM jobs WHERE workspace_id=? AND deleted_at IS NULL;
-- name: UpdateJob :one
UPDATE jobs SET name=sqlc.arg(name), description=sqlc.arg(description), tags=sqlc.arg(tags), schedule_kind=sqlc.arg(schedule_kind), schedule_expr=sqlc.arg(schedule_expr), schedule_canonical=sqlc.arg(schedule_canonical), timezone=sqlc.arg(timezone), starts_at=sqlc.arg(starts_at), ends_at=sqlc.arg(ends_at), max_occurrences=sqlc.arg(max_occurrences), occurrence_count=sqlc.arg(occurrence_count), misfire_policy=sqlc.arg(misfire_policy), sink_kind=sqlc.arg(sink_kind), sink_config=sqlc.arg(sink_config), retry_policy=sqlc.arg(retry_policy), timeout_ms=sqlc.arg(timeout_ms), sla_threshold_ms=sqlc.arg(sla_threshold_ms), state=sqlc.arg(state), disabled_reason=sqlc.arg(disabled_reason), consecutive_failures=sqlc.arg(consecutive_failures), auto_disable_threshold=sqlc.arg(auto_disable_threshold), next_run_at=sqlc.arg(next_run_at), last_run_at=sqlc.arg(last_run_at), last_run_status=sqlc.arg(last_run_status), updated_at=sqlc.arg(updated_at), deleted_at=sqlc.arg(deleted_at), overlap_policy=sqlc.arg(overlap_policy), signing_secrets=sqlc.arg(signing_secrets), schedule_anchor_at=sqlc.narg(schedule_anchor_at), failures_reset_version=sqlc.arg(failures_reset_version), version=version+1 WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND version=sqlc.arg(version) AND deleted_at IS NULL RETURNING *;
-- name: DeleteJob :execrows
UPDATE jobs SET deleted_at=sqlc.arg(now),updated_at=sqlc.arg(now),next_run_at=NULL,version=version+1 WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND deleted_at IS NULL AND version=sqlc.arg(version);
-- name: ChangeJobState :one
UPDATE jobs SET state=sqlc.arg(state),next_run_at=sqlc.narg(next_run_at),consecutive_failures=sqlc.arg(consecutive_failures),disabled_reason=NULL,failures_reset_version=CASE WHEN sqlc.arg(reset_failures)=1 THEN version+1 ELSE failures_reset_version END,version=version+1,updated_at=sqlc.arg(now) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND version=sqlc.arg(version) AND deleted_at IS NULL RETURNING *;
-- name: OverdueJobs :many
SELECT id,workspace_id,next_run_at,version FROM jobs WHERE state='active' AND deleted_at IS NULL AND next_run_at<sqlc.arg(before) ORDER BY next_run_at LIMIT 1000;
-- name: ActiveJobs :many
SELECT * FROM jobs WHERE state='active' AND deleted_at IS NULL AND id>sqlc.arg(cursor) ORDER BY id LIMIT sqlc.arg(page_size);
-- name: AdvanceJob :execrows
UPDATE jobs SET next_run_at=sqlc.narg(next_run_at),occurrence_count=occurrence_count+1,last_run_at=sqlc.arg(scheduled_at),state=sqlc.arg(state) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND version=sqlc.arg(version) AND next_run_at=sqlc.arg(scheduled_at) AND deleted_at IS NULL AND state='active';
-- name: SetNextRun :execrows
UPDATE jobs SET next_run_at=sqlc.narg(next_run_at),state=CASE WHEN sqlc.narg(next_run_at) IS NULL THEN 'completed' ELSE state END WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND version=sqlc.arg(version) AND deleted_at IS NULL;
-- name: SetConsecutiveFailures :execrows
UPDATE jobs SET consecutive_failures=sqlc.arg(failures),last_run_status=sqlc.arg(status) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND version=sqlc.arg(version) AND deleted_at IS NULL;
-- name: SeedDevUser :exec
INSERT INTO users(id,email,name,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING;
-- name: SeedDevWorkspace :exec
INSERT INTO workspaces(id,name,slug,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING;
-- name: SeedDevMember :exec
INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'owner') ON CONFLICT DO NOTHING;
-- name: JobsWithTag :many
SELECT id FROM jobs WHERE workspace_id=sqlc.arg(workspace_id) AND (sqlc.arg(tag)='' OR EXISTS(SELECT 1 FROM json_each(jobs.tags) WHERE value=sqlc.arg(tag)));
-- name: GetIdempotency :one
SELECT * FROM idempotency_records WHERE workspace_id=? AND key=? AND expires_at>?;
-- name: SaveIdempotency :exec
INSERT INTO idempotency_records(workspace_id,key,request_hash,response,status,expires_at) VALUES(?,?,?,?,?,?) ON CONFLICT(workspace_id,key) DO UPDATE SET request_hash=excluded.request_hash,response=excluded.response,status=excluded.status,expires_at=excluded.expires_at;
-- name: DispatchJob :one
SELECT * FROM jobs WHERE id=? AND deleted_at IS NULL;
-- name: ApplyRunResult :one
UPDATE jobs SET consecutive_failures=CASE WHEN sqlc.arg(status)='succeeded' THEN 0 WHEN sqlc.arg(status)='failed' THEN consecutive_failures+1 ELSE consecutive_failures END,last_run_status=sqlc.arg(status),last_run_at=max(coalesce(last_run_at,sqlc.arg(scheduled_at)),sqlc.arg(scheduled_at)),updated_at=sqlc.arg(now) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND failures_reset_version<=sqlc.arg(version) AND deleted_at IS NULL RETURNING *;
-- name: DisableJob :execrows
UPDATE jobs SET state='disabled',disabled_reason=sqlc.arg(reason),next_run_at=NULL,updated_at=sqlc.arg(now) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND version=sqlc.arg(version) AND deleted_at IS NULL AND state IN ('active','paused','completed');
-- name: RecoverJobResult :exec
UPDATE jobs SET consecutive_failures=sqlc.arg(failures),last_run_at=max(coalesce(last_run_at,sqlc.arg(last_run_at)),sqlc.arg(last_run_at)),last_run_status=sqlc.arg(status) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND deleted_at IS NULL;
-- name: JobCounts :many
SELECT state,count(*) AS total FROM jobs WHERE deleted_at IS NULL GROUP BY state;
-- name: RecoverSchedule :execrows
UPDATE jobs SET next_run_at=sqlc.narg(next_run_at),state=sqlc.arg(state),occurrence_count=occurrence_count+sqlc.arg(missed),last_run_at=sqlc.arg(last_run_at) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND version=sqlc.arg(version) AND next_run_at=sqlc.arg(previous) AND state='active' AND deleted_at IS NULL;
-- name: JobFailureCount :one
SELECT consecutive_failures FROM jobs WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND failures_reset_version<=sqlc.arg(version) AND deleted_at IS NULL;
-- name: ApplyRunResultLite :one
UPDATE jobs SET consecutive_failures=CASE WHEN sqlc.arg(status)='succeeded' THEN 0 WHEN sqlc.arg(status)='failed' THEN consecutive_failures+1 ELSE consecutive_failures END,last_run_status=sqlc.arg(status),last_run_at=max(coalesce(last_run_at,sqlc.arg(scheduled_at)),sqlc.arg(scheduled_at)),updated_at=sqlc.arg(now) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND failures_reset_version<=sqlc.arg(version) AND deleted_at IS NULL RETURNING consecutive_failures,next_run_at,state,version,auto_disable_threshold;
-- name: RecoveryClaimJob :one
-- DECISION: startup claim repair alone may read a soft-deleted job to retain
-- the workspace and run identity of a possibly delivered, uncommitted orphan.
SELECT * FROM jobs WHERE id=?;
