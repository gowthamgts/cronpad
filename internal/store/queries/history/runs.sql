-- name: UpsertRun :one
INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,status,claimed_at,next_attempt_at,job_version,quota_day) VALUES(?,?,?,?,?,?,?,sqlc.narg(next_attempt_at),coalesce(nullif(sqlc.arg(job_version),0),1),sqlc.narg(quota_day)) ON CONFLICT(job_id,scheduled_at,trigger) DO UPDATE SET id=runs.id WHERE runs.workspace_id=excluded.workspace_id RETURNING *;
-- name: GetRun :one
SELECT * FROM runs WHERE id=? AND workspace_id=?;
-- name: ListRuns :many
SELECT * FROM runs WHERE workspace_id=sqlc.arg(workspace_id)
AND (sqlc.arg(job_id)='' OR job_id=sqlc.arg(job_id))
AND (sqlc.arg(tag_filter)='' OR instr(sqlc.arg(tag_jobs),'"'||job_id||'"')>0)
AND (sqlc.arg(status)='' OR status=sqlc.arg(status))
AND (sqlc.arg(trigger_filter)='' OR trigger=sqlc.arg(trigger_filter))
AND (sqlc.arg(sla_only)=0 OR sla_breached=1)
AND scheduled_at>=sqlc.arg(from_time) AND scheduled_at<=sqlc.arg(to_time)
AND (sqlc.arg(cursor_time)=0 OR scheduled_at<sqlc.arg(cursor_time) OR (scheduled_at=sqlc.arg(cursor_time) AND id<sqlc.arg(cursor_id)))
ORDER BY scheduled_at DESC,id DESC LIMIT sqlc.arg(page_size);
-- name: InsertAttempt :exec
INSERT INTO attempts(run_id,n,started_at,finished_at,duration_ms,outcome,status_code,error,request_summary,response_body)
SELECT sqlc.arg(run_id),sqlc.arg(n),sqlc.arg(started_at),sqlc.narg(finished_at),sqlc.narg(duration_ms),sqlc.arg(outcome),sqlc.narg(status_code),sqlc.narg(error),sqlc.narg(request_summary),sqlc.narg(response_body)
WHERE EXISTS(SELECT 1 FROM runs WHERE id=sqlc.arg(run_id) AND workspace_id=sqlc.arg(workspace_id)) ON CONFLICT(run_id,n) DO NOTHING;
-- name: GetAttempts :many
SELECT a.* FROM attempts a JOIN runs r ON r.id=a.run_id WHERE r.id=? AND r.workspace_id=? ORDER BY a.n;
-- name: DeferredRuns :many
SELECT * FROM runs WHERE status='deferred' AND next_attempt_at<sqlc.arg(before) ORDER BY next_attempt_at LIMIT 1000;
-- name: FinishRun :execrows
UPDATE runs SET status=sqlc.arg(status),attempts=sqlc.arg(attempts),finished_at=sqlc.narg(finished_at),next_attempt_at=sqlc.narg(next_attempt_at),last_error=sqlc.narg(last_error) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id);
-- name: StatsRuns :many
SELECT status,sla_breached,start_lag_ms FROM runs WHERE job_id=? AND workspace_id=? AND scheduled_at>=?;
-- name: StartRun :one
UPDATE runs SET status='running',quota_day=coalesce(quota_day,sqlc.narg(quota_day)),claimed_at=coalesce(claimed_at,sqlc.arg(claimed_at)),started_at=coalesce(started_at,sqlc.arg(started_at)),start_lag_ms=coalesce(start_lag_ms,sqlc.arg(start_lag_ms)),sla_breached=CASE WHEN concurrency_deferred=1 THEN 0 ELSE max(sla_breached,sqlc.arg(sla_breached)) END,next_attempt_at=NULL WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) RETURNING *;
-- name: CompleteRun :one
UPDATE runs SET status=sqlc.arg(status),attempts=max(attempts,sqlc.arg(attempts)),finished_at=sqlc.narg(finished_at),next_attempt_at=sqlc.narg(next_attempt_at),e2e_ms=sqlc.narg(e2e_ms),last_error=sqlc.narg(last_error),skip_reason=sqlc.narg(skip_reason) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) RETURNING *;
-- name: OtherOpenRun :one
SELECT EXISTS(SELECT 1 FROM runs WHERE job_id=sqlc.arg(job_id) AND workspace_id=sqlc.arg(workspace_id) AND id<>sqlc.arg(id) AND status IN ('running','deferred') AND (started_at IS NOT NULL OR scheduled_at<sqlc.arg(scheduled_at)));
-- name: FindOccurrence :one
SELECT * FROM runs WHERE job_id=? AND workspace_id=? AND scheduled_at=? AND trigger=?;
-- name: AllDeferredRuns :many
SELECT * FROM runs WHERE status='deferred' AND id>sqlc.arg(cursor) ORDER BY id LIMIT 1000;
-- name: RecentJobs :many
SELECT DISTINCT job_id,workspace_id FROM runs WHERE finished_at>=?;
-- name: JobFinalRuns :many
SELECT * FROM runs WHERE job_id=? AND workspace_id=? AND status IN ('succeeded','failed') ORDER BY finished_at DESC,id DESC;
-- name: InsertAudit :exec
INSERT INTO audit_log(id,workspace_id,actor,action,target,details,created_at) VALUES(?,?,'system',?,?,?,?);
-- name: LastJobFinalRun :one
SELECT * FROM runs WHERE job_id=sqlc.arg(job_id) AND workspace_id=sqlc.arg(workspace_id) AND job_version>=sqlc.arg(reset_version) AND status IN ('succeeded','failed') ORDER BY finished_at DESC,id DESC LIMIT 1;
-- name: RecoveredFailures :one
SELECT count(*) FROM runs r WHERE r.job_id=sqlc.arg(job_id) AND r.workspace_id=sqlc.arg(workspace_id) AND r.job_version>=sqlc.arg(reset_version) AND r.status='failed' AND r.finished_at>coalesce((SELECT max(s.finished_at) FROM runs s WHERE s.job_id=r.job_id AND s.workspace_id=r.workspace_id AND s.job_version>=sqlc.arg(reset_version) AND s.status='succeeded'),-1);
-- name: BeginRun :one
INSERT INTO runs(id,job_id,workspace_id,scheduled_at,trigger,job_version,status,claimed_at,started_at,start_lag_ms,sla_breached,attempts,quota_day)
VALUES(sqlc.arg(id),sqlc.arg(job_id),sqlc.arg(workspace_id),sqlc.arg(scheduled_at),sqlc.arg(trigger),sqlc.arg(job_version),'running',sqlc.narg(claimed_at),sqlc.narg(started_at),sqlc.narg(start_lag_ms),sqlc.arg(sla_breached),sqlc.arg(attempt),sqlc.narg(quota_day))
ON CONFLICT(job_id,scheduled_at,trigger) DO UPDATE SET status=CASE WHEN runs.status IN ('succeeded','failed','skipped','cancelled') OR runs.attempts>excluded.attempts THEN runs.status ELSE 'running' END,quota_day=coalesce(runs.quota_day,excluded.quota_day),claimed_at=coalesce(runs.claimed_at,excluded.claimed_at),started_at=coalesce(runs.started_at,excluded.started_at),start_lag_ms=coalesce(runs.start_lag_ms,excluded.start_lag_ms),sla_breached=CASE WHEN runs.concurrency_deferred=1 THEN 0 ELSE max(runs.sla_breached,excluded.sla_breached) END,next_attempt_at=CASE WHEN runs.attempts>excluded.attempts THEN runs.next_attempt_at ELSE NULL END WHERE runs.workspace_id=excluded.workspace_id RETURNING *;
-- name: CorrectFirstStart :exec
UPDATE runs SET started_at=sqlc.arg(started_at),start_lag_ms=sqlc.arg(start_lag_ms),sla_breached=sqlc.arg(sla_breached) WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND attempts=0;
-- name: RecoveryAnchor :one
SELECT CAST(coalesce(max(finished_at),0) AS INTEGER) FROM runs WHERE finished_at IS NOT NULL;
-- name: OccurrenceWorkspace :one
SELECT workspace_id FROM runs WHERE job_id=? AND scheduled_at=? AND trigger=?;
-- name: AllPendingRuns :many
SELECT * FROM runs WHERE status IN ('running','deferred') AND id>sqlc.arg(cursor) ORDER BY id LIMIT 1000;
-- name: OverduePendingRuns :many
SELECT * FROM runs WHERE status IN ('running','deferred') AND coalesce(next_attempt_at,claimed_at,started_at,scheduled_at)<sqlc.arg(before) ORDER BY coalesce(next_attempt_at,claimed_at,started_at,scheduled_at) LIMIT 1000;
-- name: WriteAudit :exec
INSERT INTO audit_log(id,workspace_id,actor,action,target,details,created_at) VALUES(?,?,?,?,?,?,?);
-- name: ListAudit :many
SELECT * FROM audit_log WHERE workspace_id=sqlc.arg(workspace_id) AND (sqlc.arg(action)='' OR action=sqlc.arg(action)) AND id<sqlc.arg(cursor) ORDER BY id DESC LIMIT sqlc.arg(page_size);
-- name: WorkspaceRunStats :many
SELECT status,sla_breached,start_lag_ms FROM runs WHERE workspace_id=? AND scheduled_at>=?;
-- name: CountDailyRuns :one
SELECT CAST(coalesce((SELECT runs FROM workspace_daily_usage WHERE workspace_id=sqlc.arg(workspace_id) AND day=CAST(sqlc.arg(scheduled_at) AS INTEGER)/86400000),0) AS INTEGER) AS n;
-- name: RetainedTotals :one
WITH counts AS (
 SELECT count(*) AS runs,coalesce(sum(status='succeeded'),0) AS succeeded,coalesce(sum(status='failed'),0) AS failed,coalesce(sum(sla_breached),0) AS sla_breached
 FROM runs r WHERE r.workspace_id=sqlc.arg(workspace_id) AND scheduled_at>=sqlc.arg(raw_from)
 AND (sqlc.arg(job_id)='' OR r.job_id=sqlc.arg(job_id))
 UNION ALL
 SELECT runs,succeeded,failed,sla_breached FROM job_stats_daily d
 WHERE d.workspace_id=sqlc.arg(workspace_id) AND day>=sqlc.arg(from_day) AND day<sqlc.arg(raw_day)
 AND (sqlc.arg(job_id)='' OR d.job_id=sqlc.arg(job_id))
) SELECT CAST(coalesce(sum(runs),0) AS INTEGER) AS runs,CAST(coalesce(sum(succeeded),0) AS INTEGER) AS succeeded,CAST(coalesce(sum(failed),0) AS INTEGER) AS failed,CAST(coalesce(sum(sla_breached),0) AS INTEGER) AS sla_breached FROM counts;
-- name: RetainedLags :many
SELECT lag_ms,CAST(sum(n) AS INTEGER) AS n FROM (
 SELECT start_lag_ms AS lag_ms,1 AS n FROM runs r
 WHERE r.workspace_id=sqlc.arg(workspace_id) AND scheduled_at>=sqlc.arg(raw_from) AND start_lag_ms IS NOT NULL
 AND (sqlc.arg(job_id)='' OR r.job_id=sqlc.arg(job_id))
 UNION ALL
 SELECT l.lag_ms,l.n FROM job_stats_lags l JOIN job_stats_daily d ON d.job_id=l.job_id AND d.day=l.day
 WHERE d.workspace_id=sqlc.arg(workspace_id) AND d.day>=sqlc.arg(from_day) AND d.day<sqlc.arg(raw_day)
 AND (sqlc.arg(job_id)='' OR d.job_id=sqlc.arg(job_id))
) GROUP BY lag_ms ORDER BY lag_ms;

-- name: MonthlyUsage :many
SELECT workspace_id,runs FROM workspace_usage_monthly WHERE month=?;
-- name: CountMonthlyRuns :one
SELECT CAST(coalesce((SELECT runs FROM workspace_usage_monthly WHERE workspace_id=? AND month=?),0) AS INTEGER);
-- name: SetSkipReason :exec
UPDATE runs SET skip_reason=?,last_error=NULL WHERE id=? AND workspace_id=? AND status='skipped';

-- name: MarkConcurrencyDeferred :exec
UPDATE runs SET concurrency_deferred=1,sla_breached=0 WHERE id=? AND workspace_id=? AND started_at IS NULL;
