-- name: InsertAlertOutbox :exec
INSERT INTO alert_outbox(id,workspace_id,rule_id,channel_id,event_type,dedupe_key,payload,status,next_attempt_at,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING;
-- name: LastAlert :one
SELECT * FROM alert_outbox WHERE dedupe_key=? AND channel_id=? AND status IN ('pending','sent','failed') ORDER BY created_at DESC,id DESC LIMIT 1;
-- name: CountSuppressedAlerts :one
SELECT count(*) FROM alert_outbox AS a WHERE a.dedupe_key=? AND a.channel_id=? AND a.status='suppressed' AND a.rowid > COALESCE((SELECT b.rowid FROM alert_outbox AS b WHERE b.id=sqlc.arg(after_id)),0);
-- name: PendingAlerts :many
SELECT * FROM alert_outbox WHERE status='pending' AND next_attempt_at<=sqlc.arg(now) AND delivery_lease_until<=sqlc.arg(now) ORDER BY next_attempt_at,id LIMIT 100;
-- name: FinishAlert :exec
UPDATE alert_outbox SET status=?,attempts=?,next_attempt_at=?,sent_at=?,delivery_lease_until=0,delivery_token='' WHERE id=? AND status='pending' AND delivery_token=?;
-- name: ClaimAlert :execrows
UPDATE alert_outbox SET delivery_lease_until=?,delivery_token=? WHERE id=? AND status='pending' AND delivery_lease_until<=sqlc.arg(now);
